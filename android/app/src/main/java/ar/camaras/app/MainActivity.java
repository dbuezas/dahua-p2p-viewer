package ar.camaras.app;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.graphics.Bitmap;
import android.graphics.Canvas;
import android.graphics.Color;
import android.graphics.Paint;
import android.graphics.RectF;
import android.graphics.Typeface;
import android.net.Uri;
import android.opengl.GLSurfaceView;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.text.InputType;
import android.view.Gravity;
import android.view.GestureDetector;
import android.view.MotionEvent;
import android.view.ScaleGestureDetector;
import android.view.View;
import android.view.WindowManager;
import android.widget.Button;
import android.widget.EditText;
import android.widget.FrameLayout;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.media3.common.MediaItem;
import androidx.media3.common.PlaybackException;
import androidx.media3.common.Player;
import androidx.media3.common.VideoSize;
import androidx.media3.exoplayer.DefaultLoadControl;
import androidx.media3.exoplayer.ExoPlayer;
import androidx.media3.exoplayer.rtsp.RtspMediaSource;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.File;
import java.nio.file.Files;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.Set;

import ar.camaras.camaras.Camaras;
import ar.camaras.camaras.Session;

/**
 * The phone app: the same cameras, layout and controls as the TV app, with the
 * controls in the app itself (no web page): tap a camera = big / all; hold = menu
 * (HD, split camera together or apart); drag on a big PTZ camera = joystick; pinch
 * = digital zoom, drag a zoomed camera = pan.
 */
public class MainActivity extends Activity {
    private final Handler ui = new Handler(Looper.getMainLooper());
    private SharedPreferences prefs;
    private GLSurfaceView gl;
    private CamRenderer renderer;
    private Overlay overlay;
    private volatile Session session;
    private final List<ExoPlayer> players = new ArrayList<>();
    // HD on or off: the other video of a channel opening next to the one on screen (ch -> its player, its stream)
    private final Map<Integer, ExoPlayer> opening = new HashMap<>();
    private final Map<Integer, CamRenderer.Stream> openingStream = new HashMap<>();
    private final Set<ExoPlayer> released = new HashSet<>(); // a retry must not start these again
    private List<CamRenderer.Stream> streams = new ArrayList<>();
    private final List<CamRenderer.Tile> tiles = new ArrayList<>();
    private int[] chans = new int[0];
    private String[] names = new String[0];
    private String status = "Conectando con el grabador...";

    @Override protected void onCreate(Bundle b) {
        super.onCreate(b);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        prefs = getSharedPreferences("camaras", MODE_PRIVATE);
        Camaras.setLogFile(new File(getFilesDir(), "camaras.log").getAbsolutePath());
        FrameLayout root = new FrameLayout(this);
        gl = new GLSurfaceView(this);
        gl.setEGLContextClientVersion(2);
        // keep the GL textures (one SurfaceTexture per camera) alive across a pause,
        // so the players' surfaces are still valid when the app returns to the front
        gl.setPreserveEGLContextOnPause(true);
        renderer = new CamRenderer(gl);
        gl.setRenderer(renderer);
        gl.setRenderMode(GLSurfaceView.RENDERMODE_WHEN_DIRTY);
        overlay = new Overlay(this);
        // turning the phone (portrait or landscape) resizes the view: lay the cameras out again
        gl.addOnLayoutChangeListener((v, left, top, right, bottom, oLeft, oTop, oRight, oBottom) -> {
            if (right - left != oRight - oLeft || bottom - top != oBottom - oTop) relayout();
        });
        root.addView(gl);
        root.addView(overlay);
        setContentView(root);
        hideBars();
        // debug builds only: android/poner_clave.sh hands the password over from the Mac's Keychain
        String given = getIntent().getStringExtra("clave");
        if (given != null && !given.isEmpty() && (getApplicationInfo().flags & android.content.pm.ApplicationInfo.FLAG_DEBUGGABLE) != 0) {
            try { prefs.edit().putString("pass", Secret.seal(given)).apply(); } catch (Exception e) { status = "No pude guardar la clave: " + e; }
            getIntent().removeExtra("clave");
        }
        String link = (getIntent().getFlags() & Intent.FLAG_ACTIVITY_LAUNCHED_FROM_HISTORY) != 0 ? null : importLink(getIntent());
        if (link != null && link.equals(prefs.getString("link_visto", ""))) link = null; // already answered
        if (link != null) {
            setIntent(new Intent());
            askImport(link, true);
        } else start();
    }

    private void start() {
        if (prefs.getString("serial", "").isEmpty() || prefs.getString("pass", "").isEmpty()) askSettings("Datos del grabador");
        else connect();
    }

    // the app is already open (singleTask) and the camera opened a configuration QR
    @Override protected void onNewIntent(Intent i) {
        super.onNewIntent(i);
        String link = importLink(i);
        if (link != null && !link.equals(prefs.getString("link_visto", ""))) askImport(link, false);
    }

