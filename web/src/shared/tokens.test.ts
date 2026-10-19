import { describe, expect, it } from 'vitest';
import { fontExceptions, hexInventory, unsupportedInventory } from './tokens.inventory';

// A bounded source audit, not a browser cascade/compositing engine. See ADR-0007.
const sources = import.meta.glob<string>('../**/*.css', {
  query: '?raw', import: 'default', eager: true,
});
const expectedFiles = [
  './tokens.css', '../ui/styles.css', '../ui/CatalogApp.module.css',
  '../ui/OrderRead.module.css', '../ui/DemoBanner.module.css',
  '../ui/guest/GuestSheet.module.css',
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

type Specificity = [number, number, number];
function compareSpecificity(a: Specificity, b: Specificity): number {
  return a[0] - b[0] || a[1] - b[1] || a[2] - b[2];
}

// Bounded selector grammar for the focus rules; fail on unsupported syntax.
// :where() contributes zero; :is() takes its most specific argument.
function specificity(selector: string): Specificity {
  const result: Specificity = [0, 0, 0];
  let rest = selector.trim();
  while (rest) {
    const token = rest.match(/^(?:(#[\w-]+)|(\.[\w-]+|\[[\w-]+\])|(:[\w-]+)|([a-z][\w-]*)|([\s>+~*]+))/i);
    if (!token) throw new Error(`unsupported specificity syntax: ${rest}`);
    rest = rest.slice(token[0].length);
    if (token[3] && rest.startsWith('(')) {
      const name = token[3];
      if (name !== ':is' && name !== ':where') throw new Error(`unsupported specificity function: ${name}`);
      let end = 0;
      let depth = 0;
      do {
        if (rest[end] === '(') depth++;
        if (rest[end] === ')') depth--;
        end++;
      } while (depth && end < rest.length);
      if (depth) throw new Error('Unbalanced specificity function');
      const args = split(rest.slice(1, end - 1), ',').map(specificity);
      const maximum = args.reduce((a, b) => compareSpecificity(a, b) > 0 ? a : b);
      if (name === ':is') for (const i of [0, 1, 2] as const) result[i] += maximum[i];
      rest = rest.slice(end);
    } else if (token[1]) result[0]++;
    else if (token[2] || token[3]) result[1]++;
    else if (token[4]) result[2]++;
  }
  return result;
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

const guestSelector = '[data-surface="guest"]';
const guestDimensions = new Map([
  ['--text-body', '16px'], ['--size-target', '48px'], ['--size-target-min', '44px'],
  ['--size-row', '56px'], ['--size-media', '72px'], ['--size-catnav', '48px'], ['--size-cartbar', '64px'],
]);
type TokenContext = 'root' | 'guest';
const guestDefinitions = new Map([...definitions, ...allRules
  .filter((r) => r.file === './tokens.css' && r.selector === guestSelector && !r.context)
  .flatMap((r) => r.declarations.map((d) => [d.property, d.value] as const))]);
const definitionsFor = (context: TokenContext) => context === 'guest' ? guestDefinitions : definitions;
const isGuestModule = (file: string) => /^\.\.\/ui\/guest\/[^/]+\.module\.css$/.test(file);
const contextFor = (file: string): TokenContext => isGuestModule(file) ? 'guest' : 'root';

function resolve(value: string, seen: string[] = [], context: TokenContext = 'root'): string {
  const ref = value.match(/^var\((--[\w-]+)\)$/);
  if (!ref) return value;
  const name = ref[1]!;
  const target = definitionsFor(context).get(name);
  if (target === undefined) throw new Error(`unknown var() token ${name}`);
  if (seen.includes(name)) throw new Error(`cyclic var() token ${[...seen, name].join(' -> ')}`);
  return resolve(target, [...seen, name], context);
}

function hex(value: string, context: TokenContext = 'root'): string {
  const resolved = resolve(value, [], context);
  if (!/^#(?:[\da-f]{3}|[\da-f]{6})$/i.test(resolved)) throw new Error(`unsupported colour ${value} -> ${resolved}`);
  return resolved;
}
function luminance(value: string, context: TokenContext = 'root'): number {
  let h = hex(value, context).slice(1);
  if (h.length === 3) h = [...h].map((c) => c + c).join('');
  const [r, g, b] = [0, 2, 4].map((i) => {
    const channel = Number.parseInt(h.slice(i, i + 2), 16) / 255;
    return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  });
  return .2126 * r! + .7152 * g! + .0722 * b!;
}
function contrast(a: string, b: string, context: TokenContext = 'root'): number {
  const [hi, lo] = [luminance(a, context), luminance(b, context)].sort((x, y) => y - x);
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
  ['--color-demo-ink', '--color-demo-bg', 4.5],
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
    const context = contextFor(rule.file);
    const colors = declarations(rule, 'color');
    const backgrounds = rule.declarations.filter((d) => ['background', 'background-color', 'background-image'].includes(d.property));
    for (const d of declarations(rule, 'opacity')) unsupported.push(unsupportedKey(rule, `opacity requires compositing (${d.value})`));
    if (!colors.length && backgrounds.length) {
      // Background-only rules can change contrast of inherited foregrounds.
      // Still validate the value, even though the combination needs a browser.
      for (const bg of backgrounds) {
        try { hex(bg.value, context); }
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
        hex(color.value, context);
        const bgs = backgrounds.length ? backgrounds.map((d) => d.value) : ['var(--color-bg)', 'var(--color-surface)'];
        for (const bg of bgs) {
          const ratio = contrast(color.value, bg, context);
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

function checkSources(input: Record<string, unknown>, expected: string[]) {
  for (const file of expected) if (!(file in input)) throw new Error(`${file}: missing CSS raw import`);
  for (const [file, css] of Object.entries(input)) {
    if (typeof css !== 'string' || !css.trim()) throw new Error(`${file}: CSS raw import must be a non-empty string; retain test.css.include for ?raw in vite.config.ts`);
  }
}

function tokenFailures(input: Rule[]): string[] {
  const failures: string[] = [];
  for (const rule of input) for (const d of rule.declarations) {
    const context = contextFor(rule.file);
    for (const [, name] of d.value.matchAll(/var\(\s*(--[\w-]+)/g)) {
      if (!name || !definitionsFor(context).has(name)) failures.push(`${location(rule)}: undefined variable ${name} in ${context}`);
      else {
        try { resolve(`var(${name})`, [], context); }
        catch (error) { failures.push(`${location(rule)}: ${String(error)}`); }
      }
    }
    if (!d.property.startsWith('--') || (rule.selector === ':root' && !rule.context)) continue;
    const guestDimension = rule.file === './tokens.css' && rule.selector === guestSelector && !rule.context && guestDimensions.has(d.property);
    const language = rule.file === './tokens.css' && rule.selector === ':lang(zh-Hant)' && !rule.context && d.property === '--font-sans';
    const reducedMotion = rule.file === './tokens.css' && rule.selector === ':root' && rule.context === '@media (prefers-reduced-motion: reduce)' && ['--motion-fast', '--motion-sheet'].includes(d.property);
    if (!guestDimension && !language && !reducedMotion) failures.push(`${location(rule)}: unsupported scoped token ${d.property}`);
  }
  return failures;
}

function checkGuestDensity(input: Rule[]) {
  const blocks = input.filter((r) => r.file === './tokens.css' && r.selector === guestSelector && !r.context);
  if (blocks.length !== 1) throw new Error('Expected one unconditional guest density block');
  const dimensions = blocks[0]!.declarations;
  if (dimensions.length !== guestDimensions.size) throw new Error('Guest dimensions must match the exact allowlist');
  for (const [name, value] of guestDimensions) {
    const actual = dimensions.filter((d) => d.property === name);
    if (actual.length !== 1 || actual[0]!.value !== value) throw new Error(`Guest dimension ${name} must be ${value}`);
  }
  for (const [name, minimum] of [['--size-target-min', 44], ['--size-target', 44], ['--text-body', 16]] as const) {
    if (fontPixels(dimensions.find((d) => d.property === name)!.value) < minimum) throw new Error(`${name} below ${minimum}px`);
  }
}

function checkGuestFocus(input: Rule[]) {
  const base = input.filter((r) => isGuestModule(r.file) && !r.context);
  const inset = base.filter((r) => declarations(r, 'outline-offset').some((d) => d.value === '-3px'));
  if (!inset.length) throw new Error('Guest scroll areas require inset focus');
  for (const rule of inset) {
    if (rule.selector !== '.dialog :is(button, a, input, select, textarea, [tabindex]):focus-visible') throw new Error('Guest inset focus selector regressed');
    if (!declarations(rule, 'outline').some((d) => d.value === '3px solid var(--color-focus)')) throw new Error('Guest inset focus requires a 3px token outline');
    for (const element of ['button', 'a', 'input', 'select', 'textarea', '[tabindex]']) {
      if (compareSpecificity(specificity(rule.selector), specificity(`${element}:focus-visible`)) <= 0) throw new Error('Guest inset focus must outrank global focus');
    }
    const white = base.find((r) => r.file === rule.file && r.selector === '.dialog input:checked:focus-visible');
    if (!white || !declarations(white, 'outline-color').some((d) => d.value === 'var(--color-on-action)')
      || compareSpecificity(specificity(white.selector), specificity(rule.selector)) <= 0) throw new Error('Guest filled input focus override regressed');
    const scroll = base.find((r) => r.file === rule.file && r.selector === '.body');
    if (!scroll || !declarations(scroll, 'overflow-y').some((d) => d.value === 'auto')) throw new Error('Guest sheet scroll area is missing');
    const padding = scroll && declarations(scroll, 'padding')[0]?.value.split(' ')[0];
    if (!padding || fontPixels(resolve(padding, [], 'guest')) < 3) throw new Error('Guest scroll padding must protect focus outlines');
  }
}

describe('DS-1 static design-token regression gate', () => {
  it('loads every expected CSS file as a non-empty raw string', () => {
    checkSources(sources, expectedFiles);
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

  it('gives inset focus greater specificity than every global focus selector and preserves white overrides', () => {
    const base = allRules.filter((r) => r.file === '../ui/styles.css' && !r.context);
    const insetRules = base.filter((r) => declarations(r, 'outline-offset').some((d) => d.value === '-3px'));
    expect(insetRules).toHaveLength(1);
    const inset = insetRules[0]!;
    expect(inset.selector).toMatch(/^:(?:is|where)\(\.cart-lines, \.quote-lines, \.confirm-dialog\) :is\(button, a, input, select, textarea, \[tabindex\]\):focus-visible$/);
    const insetWeight = specificity(inset.selector);
    for (const element of ['button', 'a', 'input', 'select', 'textarea', '[tabindex]']) {
      const selector = `${element}:focus-visible`;
      const globalRules = base.filter((r) => split(r.selector, ',').map(normal).includes(selector)
        && declarations(r, 'outline').length);
      expect(globalRules, `${selector}: global focus rule must exist`).toHaveLength(1);
      const globalWeight = specificity(selector);
      expect(compareSpecificity(insetWeight, globalWeight),
        `${inset.selector} (${insetWeight}) must outrank ${selector} (${globalWeight})`).toBeGreaterThan(0);
    }
    const whiteRules = base.filter((r) => declarations(r, 'outline-color').some((d) => d.value === 'var(--color-on-action)'));
    expect(whiteRules).toHaveLength(1);
    const whiteSelectors = split(whiteRules[0]!.selector, ',').map(normal);
    expect(whiteSelectors).toEqual([
      ':is(.cart-lines, .quote-lines, .confirm-dialog) .button.primary:focus-visible',
      ':is(.cart-lines, .quote-lines, .confirm-dialog) input:checked:focus-visible',
    ]);
    for (const selector of whiteSelectors) {
      const weight = specificity(selector);
      expect(compareSpecificity(weight, insetWeight),
        `${selector} (${weight}) must outrank inset focus (${insetWeight})`).toBeGreaterThan(0);
    }
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
        const pixels = fontPixels(resolve(d.value, [], contextFor(rule.file)));
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
    const failures = tokenFailures(allRules);
    expect(failures, failures.join('\n')).toEqual([]);
  });
});

describe('DS-2 guest token contexts and safeguards', () => {
  it('keeps the exact guest dimension block and its accessibility floors', () => {
    checkGuestDensity(allRules);
    expect(resolve('var(--size-media)', [], 'guest')).toBe('72px');
    expect(() => resolve('var(--size-media)')).toThrow('unknown var() token');
  });

  it('uses guest density tokens in the real sheet module', () => {
    const consumers = allRules.filter((r) => isGuestModule(r.file))
      .flatMap((r) => r.declarations.flatMap((d) => [...d.value.matchAll(/var\((--[\w-]+)\)/g)]
        .filter(([, name]) => guestDimensions.has(name!)).map(([, name]) => `${r.file}: ${name}`)));
    expect(consumers).toContain('../ui/guest/GuestSheet.module.css: --size-target-min');
    expect(consumers).toContain('../ui/guest/GuestSheet.module.css: --text-body');
    const guestOnly = [...guestDimensions.keys()].filter((name) => !definitions.has(name));
    expect(consumers.some((consumer) => guestOnly.some((name) => consumer.endsWith(`: ${name}`)))).toBe(true);
    expect(consumers).toContain('../ui/guest/GuestSheet.module.css: --size-cartbar');
  });

  it('keeps DEMO primitives and decorative border unchanged', () => {
    expect(resolve('var(--color-demo-ink)')).toBe('#503b14');
    expect(resolve('var(--color-demo-bg)')).toBe('#fff6df');
    // Decorative only: this border does not define an interactive control.
    expect(resolve('var(--color-demo-border)')).toBe('#c59b4a');
    expect(contrast('var(--color-demo-ink)', 'var(--color-demo-bg)')).toBeGreaterThanOrEqual(4.5);
  });

  it('protects sheet scroll-area inset focus and filled-input overrides', () => {
    checkGuestFocus(allRules);
    expect(contrast('var(--color-focus)', 'var(--color-surface)', 'guest')).toBeGreaterThanOrEqual(3);
    expect(contrast('var(--color-on-action)', 'var(--color-action)', 'guest')).toBeGreaterThanOrEqual(3);
    expect(allRules.filter((r) => isGuestModule(r.file)).every((r) => !r.selector.includes(':global('))).toBe(true);
  });

  it.each(['--color-text', '--brand-primary', '--ref-green-800', '--font-sans'])('rejects scoped guest override %s', (name) => {
    const bad = rules('./tokens.css', `[data-surface="guest"] { ${name}: var(--color-text); }`);
    expect(tokenFailures(bad)).toEqual([expect.stringContaining(`unsupported scoped token ${name}`)]);
  });

  it('rejects dimension overrides outside the tokens.css guest block', () => {
    const bad = rules('../ui/guest/Bad.module.css', '[data-surface="guest"] { --size-target: 48px; }');
    expect(tokenFailures(bad)).toEqual([expect.stringContaining('unsupported scoped token --size-target')]);
  });

  it('rejects a 40px guest target', () => {
    const css = `[data-surface="guest"] { ${[...guestDimensions].map(([name, value]) => `${name}: ${name === '--size-target' ? '40px' : value};`).join(' ')} }`;
    const bad = rules('./tokens.css', css);
    expect(() => checkGuestDensity(bad)).toThrow('Guest dimension --size-target must be 48px');
  });

  it.each(['', '   ', {}])('rejects an empty or stubbed new CSS raw import (%j)', (bad) => {
    expect(() => checkSources({ ...sources, '../ui/guest/GuestSheet.module.css': bad }, expectedFiles)).toThrow('non-empty string');
  });

  it('rejects a missing new CSS raw import', () => {
    const { '../ui/guest/GuestSheet.module.css': omitted, ...bad } = sources;
    expect(omitted).toBeDefined();
    expect(() => checkSources(bad, expectedFiles)).toThrow('missing CSS raw import');
  });

  it('rejects weaker guest inset focus specificity', () => {
    const css = sources['../ui/guest/GuestSheet.module.css']!.replace(
      '.dialog :is(button, a, input, select, textarea, [tabindex]):focus-visible',
      ':where(.dialog) :where(button, a, input, select, textarea, [tabindex]):focus-visible');
    expect(() => checkGuestFocus(rules('../ui/guest/GuestSheet.module.css', css))).toThrow('Guest inset focus selector regressed');
  });

  it('rejects a missing guest filled-input focus override', () => {
    const css = sources['../ui/guest/GuestSheet.module.css']!.replace('outline-color: var(--color-on-action);', 'outline-color: var(--color-focus);');
    expect(() => checkGuestFocus(rules('../ui/guest/GuestSheet.module.css', css))).toThrow('Guest filled input focus override regressed');
  });

  it('rejects an undefined variable even in the guest context', () => {
    const bad = rules('../ui/guest/Bad.module.css', '.bad { width: var(--size-undefined); }');
    expect(tokenFailures(bad)).toEqual([expect.stringContaining('undefined variable --size-undefined in guest')]);
  });

  it('rejects a guest-only variable in root-context CSS', () => {
    const css = '.bad { width: var(--size-media); }';
    expect(tokenFailures(rules('../ui/Root.module.css', css))).toEqual([expect.stringContaining('undefined variable --size-media in root')]);
    expect(tokenFailures(rules('../ui/guest/Media.module.css', css))).toEqual([]);
  });
});

describe('static gate grammar safeguards', () => {
  it.each([
    ['button:focus-visible', [0, 1, 1]],
    ['[tabindex]:focus-visible', [0, 2, 0]],
    [':where(.cart-lines, .quote-lines, .confirm-dialog) :focus-visible', [0, 1, 0]],
    [':is(.cart-lines, .quote-lines, .confirm-dialog) :focus-visible', [0, 2, 0]],
    [':is(.cart-lines, .quote-lines, .confirm-dialog) :is(button, a, input, select, textarea, [tabindex]):focus-visible', [0, 3, 0]],
    [':where(.cart-lines, .quote-lines, .confirm-dialog) :is(button, a, input, select, textarea, [tabindex]):focus-visible', [0, 2, 0]],
    [':where(.cart-lines) .button.primary:focus-visible', [0, 3, 0]],
    [':where(.cart-lines) input:checked:focus-visible', [0, 2, 1]],
    [':is(button, .cart-lines, #dialog) :focus-visible', [1, 1, 0]],
    [':is(:where(#ignored), .cart-lines) :focus-visible', [0, 2, 0]],
  ] as const)('computes focus-selector specificity for %s', (selector, expected) => {
    expect(specificity(selector)).toEqual(expected);
  });
  it('compares specificity lexicographically', () => {
    expect(compareSpecificity([0, 2, 0], [0, 1, 9])).toBeGreaterThan(0);
    expect(compareSpecificity([0, 2, 0], [0, 2, 0])).toBe(0);
    expect(compareSpecificity([0, 2, 0], [1, 0, 0])).toBeLessThan(0);
  });
  it.each([':has(button)', ':is(.cart-lines', 'button::before', '[data-label="a,b"]'])('rejects unsupported specificity syntax %s', (selector) => {
    expect(() => specificity(selector)).toThrow();
  });
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
