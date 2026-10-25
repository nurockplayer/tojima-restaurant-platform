# DS-2 rendered acceptance: main-9576590

- UI config: `after`; base `http://127.0.0.1:8082`; run 2026-10-25T04:51:19.743Z; Brave headless (Chromium, reports Chrome/156.0.8078.48), mobile emulation with touch; node v24.15.0
- Viewports ja/zh x 375x812 and 320x700 only (DPR 2, mobile). Screenshots and the 1 MB raw JSON were not committed.

## Per-viewport key numbers

| combo | first product top | fully visible rows | median / max row | Tab to first action | AA fails | overflow | undersized targets | sticky/fixed (menu) |
|---|---|---|---|---|---|---|---|---|
| ja-375 | 367px | 3/9 | 118 / 118px | 6 | 0 | 0 | 0 | nav._nav_yuvqj_1 sticky |
| ja-320 | 415px | 2/9 | 118 / 118px | 6 | 0 | 0 | 0 | nav._nav_yuvqj_1 sticky |
| zh-375 | 351px | 3/9 | 117 / 117px | 6 | 0 | 0 | 0 | nav._nav_yuvqj_1 sticky |
| zh-320 | 395px | 2/9 | 117 / 117px | 6 | 0 | 0 | 0 | nav._nav_yuvqj_1 sticky |

## Acceptance checks

