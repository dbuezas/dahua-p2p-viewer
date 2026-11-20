package ar.camaras.app;

import android.graphics.RectF;
import android.graphics.SurfaceTexture;
import android.opengl.GLES11Ext;
import android.opengl.GLES20;
import android.opengl.GLSurfaceView;
import android.view.Surface;

import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.FloatBuffer;
import java.util.ArrayList;
import java.util.List;

import javax.microedition.khronos.egl.EGLConfig;
import javax.microedition.khronos.opengles.GL10;

/**
 * Draws every camera on one GL surface, like the desktop app: the decoder of each
 * stream writes into its own texture; the tiles show a whole picture, the top or
 * bottom half of a split camera, or its panorama (the phone's mesh, the same math
 * as the desktop shader in app/gpu.go). Zoom = a crop of the picture.
 */
final class CamRenderer implements GLSurfaceView.Renderer {

    static final class Stream {
        final int ch;
        int tex;
        SurfaceTexture st;
        Surface surface;
        final float[] mtx = new float[16];
        volatile boolean fresh;
        volatile int vw = 16, vh = 9;
        volatile boolean playing;
        volatile boolean software; // its hardware decoder failed: use the software one
        volatile int frames;       // frames drawn into the texture (for the log)
        volatile Mesh mesh;
        Mesh uploaded;
        int meshTex, seamTex;
        Stream(int ch) { this.ch = ch; }
    }

    static final class Tile {
        int stream;
        int half;        // 0 whole, 1 top, 2 bottom, 3 panorama
        String name;
        volatile RectF rect = new RectF();
        Tile(int stream, int half, String name) { this.stream = stream; this.half = half; this.name = name; }
    }

    interface Ready { void onSurfaces(List<Stream> streams); }

    private final GLSurfaceView view;
    volatile List<Stream> streams = new ArrayList<>();
    volatile List<Tile> tiles = new ArrayList<>();
    volatile int big = -1;
    volatile float zoom = 1, cx = 0.5f, cy = 0.5f; // digital zoom of the big tile
    private int w = 1, h = 1;
    private int progQuad, progPano;
    private final FloatBuffer vb = ByteBuffer.allocateDirect(4 * 4 * 4).order(ByteOrder.nativeOrder()).asFloatBuffer();

    CamRenderer(GLSurfaceView v) { view = v; }

    /** Makes one texture + surface per channel on the GL thread, then calls back. */
    void makeStreams(final int[] chans, final Ready ready) {
        view.queueEvent(() -> {
            List<Stream> list = new ArrayList<>();
            int[] t = new int[chans.length];
            GLES20.glGenTextures(chans.length, t, 0);
            for (int i = 0; i < chans.length; i++) {
                Stream s = new Stream(chans[i]);
                s.tex = t[i];
                GLES20.glBindTexture(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, s.tex);
                GLES20.glTexParameteri(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, GLES20.GL_TEXTURE_MIN_FILTER, GLES20.GL_LINEAR);
                GLES20.glTexParameteri(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, GLES20.GL_TEXTURE_MAG_FILTER, GLES20.GL_LINEAR);
                GLES20.glTexParameteri(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, GLES20.GL_TEXTURE_WRAP_S, GLES20.GL_CLAMP_TO_EDGE);
                GLES20.glTexParameteri(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, GLES20.GL_TEXTURE_WRAP_T, GLES20.GL_CLAMP_TO_EDGE);
                s.st = new SurfaceTexture(s.tex);
                s.st.setOnFrameAvailableListener(st -> { s.fresh = true; view.requestRender(); });
                s.surface = new Surface(s.st);
                android.opengl.Matrix.setIdentityM(s.mtx, 0);
                list.add(s);
            }
            streams = list;
            view.post(() -> ready.onSurfaces(list));
        });
    }

    @Override public void onSurfaceCreated(GL10 gl, EGLConfig cfg) {
        progQuad = program(VS, FS_QUAD);
        progPano = program(VS, FS_PANO);
    }

