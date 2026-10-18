import { describe, expect, it } from 'vitest';
import { fontExceptions, hexInventory, unsupportedInventory } from './tokens.inventory';

// A bounded source audit, not a browser cascade/compositing engine. See ADR-0007.
const sources = import.meta.glob<string>('../**/*.css', {
  query: '?raw', import: 'default', eager: true,
});
const expectedFiles = [
  './tokens.css', '../ui/styles.css', '../ui/CatalogApp.module.css',
  '../ui/OrderRead.module.css', '../ui/DemoBanner.module.css',
];
const normal = (value: string) => value.trim().replace(/\s+/g, ' ');
type Declaration = { property: string; value: string };
type Rule = { file: string; context: string; selector: string; declarations: Declaration[] };

// Split only outside strings and functions. Reject unbalanced/unsupported syntax
// rather than dropping declarations that might contain a regression.
function split(source: string, delimiter: string): string[] {
  const parts: string[] = [];
  let start = 0;
  let depth = 0;
  let quote = '';
  for (let i = 0; i < source.length; i++) {
    const c = source[i]!;
    if (c === '\\') { i++; continue; }
    if (quote) { if (c === quote) quote = ''; continue; }
    if (c === '"' || c === "'") { quote = c; continue; }
    if (c === '(') depth++;
    if (c === ')' && --depth < 0) throw new Error('Unbalanced CSS function');
    if (c === delimiter && depth === 0) { parts.push(source.slice(start, i)); start = i + 1; }
  }
  if (quote || depth) throw new Error('Unbalanced CSS string/function');
  parts.push(source.slice(start));
  return parts;
}

function rules(file: string, source: string, context = ''): Rule[] {
  source = source.replace(/\/\*[\s\S]*?\*\//g, '');
  const result: Rule[] = [];
  let start = 0;
  let opening = -1;
  let depth = 0;
  let quote = '';
  for (let i = 0; i < source.length; i++) {
    const c = source[i]!;
    if (c === '\\') { i++; continue; }
    if (quote) { if (c === quote) quote = ''; continue; }
    if (c === '"' || c === "'") { quote = c; continue; }
    if (c === '{') { if (depth++ === 0) opening = i; }
    if (c !== '}') continue;
    if (--depth < 0) throw new Error(`${file}: unbalanced CSS block`);
    if (depth !== 0) continue;
    const selector = normal(source.slice(start, opening));
    const body = source.slice(opening + 1, i);
    if (selector.startsWith('@media ') || selector.startsWith('@media(')) {
      result.push(...rules(file, body, [context, selector].filter(Boolean).join(' / ')));
    } else {
      if (!selector || /[{};]/.test(selector) || selector.startsWith('@') || /[{}]/.test(body)) {
        throw new Error(`${file}: unsupported CSS rule ${selector}`);
      }
      const declarations = split(body, ';').filter((d) => d.trim()).map((d) => {
        const colon = d.indexOf(':');
        const property = d.slice(0, colon).trim();
        const value = normal(d.slice(colon + 1)).replace(/\s*!important$/, '');
        if (colon < 1 || !/^(--[\w-]+|[a-z-]+)$/.test(property) || !value) {
          throw new Error(`${file} ${selector}: unsupported declaration ${d}`);
        }
        return { property, value };
      });
      result.push({ file, context, selector, declarations });
    }
    start = i + 1;
  }
  if (depth || quote || source.slice(start).trim()) throw new Error(`${file}: unsupported/unbalanced CSS`);
  return result;
}

// Vitest's disabled CSS transformer can return a module stub, not just ''.
// Leave those for the named raw-import assertion; never let parsing mask it.
const allRules = Object.entries(sources).flatMap(([file, source]) =>
  typeof source === 'string' && source.trim() ? rules(file, source) : []);
const location = (rule: Rule) => [rule.file, rule.context, rule.selector].join(' | ');
const declarations = (rule: Rule, property: string) => rule.declarations.filter((d) => d.property === property);
const definitions = new Map(allRules.filter((r) => r.selector === ':root' && !r.context)
  .flatMap((r) => r.declarations.filter((d) => d.property.startsWith('--')).map((d) => [d.property, d.value] as const)));

function resolve(value: string, seen: string[] = []): string {
  const ref = value.match(/^var\((--[\w-]+)\)$/);
  if (!ref) return value;
  const name = ref[1]!;
  const target = definitions.get(name);
  if (target === undefined) throw new Error(`unknown var() token ${name}`);
  if (seen.includes(name)) throw new Error(`cyclic var() token ${[...seen, name].join(' -> ')}`);
  return resolve(target, [...seen, name]);
}

function hex(value: string): string {
  const resolved = resolve(value);
  if (!/^#(?:[\da-f]{3}|[\da-f]{6})$/i.test(resolved)) throw new Error(`unsupported colour ${value} -> ${resolved}`);
  return resolved;
}
function luminance(value: string): number {
  let h = hex(value).slice(1);
  if (h.length === 3) h = [...h].map((c) => c + c).join('');
  const [r, g, b] = [0, 2, 4].map((i) => {
    const channel = Number.parseInt(h.slice(i, i + 2), 16) / 255;
    return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  });
  return .2126 * r! + .7152 * g! + .0722 * b!;
}
function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi! + .05) / (lo! + .05);
}
function fontPixels(value: string): number {
  const match = value.match(/^(\d+(?:\.\d+)?|\.\d+)(px|rem)$/);
  if (!match) throw new Error(`unsupported font-size: ${value}; only px/rem (15px root) are supported`);
  return Number(match[1]) * (match[2] === 'rem' ? 15 : 1);
}

