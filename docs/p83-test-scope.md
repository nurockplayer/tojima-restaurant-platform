# P-83 lane A — individual order read / 個別注文閲覧 / 個別訂單查閱

Lane A implements MOD-05 S8 ORDER_READ on main `46b2391`. Lane B part 1 owns
paid-under-review wording. Refund display belongs to lane C / P-82 C.
Payment/operations transitions and original-browser CHECKOUT_RESUME are reused
without modification.

## Capability and API contract

- TABLE_JOIN edits/submits the currently OPEN session's shared cart. Existing
  shared committed-order/menu/operations summaries remain the accepted P-78/P-79
  contract; they contain no ORDER_READ token. They do not authorize this new
  individual status endpoint or survive seating change.
- A new successful commit atomically inserts a 256-bit cryptographically random
  ORDER_READ grant and returns it only in that commit response. The database
  stores SHA-256 of its base64url bearer, full server-owned order scope, creation,
  expiry and optional revocation time. No raw token is persisted or logged.
- A losing commit or replay returns no read grant. A current TABLE_JOIN holder
  can explicitly request a fresh, independent grant with
  `POST /v1/guest/sessions/{sessionID}/orders/{orderID}/read-grant`. This is the
  chosen sharing design: no automatic fanout to all joined guests. A submitter
  whose commit response was lost uses this same explicit recovery request while
  the session remains OPEN. Session/grant locks and a final trusted-time check
  serialize this issuance with close, expiry and join revocation. No catalog
  availability or provider call is needed to request a read link.
- Issuance allows at most **5 active, unexpired, unrevoked grants per
  (session, order)**, including the grant returned by a new commit. The existing
  session lock serializes the budget check and insert across joined guests.
  Further requests return HTTP **429** with `order_read_limit_reached` and
  insert no row; expiry or internal revocation frees a slot. Scope/credential
  denials remain HTTP 403 even when the budget is full.
- `GET /v1/guest/orders/{orderID}/status` requires the specific ORDER_READ bearer
  in `Authorization`. Neither an order ID nor TABLE_JOIN nor another order's
  read bearer authorizes it. Query parameters are rejected. Invalid, absent,
  expired, revoked and wrong-order credentials all return the same HTTP 403
  body, without disclosing whether the order exists. Tenant/store are resolved
  through the grant's full composite foreign key and canonical order/state
  joins, with enabled tenant/store checks, never client scope fields.
- The allowlisted response has exactly `version`, `payment_status`,
  `fulfillment_status`, `display_state`, `total_gross_jpy` (decimal string),
  `currency: JPY`, and server-computed `is_terminal`. Amount comes from the immutable committed
  snapshot. Version and both states come from `operation_order_state`, read in
  the same SQL statement as authorization. No provider/session/payment IDs,
  internal holds/reasons, audit, Checkout URL or other order is returned.
- Conservative lane A terminal policy: PAID + SERVED with neither operational
  hold nor payment review. All other combinations remain nonterminal; refunds
  remain separate; lane B part 1 adds review-specific guest wording below. The
  client never infers a terminal state from payment/fulfillment text.
- Status and grant responses (including errors), and commit responses, use
  `Cache-Control: private, no-store` and `Referrer-Policy: no-referrer`.
  No new logging is added; request instrumentation must not log Authorization
  headers or capability response bodies.
- ORDER_READ is independent of TABLE_JOIN: existing read links survive close
  until their own expiry/revocation. A later party obtains a new session through
  the same physical QR and cannot request grants for the previous seating.
  Deliberately shared links and previously saved links remain bearer authority;
  an already authorized browser is not converted into an unauthorized browser
  by close. A lost/expired link cannot be reconstructed from the physical QR.
- ORDER_READ cannot edit/commit carts, issue another grant, establish Checkout
  context or replace the independent original submitter cookie proof. No
  CHECKOUT_RESUME code or payment/operations transitions are changed.

## TTL configuration — ORDER_READ TTL approved (D5); rest of EXT-4 open

