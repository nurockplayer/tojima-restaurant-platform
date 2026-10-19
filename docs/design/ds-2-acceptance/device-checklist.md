# DS-2 device checklist / DS-2 実機チェック / DS-2 實機檢查

This checklist implements P-89 §5.7 for P-84 7g physical-device acceptance. It is **not a DS-2 merge gate**. Completing it is required before a remote demo or pilot store. DS-2 does not mark P-84 7f or 7g PASS; record findings against the existing human acceptance gate.

## iPhone Safari

Device / iOS / Safari version: ____________________  Locale: ☐ ja ☐ zh-Hant  Date: __________

- [ ] With Safari's dynamic toolbar expanded and collapsed, the sticky category navigation remains visible at the top while scrolling the menu.
- [ ] With Safari's dynamic toolbar expanded and collapsed, the fixed cart bar remains reachable and does not cover the final menu action.
- [ ] The bottom safe area is respected by the cart bar and by the bottom sheet; no action is hidden behind the home indicator.
- [ ] Open and close the option sheet and cart sheet; background scrolling stays locked while either sheet is open and resumes after close.
- [ ] Scroll long sheet contents to the end and back; the sheet remains usable and the page behind it does not move.
- [ ] With VoiceOver, enter each sheet, move through its controls, and close it. Confirm focus returns to its trigger; if it disappeared, confirm an empty-cart close goes to the menu heading, confirm/replay goes to the orders heading, and session expiry goes to the entry-card heading.
- [ ] With VoiceOver, leave a required option unselected and press Add; hear the error, move to the first available option in that group, and confirm no add occurred.
- [ ] With VoiceOver, read an order card's cooking and payment tracks. Confirm the two tracks are understandable and do not announce duplicate live updates.
- [ ] Enable Airplane Mode while on the menu. Confirm degraded notice appears within 7.5 seconds and is readable on the page and in an open cart sheet; cart lines remain and preview/confirm are unavailable. Restore connectivity and confirm the notice clears within 5 seconds of network restoration after an accepted cart read; the online event alone must not clear it.

Notes / evidence: ______________________________________________________________

## Android Chrome

Device / Android / Chrome version: ____________________  Locale: ☐ ja ☐ zh-Hant  Date: __________

- [ ] With Chrome's dynamic toolbar expanded and collapsed, the sticky category navigation remains visible at the top while scrolling the menu.
- [ ] With Chrome's dynamic toolbar expanded and collapsed, the fixed cart bar remains reachable and does not cover the final menu action.
- [ ] The bottom safe area is respected by the cart bar and by the bottom sheet; no action is hidden behind system navigation.
- [ ] Open and close the option sheet and cart sheet; background scrolling stays locked while either sheet is open and resumes after close.
- [ ] Scroll long sheet contents to the end and back; the sheet remains usable and the page behind it does not move.
- [ ] With TalkBack, enter each sheet, move through its controls, and close it. Confirm focus returns to its trigger; if it disappeared, confirm an empty-cart close goes to the menu heading, confirm/replay goes to the orders heading, and session expiry goes to the entry-card heading.
- [ ] With TalkBack, leave a required option unselected and press Add; hear the error, move to the first available option in that group, and confirm no add occurred.
- [ ] With TalkBack, read an order card's cooking and payment tracks. Confirm the two tracks are understandable and do not announce duplicate live updates.
- [ ] Enable Airplane Mode while on the menu. Confirm degraded notice appears within 7.5 seconds and is readable on the page and in an open cart sheet; cart lines remain and preview/confirm are unavailable. Restore connectivity and confirm the notice clears within 5 seconds of network restoration after an accepted cart read; the online event alone must not clear it.

Notes / evidence: ______________________________________________________________

## 日本語

このチェックリストは P-89 §5.7 に基づく P-84 7g の実機確認用。**DS-2 のマージ条件ではない**が、リモートデモまたは試験店舗へ出す前に完了する。DS-2 では P-84 7f／7g を PASS にしない。結果は既存の人手による受入ゲートに記録する。

### iPhone Safari

端末／iOS／Safari バージョン：____________________　言語：☐ ja ☐ zh-Hant　日付：__________

- [ ] Safari の動的ツールバーを表示・収納した両方の状態でメニューをスクロールし、カテゴリナビが上部に表示され続ける。
- [ ] 両方のツールバー状態でカートバーを操作でき、最終商品の操作を覆わない。
- [ ] カートバーと下部シートが下端 safe-area を守り、ホームインジケータの背後に操作要素が隠れない。
- [ ] オプションシートとカートシートを開閉する。シート表示中は背景のスクロールが止まり、閉じると再開する。
- [ ] 長いシート内容を末尾までスクロールして戻す。シートを操作でき、背後のページは動かない。
- [ ] VoiceOver で各シートに入り、操作要素を移動して閉じる。フォーカスが呼出元に戻ることを確認する。呼出元が消えた場合は、カートを空にして閉じるとメニュー見出し、確定／replay 後は注文見出し、セッション失効時は入場カード見出しへ移動することを確認する。
- [ ] VoiceOver で必須オプションを未選択のまま追加を押す。エラーが読み上げられ、最初の選択可能な選択肢へ移動し、追加されないことを確認する。
- [ ] VoiceOver で注文カードの調理・支払い2トラックを読む。意味を理解でき、同じ更新が重複したライブ読み上げにならないことを確認する。
- [ ] メニュー表示中に機内モードを有効にする。7.5秒以内に通信不安定表示が出て、時刻付き通知がページと開いたカートシートに読め、カート行が保持され、見積・確定が使えないことを確認する。通信を戻し、受理された cart 読取によりネットワーク復帰から5秒以内に通知が消えることを確認する。online イベントだけでは通知が消えない。

