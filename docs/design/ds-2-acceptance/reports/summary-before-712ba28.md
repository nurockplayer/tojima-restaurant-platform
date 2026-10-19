# DS-2 rendered acceptance: before

- UI config: `before`; base `http://127.0.0.1:8096`; run 2026-10-19T18:29:32.014Z; Chrome/155.0.8059.82; node v24.15.0
- Viewports ja/zh x 375x812, 1280x900, 320x700 (DPR 2). Screenshots: `shots/`. Raw evidence: `report-before.json`

## Per-viewport key numbers

| combo | first product top | fully visible rows | median / max row | Tab to first action | AA fails | overflow | undersized targets | sticky/fixed (menu) |
|---|---|---|---|---|---|---|---|---|
| ja-375 | 809px | 0/9 | 207 / 335px | 8 | 0 | 0 | 44 | 0 |
| ja-1280 | 477px | 2/9 | 207 / 335px | 8 | 0 | 0 | 44 | aside.cart-panel sticky |
| ja-320 | 832px | 0/9 | 226 / 353px | 8 | 0 | 0 | 44 | 0 |
| zh-375 | 783px | 0/9 | 205 / 329px | 8 | 0 | 0 | 51 | 0 |
| zh-1280 | 463px | 2/9 | 205 / 329px | 8 | 0 | 0 | 51 | aside.cart-panel sticky |
| zh-320 | 804px | 0/9 | 224 / 347px | 8 | 0 | 0 | 51 | 0 |

## Sanity check against P-89 section 2 baseline (3c47f7a)

| item | P-89 section 2 | measured | |
|---|---|---|---|
| ja-375 first product top | 809 | 809 | OK |
| ja-375 fully visible rows | 0 | 0 | OK |
| ja-375 max card height | 335 | 335 | OK |
| ja-375 median card height | 207 | 207 | OK |
| ja-375 Tab to first add | 8 | 8 | OK |
| ja-375 sticky/fixed elements | 0 | 0 | OK |
| zh-375 first product top | 783 | 783 | OK |
| zh-375 fully visible rows | 0 | 0 | OK |
| zh-375 max card height | 329 | 329 | OK |
| zh-375 median card height | 205 | 205 | OK |
| zh-375 Tab to first add | 8 | 8 | OK |
| zh-375 sticky/fixed elements | 0 | 0 | OK |
| ja-320 first product top | 832 | 832 | OK |
| ja-320 fully visible rows | 0 | 0 | OK |
| ja-320 max card height | 353 | 353 | OK |
| ja-320 Tab to first add | 8 | 8 | OK |
| ja-320 sticky/fixed elements | 0 | 0 | OK |
| zh-320 first product top | 804 | 804 | OK |
| zh-320 fully visible rows | 0 | 0 | OK |
| zh-320 max card height | 347 | 347 | OK |
| zh-320 Tab to first add | 8 | 8 | OK |
| zh-320 sticky/fixed elements | 0 | 0 | OK |
| ja-1280 fully visible rows | 2 | 2 | OK |
| ja-1280 sticky elements | cart aside only | aside.cart-panel | OK |
| zh-1280 fully visible rows | 2 | 2 | OK |
| zh-1280 sticky elements | cart aside only | aside.cart-panel | OK |
| ORDER_READ button | 76x29 | 76x29 | OK |
| AA failures (guest states) | 0 | 0 | OK |
| guest JS decoded bytes | 315412 | 315412 | OK |
| guest JS gzip KB (Vite accounting) | 99.7 | 98.84 | DIFF |

## Acceptance checks

