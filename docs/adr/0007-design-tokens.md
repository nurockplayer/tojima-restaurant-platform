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
selects, textareas and `[tabindex]`. Inside `.cart-lines`, `.quote-lines` and
`.confirm-dialog` (including native dialogs), a -3px offset keeps the ring inside
the control so scroll edges cannot clip it. Green primary buttons and checked
inputs use `--color-on-action` for inset focus. Blue on the stepper's `#f5f7f3`
fill is 5.53:1; white on action/hover green is 9.71:1 / 7.24:1. Existing 44px
targets, scrolling geometry and the native StaffDialog are retained. Catalog
and KDS currently have no additional CSS scrolling regions.

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
- Every `font-size` declaration in `src/**/*.css`: px or rem, including known
  tokens resolving to those units, using the asserted 15px root. Anything below
  12px or an unsupported expression (`em`, `%`, `calc`, `clamp`, etc.) fails.
  Only exact, reasoned `0`/`initial` exceptions pass. An unconditional shared
  `small { font-size: var(--text-meta); }` rule must exist and resolve to ≥12px.
- Focus selectors: the inset rule must have greater specificity than each of
  the six global focus selectors; white-on-green overrides must outrank it.
  A dependency-free calculator handles the bounded selector grammar, including
  the maximum argument specificity of `:is()` and the zero weight of `:where()`.
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
not calculate selector matching, general cascade precedence, DOM ancestry,
browser control appearance, stacking, clipping, dynamic states or compositing. A colour-only
rule's two default surfaces do not represent every actual ancestor. Font
shorthands, inline styles and computed/inherited font sizes are outside the
font-size declaration audit. UA-relative defaults such as `small`'s `smaller`
are invisible when no author declaration exists: a 12px legend can produce a
10px child. The shared `small` base rule overrides that default, and the gate
requires it and checks its resolved size. Existing specific `small` font rules
are explicit 12px; none depends on UA shrinking. This mitigation does not turn
the audit into a computed-style check. The remaining reviewed inheritance/compositing
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

**制限と対策**：作者側の宣言がない UA 相対サイズ（`small` の `smaller`）は静的検査に
現れず、12px の legend 内で10pxになり得る。共有の `small { font-size: var(--text-meta); }`
で上書きし、ゲートで無条件の基底ルールの存在と解決後12px以上を確認する。既存の
個別指定は明示的な12pxで、UA縮小への依存はない。スクロール領域内のフォーカスは
3px・offset -3pxで内側に描き、濃緑の塗りには白を使用する。実描画・クリッピングの
受入は引き続き必要で、レイアウトとスクロールの挙動は変更しない。

## 繁體中文摘要

本 ADR 記錄 D-DS-20261019 已批准的方案 D。在 ADR-0006 範圍內維持既有綠色與
版面，引入 primitive → semantic → brand hook 三層。試營運所有店家維持 Tojima
預設色；狀態、危險、焦點、價格及 DEMO／法定標示為受保護角色，店家輸入屬 DS-4。
靜態檢查文字12px、文字對比4.5:1、邊界與焦點3:1。不支援語法除附理由且完全匹配的
清單外一律失敗，空CSS與新增寫死hex也會失敗。此門檻不是實際渲染AA保證；繼承、
合成、原生控制項、窄螢幕與鍵盤需由 Opus 的渲染驗收補足。P-84 7f／7g 維持人工
驗收，不把 GAP 改為 PASS。依序由 DS-2 處理顧客IA、DS-3 處理 Floor/KDS、DS-4
處理店家品牌、DS-5 處理 Office 密度。

**限制與對策**：沒有作者宣告的 UA 相對字級（`small` 的 `smaller`）不會出現在靜態檢查中，
12px 的 legend 內可能產生10px文字。以共用 `small { font-size: var(--text-meta); }` 覆寫，
門檻要求無條件的基底規則存在且解析後至少12px。既有個別字級皆明訂12px，不依賴 UA
縮小。捲動區域內的焦點使用3px、offset -3px向內繪製，深綠底改用白色；仍需驗收實際
渲染與裁切，版面及捲動行為維持不變。


## DS-2 addendum / DS-2 追記 / DS-2 補充

