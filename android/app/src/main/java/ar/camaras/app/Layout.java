package ar.camaras.app;

import android.graphics.RectF;

/** The desktop app's layout (app/layout.go): same rules as the old PC app. */
final class Layout {
    private Layout() {}

    /** Places pictures u[i] cells wide (1, or 2 for a panorama) to cover as much of W x H as possible. */
    static RectF[] arrange(int[] u, float W, float H) {
        int n = u.length;
        if (n == 0) return new RectF[0];
        int total = 0, widest = 1;
        for (int x : u) { total += x; widest = Math.max(widest, x); }
        RectF[] best = null;
        double bestArea = -1;
        for (int c = widest; c <= total; c++) {
            int[] row = new int[n], col = new int[n];
            int rr = 0, used = 0;
            for (int i = 0; i < n; i++) {
                if (used + u[i] > c) { rr++; used = 0; }
                row[i] = rr; col[i] = used; used += u[i];
            }
            int rows = rr + 1;
            double sc = Math.min(W / c / 16.0, H / rows / 9.0);
            float w = (float) (sc * 16), h = (float) (sc * 9);
            float x0 = (W - c * w) / 2, y0 = (H - rows * h) / 2;
            RectF[] list = new RectF[n];
            double a = 0;
            for (int i = 0; i < n; i++) {
                float x = x0 + col[i] * w, y = y0 + row[i] * h;
                list[i] = new RectF(x, y, x + u[i] * w, y + h);
                a += list[i].width() * list[i].height();
            }
            if (a > bestArea + 1) { bestArea = a; best = list; }
        }
        for (int r = 1; r <= n; r++) {
            int baseK = n / r, extra = n % r;
            int[] counts = new int[r];
            for (int i = 0; i < r; i++) counts[i] = baseK + (i >= r - extra ? 1 : 0);
            int[] rowUnits = new int[r];
            int k0 = 0;
            for (int i = 0; i < r; i++) {
                for (int j = 0; j < counts[i]; j++) rowUnits[i] += u[k0 + j];
                k0 += counts[i];
            }
            double sum = 0;
            boolean bad = false;
            for (int ru : rowUnits) { if (ru == 0) { bad = true; break; } sum += W / ru * 9.0 / 16; }
            if (bad) continue;
            double f = Math.min(1, H / sum);
            double y = (H - sum * f) / 2;
            RectF[] list = new RectF[n];
            double a = 0;
            k0 = 0;
            for (int i = 0; i < r; i++) {
                double cw = f * W / rowUnits[i], h = cw * 9 / 16;
                double x = (W - rowUnits[i] * cw) / 2;
                for (int j = 0; j < counts[i]; j++) {
                    int k = k0 + j;
                    list[k] = new RectF((float) x, (float) y, (float) (x + u[k] * cw), (float) (y + h));
                    a += list[k].width() * list[k].height();
                    x += u[k] * cw;
                }
                k0 += counts[i];
                y += h;
            }
            if (a > bestArea + 1) { bestArea = a; best = list; }
        }
        return best;
    }

    /** One camera big; the others float over its bottom edge as small pictures. */
    static RectF[] arrangeBig(int n, int big, float W, float H) {
        float fs = Math.min(W / 16, H / 9), gw = fs * 16, gh = fs * 9;
        float x0 = (W - gw) / 2, y0 = (H - gh) / 2;
        int m = n - 1;
        float tw = m > 0 ? Math.min(W * 0.10f, (gw - 20) / m) : 0;
        float th = tw * 9 / 16, gap = 10;
        float tx = (W - m * tw) / 2, ty = y0 + gh - th - gap;
        RectF[] list = new RectF[n];
        int k = 0;
        for (int i = 0; i < n; i++) {
            if (i == big) { list[i] = new RectF(x0, y0, x0 + gw, y0 + gh); continue; }
            float x = tx + k * tw;
            list[i] = new RectF(x, ty, x + tw, ty + th);
            k++;
        }
        return list;
    }
}
