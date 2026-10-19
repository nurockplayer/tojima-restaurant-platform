#!/usr/bin/env node
// Before/after comparison for the DS-2 acceptance run. Zero dependencies.
//   node compare.mjs <beforeDir> <afterDir> [out.md]
// Each dir holds report-before.json / report-after.json and shots/.  Prints (and optionally writes) a markdown table of the
// guest metrics side by side, plus a pixel diff of every DS-1 regression screenshot (staff / KDS / catalog) that exists on both sides.
import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { inflateSync } from 'node:zlib';

const [beforeDir, afterDir, outFile] = process.argv.slice(2);
if (!beforeDir || !afterDir) { console.error('usage: node compare.mjs <beforeDir> <afterDir> [out.md]'); process.exit(2); }
const load = (d, l) => JSON.parse(readFileSync(join(d, `report-${l}.json`), 'utf8'));
const B = load(beforeDir, 'before'), A = load(afterDir, 'after');

// ---- minimal PNG decoder (8-bit RGB/RGBA, non-interlaced: what Chrome screenshots are) ----
function readPng(file) {
  const buf = readFileSync(file);
  let pos = 8, w = 0, h = 0, ct = 0; const idat = [];
  while (pos < buf.length) {
    const len = buf.readUInt32BE(pos), type = buf.toString('ascii', pos + 4, pos + 8), data = buf.subarray(pos + 8, pos + 8 + len);
    if (type === 'IHDR') { w = data.readUInt32BE(0); h = data.readUInt32BE(4); ct = data[9]; if (data[8] !== 8 || data[12] !== 0) throw new Error('unsupported png'); }
    else if (type === 'IDAT') idat.push(data);
    pos += 12 + len;
  }
  const bpp = ct === 6 ? 4 : ct === 2 ? 3 : 0; if (!bpp) throw new Error('unsupported color type ' + ct);
  const raw = inflateSync(Buffer.concat(idat)); const stride = w * bpp; const px = Buffer.alloc(h * stride);
  for (let y = 0; y < h; y++) {
    const f = raw[y * (stride + 1)];
    for (let x = 0; x < stride; x++) {
      const v = raw[y * (stride + 1) + 1 + x];
      const a = x >= bpp ? px[y * stride + x - bpp] : 0, b = y > 0 ? px[(y - 1) * stride + x] : 0, c = x >= bpp && y > 0 ? px[(y - 1) * stride + x - bpp] : 0;
      let r;
      if (f === 0) r = v; else if (f === 1) r = v + a; else if (f === 2) r = v + b; else if (f === 3) r = v + ((a + b) >> 1);
      else { const p = a + b - c, pa = Math.abs(p - a), pb = Math.abs(p - b), pc = Math.abs(p - c); r = v + (pa <= pb && pa <= pc ? a : pb <= pc ? b : c); }
      px[y * stride + x] = r & 255;
    }
  }
  return { w, h, bpp, px };
}
function diffPng(fa, fb) {
  const a = readPng(fa), b = readPng(fb);
  if (a.w !== b.w || a.h !== b.h) return { sizeChanged: `${a.w}x${a.h} -> ${b.w}x${b.h}` };
  let n = 0; const total = a.w * a.h;
  for (let i = 0; i < total; i++) { let m = 0; for (let k = 0; k < Math.min(a.bpp, 3); k++) m = Math.max(m, Math.abs(a.px[i * a.bpp + k] - b.px[i * b.bpp + k])); if (m > 16) n++; }
  return { differingPct: Math.round((n / total) * 10000) / 100, differingPx: n };
}

