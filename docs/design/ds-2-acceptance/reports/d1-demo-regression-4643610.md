# DS-2 rendered acceptance: after

- UI config: `after`; base `http://127.0.0.1:8093`; run 2026-10-20T17:20:14.613Z; Chrome/155.0.8059.85; node v26.10.0
- Viewports ja/zh x 375x812, 1280x900, 320x700 (DPR 2). Screenshots: `shots/`. Raw evidence: `report-after.json`

## Per-viewport key numbers

| combo | first product top | fully visible rows | median / max row | Tab to first action | AA fails | overflow | undersized targets | sticky/fixed (menu) |
|---|---|---|---|---|---|---|---|---|
| ja-375 | 367px | 3/9 | 118 / 118px | 6 | 0 | 0 | 0 | nav._nav_yuvqj_1 sticky |
| zh-320 | 395px | 2/9 | 117 / 117px | 6 | 0 | 0 | 0 | nav._nav_yuvqj_1 sticky |

## Acceptance checks

| check | result | evidence |
|---|---|---|
| Computed-style AA contrast failures = 0 (guest states) (5.4-1) | PASS | 30 states, 0 failures |
| Minimum rendered font size >= 12px (5.4-1) | PASS | min 12px |
| No horizontal overflow (also with a sheet open) (5.4-1) | PASS | 30 states incl. 12 with a sheet open |
| First viewport: >=3 fully visible rows @375, >=2 @320; median row height <= 120px (all rows) (5.4-2) | PASS | ja-375: top 367px fully 3 median 118px; zh-320: top 395px fully 2 median 117px |
| Scrolled to the bottom: category nav and cart bar stay inside the viewport (5.4-3) | PASS | 2 combos ok |
| Tab traversal: focus stops fully hidden behind the category nav / cart bar = 0 (5.4-3) | PASS | nav/bar: 0; any fixed/sticky overlay: 0; 8 page traversals (75 stops) |
| Tab presses from page start to first product action <= 8 (baseline 8) (5.4-3) | PASS | ja-375/menu-first-viewport:6; ja-375/menu-with-cart:6; zh-320/menu-first-viewport:6; zh-320/menu-with-cart:6 |
| No visible text is cut off by ellipsis / clipping (cart-bar sync text must show the HH:mm) (5.4-7) | PASS | 30 states, none |
| Every operable element >= 44x44px (label-wrapped radio/checkbox: label rect; sr-only, disabled and inert background listed, not counted) (5.4-4) | PASS | 30 states, 0 undersized |
| DEMO: 1 page banner, 1 DemoNotice in every open ordering sheet, 1 on ORDER_READ (5.4-5) | PASS | page banner !=1 in: none; sheets: 10 checked, bad: none; order-read bad: none |
| Topbar contains 0 platform names (東島 / TABLE ORDER / TOJIMA KITCHEN / OPEN TABLE) (5.4-5) | PASS | topbar counts: ja-375=0, zh-320=0; whole page: ja-375=0[], zh-320=0[] |
| Footer shows "Powered by Tojima" exactly once (5.4-5) | PASS | ja-375=1, zh-320=1 |
| Store name is the first topbar text (5.4-5) | PASS | ja-375:"博多餃子酒場 シロナガスクジラ", zh-320:"博多餃子酒場 シロナガスクジラ" |
| Guest root and sheet font-family starts with system-ui; zh contains PingFang TC (5.4-5) | PASS | ja-375: system-ui, -apple-system, "Hiragino  / zh-320: system-ui, -apple-system, "PingFang  |
| html carries data-surface="guest" while the guest app is mounted (3.11) | PASS | ja-375=guest, zh-320=guest |
| UX-3: add with required group unselected => 0 cart POST/PUT/DELETE, error shown, aria-invalid, focus on the first option of that group, sheet stays open (5.4-6) | PASS | ja-375: posts=0 err=true invalid=1 focusFirst=true describedby=true / zh-320: posts=0 err=true invalid=1 focusFirst=true describedby=true |
| UX-5: offline => degraded notice with HH:mm within 7.5 s (page, sheet, cart bar), preview disabled, cart lines unchanged, cleared <= 5 s after online (5.4-6) | PASS | ja-375: 4ms hhmm=02:20 bar="カートを開く 2 点 税込・目安 ¥1,940 同期待ち・0" sheetNotice=true previewDisabled=true (reason: 通信が不安定です表示は 02:20 時点の内容で) lines 2->2 recovered 2151ms / zh-320: 8ms hhmm=02:22 bar="開啟購物車 2 件 含稅・預估 ¥1,940 等待同步・02" sheetNotice=true previewDisabled=true (reason: 網路連線不穩目前顯示 02:22 的內容。恢復連) lines 2->2 recovered 2130ms |
| UX-5: "confirm order" is disabled while degraded and the reason is linked with aria-describedby (5.4-6) | PASS | ja-375: disabled=true describedby=_r_0_-cart reason="通信が不安定です表示は 02:21 時点の内容です。再接続ま" / zh-320: disabled=true describedby=_r_0_-cart reason="網路連線不穩目前顯示 02:22 的內容。恢復連線前無法送出" |
| UX-5 watchdog: pending cart requests (no offline event, no response) still flip to degraded within 7.5 s; cleared after they complete (5.4-6) | PASS | ja-375: degraded 5618ms, cleared 152ms / zh-320: degraded 5791ms, cleared 152ms |
| UX-4: right after confirm cooking = waiting for staff and payment = unpaid; after staff accepts only the cooking track advances (5.4-6) | PASS | ja-375: 店員の受付待ち / 未払い -> 受付済み / 未払い (18588ms, live regions 1) / zh-320: 等待店員接單 / 尚未付款 -> 待製作 / 尚未付款 (18585ms, live regions 1) |
| Focus ring visible and not clipped (clippedBy empty): category item, product action, sheet close, radio, quantity, add, cart bar, stepper +/-, confirm, ORDER_READ language button (5.4-7) | PASS | clipped/no ring: none; unverified: none |
| Keyboard (5.5): Enter opens sheet with focus inside; 20x Tab / 20x Shift+Tab stay inside; Esc closes and returns focus; background aria-hidden/inert while open and cleared after; preview focus targets (5.5) | PASS | 2 combos ok |
| Focus destinations when the opener disappears (3.2): empty cart -> menu heading; session expiry -> entry-card heading; no aria-hidden left behind (3.2) | PASS | ja-375, zh-320 ok |
| Category nav (3.3): items >= 44px, selecting scrolls to a tabindex=-1 heading that is focused, not hidden behind the nav, exactly one aria-current (3.3) | PASS | ja-375: 3 items, h2[tabindex=-1] cur=1; h2[tabindex=-1] cur=1 / zh-320: 3 items, h2[tabindex=-1] cur=1; h2[tabindex=-1] cur=1 |
| ORDER_READ language button >= 44x44 (baseline 76x29) (5.4-4) | PASS | ja-375: 86x44 lang=zh-Hant, zh-320: 71x44 lang=ja |
| Guest JS gzip total <= 125 KB (baseline 99.7 KB, Vite accounting) (5.3) | PASS | 123.12 KB (level-6, KB=1000; harness level-9 estimate 122.88 KB), decoded 392207 B in 5 chunks |
| DS-1 regression: staff / KDS / catalog = 20 states, AA failures 0, horizontal overflow 0 (5.4-8) | n/a | skipped |
| Selector health: config keys that never matched anything in any state (adjust UI_AFTER.sel) (info) | PASS | all keys matched at least once |
| Harness steps that threw (a failed step means states after it may be missing) (info) | PASS | none |

## Undersized targets (first viewport state, ja-375)

0/14 counted targets below 44x44. Choice controls: 
