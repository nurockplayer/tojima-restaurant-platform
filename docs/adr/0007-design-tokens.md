# ADR-0007 — Design tokens / デザイントークン・設計 token

Date: 2026-10-19 JST. Status: **Technical record of Owner-approved option D,
D-DS-20261019; DS-1 implementation pending director acceptance and independent
review.** Contract: P-88 (`ds-1-plan.md`), based on `907e6ce`.

## Decision / 決定・決策

Evolve Tojima's existing green identity with plain CSS custom properties. This is
within [ADR-0006](0006-frontend-stack.md), using the React/TypeScript/Vite client
and pnpm rules of [ADR-0005](0005-typescript-guest-web.md). No dependency, UI
framework, API, database, layout or information-architecture change is needed.
Code is the source of truth; Figma is an optional future mirror.

`web/src/shared/tokens.css` loads before the existing UI styles and defines:

1. **Tier 1 — `--ref-*` primitives:** warm neutrals, the existing Tojima green
   `#174d3c`, status hues, system CJK font stacks, spacing and radii.
2. **Tier 2 — semantic roles:** light page/surface/text, muted text, action and
   selected action, decorative border, control border, focus, price and paired
   status ink/background. Default typography, dimensions and reduced-motion
   values form the foundation; they do not introduce surface density variants.
3. **Tier 3 — `--brand-*` hooks:** platform defaults only. Every pilot store uses
   the same Tojima values. There is no store input, persistence, colour derivation
   or runtime override in DS-1; opening that hook belongs to DS-4.

Status, danger, platform focus, price and DEMO/legal notices are protected roles;
store branding must not override them. The brand hook's on-fill focus value is a
separate checked pair, not permission to override `--color-focus`.

Legacy variables remain aliases for existing consumers: `--ink` → `--color-text`,
`--muted` → `--color-text-muted`, `--green` → `--color-action`, `--green-2` →
`--color-action-hover`, `--mint` → `--color-action-soft`, `--line` →
`--color-border`, `--cream` → `--ref-neutral-25`, `--orange` →
`--color-warning-ink`, and `--red` → `--color-danger-ink`. New declarations use
semantic roles. `--line` is decorative; controls use `--color-border-control`.

Readable text has a 12px declaration floor (the existing root remains 15px).
The only font exceptions are the narrow-screen badge's deliberate `0` and its
empty dot's `initial` reset, with exact source locations and reasons. Focus uses
3px `--color-focus` outlines with a 2px offset on buttons, links, inputs,
selects, textareas and `[tabindex]`; the stepper no longer clips its buttons'
outline. Existing 44px targets and the native StaffDialog are retained.

## Static regression gate / 静的回帰ゲート・靜態回歸門檻

`pnpm --dir web check` runs oxlint, TypeScript and Vitest, including
`src/shared/tokens.test.ts`. No new dependency or Node filesystem API is used.
CSS is loaded using `import.meta.glob('../**/*.css', { query: '?raw',
import: 'default', eager: true })`. Vitest 5 requires
`test.css: { include: [/[?&]raw(?:&|$)/] }` to preserve those raw imports.
The test asserts every expected CSS file exists and every matched file is a
non-empty string, so empty CSS cannot produce a vacuous pass.

The gate checks:

- Resolved semantic usage pairs: text ≥4.5:1; control boundaries and focus ≥3:1,
  including page, white and cream surfaces, selected actions, four status pairs
  and platform brand defaults. It uses WCAG sRGB relative luminance.
- Every `font-size` declaration in `src/**/*.css`: px or rem only, using the
  asserted 15px root. Anything below 12px or an unsupported expression (`em`,
  `%`, `calc`, `clamp`, token expressions, etc.) fails. Only exact, reasoned
  `0`/`initial` exceptions pass.
- Text declarations paired with an opaque hex/known token background in the
  same rule. A rule without a background is checked on **both** default page
  and white surface. All text uses the conservative 4.5:1 threshold.