const L = [];
L.push('# DS-2 before / after comparison', '');
L.push(`before: ${B.meta.at} (${B.meta.base}); after: ${A.meta.at} (${A.meta.base})`, '');
L.push('| combo | metric | before | after |', '|---|---|---|---|');
const val = (c, f) => { try { return f(c); } catch { return '-'; } };
const metrics = [
  ['first product top (px)', (c) => c.states['menu-first-viewport'].measure.rows.firstTop],
  ['fully visible rows (first viewport)', (c) => c.states['menu-first-viewport'].measure.rows.fullyInFirstViewport],
  ['median / max row height (px)', (c) => c.states['menu-first-viewport'].measure.rows.height.median + ' / ' + c.states['menu-first-viewport'].measure.rows.height.max],
  ['Tab presses to first product action', (c) => c.ux.tabsToFirstProductActionEmptyCart],
  ['sticky/fixed elements (menu)', (c) => c.states['menu-first-viewport'].measure.sticky.map((s) => s.el.split('.').slice(0, 2).join('.')).join(', ') || '0'],
  ['AA failures (all states)', (c) => Object.values(c.states).filter((s) => s && s.audit).reduce((n, s) => n + s.audit.fails.length, 0)],
  ['undersized targets (counted states)', (c) => Object.values(c.states).filter((s) => s && s.targets && !s.notApplicable).reduce((n, s) => n + s.targets.fails, 0)],
  ['horizontal overflow states', (c) => Object.values(c.states).filter((s) => s && s.measure && s.measure.overflow.over).length],
  ['topbar platform names / Powered by Tojima', (c) => { const t = c.states['menu-first-viewport'].measure.topbar; return `${t.platformNamesInTopbar} / ${t.poweredByTojima}`; }],
  ['ORDER_READ language button', (c) => { const b = c.ux.orderReadLangButton; return b ? `${b.w}x${b.h}` : '-'; }],
  ['DEMO notices (page)', (c) => c.states['menu-first-viewport'].measure.demo.page],
  ['root font-family', (c) => c.states['menu-first-viewport'].measure.fonts.root.slice(0, 28)],
];
for (const tag of Object.keys(A.combos)) for (const [name, f] of metrics) L.push(`| ${tag} | ${name} | ${val(B.combos[tag], f)} | ${val(A.combos[tag], f)} |`);
L.push('');
if (B.js && A.js) L.push(`Guest JS: before ${B.js.decodedTotal} B / ${B.js.gzipTotalViteKB} KB gzip; after ${A.js.decodedTotal} B / ${A.js.gzipTotalViteKB} KB gzip (budget 125 KB).`, '');

// ---- DS-1 regression ----
L.push('## DS-1 regression (staff / KDS / catalog): before vs after');
const sb = B.ds1?.summary, sa = A.ds1?.summary;
L.push('', `before: ${sb ? `${sb.states} states, AA ${sb.aaFailures}, overflow ${sb.overflowStates.length}` : 'n/a'}; after: ${sa ? `${sa.states} states, AA ${sa.aaFailures}, overflow ${sa.overflowStates.length}` : 'n/a'}`, '');
L.push('| state | pixels differing (>16/255 per channel) | note |', '|---|---|---|');
for (const [name, v] of Object.entries(A.ds1?.states || {})) {
  if (!v?.shot || !B.ds1?.states?.[name]?.shot) continue;
  const fa = join(beforeDir, B.ds1.states[name].shot), fb = join(afterDir, v.shot);
  if (!existsSync(fa) || !existsSync(fb)) continue;
  try {
    const d = diffPng(fa, fb);
    L.push(`| ${name} | ${d.sizeChanged ? 'size ' + d.sizeChanged : d.differingPct + '%'} | ${d.differingPct === 0 ? 'identical' : d.differingPct < 1 ? 'tiny (timestamps / ids / row state?)' : 'review the pair'} |`);
  } catch (e) { L.push(`| ${name} | error | ${e.message} |`); }
}
L.push('', 'Differences below ~1% on staff/KDS pages are usually timestamps, order ids and accepted-order state from the guest flows that ran first; open the pair of PNGs to confirm no visual change.');
const md = L.join('\n') + '\n';
console.log(md);
if (outFile) writeFileSync(outFile, md);
