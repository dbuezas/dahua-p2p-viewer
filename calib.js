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
  // The camera itself (same for every calibration): lens shape, how lens A sits on the body, lens distance.
  // Found once from three pictures taken at different times (see README).
  var CAM = { F: 671.58, K: -0.2496, tA: -0.02703, base: 0.09 };
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
    var h = [MA[1] * o.base / 2, MA[4] * o.base / 2, MA[7] * o.base / 2];
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
  function epi(g, ax, ay, bx, by) {
    var rb = [0, 0, 0], q = [0, 0], R = g.R; unproject(g.L, bx, by, rb);
    var d0 = R[0] * rb[0] + R[3] * rb[1] + R[6] * rb[2], d1 = R[1] * rb[0] + R[4] * rb[1] + R[7] * rb[2], d2 = R[2] * rb[0] + R[5] * rb[1] + R[8] * rb[2];
    var b0 = 2 * g.h[0], b1 = 2 * g.h[1], b2 = 2 * g.h[2];
    function err(w) { return project(g.L, b0 * w + d0, b1 * w + d1, b2 * w + d2, q) ? Math.hypot(q[0] - ax, q[1] - ay) : 1e4; }
    var bw = 0, be = err(0), i;
    for (i = 1; i <= 24; i++) { var w = WMAX * i / 24, e = err(w); if (e < be) { be = e; bw = w; } }
    var lo = Math.max(0, bw - WMAX / 24), hi = Math.min(WMAX, bw + WMAX / 24);
    for (i = 0; i < 25; i++) { var m1 = lo + (hi - lo) * 0.382, m2 = lo + (hi - lo) * 0.618; if (err(m1) < err(m2)) hi = m2; else lo = m1; }
    var wm = (lo + hi) / 2, em = err(wm); return em < be ? { e: em, w: wm } : { e: be, w: bw };
  }

  // ---- the whole calibration ----
  // st.inp: { A, B: gray Float32Array 640x360; Ac, Bc: RGBA Uint8ClampedArray 640x360 }
  // st.o: { F, K, tA, base, clip (0..1), outW } ; show(msg): progress to the page
  function run(st, show) {
    for (var kc in CAM) if (st.o[kc] === undefined) st.o[kc] = CAM[kc];
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
    var cand = [], best = { v: -2, pan: 0, tilt: 0 };
    for (var pd = -100; pd <= 100; pd += 2) {
      for (var td = -36; td <= 36; td += 2) { var v = rough(pd * D, td * D, 0); cand.push({ v: v, pan: pd * D, tilt: td * D }); if (v > best.v) best = cand[cand.length - 1]; }
      if (pd % 10 === 0) show({ stage: 1, text: 'Buscando hacia donde mira la camara 4: ' + Math.round((pd + 100) / 2) + '%', frac: (pd + 100) / 200 * 0.15, rig: Rig(o, best.pan, best.tilt), mix: true });
    }
    cand.sort(function (a, b) { return b.v - a.v; });
    var tops = [];
    cand.forEach(function (c) { if (tops.length < 6 && tops.every(function (t) { return Math.abs(t.pan - c.pan) > 5 * D || Math.abs(t.tilt - c.tilt) > 5 * D; })) tops.push(c); });
    best = { v: -2, pan: 0, tilt: 0 };
    tops.forEach(function (c0) {
      var b0 = { v: rough(c0.pan, c0.tilt, 1), pan: c0.pan, tilt: c0.tilt };
      [2, 1, 0.5, 0.25].forEach(function (s) {
        var bb = b0;
        for (var pi = -2; pi <= 2; pi++) for (var ti = -2; ti <= 2; ti++) { var pan = bb.pan + pi * s * D, tilt = bb.tilt + ti * s * D, vv = rough(pan, tilt, 1); if (vv > b0.v) b0 = { v: vv, pan: pan, tilt: tilt }; }
      });
      if (b0.v > best.v) best = b0;
    });
    if (best.v < 0.5) throw new Error('las dos lentes no parecen ver la misma parte (la camara 4 mira muy lejos de la 3)');
    var g0 = Rig(o, best.pan, best.tilt);
    show({ stage: 1, text: 'Camara 4 (aprox., ajuste ' + best.v.toFixed(2) + '): paneo ' + (best.pan / D).toFixed(1) + '°, inclinacion ' + (best.tilt / D).toFixed(1) + '°', frac: 0.15, rig: g0, mix: true });

    // -- 2. shared corners --
    var resp = new Float32Array(LW * LH), cs = [];
    for (y = 2; y < LH - 2; y++) for (x = 2; x < LW - 2; x++) {
      var sxx = 0, syy = 0, sxy = 0;
      for (var j = -1; j <= 1; j++) for (var i = -1; i <= 1; i++) { var kk = (y + j) * LW + x + i, gx = B[kk + 1] - B[kk - 1], gy = B[kk + LW] - B[kk - LW]; sxx += gx * gx; syy += gy * gy; sxy += gx * gy; }
      resp[y * LW + x] = sxx * syy - sxy * sxy - 0.05 * (sxx + syy) * (sxx + syy);
    }
    var mg = Math.round(LH * 0.05), rb = [0, 0, 0];
    for (y = mg; y < LH - mg; y++) for (x = 12; x < LW - 12; x++) {
      var rv = resp[y * LW + x]; if (rv <= 0) continue; var top = true;
      for (j = -3; j <= 3 && top; j++) for (i = -3; i <= 3; i++) if (resp[(y + j) * LW + x + i] > rv) { top = false; break; }
      if (!top) continue;
      unproject(g0.L, x, y, rb); var R0 = g0.R;
      if (!project(g0.L, R0[0] * rb[0] + R0[3] * rb[1] + R0[6] * rb[2], R0[1] * rb[0] + R0[4] * rb[1] + R0[7] * rb[2], R0[2] * rb[0] + R0[5] * rb[1] + R0[8] * rb[2], q)) continue;
      if (q[0] < 20 || q[0] > LW - 20 || q[1] < 20 || q[1] > LH - 20) continue;
      cs.push([x, y, rv, q[0], q[1]]);
    }
    cs.sort(function (a, b) { return b[2] - a[2]; }); cs = cs.slice(0, 800);
    var A2 = shrink(A, LW, LH, 2), B2 = shrink(B, LW, LH, 2), w2 = LW / 2, h2 = LH / 2, Rs = Math.round(w2 * 0.09), matches = [];
    for (var ci = 0; ci < cs.length; ci++) {
      var c = cs[ci], px = Math.round(c[3] / 2), py = Math.round(c[4] / 2), bx = Math.round(c[0] / 2), by = Math.round(c[1] / 2);
      var bst = -2, second = -2, bax = 0, bay = 0;
      for (y = py - Rs; y <= py + Rs; y++) for (x = px - Rs; x <= px + Rs; x++) {
        var nv = ncc(A2, B2, w2, h2, x, y, bx, by, 6), far = Math.abs(x - bax) + Math.abs(y - bay) > 2;
        if (nv > bst) { if (far) second = bst; bst = nv; bax = x; bay = y; } else if (nv > second && far) second = nv;
      }
      if (bst >= 0.75 && bst - second >= 0.03) {
        var fb = -2, fx = 0, fy = 0;
        for (y = bay * 2 - 3; y <= bay * 2 + 3; y++) for (x = bax * 2 - 3; x <= bax * 2 + 3; x++) { nv = ncc(A, B, LW, LH, x, y, c[0], c[1], 7); if (nv > fb) { fb = nv; fx = x; fy = y; } }
        if (fb > 0.8) matches.push([fx, fy, c[0], c[1]]);
      }
      if (ci % 60 === 59 || ci === cs.length - 1) show({ stage: 2, text: 'Puntos en comun: ' + matches.length + ' (de ' + (ci + 1) + ' esquinas probadas)', frac: 0.15 + 0.2 * (ci + 1) / cs.length, rig: g0, mix: true, matches: matches });
    }
    if (matches.length < 8) throw new Error('muy pocos puntos en comun (' + matches.length + ')');

    // -- 3. fine pan/tilt: every pair may be at any distance, it only has to lie on its line --
    function cost(x) {
      var g = Rig(o, x[0], x[1]), es = matches.map(function (m) { return epi(g, m[0], m[1], m[2], m[3]).e; }).sort(function (a, b) { return a - b; });
      var use = Math.max(6, Math.floor(es.length * 0.9)), e = 0;
      for (var i = 0; i < use; i++) e += es[i] < 2 ? es[i] * es[i] : 4 * es[i] - 4;
      return e / use;
    }
    var fit = nelder(cost, [best.pan, best.tilt], [1 * D, 0.5 * D], 120, function (it, x) {
      if (it % 6 === 0) show({ stage: 3, text: 'Ajustando paneo/inclinacion: ' + (x[0] / D).toFixed(2) + '°, ' + (x[1] / D).toFixed(2) + '°', frac: 0.35 + 0.15 * it / 120, rig: Rig(o, x[0], x[1]), mix: true, matches: matches });
    });
    var g = Rig(o, fit.x[0], fit.x[1]);
    var errs = matches.map(function (m) { return epi(g, m[0], m[1], m[2], m[3]); });
    var sorted = errs.map(function (e) { return e.e; }).sort(function (a, b) { return a - b; });
    st.rig = g; st.matchErr = sorted[sorted.length >> 1]; st.matches = matches;
    show({ stage: 3, text: 'Paneo ' + (g.pan / D).toFixed(1) + '°, inclinacion ' + (g.tilt / D).toFixed(1) + '°; error tipico ' + st.matchErr.toFixed(1) + ' px', frac: 0.5, rig: g, mix: true, matches: matches });

    // -- 4. distances --
    cylinder(st);
    measure(st, show);
    show({ stage: 5, text: 'Buscando la costura', frac: 0.9, rig: g, W: true });
    seam(st, show);
    framing(st);
    show({ stage: 7, text: 'Listo', frac: 1, rig: g, W: true, frame: st.fr, seam: true });
    return st;
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
      if (gy % 3 === 2 || gy === NY - 1) show({ stage: 4, text: 'Midiendo distancias: fila ' + (gy + 1) + ' de ' + NY, frac: 0.5 + 0.25 * (gy + 1) / NY, rig: g, W: null, wdots: { step: step, NX: NX, known: known, wv: wv } });
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
      if (it % 300 === 299) show({ stage: 4, text: 'Suavizando distancias', frac: 0.75 + 0.1 * it / 1500, rig: g, W: W, grid: st.grid });
    }
    var kc = 0; for (i = 0; i < N; i++) kc += keep[i];
    st.measured = kc;
  }
  function wAt(gr, x, y) {
    var gx = Math.max(0, Math.min(gr.NX - 1.001, x / gr.step)), gy = Math.max(0, Math.min(gr.NY - 1.001, y / gr.step));
    var x0 = gx | 0, y0 = gy | 0, fx = gx - x0, fy = gy - y0, k = y0 * gr.NX + x0, W = gr.W;
    return (W[k] * (1 - fx) + W[k + 1] * fx) * (1 - fy) + (W[k + gr.NX] * (1 - fx) + W[k + gr.NX + 1] * fx) * fy;
  }
  // -- 5. seam, on the cylinder (half size) --
  // Each lens is shown from its own place, unbent: lens A as it is, lens B as it is from 9 cm lower. They only
  // have to agree ON the seam. So the seam is a path from top to bottom where, with B taken at the distance
  // measured there, both lenses look alike; and each row of B is taken at the distance of its seam point (B only
  // moves up or down a bit per row, never bends sideways). Near things away from the seam keep their straight lines.
  function seam(st, show) {
    var c = st.cyl, g = st.rig, gr = st.grid, S = 2, w = Math.floor(c.CW / S), h = Math.floor(c.CH / S);
    var la = new Float32Array(w * h), lb = new Float32Array(w * h), ok = new Uint8Array(w * h), wm = new Float32Array(w * h);
    var r = [0, 0, 0], qa = [0, 0], qb = [0, 0], x, y;
    for (y = 0; y < h; y++) for (x = 0; x < w; x++) {
      var k = y * w + x, cx = x * S + S / 2, cy = y * S + S / 2; cylRay(c, cx, cy, r);
      var wv = wAt(gr, cx, cy); wm[k] = wv;
      if (toA(g, r, 0, qa) && toB(g, r, 2 * wv, qb)) { ok[k] = 1; la[k] = bil(st.inp.A, LW, LH, qa[0], qa[1]); lb[k] = bil(st.inp.B, LW, LH, qb[0], qb[1]); }
    }
    // pixels of shift per unit of w on this half size canvas (B moves by the whole lens distance)
    var KW = c.Fc / S * Math.sqrt(4 * (g.h[0] * g.h[0] + g.h[1] * g.h[1] + g.h[2] * g.h[2]));
    var ea = gradMag(la, w, h), eb = gradMag(lb, w, h), INF = 1e18, prev = new Float64Array(w).fill(INF), from = new Int32Array(w * h), rowOk = new Uint8Array(h);
    for (y = 0; y < h; y++) {
      var cur = new Float64Array(w).fill(INF);
      for (x = 1; x < w - 1; x++) {
        var k2 = y * w + x; if (!ok[k2] || !ok[k2 - 1] || !ok[k2 + 1]) continue;
        var D = Math.abs(ea[k2] - eb[k2]) + 0.5;
        if (y === 0 || !rowOk[y - 1]) { cur[x] = D + (y > 0 && prev[x] < INF ? prev[x] : 0); from[k2] = x; continue; }
        var bv = INF, bx = -1;
        // going on from the row above: the distance along the path should change little (B moves per row)
        for (var d = -1; d <= 1; d++) { var xx = x + d, pv = prev[xx] + KW * Math.abs(wm[k2] - wm[(y - 1) * w + xx]); if (pv < bv) { bv = pv; bx = xx; } }
        if (bx >= 0 && bv < INF) { cur[x] = bv + D; from[k2] = bx; }
      }
      for (x = 0; x < w; x++) if (cur[x] < INF) { rowOk[y] = 1; break; }
      // a row where the lenses do not overlap: the path goes straight on
      if (!rowOk[y]) for (x = 0; x < w; x++) { cur[x] = prev[x]; from[y * w + x] = x; }
      prev = cur;
    }
    var bx2 = -1, bvv = INF; for (x = 0; x < w; x++) if (prev[x] < bvv) { bvv = prev[x]; bx2 = x; }
    var seamC = new Float32Array(c.CH), Ws = new Float32Array(c.CH);
    if (bx2 < 0) { seamC.fill(-1); st.ws = { seam: seamC, W: Ws }; st.aLeft = true; return; }
    var hx = new Float32Array(h), hw = [], hy = [];
    for (y = h - 1; y >= 0; y--) { hx[y] = bx2; if (rowOk[y]) { hy.push(y); hw.push(wm[y * w + bx2]); } bx2 = from[y * w + bx2]; }
    hy.reverse(); hw.reverse();
    // distance per row: the seam's, a middle value over nearby rows (one odd point must not jerk B), then averaged,
    // then limited so B never shifts more than 0.08 px per row (at most 8% taller or shorter somewhere)
    var hwS = new Float32Array(h), i, j;
    for (y = 0; y < h; y++) {
      // nearest rows with a seam point
      var near = []; for (i = 0; i < hy.length; i++) if (Math.abs(hy[i] - y) <= 10) near.push(hw[i]);
      if (!near.length) { var bi = 0; for (i = 1; i < hy.length; i++) if (Math.abs(hy[i] - y) < Math.abs(hy[bi] - y)) bi = i; near.push(hy.length ? hw[bi] : 0); }
      near.sort(function (a2, b2) { return a2 - b2; }); hwS[y] = near[near.length >> 1];
    }
    var hwA = new Float32Array(h); for (y = 0; y < h; y++) { var sm = 0, n = 0; for (j = Math.max(0, y - 25); j <= Math.min(h - 1, y + 25); j++) { sm += hwS[j]; n++; } hwA[y] = sm / n; }
    var lim = 0.08 / KW;   // w change per row for 0.08 px of shift per row (same ratio at any size)
    for (var pass = 0; pass < 2000; pass++) { var ch = false; for (y = 1; y < h; y++) { var df = hwA[y] - hwA[y - 1]; if (Math.abs(df) > lim) { var fx = (Math.abs(df) - lim) / 2 * Math.sign(df); hwA[y - 1] += fx; hwA[y] -= fx; ch = true; } } if (!ch) break; }
    for (y = 0; y < c.CH; y++) { var yy = Math.min(h - 1, Math.floor(y / S)); seamC[y] = hx[yy] * S + S / 2; Ws[y] = hwA[yy]; }
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
    if (!c || opt.stage < 4) { var keep = st.rig; st.rig = g; cylinder(st); c = st.cyl; st.rig = keep || g; }
    var OW = fr ? fr.OW : c.CW, OH = fr ? fr.OH : c.CH, s = OW / PW, PH = Math.max(1, Math.round(OH / s));
    var img = new Uint8ClampedArray(PW * PH * 4), p = [0, 0], r = [0, 0, 0], qa = [0, 0], qb = [0, 0];
    for (var y = 0; y < PH; y++) for (var x = 0; x < PW; x++) {
      var ox = (x + 0.5) * s, oy = (y + 0.5) * s;
      if (fr) outToCyl(fr, ox, oy, p); else { p[0] = ox; p[1] = oy; }
      cylRay(c, p[0], p[1], r);
      var inA = toA(g, r, 0, qa), inB = opt.W && st.ws ? toBrow(st, r, p[1], qb) : toB(g, r, 0, qb), k = (y * PW + x) * 4, t;
      if (!inA && !inB) { img[k + 3] = 255; continue; }
      if (opt.seam && st.ws && inA && inB) { var sxx = st.ws.seam[Math.max(0, Math.min(c.CH - 1, Math.round(p[1])))]; t = st.aLeft ? (p[0] > sxx ? 1 : 0) : (p[0] < sxx ? 1 : 0); }
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
        lines.push(pa[0] / s, pa[1] / s, pb[0] / s, pb[1] / s, e);
      });
    }
    var dots = [];
    if (opt.wdots && !fr) { var wd = opt.wdots; for (var i = 0; i < wd.known.length; i++) if (wd.known[i]) dots.push((i % wd.NX) * wd.step / s, Math.floor(i / wd.NX) * wd.step / s, wd.wv[i]); }
    return { w: PW, h: PH, data: img, lines: lines, dots: dots };
  }
  function colAt(img, x, y, ch) {
    var x0 = x | 0, y0 = y | 0, fx = x - x0, fy = y - y0, k = (y0 * LW + x0) * 4 + ch;
    return (img[k] * (1 - fx) + img[k + 4] * fx) * (1 - fy) + (img[k + LW * 4] * (1 - fx) + img[k + LW * 4 + 4] * fx) * fy;
  }
  return { run: run, setClip: setClip, seam: seam, mesh: mesh, pack: pack, preview: preview, Rig: Rig, epi: epi, LW: LW, LH: LH };
})();