Date: 2026-10-20 JST. Contract: [P-89](https://linear.app/higashijima-manage-system/issue/P-89) §3.11 and §5.1–5.3. This addendum records the guest density consumer and the first complex guest modal adopted under [ADR-0006](0006-frontend-stack.md). It does not change staff, KDS or Catalog styling.

### Guest density and protected DEMO roles

The `tokens.css` `[data-surface="guest"]` block contains dimensions only:

| Token | Value |
|---|---:|
| `--text-body` | `16px` |
| `--size-target` | `48px` |
| `--size-target-min` | `44px` |
| `--size-row` | `56px` |
| `--size-media` | `72px` |
| `--size-catnav` | `48px` |
| `--size-cartbar` | `64px` |

The guest block does not redefine colors, `--brand-*` or `--ref-*`. The guest root and sheets use `font-family: var(--font-sans)`; the document language lets Traditional Chinese resolve through the existing `:lang(zh-Hant)` stack. DEMO notice color is a protected semantic role: `--color-demo-ink`, `--color-demo-bg` and `--color-demo-border` resolve to Tier-1 primitives retaining the prior values `#503b14`, `#fff6df` and `#c59b4a`. Store branding cannot override these roles.

### Modal choice

ADR-0006 adopts React Aria Components for a real complex control when it first has a consumer. DS-2 uses exact `react-aria-components@1.21.1` for the guest sheet. The DS-1 native `<dialog>` comparison added no bundle bytes (`+0 KB`), but its recorded keyboard behavior lets Tab leave the document after the last dialog control. React Aria provides focus containment, makes the background inert while open and restores focus after close. P-89 §2 recorded an initial App estimate of `11.59 → 29.17 KB gzip` (`+17.6 KB`); this trial estimate is not an attribution of the full final App delta to React Aria. The current build measures emitted chunks below.

### Bundle measurements

Measured with `pnpm --dir web build` on the final DS-2 tree (after the bot P1 sync fix, with `@react-aria/optimize-locales-plugin`). Raw and gzip figures below are Vite-reported per emitted JavaScript chunk; guest totals and raw size comparisons use those reported two-decimal values. Baseline figures are from P-89 §2. The guest total is `124.46 KB gzip`, `24.76 KB` above its `99.7 KB` baseline; App is `36.11 KB gzip`, `24.52 KB` above `11.59 KB`, and the full App delta is not attributed solely to React Aria. StaffApp is unchanged in the reported raw size (`0.00 KB`, `0.000%`); CatalogApp is up `0.01 KB` (`0.012%`), within the ±1% baseline.

| Chunk / measure | Baseline (gzip unless marked raw) | DS-2 measured raw / gzip |
|---|---:|---:|
| Guest entry / `index` chunk | included in guest total | 223.77 KB / 70.52 KB |
| Guest initial `App` | `11.59 KB` gzip | 114.78 KB / 36.11 KB |
| Guest `OrderRead` | included in guest total | 43.94 KB / 14.27 KB |
| Guest `cart-state` | included in guest total | 5.41 KB / 1.91 KB |
| Guest `DemoBanner` | included in guest total | 4.03 KB / 1.65 KB |
| Other guest chunks / guest total | `99.7 KB` gzip total | 391.93 KB / 124.46 KB |
| `StaffApp` | `64.14 KB` raw | 64.14 KB / 17.42 KB |
| `CatalogApp` | `84.64 KB` raw | 84.65 KB / 26.32 KB |

The production build module inspection found 98 rendered React Aria/Stately modules in App and none in index, OrderRead, cart-state, DemoBanner, StaffApp or CatalogApp. The remote-demo build and `node scripts/check-remote-demo-bundle.mjs web/dist` also pass; its guest JavaScript total is `124.46 KB gzip` (cart-state `1.90 KB`, DemoBanner `1.66 KB`). `@react-aria/optimize-locales-plugin` `2.0.2` (exact, dev-only) limits React Aria's built-in strings to `ja-JP`, `zh-TW` and the `en-US` fallback, saving about `0.58 KB gzip`; the remaining headroom is small, so further guest growth needs a size reduction first.

### Store-name source

The guest API does not yet supply a store display name. DS-2 therefore uses the shared DEMO-only constant for demo presentation and omits the store-name slot outside demo mode. Adding a store display name to the customer API is a separate follow-up issue (P-89 default D1); this ADR does not invent an API source or persist a local name.

## 日本語（DS-2 追記）

2026-10-20 JST。P-89 §3.11 および §5.1〜5.3 に基づき、guest 密度ブロックと、最初の複雑な guest モーダルであるシートを記録する。スタッフ・KDS・Catalog の見た目は変更しない。

`[data-surface="guest"]` は寸法だけを定義する：`--text-body: 16px`、`--size-target: 48px`、`--size-target-min: 44px`、`--size-row: 56px`、`--size-media: 72px`、`--size-catnav: 48px`、`--size-cartbar: 64px`。色、`--brand-*`、`--ref-*` は再定義しない。DEMO の保護ロール `--color-demo-ink`、`--color-demo-bg`、`--color-demo-border` は Tier-1 primitive を参照し、従来の `#503b14`、`#fff6df`、`#c59b4a` を維持する。店舗ブランドから上書きできない。

ADR-0006 の方針に従い、React Aria Components `1.21.1` を初めて実需要のある複雑な制御として guest sheet に採用した。DS-1 の native `<dialog>` は gzip `+0 KB` だが、記録済みの動作では最後の項目から Tab すると document 全体の外へ移動する。React Aria はフォーカスをシート内に保ち、表示中の背景を inert にし、閉じた後にフォーカスを復帰する。P-89 §2 の初期 App 試算 `11.59 → 29.17 KB gzip`（`+17.6 KB`）は試算であり、現在の App サイズ増分すべてを React Aria に帰属させるものではない。Vite production build の programmatic module inspection では react-aria／react-stately の rendered module が App chunk に98件あり、index、OrderRead、cart-state、DemoBanner、StaffApp、CatalogApp には0件だった。guest 合計は gzip `124.46 KB` で基準 `99.7 KB` より `24.76 KB` 増、App は gzip `36.11 KB` で基準 `11.59 KB` より `24.52 KB` 増だが、App の全増分を React Aria だけに帰属させない。StaffApp の報告 raw サイズは不変（`0.00 KB`、`0.000%`）、CatalogApp は `0.01 KB`（`0.012%`）増で ±1% 以内。remote-demo build と `node scripts/check-remote-demo-bundle.mjs web/dist` も成功し、その guest JS 合計は `124.46 KB gzip`（cart-state `1.90 KB`、DemoBanner `1.66 KB`）。`@react-aria/optimize-locales-plugin` `2.0.2`（固定版、開発時のみ）で React Aria 内蔵文字列を `ja-JP`、`zh-TW` と代替の `en-US` に限定し、約 `0.58 KB gzip` 削減した。余裕は小さいため、今後 guest を増やす場合は先に削減が必要。現行 build の各 chunk は下表のとおり。顧客 root と sheet は `font-family: var(--font-sans)` を使い、html の `lang` により繁體中文は既存の `:lang(zh-Hant)` font stack を使う。

下表は DS-2 最終ツリー（bot P1 同期修正と `@react-aria/optimize-locales-plugin` 適用後）の `pnpm --dir web build` で得た各チャンクの Vite 報告 raw／gzip 計測値。guest 合計と raw サイズ比較は小数2桁の報告値を使用する。P-89 §2 の StaffApp `64.14 KB`、CatalogApp `84.64 KB` は raw サイズ。

| チャンク／計測 | 基準（明記がなければ gzip） | DS-2 実測 raw／gzip |
|---|---:|---:|
| Guest entry／`index` chunk | guest 合計に含む | 223.77 KB / 70.52 KB |
| Guest 初期 `App` | `11.59 KB` gzip | 114.78 KB / 36.11 KB |
| Guest `OrderRead` | guest 合計に含む | 43.94 KB / 14.27 KB |
| Guest `cart-state` | guest 合計に含む | 5.41 KB / 1.91 KB |
| Guest `DemoBanner` | guest 合計に含む | 4.03 KB / 1.65 KB |
| その他 guest chunks／guest 合計 | 合計 `99.7 KB` gzip | 391.93 KB / 124.46 KB |
| `StaffApp` | `64.14 KB` raw | 64.14 KB / 17.42 KB |
| `CatalogApp` | `84.64 KB` raw | 84.65 KB / 26.32 KB |

顧客 API に店舗表示名はまだ無い。DS-2 は demo 時だけ共有 DEMO 定数を表示し、非 demo では店舗名枠を表示しない。店舗名の API 追加は後続 issue とする（P-89 既定 D1）。API にない値を推測したりローカル保存したりしない。

## 繁體中文（DS-2 補充）

日期：2026-10-20 JST。依據 P-89 §3.11 與 §5.1–5.3，記錄 guest 密度區塊與第一個有實際需求的複雜 guest modal。店員、KDS 與 Catalog 外觀不變。

`[data-surface="guest"]` 僅定義尺寸：`--text-body: 16px`、`--size-target: 48px`、`--size-target-min: 44px`、`--size-row: 56px`、`--size-media: 72px`、`--size-catnav: 48px`、`--size-cartbar: 64px`。不重新定義顏色、`--brand-*` 或 `--ref-*`。受保護 DEMO 角色 `--color-demo-ink`、`--color-demo-bg`、`--color-demo-border` 連結 Tier-1 primitive，保留既有 `#503b14`、`#fff6df`、`#c59b4a`；店家品牌不得覆寫。

依 ADR-0006 的採用時機，guest sheet 首次使用 React Aria Components `1.21.1` 作為複雜控制項。DS-1 的原生 `<dialog>` 增加 gzip `+0 KB`，但紀錄顯示按 Tab 會在最後一個 dialog 控制項後離開整份文件（document）。React Aria 會將焦點限制在 sheet 內、開啟時使背景 inert，並於關閉後還原焦點。套件只進入 guest 分塊；下表量測 StaffApp 與 CatalogApp。guest 根節點與 sheet 使用 `font-family: var(--font-sans)`；文件的 `lang` 讓繁體中文解析至既有的 `:lang(zh-Hant)` 字型堆疊。

下表列出 DS-2 最終程式碼（bot P1 同步修正與 `@react-aria/optimize-locales-plugin` 套用後）的 `pnpm --dir web build` 各 chunk Vite 回報 raw／gzip 數據。guest 合計與 raw 尺寸比較使用回報至小數兩位的數值。StaffApp 與 CatalogApp 的 P-89 §2 基準是 raw 尺寸。Vite production build 的程式化 module inspection 顯示 App chunk 含98個 rendered react-aria／react-stately modules；index、OrderRead、cart-state、DemoBanner、StaffApp 與 CatalogApp 均為0個。guest 合計 gzip `124.46 KB`，比 `99.7 KB` 基準增加 `24.76 KB`；App gzip `36.11 KB`，比 `11.59 KB` 基準增加 `24.52 KB`，不把 App 全部增幅歸因於 React Aria。StaffApp 回報的 raw 尺寸不變（`0.00 KB`、`0.000%`）；CatalogApp 增加 `0.01 KB`（`0.012%`），仍在 ±1% 內。remote-demo build 與 `node scripts/check-remote-demo-bundle.mjs web/dist` 也通過，該模式 guest JS 合計 `124.46 KB gzip`（cart-state `1.90 KB`、DemoBanner `1.66 KB`）。以 `@react-aria/optimize-locales-plugin` `2.0.2`（固定版本，僅開發依賴）將 React Aria 內建字串限縮為 `ja-JP`、`zh-TW` 與備援 `en-US`，約減少 `0.58 KB gzip`。剩餘空間很小，之後若再增加 guest 程式碼，須先縮減容量。

| Chunk／量測 | 基準（未特別標示時為 gzip） | DS-2 實測 raw／gzip |
|---|---:|---:|
| Guest entry／`index` chunk | 已包含於 guest 合計 | 223.77 KB / 70.52 KB |
| Guest 初始 `App` | `11.59 KB` gzip | 114.78 KB / 36.11 KB |
| Guest `OrderRead` | 已包含於 guest 合計 | 43.94 KB / 14.27 KB |
| Guest `cart-state` | 已包含於 guest 合計 | 5.41 KB / 1.91 KB |
| Guest `DemoBanner` | 已包含於 guest 合計 | 4.03 KB / 1.65 KB |
| 其他 guest chunks／guest 合計 | 合計 `99.7 KB` gzip | 391.93 KB / 124.46 KB |
| `StaffApp` | `64.14 KB` raw | 64.14 KB / 17.42 KB |
| `CatalogApp` | `84.64 KB` raw | 84.65 KB / 26.32 KB |

顧客 API 尚未提供店家顯示名稱。DS-2 僅在 demo 模式顯示共用的 DEMO 常數，非 demo 不顯示店名欄位。新增顧客 API 店名屬後續 issue（P-89 預設 D1）；本 ADR 不虛構 API 值或另行儲存店名。