記録／証跡：________________________________________________________________

### Android Chrome

端末／Android／Chrome バージョン：____________________　言語：☐ ja ☐ zh-Hant　日付：__________

- [ ] Chrome の動的ツールバーを表示・収納した両方の状態でメニューをスクロールし、カテゴリナビが上部に表示され続ける。
- [ ] 両方のツールバー状態でカートバーを操作でき、最終商品の操作を覆わない。
- [ ] カートバーと下部シートが下端 safe-area を守り、システムナビゲーションの背後に操作要素が隠れない。
- [ ] オプションシートとカートシートを開閉する。シート表示中は背景のスクロールが止まり、閉じると再開する。
- [ ] 長いシート内容を末尾までスクロールして戻す。シートを操作でき、背後のページは動かない。
- [ ] TalkBack で各シートに入り、操作要素を移動して閉じる。フォーカスが呼出元に戻ることを確認する。呼出元が消えた場合は、カートを空にして閉じるとメニュー見出し、確定／replay 後は注文見出し、セッション失効時は入場カード見出しへ移動することを確認する。
- [ ] TalkBack で必須オプションを未選択のまま追加を押す。エラーが読み上げられ、最初の選択可能な選択肢へ移動し、追加されないことを確認する。
- [ ] TalkBack で注文カードの調理・支払い2トラックを読む。意味を理解でき、同じ更新が重複したライブ読み上げにならないことを確認する。
- [ ] メニュー表示中に機内モードを有効にする。7.5秒以内に通信不安定表示が出て、時刻付き通知がページと開いたカートシートに読め、カート行が保持され、見積・確定が使えないことを確認する。通信を戻し、受理された cart 読取によりネットワーク復帰から5秒以内に通知が消えることを確認する。online イベントだけでは通知が消えない。

記録／証跡：________________________________________________________________

## 繁體中文

本清單依 P-89 §5.7 執行 P-84 7g 實機檢查。**這不是 DS-2 合併門檻**，但在遠端 demo 或試營運店上線前必須完成。DS-2 不將 P-84 7f／7g 標記為 PASS。結果應記錄在既有人工驗收 gate。

### iPhone Safari

裝置／iOS／Safari 版本：____________________　語言：☐ ja ☐ zh-Hant　日期：__________

- [ ] 在 Safari 動態工具列展開與收合時捲動菜單，分類導覽都持續顯示於頂端。
- [ ] 兩種工具列狀態下皆可操作購物車列，且不遮住最後一項商品操作。
- [ ] 購物車列與底部 sheet 尊重底部 safe-area，操作項目不被 home indicator 遮住。
- [ ] 開啟並關閉選項 sheet 與購物車 sheet；sheet 開啟時背景捲動鎖定，關閉後恢復。
- [ ] 將較長的 sheet 內容捲至底部再返回；sheet 仍可操作，背景頁面不移動。
- [ ] 使用 VoiceOver 進入每個 sheet、移動瀏覽控制項並關閉；確認焦點回到觸發項目。若觸發項目已消失，確認清空購物車後關閉會移至菜單標題、確認／replay 後會移至訂單標題、session 失效時會移至入場卡片標題。
- [ ] 使用 VoiceOver 在必選項目未選時按加入；確認錯誤會朗讀、焦點移至該組第一個可選項目，且沒有加入商品。
- [ ] 使用 VoiceOver 閱讀訂單卡的烹調與付款兩條狀態軌；確認內容清楚，且不會重複朗讀即時更新。
- [ ] 在菜單頁開啟飛航模式。確認 7.5 秒內出現降級通知，頁面與已開啟的購物車 sheet 都有可讀的時間標示、購物車明細保留、預覽與確認不可用。恢復網路後，確認透過已接受的 cart 讀取在 5 秒內清除通知；單獨的 online 事件不會清除通知。

紀錄／證據：________________________________________________________________

### Android Chrome

裝置／Android／Chrome 版本：____________________　語言：☐ ja ☐ zh-Hant　日期：__________

- [ ] 在 Chrome 動態工具列展開與收合時捲動菜單，分類導覽都持續顯示於頂端。
- [ ] 兩種工具列狀態下皆可操作購物車列，且不遮住最後一項商品操作。
- [ ] 購物車列與底部 sheet 尊重底部 safe-area，操作項目不被系統導覽區遮住。
- [ ] 開啟並關閉選項 sheet 與購物車 sheet；sheet 開啟時背景捲動鎖定，關閉後恢復。
- [ ] 將較長的 sheet 內容捲至底部再返回；sheet 仍可操作，背景頁面不移動。
- [ ] 使用 TalkBack 進入每個 sheet、移動瀏覽控制項並關閉；確認焦點回到觸發項目。若觸發項目已消失，確認清空購物車後關閉會移至菜單標題、確認／replay 後會移至訂單標題、session 失效時會移至入場卡片標題。
- [ ] 使用 TalkBack 在必選項目未選時按加入；確認錯誤會朗讀、焦點移至該組第一個可選項目，且沒有加入商品。
- [ ] 使用 TalkBack 閱讀訂單卡的烹調與付款兩條狀態軌；確認內容清楚，且不會重複朗讀即時更新。
- [ ] 在菜單頁開啟飛航模式。確認 7.5 秒內出現降級通知，頁面與已開啟的購物車 sheet 都有可讀的時間標示、購物車明細保留、預覽與確認不可用。恢復網路後，確認透過已接受的 cart 讀取在 5 秒內清除通知；單獨的 online 事件不會清除通知。

紀錄／證據：________________________________________________________________
