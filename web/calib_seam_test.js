// Seam test over many lens B positions: bun web/calib_seam_test.js <dir with NAME_A.raw NAME_B.raw (640x360 RGBA)> NAME...
// For each picture and each pan/tilt of lens B (also far from the right one: the seam must behave anyway), the seam
// is checked: present in every row where both lenses show, inside the shared part, the clock of lens A never on
// lens B's side, lens B's label never on its own side.
'use strict';
const fs = require('fs'), path = require('path');
const Calib = require(process.env.CALIBJS || './calib.js'), T = Calib._t, LW = Calib.LW, LH = Calib.LH;
const OSD = [0.60, 0.035, 1.0, 0.125], OSDB = [0, 0.90, 0.12, 1.0];
const dir = process.argv[2], names = process.argv.slice(3);
const gray = c => { const g = new Float32Array(LW * LH); for (let i = 0; i < g.length; i++) g[i] = 0.299 * c[4 * i] + 0.587 * c[4 * i + 1] + 0.114 * c[4 * i + 2]; return g; };
const D = Math.PI / 180;
let bad = 0, runs = 0;
for (const n of names) {
  const a = new Uint8Array(fs.readFileSync(path.join(dir, n + '_A.raw'))), b = new Uint8Array(fs.readFileSync(path.join(dir, n + '_B.raw')));
  for (let pan = -60; pan <= 0; pan += 6) for (let tilt = -20; tilt <= 10; tilt += 6) {
    const o = { clip: 1, outW: 1400 }; for (const k in Calib.CAM) o[k] = Calib.CAM[k];
    const st = { inp: { A: gray(a), B: gray(b), Ac: a, Bc: b }, o, rig: Calib.Rig(o, pan * D, tilt * D), weak: [] };
    T.cylinder(st); Calib.seam(st, null);
    const c = st.cyl, r = [0, 0, 0], qa = [0, 0], qb = [0, 0], errs = new Set();
    let shared = 0, noSeam = 0;
    // the rows the seam was worked out on (it works on a half-size grid: the middle of every second row)
    for (let y = 1; y < c.CH; y += 2) {
      let xl = -1, xr = -1;
      for (let x = 1; x < c.CW; x += 2) { T.cylRay(c, x, y, r); if (T.toA(st.rig, r, 0, qa) && T.toBrow(st, r, y, qb)) { if (xl < 0) xl = x; xr = x; } }
      if (xr - xl < 40) continue;   // no real shared part in this row (the thin tips at its ends do not count)
      shared++;
      const sx = st.ws.seam[y];
      // (one row at the very top or bottom of the shared part may miss it: the grid's edge)
      if (sx < 0) { if (++noSeam > 2) errs.add('sin costura'); continue; }
      if (sx < xl - 8 || sx > xr + 8) errs.add('costura fuera de la parte compartida');
      // what each point shows: A on A's side of the seam (where A sees), else B
      for (let x = 1; x < c.CW; x += 2) {
        T.cylRay(c, x, y, r);
        const seeA = T.toA(st.rig, r, 0, qa), seeB = T.toBrow(st, r, y, qb);
        const showsA = seeA && (!seeB || (st.aLeft ? x < sx : x > sx));
        if (seeA && !showsA && qa[0] >= OSD[0] * LW && qa[0] <= OSD[2] * LW && qa[1] >= OSD[1] * LH && qa[1] <= OSD[3] * LH && Math.abs(x - sx) > 12) errs.add('reloj tapado');
        if (seeB && !showsA && seeA && qb[0] >= OSDB[0] * LW && qb[0] <= OSDB[2] * LW && qb[1] >= OSDB[1] * LH && qb[1] <= OSDB[3] * LH && Math.abs(x - sx) > 12) errs.add('Device1 visible');
      }
    }
    runs++;
    if (errs.size) { bad++; console.log(n, 'paneo', pan, 'inclinacion', tilt, 'filas compartidas', shared, '->', [...errs].join(', ')); }
  }
}
console.log(runs + ' pruebas, ' + bad + ' con problemas');
process.exit(bad ? 1 : 0);