`ordering.DefaultOrderReadTTL` is **24 hours**, approved for the MVP pilot by
Owner decision D5 (2026-10-19). The constructor
`ordering.NewWithOrderReadTTL` accepts 1 minute through 7 days inclusive and
rejects values outside that range; existing constructors use that default.
Composition configures it once, never from guest input. Expiry uses trusted
database time. `RevokeOrderRead` is an internal token-specific lifecycle
method with no guest write endpoint. Neither expiry nor revocation changes
immutable orders, payment, fulfillment or other grants.

**D5 approves only the 24-hour ORDER_READ lifetime for the MVP pilot. Other
TTLs and retention/sharing policy remain open (EXT-4). This feature promises no
indefinite history.**

## Guest web contract

`/order#order=…&grant=…&expires=…` is a directly addressable, lazily loaded read
screen. The isolated `guesttest` runtime also serves `/order` directly, tested
by `TestCopiedOrderLinkDirectEntry`; the operations runtime already has SPA
fallback. The fragment is not sent in the HTTP request or referrer; JS derives the
bearer and calls the shared `web/src/shared/http.ts` transport. Copy uses the
clipboard API and exposes a selectable link when clipboard access fails. The
screen explains that possessing the time-limited link grants viewing access.

ADR-0006 explicitly makes a new P-83 read/poll screen the first TanStack Query
consumer. This change pins `@tanstack/react-query` and uses a dedicated QueryClient
per capability, keyed by order + grant, cleared/cancelled on unmount or grant
change. Retry, focus/reconnect behavior, cache lifetime and polling are explicit.
The Query-provided AbortController signal reaches fetch; hide cancels pending
requests. Even a transport settling after cancellation cannot overwrite a
resumed view or overlap another request. A visible unresolved view polls every
30 seconds by default, configurable/clamped to 15–30 seconds; hidden/unmounted
views have no polling. Returning to visible refreshes immediately, including a
previously terminal result. Server terminal state stops periodic polling.
Failed reads retain the last confirmed values and show a stale notice, with no
invented payment or fulfillment result. This polling is independent of the
existing 2.5-second shared-cart loop and existing Checkout continuation UI.

The Japanese `マイ注文` / Traditional Chinese `我的訂單` list stores up to 100
unexpired per-order links in this browser's localStorage. Reads/writes use
try/catch, including a memory fallback for quota errors when reads still work.
Malformed and expired entries are omitted. This is a convenience list of bearer
links, not a user account or guaranteed/durable order history. New commit grants
are saved automatically; joined guests explicitly save individual order links.
Both new surface dictionaries have identical key sets checked by TypeScript.
No router library is needed for this standalone direct-entry surface, with no
nested navigation/history/URL-state requirements. Styling uses CSS Modules.

## Coverage and verification

`TestPostgresOrderReadGrantBoundary` uses the real isolated-schema PostgreSQL
fixture and complete payments HTTP runtime. It checks three guests; explicit
joined-guest issuance; no grants in commit replays; wrong tenant/store/order and
nonexistent-order uniform denial; exact JPY above 2^53; random distinct grants;
hash-only database storage; expired/revoked tokens; response allowlist/headers;
ORDER_READ denial for submit/grant issuance/Checkout context/resume; real cash,
acceptance and kitchen transitions; terminal/version changes; real close and
next-party physical-QR isolation; and rejected out-of-bounds TTL configuration.
Repeated and concurrent grant requests check the shared five-grant budget,
stable HTTP 429 errors, no inserts on rejection, and slot recovery after
revocation/expiry, using the same real PostgreSQL fixture and HTTP handler.
The migration test covers 009 up/down/reapply. The runner's explicit version
list supports gaps, so 008 remains reserved for P-82; integration must insert
008 before 009 in the list, with renumbering only if the director chooses it.

New Vitest + Testing Library tests cover initial hidden state, 30-second polling,
immediate resume, a 35-second slow request without overlap, abort on hide and
unmount, a late response after abort, failure/stale/recovery, server terminal
behavior, capability changes, fragment/header transport, clipboard, interval
clamping, persistence, corrupt/expired data and throwing storage. Tests use
user-event with real timers for the copy control, awaiting the rendered result
before checking clipboard and fragment/bearer transport assertions. Polling
tests retain fake timers. They are added for the director to execute:
**Vitest is not run by the implementing sandbox**, per lane instructions.

