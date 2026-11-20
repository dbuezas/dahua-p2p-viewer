package ar.camaras.app;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Context;
import android.content.SharedPreferences;
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
import android.view.GestureDetector;
import android.view.MotionEvent;
import android.view.ScaleGestureDetector;
import android.view.View;
import android.view.WindowManager;
import android.widget.EditText;
import android.widget.FrameLayout;
import android.widget.LinearLayout;

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
import java.util.HashSet;
import java.util.List;
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
        if (prefs.getString("serial", "").isEmpty() || prefs.getString("pass", "").isEmpty()) askSettings("Datos del grabador");
        else connect();
    }

    private void hideBars() {
        getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_FULLSCREEN | View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
                | View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY | View.SYSTEM_UI_FLAG_LAYOUT_STABLE
                | View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION | View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN);
    }

    // ---- settings (the camaras.ini of the TV app) ----
    private void askSettings(String title) {
        LinearLayout l = new LinearLayout(this);
        l.setOrientation(LinearLayout.VERTICAL);
        l.setPadding(40, 20, 40, 0);
        EditText sn = field(l, "Número de serie del grabador", prefs.getString("serial", ""), false);
        EditText user = field(l, "Usuario", prefs.getString("user", "admin"), false);
        EditText pass = field(l, "Clave (la misma que en DMSS)", "", true);
        EditText ptz = field(l, "Cámaras con PTZ (ej. 2.2)", prefs.getString("ptz", "2.2"), false);
        EditText vel = field(l, "Velocidad del PTZ (%)", String.valueOf(prefs.getInt("ptz_velocidad", 10)), false);
        EditText div = field(l, "Canales con dos cámaras (ej. 2)", prefs.getString("dividir", "2"), false);
        new AlertDialog.Builder(this).setTitle(title).setView(l).setCancelable(false)
                .setPositiveButton("Guardar", (d, w) -> {
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
            for (int attempt = 1; ; attempt++) {
                try {
                    Session s = Camaras.connect(prefs.getString("serial", ""), prefs.getString("user", "admin"),
                            Secret.open(prefs.getString("pass", "")));
                    session = s;
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
                    for (int i = 0; i < players.size() && i < streams.size(); i++) {
                        ExoPlayer p = players.get(i);
                        CamRenderer.Stream st = streams.get(i);
                        // stay near live, like the TV app's cushion: the player gathers several
                        // seconds when the video comes in bursts; play a bit faster to catch up
                        long ahead = p.getBufferedPosition() - p.getCurrentPosition();
                        float speed = ahead > 2500 ? 1.5f : ahead > 1500 ? 1.15f : 1f;
                        if (p.getPlaybackParameters().speed != speed) p.setPlaybackSpeed(speed);
                        if (n % 5 == 0)
                            android.util.Log.i("camaras", "canal " + st.ch + ": estado " + p.getPlaybackState() + " jugando " + p.isPlaying()
                                    + " adelante " + ahead + " ms, velocidad " + speed + ", cuadros " + st.frames + (st.software ? " (software)" : ""));
                    }
                    n++;
                    ui.postDelayed(this, 1000);
                }
            }, 1000);
        });
    }

    // ---- video: one player per channel, its decoder writes into the stream's texture ----
    private ExoPlayer play(CamRenderer.Stream s) {
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
                ui.postDelayed(() -> start(p, s), 2000); // try again, like the TV app's watchdog
            }
        });
        start(p, s);
        return p;
    }

    private void start(ExoPlayer p, CamRenderer.Stream s) {
        Session ss = session;
        if (ss == null) return;
        boolean hd = hdSet().contains(s.ch);
        // an HD panorama needs the phone's mesh, like the TV app
        if (hd && isSplit(s.ch) && panoOn() && s.mesh == null) hd = false;
        p.setMediaSource(new RtspMediaSource.Factory().setForceUseRtpTcp(true)
                .createMediaSource(MediaItem.fromUri(Uri.parse(ss.rtsp(s.ch, hd)))));
        p.prepare();
        p.play();
    }

    private void restart(int ch) {
        for (int i = 0; i < streams.size(); i++) if (streams.get(i).ch == ch) start(players.get(i), streams.get(i));
    }

    private void stopPlayers() {
        for (ExoPlayer p : players) p.release();
        players.clear();
    }

    @Override protected void onDestroy() {
        stopPlayers();
        Session s = session;
        if (s != null) new Thread(s::close).start();
        super.onDestroy();
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
        renderer.big = -1;
        renderer.zoom = 1;
        relayout();
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

        Overlay(Context c) {
            super(c);
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
                    if (i < 0) { askSettings("Ajustes"); return; }
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
            sgd.onTouchEvent(e);
            gd.onTouchEvent(e);
            if ((e.getActionMasked() == MotionEvent.ACTION_UP || e.getActionMasked() == MotionEvent.ACTION_CANCEL) && ptzDrag) {
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
            for (int i = 0; i < tiles.size(); i++) {
                CamRenderer.Tile t = tiles.get(i);
                CamRenderer.Stream s = streams.get(t.stream);
                RectF r = t.rect;
                boolean small = renderer.big >= 0 && i != renderer.big;
                if (!s.playing) { text.setTextSize(small ? 18 : 30); c.drawText(t.name + "  (conectando...)", r.left + 12, r.top + 40, text); }
                if (hd.contains(s.ch) && !small) {
                    tag.setTextSize(34);
                    float tw = tag.measureText("HD");
                    c.drawRect(r.left + 24, r.top + 20, r.left + 24 + tw + 20, r.top + 66, box);
                    c.drawText("HD", r.left + 34, r.top + 56, tag);
                }
            }
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
            @Override public void onPageFinished(android.webkit.WebView v, String u) {
                // open the calibration right away (the page's "Calibrar" button)
                ui.postDelayed(() -> v.evaluateJavascript("document.getElementById('calbtn').click()", null), 1500);
            }
        });
        ((FrameLayout) gl.getParent()).addView(wv);
        calView = wv;
        wv.loadUrl(url);
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
        new AlertDialog.Builder(this).setTitle(t.name).setItems(items.toArray(new String[0]), (d, which) -> {
            if (which == 0) {
                Set<Integer> s = hdSet();
                if (hd) s.remove(ch); else s.add(ch);
                StringBuilder sb = new StringBuilder();
                for (int x : s) sb.append(sb.length() > 0 ? "," : "").append(x);
                prefs.edit().putString("hd", sb.toString()).apply();
                restart(ch);
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
}
