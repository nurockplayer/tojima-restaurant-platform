# DS-2 acceptance summary / DS-2 受入サマリー / DS-2 驗收摘要

Scope: Linear P-89 (DS-2 guest Mobile Order redesign), PR #43, branch `codex/ds-2-guest-ia`, base `712ba28`.
Final code tree: `4c7ac5c` + the bot P1 sync fix and React Aria locale trimming below; rendered evidence was captured on `668bd9e`, `4c7ac5c` and `d9b5caf` (`reports/summary-after-final.md`, 28/28 PASS); the last code commit `e62cbba` only tightens one sync comparison and is covered by unit tests.

## How the evidence was produced

- `capture.mjs` drives real Chrome over CDP against an isolated `cmd/operationstest -demo-shironagasukujira -catalog-fixture` demo with a disposable PostgreSQL 17 container. It walks ja/zh × 375×812, 1280×900, 320×700 (DPR 2) through 30+ guest states (menu, option sheet, cart sheet, preview, confirm, staff accept, degraded, session expiry, ORDER_READ) and measures computed styles, geometry, focus and network side effects. `lib/page-probes.js` holds the in-page probes; `lib/evaluate.mjs` turns raw data into the P-89 §5.4/§5.5 checks; `compare.mjs` builds the before/after table; `reevaluate.mjs` re-scores a saved JSON report.
- `webkit-check.mjs` repeats the sheet focus/scroll checks in Playwright WebKit (iPhone-sized 390×844).
- Credentials for the demo are written to a temp file that the demo deletes on SIGINT; no credentials appear in any report.
- Screenshots in `shots/` are downscaled PNGs of the captured states.
- Running: `node capture.mjs <before|after> <baseUrl> <credsFile> <outDir> [--combos=ja:375,zh:320] [--skip-ds1]` (Node 24, local Google Chrome by default or any Chromium via `CHROME_PATH`, e.g. Brave; no npm dependencies) and `node webkit-check.mjs <credsFile> <outDir>` (needs `playwright-core` with its WebKit build installed outside the repo). Neither is wired into CI.

## Results

| run | tree | scope | result |
|---|---|---|---|
| before | `712ba28` | ja/zh × 375/1280/320 | baseline, see `reports/summary-before-712ba28.md` |
| after (full) | `668bd9e` | ja/zh × 375/1280/320, DS-1 regression 20 states | every §5.4/§5.5 check PASS, see `reports/summary-after-668bd9e.md` |
| after (targeted) | `4c7ac5c` | ja-375, zh-320 (review r6 change only touches the option sheet) | 28 PASS, DS-1 skipped (no staff/catalog change), see `reports/summary-after-4c7ac5c.md` |
| WebKit | `bf1659b` | ja/zh option sheet, cart sheet, preview | focus stays in sheet for 20×Tab / 20×Shift+Tab, focus restored, no leftover inert, nav sticky, bar fixed, no overflow, see `reports/webkit-report-bf1659b.json` |

### Before / after (375×812 ja unless noted)

| metric | before `712ba28` | after |
|---|---|---|
| first product top | 809 px | 367 px |
| fully visible menu rows in first viewport | 0 (375), 0 (320) | 3 (375), 2 (320) |
| median / max menu row height | 207 / 335 px | 118 / 118 px |
| Tab presses to first product action | 8 | 6 |
| undersized (< 44×44) targets, counted states | 44 (zh 51) | 0 |
| AA contrast failures, all states | 0 | 0 |
| horizontal overflow states | 0 | 0 |
| topbar platform names / "Powered by Tojima" | 2 / 0 | 0 / 1 |
| ORDER_READ language button | 76×29 | 86×44 |
| sticky category nav / fixed cart bar | none | yes / yes |
| guest JS gzip (Vite) | 99.7 KB | 124.46 KB (budget ≤ 125) |

Full per-combo table: `reports/compare-before-vs-after-668bd9e.md`.

### Behavioural checks (after)

- UX-3: Add with a required group unselected → 0 cart writes, error shown, `aria-invalid`, focus on the first option of that group, sheet stays open. Over-limit selections after a menu refresh are surfaced with 「選び直す」 (review r6).
- UX-4: right after confirm, cooking = waiting for staff and payment = unpaid; staff accept advances only the cooking track; one live region.
- UX-5: offline → degraded notice with HH:mm within 7.5 s on page, sheet and cart bar; preview/confirm disabled with `aria-describedby` reason; cart lines unchanged; cleared within 5 s after an accepted read. Watchdog flips to degraded for hung requests (≈5.7 s) without an offline event.
- Keyboard: sheets trap focus, Esc returns focus to the opener; when the opener disappears focus goes to the menu heading (empty cart), orders heading (confirm) or entry-card heading (session expiry); no `aria-hidden`/`inert` left behind.
- Branding: store name is the first topbar text, DEMO banner exactly once on the page and once per ordering sheet, footer "Powered by Tojima" once, `data-surface="guest"` on `<html>`.