Required handoff checks: `make fmt-check vet build`, real PostgreSQL
`go test -race -count=1 ./...`, frontend oxlint/tsc/build. The director runs
`pnpm --dir web check` and sends the exact result for separate independent
review. Browser/device acceptance, production configuration/hosting, and
Owner TTL approval remain outside this implementation evidence.

## 日本語

P-83 A は注文ごとの匿名・期限付き ORDER_READ を追加する。新規確定の応答で
本人に発行し、同じ OPEN 卓の参加者は注文単位で明示的に取得する。DB は hash
のみ保存し、失効・期限・tenant/store/order の実データ所属を検証する。閉卓後も
既発行リンクは自身の期限まで有効だが、同じ物理 QR の次の客は前卓の閲覧権限を
取得できない。支払継続は既存の本人 cookie proof のみであり、読取権限では不可。
発行上限は (session, order) ごとに有効期限内・未失効の権限5件（新規確定時の1件を
含む）。既存のセッションロック内で検査・発行し、超過は行を追加せず HTTP 429
`order_read_limit_reached` を返す。期限切れ・内部失効で枠が空き、並行要求でも
上限を超えないことを PostgreSQL 実試験で検証する。
画面は可視時のみ30秒（設定15〜30秒）で取得し、非表示・終了時は中止、再表示時は
即時確認、失敗時は古い情報の注意を表示する。マイ注文とコピーリンクを提供する。
PostgreSQL 実試験とフロント試験を追加した。Vitest と独立レビューは director が
実施する。**24時間は D5（2026-10-19）で MVP 試店向けに承認済み。他の TTL・保存方針は
未決（EXT-4）。無期限の履歴を約束しない。** 要確認文言は下記 P-83 B で追加し、返金表示は lane C / P-82 C に残す。

## 繁體中文

P-83 A 加入單筆訂單匿名、限時、唯讀的 ORDER_READ。新送單成功時發給送單者，
同一 OPEN 桌次的參與者須逐筆明確申請。DB 只存 hash，驗證到期、撤銷與服務端
tenant/store/order 所屬。關桌後原連結依自身期限有效，但下一組透過相同實體 QR
加入的客人無法取得前桌權限。續付沿用原送單瀏覽器的 cookie proof，唯讀連結不能
代替。新畫面只在可見時每30秒（設定限制15〜30秒）輪詢，隱藏或卸載即中止，重新
可見立即更新，失敗保留已確認值並顯示過期提示。提供我的訂單與複製連結、儲存失敗
的記憶體備援。已新增真實 PostgreSQL 與前端測試；Vitest 與獨立審查由 director
執行。**24小時已由 D5（2026-10-19）核准用於 MVP 試店；其他 TTL 與保留政策仍待決定
（EXT-4），不承諾永久歷史。** 付款待店家確認文案由下述 P-83 B 加入，退款顯示仍屬 lane C / P-82 C。

每組 (session, order) 最多可持有5筆未到期且未撤銷的權限，包含新送單時發出的
1筆。檢查與新增都在既有桌次鎖內執行；超額回傳 HTTP 429
`order_read_limit_reached`，不新增資料列。到期或內部撤銷後可釋出名額；真實
PostgreSQL 測試涵蓋重複及並行申請，確認不超過上限。

## Implementer execution receipt

- `make fmt-check vet build`: PASS, with writable temporary Go build/module
  caches (the default user caches are read-only in this sandbox).
- `TEST_DATABASE_URL=… go test -race -count=1 ./...`: PASS against the supplied
  disposable loopback PostgreSQL. After the direct-link route and expanded
  migration/TTL assertions, the affected `./cmd/guesttest`,
  `./migrations/postgres`, and `./internal/httpapi` packages were rerun with
  `-race -count=1`: PASS.
- `python3 scripts/migration-smoke.py`: PASS (isolated legacy SQLite smoke).
- `pnpm --dir web install --frozen-lockfile`, `pnpm --dir web lint`,
  `pnpm --dir web exec tsc -b --pretty false`, and `pnpm --dir web build`: PASS;
  final frontend lint/type/build checks used the repository's Node 24.15.0 and
  pnpm 12.11.2. The new TanStack Query dependency is pinned to 5.104.3.