| check | result | evidence |
|---|---|---|
| Computed-style AA contrast failures = 0 (guest states) (5.4-1) | PASS | 72 states, 0 failures |
| Minimum rendered font size >= 12px (5.4-1) | PASS | min 12px |
| No horizontal overflow (also with a sheet open) (5.4-1) | PASS | 72 states incl. 0 with a sheet open |
| First viewport: >=3 fully visible rows @375, >=2 @320; median row height <= 120px (all rows) (5.4-2) | n/a | ja-375: top 809px fully 0 median 207px; ja-1280: top 477px fully 2 median 207px; ja-320: top 832px fully 0 median 226px; zh-375: top 783px fully 0 median 205px; zh-1280: top 463px fully 2 median 205px; zh-320: top 804px fully 0 median 224px |
| No visible text is cut off by ellipsis / clipping (cart-bar sync text must show the HH:mm) (5.4-7) | n/a | 72 states, none |
| Every operable element >= 44x44px (label-wrapped radio/checkbox: label rect; sr-only, disabled and inert background listed, not counted) (5.4-4) | n/a | distinct failing elements: button[共有カート0] 95.5x33 in menu-first-viewport/order-just-confirmed/order-accepted; button[注文履歴0] 82.5x33 in menu-first-viewport/option-sheet/required-error/menu-with-cart/scrolled-bottom/cart-sheet/confirm-preview/degraded-closed; input[普通¥20] 305x18 in menu-first-viewport/option-sheet/required-error/menu-with-cart/scrolled-bottom/cart-sheet; input[小（100g）¥0] 305x18 in menu-first-viewport/option-sheet/required-error/menu-with-cart/scrolled-bottom/cart-sheet; select[シロナガスクジラ定食 数量] 50x26 in menu-first-viewport/option-sheet/required-error/menu-with-cart/scrolled-bottom/cart-sheet; select[塩唐揚げ定食 数量] 50x26 in menu-first-viewport/option-sheet/required-error/menu-with-car |
| DEMO: 1 page banner, 1 DemoNotice in every open ordering sheet, 1 on ORDER_READ (5.4-5) | n/a | page banner !=1 in: none; sheets: 0 checked, bad: none; order-read bad: none |
| Topbar contains 0 platform names (東島 / TABLE ORDER / TOJIMA KITCHEN / OPEN TABLE) (5.4-5) | n/a | topbar counts: ja-375=2, ja-1280=2, ja-320=2, zh-375=2, zh-1280=2, zh-320=2; whole page: ja-375=5[東島/TABLE ORDER/TOJIMA KITCHEN/OPEN TABLE], ja-1280=5[東島/TABLE ORDER/TOJIMA KITCHEN/OPEN TABLE], ja-320=5[東島/TABLE ORDER/TOJIMA KITCHEN/OPEN TABLE], zh-375=5[東島/桌邊點餐/TOJIMA KITCHEN/OPEN TABLE], zh-1280=5[東島/桌邊點餐/TOJIMA KITCHEN/OPEN TABLE], zh-320=5[東島/桌邊點餐/TOJIMA KITCHEN/OPEN TABLE] |
| Footer shows "Powered by Tojima" exactly once (5.4-5) | n/a | ja-375=0, ja-1280=0, ja-320=0, zh-375=0, zh-1280=0, zh-320=0 |
| Store name is the first topbar text (5.4-5) | n/a | ja-375:"東", ja-1280:"東", ja-320:"東", zh-375:"東", zh-1280:"東", zh-320:"東" |
| Guest root and sheet font-family starts with system-ui; zh contains PingFang TC (5.4-5) | n/a | ja-375 root: "DM Sans", "Noto Sans JP", "Noto Sans TC; ja-1280 root: "DM Sans", "Noto Sans JP", "Noto Sans TC; ja-320 root: "DM Sans", "Noto Sans JP", "Noto Sans TC; zh-375 root: "DM Sans", "Noto Sans JP", "Noto Sans TC; zh-375 root lacks PingFang TC; zh-1280 root: "DM Sans", "Noto Sans JP", "Noto Sans TC; ... (+3) |
| html carries data-surface="guest" while the guest app is mounted (3.11) | n/a | ja-375=null, ja-1280=null, ja-320=null, zh-375=null, zh-1280=null, zh-320=null |
| Focus ring visible and not clipped (clippedBy empty): category item, product action, sheet close, radio, quantity, add, cart bar, stepper +/-, confirm, ORDER_READ language button (5.4-7) | n/a | clipped/no ring: none; unverified: none |
| ORDER_READ language button >= 44x44 (baseline 76x29) (5.4-4) | n/a | ja-375: 76x29 lang=null, ja-1280: 76x29 lang=null, ja-320: 76x29 lang=null, zh-375: 61x27 lang=null, zh-1280: 61x27 lang=null, zh-320: 61x27 lang=null |
| Guest JS gzip total <= 125 KB (baseline 99.7 KB, Vite accounting) (5.3) | n/a | 98.84 KB (level-6, KB=1000; harness level-9 estimate 98.63 KB), decoded 315412 B in 5 chunks |
| DS-1 regression: staff / KDS / catalog = 20 states, AA failures 0, horizontal overflow 0 (5.4-8) | PASS | 20/20 states, AA failures 0, overflow: none, min font 12px |
| Harness steps that threw (a failed step means states after it may be missing) (info) | PASS | none |

## States that do not exist in this UI (recorded as notApplicable, with the closest old equivalent measured)

- `option-sheet`: old UI has no option sheet; options are inline in the product card (closest old equivalent: inline fieldset in .product-card (scrolled to the option product))
- `required-error`: old UI disables "add" until the required group is chosen (no error, no focus move) (closest old equivalent: disabled add button + .field-hint text)
- `cart-sheet`: old UI has no cart sheet (closest old equivalent: in-flow .cart-panel with 2 lines)
- `confirm-preview`: old UI has no preview sheet (closest old equivalent: old preview mode of .cart-panel)
- `degraded-closed`: old UI has no sync-degraded state: it keeps showing "latest / shared with the table" while offline (closest old equivalent: same menu page while offline (oldLatest=true is the defect))
- `order-just-confirmed`: old UI has one joined status line, no two tracks (closest old equivalent: .guest-order-status single line)
- `order-accepted`: old UI has one joined status line, no two tracks (closest old equivalent: .guest-order-status single line after staff accepted)

## DS-1 regression (staff / KDS / catalog)

20/20 states, AA failures 0, horizontal overflow states: none, min font 12px. Compare screenshots with `node compare.mjs <before-dir> <after-dir>`.

## Undersized targets (first viewport state, ja-375)

13/24 counted targets below 44x44. Choice controls: radio input 13x13 / effective(label) 305x18; radio input 13x13 / effective(label) 305x18
