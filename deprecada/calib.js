// Panorama calibration of the split camera (lens A on top, fixed; lens B below it on a PTZ motor), as two real
// cameras 9 cm apart. Runs on the phone in a Web Worker (the remote control page starts it) and in bun for tests.
//
// Fixed for the camera: lens shape (F, K), how lens A sits on the body (tilt tA), the 9 cm between the lenses.
// Per calibration: pan and tilt of lens B (the PTZ motor moves it), the lens shape nudged near its known values,
// and one distance per band of rows of lens B. Each lens is shown unbent from its own place.
//
// Steps (the phone shows each as it goes, always with the best result so far):
//  1. The person lines both lenses up roughly by hand.
//  2. Search: spread tries of pan, tilt and lens shape around there, scored by how well both lenses' edge pictures
//     match over the shared part; then a descent with smaller and smaller steps.
//  3. Shared corners: corners of B, each searched in A near where the search puts it.
//  4. Fine fit on the pairs: each pair may sit at any distance (it only has to fall on its line), but a distance
//     costs a little, so the turn explains what it can; corners searched again, a few rounds.
//  5. Distance per band of rows of lens B, chosen by looking (best match over the band), smooth and limited.
//  6. Seam: a path through the overlap where both lenses, as drawn, look most alike.
//  7. Frame (cut to only picture) and mesh: for a grid of output points, which pixel of A and of B to take.
//     The PC only draws this mesh.
// Lens pixel units: a lens picture is 640x360 (the PC scales to its frame size).
'use strict';
var Calib = (function () {
  var LW = 640, LH = 360;
  // The camera itself (same for every calibration): lens shape, how lens A sits on the body (tilt tA), and where
  // lens B is from lens A: 9 cm (only scales the distances, the picture does not change with it), leaned ax
  // sideways and az forward from straight down. Chosen by what one sees: the values that make both lenses match best
  // over ten test pictures, each fully calibrated (a search run on the computer).
  var CAM = { F: 696.045, K: -0.247393, tA: -0.074596, base: 0.09, ax: 0.044947, az: 0.018557 };
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
    for (i = 1; i <= 14; i++) { var w = WMAX * i / 14, ob = obj(w); if (ob < bo) { bo = ob; bw = w; } }
    var lo = Math.max(0, bw - WMAX / 14), hi = Math.min(WMAX, bw + WMAX / 14);
    for (i = 0; i < 16; i++) { var m1 = lo + (hi - lo) * 0.382, m2 = lo + (hi - lo) * 0.618; if (obj(m1) < obj(m2)) hi = m2; else lo = m1; }
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
    var FIT = { lam: 0.05, joint: true, rounds: 2, pos: true, loose: 1 };
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
    }).sort(function (a, b) { return b.v - a.v; });
    // Never refuse: a person or a near thing can spoil the scores. Keep going with the best there is and
    // only warn (st.weak lists the reasons; the final message says the result may be wrong).
    st.weak = [];
    var goodTops = tops.filter(function (t) { return o.start || t.v >= 0.5; });
    if (goodTops.length) tops = goodTops;
    else { st.weak.push('las dos lentes casi no se parecen'); tops = tops.slice(0, 1); }

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
    var A4 = shrink(A, LW, LH, 4), B4 = shrink(B, LW, LH, 4), w4b = LW / 4, h4b = LH / 4, Rs4 = Math.round(Rs / 2);
    // correlation of A's patches with one fixed patch of B (its mean and spread worked out once)
    function nccFixed(I, J, w, h, bx, by, P) {
      if (bx < P || by < P || bx >= w - P || by >= h - P) return null;
      var vals = [], s = 0, ss = 0; for (var j = -P; j <= P; j++) for (var i = -P; i <= P; i++) { var v = J[(by + j) * w + bx + i]; vals.push(v); s += v; ss += v * v; }
      var n = vals.length, mb = s / n, vb = ss / n - mb * mb; if (vb <= 4) return null;
      for (var k = 0; k < n; k++) vals[k] -= mb;
      return function (ax, ay) {
        if (ax < P || ay < P || ax >= w - P || ay >= h - P) return -2;
        var sa = 0, saa = 0, sab = 0, m = 0;
        for (var j = -P; j <= P; j++) { var row = (ay + j) * w + ax; for (var i = -P; i <= P; i++) { var a = I[row + i]; sa += a; saa += a * a; sab += a * vals[m++]; } }
        var va = saa / n - (sa / n) * (sa / n); return va > 4 ? (sab / n) / Math.sqrt(va * vb) : -1;
      };
    }
    function findMatches(g0, maxC, progress) {
      var cs = [], rb = [0, 0, 0], R0 = g0.R, ms = [];
      for (var k = 0; k < corners.length && cs.length < maxC; k++) {
        var c = corners[k]; unproject(g0.L, c[0], c[1], rb);
        if (!project(g0.L, R0[0] * rb[0] + R0[3] * rb[1] + R0[6] * rb[2], R0[1] * rb[0] + R0[4] * rb[1] + R0[7] * rb[2], R0[2] * rb[0] + R0[5] * rb[1] + R0[8] * rb[2], q)) continue;
        if (q[0] < 20 || q[0] > LW - 20 || q[1] < 20 || q[1] > LH - 20) continue;
        cs.push([c[0], c[1], c[2], q[0], q[1]]);
      }
      for (var ci = 0; ci < cs.length; ci++) {
        var c2 = cs[ci], bst = -2, second = -2, bax = 0, bay = 0, xx, yy, nv;
        // coarse: quarter size, wide window; then half size, a small window around the best
        var f4 = nccFixed(A4, B4, w4b, h4b, Math.round(c2[0] / 4), Math.round(c2[1] / 4), 4);
        if (f4) {
          // the few best places at quarter size (apart from each other), each checked at half size; the best two
          // half-size results give the "clearly better than anything else" test
          var px4 = Math.round(c2[3] / 4), py4 = Math.round(c2[4] / 4), tops4 = [];
          for (yy = py4 - Rs4; yy <= py4 + Rs4; yy++) for (xx = px4 - Rs4; xx <= px4 + Rs4; xx++) {
            nv = f4(xx, yy); if (nv < 0.4) continue;
            if (tops4.length < 4 || nv > tops4[tops4.length - 1][0]) {
              var near = -1; for (var t4 = 0; t4 < tops4.length; t4++) if (Math.abs(tops4[t4][1] - xx) + Math.abs(tops4[t4][2] - yy) <= 2) { near = t4; break; }
              if (near >= 0) { if (nv > tops4[near][0]) tops4[near] = [nv, xx, yy]; } else tops4.push([nv, xx, yy]);
              tops4.sort(function (a3, b3) { return b3[0] - a3[0]; }); if (tops4.length > 4) tops4.length = 4;
            }
          }
          var f2 = tops4.length ? nccFixed(A2, B2, w2, h2, Math.round(c2[0] / 2), Math.round(c2[1] / 2), 6) : null;
          if (f2) tops4.forEach(function (t) {
            var tb = -2, tx = 0, ty = 0;
            for (var y5 = t[2] * 2 - 3; y5 <= t[2] * 2 + 3; y5++) for (var x5 = t[1] * 2 - 3; x5 <= t[1] * 2 + 3; x5++) { var v5 = f2(x5, y5); if (v5 > tb) { tb = v5; tx = x5; ty = y5; } }
            if (tb > bst) { if (Math.abs(tx - bax) + Math.abs(ty - bay) > 2) second = bst; bst = tb; bax = tx; bay = ty; } else if (tb > second && Math.abs(tx - bax) + Math.abs(ty - bay) > 2) second = tb;
          });
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
    // x = [pan, tilt, F, K, ax, az]: direction of lens B, lens shape, and where lens B sits from lens A (leaning
    // sideways ax and forward az from straight down: the PTZ turns around its own axis, not around the lens)
    var lensO = function (F, K, ax, az) { return Object.assign({}, o, { F: F, K: K, ax: ax || 0, az: az || 0 }); };
    var rigX = function (x) { return Rig(lensO(x[2], x[3], x[4], x[5]), x[0], x[1]); };
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
    var D2 = function (x) { return (x[0] / D).toFixed(1) + '°, ' + (x[1] / D).toFixed(1) + '°, F ' + x[2].toFixed(0) + ', K ' + x[3].toFixed(2) + (x[4] || x[5] ? ', B ' + ((x[4] || 0) / D).toFixed(1) + '°/' + ((x[5] || 0) / D).toFixed(1) + '°' : ''); };
    // 1. spread tries (also the rough search's best places), on the soft edge pictures
    var curV = roughX(cur, 0), tried = 0;
    var halton = function (i, b) { var f = 1, r = 0; while (i > 0) { f /= b; r += f * (i % b); i = Math.floor(i / b); } return r; };
    var seeds = tops.slice(0, 6).map(function (t) { return [t.pan, t.tilt, o.F, o.K]; });
    for (var hi = 1; hi <= 70; hi++) seeds.push([p0.pan + (2 * halton(hi, 2) - 1) * 8 * D, p0.tilt + (2 * halton(hi, 3) - 1) * 5 * D, o.F * (1 + (2 * halton(hi, 5) - 1) * 0.04), o.K + (2 * halton(hi, 7) - 1) * 0.06]);
    show({ stage: 2, text: 'Probando alineaciones y lentes', frac: 0.05, rig: shown, mix: true, viewCyl: true, hold: 300 });
    seeds.forEach(function (x) {
      tried++; var v = roughX(x, 0);
      if (tried % 8 === 0) show({ textOnly: true, text: 'Probando alineaciones y lentes: ' + tried + ' de ' + seeds.length + ' (la mejor: ' + D2(cur) + ')', frac: 0.05 + 0.1 * tried / seeds.length });
      if (v > curV) { cur = x; curV = v; showBest('Probando alineaciones y lentes: ' + tried + ' probadas, la mejor: ' + D2(cur), 0.05 + 0.1 * tried / seeds.length, cur, 220); }
    });
    // 2. descent on the sharper pictures: a small step in each direction, keep what improves, then smaller steps
    curV = roughX(cur, 1);
    [[1, 0.6, 8, 0.02], [0.5, 0.3, 4, 0.01], [0.25, 0.15, 2, 0.005], [0.12, 0.08, 1, 0.0025]].forEach(function (stp, si) {
      for (var it = 0; it < 30; it++) {
        show({ textOnly: true, text: 'Bajando por la pendiente (paso ' + (si + 1) + ' de 4), vuelta ' + (it + 1) + ': ' + D2(cur), frac: 0.15 + 0.05 * si });
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
    var matches = findMatches(g0, 800, function (ms, n, tot) { show({ textOnly: true, text: 'Buscando puntos en comun: ' + ms.length + ' (de ' + n + ' esquinas probadas)', frac: 0.35 + 0.05 * n / tot }); });
    var fr2 = matches.length >= 8 ? fitRig(win.t, matches, 80) : { good: 0 };
    if (fr2.good < 15) {
      // A near thing seen by both lenses (a ladder) can fool the picture score: try the rough search's best
      // places for real (shared corners and fine fit each) and keep the one where the most pairs fit.
      tops.slice(0, 8).forEach(function (t) {
        var ms = findMatches(Rig(o, t.pan, t.tilt), 250), f = ms.length >= 8 ? fitRig(t, ms, 80) : { good: 0 };
        if (f.good > fr2.good) { fr2 = f; win = { t: t }; }
      });
      if (!fr2.good) st.weak.push('no encontre puntos en comun entre las dos lentes');
      else { g0 = Rig(o, win.t.pan, win.t.tilt); matches = findMatches(g0, 800); }
      if (matches.length < 8) st.weak.push('muy pocos puntos en comun (' + matches.length + ')');
    }
    // 4. fine fit on the pairs (pan, tilt and lens shape together), showing each better result; then match again
    // only changes one can see (a twentieth of a degree, or a visible lens change) are shown
    var lastShown = -1, lastX = null;
    var eachFit = function (label) { return function (it, xv) {
      var x4 = xv.length > 2 ? xv : [xv[0], xv[1], o.F, o.K, 0, 0];
      if (it % 10 === 0) show({ textOnly: true, text: label + ', iteracion ' + it + ': ' + D2(x4), frac: 0.42 });
      if (lastX && Math.abs(x4[0] - lastX[0]) < 0.05 * D && Math.abs(x4[1] - lastX[1]) < 0.05 * D && Math.abs(x4[2] - lastX[2]) < 1 && Math.abs(x4[3] - lastX[3]) < 0.003 && Math.abs((x4[4] || 0) - (lastX[4] || 0)) < 0.2 * D && Math.abs((x4[5] || 0) - (lastX[5] || 0)) < 0.2 * D) return;
      if (it - lastShown < 3) return;
      lastShown = it; lastX = x4.slice(); showBest(label + ': ' + D2(x4), 0.42, x4, 110);
    }; };
    // Rounds that take turns: shared corners searched again with the current model, then the direction (pan,
    // tilt) with the lens kept, then the lens (F, K) with the direction kept; until a round no longer helps.
    // (Each part can move the best of the other, so they take turns rather than one after the other once.)
    function pairCost(x, ms) {
      var gg = rigX(x), es = ms.map(function (m) { return epi(gg, m[0], m[1], m[2], m[3], lam).c; }).sort(function (a, b) { return a - b; });
      // (with few pairs use all of them; with none the lens priors alone hold the fit where it is)
      var use = Math.min(es.length, Math.max(6, Math.floor(es.length * 0.9))), e = 0;
      for (var i2 = 0; i2 < use; i2++) e += es[i2] < 2 ? es[i2] * es[i2] : 4 * es[i2] - 4;
      if (use) e /= use;
      // the lens stays near the camera's known shape (loosely: a picture this narrow says little about it)
      var pf = o.loose || 1;   // how loosely the lens and lens B's place are held near the known values
      return e + Math.pow((x[2] - o.F) / (20 * pf), 2) + Math.pow((x[3] - o.K) / (0.05 * pf), 2) + (o.pos ? Math.pow((x[4] || 0) / (0.15 * pf), 2) + Math.pow((x[5] || 0) / (0.15 * pf), 2) : 0);
    }
    function part(x, ms, which, label) {
      var idx = which === 'dir' ? [0, 1] : which === 'lens' ? [2, 3] : [4, 5], stp = which === 'dir' ? [0.5 * D, 0.3 * D] : which === 'lens' ? [6, 0.02] : [0.05, 0.05];
      var f = nelder(function (v) { var y = x.slice(); y[idx[0]] = v[0]; y[idx[1]] = v[1]; return pairCost(y, ms); }, [x[idx[0]], x[idx[1]]], stp, 60, function (it, v) {
        var y = x.slice(); y[idx[0]] = v[0]; y[idx[1]] = v[1]; eachFit(label)(it, y);
      });
      var y2 = x.slice(); y2[idx[0]] = f.x[0]; y2[idx[1]] = f.x[1]; return { x: y2, v: f.v };
    }
    var xb = [win.t.pan, win.t.tilt, win.t.F || o.F, win.t.K != null ? win.t.K : o.K, 0, 0], vb = pairCost(xb, matches);
    for (var round = 1; round <= 5; round++) {
      if (round > 1) { var ms2 = findMatches(rigX(xb), 800, function (ms, n, tot) { show({ textOnly: true, text: 'Vuelta ' + round + ': buscando puntos en comun ' + ms.length + ' (de ' + n + ')', frac: 0.42 }); }); if (ms2.length >= 8) { matches = ms2; vb = pairCost(xb, matches); } }
      var before = vb;
      lastShown = -1; var pd = part(xb, matches, 'dir', 'Vuelta ' + round + ', ' + matches.length + ' puntos: direccion'); if (pd.v < vb) { xb = pd.x; vb = pd.v; }
      lastShown = -1; var pl = part(xb, matches, 'lens', 'Vuelta ' + round + ', ' + matches.length + ' puntos: lente'); if (pl.v < vb) { xb = pl.x; vb = pl.v; }
      if (o.pos) { lastShown = -1; var pp = part(xb, matches, 'pos', 'Vuelta ' + round + ', ' + matches.length + ' puntos: posicion'); if (pp.v < vb) { xb = pp.x; vb = pp.v; } }
      show({ textOnly: true, text: 'Vuelta ' + round + ': costo ' + before.toFixed(2) + ' -> ' + vb.toFixed(2), frac: 0.44 });
      if (round > 1 && vb > before * 0.99) break;   // this round did not help: done
    }
    if (o.joint2 !== false) { st.rig = rigX(xb); cylinder(st); st.rowKnots = null; var msJ = matches; xb = jointDescent(st, xb, rigX, show, showBest, D, function (v) { return pairCost(v.slice(0, 6), msJ); }); shown = rigX(xb); }
    var gF = rigX(xb), esF = matches.map(function (m) { return epi(gF, m[0], m[1], m[2], m[3]).e; }).sort(function (a, b) { return a - b; });
    st.fitX = xb;
    fr2 = { g: gF, x: xb, good: esF.filter(function (e) { return e < 2.5; }).length, med: esF.length ? esF[esF.length >> 1] : 99 };
    var g = fr2.g;
    st.rig = g; st.matchErr = fr2.med; st.matches = matches;
    // zoom onto the shared strip, lens B gliding the last bit into place
    cylinder(st);
    st.view = { cyl: st.viewCyl, crop: stripBox(st, st.viewCyl, g) || st.viewCrop };
    show({ stage: 3, text: 'Encajando las lentes', frac: 0.48, rig: g, mix: true, matches: matches, cylObj: st.view.cyl, crop: st.view.crop, hold: 1300,
      slide: { from: shown, to: g, c: st.view.cyl, crop0: st.jointCrop || st.viewCrop || st.view.crop, crop1: st.view.crop, matches: matches, knots: st.rowKnots, dur: 1200 } });
    show({ stage: 3, text: 'Encajadas: error tipico ' + st.matchErr.toFixed(1) + ' px, ' + fr2.good + ' de ' + matches.length + ' puntos encajan', frac: 0.5, rig: g, mix: true, matches: matches, knots: st.rowKnots, cylObj: st.view.cyl, crop: st.view.crop, hold: 900 });
    // a doubtful result is still used (never refuse); the final message warns about it
    if (fr2.good < 15 || fr2.med > 4) st.weak.push('las lentes encajan mal (error tipico ' + fr2.med.toFixed(1) + ' px, ' + fr2.good + ' puntos buenos)');

    // -- 4. distances --
    cylinder(st);
    seam(st, show);
    // the per-row distances must make the shared part match better than none at all; otherwise none
    if (stripMatch(st, true) < stripMatch(st, false)) seam(st, show, true);
    if (o.warp !== false) seamWarp(st, show);
    // lens B was shown as if everything were far; now each row settles at its band's distance (smoothly)
    // (when the descent already moved the rows, they are already in place: nothing to settle)
    if (!st.rowKnots) show({ stage: 5, text: 'Ajustando la camara 4 a la distancia de cada cosa', frac: 0.88, rig: g, mix: true, W: true, cylObj: st.view.cyl, crop: st.view.crop, hold: 1500,
      slide: { rows: true, to: g, c: st.view.cyl, crop1: st.view.crop, dur: 1400 } });
    // then the bending grows smoothly from none to all (it used to pop in with the seam)
    if (st.warp) {
      for (var wsI = 0; wsI <= 12; wsI++) {
        var wt = wsI / 12; st.warpScale = wt * wt * (3 - 2 * wt);
        show({ stage: 5, text: 'Doblando las lentes para que encajen (' + st.warp.measured + ' medidas)', frac: 0.9, rig: g, mix: true, W: true, cylObj: st.view.cyl, crop: st.view.crop, hold: 90, fade: wsI ? 80 : 0 });
      }
      st.warpScale = null;
    }
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

  // -- 5. seam, on the cylinder (half size) --
  // Each lens is shown from its own place, unbent: lens A as it is, lens B as it is from 9 cm lower, each row of B at
  // one distance (B only moves up or down a little per row, never bends sideways: straight lines stay straight).
  // That distance is chosen by looking at the whole band of rows (mostly background): a near thing at the seam (the
  // table the camera stands on) would otherwise pull the whole row of B with it. Then the seam is a path from top to
  // bottom where both lenses, exactly as they will be drawn, look most alike.
  function seam(st, show, flat) {
    var c = st.cyl, g = st.rig, S = 2, w = Math.floor(c.CW / S), h = Math.floor(c.CH / S), x, y, i, j;
    var KW = c.Fc / S * Math.sqrt(4 * (g.h[0] * g.h[0] + g.h[1] * g.h[1] + g.h[2] * g.h[2]));   // half-size px per unit w
    // Distance per band of rows, chosen by looking: lens B is tried at many distances (also "no shift", also a bit
    // "beyond far": the measuring has errors both ways) and the one where both lenses match best over the whole
    // shared part of that band wins; among nearly as good ones, the smallest shift. So it can only look better.
    var BAND = 8, nb = Math.ceil(h / BAND), hw = new Float32Array(h), bandW = new Float64Array(nb).fill(NaN), rq = [0, 0, 0], qa0 = [0, 0], qb0 = [0, 0];
    var wLo = -10 / KW, wHi = Math.min(WMAX, 70 / KW), wStep = 0.5 / KW;
    for (var bi = 0; bi < nb && !flat && !st.rowKnots; bi++) {
      var rays2 = [], av2 = [];
      for (y = bi * BAND; y < Math.min(h, (bi + 1) * BAND); y++) for (x = 0; x < w; x += 2) {
        cylRay(c, x * S + S / 2, y * S + S / 2, rq); if (!toA(g, rq, 0, qa0) || !toB(g, rq, 0, qb0)) continue;
        rays2.push(rq.slice()); av2.push(bil(st.inp.A, LW, LH, qa0[0], qa0[1]));
      }
      if (rays2.length < 150) continue;
      var scores = [], bestS = -2;
      for (var wv = wLo; wv <= wHi; wv += wStep) {
        var sa = 0, sb = 0, saa = 0, sbb = 0, sab = 0, n = 0;
        for (i = 0; i < rays2.length; i++) { if (!toB(g, rays2[i], 2 * wv, qb0)) continue; var bv = bil(st.inp.B, LW, LH, qb0[0], qb0[1]); if (bv !== bv) continue; var a3 = av2[i]; sa += a3; sb += bv; saa += a3 * a3; sbb += bv * bv; sab += a3 * bv; n++; }
        var sc = -2; if (n > rays2.length * 0.6) { var va = saa / n - (sa / n) * (sa / n), vb = sbb / n - (sb / n) * (sb / n); if (va > 1 && vb > 1) sc = (sab / n - sa / n * sb / n) / Math.sqrt(va * vb); }
        scores.push([wv, sc]); if (sc > bestS) bestS = sc;
      }
      var pick = null; scores.forEach(function (q2) { if (q2[1] >= bestS - 0.01 && (!pick || Math.abs(q2[0]) < Math.abs(pick[0]))) pick = q2; });
      if (pick && bestS > 0.3) bandW[bi] = pick[0];
      if (show && bi % 6 === 5) show({ textOnly: true, text: 'Distancia por franjas: ' + (bi + 1) + ' de ' + nb, frac: 0.86 });
    }
    for (y = 0; y < h; y++) {
      var bb0 = Math.floor(y / BAND), best = NaN, bd = 1e9;
      for (bi = 0; bi < nb; bi++) if (bandW[bi] === bandW[bi] && Math.abs(bi - bb0) < bd) { bd = Math.abs(bi - bb0); best = bandW[bi]; }
      hw[y] = best === best ? best : 0;
    }
    // smooth a little (middle value of 5 bands' rows), then follow the bands as closely as possible while never
    // changing more than LIM px of shift per row (no stretching beyond that): pull toward the bands, clamp the
    // change per row both ways, many times
    var LIM = st.o.rowLim || 0.15;
    if (st.rowKnots && !flat) { var hwK = new Float32Array(h); for (y = 0; y < h; y++) hwK[y] = rowW(st, y * S + S / 2); hw = hwK; }
    var hs = new Float32Array(h); if (st.rowKnots && !flat) { hs = hw; } else for (y = 0; y < h; y++) { var nn = []; for (j = Math.max(0, y - 4); j <= Math.min(h - 1, y + 4); j++) nn.push(hw[j]); nn.sort(function (a2, b2) { return a2 - b2; }); hs[y] = nn[nn.length >> 1]; }
    // then a wide average (only slow changes: a near thing in a few bands must not make lens B wave)
    var SM = st.rowKnots ? 0 : st.o.rowSmooth != null ? st.o.rowSmooth : 40, hs2 = new Float32Array(h);
    if (SM > 0) for (y = 0; y < h; y++) { var sw = 0, sv = 0; for (j = Math.max(0, y - 2 * SM); j <= Math.min(h - 1, y + 2 * SM); j++) { var wg = Math.exp(-0.5 * Math.pow((j - y) / Math.max(1, SM), 2)); sw += wg; sv += wg * hs[j]; } hs2[y] = sv / sw; }
    if (SM > 0) hs = hs2;
    var lim = (st.rowKnots ? 100 : LIM) / KW, hwA = Float32Array.from(hs);
    for (var itp = 0; itp < 200; itp++) {
      for (y = 0; y < h; y++) hwA[y] = 0.7 * hwA[y] + 0.3 * hs[y];
      for (y = 1; y < h; y++) hwA[y] = Math.max(hwA[y - 1] - lim, Math.min(hwA[y - 1] + lim, hwA[y]));
      for (y = h - 2; y >= 0; y--) hwA[y] = Math.max(hwA[y + 1] - lim, Math.min(hwA[y + 1] + lim, hwA[y]));
    }
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
        // the person may say where the seam should go: it stays near there
        if (st.o.seamTarget != null) D += 0.08 * Math.abs(x * S + S / 2 - st.o.seamTarget);
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
    // test: a straight up-and-down seam in the middle of the shared part (middle value of every row's middle)
    if (SEAM_MID) {
      var mids = [];
      for (y = 0; y < h; y++) { var f0 = -1, f1 = -1; for (x = 0; x < w; x++) if (ok[y * w + x]) { if (f0 < 0) f0 = x; f1 = x; } if (f0 >= 0 && f1 - f0 > 4) mids.push((f0 + f1) / 2); }
      mids.sort(function (a2, b2) { return a2 - b2; });
      if (mids.length) { var xm = Math.round(mids[mids.length >> 1]); for (y = 0; y < h; y++) hx[y] = xm; }
    }
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

  // Row distance curve of the joint descent: knots over the work canvas height, straight lines between them.
  // distance of lens B at ray r by a row curve's knots (for the show: rows as the descent has them now)
  function knotWr(st, k, r) { var y = cylOf(st.cyl, r)[1], n = k.length, f = Math.max(0, Math.min(n - 1.0001, y / (st.cyl.CH - 1) * (n - 1))), i = f | 0; return k[i] + (k[i + 1] - k[i]) * (f - i); }
  function rowW(st, y) {
    var k = st.rowKnots, n = k.length, f = Math.max(0, Math.min(n - 1.0001, y / (st.cyl.CH - 1) * (n - 1))), i = f | 0;
    return k[i] + (k[i + 1] - k[i]) * (f - i);
  }
  // -- 4c. Everything together: one score (how well both lenses match over the whole shared strip, exactly as drawn,
  // lens B's rows at the curve's distance), and a descent that takes one small step in each variable in turn
  // (pan, tilt, F, K, where lens B sits, and each knot of the row curve), keeps what helps, then smaller steps.
  // The curve may not bend sharply (costs), so lens B never waves.
  function jointDescent(st, x0, rigX, show, showBest, D, pairs) {
    var c = st.cyl, g0 = rigX(x0), r = [0, 0, 0], qa = [0, 0], qb = [0, 0], pts = [];
    for (var y = 0; y < c.CH; y += 4) for (var x = 0; x < c.CW; x += 4) { cylRay(c, x, y, r); if (toA(g0, r, 0, qa) && toB(g0, r, 0, qb)) pts.push(r[0], r[1], r[2], y); }
    var NK = 8, KWf = c.Fc * 2 * Math.sqrt(g0.h[0] * g0.h[0] + g0.h[1] * g0.h[1] + g0.h[2] * g0.h[2]);   // px per unit w
    var o = st.o, LAMC = o.curveCost != null ? o.curveCost : 0.0004;
    function score(v) {
      var g = rigX(v), sa = 0, sb = 0, saa = 0, sbb = 0, sab = 0, n = 0, rr = [0, 0, 0];
      for (var i = 0; i < pts.length; i += 4) {
        rr[0] = pts[i]; rr[1] = pts[i + 1]; rr[2] = pts[i + 2]; var yy = pts[i + 3];
        if (!toA(g, rr, 0, qa)) continue;
        var f = yy / (c.CH - 1) * (NK - 1), k = Math.min(NK - 2, f | 0), w = v[6 + k] + (v[7 + k] - v[6 + k]) * (f - k);
        if (!toB(g, rr, 2 * w, qb)) continue;
        var a = bil(st.inp.A, LW, LH, qa[0], qa[1]), b = bil(st.inp.B, LW, LH, qb[0], qb[1]); if (a !== a || b !== b) continue;
        sa += a; sb += b; saa += a * a; sbb += b * b; sab += a * b; n++;
      }
      if (n < 500) return -2;
      var m = (sab / n - sa / n * sb / n) / Math.sqrt((saa / n - (sa / n) * (sa / n)) * (sbb / n - (sb / n) * (sb / n)));
      var bend = 0; for (var j = 1; j < NK - 1; j++) { var d2 = (v[6 + j - 1] - 2 * v[6 + j] + v[6 + j + 1]) * KWf; bend += d2 * d2; }
      var pf = o.loose || 1, prior = Math.pow((v[2] - o.F) / (20 * pf), 2) + Math.pow((v[3] - o.K) / (0.05 * pf), 2) + Math.pow(v[4] / (0.15 * pf), 2) + Math.pow(v[5] / (0.15 * pf), 2);
      // the shared pairs must keep fitting (a tilt and the opposite shift of all rows look alike on the strip; the
      // pairs tell them apart)
      var pc = pairs ? pairs(v) : 0;
      return m - LAMC * bend - 0.002 * prior - (o.pairW != null ? o.pairW : 0.01) * pc;
    }
    var v = x0.slice(0, 6); for (var k0 = 0; k0 < NK; k0++) v.push(0);
    var best = score(v), it = 0, zoomI = 0; st.jointCrop = null;
    var step0 = [0.15 * D, 0.1 * D, 3, 0.008, 0.02, 0.02]; for (k0 = 0; k0 < NK; k0++) step0.push(3 / KWf);
    var names = ['paneo', 'inclinacion', 'foco', 'curvatura', 'B costado', 'B adelante']; for (k0 = 0; k0 < NK; k0++) names.push('fila ' + (k0 + 1));
    [1, 0.5, 0.25, 0.12].forEach(function (sc) {
      for (var pass = 0; pass < 8; pass++) {
        var improved = false;
        for (var p = 0; p < v.length; p++) for (var sg = -1; sg <= 1; sg += 2) {
          var t = v.slice(); t[p] += sg * step0[p] * sc; var sv = score(t); it++;
          if (sv > best) { best = sv; v = t; improved = true; }
        }
        // what the descent has now, all of it (direction, lens, where B sits and the row curve) in one picture
        // ...and the view zooms in a bit more each time, onto the part both lenses see (live, while things move)
        if (improved && show && st.viewCrop) {
          var gz = rigX(v), tgt = stripBox(st, st.viewCyl, gz) || st.viewCrop, tz = 1 - Math.pow(0.82, ++zoomI), c0z = st.viewCrop;
          var cz = { x0: Math.round(c0z.x0 + (tgt.x0 - c0z.x0) * tz), x1: Math.round(c0z.x1 + (tgt.x1 - c0z.x1) * tz), y0: Math.round(c0z.y0 + (tgt.y0 - c0z.y0) * tz), y1: Math.round(c0z.y1 + (tgt.y1 - c0z.y1) * tz) };
          st.jointCrop = cz;
          show({ stage: 2, text: 'Todo junto: paso ' + sc + ', vuelta ' + (pass + 1) + ', coincidencia ' + best.toFixed(3), frac: 0.47, rig: gz, knots: v.slice(6), mix: true, cylObj: st.viewCyl, crop: cz, hold: 110, fade: 90 });
        }
        if (show) show({ textOnly: true, text: 'Todo junto: paso ' + sc + ', vuelta ' + (pass + 1) + ', ' + it + ' pruebas, coincidencia ' + best.toFixed(3), frac: 0.47 });
        if (!improved) break;
      }
    });
    st.rowKnots = v.slice(6);
    return v.slice(0, 6);
  }

  // -- 6b. Small bending near the seam only, so a wide soft seam shows no doubles. --
  // In a band of +-HB pixels around the seam, what is left between both lenses is measured (patches of A searched in
  // B, both as drawn), cleaned, smoothed, and kept gentle (neighbours differ by little, so nothing folds). Each lens
  // then bends half of it toward the other, fully in the middle of the band and fading to nothing at its edges.
  // HB 128: the whole shared part bends (not only next to the seam), so far things line up in it too.
  var HB = 128;
  var SEAM_MID = false;   // test switch: true = straight seam in the middle
  function seamWarp(st, show) {
    var c = st.cyl, g = st.rig, ws = st.ws, R = 16, P = 4, step = 6, r = [0, 0, 0], q = [0, 0], y, x, i, j;
    st.warp = null;
    if (!ws) return;
    var lo = 1e9, hi = -1; for (y = 0; y < c.CH; y++) if (ws.seam[y] >= 0) { lo = Math.min(lo, ws.seam[y]); hi = Math.max(hi, ws.seam[y]); }
    if (hi < 0) return;
    var X0 = Math.max(0, Math.floor(lo - HB - R - P - 2)), X1 = Math.min(c.CW - 1, Math.ceil(hi + HB + R + P + 2)), BW = X1 - X0 + 1, H = c.CH;
    // both lenses as drawn (A unshifted, B at its row's distance) over the box
    var A = new Float32Array(BW * H).fill(NaN), B = new Float32Array(BW * H).fill(NaN);
    for (y = 0; y < H; y++) { var sy = ws.seam[y]; if (sy < 0) continue;
      for (x = Math.max(X0, Math.floor(sy - HB - R - P - 1)); x <= Math.min(X1, Math.ceil(sy + HB + R + P + 1)); x++) {
        cylRay(c, x, y, r); var k = y * BW + x - X0;
        if (toA(g, r, 0, q)) A[k] = bil(st.inp.A, LW, LH, q[0], q[1]);
        if (toBrow(st, r, y, q)) B[k] = bil(st.inp.B, LW, LH, q[0], q[1]);
      } }
    // measures on a grid of nodes: B's patch that best matches A's patch, within +-R pixels
    var NX = Math.floor(BW / step) + 1, NY = Math.floor(H / step) + 1, N = NX * NY, mdx = new Float32Array(N), mdy = new Float32Array(N), has = new Uint8Array(N), out = new Uint8Array(N);
    for (var gy = 0; gy < NY; gy++) for (var gx = 0; gx < NX; gx++) {
      var cx = X0 + gx * step, cy = gy * step, gi = gy * NX + gx, sc = ws.seam[Math.min(H - 1, cy)];
      if (sc < 0 || Math.abs(cx - sc) > HB) { out[gi] = 1; continue; }
      if (cy < P + R || cy >= H - P - R || cx - X0 < P + R || X1 - cx < P + R) continue;
      var pa = [], sa = 0, saa = 0, okp = true;
      for (j = -P; j <= P && okp; j++) for (i = -P; i <= P; i++) { var av = A[(cy + j) * BW + cx - X0 + i]; if (av !== av) { okp = false; break; } pa.push(av); sa += av; saa += av * av; }
      if (!okp) continue; var n = pa.length, va = saa / n - (sa / n) * (sa / n); if (va < 25) continue;
      var best = -2, second = -2, bx = 0, by = 0;
      for (var dy = -R; dy <= R; dy++) for (var dx = -R; dx <= R; dx++) {
        var sb = 0, sbb = 0, sab = 0, m = 0, okb = true;
        for (j = -P; j <= P && okb; j++) for (i = -P; i <= P; i++) { var bv = B[(cy + dy + j) * BW + cx + dx - X0 + i]; if (bv !== bv) { okb = false; break; } var aa = pa[m++]; sb += bv; sbb += bv * bv; sab += aa * bv; }
        if (!okb) continue; var vb = sbb / n - (sb / n) * (sb / n); if (vb < 1) continue;
        var v = (sab / n - sa / n * sb / n) / Math.sqrt(va * vb);
        if (v > best) { if (Math.abs(dx - bx) + Math.abs(dy - by) > 2) second = best; best = v; bx = dx; by = dy; } else if (v > second && Math.abs(dx - bx) + Math.abs(dy - by) > 2) second = v;
      }
      if (best > 0.75 && best - second > 0.02 && Math.abs(bx) < R && Math.abs(by) < R) { mdx[gi] = bx; mdy[gi] = by; has[gi] = 1; }
      if (show && gx === 0 && gy % 20 === 0) show({ textOnly: true, text: 'Encajando la costura: fila ' + gy + ' de ' + NY, frac: 0.95 });
    }
    // clean: a measure far (2.5 px) from the middle value of those around it goes
    var keep = has.slice();
    for (gy = 0; gy < NY; gy++) for (gx = 0; gx < NX; gx++) { gi = gy * NX + gx; if (!has[gi]) continue; var lx = [], ly = [];
      for (j = -2; j <= 2; j++) for (i = -2; i <= 2; i++) { var xx = gx + i, yy = gy + j; if (xx < 0 || yy < 0 || xx >= NX || yy >= NY || !has[yy * NX + xx]) continue; lx.push(mdx[yy * NX + xx]); ly.push(mdy[yy * NX + xx]); }
      if (lx.length < 4) { keep[gi] = 0; continue; } lx.sort(function (a2, b2) { return a2 - b2; }); ly.sort(function (a2, b2) { return a2 - b2; });
      if (Math.abs(mdx[gi] - lx[lx.length >> 1]) > 2.5 || Math.abs(mdy[gi] - ly[ly.length >> 1]) > 2.5) keep[gi] = 0; }
    // fill: smooth surface pulled hard to the measures (free at the band's edges: warpAt fades it there)
    var fx = new Float32Array(N), fy = new Float32Array(N);
    for (var it = 0; it < 600; it++) for (gy = 0; gy < NY; gy++) for (gx = 0; gx < NX; gx++) {
      gi = gy * NX + gx;
      var s1 = 0, s2 = 0, cn = 0;
      if (gx > 0) { s1 += fx[gi - 1]; s2 += fy[gi - 1]; cn++; } if (gx < NX - 1) { s1 += fx[gi + 1]; s2 += fy[gi + 1]; cn++; }
      if (gy > 0) { s1 += fx[gi - NX]; s2 += fy[gi - NX]; cn++; } if (gy < NY - 1) { s1 += fx[gi + NX]; s2 += fy[gi + NX]; cn++; }
      // a measure counts 8 times its neighbours: the bending reaches what was measured (it is faded later, by warpAt)
      if (keep[gi]) { s1 += 8 * cn * mdx[gi]; s2 += 8 * cn * mdy[gi]; cn *= 9; }
      fx[gi] = s1 / cn; fy[gi] = s2 / cn;
    }
    // gentle: neighbouring nodes differ by at most half their distance (nothing folds)
    var lim = step * 0.5;
    for (var pass = 0; pass < 50; pass++) { var chg = false;
      for (gy = 0; gy < NY; gy++) for (gx = 0; gx < NX; gx++) { gi = gy * NX + gx;
        [gx + 1 < NX ? gi + 1 : -1, gy + 1 < NY ? gi + NX : -1].forEach(function (h2) { if (h2 < 0) return; [fx, fy].forEach(function (f) { var d = f[h2] - f[gi]; if (Math.abs(d) > lim) { var fix = (Math.abs(d) - lim) / 2 * Math.sign(d); f[gi] += fix; f[h2] -= fix; chg = true; } }); }); }
      if (!chg) break; }
    var kc = 0; for (i = 0; i < N; i++) kc += keep[i];
    st.warp = { X0: X0, NX: NX, NY: NY, step: step, dx: fx, dy: fy, measured: kc };
  }
  // the bending at work-canvas point (x, y), already faded by the distance to the seam: [dx, dy] (B minus A)
  function warpAt(st, x, y) {
    var wp = st.warp; if (!wp) return null;
    var sy = st.ws.seam[Math.max(0, Math.min(st.ws.seam.length - 1, Math.round(y)))]; if (sy < 0) return null;
    var d = Math.abs(x - sy), f = d <= HB / 2 ? 1 : d >= HB ? 0 : 1 - (d - HB / 2) / (HB / 2); if (f <= 0) return null;
    var gx = Math.max(0, Math.min(wp.NX - 1.001, (x - wp.X0) / wp.step)), gy = Math.max(0, Math.min(wp.NY - 1.001, y / wp.step));
    var x0 = gx | 0, y0 = gy | 0, ax = gx - x0, ay = gy - y0, k = y0 * wp.NX + x0;
    var bl = function (F) { return (F[k] * (1 - ax) + F[k + 1] * ax) * (1 - ay) + (F[k + wp.NX] * (1 - ax) + F[k + wp.NX + 1] * ax) * ay; };
    f = f * f * (3 - 2 * f);
    if (st.warpScale != null) f *= st.warpScale;   // the show grows the bending from none to all
    return [bl(wp.dx) * f, bl(wp.dy) * f];
  }
  // A's and B's pixel for work-canvas point (x, y): rows of B at their distance, each lens bent half toward the other
  function sampleAB(st, x, y, qa, qb) {
    var c = st.cyl, g = st.rig, r = [0, 0, 0], wv = warpAt(st, x, y), hx = wv ? wv[0] / 2 : 0, hy = wv ? wv[1] / 2 : 0;
    cylRay(c, x - hx, y - hy, r); var inA = toA(g, r, 0, qa);
    cylRay(c, x + hx, y + hy, r); var inB = toBrow(st, r, y + hy, qb);
    return (inA ? 1 : 0) | (inB ? 2 : 0);
  }

  // -- 7. mesh for the PC --
  function mesh(st) {
    var fr = st.fr, c = st.cyl, g = st.rig, step = 6, NXm = Math.ceil((fr.OW - 1) / step) + 1, NYm = Math.ceil((fr.OH - 1) / step) + 1;
    var m = new Float32Array(NXm * NYm * 4), p = [0, 0], r = [0, 0, 0], qa = [0, 0], qb = [0, 0], ws = st.ws;
    for (var j = 0; j < NYm; j++) for (var i = 0; i < NXm; i++) {
      var ox = Math.min(fr.OW - 1, i * step), oy = Math.min(fr.OH - 1, j * step), k = (j * NXm + i) * 4;
      outToCyl(fr, ox, oy, p);
      // raw lens coordinates (may fall outside the picture: the PC checks each pixel)
      var wv = warpAt(st, p[0], p[1]), hx = wv ? wv[0] / 2 : 0, hy = wv ? wv[1] / 2 : 0;
      cylRay(c, p[0] - hx, p[1] - hy, r);
      if (project(g.L, r[0], r[1], r[2], qa)) { m[k] = qa[0] / LW; m[k + 1] = qa[1] / LH; } else { m[k] = NaN; m[k + 1] = NaN; }
      cylRay(c, p[0] + hx, p[1] + hy, r);
      var w = ws ? ws.W[Math.max(0, Math.min(ws.W.length - 1, Math.round(p[1] + hy)))] : 0;
      var x = r[0] - 2 * g.h[0] * w, y = r[1] - 2 * g.h[1] * w, z = r[2] - 2 * g.h[2] * w, R = g.R;
      if (project(g.L, R[0] * x + R[1] * y + R[2] * z, R[3] * x + R[4] * y + R[5] * z, R[6] * x + R[7] * y + R[8] * z, qb)) { m[k + 2] = qb[0] / LW; m[k + 3] = qb[1] / LH; } else { m[k + 2] = NaN; m[k + 3] = NaN; }
    }
    var seamU = new Float32Array(fr.OH);
    for (var y2 = 0; y2 < fr.OH; y2++) { var sc = ws ? ws.seam[Math.max(0, Math.min(c.CH - 1, Math.round(fr.y0 + y2)))] : -1; seamU[y2] = sc < 0 ? -1 : (sc - fr.x0 + 0.5) / fr.OW; }
    return { head: { OW: fr.OW, OH: fr.OH, NX: NXm, NY: NYm, step: step, aLeft: st.aLeft, band: st.warp ? HB / 2 : 0, pan: g.pan, tilt: g.tilt, clip: fr.clip }, mesh: m, seam: seamU };
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
      var inA, inB, k = (y * PW + x) * 4, t;
      if (opt.W && st.ws && st.warp && g === st.rig) { var fl = sampleAB(st, pw[0], pw[1], qa, qb); inA = !!(fl & 1); inB = !!(fl & 2); }
      else { inA = toA(g, r, 0, qa); inB = opt.W && st.ws ? toBrow(st, r, pw[1], qb) : opt.knots ? toB(g, r, 2 * knotWr(st, opt.knots, r), qb) : toB(g, r, 0, qb); }
      if (!inA && !inB) { img[k + 3] = 255; continue; }
      if (opt.seam && st.ws && inA && inB) { var sxx = st.ws.seam[Math.max(0, Math.min(wc.CH - 1, Math.round(pw[1])))], bw = st.warp ? HB / 2 : 0.5, dd = st.aLeft ? pw[0] - sxx : sxx - pw[0]; t = Math.max(0, Math.min(1, 0.5 + dd / (2 * bw))); }
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
      if (toB(g, r, sl.knots ? 2 * knotWr(st, sl.knots, r) : 0, qb)) { for (ch = 0; ch < 3; ch++) lb[k + ch] = colAt(st.inp.Bc, qb[0], qb[1], ch); lb[k + 3] = 255; }
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
  // Lens B settling row by row at the band distances: layers of both lenses as if everything were far, and per
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
  // How well both lenses match over the shared part (correlation of brightness), B with or without the per-row distances.
  function stripMatch(st, useW) {
    var c = st.cyl, g = st.rig, r = [0, 0, 0], qa = [0, 0], qb = [0, 0], sa = 0, sb = 0, saa = 0, sbb = 0, sab = 0, n = 0;
    for (var y = 0; y < c.CH; y += 2) for (var x = 0; x < c.CW; x += 2) {
      cylRay(c, x, y, r); if (!toA(g, r, 0, qa)) continue; var ok = useW ? toBrow(st, r, y, qb) : toB(g, r, 0, qb); if (!ok) continue;
      var a = bil(st.inp.A, LW, LH, qa[0], qa[1]), b = bil(st.inp.B, LW, LH, qb[0], qb[1]); if (a !== a || b !== b) continue;
      sa += a; sb += b; saa += a * a; sbb += b * b; sab += a * b; n++;
    }
    return (sab / n - sa / n * sb / n) / Math.sqrt((saa / n - (sa / n) * (sa / n)) * (sbb / n - (sb / n) * (sb / n)));
  }
  // The person tapped the final view at (fx, fy) (parts of its width and height): the seam goes near there.
  function seamAt(st, fx, fy) {
    var V = st.view.cyl, cr = st.view.final || st.view.crop, r = [0, 0, 0];
    cylRay(V, cr.x0 + fx * (cr.x1 - cr.x0), cr.y0 + fy * (cr.y1 - cr.y0), r);
    st.o.seamTarget = cylOf(st.cyl, r)[0];
    seam(st, null); if (st.o.warp !== false) seamWarp(st, null); framing(st);
  }
  return { CAM: CAM, seamAt: seamAt, slideData: slideData, stripMatch: stripMatch, run: run, setClip: setClip, seam: seam, mesh: mesh, pack: pack, preview: preview, Rig: Rig, epi: epi, LW: LW, LH: LH };
})();

// ---- Web Worker side: the page sends the pictures; pictures of the work go back as it runs. ----
if (typeof WorkerGlobalScope !== 'undefined' && typeof self !== 'undefined') {
  var state = null, PW = 720, lastShow = 0;
  // Every step of the work is one frame; the page plays them at their own pace (hold ms each, fade = cross-fade).
  var post = function (opt) {
    if (opt.textOnly) { self.postMessage({ t: 'show', kind: 'text', text: opt.text, frac: opt.frac }); return; }
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
      else if (m.t === 'seamAt' && state && state.view) { Calib.seamAt(state, m.fx, m.fy); finish(); }
      else if (m.t === 'start') {
        if (m.inp) { PW = m.pw || 720; state = { inp: m.inp, o: m.o }; }
        state.o.start = m.start || null; state.o.seamTarget = null; state.rowKnots = null; state.cyl = null; state.viewCyl = null; state.viewCrop = null;
        Calib.run(state, post);
        finish();
      }
    } catch (ex) { self.postMessage({ t: 'error', text: String(ex.message || ex) }); }
  };
  var finish = function () {
    var ms = Calib.mesh(state), buf = Calib.pack(ms);
    post({ stage: 7, cylObj: state.view.cyl, crop: state.view.final || state.view.crop, text: 'Listo: paneo ' + (state.rig.pan * 180 / Math.PI).toFixed(1) + '°, inclinacion ' + (state.rig.tilt * 180 / Math.PI).toFixed(1) + '°, error tipico ' + state.matchErr.toFixed(1) + ' px, ' + state.matches.length + ' puntos' +
      (state.weak && state.weak.length ? '. OJO, poca confianza: ' + state.weak.join('; ') + '. Si se ve mal, calibrá de nuevo cuando no haya nadie cerca.' : ''),
      frac: 1, rig: state.rig, W: true, seam: true }, true);
    var cr0 = state.view.final || state.view.crop, fs = Calib.preview(state, PW, { stage: 7, rig: state.rig, W: true, seam: true, cylObj: state.view.cyl, crop: cr0 }), fm = Calib.preview(state, PW, { stage: 7, rig: state.rig, W: true, mix: true, cylObj: state.view.cyl, crop: cr0 });
    self.postMessage({ t: 'final', w: fs.w, h: fs.h, seam: fs.data, mix: fm.data, seamLine: fs.seam }, [fs.data.buffer, fm.data.buffer]);
    self.postMessage({ t: 'done', mesh: buf, OW: ms.head.OW, OH: ms.head.OH }, [buf]);
  };
}
if (typeof module !== 'undefined' && module.exports) module.exports = Calib;
