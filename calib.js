// Panorama calibration of the split camera (lens A on top, fixed; lens B below it on a PTZ motor), as two real
// cameras 9 cm apart. Runs on the phone in a Web Worker (the remote control page starts it) and in bun for tests.
//
// Fixed for the camera: lens shape (F, K), how lens A sits on the body (tilt tA), the 9 cm between the lenses.
// Per calibration: pan and tilt of lens B (the PTZ motor moves it), and one number per grid point: the inverse
// distance w (1/metres) of what is there. Nothing else can move: a point can only slide along its line.
//
// Steps (each one sends pictures to the page, so the phone shows the work as it goes):
//  1. Rough pan/tilt: try many, compare edge pictures of both lenses where they overlap.
//  2. Shared corners: corners of B, each searched in A near where the rough pan/tilt puts it.
//  3. Fine pan/tilt: each pair may sit at any distance, so it only has to fall on its line.
//  4. Distances: for grid points of A's view, search along B's line; clean; fill smoothly.
//  5. Frame: how much to cut, from everything (black corners) to only picture (straight cut).
//  6. Seam: a path through the overlap where both lenses look most alike.
//  7. Mesh: for a grid of output points, which pixel of A and of B to take. The PC only draws this mesh.
// Lens pixel units: a lens picture is 640x360 (the PC scales to its frame size).
'use strict';
var Calib = (function () {
  var LW = 640, LH = 360;
  // The camera itself (same for every calibration): lens shape, how lens A sits on the body (tilt tA), and where
  // lens B is from lens A: 9 cm (only scales the distances, the picture does not change with it), leaned ax
  // sideways and az forward from straight down (0: straight down). Found once from three pictures.
  var CAM = { F: 671.58, K: -0.2496, tA: -0.02703, base: 0.09, ax: 0, az: 0 };
  function mul(a, b) { var r = [0, 0, 0, 0, 0, 0, 0, 0, 0]; for (var i = 0; i < 3; i++) for (var j = 0; j < 3; j++) for (var k = 0; k < 3; k++) r[i * 3 + j] += a[i * 3 + k] * b[k * 3 + j]; return r; }
  function tr(a) { return [a[0], a[3], a[6], a[1], a[4], a[7], a[2], a[5], a[8]]; }
  // Rz(roll) * Rx(pitch) * Ry(yaw), like the PC's Lens
  function rot(yaw, pitch, roll) {
    var cy = Math.cos(yaw), sy = Math.sin(yaw), cp = Math.cos(pitch), sp = Math.sin(pitch), cr = Math.cos(roll), sr = Math.sin(roll);
    return mul([cr, -sr, 0, sr, cr, 0, 0, 0, 1], mul([1, 0, 0, 0, cp, -sp, 0, sp, cp], [cy, 0, sy, 0, 1, 0, -sy, 0, cy]));
  }
  // ray -> pixel; false when behind the lens
  function project(L, x, y, z, q) {
    var len = Math.sqrt(x * x + y * y + z * z); if (z < 0.05 * len) return false;
    var xy = Math.sqrt(x * x + y * y); if (xy < 1e-12) { q[0] = LW / 2; q[1] = LH / 2; return true; }
    var t = Math.acos(Math.min(1, z / len)), rd = L.F * (t + L.K * t * t * t);
    q[0] = LW / 2 + x / xy * rd; q[1] = LH / 2 + y / xy * rd; return true;
  }
  function unproject(L, px, py, r) {
    var u = px - LW / 2, v = py - LH / 2, rd = Math.sqrt(u * u + v * v) / L.F;
    if (rd < 1e-9) { r[0] = 0; r[1] = 0; r[2] = 1; return; }
    var t = rd; for (var i = 0; i < 8; i++) { var g = t + L.K * t * t * t - rd, d = 1 + 3 * L.K * t * t; t -= g / d; }
    var sn = Math.sin(t) / (rd * L.F); r[0] = u * sn; r[1] = v * sn; r[2] = Math.cos(t);
  }
  // The rig: R turns a ray seen by A into the same direction seen by B; h = half of B's centre seen from A (metres).
  function Rig(o, pan, tilt) {
    var MA = rot(0, o.tA, 0);
    var R = mul(rot(0, tilt, 0), mul(rot(pan, 0, 0), tr(MA)));
    var ax = o.ax || 0, az = o.az || 0, d = [Math.sin(ax), Math.cos(ax) * Math.cos(az), Math.sin(az)];
    var h = [0, 1, 2].map(function (i) { return (MA[i * 3] * d[0] + MA[i * 3 + 1] * d[1] + MA[i * 3 + 2] * d[2]) * o.base / 2; });
    return { L: { F: o.F, K: o.K }, R: R, h: h, pan: pan, tilt: tilt };
  }
  function inside(q) { return q[0] >= 0 && q[1] >= 0 && q[0] < LW - 1 && q[1] < LH - 1; }
  // From the point between the lenses, ray r, inverse distance w: A sees r + h*w, B sees R(r - h*w).
  function toA(g, r, w, q) { return project(g.L, r[0] + g.h[0] * w, r[1] + g.h[1] * w, r[2] + g.h[2] * w, q) && inside(q); }
  function toB(g, r, w, q) {
    var x = r[0] - g.h[0] * w, y = r[1] - g.h[1] * w, z = r[2] - g.h[2] * w, R = g.R;
    return project(g.L, R[0] * x + R[1] * y + R[2] * z, R[3] * x + R[4] * y + R[5] * z, R[6] * x + R[7] * y + R[8] * z, q) && inside(q);
  }
  function bil(img, w, h, x, y) {
    if (!(x >= 0 && y >= 0 && x < w - 1 && y < h - 1)) return NaN;
    var x0 = x | 0, y0 = y | 0, fx = x - x0, fy = y - y0, k = y0 * w + x0;
    return (img[k] * (1 - fx) + img[k + 1] * fx) * (1 - fy) + (img[k + w] * (1 - fx) + img[k + w + 1] * fx) * fy;
  }
  function shrink(img, w, h, f) {
    var w2 = (w / f) | 0, h2 = (h / f) | 0, o = new Float32Array(w2 * h2);
    for (var y = 0; y < h2; y++) for (var x = 0; x < w2; x++) { var s = 0; for (var j = 0; j < f; j++) for (var i = 0; i < f; i++) s += img[(y * f + j) * w + x * f + i]; o[y * w2 + x] = s / (f * f); }
    return o;
  }
  function gradMag(img, w, h) {
    var o = new Float32Array(w * h);
    for (var y = 1; y < h - 1; y++) for (var x = 1; x < w - 1; x++) { var k = y * w + x, gx = img[k + 1] - img[k - 1], gy = img[k + w] - img[k - w]; o[k] = Math.sqrt(gx * gx + gy * gy); }
    return o;
  }
  function blur3(img, w, h) {
    var o = new Float32Array(w * h);
    for (var y = 1; y < h - 1; y++) for (var x = 1; x < w - 1; x++) { var s = 0; for (var j = -1; j <= 1; j++) for (var i = -1; i <= 1; i++) s += img[(y + j) * w + x + i]; o[y * w + x] = s / 9; }
    return o;
  }
  function ncc(I, J, w, h, ax, ay, bx, by, P) {
    if (ax < P || ay < P || bx < P || by < P || ax >= w - P || ay >= h - P || bx >= w - P || by >= h - P) return -2;
    var sa = 0, sb = 0, saa = 0, sbb = 0, sab = 0, n = 0;
    for (var j = -P; j <= P; j++) for (var i = -P; i <= P; i++) { var a = I[(ay + j) * w + ax + i], b = J[(by + j) * w + bx + i]; sa += a; sb += b; saa += a * a; sbb += b * b; sab += a * b; n++; }
    var va = saa / n - (sa / n) * (sa / n), vb = sbb / n - (sb / n) * (sb / n);
    return va > 4 && vb > 4 ? (sab / n - sa / n * sb / n) / Math.sqrt(va * vb) : -1;
  }
  function nelder(fn, x0, st, iters, each) {
    var n = x0.length, pts = [x0.slice()], i, j;
    for (i = 0; i < n; i++) { var p = x0.slice(); p[i] += st[i]; pts.push(p); }
    var vals = pts.map(fn);
    for (var it = 0; it < iters; it++) {
      var ord = vals.map(function (v, k) { return k; }).sort(function (a, b) { return vals[a] - vals[b]; });
      pts = ord.map(function (k) { return pts[k]; }); vals = ord.map(function (k) { return vals[k]; });
      if (each) each(it, pts[0], vals[0]);
      var c = new Array(n).fill(0); for (i = 0; i < n; i++) for (j = 0; j < n; j++) c[j] += pts[i][j] / n;
      var at = function (t) { return c.map(function (v, k) { return v + t * (pts[n][k] - v); }); };
      var xr = at(-1), fr = fn(xr);
      if (fr < vals[0]) { var xe = at(-2), fe = fn(xe); if (fe < fr) { pts[n] = xe; vals[n] = fe; } else { pts[n] = xr; vals[n] = fr; } }
      else if (fr < vals[n - 1]) { pts[n] = xr; vals[n] = fr; }
      else { var xc = at(0.5), fc = fn(xc); if (fc < vals[n]) { pts[n] = xc; vals[n] = fc; } else for (i = 1; i <= n; i++) { pts[i] = pts[i].map(function (v, k) { return pts[0][k] + 0.5 * (v - pts[0][k]); }); vals[i] = fn(pts[i]); } }
    }
    var b = vals.indexOf(Math.min.apply(null, vals)); return { x: pts[b], v: vals[b] };
  }
  var WMAX = 1 / 0.2;   // nothing nearer than 20 cm
  // Pair a (in A), b (in B): the B ray seen from A is the line 2h*w + d. Error at the best w.
  // lam > 0: a distance also costs (lam * its shift in pixels squared), so the turn explains as much as it can and
  // the distances only what is left (with a strip this narrow, a bit more turn and a bit nearer look the same).
  function epi(g, ax, ay, bx, by, lam) {
    var rb = [0, 0, 0], q = [0, 0], R = g.R; unproject(g.L, bx, by, rb);
    var Dpx = 2 * Math.sqrt(g.h[0] * g.h[0] + g.h[1] * g.h[1] + g.h[2] * g.h[2]) * g.L.F, L2 = lam ? lam * Dpx * Dpx : 0;
    var d0 = R[0] * rb[0] + R[3] * rb[1] + R[6] * rb[2], d1 = R[1] * rb[0] + R[4] * rb[1] + R[7] * rb[2], d2 = R[2] * rb[0] + R[5] * rb[1] + R[8] * rb[2];
    var b0 = 2 * g.h[0], b1 = 2 * g.h[1], b2 = 2 * g.h[2];
    function err(w) { return project(g.L, b0 * w + d0, b1 * w + d1, b2 * w + d2, q) ? Math.hypot(q[0] - ax, q[1] - ay) : 1e4; }
    function obj(w) { var e = err(w); return e * e + L2 * w * w; }
    var bw = 0, bo = obj(0), i;
    for (i = 1; i <= 24; i++) { var w = WMAX * i / 24, ob = obj(w); if (ob < bo) { bo = ob; bw = w; } }
    var lo = Math.max(0, bw - WMAX / 24), hi = Math.min(WMAX, bw + WMAX / 24);
    for (i = 0; i < 25; i++) { var m1 = lo + (hi - lo) * 0.382, m2 = lo + (hi - lo) * 0.618; if (obj(m1) < obj(m2)) hi = m2; else lo = m1; }
    var wm = (lo + hi) / 2; if (obj(wm) > bo) wm = bw;
    // e: pixel error left; c: the whole cost (error and the price of the distance), as a pixel-like number
    return { e: err(wm), w: wm, c: Math.sqrt(obj(wm)) };
  }

  // ---- the whole calibration ----
  // st.inp: { A, B: gray Float32Array 640x360; Ac, Bc: RGBA Uint8ClampedArray 640x360 }
  // st.o: { F, K, tA, base, clip (0..1), outW } ; show(msg): progress to the page
  function run(st, show) {
    for (var kc in CAM) if (st.o[kc] === undefined) st.o[kc] = CAM[kc];
    // how the fit works (see fitRig): distances cost a little, lens shape fitted with the turn, 2 rounds of matching again
    var FIT = { lam: 0.05, joint: true, rounds: 2 };
    for (kc in FIT) if (st.o[kc] === undefined) st.o[kc] = FIT[kc];
    var inp = st.inp, o = st.o, A = inp.A, B = inp.B;
    var D = 2 * Math.PI / 360;
    // -- 1. rough pan/tilt (edge pictures, 160x90) --
    var f4 = 4, w4 = LW / f4, h4 = LH / f4;
    // Two levels: very soft edge pictures for the wide search in 2° steps (a sharp edge would be missed between
    // steps), then sharper ones to refine. The few best places of the wide search are each refined.
    var lvl = [6, 1].map(function (nb) {
      var a = gradMag(shrink(A, LW, LH, f4), w4, h4), b = gradMag(shrink(B, LW, LH, f4), w4, h4);
      for (var i = 0; i < nb; i++) { a = blur3(a, w4, h4); b = blur3(b, w4, h4); }
      return { gA: a, gB: b };
    });
    var rays = [], pix = [], r = [0, 0, 0], q = [0, 0];
    for (var y = 3; y < h4 - 3; y++) for (var x = 3; x < w4 - 3; x++) { unproject(st.o, x * f4 + 2, y * f4 + 2, r); rays.push(r.slice()); pix.push(y * w4 + x); }
    function rough(pan, tilt, L) {
      var g = Rig(o, pan, tilt), sa = 0, sb = 0, saa = 0, sbb = 0, sab = 0, n = 0, R = g.R, ga = lvl[L].gA, gb = lvl[L].gB;
      for (var i = 0; i < rays.length; i++) {
        var v = rays[i]; if (!project(g.L, R[0] * v[0] + R[1] * v[1] + R[2] * v[2], R[3] * v[0] + R[4] * v[1] + R[5] * v[2], R[6] * v[0] + R[7] * v[1] + R[8] * v[2], q)) continue;
        var b = bil(gb, w4, h4, q[0] / f4 - 0.5, q[1] / f4 - 0.5); if (b !== b || q[0] < 12 || q[1] < 12 || q[0] > LW - 12 || q[1] > LH - 12) continue;
        var a = ga[pix[i]]; sa += a; sb += b; saa += a * a; sbb += b * b; sab += a * b; n++;
      }
      if (n < 150) return -1;
      var va = saa / n - (sa / n) * (sa / n), vb = sbb / n - (sb / n) * (sb / n);
      if (va <= 0 || vb <= 0) return -1;
      // a tiny overlap fits anything by chance: the fit counts by the square root of the overlap
      return (sab / n - sa / n * sb / n) / Math.sqrt(va * vb) * Math.sqrt(n / 400);   // like a z-score: same fit over more pixels is surer
    }
    if (o.debugRough) o.debugRough(rough);
    var cand = [], best = { v: -2, pan: 0, tilt: 0 }, tops = [];
    if (o.start) {
      if (!st.viewCyl) makeView(st, Rig(o, o.start.pan, o.start.tilt));
      // The person lined the lenses up roughly by hand: search only around there (±10°), keep that place too.
      for (var pd2 = -10; pd2 <= 10; pd2++) for (var td2 = -10; td2 <= 10; td2++) {
        var pp = o.start.pan + pd2 * D, tt = o.start.tilt + td2 * D; cand.push({ v: rough(pp, tt, 0), pan: pp, tilt: tt });
      }
      cand.sort(function (a, b) { return b.v - a.v; });
      tops.push({ v: 0, pan: o.start.pan, tilt: o.start.tilt });
      cand.forEach(function (c) { if (tops.length < 5 && tops.every(function (t) { return Math.abs(t.pan - c.pan) > 3 * D || Math.abs(t.tilt - c.tilt) > 3 * D; })) tops.push(c); });
      show({ stage: 1, text: 'Afinando cerca de donde la pusiste', frac: 0.05, rig: Rig(o, o.start.pan, o.start.tilt), mix: true, viewCyl: true, hold: 400 });
    } else {
      for (var pd = -100; pd <= 100; pd += 2) {
        for (var td = -36; td <= 36; td += 2) { var v = rough(pd * D, td * D, 0); cand.push({ v: v, pan: pd * D, tilt: td * D }); if (v > best.v) best = cand[cand.length - 1]; }
        if (pd % 10 === 0) show({ stage: 1, text: 'Buscando hacia donde mira la camara 4: ' + Math.round((pd + 100) / 2) + '%', frac: (pd + 100) / 200 * 0.15, rig: Rig(o, best.pan, best.tilt), mix: true });
      }
      cand.sort(function (a, b) { return b.v - a.v; });
      cand.forEach(function (c) { if (tops.length < 10 && tops.every(function (t) { return Math.abs(t.pan - c.pan) > 5 * D || Math.abs(t.tilt - c.tilt) > 5 * D; })) tops.push(c); });
    }
    // each of the few best places, refined on the sharper pictures
    tops = tops.map(function (c0) {
      var b0 = { v: rough(c0.pan, c0.tilt, 1), pan: c0.pan, tilt: c0.tilt };
      [2, 1, 0.5, 0.25].forEach(function (s) {
        var bb = b0;
        for (var pi = -2; pi <= 2; pi++) for (var ti = -2; ti <= 2; ti++) { var pan = bb.pan + pi * s * D, tilt = bb.tilt + ti * s * D, vv = rough(pan, tilt, 1); if (vv > b0.v) b0 = { v: vv, pan: pan, tilt: tilt }; }
      });
      return b0;
    }).filter(function (t) { return o.start || t.v >= 0.5; }).sort(function (a, b) { return b.v - a.v; });
    if (!tops.length) throw new Error('las dos lentes no parecen ver la misma parte (la camara 4 mira muy lejos de la 3)');

    // -- 2. shared corners (corners of B, each searched in A near where a pan/tilt puts it) --
    var resp = new Float32Array(LW * LH), corners = [];
    for (y = 2; y < LH - 2; y++) for (x = 2; x < LW - 2; x++) {
      var sxx = 0, syy = 0, sxy = 0;
      for (var j = -1; j <= 1; j++) for (var i = -1; i <= 1; i++) { var kk = (y + j) * LW + x + i, gx = B[kk + 1] - B[kk - 1], gy = B[kk + LW] - B[kk - LW]; sxx += gx * gx; syy += gy * gy; sxy += gx * gy; }
      resp[y * LW + x] = sxx * syy - sxy * sxy - 0.05 * (sxx + syy) * (sxx + syy);
    }
    var mg = Math.round(LH * 0.05);
    for (y = mg; y < LH - mg; y++) for (x = 12; x < LW - 12; x++) {
      var rv = resp[y * LW + x]; if (rv <= 0) continue; var top = true;
      for (j = -3; j <= 3 && top; j++) for (i = -3; i <= 3; i++) if (resp[(y + j) * LW + x + i] > rv) { top = false; break; }
      if (top) corners.push([x, y, rv]);
    }
    corners.sort(function (a, b) { return b[2] - a[2]; });
    var A2 = shrink(A, LW, LH, 2), B2 = shrink(B, LW, LH, 2), w2 = LW / 2, h2 = LH / 2, Rs = Math.round(w2 * 0.09);
    function findMatches(g0, maxC, progress) {
      var cs = [], rb = [0, 0, 0], R0 = g0.R, ms = [];
      for (var k = 0; k < corners.length && cs.length < maxC; k++) {
        var c = corners[k]; unproject(g0.L, c[0], c[1], rb);
        if (!project(g0.L, R0[0] * rb[0] + R0[3] * rb[1] + R0[6] * rb[2], R0[1] * rb[0] + R0[4] * rb[1] + R0[7] * rb[2], R0[2] * rb[0] + R0[5] * rb[1] + R0[8] * rb[2], q)) continue;
        if (q[0] < 20 || q[0] > LW - 20 || q[1] < 20 || q[1] > LH - 20) continue;
        cs.push([c[0], c[1], c[2], q[0], q[1]]);
      }
      for (var ci = 0; ci < cs.length; ci++) {
        var c2 = cs[ci], px = Math.round(c2[3] / 2), py = Math.round(c2[4] / 2), bx = Math.round(c2[0] / 2), by = Math.round(c2[1] / 2);
        var bst = -2, second = -2, bax = 0, bay = 0, xx, yy, nv;
        for (yy = py - Rs; yy <= py + Rs; yy++) for (xx = px - Rs; xx <= px + Rs; xx++) {
          nv = ncc(A2, B2, w2, h2, xx, yy, bx, by, 6); var far = Math.abs(xx - bax) + Math.abs(yy - bay) > 2;
          if (nv > bst) { if (far) second = bst; bst = nv; bax = xx; bay = yy; } else if (nv > second && far) second = nv;
        }
        if (bst >= 0.75 && bst - second >= 0.03) {
          var fb = -2, fx = 0, fy = 0;
          for (yy = bay * 2 - 3; yy <= bay * 2 + 3; yy++) for (xx = bax * 2 - 3; xx <= bax * 2 + 3; xx++) { nv = ncc(A, B, LW, LH, xx, yy, c2[0], c2[1], 7); if (nv > fb) { fb = nv; fx = xx; fy = yy; } }
          if (fb > 0.8) ms.push([fx, fy, c2[0], c2[1]]);
        }
        if (progress && (ci % 60 === 59 || ci === cs.length - 1)) progress(ms, ci + 1, cs.length);
      }
      return ms;
    }
    // -- 3. fine pan/tilt: every pair may be at any distance, it only has to lie on its line --
    // Fine fit. With o.joint the lens shape (F, K) is fitted at the same time as pan and tilt, held near the
    // camera's known shape (a turn and a lens change can partly stand in for each other).
    var lam = o.lam || 0;
    function rigOf(xv) { return Rig(o.joint ? Object.assign({}, o, { F: xv[2], K: xv[3] }) : o, xv[0], xv[1]); }
    function fitRig(start, ms, iters, each) {
      function cost(xv) {
        var gg = rigOf(xv), es = ms.map(function (m) { return epi(gg, m[0], m[1], m[2], m[3], lam).c; }).sort(function (a, b) { return a - b; });
        var use = Math.max(6, Math.floor(es.length * 0.9)), e = 0;
        for (var i2 = 0; i2 < use; i2++) e += es[i2] < 2 ? es[i2] * es[i2] : 4 * es[i2] - 4;
        e /= use;
        if (o.joint) e += Math.pow((xv[2] - o.F) / 8, 2) + Math.pow((xv[3] - o.K) / 0.02, 2);
        return e;
      }
      var x0 = [start.pan, start.tilt], st0 = [1 * D, 0.5 * D];
      if (o.joint) { x0.push(start.F || o.F, start.K != null ? start.K : o.K); st0.push(8, 0.02); }
      var f = nelder(cost, x0, st0, iters * (o.joint ? 2 : 1), each), gg = rigOf(f.x);
      var es = ms.map(function (m) { return epi(gg, m[0], m[1], m[2], m[3]).e; }).sort(function (a, b) { return a - b; });
      return { g: gg, x: f.x, good: es.filter(function (e) { return e < 2.5; }).length, med: es[es.length >> 1] };
    }
    // ---- The visible search: the picture always shows the best alignment found so far. ----
    // Score: how well both lenses' edge pictures match over all of the shared part (at infinity), for a pan, tilt
    // and lens shape (F, K) together. First spread tries around the start (the hand alignment), then small steps in
    // each direction that keep only what improves, smaller and smaller (a descent), then the shared pairs.
    var lensO = function (F, K) { return Object.assign({}, o, { F: F, K: K }); };
    var rigX = function (x) { return Rig(lensO(x[2], x[3]), x[0], x[1]); };
    var rayCache = {}, rayKeys = [];
    function raysFor(F, K) {
      var key = F.toFixed(1) + '/' + K.toFixed(4), rs = rayCache[key];
      if (rs) return rs;
      rs = []; var L0 = { F: F, K: K }, rr = [0, 0, 0];
      for (var yy = 3; yy < h4 - 3; yy++) for (var xx = 3; xx < w4 - 3; xx++) { unproject(L0, xx * f4 + 2, yy * f4 + 2, rr); rs.push(rr[0], rr[1], rr[2]); }
      rayCache[key] = rs; rayKeys.push(key); if (rayKeys.length > 40) delete rayCache[rayKeys.shift()];
      return rs;
    }
    function roughX(x, L) {
      var g = rigX(x), rs = raysFor(x[2], x[3]), sa = 0, sb = 0, saa = 0, sbb = 0, sab = 0, n = 0, R = g.R, ga = lvl[L].gA, gb = lvl[L].gB;
      for (var i = 0, m = 0; i < rs.length; i += 3, m++) {
        if (!project(g.L, R[0] * rs[i] + R[1] * rs[i + 1] + R[2] * rs[i + 2], R[3] * rs[i] + R[4] * rs[i + 1] + R[5] * rs[i + 2], R[6] * rs[i] + R[7] * rs[i + 1] + R[8] * rs[i + 2], q)) continue;
        var bv = bil(gb, w4, h4, q[0] / f4 - 0.5, q[1] / f4 - 0.5); if (bv !== bv || q[0] < 12 || q[1] < 12 || q[0] > LW - 12 || q[1] > LH - 12) continue;
        var av = ga[pix[m]]; sa += av; sb += bv; saa += av * av; sbb += bv * bv; sab += av * bv; n++;
      }
      if (n < 150) return -1;
      var va = saa / n - (sa / n) * (sa / n), vb = sbb / n - (sb / n) * (sb / n);
      return va > 0 && vb > 0 ? (sab / n - sa / n * sb / n) / Math.sqrt(va * vb) * Math.sqrt(n / 400) : -1;
    }
    var p0 = o.start || tops[0];
    if (!st.viewCyl) makeView(st, Rig(o, p0.pan, p0.tilt));
    var cur = [p0.pan, p0.tilt, o.F, o.K], shown = rigX(cur), ref = null;
    // Each better result moves lens B's picture there. The pictures of both lenses are sent once (at the first
    // result) and each better one only says how far to move lens B's picture: cheap, so many can be shown.
    function showBest(text, frac, x, dur) {
      var nr = rigX(x);
      if (!ref) { ref = nr; show({ stage: 2, text: text, frac: frac, rig: nr, mix: true, viewCyl: true, hold: 60, slide: { from: nr, to: nr, c: st.viewCyl, crop0: st.viewCrop, crop1: st.viewCrop, dur: 50, pad: 160 } }); }
      show({ stage: 2, text: text, frac: frac, move: moveOf(st.viewCyl, st.viewCrop, ref, nr), hold: dur + 20, dur: dur });
      shown = nr;
    }
    var D2 = function (x) { return (x[0] / D).toFixed(1) + '°, ' + (x[1] / D).toFixed(1) + '°, F ' + x[2].toFixed(0) + ', K ' + x[3].toFixed(2); };
    // 1. spread tries (also the rough search's best places), on the soft edge pictures
    var curV = roughX(cur, 0), tried = 0;
    var halton = function (i, b) { var f = 1, r = 0; while (i > 0) { f /= b; r += f * (i % b); i = Math.floor(i / b); } return r; };
    var seeds = tops.slice(0, 6).map(function (t) { return [t.pan, t.tilt, o.F, o.K]; });
    for (var hi = 1; hi <= 70; hi++) seeds.push([p0.pan + (2 * halton(hi, 2) - 1) * 8 * D, p0.tilt + (2 * halton(hi, 3) - 1) * 5 * D, o.F * (1 + (2 * halton(hi, 5) - 1) * 0.04), o.K + (2 * halton(hi, 7) - 1) * 0.06]);
    show({ stage: 2, text: 'Probando alineaciones y lentes', frac: 0.05, rig: shown, mix: true, viewCyl: true, hold: 300 });
    seeds.forEach(function (x) {
      tried++; var v = roughX(x, 0);
      if (v > curV) { cur = x; curV = v; showBest('Probando alineaciones y lentes: ' + tried + ' probadas, la mejor: ' + D2(cur), 0.05 + 0.1 * tried / seeds.length, cur, 220); }
    });
    // 2. descent on the sharper pictures: a small step in each direction, keep what improves, then smaller steps
    curV = roughX(cur, 1);
    [[1, 0.6, 8, 0.02], [0.5, 0.3, 4, 0.01], [0.25, 0.15, 2, 0.005], [0.12, 0.08, 1, 0.0025]].forEach(function (stp, si) {
      for (var it = 0; it < 30; it++) {
        var improved = false;
        for (var pi = 0; pi < 4; pi++) for (var sg = -1; sg <= 1; sg += 2) {
          var y2 = cur.slice(); y2[pi] += sg * stp[pi] * (pi < 2 ? D : 1); var v2 = roughX(y2, 1);
          if (v2 > curV) { cur = y2; curV = v2; improved = true; showBest('Bajando por la pendiente (paso ' + (si + 1) + ' de 4): ' + D2(cur), 0.15 + 0.05 * si, cur, 140); }
        }
        if (!improved) break;
      }
    });
    // 3. shared pairs where the pictures line up best now
    var g0 = rigX(cur), win = { t: { pan: cur[0], tilt: cur[1], F: cur[2], K: cur[3] } };
    var matches = findMatches(g0, 800, function (ms, n, tot) { show({ stage: 2, text: 'Buscando puntos en comun: ' + ms.length + ' (de ' + n + ' esquinas probadas)', frac: 0.35 + 0.05 * n / tot, rig: g0, mix: true, matches: ms, viewCyl: true, hold: 160 }); });
    var fr2 = matches.length >= 8 ? fitRig(win.t, matches, 80) : { good: 0 };
    if (fr2.good < 15) {
      // A near thing seen by both lenses (a ladder) can fool the picture score: try the rough search's best
      // places for real (shared corners and fine fit each) and keep the one where the most pairs fit.
      tops.slice(0, 8).forEach(function (t) {
        var ms = findMatches(Rig(o, t.pan, t.tilt), 250), f = ms.length >= 8 ? fitRig(t, ms, 80) : { good: 0 };
        if (f.good > fr2.good) { fr2 = f; win = { t: t }; }
      });
      if (!fr2.good) throw new Error('no encontre puntos en comun entre las dos lentes');
      g0 = Rig(o, win.t.pan, win.t.tilt); matches = findMatches(g0, 800);
      if (matches.length < 8) throw new Error('muy pocos puntos en comun (' + matches.length + ')');
    }
    // 4. fine fit on the pairs (pan, tilt and lens shape together), showing each better result; then match again
    // only changes one can see (a twentieth of a degree, or a visible lens change) are shown
    var lastShown = -1, lastX = null;
    var eachFit = function (label) { return function (it, xv) {
      var x4 = xv.length > 2 ? xv : [xv[0], xv[1], o.F, o.K];
      if (lastX && Math.abs(x4[0] - lastX[0]) < 0.05 * D && Math.abs(x4[1] - lastX[1]) < 0.05 * D && Math.abs(x4[2] - lastX[2]) < 1 && Math.abs(x4[3] - lastX[3]) < 0.003) return;
      if (it - lastShown < 3) return;
      lastShown = it; lastX = x4.slice(); showBest(label + ': ' + D2(x4), 0.42, x4, 110);
    }; };
    fr2 = fitRig(win.t, matches, 120, eachFit('Afinando con ' + matches.length + ' puntos en comun'));
    for (var round = 0; round < (o.rounds || 0); round++) {
      var ms2 = findMatches(fr2.g, 800);
      if (ms2.length < 8) break;
      lastShown = -1;
      var fr3 = fitRig({ pan: fr2.g.pan, tilt: fr2.g.tilt, F: fr2.g.L.F, K: fr2.g.L.K }, ms2, 120, eachFit('Vuelta ' + (round + 2) + ', ' + ms2.length + ' puntos'));
      if (fr3.good < fr2.good * 0.9) break;
      matches = ms2; fr2 = fr3;
    }
    var g = fr2.g;
    st.rig = g; st.matchErr = fr2.med; st.matches = matches;
    // zoom onto the shared strip, lens B gliding the last bit into place
    cylinder(st);
    st.view = { cyl: st.viewCyl, crop: stripBox(st, st.viewCyl, g) || st.viewCrop };
    show({ stage: 3, text: 'Encajando las lentes', frac: 0.48, rig: g, mix: true, matches: matches, cylObj: st.view.cyl, crop: st.view.crop, hold: 1300,
      slide: { from: shown, to: g, c: st.view.cyl, crop0: st.viewCrop || st.view.crop, crop1: st.view.crop, matches: matches, dur: 1200 } });
    show({ stage: 3, text: 'Encajadas: error tipico ' + st.matchErr.toFixed(1) + ' px, ' + fr2.good + ' de ' + matches.length + ' puntos encajan', frac: 0.5, rig: g, mix: true, matches: matches, cylObj: st.view.cyl, crop: st.view.crop, hold: 900 });
    // a wrong result must not reach the TV
    if (fr2.good < 15 || fr2.med > 4) throw new Error('no pude encajar las lentes (error tipico ' + fr2.med.toFixed(1) + ' px, ' + fr2.good + ' puntos buenos). Proba de nuevo o mové la camara 4.');

    // -- 4. distances --
    cylinder(st);
    measure(st, show);
    seam(st, show);
    // lens B was shown as if everything were far; now each row settles at its measured distance (smoothly)
    show({ stage: 5, text: 'Ajustando la camara 4 a la distancia de cada cosa', frac: 0.88, rig: g, mix: true, W: true, cylObj: st.view.cyl, crop: st.view.crop, hold: 1500,
      slide: { rows: true, to: g, c: st.view.cyl, crop1: st.view.crop, dur: 1400 } });
    // the seam is drawn from top to bottom while lens B settles row by row, then the final picture fades in
    show({ stage: 5, text: 'Costura: por donde las dos lentes se ven igual', frac: 0.92, rig: g, mix: true, W: true, seamLine: true, cylObj: st.view.cyl, crop: st.view.crop, hold: 1900, anim: 1700, fade: 400 });
    framing(st);
    // the result, zoomed on the seam (that is where one can see whether it fits): about 25 degrees wide around it
    st.view.final = seamBox(st);
    show({ stage: 7, text: 'Listo', frac: 1, rig: g, W: true, seam: true, cylObj: st.view.cyl, crop: st.view.final, hold: 300, fade: 700 });
    return st;
  }

  // The show's canvas: one fixed, wide cylinder (from the hand alignment to the seam, so nothing jumps), zoomed on
  // where the lenses meet, with some of each lens around it.
  function makeView(st, g) {
    var keep0 = st.rig, keepC = st.cyl; st.rig = g; cylinder(st); var vc = st.cyl, m = 0.35;
    var V = st.viewCyl = { a0: vc.a0 - m, a1: vc.a1 + m, Fc: vc.Fc, h0: vc.h0 - 0.15, CW: Math.round(vc.CW + 2 * m * vc.Fc), CH: Math.round(vc.CH + 0.3 * vc.Fc) };
    var rr = [0, 0, 0], qq = [0, 0], bx0 = 1e9, bx1 = -1, by0 = 1e9, by1 = -1, ax0 = 1e9, ax1 = -1;
    for (var yv = 0; yv < V.CH; yv += 6) for (var xv = 0; xv < V.CW; xv += 6) { cylRay(V, xv, yv, rr); var ia = toA(g, rr, 0, qq), ib = toB(g, rr, 0, qq);
      if (ia) { by0 = Math.min(by0, yv); by1 = Math.max(by1, yv); ax0 = Math.min(ax0, xv); ax1 = Math.max(ax1, xv); }
      if (ia && ib) { bx0 = Math.min(bx0, xv); bx1 = Math.max(bx1, xv); } }
    st.viewCrop = null;
    if (bx1 > 0) {
      var mid = (bx0 + bx1) / 2, half = Math.max((bx1 - bx0) * 2.5, (ax1 - ax0) * 0.35);
      st.viewCrop = { x0: Math.max(0, Math.round(mid - half)), x1: Math.min(V.CW, Math.round(mid + half)), y0: Math.max(0, by0 - 10), y1: Math.min(V.CH, by1 + 10) };
    }
    st.cyl = keepC; st.rig = keep0;
  }
  // Box on the show's canvas centred on the seam, about 25 degrees wide, over the rows where both lenses see.
  function seamBox(st) {
    var V = st.view.cyl, cr = st.view.crop, r = [0, 0, 0], xs = [];
    if (!st.ws) return cr;
    for (var y = 0; y < st.cyl.CH; y += 4) { var sx = st.ws.seam[y]; if (sx < 0) continue; cylRay(st.cyl, sx, y, r); var p = cylOf(V, r); if (p[1] >= cr.y0 && p[1] <= cr.y1) xs.push(p[0]); }
    if (!xs.length) return cr;
    xs.sort(function (a, b) { return a - b; });
    var mid = xs[xs.length >> 1], half = Math.max(0.22 * V.Fc, (xs[xs.length - 1] - xs[0]) * 0.7);
    return { x0: Math.max(0, Math.round(mid - half)), x1: Math.min(V.CW, Math.round(mid + half)), y0: cr.y0, y1: cr.y1 };
  }
  // Box around the part both lenses see (cylinder pixels of c), wider on the sides: the show zooms on it.
  function stripBox(st, c, g) {
    c = c || st.cyl; g = g || st.rig; var r = [0, 0, 0], q = [0, 0], x0 = 1e9, x1 = -1, y0 = 1e9, y1 = -1;
    for (var y = 0; y < c.CH; y += 4) for (var x = 0; x < c.CW; x += 4) { cylRay(c, x, y, r); if (toA(g, r, 0, q) && toB(g, r, 0, q)) { x0 = Math.min(x0, x); x1 = Math.max(x1, x); y0 = Math.min(y0, y); y1 = Math.max(y1, y); } }
    if (x1 < 0) return null;
    var w = x1 - x0, h = y1 - y0, cx = (x0 + x1) / 2, ww = Math.max(w * 2.4, h * 1.2);
    return { x0: Math.max(0, Math.round(cx - ww / 2)), x1: Math.min(c.CW, Math.round(cx + ww / 2)), y0: Math.max(0, Math.round(y0 - h * 0.05)), y1: Math.min(c.CH, Math.round(y1 + h * 0.05)) };
  }
  // Cylinder around A's view: angle a0..a1 (both lenses), heights hA..hB (any picture at all). Canvas CW wide.
  function cylinder(st) {
    var g = st.rig, r = [0, 0, 0], q = [0, 0], angs = [], R = g.R;
    [0, LW - 1].forEach(function (x) {
      unproject(g.L, x, LH / 2, r); angs.push(Math.atan2(r[0], r[2]));
      var d = [R[0] * r[0] + R[3] * r[1] + R[6] * r[2], R[1] * r[0] + R[4] * r[1] + R[7] * r[2], R[2] * r[0] + R[5] * r[1] + R[8] * r[2]];
      angs.push(Math.atan2(d[0], d[2]));
    });
    angs.sort(function (a, b) { return a - b; });
    var c = { a0: angs[0], a1: angs[3], CW: st.o.outW || 1400 };
    c.Fc = c.CW / (c.a1 - c.a0);
    var hs = LH / 2 / g.L.F * 1.8; c.h0 = -hs; c.CH = Math.round(2 * hs * c.Fc);
    st.cyl = c;
  }
  function cylRay(c, x, y, out) { var ang = c.a0 + (x + 0.5) / c.Fc, hg = c.h0 + (y + 0.5) / c.Fc, n = Math.sqrt(1 + hg * hg); out[0] = Math.sin(ang) / n; out[1] = hg / n; out[2] = Math.cos(ang) / n; }
  function cylOf(c, v) { var ang = Math.atan2(v[0], v[2]), hg = v[1] / Math.sqrt(v[0] * v[0] + v[2] * v[2]); return [(ang - c.a0) * c.Fc - 0.5, (hg - c.h0) * c.Fc - 0.5]; }

  // Grid over A's cylinder. For each point both lenses see: A's patch fixed, B's patch slid along its line.
  function measure(st, show) {
    var c = st.cyl, g = st.rig, A = st.inp.A, B = st.inp.B;
    var step = Math.max(8, Math.round(c.CW / 120)), NX = Math.floor((c.CW - 1) / step) + 1, NY = Math.floor((c.CH - 1) / step) + 1;
    var N = NX * NY, wv = new Float64Array(N), known = new Uint8Array(N), both = new Uint8Array(N), NW = 160;
    var P = Math.max(6, Math.round(step * 0.7)), ps = Math.max(1, Math.round(P / 4)), pr = [0, 0, 0], qa = [0, 0], qb = [0, 0];
    var W = new Float64Array(N);
    st.grid = { step: step, NX: NX, NY: NY, W: W };
    for (var gy = 0; gy < NY; gy++) {
      for (var gx = 0; gx < NX; gx++) {
        var gi = gy * NX + gx, cx = gx * step, cy = gy * step; cylRay(c, cx, cy, pr);
        if (!toA(g, pr, 0, qa) || !toB(g, pr, 0, qb)) continue; both[gi] = 1;
        // A's patch once
        var ap = [], rs = [];
        for (var j = -P; j <= P; j += ps) for (var i = -P; i <= P; i += ps) { cylRay(c, cx + i, cy + j, pr); if (!toA(g, pr, 0, qa)) continue; var av = bil(A, LW, LH, qa[0], qa[1]); if (av !== av) continue; ap.push(av); rs.push(pr.slice()); }
        var tot = Math.pow(2 * Math.floor(P / ps) + 1, 2); if (ap.length < tot * 0.6) continue;
        var sa = 0, saa = 0; for (i = 0; i < ap.length; i++) { sa += ap[i]; saa += ap[i] * ap[i]; }
        var na = ap.length, va = saa / na - (sa / na) * (sa / na); if (va < 30) continue;
        var sc = new Float64Array(NW + 1), bestv = -2;
        for (var k = 0; k <= NW; k++) {
          var w = WMAX * k / NW, sb = 0, sbb = 0, sab = 0, sa2 = 0, saa2 = 0, n = 0;
          for (i = 0; i < rs.length; i++) { if (!toB(g, rs[i], 2 * w, qb)) continue; var bv = bil(B, LW, LH, qb[0], qb[1]); if (bv !== bv) continue; var a = ap[i]; sa2 += a; saa2 += a * a; sb += bv; sbb += bv * bv; sab += a * bv; n++; }
          var v = -2;
          if (n >= tot * 0.5) { var vA = saa2 / n - (sa2 / n) * (sa2 / n), vB = sbb / n - (sb / n) * (sb / n); v = vA > 10 && vB > 1 ? (sab / n - sa2 / n * sb / n) / Math.sqrt(vA * vB) : -1; }
          sc[k] = v; if (v > bestv) bestv = v;
        }
        if (bestv < 0.72) continue;
        var kk = 0; while (sc[kk] < bestv - 0.04) kk++;   // of the w that fit about as well as the best, the farthest
        if (kk === NW) continue;
        wv[gi] = WMAX * kk / NW; known[gi] = 1; W[gi] = wv[gi];
      }
      if (gy === NY - 1) show({ stage: 4, text: 'Distancias medidas (calido = cerca)', frac: 0.8, rig: g, mix: true, wdots: { step: step, NX: NX, known: known, wv: wv }, dotsFade: true, cylObj: st.view && st.view.cyl, crop: st.view && st.view.crop, hold: 1300, anim: 400 });
    }
    // clean: a measure needs 2 neighbours (in the 5x5 around) that agree with it
    var keep = known.slice(), tolW = function (m) { return 0.08 + 0.25 * m; };
    for (var pass = 0; pass < 3; pass++) {
      var drop = [];
      for (gy = 0; gy < NY; gy++) for (gx = 0; gx < NX; gx++) {
        gi = gy * NX + gx; if (!keep[gi]) continue; var agree = 0;
        for (j = -2; j <= 2; j++) for (i = -2; i <= 2; i++) { var x = gx + i, y = gy + j; if ((i || j) && x >= 0 && y >= 0 && x < NX && y < NY && keep[y * NX + x] && Math.abs(wv[y * NX + x] - wv[gi]) <= tolW(Math.max(wv[y * NX + x], wv[gi]))) agree++; }
        if (agree < 2) drop.push(gi);
      }
      drop.forEach(function (d) { keep[d] = 0; }); if (!drop.length) break;
    }
    // fill: soft pull to the measures, smooth elsewhere; 0 (no correction) far from the shared strip
    var zero = new Uint8Array(N), fade = Math.max(3, Math.round(NX / 8));
    for (gy = 0; gy < NY; gy++) {
      var lo = 1e9, hi = -1; for (gx = 0; gx < NX; gx++) if (both[gy * NX + gx]) { lo = Math.min(lo, gx); hi = gx; }
      for (gx = 0; gx < NX; gx++) if (!both[gy * NX + gx] && (hi < 0 || gx > hi + fade || gx < lo - fade)) zero[gy * NX + gx] = 1;
    }
    var m0 = new Float64Array(N); for (i = 0; i < N; i++) { m0[i] = keep[i] ? wv[i] : 0; W[i] = m0[i]; }
    var Pull = 4;
    for (var it = 0; it < 1500; it++) {
      for (gy = 0; gy < NY; gy++) for (gx = 0; gx < NX; gx++) {
        gi = gy * NX + gx; if (zero[gi]) { W[gi] = 0; continue; } var s = 0, cnt = 0;
        if (gx > 0) { s += W[gi - 1]; cnt++; } if (gx < NX - 1) { s += W[gi + 1]; cnt++; } if (gy > 0) { s += W[gi - NX]; cnt++; } if (gy < NY - 1) { s += W[gi + NX]; cnt++; }
        if (keep[gi]) { s += Pull * cnt * m0[gi]; cnt += Pull * cnt; } W[gi] = Math.max(0, s / cnt);
      }
    }
    var kc = 0; for (i = 0; i < N; i++) kc += keep[i];
    st.measured = kc; st.keep = keep;
  }
  function wAt(gr, x, y) {
    var gx = Math.max(0, Math.min(gr.NX - 1.001, x / gr.step)), gy = Math.max(0, Math.min(gr.NY - 1.001, y / gr.step));
    var x0 = gx | 0, y0 = gy | 0, fx = gx - x0, fy = gy - y0, k = y0 * gr.NX + x0, W = gr.W;
    return (W[k] * (1 - fx) + W[k + 1] * fx) * (1 - fy) + (W[k + gr.NX] * (1 - fx) + W[k + gr.NX + 1] * fx) * fy;
  }
  // -- 5. seam, on the cylinder (half size) --
  // Each lens is shown from its own place, unbent: lens A as it is, lens B as it is from 9 cm lower, each row of B at
  // one distance (B only moves up or down a little per row, never bends sideways: straight lines stay straight).
  // That distance is the BACKGROUND's in that row (a low value of the measures there): a near thing at the seam (the
  // table the camera stands on) would pull the whole row of B with it. Then the seam is a path from top to bottom
  // where both lenses, exactly as they will be drawn, look most alike.
  function seam(st, show) {
    var c = st.cyl, g = st.rig, gr = st.grid, S = 2, w = Math.floor(c.CW / S), h = Math.floor(c.CH / S), x, y, i, j;
    var KW = c.Fc / S * Math.sqrt(4 * (g.h[0] * g.h[0] + g.h[1] * g.h[1] + g.h[2] * g.h[2]));   // half-size px per unit w
    // background distance per grid row: the 30% point of the good measures in that row and the next ones
    var rowW = new Float64Array(gr.NY).fill(NaN);
    for (var gy = 0; gy < gr.NY; gy++) {
      var l = []; for (var dy = -1; dy <= 1; dy++) { var yy = gy + dy; if (yy < 0 || yy >= gr.NY) continue; for (var gx = 0; gx < gr.NX; gx++) { var gi = yy * gr.NX + gx; if (st.keep && st.keep[gi]) l.push(gr.W[gi]); } }
      if (l.length >= 2) { l.sort(function (a2, b2) { return a2 - b2; }); rowW[gy] = l[Math.floor(l.length * 0.3)]; }
    }
    var hw = new Float32Array(h);
    for (y = 0; y < h; y++) {
      var gyf = (y * S + S / 2) / gr.step, best = NaN, bd = 1e9;
      for (gy = 0; gy < gr.NY; gy++) if (rowW[gy] === rowW[gy] && Math.abs(gy - gyf) < bd) { bd = Math.abs(gy - gyf); best = rowW[gy]; }
      hw[y] = best === best ? best : 0;
    }
    // smooth (middle value, then average), then at most 0.08 px more shift per row (at most 8% taller or shorter)
    var hs = new Float32Array(h); for (y = 0; y < h; y++) { var nn = []; for (j = Math.max(0, y - 10); j <= Math.min(h - 1, y + 10); j++) nn.push(hw[j]); nn.sort(function (a2, b2) { return a2 - b2; }); hs[y] = nn[nn.length >> 1]; }
    var hwA;
    // the limit, for sure: the lowest curve under the measures that never changes faster than the limit (it stays
    // with the background and only rises slowly where every row around needs more)
    var lim = 0.08 / KW, env = new Float32Array(h);
    for (y = 0; y < h; y++) { var mn = Infinity; for (j = 0; j < h; j++) { var v = hs[j] + lim * Math.abs(y - j); if (v < mn) mn = v; } env[y] = mn; }
    hwA = env;
    // both lenses exactly as they will be drawn
    var la = new Float32Array(w * h), lb = new Float32Array(w * h), ok = new Uint8Array(w * h), r = [0, 0, 0], qa = [0, 0], qb = [0, 0];
    for (y = 0; y < h; y++) for (x = 0; x < w; x++) {
      var k = y * w + x; cylRay(c, x * S + S / 2, y * S + S / 2, r);
      if (toA(g, r, 0, qa) && toB(g, r, 2 * hwA[y], qb)) { ok[k] = 1; la[k] = bil(st.inp.A, LW, LH, qa[0], qa[1]); lb[k] = bil(st.inp.B, LW, LH, qb[0], qb[1]); }
    }
    var ea = gradMag(la, w, h), eb = gradMag(lb, w, h), INF = 1e18, prev = new Float64Array(w).fill(INF), from = new Int32Array(w * h), rowOk = new Uint8Array(h);
    for (y = 0; y < h; y++) {
      var cur = new Float64Array(w).fill(INF);
      for (x = 1; x < w - 1; x++) {
        var k2 = y * w + x; if (!ok[k2] || !ok[k2 - 1] || !ok[k2 + 1]) continue;
        // edges that differ, and brightness that differs a little (both as drawn)
        var D = Math.abs(ea[k2] - eb[k2]) + 0.15 * Math.abs(la[k2] - lb[k2]) + 0.5;
        if (y === 0 || !rowOk[y - 1]) { cur[x] = D + (y > 0 && prev[x] < INF ? prev[x] : 0); from[k2] = x; continue; }
        var bv = INF, bx = -1;
        for (var d = -1; d <= 1; d++) { var xx = x + d; if (prev[xx] < bv) { bv = prev[xx]; bx = xx; } }
        if (bx >= 0 && bv < INF) { cur[x] = bv + D; from[k2] = bx; }
      }
      for (x = 0; x < w; x++) if (cur[x] < INF) { rowOk[y] = 1; break; }
      if (!rowOk[y]) for (x = 0; x < w; x++) { cur[x] = prev[x]; from[y * w + x] = x; }
      prev = cur;
    }
    var bx2 = -1, bvv = INF; for (x = 0; x < w; x++) if (prev[x] < bvv) { bvv = prev[x]; bx2 = x; }
    var seamC = new Float32Array(c.CH), Ws = new Float32Array(c.CH), hx = new Float32Array(h);
    if (bx2 >= 0) for (y = h - 1; y >= 0; y--) { hx[y] = bx2; bx2 = from[y * w + bx2]; } else hx.fill(-1);
    for (y = 0; y < c.CH; y++) { var yh = Math.min(h - 1, Math.floor(y / S)); seamC[y] = hx[yh] < 0 ? -1 : hx[yh] * S + S / 2; Ws[y] = hwA[yh]; }
    st.ws = { seam: seamC, W: Ws };
    // which lens is left: where only A shows, on average
    var sA = 0, nA = 0; for (y = 0; y < c.CH; y += 8) for (x = 0; x < c.CW; x += 8) { cylRay(c, x, y, r); if (toA(g, r, 0, qa) && !toB(g, r, 0, qb)) { sA += x; nA++; } }
    st.aLeft = nA === 0 || sA / nA < c.CW / 2;
  }
  // B's pixel for cylinder point (cx, cy): at the distance of that row's seam point
  function toBrow(st, r, cy, q) { var ws = st.ws, w = ws ? ws.W[Math.max(0, Math.min(ws.W.length - 1, Math.round(cy)))] : 0; return toB(st.rig, r, 2 * w, q); }

  // -- 6. frame: which part of the cylinder the output shows (a straight rectangle) --
  function framing(st) {
    var c = st.cyl, g = st.rig, CW = c.CW, CH = c.CH, top = new Float64Array(CW), bot = new Float64Array(CW), r = [0, 0, 0], q = [0, 0];
    for (var x = 0; x < CW; x++) {
      var t = -1, b = -1;
      for (var y = 0; y < CH; y++) { cylRay(c, x, y, r); if (toA(g, r, 0, q) || toBrow(st, r, y, q)) { if (t < 0) t = y; b = y; } }
      top[x] = t; bot[x] = b;
    }
    // clip 0: everything any lens sees (black where nothing is); clip 1: a straight cut with picture everywhere
    // (only the rows every kept column has; far end columns with little picture dropped).
    var xa0 = 0, xa1 = CW - 1; while (xa0 < CW && top[xa0] < 0) xa0++; while (xa1 > xa0 && top[xa1] < 0) xa1--;
    var ext = []; for (x = 0; x < CW; x++) if (top[x] >= 0) ext.push(bot[x] - top[x]);
    ext.sort(function (a, b) { return a - b; }); var med = ext[ext.length >> 1];
    var x0 = xa0, x1 = xa1; while (x0 < x1 && !(top[x0] >= 0 && bot[x0] - top[x0] > med * 0.75)) x0++; while (x1 > x0 && !(top[x1] >= 0 && bot[x1] - top[x1] > med * 0.75)) x1--;
    var oT = 1e9, oB = -1e9, iT = -1e9, iB = 1e9;
    for (x = xa0; x <= xa1; x++) if (top[x] >= 0) { oT = Math.min(oT, top[x]); oB = Math.max(oB, bot[x]); }
    for (x = x0; x <= x1; x++) { iT = Math.max(iT, top[x]); iB = Math.min(iB, bot[x]); }
    st.edges = { xa0: xa0, xa1: xa1, x0: x0, x1: x1, oT: oT, oB: oB, iT: iT, iB: iB };
    setClip(st, st.o.clip);
  }
  function setClip(st, clip) {
    var e = st.edges, X0 = Math.round(e.xa0 + clip * (e.x0 - e.xa0)), X1 = Math.round(e.xa1 + clip * (e.x1 - e.xa1));
    var t = e.oT + clip * (e.iT - e.oT), b = e.oB + clip * (e.iB - e.oB);
    st.fr = { x0: X0, y0: t, OW: X1 - X0 + 1, OH: Math.max(2, Math.round(b - t)), clip: clip };
  }
  // output pixel -> cylinder point
  function outToCyl(fr, ox, oy, p) { p[0] = fr.x0 + ox; p[1] = fr.y0 + oy; }

  // -- 7. mesh for the PC --
  function mesh(st) {
    var fr = st.fr, c = st.cyl, g = st.rig, step = 8, NXm = Math.ceil((fr.OW - 1) / step) + 1, NYm = Math.ceil((fr.OH - 1) / step) + 1;
    var m = new Float32Array(NXm * NYm * 4), p = [0, 0], r = [0, 0, 0], qa = [0, 0], qb = [0, 0], ws = st.ws;
    for (var j = 0; j < NYm; j++) for (var i = 0; i < NXm; i++) {
      var ox = Math.min(fr.OW - 1, i * step), oy = Math.min(fr.OH - 1, j * step), k = (j * NXm + i) * 4;
      outToCyl(fr, ox, oy, p); cylRay(c, p[0], p[1], r);
      var w = ws ? ws.W[Math.max(0, Math.min(ws.W.length - 1, Math.round(p[1])))] : 0;
      // raw lens coordinates (may fall outside the picture: the PC checks each pixel)
      if (project(g.L, r[0], r[1], r[2], qa)) { m[k] = qa[0] / LW; m[k + 1] = qa[1] / LH; } else { m[k] = NaN; m[k + 1] = NaN; }
      var x = r[0] - 2 * g.h[0] * w, y = r[1] - 2 * g.h[1] * w, z = r[2] - 2 * g.h[2] * w, R = g.R;
      if (project(g.L, R[0] * x + R[1] * y + R[2] * z, R[3] * x + R[4] * y + R[5] * z, R[6] * x + R[7] * y + R[8] * z, qb)) { m[k + 2] = qb[0] / LW; m[k + 3] = qb[1] / LH; } else { m[k + 2] = NaN; m[k + 3] = NaN; }
    }
    var seamU = new Float32Array(fr.OH);
    for (var y2 = 0; y2 < fr.OH; y2++) { var sc = ws ? ws.seam[Math.max(0, Math.min(c.CH - 1, Math.round(fr.y0 + y2)))] : -1; seamU[y2] = sc < 0 ? -1 : (sc - fr.x0 + 0.5) / fr.OW; }
    return { head: { OW: fr.OW, OH: fr.OH, NX: NXm, NY: NYm, step: step, aLeft: st.aLeft, pan: g.pan, tilt: g.tilt, clip: fr.clip }, mesh: m, seam: seamU };
  }
  // Binary message for the PC: "P3", header length (uint32), header JSON, padding to 4, mesh floats, seam floats.
  function pack(ms) {
    var hj = new TextEncoder().encode(JSON.stringify(ms.head)), hl = hj.length, pad = (4 - (6 + hl) % 4) % 4;
    var buf = new ArrayBuffer(6 + hl + pad + ms.mesh.length * 4 + ms.seam.length * 4), u8 = new Uint8Array(buf);
    u8[0] = 80; u8[1] = 51; new DataView(buf).setUint32(2, hl, true); u8.set(hj, 6);
    var off = 6 + hl + pad; new Float32Array(buf, off, ms.mesh.length).set(ms.mesh); new Float32Array(buf, off + ms.mesh.length * 4, ms.seam.length).set(ms.seam);
    return buf;
  }

  // ---- pictures for the page ----
  // Panorama preview, PW wide. mix: both lenses half and half where both see (ghosts show what does not fit yet).
  // Before the frame exists: the whole cylinder of the rough/fine rig at infinity.
  function preview(st, PW, opt) {
    var g = opt.rig, inp = st.inp, c = st.cyl, fr = opt.frame || null;
    if (opt.cylObj) c = opt.cylObj;
    else if (opt.viewCyl) {
      // hand alignment: one fixed, wider canvas, so lens A stays put while lens B is dragged around
      if (!st.viewCyl) makeView(st, g);
      c = st.viewCyl;
    } else if (!c || opt.stage < 4) { var keep = st.rig; st.rig = g; cylinder(st); c = st.cyl; st.rig = keep || g; }
    var cr = fr ? null : opt.crop || (opt.viewCyl ? st.viewCrop : null), cx0 = cr ? cr.x0 : 0, cy0 = cr ? cr.y0 : 0;
    var OW = fr ? fr.OW : cr ? cr.x1 - cr.x0 : c.CW, OH = fr ? fr.OH : cr ? cr.y1 - cr.y0 : c.CH, s = OW / PW, PH = Math.max(1, Math.round(OH / s));
    var img = new Uint8ClampedArray(PW * PH * 4), p = [0, 0], r = [0, 0, 0], qa = [0, 0], qb = [0, 0];
    // distances and seam live on the work canvas (st.cyl); the show may use another one: convert through the ray
    var wc = st.cyl, other = wc && c !== wc, pw = p;
    for (var y = 0; y < PH; y++) for (var x = 0; x < PW; x++) {
      var ox = (x + 0.5) * s, oy = (y + 0.5) * s;
      if (fr) outToCyl(fr, ox, oy, p); else { p[0] = cx0 + ox; p[1] = cy0 + oy; }
      cylRay(c, p[0], p[1], r);
      if ((opt.W || opt.seam) && st.ws) pw = other ? cylOf(wc, r) : p;
      var inA = toA(g, r, 0, qa), inB = opt.W && st.ws ? toBrow(st, r, pw[1], qb) : toB(g, r, 0, qb), k = (y * PW + x) * 4, t;
      if (!inA && !inB) { img[k + 3] = 255; continue; }
      if (opt.seam && st.ws && inA && inB) { var sxx = st.ws.seam[Math.max(0, Math.min(wc.CH - 1, Math.round(pw[1])))]; t = st.aLeft ? (pw[0] > sxx ? 1 : 0) : (pw[0] < sxx ? 1 : 0); }
      else t = inA && inB ? 0.5 : inB ? 1 : 0;
      for (var ch = 0; ch < 3; ch++) {
        var a = inA ? colAt(inp.Ac, qa[0], qa[1], ch) : 0, b = inB ? colAt(inp.Bc, qb[0], qb[1], ch) : 0;
        img[k + ch] = a * (1 - t) + b * t;
      }
      img[k + 3] = 255;
    }
    // lines: each shared pair, from where A shows it to where B shows it (at infinity before distances are known)
    var lines = [];
    if (opt.matches && !fr) {
      var ra = [0, 0, 0], rb = [0, 0, 0], R = g.R;
      opt.matches.forEach(function (m) {
        unproject(g.L, m[0], m[1], ra); unproject(g.L, m[2], m[3], rb);
        var d = [R[0] * rb[0] + R[3] * rb[1] + R[6] * rb[2], R[1] * rb[0] + R[4] * rb[1] + R[7] * rb[2], R[2] * rb[0] + R[5] * rb[1] + R[8] * rb[2]];
        var pa = cylOf(c, ra), pb = cylOf(c, d), e = epi(g, m[0], m[1], m[2], m[3]).e;
        lines.push((pa[0] - cx0) / s, (pa[1] - cy0) / s, (pb[0] - cx0) / s, (pb[1] - cy0) / s, e);
      });
    }
    var dots = [];
    var wv2 = function (x2, y2) { if (!other) return [x2, y2]; cylRay(wc, x2, y2, r); return cylOf(c, r); };   // work canvas -> this one
    if (opt.wdots && !fr) { var wd = opt.wdots; for (var i = 0; i < wd.known.length; i++) if (wd.known[i]) { var dp = wv2((i % wd.NX) * wd.step, Math.floor(i / wd.NX) * wd.step); dots.push((dp[0] - cx0) / s, (dp[1] - cy0) / s, wd.wv[i]); } }
    // the seam as a line (in preview pixels), and the measuring scan line
    var seamPts = [];
    if (opt.seamLine && st.ws) for (var yy = 0; yy < wc.CH; yy += 3) {
      var sxc = st.ws.seam[yy]; if (sxc < 0) continue;
      var sp = fr ? [sxc - fr.x0, yy - fr.y0] : (function () { var q2 = wv2(sxc, yy); return [q2[0] - cx0, q2[1] - cy0]; })();
      if (sp[1] >= 0 && sp[1] <= OH) seamPts.push(sp[0] / s, sp[1] / s);
    }
    var scan = null;
    if (opt.scan != null && !fr) { cylRay(c, cx0 + OW / 2, cy0 + OH / 2, r); var mx = other ? cylOf(wc, r)[0] : cx0 + OW / 2; scan = (wv2(mx, opt.scan)[1] - cy0) / s; }
    return { w: PW, h: PH, data: img, lines: lines, dots: dots, rpp: s / c.Fc, seam: seamPts, scan: scan };   // rpp: radians per preview pixel
  }
  // For a slide (lens B moving from rig 'from' to rig 'to'), the phone gets two layers once (lens A, and lens B at
  // 'to', each with see-through where that lens has no picture) and moves lens B's layer itself, every screen frame.
  // On the narrow zoomed view a small pan/tilt change is almost a plain shift, so the shift of the view's middle is
  // used. Also the zoom (crop0 -> crop1) and the pair lines (both ends) for in between.
  function slideData(st, PW, sl) {
    if (sl.rows) return rowData(st, PW, sl);
    var c = sl.c, g = sl.to, f = sl.from, c0 = sl.crop0 || sl.crop1, c1 = sl.crop1 || c0, r = [0, 0, 0], q = [0, 0], rb = [0, 0, 0];
    // where the middle of the final view's lens B picture showed with 'from'
    cylRay(c, (c1.x0 + c1.x1) / 2, (c1.y0 + c1.y1) / 2, r); var off = [0, 0];
    if (toB(g, r, 0, q)) { unproject(g.L, q[0], q[1], rb); var R = f.R, d = [R[0] * rb[0] + R[3] * rb[1] + R[6] * rb[2], R[1] * rb[0] + R[4] * rb[1] + R[7] * rb[2], R[2] * rb[0] + R[5] * rb[1] + R[8] * rb[2]], pf = cylOf(c, d); off = [pf[0] - (c1.x0 + c1.x1) / 2, pf[1] - (c1.y0 + c1.y1) / 2]; }
    var pad = sl.pad || 8, U = { x0: Math.max(0, Math.min(c0.x0, c1.x0) - Math.abs(off[0]) - pad), y0: Math.max(0, Math.min(c0.y0, c1.y0) - Math.abs(off[1]) - pad) };
    U.x1 = Math.min(c.CW, Math.max(c0.x1, c1.x1) + Math.abs(off[0]) + pad); U.y1 = Math.min(c.CH, Math.max(c0.y1, c1.y1) + Math.abs(off[1]) + pad);
    // layer size: the final view at about PW wide (the zoom must stay sharp), at most 1800 wide
    var sc = Math.max((c1.x1 - c1.x0) / PW, (U.x1 - U.x0) / 1800), LWp = Math.max(1, Math.round((U.x1 - U.x0) / sc)), LHp = Math.max(1, Math.round((U.y1 - U.y0) / sc));
    var la = new Uint8ClampedArray(LWp * LHp * 4), lb = new Uint8ClampedArray(LWp * LHp * 4), qa = [0, 0], qb = [0, 0];
    for (var y = 0; y < LHp; y++) for (var x = 0; x < LWp; x++) {
      cylRay(c, U.x0 + (x + 0.5) * sc, U.y0 + (y + 0.5) * sc, r); var k = (y * LWp + x) * 4;
      if (toA(g, r, 0, qa)) { for (var ch = 0; ch < 3; ch++) la[k + ch] = colAt(st.inp.Ac, qa[0], qa[1], ch); la[k + 3] = 255; }
      if (toB(g, r, 0, qb)) { for (ch = 0; ch < 3; ch++) lb[k + ch] = colAt(st.inp.Bc, qb[0], qb[1], ch); lb[k + 3] = 255; }
    }
    var L = function (v, o0) { return (v - o0) / sc; };
    var lines = [];
    (sl.matches || []).forEach(function (m) {
      var ra = [0, 0, 0], rbb = [0, 0, 0]; unproject(g.L, m[0], m[1], ra); unproject(g.L, m[2], m[3], rbb);
      var ends = [f, g].map(function (rg) { var R2 = rg.R, d2 = [R2[0] * rbb[0] + R2[3] * rbb[1] + R2[6] * rbb[2], R2[1] * rbb[0] + R2[4] * rbb[1] + R2[7] * rbb[2], R2[2] * rbb[0] + R2[5] * rbb[1] + R2[8] * rbb[2]]; return cylOf(c, d2); });
      var pa = cylOf(c, ra);
      lines.push(L(pa[0], U.x0), L(pa[1], U.y0), L(ends[0][0], U.x0), L(ends[0][1], U.y0), L(ends[1][0], U.x0), L(ends[1][1], U.y0), epi(f, m[0], m[1], m[2], m[3]).e, epi(g, m[0], m[1], m[2], m[3]).e);
    });
    var cr = function (cc) { return [L(cc.x0, U.x0), L(cc.y0, U.y0), L(cc.x1, U.x0), L(cc.y1, U.y0)]; };
    return { w: LWp, h: LHp, A: la, B: lb, off: [off[0] / sc, off[1] / sc], crop0: cr(c0), crop1: cr(c1), lines: lines, outW: PW, outH: Math.round(PW * (c1.y1 - c1.y0) / (c1.x1 - c1.x0)), sc: sc };
  }
  // Lens B settling row by row at the measured distances: layers of both lenses as if everything were far, and per
  // layer row the shift of lens B from there to its place at that row's distance (the phone moves B in thin strips).
  function rowData(st, PW, sl) {
    var c = sl.c, g = sl.to, cr = sl.crop1, sc = (cr.x1 - cr.x0) / PW, LWp = PW, LHp = Math.max(1, Math.round((cr.y1 - cr.y0) / sc));
    var la = new Uint8ClampedArray(LWp * LHp * 4), lb = new Uint8ClampedArray(LWp * LHp * 4), r = [0, 0, 0], qa = [0, 0], qb = [0, 0], rb = [0, 0, 0];
    for (var y = 0; y < LHp; y++) for (var x = 0; x < LWp; x++) {
      cylRay(c, cr.x0 + (x + 0.5) * sc, cr.y0 + (y + 0.5) * sc, r); var k = (y * LWp + x) * 4;
      if (toA(g, r, 0, qa)) { for (var ch = 0; ch < 3; ch++) la[k + ch] = colAt(st.inp.Ac, qa[0], qa[1], ch); la[k + 3] = 255; }
      if (toB(g, r, 0, qb)) { for (ch = 0; ch < 3; ch++) lb[k + ch] = colAt(st.inp.Bc, qb[0], qb[1], ch); lb[k + 3] = 255; }
    }
    var rows = new Float32Array(LHp * 2), R = g.R, xm = st.ws ? null : (cr.x0 + cr.x1) / 2;
    for (y = 0; y < LHp; y++) {
      // at the seam of this row (where it matters), else the middle
      var vy = cr.y0 + (y + 0.5) * sc, vx = (cr.x0 + cr.x1) / 2;
      cylRay(c, vx, vy, r); var pw = cylOf(st.cyl, r), sx = st.ws ? st.ws.seam[Math.max(0, Math.min(st.cyl.CH - 1, Math.round(pw[1])))] : -1;
      if (sx >= 0) { cylRay(st.cyl, sx, pw[1], r); var pv = cylOf(c, r); vx = pv[0]; cylRay(c, vx, vy, r); }
      if (!toBrow(st, r, cylOf(st.cyl, r)[1], qb)) continue;
      unproject(g.L, qb[0], qb[1], rb);
      var d = [R[0] * rb[0] + R[3] * rb[1] + R[6] * rb[2], R[1] * rb[0] + R[4] * rb[1] + R[7] * rb[2], R[2] * rb[0] + R[5] * rb[1] + R[8] * rb[2]], p0 = cylOf(c, d);
      rows[2 * y] = (vx - p0[0]) / sc; rows[2 * y + 1] = (vy - p0[1]) / sc;
    }
    var cc = [0, 0, LWp, LHp];
    return { w: LWp, h: LHp, A: la, B: lb, off: [0, 0], rows: rows, crop0: cc, crop1: cc, lines: [], outW: PW, outH: LHp };
  }
  // How far (canvas pixels) lens B's picture drawn for rig 'ref' must move to look like rig 'to', at the view's middle.
  function moveOf(c, cr, ref, to) {
    var r = [0, 0, 0], q = [0, 0], rb = [0, 0, 0], cx = (cr.x0 + cr.x1) / 2, cy = (cr.y0 + cr.y1) / 2;
    cylRay(c, cx, cy, r); if (!toB(to, r, 0, q)) return [0, 0];
    unproject(to.L, q[0], q[1], rb); var R = ref.R, d = [R[0] * rb[0] + R[3] * rb[1] + R[6] * rb[2], R[1] * rb[0] + R[4] * rb[1] + R[7] * rb[2], R[2] * rb[0] + R[5] * rb[1] + R[8] * rb[2]], pf = cylOf(c, d);
    return [cx - pf[0], cy - pf[1]];
  }
  function colAt(img, x, y, ch) {
    var x0 = x | 0, y0 = y | 0, fx = x - x0, fy = y - y0, k = (y0 * LW + x0) * 4 + ch;
    return (img[k] * (1 - fx) + img[k + 4] * fx) * (1 - fy) + (img[k + LW * 4] * (1 - fx) + img[k + LW * 4 + 4] * fx) * fy;
  }
  // 3D points of the shared strip, only near real measures (the fill between them is a guess): each one is lens A's
  // pixel, at its measured distance along A's ray. [x, y, z, r, g, b] per point, metres, from lens A.
  function cloud(st) {
    var c = st.cyl, g = st.rig, gr = st.grid, keep = st.keep, out = [], r = [0, 0, 0], qa = [0, 0];
    if (!gr || !keep) return new Float32Array(0);
    for (var y = 0; y < c.CH; y += 2) for (var x = 0; x < c.CW; x += 2) {
      var gx = Math.round(x / gr.step), gy = Math.round(y / gr.step);
      if (gx >= gr.NX || gy >= gr.NY || !keep[gy * gr.NX + gx]) continue;
      cylRay(c, x, y, r); if (!toA(g, r, 0, qa)) continue;
      var w = wAt(gr, x, y); if (w < 0.1) continue;   // farther than 10 m: not measurable with 9 cm
      var d = 1 / w; out.push(r[0] * d, r[1] * d, r[2] * d, colAt(st.inp.Ac, qa[0], qa[1], 0), colAt(st.inp.Ac, qa[0], qa[1], 1), colAt(st.inp.Ac, qa[0], qa[1], 2));
    }
    return new Float32Array(out);
  }
  return { CAM: CAM, slideData: slideData, run: run, cloud: cloud, setClip: setClip, seam: seam, mesh: mesh, pack: pack, preview: preview, Rig: Rig, epi: epi, LW: LW, LH: LH };
})();

