// Tojima DS v0.1 contrast gate (research artifact, DRAFT · 待批准).
// Usage: node contrast-gate.mjs [tokens.css] [--json]
// 1. Resolves tokens.css per surface and checks every declared usage pair (WCAG 2.1).
// 2. Runs the store-brand gate on presets and 2,000 random colours and asserts
//    that every derived brand token set passes.
// Exit code 1 on any failure.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const fileArg = process.argv.slice(2).find((a) => !a.startsWith('--'));
const css = readFileSync(fileArg ?? join(here, 'tokens.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');

// ---- WCAG helpers (shared with the prototype page) ----
export function hexToRgb(hex) {
  if (!/^#([0-9a-f]{3}|[0-9a-f]{6})$/i.test(String(hex).trim())) throw new Error(`invalid colour: ${hex}`);
  const h = hex.trim().replace('#', '');
  const v = h.length === 3 ? h.split('').map((c) => c + c).join('') : h;
  return [0, 2, 4].map((i) => parseInt(v.slice(i, i + 2), 16));
}
export function luminance(hex) {
  const [r, g, b] = hexToRgb(hex).map((c) => {
    const s = c / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}
export function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}
function mix(hex, toward, t) {
  const a = hexToRgb(hex);
  const b = hexToRgb(toward);
  return '#' + a.map((c, i) => Math.round(c + (b[i] - c) * t).toString(16).padStart(2, '0')).join('');
}

// ---- Store brand gate ----
// Input: one store colour. Output: the only brand tokens a store can influence.
// on-primary: white or ink, whichever passes 4.5:1 (one always does).
// ink: the brand darkened (or lightened) until it reads as text on the page bg.
// focus: a ring colour that is >= 3:1 against the brand fill.
// Mid-luminance colours (e.g. #8a7f74) pass with neither white nor ink, so the
// gate darkens the fill toward ink until white text passes and reports `adjusted`.
export function deriveBrand(input, { bg = '#f6f6f1', surface = '#ffffff', ink = '#1b2a23', white = '#ffffff' } = {}) {
  let primary = input.toLowerCase();
  let adjusted = false;
  if (contrast(white, primary) < 4.5 && contrast(ink, primary) < 4.5) {
    for (let t = 0.05; t <= 1.0001 && contrast(white, primary) < 4.5; t += 0.05) primary = mix(input, ink, t);
    adjusted = true;
  }
  const onPrimary = contrast(white, primary) >= 4.5 ? white : ink;
  let brandInk = primary;
  for (let t = 0; t <= 1.0001 && (contrast(brandInk, bg) < 4.5 || contrast(brandInk, surface) < 4.5); t += 0.05) {
    brandInk = mix(primary, ink, t);
  }
  const focus = onPrimary;
  return { input, primary, adjusted, onPrimary, ink: brandInk, focus };
}
export function checkBrand(b, { bg = '#f6f6f1', surface = '#ffffff' } = {}) {
  return [
    ['brand on-primary text on fill', contrast(b.onPrimary, b.primary), 4.5],
    ['brand ink on page bg', contrast(b.ink, bg), 4.5],
    ['brand ink on surface', contrast(b.ink, surface), 4.5],
    ['focus ring on brand fill', contrast(b.focus, b.primary), 3],
  ];
}

// ---- Token resolution ----
function blocks(source) {
  const out = [];
  const re = /([^{}]+)\{([^{}]*)\}/g;
  let m;
  while ((m = re.exec(source))) {
    const selectors = m[1].trim();
    if (selectors.startsWith('@')) continue;
    const decls = {};
    for (const d of m[2].split(';')) {
      const i = d.indexOf(':');
      if (i < 0) continue;
      const k = d.slice(0, i).trim();
      if (k.startsWith('--')) decls[k] = d.slice(i + 1).trim();
    }
    out.push({ selectors: selectors.split(',').map((s) => s.trim()), decls });
  }
  return out;
}
const top = css.replace(/@media[^{]*\{(?:[^{}]*\{[^{}]*\})*[^{}]*\}/g, '');
const all = blocks(top);
function resolveSurface(surface) {
  const vars = {};
  for (const b of all) {
    if (b.selectors.includes(':root') || b.selectors.includes(`[data-surface="${surface}"]`)) Object.assign(vars, b.decls);
  }
  const get = (name, depth = 0) => {
    const raw = vars[name];
    if (raw === undefined) throw new Error(`${surface}: undefined token ${name}`);
    const ref = raw.match(/^var\((--[\w-]+)\)$/);
    if (ref && depth < 10) return get(ref[1], depth + 1);
    return raw;
  };
  return get;
}

// Usage pairs: [foreground, background, minimum ratio, why]
const PAIRS = [
  ['--color-text', '--color-bg', 4.5, 'body text'],
  ['--color-text', '--color-surface', 4.5, 'body text on card'],
  ['--color-text-muted', '--color-bg', 4.5, 'secondary text'],
  ['--color-text-muted', '--color-surface', 4.5, 'secondary text on card'],
  ['--color-text-muted', '--color-surface-sunken', 4.5, 'secondary text on sunken'],
  ['--color-price', '--color-surface', 4.5, 'price'],
  ['--color-on-action', '--color-action', 4.5, 'primary button label'],
  ['--color-on-action', '--color-action-hover', 4.5, 'primary button hover'],
  ['--color-action-soft-ink', '--color-action-soft', 4.5, 'selected chip'],
  ['--color-action', '--color-surface', 3, 'primary as non-text indicator'],
  ['--color-border-control', '--color-surface', 3, 'control boundary (1.4.11)'],
  ['--color-focus', '--color-bg', 3, 'focus ring on page'],
  ['--color-focus', '--color-surface', 3, 'focus ring on card'],
  ['--color-success-ink', '--color-success-bg', 4.5, 'status: success'],
  ['--color-warning-ink', '--color-warning-bg', 4.5, 'status: warning'],
  ['--color-danger-ink', '--color-danger-bg', 4.5, 'status: danger'],
  ['--color-info-ink', '--color-info-bg', 4.5, 'status: info'],
  ['--color-danger-ink', '--color-surface', 4.5, 'inline error text'],
];

let failures = 0;
const report = [];
for (const surface of ['guest', 'floor', 'office', 'kds']) {
  const get = resolveSurface(surface);
  for (const [fg, bg, min, why] of PAIRS) {
    const r = contrast(get(fg), get(bg));
    const ok = r >= min;
    if (!ok) failures++;
    report.push({ surface, pair: `${fg} / ${bg}`, why, ratio: +r.toFixed(2), min, ok });
  }
  // Size floors (px only; anything else is a failure, not a silent pass)
  const px = (name) => {
    const raw = get(name);
    if (!/^\d+(\.\d+)?px$/.test(raw)) throw new Error(`${surface}: ${name} must be a px length, got ${raw}`);
    return Number.parseFloat(raw);
  };
  const meta = px('--text-meta');
  const target = px('--size-target-min');
  const touch = surface !== 'office';
  if (meta < 12) { failures++; report.push({ surface, pair: '--text-meta', ratio: meta, min: 12, ok: false }); }
  if (touch && target < 44) { failures++; report.push({ surface, pair: '--size-target-min', ratio: target, min: 44, ok: false }); }
}

const presets = {
  'Tojima 既定 (#174d3c)': '#174d3c',
  '假想・深海藍 (#1d3b6e)': '#1d3b6e',
  '假想・朱紅 (#b3261e)': '#b3261e',
  '假想・芥末黃 (#f2c94c)': '#f2c94c',
  '假想・橘 (#e07a2f)': '#e07a2f',
};
const brandReport = [];
for (const [name, hex] of Object.entries(presets)) {
  const b = deriveBrand(hex);
  const checks = checkBrand(b);
  const ok = checks.every(([, r, min]) => r >= min);
  if (!ok) failures++;
  brandReport.push({ name, ...b, ok, checks: checks.map(([n, r, m]) => `${n} ${r.toFixed(2)}/${m}`) });
}
let seed = 20261019;
const rand = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
let fuzzFail = 0;
let fuzzAdjusted = 0;
const fuzzReasons = {};
for (let i = 0; i < 2000; i++) {
  const hex = '#' + [0, 0, 0].map(() => Math.floor(rand() * 256).toString(16).padStart(2, '0')).join('');
  const b = deriveBrand(hex);
  if (b.adjusted) fuzzAdjusted++;
  const bad = checkBrand(b).filter(([, r, min]) => r < min);
  if (bad.length) {
    fuzzFail++;
    for (const [n] of bad) fuzzReasons[n] = (fuzzReasons[n] ?? 0) + 1;
  }
}
failures += fuzzFail;

if (process.argv.includes('--json')) {
  console.log(JSON.stringify({ report, brandReport, fuzz: { samples: 2000, failures: fuzzFail, adjusted: fuzzAdjusted, reasons: fuzzReasons } }, null, 2));
} else {
  for (const r of report) console.log(`${r.ok ? 'PASS' : 'FAIL'}  ${r.surface.padEnd(6)} ${String(r.ratio).padStart(5)} >= ${r.min}  ${r.pair}  ${r.why ?? ''}`);
  for (const b of brandReport) console.log(`${b.ok ? 'PASS' : 'FAIL'}  brand  ${b.name} → fill=${b.primary}${b.adjusted ? ' (adjusted)' : ''} on=${b.onPrimary} ink=${b.ink} focus=${b.focus}`);
  console.log(`fuzz: 2000 random store colours, ${fuzzFail} failures, ${fuzzAdjusted} fills adjusted`, fuzzReasons);
  console.log(failures ? `\n${failures} FAILURE(S)` : '\nALL CHECKS PASS');
}
process.exit(failures ? 1 : 0);