| check | result | evidence |
|---|---|---|
| Computed-style AA contrast failures = 0 (guest states) (5.4-1) | PASS | 58 states, 0 failures |
| Minimum rendered font size >= 12px (5.4-1) | PASS | min 12px |
| No horizontal overflow (also with a sheet open) (5.4-1) | PASS | 58 states incl. 24 with a sheet open |
| First viewport: >=3 fully visible rows @375, >=2 @320; median row height <= 120px (all rows) (5.4-2) | PASS | ja-375: top 367px fully 3 median 118px; ja-320: top 415px fully 2 median 118px; zh-375: top 351px fully 3 median 117px; zh-320: top 395px fully 2 median 117px |
| Scrolled to the bottom: category nav and cart bar stay inside the viewport (5.4-3) | PASS | 4 combos ok |
| Tab traversal: focus stops fully hidden behind the category nav / cart bar = 0 (5.4-3) | PASS | nav/bar: 0; any fixed/sticky overlay: 0; 16 page traversals (154 stops) |
| Tab presses from page start to first product action <= 8 (baseline 8) (5.4-3) | PASS | ja-375/menu-first-viewport:6; ja-375/menu-with-cart:6; ja-320/menu-first-viewport:6; ja-320/menu-with-cart:6; zh-375/menu-first-viewport:6; zh-375/menu-with-cart:6; zh-320/menu-first-viewport:6; zh-320/menu-with-cart:6 |
| No visible text is cut off by ellipsis / clipping (cart-bar sync text must show the HH:mm) (5.4-7) | PASS | 58 states, none |
| Every operable element >= 44x44px (label-wrapped radio/checkbox: label rect; sr-only, disabled and inert background listed, not counted) (5.4-4) | PASS | 58 states, 0 undersized |
| DEMO: 1 page banner, 1 DemoNotice in every open ordering sheet, 1 on ORDER_READ (5.4-5) | PASS | page banner !=1 in: none; sheets: 20 checked, bad: none; order-read bad: none |
| Topbar contains 0 platform names (東島 / TABLE ORDER / TOJIMA KITCHEN / OPEN TABLE) (5.4-5) | PASS | topbar counts: ja-375=0, ja-320=0, zh-375=0, zh-320=0; whole page: ja-375=0[], ja-320=0[], zh-375=0[], zh-320=0[] |
| Footer shows "Powered by Tojima" exactly once (5.4-5) | PASS | ja-375=1, ja-320=1, zh-375=1, zh-320=1 |
| Store name is the first topbar text (5.4-5) | PASS | ja-375:"博多餃子酒場 シロナガスクジラ", ja-320:"博多餃子酒場 シロナガスクジラ", zh-375:"博多餃子酒場 シロナガスクジラ", zh-320:"博多餃子酒場 シロナガスクジラ" |
| Guest root and sheet font-family starts with system-ui; zh contains PingFang TC (5.4-5) | PASS | ja-375: system-ui, -apple-system, "Hiragino  / ja-320: system-ui, -apple-system, "Hiragino  / zh-375: system-ui, -apple-system, "PingFang  / zh-320: system-ui, -apple-system, "PingFang  |
| html carries data-surface="guest" while the guest app is mounted (3.11) | PASS | ja-375=guest, ja-320=guest, zh-375=guest, zh-320=guest |
| UX-3: add with required group unselected => 0 cart POST/PUT/DELETE, error shown, aria-invalid, focus on the first option of that group, sheet stays open (5.4-6) | PASS | ja-375: posts=0 err=true invalid=1 focusFirst=true describedby=true / ja-320: posts=0 err=true invalid=1 focusFirst=true describedby=true / zh-375: posts=0 err=true invalid=1 focusFirst=true describedby=true / zh-320: posts=0 err=true invalid=1 focusFirst=true describedby=true |
| UX-5: offline => degraded notice with HH:mm within 7.5 s (page, sheet, cart bar), preview disabled, cart lines unchanged, cleared <= 5 s after online (5.4-6) | PASS | ja-375: 154ms hhmm=13:52 bar="カートを開く 2 点 税込・目安 ¥1,940 同期待ち・1" sheetNotice=true previewDisabled=true (reason: 通信が不安定です表示は 13:52 時点の内容で) lines 2->2 recovered 1837ms / ja-320: 10ms hhmm=13:53 bar="カートを開く 2 点 税込・目安 ¥1,940 同期待ち・1" sheetNotice=true previewDisabled=true (reason: 通信が不安定です表示は 13:53 時点の内容で) lines 2->2 recovered 1371ms / zh-375: 38ms hhmm=13:55 bar="開啟購物車 2 件 含稅・預估 ¥1,940 等待同步・13" sheetNotice=true previewDisabled=true (reason: 網路連線不穩目前顯示 13:55 的內容。恢復連) lines 2->2 recovered 1931ms / zh-320: 174ms hhmm=13:57 bar="開啟購物車 2 件 含稅・預估 ¥1,940 等待同步・13" sheetNotice=true previewDisabled=true (reason: 網路連線不穩目前顯示 13:57 的內容。恢復連) lines 2->2 recovered 1091ms |
| UX-5: "confirm order" is disabled while degraded and the reason is linked with aria-describedby (5.4-6) | PASS | ja-375: disabled=true describedby=_r_0_-cart reason="通信が不安定です表示は 13:52 時点の内容です。再接続ま" / ja-320: disabled=true describedby=_r_0_-cart reason="通信が不安定です表示は 13:53 時点の内容です。再接続ま" / zh-375: disabled=true describedby=_r_0_-cart reason="網路連線不穩目前顯示 13:55 的內容。恢復連線前無法送出" / zh-320: disabled=true describedby=_r_0_-cart reason="網路連線不穩目前顯示 13:57 的內容。恢復連線前無法送出" |
| UX-5 watchdog: pending cart requests (no offline event, no response) still flip to degraded within 7.5 s; cleared after they complete (5.4-6) | PASS | ja-375: degraded 6539ms, cleared 153ms / ja-320: degraded 5540ms, cleared 488ms / zh-375: degraded 7437ms, cleared 314ms / zh-320: degraded 5716ms, cleared 153ms |
| UX-4: right after confirm cooking = waiting for staff and payment = unpaid; after staff accepts only the cooking track advances (5.4-6) | PASS | ja-375: 店員の受付待ち / 未払い -> 受付済み / 未払い (18777ms, live regions 1) / ja-320: 店員の受付待ち / 未払い -> 受付済み / 未払い (16725ms, live regions 1) / zh-375: 等待店員接單 / 尚未付款 -> 待製作 / 尚未付款 (17340ms, live regions 1) / zh-320: 等待店員接單 / 尚未付款 -> 待製作 / 尚未付款 (18624ms, live regions 1) |
| Focus ring visible and not clipped (clippedBy empty): category item, product action, sheet close, radio, quantity, add, cart bar, stepper +/-, confirm, ORDER_READ language button (5.4-7) | PASS | clipped/no ring: none; unverified: none |
| Keyboard (5.5): Enter opens sheet with focus inside; 20x Tab / 20x Shift+Tab stay inside; Esc closes and returns focus; background aria-hidden/inert while open and cleared after; preview focus targets (5.5) | PASS | 4 combos ok |
| Focus destinations when the opener disappears (3.2): empty cart -> menu heading; session expiry -> entry-card heading; no aria-hidden left behind (3.2) | PASS | ja-375, zh-320 ok |
| Category nav (3.3): items >= 44px, selecting scrolls to a tabindex=-1 heading that is focused, not hidden behind the nav, exactly one aria-current (3.3) | PASS | ja-375: 3 items, h2[tabindex=-1] cur=1; h2[tabindex=-1] cur=1 / ja-320: 3 items, h2[tabindex=-1] cur=1; h2[tabindex=-1] cur=1 / zh-375: 3 items, h2[tabindex=-1] cur=1; h2[tabindex=-1] cur=1 / zh-320: 3 items, h2[tabindex=-1] cur=1; h2[tabindex=-1] cur=1 |
| ORDER_READ language button >= 44x44 (baseline 76x29) (5.4-4) | PASS | ja-375: 86x44 lang=zh-Hant, ja-320: 86x44 lang=zh-Hant, zh-375: 71x44 lang=ja, zh-320: 71x44 lang=ja |
| Guest JS gzip total <= 125 KB (baseline 99.7 KB, Vite accounting) (5.3) | PASS | 123.41 KB (level-6, KB=1000; harness level-9 estimate 123.18 KB), decoded 392207 B in 5 chunks |
| DS-1 regression: staff / KDS / catalog = 20 states, AA failures 0, horizontal overflow 0 (5.4-8) | PASS | 20/20 states, AA failures 0, overflow: none, min font 12px |
| Selector health: config keys that never matched anything in any state (adjust UI_AFTER.sel) (info) | PASS | all keys matched at least once |
| Harness steps that threw (a failed step means states after it may be missing) (info) | PASS | none |

## DS-1 regression (staff / KDS / catalog)

20/20 states, AA failures 0, horizontal overflow states: none, min font 12px. Compare screenshots with `node compare.mjs <before-dir> <after-dir>`.

## Undersized targets (first viewport state, ja-375)

0/14 counted targets below 44x44. Choice controls: 