- Unsupported colours (`currentColor`, unknown `var`, `color-mix`, inherited
  colours), transparent/image backgrounds, opacity and background rules with
  inherited foregrounds fail unless their exact colour/opacity signature is
  recorded with a source-review reason in `tokens.inventory.ts`. Stale entries
  and duplicates fail too. Reasons identify which cases need rendered review;
  they do not assert that those cases passed a rendered audit.
- Every literal hex occurrence outside token primitive definitions is fixed
  by file, media context, selector, property and value in the reviewed inventory.
  Additions, deletions and changed occurrence counts require inventory review.
- Undefined custom-property references, cycles and scoped colour-token overrides
  fail. The parser accepts ordinary declaration blocks and `@media`; unsupported
  at-rules, nested style rules and malformed declarations fail explicitly.

**This is a static regression gate, not a rendered WCAG AA guarantee.** It does
not calculate selector matching, specificity, DOM ancestry, browser control
appearance, stacking, clipping, dynamic states or compositing. A colour-only
rule's two default surfaces do not represent every actual ancestor. Font
shorthands, inline styles and computed/inherited font sizes are outside the
font-size declaration audit. The remaining reviewed inheritance/compositing
exceptions need Opus's computed-style audit. Native input appearance, 320px
horizontal overflow, JA/ZH wrapping, focus visibility and touch targets need the
planned browser, screenshot and keyboard acceptance. A green static check does
not satisfy these acceptance steps.

**P-84 7f (JA/ZH assistive-technology journeys) and 7g (WebKit/physical devices)
remain human gates. DS-1 does not change a GAP to PASS.** The director accepts
rendered evidence; a fresh read-only Codex reviews the committed exact SHA.

## Sequence / 順序

DS-1 provides tokens and bounded accessibility repairs. DS-2 owns guest IA,
menu-first navigation, sheets, cart bar and two-track statuses, with React Aria
introduced only at its first complex consumer. DS-3 owns Floor/KDS behaviour,
large type and dark KDS. DS-4 owns store-brand input and its validation after the
pilot, subject to Owner authorization. DS-5 adds Office density only when
MOD-02/03 requirements exist. DS-1 includes no `data-surface` overrides, dark
scheme, store-brand input, theme-color change or StaffDialog replacement.

Rollback is a revert of the DS-1 delivery; there is no API or data migration.

## 日本語要約

D-DS-20261019 の承認済み案Dを技術記録する。ADR-0006 の範囲内で、既存の緑と
レイアウトを維持し、primitive → semantic → brand hook の3層を導入する。
試験運用中は全店舗 Tojima 既定色。状態・危険・フォーカス・価格・DEMO／法定表示は
保護ロールで、店舗入力は DS-4。文字12px、文字対比4.5:1、境界とフォーカス3:1を
静的に検査する。未対応構文は理由付きの完全一致リスト以外は失敗し、空のCSSや
新しい直書きhexも失敗する。このゲートは描画状態のAA保証ではない。継承・合成・
ネイティブ部品・狭幅・キーボードは Opus の描画受入で補完する。P-84 7f／7g は
人による受入のまま、GAPをPASSにしない。DS-2（顧客IA）、DS-3（Floor/KDS）、
DS-4（店舗ブランド）、DS-5（Office）の順序を維持する。

## 繁體中文摘要

本 ADR 記錄 D-DS-20261019 已批准的方案 D。在 ADR-0006 範圍內維持既有綠色與
版面，引入 primitive → semantic → brand hook 三層。試營運所有店家維持 Tojima
預設色；狀態、危險、焦點、價格及 DEMO／法定標示為受保護角色，店家輸入屬 DS-4。
靜態檢查文字12px、文字對比4.5:1、邊界與焦點3:1。不支援語法除附理由且完全匹配的
清單外一律失敗，空CSS與新增寫死hex也會失敗。此門檻不是實際渲染AA保證；繼承、
合成、原生控制項、窄螢幕與鍵盤需由 Opus 的渲染驗收補足。P-84 7f／7g 維持人工
驗收，不把 GAP 改為 PASS。依序由 DS-2 處理顧客IA、DS-3 處理 Floor/KDS、DS-4
處理店家品牌、DS-5 處理 Office 密度。