    private void hideBars() {
        getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_FULLSCREEN | View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
                | View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY | View.SYSTEM_UI_FLAG_LAYOUT_STABLE
                | View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN);
    }

    // ---- settings (the camaras.ini of the TV app) ----
    private void askSettings(String title) { askSettings(title, false); }

    /** canShare: also offer "Compartir" (the configuration QR), from the long press only. */
    private void askSettings(String title, boolean canShare) {
        LinearLayout l = new LinearLayout(this);
        l.setOrientation(LinearLayout.VERTICAL);
        l.setPadding(40, 20, 40, 0);
        EditText sn = field(l, "Número de serie del grabador", prefs.getString("serial", ""), false);
        EditText user = field(l, "Usuario", prefs.getString("user", "admin"), false);
        EditText pass = field(l, "Clave (la misma que en DMSS)", "", true);
        EditText ptz = field(l, "Cámaras con PTZ (ej. 2.2)", prefs.getString("ptz", "2.2"), false);
        EditText vel = field(l, "Velocidad del PTZ (%)", String.valueOf(prefs.getInt("ptz_velocidad", 10)), false);
        EditText div = field(l, "Canales con dos cámaras (ej. 2)", prefs.getString("dividir", "2"), false);
        AlertDialog.Builder dlg = new AlertDialog.Builder(this).setTitle(title).setView(l).setCancelable(false);
        if (canShare && !prefs.getString("serial", "").isEmpty())
            dlg.setNeutralButton("Compartir configuración", (d, w) -> showShare());
        if (!prefs.getString("serial", "").isEmpty() && !prefs.getString("pass", "").isEmpty())
            dlg.setNegativeButton("Cancelar", null);
        dlg.setPositiveButton("Guardar", (d, w) -> {
            SharedPreferences.Editor e = prefs.edit()
                    .putString("serial", sn.getText().toString().trim())
                    .putString("user", user.getText().toString().trim())
                    .putString("ptz", ptz.getText().toString().trim())
                    .putString("dividir", div.getText().toString().trim());
            try { e.putInt("ptz_velocidad", Integer.parseInt(vel.getText().toString().trim())); } catch (Exception ignored) { }
            String p = pass.getText().toString();
            if (!p.isEmpty()) {
                try { e.putString("pass", Secret.seal(p)); } catch (Exception ex) { status = "No pude guardar la clave: " + ex; }
            }
            e.apply();
            reconnect();
        }).show();
    }

    private EditText field(LinearLayout l, String hint, String val, boolean secret) {
        EditText t = new EditText(this);
        t.setHint(hint);
        t.setText(val);
        if (secret) t.setInputType(InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        l.addView(t);
        return t;
    }

    // ---- connection ----
    private void reconnect() {
        stopPlayers();
        Session s = session;
        session = null;
        if (s != null) new Thread(s::close).start();
        connect();
    }

    private void connect() {
        setStatus("Conectando con el grabador...");
        new Thread(() -> {
            long wait = 2000;
            String clouds = prefs.getString("servidores", "");
            if (!clouds.isEmpty()) Camaras.setClouds(clouds);
            for (int attempt = 1; ; attempt++) {
                try {
                    Session s = Camaras.connect(prefs.getString("serial", ""), prefs.getString("user", "admin"),
                            Secret.open(prefs.getString("pass", "")));
                    session = s;
                    addExtras(s); // standalone cameras (own tunnel each), best-effort
                    String cams = s.cameras();
                    ui.post(() -> started(cams));
                    return;
                } catch (Exception e) {
                    String msg = String.valueOf(e.getMessage());
                    if (msg.contains(Camaras.ErrBadPassword)) {
                        ui.post(() -> askSettings("El grabador no aceptó la clave"));
                        return;
                    }
                    setStatus("No hay conexión con las cámaras (intento " + attempt + "):\n" + msg + "\n\nVuelvo a probar en " + wait / 1000 + " s.");
                    try { Thread.sleep(wait); } catch (InterruptedException ie) { return; }
                    wait = Math.min(wait * 2, 15000);
                }
            }
        }).start();
    }

    // addExtras opens the standalone cameras from the imported config; each becomes
    // one more camera in the list (its own tunnel). A camera that does not open is
    // skipped (logged), so the NVR cameras still come up.
    private void addExtras(Session s) {
        String js = prefs.getString("extra_cams", "");
        if (js.isEmpty()) return;
        try {
            JSONArray a = new JSONArray(js);
            for (int i = 0; i < a.length(); i++) {
                JSONObject c = a.getJSONObject(i);
                String sealed = c.optString("p", "");
                String pass = sealed.isEmpty() ? "" : Secret.open(sealed);
                try {
                    s.addExtra(c.optString("sn").trim(), c.optString("u", "admin").trim(), pass, c.optString("n", "").trim());
                } catch (Exception ex) {
                    android.util.Log.w("camaras", "camara suelta " + c.optString("n") + ": " + ex.getMessage());
                }
            }
        } catch (Exception ignored) {
        }
    }

    private void setStatus(String s) { status = s; ui.post(() -> overlay.invalidate()); }

    private void started(String camsJson) {
        try {
            JSONArray a = new JSONArray(camsJson);
            List<Integer> cs = new ArrayList<>();
            List<String> ns = new ArrayList<>();
            for (int i = 0; i < a.length(); i++) {
                JSONObject o = a.getJSONObject(i);
                if (o.optBoolean("on", true)) { cs.add(o.getInt("ch")); ns.add(o.optString("name", "")); }
            }
            chans = new int[cs.size()];
            names = new String[cs.size()];
            for (int i = 0; i < chans.length; i++) { chans[i] = cs.get(i); names[i] = ns.get(i); }
        } catch (Exception e) {
            setStatus("Lista de cámaras rara: " + e);
            return;
        }
        status = "";
        renderer.makeStreams(chans, list -> {
            streams = list;
            loadMeshes();
            buildTiles();
            for (CamRenderer.Stream s : streams) players.add(play(s));
            ui.postDelayed(new Runnable() {
                int n;
                @Override public void run() {
                    long nowMs = android.os.SystemClock.elapsedRealtime();
                    boolean statusChanged = false;
                    for (int i = 0; i < players.size() && i < streams.size(); i++) {
                        ExoPlayer p = players.get(i);
                        CamRenderer.Stream st = streams.get(i);
                        // track real progress: new frames reaching the screen. A frozen
                        // picture (no new frames) is the only honest "stuck" signal; the
                        // player can sit in BUFFERING or even READY and still show nothing.
                        if (st.frames != st.lastFrames) { st.lastFrames = st.frames; st.lastProgressMs = nowMs; }
                        // live = real frames reached the screen in the last 3 s (never a frame
                        // yet => not live, so the first connect still shows "conectando")
                        boolean live = st.lastProgressMs != 0 && nowMs - st.lastProgressMs < 3000;
                        if (live != st.live) { st.live = live; statusChanged = true; }
                        // single owner of "keep playing": restart a player that is IDLE
                        // (stopped/errored) OR stuck (no new frame for 8 s: a dead tunnel
                        // after the app was in the background, a silent RTSP drop, any
                        // stall), at most once every 5 s so a down tunnel is not hammered.
                        long sinceFrame = st.lastProgressMs != 0 ? nowMs - st.lastProgressMs : nowMs - st.lastStart;
                        boolean stuck = sinceFrame > 8000;
                        if ((p.getPlaybackState() == Player.STATE_IDLE || stuck) && !released.contains(p)
                                && nowMs - st.lastStart > 5000) {
                            start(p, st);
                            continue;
                        }
                        // stay near live, like the TV app's cushion (1.2 s): a little behind, play
                        // a bit faster (not noticeable); far behind after a stall (3 cushions),
                        // jump ahead to one cushion instead of playing the backlog fast (live view)
                        long ahead = p.getBufferedPosition() - p.getCurrentPosition();
                        if (ahead > 3600) {
                            p.seekTo(p.getBufferedPosition() - 1200);
                            android.util.Log.i("camaras", "canal " + st.ch + ": " + ahead + " ms atrasado, salto al presente");
                        }
                        float speed = ahead > 1800 && ahead <= 3600 ? 1.25f : 1f;
                        if (p.getPlaybackParameters().speed != speed) p.setPlaybackSpeed(speed);
                        if (n % 5 == 0)
                            android.util.Log.i("camaras", "canal " + st.ch + ": estado " + p.getPlaybackState() + " jugando " + p.isPlaying()
                                    + " adelante " + ahead + " ms, velocidad " + speed + ", cuadros " + st.frames + (st.software ? " (software)" : ""));
                    }
                    if (statusChanged) {
                        overlay.invalidate(); // redraw the conectando/reconectando label
                        gl.requestRender();   // and the GL view, to black out (or restore) the tile
                    }
                    n++;
                    ui.postDelayed(this, 1000);
                }
            }, 1000);
        });
    }

    // ---- video: one player per channel, its decoder writes into the stream's texture ----
    private ExoPlayer play(CamRenderer.Stream s) { return play(s, true); }

    /** go = false: the caller starts it (after adding its own listeners). */
    private ExoPlayer play(CamRenderer.Stream s, boolean go) {
        // a stream whose hardware decoder failed (the Pixel 8's HEVC decoder fails on
        // the 2304x2592 HD of the split camera) gets the phone's software decoders
        androidx.media3.exoplayer.mediacodec.MediaCodecSelector sel = (mime, secure, tunneling) -> {
            List<androidx.media3.exoplayer.mediacodec.MediaCodecInfo> all =
                    androidx.media3.exoplayer.mediacodec.MediaCodecUtil.getDecoderInfos(mime, secure, tunneling);
            if (!s.software) return all;
            List<androidx.media3.exoplayer.mediacodec.MediaCodecInfo> soft = new ArrayList<>();
            for (androidx.media3.exoplayer.mediacodec.MediaCodecInfo i : all) if (i.softwareOnly) soft.add(i);
            return soft.isEmpty() ? all : soft;
        };
        ExoPlayer p = new ExoPlayer.Builder(this, new androidx.media3.exoplayer.DefaultRenderersFactory(this)
                .setEnableDecoderFallback(true).setMediaCodecSelector(sel))
                .setLoadControl(new DefaultLoadControl.Builder().setBufferDurationsMs(800, 3000, 300, 300).build())
                .build();
        p.setVideoSurface(s.surface);
        // video only: the cameras' audio is not used, and waiting for it stalled the
        // cameras that send it (state "buffering" while the video kept coming)
        p.setTrackSelectionParameters(p.getTrackSelectionParameters().buildUpon()
                .setTrackTypeDisabled(androidx.media3.common.C.TRACK_TYPE_AUDIO, true).build());
        p.addListener(new Player.Listener() {
            @Override public void onVideoSizeChanged(VideoSize v) {
                if (v.width > 0) { s.vw = v.width; s.vh = v.height; ui.post(MainActivity.this::relayout); }
            }
            @Override public void onPlayerError(PlaybackException e) {
                if (!s.software && e.errorCode == PlaybackException.ERROR_CODE_DECODING_FAILED) {
                    s.software = true; // the selector now gives the software decoder
                    android.util.Log.w("camaras", "canal " + s.ch + ": decodificador por software");
                }
                // the error drops the player to IDLE; the once-a-second loop restarts any
                // IDLE player (with a few seconds between tries). One place owns restarting.
            }
        });
        if (go) start(p, s);
        else p.setMediaSource(new RtspMediaSource.Factory().setForceUseRtpTcp(true)
                .createMediaSource(MediaItem.fromUri(Uri.parse(session.rtsp(s.ch, s.hd = wantHD(s))))));
        return p;
    }

    private void start(ExoPlayer p, CamRenderer.Stream s) {
        Session ss = session;
        if (ss == null || released.contains(p)) return; // stopped meanwhile
        s.lastStart = android.os.SystemClock.elapsedRealtime();
        boolean hd = wantHD(s);
        s.hd = hd;
        p.setMediaSource(new RtspMediaSource.Factory().setForceUseRtpTcp(true)
                .createMediaSource(MediaItem.fromUri(Uri.parse(ss.rtsp(s.ch, hd)))));
        p.prepare();
        p.play();
    }

    /** HD or not for this stream: an HD panorama needs the phone's mesh, like the TV app. */
    private boolean wantHD(CamRenderer.Stream s) {
        boolean hd = hdSet().contains(s.ch);
        if (hd && isSplit(s.ch) && panoOn() && s.mesh == null) hd = false;
        return hd;
    }

    /**
     * HD on or off, like the TV app: the other video opens next to the one on screen,
     * which keeps playing; when the new one has played a cushion (1.2 s), it takes the
     * old one's place (no stop on screen). Meanwhile a snake runs around the HD tag.
     */
    private void switchVideo(int ch) {
        int i = -1;
        for (int k = 0; k < streams.size(); k++) if (streams.get(k).ch == ch) i = k;
        if (i < 0) return;
        dropOpening(ch); // asked again while opening: start over with what is wanted now
        CamRenderer.Stream cur = streams.get(i);
        if (wantHD(cur) == cur.hd) { overlay.invalidate(); return; } // same video as now
        final int idx = i;
        opening.put(ch, null); // the snake starts now
        overlay.invalidate();
        renderer.makeSpare(ch, ns -> {
            if (!opening.containsKey(ch) || opening.get(ch) != null) { renderer.free(ns); return; } // given up meanwhile
            ns.mesh = streams.get(idx).mesh;
            ns.software = streams.get(idx).software && wantHD(ns) == streams.get(idx).hd;
            openingStream.put(ch, ns);
            ExoPlayer np = play(ns, false);
            opening.put(ch, np);
            np.addListener(new Player.Listener() {
                @Override public void onRenderedFirstFrame() {
                    np.removeListener(this);
                    ui.postDelayed(() -> take(ch, idx, np, ns), 1200);
                }
            });
            np.prepare();
            np.play();
            // never opens: forget it and open the old way (stop and open again)
            ui.postDelayed(() -> {
                if (opening.get(ch) == np) {
                    android.util.Log.w("camaras", "canal " + ch + ": cambio de video: el nuevo no abrio");
                    dropOpening(ch);
                    restart(ch);
                }
            }, 25000);
        });
    }

    /** The new video replaces the old one on screen. */
    private void take(int ch, int idx, ExoPlayer np, CamRenderer.Stream ns) {
        if (opening.get(ch) != np || idx >= streams.size() || streams.get(idx).ch != ch) return;
        opening.remove(ch);
        openingStream.remove(ch);
        renderer.swap(idx, ns, old -> {
            List<CamRenderer.Stream> list = new ArrayList<>(streams);
            list.set(idx, ns);
            streams = list;
            ExoPlayer op = players.set(idx, np);
            released.add(op);
            op.release();
            renderer.free(old);
            android.util.Log.i("camaras", "canal " + ch + ": cambio de video (" + (ns.hd ? "HD" : "baja") + ")");
            relayout();
            overlay.invalidate();
        });
    }

    /** Gives up the video opening for this channel, if any. */
    private void dropOpening(int ch) {
        if (!opening.containsKey(ch)) return;
        ExoPlayer p = opening.remove(ch);
        CamRenderer.Stream s = openingStream.remove(ch);
        if (p != null) { released.add(p); p.release(); }
        if (s != null) renderer.free(s);
        overlay.invalidate();
    }

    private void restart(int ch) {
        for (int i = 0; i < streams.size(); i++) if (streams.get(i).ch == ch) start(players.get(i), streams.get(i));
    }

    private void stopPlayers() {
        for (Integer ch : new ArrayList<>(opening.keySet())) dropOpening(ch);
        for (ExoPlayer p : players) { released.add(p); p.release(); }
        players.clear();
    }

    @Override protected void onDestroy() {
        stopPlayers();
        Session s = session;
        if (s != null) new Thread(s::close).start();
        super.onDestroy();
    }

    @Override protected void onPause() {
        super.onPause();
        gl.onPause(); // pause the GL thread (the surfaces survive, see setPreserveEGLContextOnPause)
        // stop pulling video while in the background: the NVR frees the stream, and the
        // decoder does not block on a frame no one is drawing. The players fall to IDLE;
        // the once-a-second loop re-opens them when the app comes back to the front.
        for (Integer ch : new ArrayList<>(opening.keySet())) dropOpening(ch);
        for (ExoPlayer p : players) p.stop();
    }

    @Override protected void onResume() {
        super.onResume();
        gl.onResume();
    }

    // ---- tiles (same keys as the TV app: "2", "2.1", "2.2", "2.3") ----
    private Set<Integer> splitSet() { return ints(prefs.getString("dividir", "2")); }
    private boolean isSplit(int ch) { return splitSet().contains(ch); }
    private boolean panoOn() { return prefs.getBoolean("pano", true); }
    private Set<Integer> hdSet() { return ints(prefs.getString("hd", "")); }

    private static Set<Integer> ints(String s) {
        Set<Integer> r = new HashSet<>();
        for (String p : s.split(",")) try { r.add(Integer.parseInt(p.trim())); } catch (Exception ignored) { }
        return r;
    }

    private void loadMeshes() {
        for (CamRenderer.Stream s : streams) {
            File f = new File(getFilesDir(), "panoramica3_" + s.ch + ".bin");
            try { if (f.exists()) s.mesh = Mesh.parse(Files.readAllBytes(f.toPath())); } catch (Exception ignored) { }
        }
    }

    private void buildTiles() {
        tiles.clear();
        for (int i = 0; i < streams.size(); i++) {
            CamRenderer.Stream s = streams.get(i);
            String n = names[i];
            if (isSplit(s.ch) && panoOn() && s.mesh != null) tiles.add(new CamRenderer.Tile(i, 3, n));
            else if (isSplit(s.ch)) { tiles.add(new CamRenderer.Tile(i, 1, n)); tiles.add(new CamRenderer.Tile(i, 2, n)); }
            else tiles.add(new CamRenderer.Tile(i, 0, n));
        }
        // show them in the saved order (same keys as the TV: "0", "2.3", "100"); unknown keys keep their place
        final java.util.List<String> ord = java.util.Arrays.asList(prefs.getString("orden", "").split(","));
        java.util.Collections.sort(tiles, (a, b) -> {
            int ia = ord.indexOf(key(a)), ib = ord.indexOf(key(b));
            if (ia < 0) ia = Integer.MAX_VALUE;
            if (ib < 0) ib = Integer.MAX_VALUE;
            return Integer.compare(ia, ib);
        });
        renderer.big = -1;
        renderer.zoom = 1;
        relayout();
    }

    /** Trade two cameras' places and save the order (drag to reorder). */
    private void swapTiles(int a, int b) {
        if (a < 0 || b < 0 || a >= tiles.size() || b >= tiles.size() || a == b) return;
        java.util.List<String> keys = new ArrayList<>();
        for (CamRenderer.Tile t : tiles) keys.add(key(t));
        String ka = keys.get(a);
        keys.set(a, keys.get(b));
        keys.set(b, ka);
        prefs.edit().putString("orden", android.text.TextUtils.join(",", keys)).apply();
        buildTiles();
    }

    private void relayout() {
        float W = gl.getWidth(), H = gl.getHeight();
        if (W <= 0 || tiles.isEmpty()) { overlay.invalidate(); return; }
        RectF[] r;
        if (renderer.big >= 0) r = Layout.arrangeBig(tiles.size(), renderer.big, W, H);
        else {
            int[] u = new int[tiles.size()];
            for (int i = 0; i < u.length; i++) u[i] = tiles.get(i).half == 3 ? 2 : 1;
            r = Layout.arrange(u, W, H);
        }
        for (int i = 0; i < tiles.size(); i++) tiles.get(i).rect = r[i];
        renderer.tiles = new ArrayList<>(tiles);
        gl.requestRender();
        overlay.invalidate();
    }

    private String key(CamRenderer.Tile t) {
        int ch = streams.get(t.stream).ch;
        return t.half == 0 ? String.valueOf(ch) : ch + "." + t.half;
    }

    private boolean canPTZ(CamRenderer.Tile t) {
        if (t.half == 3) return false;
        for (String s : prefs.getString("ptz", "2.2").split(",")) {
            s = s.trim();
            if (!s.isEmpty() && (s.equals(key(t)) || s.equals(String.valueOf(streams.get(t.stream).ch)))) return true;
        }
        return false;
    }

    private int tileAt(float x, float y) {
        int b = renderer.big;
        if (b >= 0) { // the small ones float over the big one
            for (int i = 0; i < tiles.size(); i++) if (i != b && tiles.get(i).rect.contains(x, y)) return i;
            return tiles.get(b).rect.contains(x, y) ? b : -1;
        }
        for (int i = 0; i < tiles.size(); i++) if (tiles.get(i).rect.contains(x, y)) return i;
        return -1;
    }

    // ---- gestures ----
    private final class Overlay extends View {
        final Paint text = new Paint(Paint.ANTI_ALIAS_FLAG), tag = new Paint(Paint.ANTI_ALIAS_FLAG), box = new Paint();
        final GestureDetector gd;
        final ScaleGestureDetector sgd;
        float pressX, pressY;
        boolean ptzDrag, scaling;
        // drag to reorder (all-cameras view): the picked tile, the one under the finger, and where the finger is
        int dragFrom = -1, dragTo = -1;
        boolean dragging;
        float dragX, dragY, downX, downY;
        final int slop;
        final Paint hl = new Paint(Paint.ANTI_ALIAS_FLAG);

        Overlay(Context c) {
            super(c);
            slop = android.view.ViewConfiguration.get(c).getScaledTouchSlop() * 2;
            text.setColor(Color.rgb(0xc0, 0xc0, 0xc0));
            text.setTextSize(38);
            tag.setColor(Color.WHITE);
            tag.setTypeface(Typeface.DEFAULT_BOLD);
            box.setColor(Color.rgb(0xd5, 0x10, 0x18));
            gd = new GestureDetector(c, new GestureDetector.SimpleOnGestureListener() {
                @Override public boolean onSingleTapConfirmed(MotionEvent e) {
                    if (tiles.isEmpty()) { askSettings("Datos del grabador"); return true; }
                    int i = tileAt(e.getX(), e.getY());
                    if (i < 0) return true;
                    renderer.big = renderer.big == i ? -1 : i;
                    renderer.zoom = 1; renderer.cx = 0.5f; renderer.cy = 0.5f;
                    relayout();
                    return true;
                }
                @Override public void onLongPress(MotionEvent e) {
                    int i = tileAt(e.getX(), e.getY());
                    if (i < 0) { askSettings("Ajustes", true); return; }
                    menu(tiles.get(i));
                }
                @Override public boolean onScroll(MotionEvent e1, MotionEvent e2, float dx, float dy) {
                    int b = renderer.big;
                    if (b < 0 || scaling || e1 == null) return false;
                    CamRenderer.Tile t = tiles.get(b);
                    if (canPTZ(t) && renderer.zoom <= 1.01f) {
                        Session s = session;
                        if (s != null) {
                            ptzDrag = true;
                            s.ptzDrag(streams.get(t.stream).ch, (int) (e2.getX() - e1.getX()), (int) (e2.getY() - e1.getY()),
                                    prefs.getBoolean("ptz_invertir_pan", true), prefs.getInt("ptz_velocidad", 10));
                        }
                    } else if (renderer.zoom > 1.01f) { // pan the zoomed picture
                        RectF r = renderer.fitted(t);
                        float z = renderer.zoom;
                        renderer.cx = clampPan(renderer.cx + dx / (r.width() * z), r.width() * z, gl.getWidth());
                        renderer.cy = clampPan(renderer.cy + dy / (r.height() * z), r.height() * z, gl.getHeight());
                        gl.requestRender();
                    }
                    return true;
                }
            });
            sgd = new ScaleGestureDetector(c, new ScaleGestureDetector.SimpleOnScaleGestureListener() {
                @Override public boolean onScaleBegin(ScaleGestureDetector d) { scaling = true; return renderer.big >= 0; }
                @Override public boolean onScale(ScaleGestureDetector d) {
                    float z = Math.max(1, Math.min(6, renderer.zoom * d.getScaleFactor()));
                    renderer.zoom = z;
                    RectF r = renderer.fitted(tiles.get(renderer.big));
                    renderer.cx = clampPan(renderer.cx, r.width() * z, gl.getWidth());
                    renderer.cy = clampPan(renderer.cy, r.height() * z, gl.getHeight());
                    gl.requestRender();
                    return true;
                }
                @Override public void onScaleEnd(ScaleGestureDetector d) { scaling = false; }
            });
        }

        /** The point at the screen's middle, kept so the picture covers the screen (or stays centred if smaller). */
        float clampPan(float c, float pictureSize, float screenSize) {
            if (pictureSize <= screenSize) return 0.5f;
            float h = screenSize / 2f / pictureSize;
            return Math.max(h, Math.min(1 - h, c));
        }

        @Override public boolean onTouchEvent(MotionEvent e) {
            int a = e.getActionMasked();
            float x = e.getX(), y = e.getY();
            // drag to reorder, only in the all-cameras view and only a real drag (not a tap or a pinch)
            if (renderer.big < 0 && !scaling) {
                if (a == MotionEvent.ACTION_DOWN) { dragFrom = tileAt(x, y); downX = x; downY = y; dragging = false; }
                else if (a == MotionEvent.ACTION_MOVE && dragFrom >= 0 && !dragging && Math.hypot(x - downX, y - downY) > slop) {
                    dragging = true; // stop the tap/long-press/scroll from also firing
                    MotionEvent cxl = MotionEvent.obtain(e); cxl.setAction(MotionEvent.ACTION_CANCEL);
                    gd.onTouchEvent(cxl); sgd.onTouchEvent(cxl); cxl.recycle();
                }
                if (dragging) {
                    if (a == MotionEvent.ACTION_MOVE) { dragX = x; dragY = y; int t = tileAt(x, y); dragTo = (t >= 0 && t != dragFrom) ? t : -1; invalidate(); }
                    else if (a == MotionEvent.ACTION_UP || a == MotionEvent.ACTION_CANCEL) {
                        if (a == MotionEvent.ACTION_UP) swapTiles(dragFrom, dragTo);
                        dragFrom = dragTo = -1; dragging = false; invalidate();
                    }
                    return true;
                }
                if (a == MotionEvent.ACTION_UP || a == MotionEvent.ACTION_CANCEL) dragFrom = -1;
            }
            sgd.onTouchEvent(e);
            gd.onTouchEvent(e);
            if ((a == MotionEvent.ACTION_UP || a == MotionEvent.ACTION_CANCEL) && ptzDrag) {
                ptzDrag = false;
                Session s = session;
                if (s != null) s.ptzRelease();
            }
            return true;
        }

        @Override protected void onDraw(Canvas c) {
            if (!status.isEmpty() || tiles.isEmpty()) {
                text.setTextSize(44);
                float y = getHeight() / 2f - 60;
                String s = status.isEmpty() ? "Conectando..." : status;
                for (String line : s.split("\n")) {
                    c.drawText(line, (getWidth() - text.measureText(line)) / 2, y, text);
                    y += 56;
                }
                return;
            }
            Set<Integer> hd = hdSet();
            boolean snaking = false;
            for (int i = 0; i < tiles.size(); i++) {
                CamRenderer.Tile t = tiles.get(i);
                CamRenderer.Stream s = streams.get(t.stream);
                RectF r = t.rect;
                boolean small = renderer.big >= 0 && i != renderer.big;
                if (!s.live) { // no fresh frames: say so instead of leaving a frozen picture silent
                    String msg = s.playing ? "  (reconectando...)" : "  (conectando...)";
                    text.setTextSize(small ? 18 : 30);
                    c.drawText(t.name + msg, r.left + 12, r.top + 40, text);
                }
                if (small) continue;
                tag.setTextSize(34);
                float tw = tag.measureText("HD");
                if (hd.contains(s.ch)) {
                    c.drawRect(r.left + 24, r.top + 20, r.left + 24 + tw + 20, r.top + 66, box);
                    c.drawText("HD", r.left + 34, r.top + 56, tag);
                }
                // the other video is opening (HD on or off): a snake runs around the tag (or where it was)
                if (opening.containsKey(s.ch)) {
                    float g = 34 / 6f;
                    snake(c, new RectF(r.left + 24 - g, r.top + 20 - g, r.left + 24 + tw + 20 + g, r.top + 66 + g), 34 / 4f);
                    snaking = true;
                }
            }
            if (dragging && renderer.big < 0) {
                if (dragTo >= 0 && dragTo < tiles.size()) { hl.setColor(0x5540a0ff); c.drawRect(tiles.get(dragTo).rect, hl); }
                if (dragFrom >= 0) {
                    hl.setColor(0xcc000000); c.drawCircle(dragX, dragY, 46, hl);
                    tag.setTextSize(44);
                    String lbl = String.valueOf(dragFrom + 1);
                    c.drawText(lbl, dragX - tag.measureText(lbl) / 2, dragY + 16, tag);
                }
            }
            if (snaking) postInvalidateOnAnimation();
        }

        private final Paint snakePaint = new Paint(Paint.ANTI_ALIAS_FLAG);
        private final android.graphics.Path snakeRing = new android.graphics.Path(), snakePart = new android.graphics.Path();

        /** 40% of a rounded rectangle's outline, going round clockwise (like the TV app's drawSnake). */
        private void snake(Canvas c, RectF r, float rc) {
            snakePaint.setStyle(Paint.Style.STROKE);
            snakePaint.setStrokeWidth(34 / 7f);
            snakePaint.setStrokeCap(Paint.Cap.ROUND);
            snakePaint.setStrokeJoin(Paint.Join.ROUND);
            snakePaint.setColor(0xffffd040);
            snakeRing.reset();
            snakeRing.addRoundRect(r, rc, rc, android.graphics.Path.Direction.CW);
            android.graphics.PathMeasure pm = new android.graphics.PathMeasure(snakeRing, true);
            float total = pm.getLength();
            float start = (float) ((System.currentTimeMillis() / 1000.0 * 0.8) % 1.0) * total, end = start + 0.4f * total;
            snakePart.reset();
            pm.getSegment(start, Math.min(end, total), snakePart, true);
            if (end > total) pm.getSegment(0, end - total, snakePart, true);
            c.drawPath(snakePart, snakePaint);
        }
    }

    // ---- panorama calibration: the TV app's page and calib.js, served by the Go core ----
    private android.webkit.WebView calView;

    private void calibrate(int ch) {
        Session s = session;
        if (s == null) return;
        File mesh = new File(getFilesDir(), "panoramica3_" + ch + ".bin");
        String url;
        try { url = s.startCalib(ch, mesh.getAbsolutePath()); } catch (Exception e) { status = "Calibrar: " + e.getMessage(); overlay.invalidate(); return; }
        long ver0 = s.meshVersion();
        android.webkit.WebView wv = new android.webkit.WebView(this);
        wv.getSettings().setJavaScriptEnabled(true);
        wv.getSettings().setDomStorageEnabled(true);
        wv.setWebViewClient(new android.webkit.WebViewClient() {
            // the page's "Cerrar" (in-app mode) asks to close the whole calibration
            @Override public boolean shouldOverrideUrlLoading(android.webkit.WebView v, android.webkit.WebResourceRequest req) {
                if ("camaras-app".equals(req.getUrl().getScheme())) { closeCalib(); return true; }
                return false;
            }
            @Override public void onPageFinished(android.webkit.WebView v, String u) {
                // open the calibration right away (what the page's "Calibrar" button does)
                ui.postDelayed(() -> v.evaluateJavascript("calOpen()", null), 1500);
            }
        });
        ((FrameLayout) gl.getParent()).addView(wv);
        calView = wv;
        wv.loadUrl(url + (url.contains("?") ? "&" : "?") + "app=1");
        // the mesh arrives through the page's "Usar en la tele": then use it here
        ui.postDelayed(new Runnable() {
            @Override public void run() {
                if (calView != wv) return;
                if (s.meshVersion() != ver0) {
                    closeCalib();
                    prefs.edit().putBoolean("pano", true).apply();
                    loadMeshes();
                    buildTiles();
                    restart(ch);
                    return;
                }
                ui.postDelayed(this, 500);
            }
        }, 500);
    }

    private void closeCalib() {
        if (calView == null) return;
        ((FrameLayout) gl.getParent()).removeView(calView);
        calView.destroy();
        calView = null;
        Session s = session;
        if (s != null) s.stopCalib();
        hideBars();
    }

    @Override public void onBackPressed() {
        if (shareView != null) { closeShare(); return; }
        if (calView != null) { closeCalib(); return; }
        if (renderer.big >= 0) { renderer.big = -1; renderer.zoom = 1; relayout(); return; }
        super.onBackPressed();
    }

    private void menu(CamRenderer.Tile t) {
        int ch = streams.get(t.stream).ch;
        boolean hd = hdSet().contains(ch);
        List<String> items = new ArrayList<>();
        items.add(hd ? "Calidad baja (más rápido)" : "HD");
        if (isSplit(ch)) {
            items.add(panoOn() ? "Cámaras separadas" : "Cámaras juntas (panorámica)");
            items.add("Calibrar panorámica");
        }
        items.add("Compartir configuración (QR)");
        new AlertDialog.Builder(this).setTitle(t.name).setItems(items.toArray(new String[0]), (d, which) -> {
            if (items.get(which).startsWith("Compartir")) { showShare(); return; }
            if (which == 0) {
                Set<Integer> s = hdSet();
                if (hd) s.remove(ch); else s.add(ch);
                StringBuilder sb = new StringBuilder();
                for (int x : s) sb.append(sb.length() > 0 ? "," : "").append(x);
                prefs.edit().putString("hd", sb.toString()).apply();
                switchVideo(ch);
                overlay.invalidate();
            } else if (which == 1) {
                prefs.edit().putBoolean("pano", !panoOn()).apply();
                buildTiles();
                restart(ch);
            } else {
                calibrate(ch);
            }
        }).show();
    }

    // ---- the configuration QR (camaras://config?v=1&d=..., the Go core's compartir) ----
    // It holds the NVR password: it is only shown when asked for, never logged.

    // the settings this app knows; the other camaras.ini keys are kept as they came
    private static final String[] KNOWN = {"ptz", "dividir", "ptz_velocidad", "ptz_invertir_pan", "servidores"};

    private static String importLink(Intent i) {
        if (i == null || !Intent.ACTION_VIEW.equals(i.getAction()) || i.getData() == null) return null;
        return "camaras".equalsIgnoreCase(i.getData().getScheme()) ? i.getData().toString() : null;
    }

    /** Shows what the link holds and imports it on "Importar"; starting: the app was opened by the link. */
    private void askImport(String link, boolean starting) {
        // answered once (import, cancel or error) is enough: Android may hand the same link again
        prefs.edit().putString("link_visto", link).apply();
        String js, summary;
        try {
            js = Camaras.configFromURI(link);
            summary = Camaras.configSummary(link);
        } catch (Exception e) {
            new AlertDialog.Builder(this).setTitle("No pude leer la configuración").setMessage(String.valueOf(e.getMessage()))
                    .setCancelable(false).setPositiveButton("OK", (d, w) -> { if (starting) start(); }).show();
            return;
        }
        new AlertDialog.Builder(this).setTitle("¿Importar esta configuración?")
                .setMessage(summary + "\nReemplaza la de este teléfono. La calibración de la panorámica no viene: se hace aquí.")
                .setCancelable(false)
                .setPositiveButton("Importar", (d, w) -> {
                    String err = importConfig(js);
                    if (err == null) { reconnect(); return; }
                    new AlertDialog.Builder(this).setTitle("No pude importar").setMessage(err).setCancelable(false)
                            .setPositiveButton("OK", (d2, w2) -> { if (starting) start(); }).show();
                })
                .setNegativeButton("Cancelar", (d, w) -> { if (starting) start(); })
                .show();
    }

    /** Stores the configuration like the settings screen does; null when it went well. */
    private String importConfig(String js) {
        try {
            JSONObject o = new JSONObject(js);
            SharedPreferences.Editor e = prefs.edit();
            e.putString("serial", o.getString("sn").trim());
            String user = o.optString("u", "").trim();
            e.putString("user", user.isEmpty() ? "admin" : user);
            String p = o.optString("p", "");
            if (!p.isEmpty()) e.putString("pass", Secret.seal(p)); // empty: keep the stored one
            JSONObject ini = o.optJSONObject("ini");
            if (ini == null) ini = new JSONObject();
            // a setting that did not come gets this app's default
            if (ini.has("ptz")) e.putString("ptz", ini.optString("ptz").trim()); else e.remove("ptz");
            if (ini.has("dividir")) e.putString("dividir", ini.optString("dividir").trim()); else e.remove("dividir");
            e.remove("ptz_velocidad");
            try { if (ini.has("ptz_velocidad")) e.putInt("ptz_velocidad", Integer.parseInt(ini.optString("ptz_velocidad").trim())); } catch (Exception ignored) { }
            if (ini.has("ptz_invertir_pan")) e.putBoolean("ptz_invertir_pan", "1".equals(ini.optString("ptz_invertir_pan").trim()));
            else e.remove("ptz_invertir_pan");
            e.putString("servidores", ini.optString("servidores", "").trim());
            JSONObject extra = new JSONObject();
            for (Iterator<String> it = ini.keys(); it.hasNext(); ) {
                String k = it.next();
                if (!java.util.Arrays.asList(KNOWN).contains(k)) extra.put(k, ini.optString(k));
            }
            e.putString("ini_extra", extra.toString());
            // standalone cameras (not on the NVR): keep each one's serial, user, sealed password and name
            JSONArray ex = o.optJSONArray("extra");
            if (ex != null && ex.length() > 0) {
                JSONArray store = new JSONArray();
                for (int i = 0; i < ex.length(); i++) {
                    JSONObject c = ex.getJSONObject(i);
                    JSONObject d = new JSONObject();
                    d.put("sn", c.optString("sn").trim());
                    d.put("u", c.optString("u", "admin").trim());
                    String cp = c.optString("p", "");
                    d.put("p", cp.isEmpty() ? "" : Secret.seal(cp));
                    d.put("n", c.optString("n", "").trim());
                    store.put(d);
                }
                e.putString("extra_cams", store.toString());
            } else {
                e.remove("extra_cams"); // the shared config is the source of truth
            }
            e.putString("hd", o.optString("hd", ""));
            e.putString("orden", o.optString("orden", ""));
            // the TV app keeps the panorama per channel; this app has one switch: on if any is on
            JSONObject pano = o.optJSONObject("pano");
            if (pano != null && pano.length() > 0) {
                boolean on = false;
                for (Iterator<String> it = pano.keys(); it.hasNext(); ) if (pano.optInt(it.next()) == 3) on = true;
                e.putBoolean("pano", on);
            }
            e.apply();
            return null;
        } catch (Exception ex) {
            return String.valueOf(ex.getMessage());
        }
    }

    /** This phone's configuration, as the Go core's compartir.Config JSON. */
    private JSONObject myConfig() throws Exception {
        JSONObject o = new JSONObject();
        o.put("sn", prefs.getString("serial", ""));
        o.put("u", prefs.getString("user", "admin"));
        o.put("p", Secret.open(prefs.getString("pass", "")));
        JSONObject ini;
        try { ini = new JSONObject(prefs.getString("ini_extra", "{}")); } catch (Exception e) { ini = new JSONObject(); }
        ini.put("ptz", prefs.getString("ptz", "2.2"));
        ini.put("dividir", prefs.getString("dividir", "2"));
        ini.put("ptz_velocidad", String.valueOf(prefs.getInt("ptz_velocidad", 10)));
        ini.put("ptz_invertir_pan", prefs.getBoolean("ptz_invertir_pan", true) ? "1" : "0");
        String clouds = prefs.getString("servidores", "");
        if (!clouds.isEmpty()) ini.put("servidores", clouds);
        o.put("ini", ini);
        if (!prefs.getString("hd", "").isEmpty()) o.put("hd", prefs.getString("hd", ""));
        if (!prefs.getString("orden", "").isEmpty()) o.put("orden", prefs.getString("orden", ""));
        JSONObject pano = new JSONObject();
        for (int ch : splitSet()) pano.put(String.valueOf(ch), panoOn() ? 3 : 0);
        if (pano.length() > 0) o.put("pano", pano);
        // standalone cameras (own serial): share them too (each password in the clear, like the NVR's)
        String exjs = prefs.getString("extra_cams", "");
        if (!exjs.isEmpty()) {
            JSONArray src = new JSONArray(exjs);
            JSONArray out = new JSONArray();
            for (int i = 0; i < src.length(); i++) {
                JSONObject c = src.getJSONObject(i);
                JSONObject d = new JSONObject();
                d.put("sn", c.optString("sn"));
                if (!c.optString("u").isEmpty()) d.put("u", c.optString("u"));
                String sealed = c.optString("p", "");
                if (!sealed.isEmpty()) d.put("p", Secret.open(sealed));
                if (!c.optString("n").isEmpty()) d.put("n", c.optString("n"));
                out.put(d);
            }
            if (out.length() > 0) o.put("extra", out);
        }
        return o;
    }

    private View shareView;

    /** The configuration QR, full screen, until "Cerrar" (or back, or leaving the app). */
    private void showShare() {
        Bitmap qr;
        try {
            byte[] m = Camaras.qrModules(Camaras.configURI(myConfig().toString()));
            int n = (int) Math.round(Math.sqrt(m.length)), q = n + 4; // with a quiet zone of 2 modules
            int[] px = new int[q * q];
            java.util.Arrays.fill(px, Color.WHITE);
            for (int y = 0; y < n; y++) for (int x = 0; x < n; x++) if (m[y * n + x] != 0) px[(y + 2) * q + x + 2] = Color.BLACK;
            qr = Bitmap.createBitmap(px, q, q, Bitmap.Config.ARGB_8888);
        } catch (Exception e) {
            new AlertDialog.Builder(this).setTitle("No pude hacer el QR").setMessage(String.valueOf(e.getMessage()))
                    .setPositiveButton("OK", null).show();
            return;
        }
        closeShare();
        LinearLayout l = new LinearLayout(this);
        l.setOrientation(LinearLayout.VERTICAL);
        l.setBackgroundColor(Color.WHITE);
        l.setPadding(40, 60, 40, 40);
        l.setClickable(true); // the cameras below get no touches
        TextView t = new TextView(this);
        t.setText("Configuración de las cámaras");
        t.setTextColor(Color.BLACK);
        t.setTextSize(22);
        t.setTypeface(Typeface.DEFAULT_BOLD);
        t.setGravity(Gravity.CENTER);
        l.addView(t);
        TextView t2 = new TextView(this);
        t2.setText("Escanealo con la cámara del otro teléfono (con esta app instalada).\nIncluye la clave del grabador: no lo muestres a nadie más.");
        t2.setTextColor(Color.DKGRAY);
        t2.setTextSize(15);
        t2.setGravity(Gravity.CENTER);
        l.addView(t2);
        View code = new View(this) {
            final Paint p = new Paint();
            { p.setFilterBitmap(false); p.setAntiAlias(false); }
            @Override protected void onDraw(Canvas c) {
                int q = qr.getWidth(), k = Math.max(1, Math.min(getWidth(), getHeight()) / q), s = q * k; // whole pixels per module: sharp
                int x = (getWidth() - s) / 2, y = (getHeight() - s) / 2;
                c.drawBitmap(qr, null, new android.graphics.Rect(x, y, x + s, y + s), p);
            }
        };
        l.addView(code, new LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, 0, 1));
        Button close = new Button(this);
        close.setText("Cerrar");
        close.setOnClickListener(v -> closeShare());
        l.addView(close);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_SECURE); // no screenshots, blank in the recent apps list
        ((FrameLayout) gl.getParent()).addView(l);
        shareView = l;
    }

    private void closeShare() {
        if (shareView == null) return;
        ((FrameLayout) gl.getParent()).removeView(shareView);
        shareView = null;
        getWindow().clearFlags(WindowManager.LayoutParams.FLAG_SECURE);
        hideBars();
    }

    @Override protected void onStop() {
        closeShare(); // the password does not stay on screen when the app is left
        super.onStop();
    }
}