    @Override public void onSurfaceChanged(GL10 gl, int width, int height) {
        w = width; h = height;
        GLES20.glViewport(0, 0, w, h);
    }

    @Override public void onDrawFrame(GL10 gl) {
        for (Stream s : streams) {
            if (s.fresh) {
                s.fresh = false;
                s.st.updateTexImage();
                s.frames++;
                s.st.getTransformMatrix(s.mtx);
                s.playing = true;
            }
        }
        GLES20.glClearColor(0, 0, 0, 1);
        GLES20.glClear(GLES20.GL_COLOR_BUFFER_BIT);
        List<Tile> ts = tiles;
        int b = big;
        if (b >= 0 && b < ts.size()) draw(ts.get(b), true);
        for (int i = 0; i < ts.size(); i++) if (i != b) draw(ts.get(i), false);
    }

    /** Where the picture goes inside its tile: whole cameras fill it, halves and panoramas keep their shape. */
    RectF fitted(Tile t) {
        RectF r = t.rect;
        if (t.half == 0 || t.stream >= streams.size()) return r;
        Stream s = streams.get(t.stream);
        float aspect = t.half == 3 && s.mesh != null ? (float) s.mesh.ow / s.mesh.oh : s.vw / (s.vh / 2f);
        float rw = r.width(), rh = r.height();
        if (rw / rh > aspect) { float nw = rh * aspect; return new RectF(r.centerX() - nw / 2, r.top, r.centerX() + nw / 2, r.bottom); }
        float nh = rw / aspect;
        return new RectF(r.left, r.centerY() - nh / 2, r.right, r.centerY() + nh / 2);
    }

    private void draw(Tile t, boolean isBig) {
        if (t.stream >= streams.size()) return;
        Stream s = streams.get(t.stream);
        if (!s.playing) return;
        RectF r = fitted(t);
        float u0 = 0, u1 = 1, y0 = 0, y1 = 1; // the part of the picture shown, 0..1 from the top left
        if (isBig && zoom > 1.01f) {
            // zoomed: the picture grows past its square and fills the whole screen,
            // the point being looked at (cx, cy) in the middle
            float pw = r.width() * zoom, ph = r.height() * zoom;
            float x = w / 2f - cx * pw, y = h / 2f - cy * ph;
            r = new RectF(x, y, x + pw, y + ph);
        }
        if (t.half == 3 && s.mesh != null) { drawPano(s, r, u0, u1, y0, y1); return; }
        // picture rows of this tile (GL texture coords: v from the bottom)
        float vTop = 1, vBot = 0;
        if (t.half == 1) vBot = 0.5f;
        if (t.half == 2) vTop = 0.5f;
        float va = vTop - (vTop - vBot) * y0, vb2 = vTop - (vTop - vBot) * y1;
        GLES20.glUseProgram(progQuad);
        quad(progQuad, r, u0, va, u1, vb2);
        GLES20.glActiveTexture(GLES20.GL_TEXTURE0);
        GLES20.glBindTexture(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, s.tex);
        GLES20.glUniform1i(GLES20.glGetUniformLocation(progQuad, "uFrame"), 0);
        GLES20.glUniformMatrix4fv(GLES20.glGetUniformLocation(progQuad, "uTex"), 1, false, s.mtx, 0);
        GLES20.glDrawArrays(GLES20.GL_TRIANGLE_STRIP, 0, 4);
    }