- Vitest / `pnpm --dir web check`: NOT RUN by the implementing sandbox as
  instructed; director execution remains required. No Ego-lite browser/device
  validation or separate independent review was performed by this lane.
- No push, PR, deployment or external-state mutation is part of this handoff.


## P-83 B (review display)

### English

Part 1 adds the server-derived `display_state` to the existing ORDER_READ status
allowlist and the in-session guest operations summary. `PAID_UNDER_REVIEW` means
payment is PAID, `payment_review_required` is true and no durable KDS card exists.
It renders exactly **お支払いを確認しました。店舗でご注文を確認中です** / **已確認付款，店家正在確認您的訂單**
in both guest surfaces, without kitchen/preparing or refund text. The status DTO
remains nonterminal, so visible ORDER_READ polling continues; the existing
in-session history polling also continues. The Checkout continuation panel is
suppressed while the summary shows review or an unsupported display state.

`STANDARD` uses the existing payment/kitchen labels only for explicitly supported
payment and fulfillment values. `UNKNOWN`, unsupported/missing display states,
refund states and unrecognized raw states use a neutral staff-confirmation
message. They cannot fall through to a paid/served success label. The server
projection uses existing columns and KDS-card presence; review flags, hold
reasons, provider identifiers and other internal fields are never added to the
guest allowlists. Payment and operations state transitions are unchanged.
After the separately owned P-82 B manager release, the next read uses the
existing kitchen state normally. That manager flow is unavailable in this lane;
its integration acceptance remains with P-82 B. Refund display remains lane C /
P-82 C.

PostgreSQL coverage reuses the fake payment provider and a signed paid webhook
after an operational hold. It verifies actual PAID/review/no-card state,
`PAID_UNDER_REVIEW`, `is_terminal=false`, both guest DTO allowlists, and a real
paid/released control order. Projection tests cover card/review combinations,
refunds and future values. Vitest covers both exact translations, neutral
fallbacks, continued polling and the next kitchen display after release.

### 日本語

P-83 B part 1 は ORDER_READ と同一卓内の注文履歴に、サーバー導出の
`display_state` を追加する。PAID・支払確認要・KDS カード未作成の場合は
`PAID_UNDER_REVIEW` とし、**お支払いを確認しました。店舗でご注文を確認中です**
をそのまま表示する。調理中・返金とは表示しない。`is_terminal=false` のため
可視時の取得を続け、既存の注文履歴も定期更新を続ける。要確認・未対応状態では
支払継続パネルを表示しない。未知の表示状態・支払状態・調理状態、未対応の返金は
成功表示へ落とさず「店舗に確認してください」と表示する。
内部理由・保留理由・Stripe ID は公開せず、支払・業務の状態遷移は変更しない。
別担当の P-82 B による KDS 解放後は次回取得で通常の厨房状態を表示する。
その管理者フローは本 lane では未提供のため統合受入は別途必要。
返金表示は lane C / P-82 C の範囲とする。
実 PostgreSQL の署名付き支払 webhook、保留あり／なしの DTO・非終端・漏洩防止、
Vitest の日中両文言・未知状態・定期取得継続・解放後表示を検証する。

### 繁體中文

P-83 B part 1 為 ORDER_READ 與同桌訂單歷史加入服務端推導的 `display_state`。
付款為 PAID、需要付款確認且尚無 KDS 卡時回傳 `PAID_UNDER_REVIEW`，兩處皆完整
顯示 **已確認付款，店家正在確認您的訂單**，不顯示製作中或退款。
`is_terminal=false` 讓可見頁面持續輪詢，既有同桌歷史也繼續定期更新。
待確認或未支援狀態不顯示續付面板。未知或缺漏的顯示狀態、未知付款／製作狀態及
尚未支援的退款皆顯示「請洽服務人員確認」，不降級成已付款／已送達的成功文案。
不公開內部原因、保留原因或 Stripe ID，不變更付款與營運狀態轉移。
另由 P-82 B 負責的管理員釋放 KDS 後，下次讀取恢復普通廚房狀態；本 lane 尚無
該管理員流程，須另做整合驗收。退款顯示屬 lane C / P-82 C。
真實 PostgreSQL 測試使用假付款供應者與簽章付款 webhook，涵蓋營運保留、正常釋放、
DTO 白名單與非終止狀態；Vitest 涵蓋日中完整文案、中立回退、輪詢持續及釋放後顯示。