### Automated tests

`pnpm --dir web test`: 487 tests in 28 files pass (baseline 297). `lint`, `typecheck`, `build`, remote-demo build + `scripts/check-remote-demo-bundle.mjs` pass. The 10 P-84 test names are unchanged; no tests were deleted.

## Review history

Fresh read-only Codex (gpt-6.1-sol) reviews: r1–r6 found issues that were fixed in `380a8ef`, `bacaaa2`, `37a0a87`, `bf1659b`, `5fba1fc`, `4c7ac5c`; r7 on `4c7ac5c`: **PASS, no findings**. GitHub Codex bot P2 (cart bar accessible name) fixed in `9532a47`; bot P1 on `0b6f0dc` (overlapping 2.5 s polls slower than 2.5 s never counted as synced, so the watchdog blocked preview) fixed in `e95e4a0`: a superseded 2xx read whose generation/version equals the displayed cart now records sync success without adopting it (adoption rule unchanged); review r9 tightened it to an identical whole-cart snapshot in `e62cbba`, because availability can change without a version bump. Real-Chrome-only defects (focus while background still inert, cart-bar sync ellipsis, 320 px language switch wrap; jsdom has no `inert`) fixed in `668bd9e`.

## Known limitations / remaining items

- Guest bundle headroom is small: 124.46 KB (normal build) and 124.46 KB (remote-demo build) after `@react-aria/optimize-locales-plugin` 2.0.2 trimmed React Aria strings to ja-JP / zh-TW / en-US (the P1 fix alone would have reached 125.02 KB). Any further guest growth needs a reduction first.
- Harness gzip (level 6, KB = 1000) reads the baseline as 98.84 KB versus 99.7 KB in P-89 §2; the decoded bytes are identical, so this is a method difference.
- WebKit: `window.scrollBy` from script moves the page behind an open sheet at scrollY 0; user-gesture scroll lock on iOS is left to the P-84 7g device checklist (`device-checklist.md`).
- DS-1 regression (staff/KDS/catalog) was measured on `668bd9e` only; later commits do not touch those surfaces.
- D1: [MOD-05 P-90](https://linear.app/higashijima-manage-system/issue/P-90) implements an optional public store-name API (NULL means no slot, no internal-name fallback); DEMO retains its constant. The focused `topbar` harness covers pre-join and menu views. On `4643610` (real Chrome, non-demo fixture) it passed 8/8 states with a 40-character name and 8/8 with no name; a DEMO regression run with an API name set showed only the DEMO constant (0 FAIL). Evidence: `reports/d1-topbar-named-4643610.*`, `reports/d1-topbar-none-4643610.*`, `reports/d1-demo-regression-4643610.md`, `shots/d1-*.png`. See the [controlled-SQL and rollback runbook](../../ops/store-guest-display-name.md).
- P-84 7f / 7g human and device gates remain open; DS-2 does not mark them PASS.

## 日本語

P-89（DS-2 顧客 Mobile Order 改版）の受入証跡。実 Chrome（CDP）で ja/zh × 375／1280／320 の30以上の状態を計測し、§5.4／§5.5 の全項目が PASS（`668bd9e` 全量、`4c7ac5c` と最終ツリーは変更箇所の再確認で PASS）。WebKit でもシート内フォーカス保持・復帰を確認。375 px で最初の画面に見える商品行は 0 → 3、商品操作まで Tab 8 → 6、44 px 未満の操作対象 44 → 0、AA 不合格 0。顧客 JS は gzip 124.46 KB で予算 125 KB 以内（remote-demo 124.46 KB）。独立レビュー PASS。D1（[P-90](https://linear.app/higashijima-manage-system/issue/P-90)）の描画受入は 40文字名・未設定とも全状態 PASS。残課題：P-84 7f／7g の人手・実機確認。

## 繁體中文

P-89（DS-2 顧客 Mobile Order 改版）的驗收證據。以實際 Chrome（CDP）量測 ja/zh × 375／1280／320 共 30 多個狀態，§5.4／§5.5 全數 PASS（`668bd9e` 全量；`4c7ac5c` 與最終程式碼針對變更處重測 PASS）。WebKit 也確認 sheet 內焦點維持與還原。375 px 首屏可見商品列 0 → 3，到第一個商品操作的 Tab 次數 8 → 6，小於 44 px 的操作目標 44 → 0，AA 不合格 0。顧客端 JS gzip 124.46 KB，在 125 KB 預算內（remote-demo 為 124.46 KB）。獨立審查 PASS。D1（[P-90](https://linear.app/higashijima-manage-system/issue/P-90)）畫面驗收在 40 字店名與未設定兩種情況全部 PASS。待辦：P-84 7f／7g 人工與實機驗收。