const usagePairs: [string, string, number][] = [
  ['--color-text', '--color-bg', 4.5],
  ['--color-text', '--color-surface', 4.5],
  ['--color-text-muted', '--color-bg', 4.5],
  ['--color-text-muted', '--color-surface', 4.5],
  ['--color-text-muted', '--color-surface-sunken', 4.5],
  ['--color-price', '--color-surface', 4.5],
  ['--color-on-action', '--color-action', 4.5],
  ['--color-on-action', '--color-action-hover', 4.5],
  ['--color-action-soft-ink', '--color-action-soft', 4.5],
  ['--color-action', '--color-surface', 3],
  ['--color-border-control', '--color-bg', 3],
  ['--color-border-control', '--color-surface', 3],
  ['--color-border-control', '--ref-neutral-25', 3],
  ['--color-focus', '--color-bg', 3],
  ['--color-focus', '--color-surface', 3],
  ['--color-focus', '--ref-neutral-25', 3],
  ['--color-success-ink', '--color-success-bg', 4.5],
  ['--color-warning-ink', '--color-warning-bg', 4.5],
  ['--color-danger-ink', '--color-danger-bg', 4.5],
  ['--color-info-ink', '--color-info-bg', 4.5],
  ['--color-danger-ink', '--color-surface', 4.5],
  ['--brand-on-primary', '--brand-primary', 4.5],
  ['--brand-ink', '--color-bg', 4.5],
  ['--brand-ink', '--color-surface', 4.5],
  ['--brand-focus', '--brand-primary', 3],
];

// An exception keys the entire colour/opacity signature so changing even one
// participating value invalidates the exception. Duplicates remain significant.
function unsupportedKey(rule: Rule, why: string): string {
  const signature = rule.declarations.filter((d) => ['color', 'background', 'background-color', 'background-image', 'opacity'].includes(d.property))
    .map((d) => `${d.property}:${d.value}`).join(';');
  return `${location(rule)} | ${signature} | ${why}`;
}
function textAudit(input: Rule[]) {
  const unsupported: string[] = [];
  const failures: string[] = [];
  let checked = 0;
  for (const rule of input) {
    const colors = declarations(rule, 'color');
    const backgrounds = rule.declarations.filter((d) => ['background', 'background-color', 'background-image'].includes(d.property));
    for (const d of declarations(rule, 'opacity')) unsupported.push(unsupportedKey(rule, `opacity requires compositing (${d.value})`));
    if (!colors.length && backgrounds.length) {
      // Background-only rules can change contrast of inherited foregrounds.
      // Still validate the value, even though the combination needs a browser.
      for (const bg of backgrounds) {
        try { hex(bg.value); }
        catch (error) { unsupported.push(unsupportedKey(rule, String(error))); }
      }
      unsupported.push(unsupportedKey(rule, 'background with inherited foreground'));
      continue;
    }
    if (colors.length > 1 || backgrounds.length > 1) {
      unsupported.push(unsupportedKey(rule, 'multiple colour/background declarations require cascade'));
      continue;
    }
    for (const color of colors) {
      try {
        hex(color.value);
        const bgs = backgrounds.length ? backgrounds.map((d) => d.value) : ['var(--color-bg)', 'var(--color-surface)'];
        for (const bg of bgs) {
          const ratio = contrast(color.value, bg);
          checked++;
          if (ratio < 4.5) failures.push(`${location(rule)} | ${color.value} on ${bg}: ${ratio.toFixed(2)} < 4.5`);
        }
      } catch (error) { unsupported.push(unsupportedKey(rule, String(error))); }
    }
  }
  return { unsupported, failures, checked };
}