### Lane B part 1 execution receipt

- Base: accepted lane A `c402789`, local branch `codex/p83-b-review-display`.
- `TEST_DATABASE_URL=postgres://postgres@127.0.0.1:55439/tojima_test?sslmode=disable
  go test -race -count=1 ./internal/... ./migrations/postgres`: PASS, all packages,
  with temporary writable Go build/module caches.
- `make fmt-check vet build`: PASS.
- `pnpm --dir web install --frozen-lockfile`, `pnpm --dir web lint`,
  `pnpm --dir web exec tsc -b --pretty false`: PASS, Node 24.15.0 / pnpm 12.11.2.
- `pnpm --dir web exec vitest run src/ui/OrderRead.test.tsx src/ui/App.test.tsx`:
  PASS, 59 tests. `pnpm --dir web test`: PASS, 144 tests across 10 files.
- No P-82 B manager-release integration, Ego-lite browser/device acceptance or
  separate Oracle review is claimed. No push, PR or deployment.

## P-83 B part 2

### English

This part supersedes the earlier refund-display deferrals and original-only
amount contract. Both ORDER_READ and in-session GuestOperations now expose the
same allowlisted details: `total_gross_jpy` remains the immutable original total;
`effective_total_gross_jpy` comes from C1's persisted, server-recomputed tax
result (falling back to the original total before any adjustment).
`effective_lines` contains only `product_name`, public `option_names`, effective
`quantity`, and `line_total_jpy`, calculated from committed prices, including
options, and finalized cancelled quantities. Selected option names retain
committed order and distinguish variants of the same product on both surfaces;
no internal product/option keys are included. `adjusted` remains true after kitchen
acknowledgement. Original totals appear as secondary information; the current
lines and total are primary. No original order snapshot is modified.

The guest sees 「店舗により注文内容が変更されました」 / 「店家已調整您的訂單」
for adjustments and 「この注文は取り消されました」 / 「此訂單已取消」 for
whole-order cancellation. Refunds are independent of payment/kitchen labels:
`refunds` contains only `status`, `amount_jpy`, and `method` (`CASH`/`STRIPE`);
`refunded_total_jpy` sums only SUCCEEDED rows. REQUESTED and PROCESSING render
「返金処理中」 / 「退款處理中」; FAILED renders
「返金を確認中です。店舗にお問い合わせください」 /
「退款確認中，請洽店家」; only SUCCEEDED renders
「返金済み ¥X」 / 「已退款 ¥X」. Unknown states remain neutral. All JPY
amounts are decimal strings at HTTP boundaries. Staff reasons, actors, safe
error codes, provider identifiers and internal refund identifiers are excluded
by construction. Staff DTOs and OrderState remain unchanged.

Pending or kitchen-confirmed C2 stop requests do not affect guest lines,
amounts, adjustment notices or refund summaries. Only finalization writes the
adjustment that becomes visible. Existing server terminal rules and client
polling remain in force, including nonterminal status for unresolved refunds.
Failed refreshes retain the last confirmed effective state with a stale notice;
they cannot promote pending/failed refunds to success.
When no effective snapshot has been confirmed, history does not substitute the
immutable original amount as the current amount; it shows a neutral unavailable
message until the current details can be read.

Coverage includes the director's unpaid CASH ¥660 → ¥330 reduction, original
¥660 retention, cash refunds and whole cancellation, Stripe refund processing,
failure, retry and success, pending/finalized C2 behavior, exact nested response
key allowlists, both languages, and stale refresh/recovery on both web surfaces.
This lane does not push, open a PR, deploy, or change payment/operations state
transitions. Earlier execution receipts describe their own historical lanes;
the B part 2 receipt below records this candidate's verification.

### 日本語