    private void drawPano(Stream s, RectF r, float u0, float u1, float y0, float y1) {
        Mesh m = s.mesh;
        if (s.uploaded != m) {
            if (s.meshTex == 0) { int[] t = new int[2]; GLES20.glGenTextures(2, t, 0); s.meshTex = t[0]; s.seamTex = t[1]; }
            upload(s.meshTex, m.nx * 2, m.ny, m.nodeBytes());
            upload(s.seamTex, m.oh, 1, m.seamBytes());
            s.uploaded = m;
        }
        int ow = Math.min(m.ow, 1600), oh = m.oh * ow / m.ow;
        float sx = (float) m.ow / ow;
        GLES20.glUseProgram(progPano);
        // aUV here = panorama coords 0..1 from the top left (cropped by the zoom)
        quad(progPano, r, u0, y0, u1, y1);
        int p = progPano;
        GLES20.glActiveTexture(GLES20.GL_TEXTURE0);
        GLES20.glBindTexture(GLES11Ext.GL_TEXTURE_EXTERNAL_OES, s.tex);
        GLES20.glUniform1i(GLES20.glGetUniformLocation(p, "uFrame"), 0);
        GLES20.glActiveTexture(GLES20.GL_TEXTURE1);
        GLES20.glBindTexture(GLES20.GL_TEXTURE_2D, s.meshTex);
        GLES20.glUniform1i(GLES20.glGetUniformLocation(p, "uMesh"), 1);
        GLES20.glActiveTexture(GLES20.GL_TEXTURE2);
        GLES20.glBindTexture(GLES20.GL_TEXTURE_2D, s.seamTex);
        GLES20.glUniform1i(GLES20.glGetUniformLocation(p, "uSeam"), 2);
        GLES20.glUniformMatrix4fv(GLES20.glGetUniformLocation(p, "uTex"), 1, false, s.mtx, 0);
        GLES20.glUniform2f(GLES20.glGetUniformLocation(p, "uOut"), ow, oh);
        GLES20.glUniform2f(GLES20.glGetUniformLocation(p, "uOSize"), m.ow, m.oh);
        GLES20.glUniform3f(GLES20.glGetUniformLocation(p, "uGrid"), m.nx, m.ny, m.step);
        GLES20.glUniform1f(GLES20.glGetUniformLocation(p, "uBand"), Math.max(2f, m.band / sx));
        GLES20.glUniform1f(GLES20.glGetUniformLocation(p, "uALeft"), m.aLeft ? 1 : 0);
        GLES20.glDrawArrays(GLES20.GL_TRIANGLE_STRIP, 0, 4);
        GLES20.glActiveTexture(GLES20.GL_TEXTURE0);
    }

