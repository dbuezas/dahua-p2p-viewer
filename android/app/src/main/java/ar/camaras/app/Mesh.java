package ar.camaras.app;

import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** The phone's panorama calibration ("P3" file, panoramica3_<ch>.bin), as in app/mesh.go. */
final class Mesh {
    int ow, oh, nx, ny, step, band;
    boolean aLeft;
    float[] m;    // nx*ny nodes x (lens A x,y, lens B x,y), parts of the lens size; NaN = behind the lens
    float[] seam; // per output row, part of the width; -1 = none

    static Mesh parse(byte[] b) {
        if (b == null || b.length < 6 || b[0] != 'P' || b[1] != '3') return null;
        ByteBuffer bb = ByteBuffer.wrap(b).order(ByteOrder.LITTLE_ENDIAN);
        int hl = bb.getInt(2);
        if (6 + hl > b.length) return null;
        String head = new String(b, 6, hl);
        Mesh r = new Mesh();
        r.ow = num(head, "OW"); r.oh = num(head, "OH"); r.nx = num(head, "NX"); r.ny = num(head, "NY");
        r.step = num(head, "step"); r.band = Math.max(0, num(head, "band"));
        r.aLeft = head.contains("\"aLeft\":true");
        if (r.ow < 2 || r.oh < 2 || r.nx < 2 || r.ny < 2 || r.step < 1) return null;
        int off = 6 + hl;
        off += (4 - off % 4) % 4;
        int nm = r.nx * r.ny * 4;
        if (b.length < off + (nm + r.oh) * 4) return null;
        r.m = new float[nm];
        for (int i = 0; i < nm; i++) r.m[i] = bb.getFloat(off + i * 4);
        int o2 = off + nm * 4;
        r.seam = new float[r.oh];
        for (int i = 0; i < r.oh; i++) r.seam[i] = bb.getFloat(o2 + i * 4);
        return r;
    }

    private static int num(String head, String key) {
        Matcher mt = Pattern.compile("\"" + key + "\":(-?[0-9.]+)").matcher(head);
        return mt.find() ? (int) Double.parseDouble(mt.group(1)) : 0;
    }

    /** 16-bit packing used by the shader: (v+1)/3 of 65534; 65535 = none. */
    static void enc16(float v, byte[] out, int o) {
        if (Float.isNaN(v) || v < -1 || v > 2) { out[o] = (byte) 0xff; out[o + 1] = (byte) 0xff; return; }
        int n = Math.round((v + 1) / 3 * 65534);
        out[o] = (byte) (n >> 8); out[o + 1] = (byte) n;
    }

    /** RGBA bytes of the node texture (2*nx x ny): texel (2ix+k, iy) = lens k's x (r,g) and y (b,a). */
    byte[] nodeBytes() {
        byte[] t = new byte[nx * 2 * ny * 4];
        for (int iy = 0; iy < ny; iy++)
            for (int ix = 0; ix < nx; ix++)
                for (int k = 0; k < 2; k++) {
                    int i = (iy * nx + ix) * 4, o = (iy * nx * 2 + ix * 2 + k) * 4;
                    enc16(m[i + k * 2], t, o);
                    enc16(m[i + k * 2 + 1], t, o + 2);
                }
        return t;
    }

    /** RGBA bytes of the seam texture (oh x 1). */
    byte[] seamBytes() {
        byte[] t = new byte[oh * 4];
        for (int y = 0; y < oh; y++) {
            if (seam[y] < 0) { t[y * 4] = (byte) 0xff; t[y * 4 + 1] = (byte) 0xff; }
            else enc16(seam[y], t, y * 4);
            t[y * 4 + 3] = (byte) 0xff;
        }
        return t;
    }
}