P-83 B part 2 は、ORDER_READ と同一卓内の注文履歴の両方に現在の有効な
商品・数量・税込合計を表示する。C1 がサーバー側で税を再計算して保存した金額を
使用し、元の注文スナップショットは変更しない。変更後の表示を主とし、元の合計は
補足情報として残す。「店舗により注文内容が変更されました」を表示し、厨房での
変更確認後も変更済みの表示を維持する。選択したオプション名も保持して、同じ商品
の異なる選択内容を区別できるようにする。内部キーは公開しない。
全取消は「この注文は取り消されました」。

返金はサーバーの返金行のみから表示する。REQUESTED／PROCESSING は
「返金処理中」、FAILED は「返金を確認中です。店舗にお問い合わせください」、
SUCCEEDED のみ「返金済み ¥X」。支払済みや取消済みの状態から返金成功を
推測しない。返金合計は成功した返金だけを集計する。内部理由・担当者・安全な
エラーコード・プロバイダー ID・内部返金 ID は公開しない。
C2 の停止申請中・厨房確認済みの段階は客に表示せず、最終確定した調整だけを
反映する。返金未解決時の非終端・定期取得の規則は維持する。更新失敗時は前回
確認した有効な注文と返金状態を保ち、古い情報である旨を表示する。
実 PostgreSQL 試験と日中両言語の Vitest で、未払い現金 ¥660 → ¥330、元金額の
保持、全取消、返金の処理中・失敗・再試行・成功、C2 の最終確定境界、JSON の
許可キーと更新失敗からの回復を検証する。

### 繁體中文

P-83 B part 2 在 ORDER_READ 與同桌訂單歷史皆顯示目前有效的品項、數量與
含稅總額。金額採用 C1 由服務端重新計稅後儲存的結果，原始訂單快照維持不變。
目前內容為主要資訊，原始總額保留為次要資訊。調整顯示「店家已調整您的訂單」，
廚房確認變更後仍保留此標示。保留選用項目名稱以區分同品項的不同選擇，不公開
內部選項鍵值；整筆取消顯示「此訂單已取消」。

退款只依服務端退款紀錄顯示：REQUESTED／PROCESSING 為「退款處理中」，
FAILED 為「退款確認中，請洽店家」，僅 SUCCEEDED 可顯示「已退款 ¥X」。
不得從已付款或已取消推測退款成功；已退款總額只加總成功紀錄。不得公開員工
理由、操作人員、內部錯誤碼、供應者 ID 或內部退款 ID。C2 停止申請與廚房確認
階段不對客人顯示，最終確定後才以訂單調整呈現。維持未解決退款的非終止與輪詢
規則。更新失敗時保留上次確認的有效內容與退款狀態，並顯示資訊可能過時的提示。
真實 PostgreSQL 與日中雙語 Vitest 涵蓋未付現金 ¥660 → ¥330、保留原始金額、
整筆取消、退款處理中／失敗／重試／成功、C2 最終確定邊界、JSON 欄位白名單及
更新失敗後恢復。

### B part 2 execution receipt

- Base: reviewed integration `ec886ed`; branch `codex/p83-b2-adjustment-display`.
- Supplied `TEST_DATABASE_URL` with `go test -race -count=1 ./...`: PASS,
  all packages, using writable temporary Go build/module caches. The complete
  final run includes the extended paid/unpaid whole-cash cancellation and
  multiple-Stripe-refund summary assertions.
- Selected-option variant regression:
  `go test -race -count=1 ./internal/httpapi -run '^TestPostgresP83B2GuestEffectiveLineOptionNames$'`:
  PASS, including two configured lines, partial reduction and immutable names
  after live catalog renaming. HTTP coverage also checks an exact nested
  `line_total_jpy` above the JavaScript safe integer range.
- `make fmt-check vet build migration-smoke`: PASS.
- Node **24.15.0**, pnpm **12.11.2**:
  `pnpm --dir web install --frozen-lockfile`, `pnpm --dir web lint`,
  `pnpm --dir web exec tsc -b --pretty false`, `pnpm --dir web test`, and
  `pnpm --dir web build`: PASS. Vitest: **238 tests across 12 files**.
- `.pnpm-store/` is absent. No push, PR, deployment or payment-provider
  configuration change. These are automated implementation checks; they do
  not claim a new end-to-end browser walkthrough or device acceptance.