    private static void upload(int tex, int tw, int th, byte[] rgba) {
        GLES20.glBindTexture(GLES20.GL_TEXTURE_2D, tex);
        GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_MIN_FILTER, GLES20.GL_NEAREST);
        GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_MAG_FILTER, GLES20.GL_NEAREST);
        GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_WRAP_S, GLES20.GL_CLAMP_TO_EDGE);
        GLES20.glTexParameteri(GLES20.GL_TEXTURE_2D, GLES20.GL_TEXTURE_WRAP_T, GLES20.GL_CLAMP_TO_EDGE);
        GLES20.glPixelStorei(GLES20.GL_UNPACK_ALIGNMENT, 1);
        GLES20.glTexImage2D(GLES20.GL_TEXTURE_2D, 0, GLES20.GL_RGBA, tw, th, 0, GLES20.GL_RGBA, GLES20.GL_UNSIGNED_BYTE, ByteBuffer.wrap(rgba));
    }

    /** A rectangle (screen pixels) with texture coords (ua,va) at its top left and (ub,vb) at its bottom right. */
    private void quad(int prog, RectF r, float ua, float va, float ub, float vb2) {
        float x0 = 2 * r.left / w - 1, x1 = 2 * r.right / w - 1, yt = 1 - 2 * r.top / h, yb = 1 - 2 * r.bottom / h;
        vb.clear();
        vb.put(new float[]{x0, yb, ua, vb2, x1, yb, ub, vb2, x0, yt, ua, va, x1, yt, ub, va});
        vb.position(0);
        int aPos = GLES20.glGetAttribLocation(prog, "aPos"), aUV = GLES20.glGetAttribLocation(prog, "aUV");
        GLES20.glEnableVertexAttribArray(aPos);
        GLES20.glVertexAttribPointer(aPos, 2, GLES20.GL_FLOAT, false, 16, vb);
        vb.position(2);
        GLES20.glEnableVertexAttribArray(aUV);
        GLES20.glVertexAttribPointer(aUV, 2, GLES20.GL_FLOAT, false, 16, vb);
        vb.position(0);
    }

    private static int program(String vs, String fs) {
        int p = GLES20.glCreateProgram();
        GLES20.glAttachShader(p, shader(GLES20.GL_VERTEX_SHADER, vs));
        GLES20.glAttachShader(p, shader(GLES20.GL_FRAGMENT_SHADER, fs));
        GLES20.glLinkProgram(p);
        int[] ok = new int[1];
        GLES20.glGetProgramiv(p, GLES20.GL_LINK_STATUS, ok, 0);
        if (ok[0] == 0) android.util.Log.e("camaras", "shader link: " + GLES20.glGetProgramInfoLog(p));
        return p;
    }

    private static int shader(int type, String src) {
        int s = GLES20.glCreateShader(type);
        GLES20.glShaderSource(s, src);
        GLES20.glCompileShader(s);
        int[] ok = new int[1];
        GLES20.glGetShaderiv(s, GLES20.GL_COMPILE_STATUS, ok, 0);
        if (ok[0] == 0) android.util.Log.e("camaras", "shader: " + GLES20.glGetShaderInfoLog(s));
        return s;
    }

    private static final String VS =
            "attribute vec2 aPos; attribute vec2 aUV; varying vec2 vUV;\n" +
            "void main() { vUV = aUV; gl_Position = vec4(aPos, 0.0, 1.0); }\n";

    private static final String FS_QUAD =
            "#extension GL_OES_EGL_image_external : require\n" +
            "precision mediump float; uniform samplerExternalOES uFrame; uniform mat4 uTex; varying vec2 vUV;\n" +
            "void main() { gl_FragColor = texture2D(uFrame, (uTex * vec4(vUV, 0.0, 1.0)).xy); }\n";

    /** The desktop panoKage (app/gpu.go) in GLSL ES. vUV = panorama point, 0..1 from the top left. */
    private static final String FS_PANO =
            "#extension GL_OES_EGL_image_external : require\n" +
            "precision highp float;\n" +
            "uniform samplerExternalOES uFrame; uniform mat4 uTex;\n" +
            "uniform sampler2D uMesh; uniform sampler2D uSeam;\n" +
            "uniform vec2 uOut; uniform vec2 uOSize; uniform vec3 uGrid; uniform float uBand; uniform float uALeft;\n" +
            "varying vec2 vUV;\n" +
            "float n16(vec2 v) { return v.x * 255.0 * 256.0 + v.y * 255.0; }\n" +
            "vec3 node(float ix, float iy, float k) {\n" +
            "  vec4 t = texture2D(uMesh, (vec2(ix * 2.0 + k, iy) + 0.5) / vec2(uGrid.x * 2.0, uGrid.y));\n" +
            "  float x = n16(t.rg); float y = n16(t.ba);\n" +
            "  if (x > 65534.5 || y > 65534.5) return vec3(0.0);\n" +
            "  return vec3(x / 65534.0 * 3.0 - 1.0, y / 65534.0 * 3.0 - 1.0, 1.0);\n" +
            "}\n" +
            "vec3 meshAt(vec2 o, float k) {\n" +
            "  float gx = clamp(o.x / uGrid.z, 0.0, uGrid.x - 1.0001); float gy = clamp(o.y / uGrid.z, 0.0, uGrid.y - 1.0001);\n" +
            "  float x0 = floor(gx); float y0 = floor(gy); float fx = gx - x0; float fy = gy - y0;\n" +
            "  vec3 a = node(x0, y0, k); vec3 b = node(x0 + 1.0, y0, k); vec3 c = node(x0, y0 + 1.0, k); vec3 d = node(x0 + 1.0, y0 + 1.0, k);\n" +
            "  if (a.z * b.z * c.z * d.z < 0.5) return vec3(0.0);\n" +
            "  return vec3((a.xy * (1.0 - fx) + b.xy * fx) * (1.0 - fy) + (c.xy * (1.0 - fx) + d.xy * fx) * fy, 1.0);\n" +
            "}\n" +
            "vec4 lens(vec3 m, float bottom) {\n" +
            "  if (m.z < 0.5 || m.x < 0.0 || m.x >= 1.0 || m.y < 0.0 || m.y >= 1.0) return vec4(0.0);\n" +
            "  float v = bottom > 0.5 ? 0.5 - m.y * 0.5 : 1.0 - m.y * 0.5;\n" +
            "  return vec4(texture2D(uFrame, (uTex * vec4(m.x, v, 0.0, 1.0)).xy).rgb, 1.0);\n" +
            "}\n" +
            "void pair(float px, float oy, float sx, out vec4 a, out vec4 b) {\n" +
            "  vec2 o = vec2((px + 0.5) * sx, oy);\n" +
            "  a = lens(meshAt(o, 0.0), 0.0); b = lens(meshAt(o, 1.0), 1.0);\n" +
            "  if (a.a < 0.5) a = b; if (b.a < 0.5) b = a;\n" +
            "}\n" +
            "void main() {\n" +
            "  vec2 p = floor(vUV * uOut);\n" +
            "  float sx = uOSize.x / uOut.x; float sy = uOSize.y / uOut.y;\n" +
            "  float oy = (p.y + 0.5) * sy; float row = min(uOSize.y - 1.0, floor(oy));\n" +
            "  vec4 st = texture2D(uSeam, vec2((row + 0.5) / uOSize.y, 0.5));\n" +
            "  float sv = n16(st.rg); bool hasSeam = sv < 65534.5; float seam = sv / 65534.0 * 3.0 - 1.0;\n" +
            "  vec2 o = vec2((p.x + 0.5) * sx, oy);\n" +
            "  vec4 a = lens(meshAt(o, 0.0), 0.0); vec4 b = lens(meshAt(o, 1.0), 1.0);\n" +
            "  if (a.a < 0.5 && b.a < 0.5) { gl_FragColor = vec4(0.0, 0.0, 0.0, 1.0); return; }\n" +
            "  float w = 0.5; float seamX = 0.0;\n" +
            "  if (hasSeam) { seamX = seam * uOSize.x / sx; float d = ((o.x - seam * uOSize.x) / sx) / uBand; w = uALeft > 0.5 ? 0.5 + d : 0.5 - d; }\n" +
            "  if (a.a < 0.5) w = 1.0; if (b.a < 0.5) w = 0.0; w = clamp(w, 0.0, 1.0);\n" +
            "  vec3 outc = a.rgb * (1.0 - w) + b.rgb * w;\n" +
            "  float x0 = max(floor(seamX - uBand), 0.0); float x1 = min(floor(seamX + uBand), uOut.x);\n" +
            "  if (hasSeam && p.x >= x0 && p.x < x1 && x1 - x0 >= 12.0) {\n" +
            "    vec4 A; vec4 B; pair(p.x, oy, sx, A, B);\n" +
            "    vec3 sa = vec3(0.0); vec3 sb = vec3(0.0); float cnt = 0.0;\n" +
            "    for (int i = -6; i <= 6; i++) { float q = p.x + float(i); if (q >= x0 && q < x1) { vec4 qa; vec4 qb; pair(q, oy, sx, qa, qb); sa += qa.rgb; sb += qb.rgb; cnt += 1.0; } }\n" +
            "    vec3 ba = sa / cnt; vec3 bb = sb / cnt; float sgn = uALeft > 0.5 ? 1.0 : -1.0;\n" +
            "    float dd = p.x + 0.5 - seamX; float wW = clamp(0.5 + sgn * dd / uBand, 0.0, 1.0); float wN = clamp(0.5 + sgn * dd / 6.0, 0.0, 1.0);\n" +
            "    outc = ba * (1.0 - wW) + bb * wW + (A.rgb - ba) * (1.0 - wN) + (B.rgb - bb) * wN;\n" +
            "  }\n" +
            "  gl_FragColor = vec4(clamp(outc, 0.0, 1.0), 1.0);\n" +
            "}\n";
}