// ---- Web Worker side: the page sends the pictures; pictures of the work go back as it runs. ----
if (typeof WorkerGlobalScope !== 'undefined' && typeof self !== 'undefined') {
  var state = null, PW = 720, lastShow = 0;
  var post = function (opt, force) {
    var now = Date.now(); if (!force && now - lastShow < 250) return; lastShow = now;
    var pv = Calib.preview(state, PW, opt);
    self.postMessage({ t: 'show', text: opt.text, frac: opt.frac, w: pv.w, h: pv.h, img: pv.data, lines: pv.lines, dots: pv.dots }, [pv.data.buffer]);
  };
  self.onmessage = function (e) {
    var m = e.data;
    try {
      if (m.t === 'start') {
        PW = m.pw || 720;
        state = { inp: m.inp, o: m.o };
        Calib.run(state, function (opt) { post(opt, opt.frac === 1); });
        finish();
      } else if (m.t === 'clip' && state) {
        Calib.setClip(state, m.clip); finish();
      }
    } catch (ex) { self.postMessage({ t: 'error', text: String(ex.message || ex) }); }
  };
  var finish = function () {
    var ms = Calib.mesh(state), buf = Calib.pack(ms);
    post({ stage: 7, text: 'Listo: paneo ' + (state.rig.pan * 180 / Math.PI).toFixed(1) + '°, inclinacion ' + (state.rig.tilt * 180 / Math.PI).toFixed(1) + '°, error tipico ' + state.matchErr.toFixed(1) + ' px, ' + state.matches.length + ' puntos, ' + state.measured + ' distancias', frac: 1, rig: state.rig, W: true, frame: state.fr, seam: true }, true);
    self.postMessage({ t: 'done', mesh: buf, OW: ms.head.OW, OH: ms.head.OH }, [buf]);
  };
}
if (typeof module !== 'undefined' && module.exports) module.exports = Calib;