function literalInventory(input: Rule[]) {
  return input.flatMap((rule) => rule.declarations.flatMap((d) => {
    if (rule.file === './tokens.css' && rule.selector === ':root' && !rule.context && d.property.startsWith('--ref-')) return [];
    return [...d.value.matchAll(/#[\da-f]+\b/gi)].map(([value]) => `${location(rule)} | ${d.property} | ${value.toLowerCase()}`);
  })).sort();
}

function reviewed(actual: string[], inventory: { key: string; reason: string }[], label: string) {
  expect(inventory.every((item) => item.reason.trim().length > 20), `${label}: every exception needs a review reason`).toBe(true);
  const expected = inventory.map((item) => item.key).sort();
  const unexpected = actual.filter((key) => !expected.includes(key));
  expect(unexpected, `${label}: unreviewed CSS (add an exact, reasoned exception only after review)\n${unexpected.join('\n')}`).toEqual([]);
  expect([...actual].sort(), `${label}: stale/duplicate exceptions must be updated`).toEqual(expected);
}

describe('DS-1 static design-token regression gate', () => {
  it('loads every expected CSS file as a non-empty raw string', () => {
    expect(Object.keys(sources)).toEqual(expect.arrayContaining(expectedFiles));
    for (const [file, css] of Object.entries(sources)) {
      expect(typeof css, `${file}: CSS raw import must be a non-empty string; retain test.css.include for ?raw in vite.config.ts`).toBe('string');
      expect(css.trim().length, `${file}: empty CSS; retain test.css.include for ?raw in vite.config.ts`).toBeGreaterThan(0);
    }
    expect(allRules.length).toBeGreaterThan(0);
  });

  it('meets 4.5:1 text and 3:1 control/focus usage pairs', () => {
    for (const [fg, bg, minimum] of usagePairs) {
      expect(contrast(`var(${fg})`, `var(${bg})`), `${fg} / ${bg} must reach ${minimum}:1`).toBeGreaterThanOrEqual(minimum);
    }
    const stepper = allRules.find((r) => r.file === '../ui/styles.css' && r.selector === '.stepper button' && !r.context);
    const background = stepper && declarations(stepper, 'background')[0]?.value;
    expect(background, 'Inset stepper focus must contrast with the button fill').toBeDefined();
    expect(contrast('var(--color-focus)', background!)).toBeGreaterThanOrEqual(3);
  });

  it('overrides UA small shrinking with an unconditional base rule of at least 12px', () => {
    const base = allRules.filter((r) => r.file === '../ui/styles.css' && !r.context && split(r.selector, ',').map(normal).includes('small'));
    const sizes = base.flatMap((r) => declarations(r, 'font-size'));
    expect(sizes.length, 'Shared base small rule must override the UA font-size: smaller').toBeGreaterThan(0);
    const value = sizes.at(-1)!.value;
    expect(value).toBe('var(--text-meta)');
    expect(fontPixels(resolve(value)), 'Base small size must resolve to at least 12px').toBeGreaterThanOrEqual(12);
  });

  it('keeps every font-size at 12px or above with only reviewed 0/initial exceptions', () => {
    expect(definitions.get('--text-meta')).toBe('12px');
    expect(allRules.find((r) => r.selector === ':root' && declarations(r, 'font-size').length)?.declarations)
      .toContainEqual({ property: 'font-size', value: '15px' });
    const exceptions: string[] = [];
    const failures: string[] = [];
    for (const rule of allRules) for (const d of declarations(rule, 'font-size')) {
      const key = `${location(rule)} | font-size | ${d.value}`;
      if (d.value === '0' || d.value === 'initial') { exceptions.push(key); continue; }
      try {
        const pixels = fontPixels(resolve(d.value));
        if (pixels < 12) failures.push(`${key}: ${pixels}px < 12px (rem base 15px)`);
      } catch (error) { failures.push(`${key}: ${String(error)}`); }
    }
    expect(failures, failures.join('\n')).toEqual([]);
    reviewed(exceptions, fontExceptions, 'font-size');
  });

  it('meets text AA in the supported grammar and inventories every unsupported combination', () => {
    const audit = textAudit(allRules);
    expect(audit.failures, `AA-failing text pairs:\n${audit.failures.join('\n')}`).toEqual([]);
    reviewed(audit.unsupported, unsupportedInventory, 'text contrast');
    expect(audit.checked).toBeGreaterThan(100);
  });

  it('fixes every non-primitive literal hex occurrence to the reviewed inventory', () => {
    const actual = literalInventory(allRules);
    const additions = actual.filter((key) => !hexInventory.includes(key));
    expect(additions, `New hex outside reviewed inventory:\n${additions.join('\n')}`).toEqual([]);
    expect(actual, 'Deleted/duplicate hex occurrences require an inventory update').toEqual([...hexInventory].sort());
  });

  it('has no undefined variables or scoped colour-token overrides', () => {
    const failures: string[] = [];
    for (const rule of allRules) for (const d of rule.declarations) {
      for (const [, name] of d.value.matchAll(/var\(\s*(--[\w-]+)/g)) {
        if (!name || !definitions.has(name)) failures.push(`${location(rule)}: undefined variable ${name}`);
        else resolve(`var(${name})`);
      }
      if (d.property.startsWith('--') && !(rule.selector === ':root' && !rule.context)
        && !['--font-sans', '--motion-fast', '--motion-sheet'].includes(d.property)) {
        failures.push(`${location(rule)}: unsupported scoped token ${d.property}`);
      }
    }
    expect(failures, failures.join('\n')).toEqual([]);
  });
});

describe('static gate grammar safeguards', () => {
  it('uses WCAG sRGB luminance and expands short hex', () => {
    expect(contrast('#000', '#fff')).toBe(21);
    expect(contrast('#fff', '#ffffff')).toBe(1);
    expect(contrast('#748079', '#f4f4ee')).toBeCloseTo(3.73, 2);
  });
  it.each(['11px', '.75rem'])('converts and rejects the below-floor size %s', (value) => {
    expect(fontPixels(value)).toBeLessThan(12);
  });
  it.each(['calc(12px - 1px)', 'clamp(10px, 2vw, 14px)', '1em', '80%', 'var(--text-meta)'])('rejects unresolved or unsupported font-size %s', (value) => {
    expect(() => fontPixels(value)).toThrow('unsupported font-size');
  });
  it.each(['var(--unknown)', 'currentColor', 'color-mix(in srgb, #fff, #000)', 'inherit'])('reports unsupported text colour %s', (value) => {
    const result = textAudit(rules('fixture.css', `.fixture { color: ${value}; }`));
    expect(result.unsupported).toHaveLength(1);
    expect(result.unsupported[0]).toContain(value);
  });
  it('rejects background images rather than assuming the page background', () => {
    const audit = textAudit(rules('fixture.css', '.a { color: #000; background-image: linear-gradient(#fff, #000); }'));
    expect(audit.unsupported).toHaveLength(1);
    expect(audit.unsupported[0]).toContain('unsupported colour');
  });
  it('reports transparency, opacity and inherited foregrounds', () => {
    const audit = textAudit(rules('fixture.css', '.a { color: #000; background: transparent; } .b { opacity: .5; } .c { background: #fff; }'));
    expect(audit.unsupported).toHaveLength(3);
  });
  it.each(['@import "other.css";', '.x { .y { color: #fff; } }', '@supports (display:grid) { .x { color:#000; } }', '.x { font-size }'])('fails closed on unhandled CSS structure %s', (css) => {
    expect(() => rules('fixture.css', css)).toThrow(/unsupported/);
  });
});