// ---- Web Worker side: the page sends the pictures; pictures of the work go back as it runs. ----
if (typeof WorkerGlobalScope !== 'undefined' && typeof self !== 'undefined') {
  var state = null, PW = 720, lastShow = 0;
  // Every step of the work is one frame; the page plays them at their own pace (hold ms each, fade = cross-fade).
  var post = function (opt) {
    if (opt.move) { self.postMessage({ t: 'show', kind: 'move', text: opt.text, frac: opt.frac, move: opt.move, hold: opt.hold || 150, dur: opt.dur || 120 }); return; }
    if (opt.slide) {
      var sd = Calib.slideData(state, PW, opt.slide);
      self.postMessage({ t: 'show', kind: 'slide', text: opt.text, frac: opt.frac, w: sd.w, h: sd.h, A: sd.A, B: sd.B, off: sd.off, crop0: sd.crop0, crop1: sd.crop1, lines: sd.lines, rows: sd.rows || null, sc: sd.sc || 1, outW: sd.outW, outH: sd.outH, hold: opt.hold || 1000, dur: opt.slide.dur || 1000 }, [sd.A.buffer, sd.B.buffer]);
      return;
    }
    var pv = Calib.preview(state, PW, opt);
    self.postMessage({ t: 'show', text: opt.text, frac: opt.frac, w: pv.w, h: pv.h, img: pv.data, lines: pv.lines, dots: pv.dots, seam: pv.seam, reveal: opt.reveal, scan: pv.scan, hold: opt.hold || 120, fade: opt.fade || 0, anim: opt.anim || 0, dotsFade: !!opt.dotsFade }, [pv.data.buffer]);
  };
  self.onmessage = function (e) {
    var m = e.data;
    try {
      if (m.t === 'load') { PW = m.pw || 720; state = { inp: m.inp, o: m.o }; state.o.pw = PW; for (var kc in Calib.CAM) if (state.o[kc] === undefined) state.o[kc] = Calib.CAM[kc]; }
      else if (m.t === 'view' && state) {
        // hand alignment picture: both lenses half and half at this pan/tilt
        var pv = Calib.preview(state, Math.min(PW, 640), { stage: 1, viewCyl: true, rig: Calib.Rig(state.o, m.pan, m.tilt) });
        self.postMessage({ t: 'view', w: pv.w, h: pv.h, img: pv.data, rpp: pv.rpp }, [pv.data.buffer]);
      }
      else if (m.t === 'start') {
        if (m.inp) { PW = m.pw || 720; state = { inp: m.inp, o: m.o }; }
        state.o.start = m.start || null; state.cyl = null; state.viewCyl = null; state.viewCrop = null;
        Calib.run(state, post);
        finish();
        var pc = Calib.cloud(state); self.postMessage({ t: 'cloud', pts: pc }, [pc.buffer]);
      }
    } catch (ex) { self.postMessage({ t: 'error', text: String(ex.message || ex) }); }
  };
  var finish = function () {
    var ms = Calib.mesh(state), buf = Calib.pack(ms);
    post({ stage: 7, cylObj: state.view.cyl, crop: state.view.final || state.view.crop, text: 'Listo: paneo ' + (state.rig.pan * 180 / Math.PI).toFixed(1) + '°, inclinacion ' + (state.rig.tilt * 180 / Math.PI).toFixed(1) + '°, error tipico ' + state.matchErr.toFixed(1) + ' px, ' + state.matches.length + ' puntos, ' + state.measured + ' distancias', frac: 1, rig: state.rig, W: true, seam: true }, true);
    self.postMessage({ t: 'done', mesh: buf, OW: ms.head.OW, OH: ms.head.OH }, [buf]);
  };
}
if (typeof module !== 'undefined' && module.exports) module.exports = Calib;
