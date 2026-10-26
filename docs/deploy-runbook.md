# First deployment / 初回デプロイ / 首次部署 — Owner runbook

P-84 C, base `90addf6`, 2026-10-19 JST; composition update 2026-10-26 JST.
This runbook does not deploy or approve a pilot. `cmd/server` now composes the
guest API, SPA and opt-in Stripe test worker; see the [runtime guide](ops/containers.md).
Staff APIs stay denied pending the MOD-08 Keycloak adapter. Production hosting,
DB policy, identity integration and live payments still need Owner acceptance.

初回公開は Owner 判断。顧客API・SPA・明示した Stripe test worker の構成と
migration 起動手段は [runtime guide](ops/containers.md) を参照。スタッフAPIは
MOD-08 Keycloak 未統合のため拒否。本番 hosting・DB policy・IdP・live 決済の
承認やデプロイ・試験営業の許可は含まない。

本手冊不部署、不核准試營運。客人API、SPA、明確啟用的 Stripe test worker 與
migration 入口見 [runtime guide](ops/containers.md)。員工API因 MOD-08 Keycloak
尚未整合而拒絕；正式主機、DB政策、IdP與真實付款仍待 Owner 驗收。

Sources / 根拠 / 依據: [README](../README.md), [P-80 scope](p80-test-scope.md),
[Stripe adapter](p80-stripe-provider.md), [P-81 scope](p81-test-scope.md),
[P-82 scope](p82-test-scope.md), [P-83 scope](p83-test-scope.md),
[P-84 matrix](p84-acceptance-matrix.md), [pilot operations checklist](ops/pilot-ops-checklist.md).
Scope documents contain historical lane receipts; use the current code and matrix
for the integrated boundary.

## Owner gates / Owner 確認 / Owner 關卡

Record a named owner, decision and acceptance receipt for each item before the
first deployment. Local synthetic tests cannot close external gates.
初回デプロイ前に各担当者・決定・受入証跡を記録する。合成試験で外部 gate は閉じない。
首次部署前記錄每項負責人、決策及驗收證據；合成測試不能關閉外部 gate。

| Gate | English checklist | 日本語 | 繁體中文 |
| --- | --- | --- | --- |
| EXT-1 | Payments/merchant owner approves Stripe Connect charge mode, tenant/store→account mapping, test credentials/endpoint ownership and real two-merchant Sandbox async paid/failed/expiry/reconcile/refund evidence. Current adapter accepts only its explicit fixture mode and `sk_test_` keys; this is not live activation. | 決済責任者が Connect 方式・店舗/account 対応・試験鍵と endpoint 管理・実 Sandbox 2店舗の非同期決済／失敗／期限切れ／照合／返金を受入。現 adapter は fixture mode と `sk_test_` のみで本番決済未承認。 | 付款負責人核准 Connect 模式、店別帳戶、測試金鑰及 endpoint 管理，驗證真實 Sandbox 雙商家非同步付款／失敗／到期／核對／退款；目前只接受 fixture mode 與 `sk_test_`，不代表正式啟用。 |
| EXT-3 | Identity/hosting/security/recovery owners approve real MOD-08 current membership + MFA, runtime composition, HTTPS/cookies/origin/ingress, direct `/order` SPA entry, Secret Manager/rotation, monitoring, backup retention and restore access. Approve RPO/RTO and execute a timed restore rehearsal with integrity checks. | 実 MOD-08 の現在所属・MFA、本番構成、HTTPS/cookie/origin/ingress、`/order` 直接表示、Secret Manager・鍵更新、監視、backup 保存と復元権限を決定。RPO/RTO と実復元・整合性確認を受入。 | 核准真實 MOD-08 當前權限與 MFA、正式組裝、HTTPS/cookie/origin/入口、`/order` 直入、Secret Manager／輪替、監控、備份保留及還原權限；核准 RPO/RTO 並實測還原時間與完整性。 |
| EXT-4 | Product Owner approves session, TABLE_JOIN, PIN, checkout-proof/continuation and ORDER_READ TTLs and retention/sharing policy. Owner decision 2026-10-26 adopts the MVP pilot TTLs (session 4h, TABLE_JOIN 1h, PIN 15m with 3 attempts and a 1m lockout, checkout proof and payment continuation 30m, ORDER_READ 24h per D5 2026-10-19); retention/sharing policy remains open. Only the ORDER_READ lifetime accepts a 1 minute–7 days range; the session/grant/PIN options and the proof/continuation TTLs are validated as positive durations only. | session・TABLE_JOIN・PIN・Checkout 継続・ORDER_READ の TTL は 2026-10-26 の Owner 決定で承認（session 4時間、TABLE_JOIN 1時間、PIN 15分・3回・1分ロック、Checkout 30分、ORDER_READ 24時間＝D5）。保存・共有方針は未決。1分〜7日の範囲が効くのは ORDER_READ のみで、他の TTL は正の値であることだけを検証する。 | 核准 session、TABLE_JOIN、PIN、Checkout 憑證／延續、ORDER_READ 的 TTL 與保留／分享政策；Owner 於 2026-10-26 核定 MVP 試店數值（session 4 小時、TABLE_JOIN 1 小時、PIN 15 分鐘／3 次／鎖定 1 分鐘、Checkout 憑證與付款延續 30 分鐘、ORDER_READ 24 小時依 D5）；保留／分享政策仍待決定；1 分鐘至 7 天的範圍只適用 ORDER_READ，其餘 TTL 只檢查為正值。 |
| P-81 C | Product Owner explicitly accepts the MVP omission/deferment or approves the MOD-01 ingredient/recipe identity contract and delivery scope. Catalog/hold tests do not implement ingredient mapping. | MVP での延期・除外か MOD-01 食材／レシピ identity 契約と実装範囲を明示。catalog/hold 試験は食材対応の実装ではない。 | 明確接受 MVP 延後／排除，或核准 MOD-01 食材／食譜 identity 契約與交付範圍；菜單／停售測試不等於食材映射。 |
| Shared tablet: My Orders | Decided by D6 (2026-10-19): the MVP supports guests' own phones only; shared-tablet clearing is post-MVP. Do not deploy shared store tablets for guest ordering in the pilot. Closing a table does not revoke independent ORDER_READ links. | D6（2026-10-19）で決定：MVP は客自身のスマホのみ、共用タブレットの消去機能は Post-MVP。試店で客用共用タブレットを置かない。閉卓だけでは ORDER_READ は失効しない。 | 已由 D6（2026-10-19）決定：MVP 只支援顧客自己的手機，共用平板清除列 Post-MVP；試店不放置供顧客點餐的共用平板。關桌不會撤銷獨立 ORDER_READ。 |
| Fixed QR, 3c | Decided by D7 (re-confirmed 2026-10-19): STAFF_OPEN with one fixed QR per table. While a table is open, anyone with the QR (including a photo) can join, and PAY_LATER cash orders go to the kitchen without staff acceptance. Configure stores as STAFF_OPEN; join/PIN rate limits follow 8h. | D7（2026-10-19 再確認）で決定：STAFF_OPEN＋卓ごとの固定 QR。開卓中は QR（写真含む）を持つ人なら参加でき、後払い現金はスタッフ受付なしで厨房へ。店舗は STAFF_OPEN に設定、join/PIN のレート制限は 8h に従う。 | 已由 D7（2026-10-19 重新確認）決定：STAFF_OPEN＋每桌固定 QR。開桌期間持 QR（含照片）者都能加入，後付現金單不經店員接單直接進廚房。店家設定為 STAFF_OPEN，加入／PIN 速率限制依 8h。 |
| Rate policy, 8h | API/security owner approves payment/staff/API thresholds, bursts and ingress enforcement with deployed abuse tests. Existing PIN/join limits do not qualify the perimeter. | 決済・店員・API の閾値、burst、入口の制限と配備後の負荷／不正試験を承認。PIN/join 制限だけでは不足。 | 核准付款／員工／API 門檻、突發流量與入口限制，部署後驗證濫用防護；PIN／加入限制不足以代表完整周界。 |

Also retain 5h lost-cookie staff verification, 7f accessibility, 7g mobile/device
and human cash/KDS/close/refund acceptance in the matrix. Log evidence (8j) is
local and finite; evaluate deployed proxy/APM/collector logging separately.
5h 本人確認、7f a11y、7g 実機、現金・厨房・閉卓・返金の運用受入も必要。
8j は有限のローカル証拠で、配備先 proxy/APM/collector のログは別途確認する。
另保留 5h 憑證遺失驗證、7f 無障礙、7g 手機實機及現金／廚房／關桌／退款人工驗收；
8j 僅涵蓋有限本地路徑，部署端 proxy/APM/collector 日誌須另驗。

## EXT-4 pilot TTLs / EXT-4 試店 TTL / EXT-4 試店有效期限

Owner decision 2026-10-26 adopts these MVP pilot values. Retention and sharing
policy stays open; this section deliberately records no retention periods.
Pinned by `internal/payments/ext4_ttl_integration_test.go`, which needs a
disposable PostgreSQL via `TEST_DATABASE_URL` (`sh scripts/test-postgres.sh`
provides one, then runs the whole `make ci`).

| Setting | Adopted | What it governs | When it expires |
| --- | --- | --- | --- |
| `tablesession.Options.SessionTTL` | 4h | Physical seating session, fixed at `opened_at` (absolute, not sliding). Gates guest join, cart/order commit, payment continuation and asynchronous Checkout creation. | Guests cannot join or order; staff can still settle cash, approve reviews, advance fulfillment and close the table, then open a new session. |
| `tablesession.Options.GrantTTL` | 1h | TABLE_JOIN grants issued by each join, capped at the session's remaining lifetime. | That browser token stops reading/ordering; a new join issues a new grant (rate-limited by `GrantWindow`). Table close revokes all join tokens; ORDER_READ is independent. |
| `tablesession.Options.PINTTL` (3 attempts, 1m lockout) | 15m | Only new SESSION_PIN joins. Existing grants keep working while the PIN is live. | PIN joins return `ErrInvalidPIN` until staff reset the PIN. `ResetPIN` also bumps the grant generation and so revokes every TABLE_JOIN grant on that table: every guest must join again with the new PIN. |
| `checkoutproof.Config.TTL` | 30m | The browser checkout context bound to one Stripe order commit/continuation. | The proof cannot commit a new Stripe order or resume a Checkout. |
| `payments.Config.ContinuationTTL` | 30m (capped by the proof expiry) | Only re-displaying the private Stripe Checkout URL (`CanResume`/`Resume`). | Resume is denied, but a payment completed on the Stripe page already open still settles by webhook — exactly once. If the session is no longer open it lands `PAID` + manager review. |
| ORDER_READ (D5, 2026-10-19) | 24h | Per-order guest status link issued at commit (or requested while a join grant is valid). | The link stops working; it cannot be re-issued after table close. It intentionally survives session expiry and close. |

Staff recovery when a long meal outlives the session: ask the guest to finish
the Stripe page already open (the payment still settles; a late match flags
review), approve the review, settle any cash, serve the orders, close the
expired session (allowed), then open a new session for the next party. The
earlier guests keep their 24h ORDER_READ links either way. At a SESSION_PIN
table an expired PIN blocks new joins only, and the fix is `ResetPIN` — note
that this revokes the join tokens of guests still seated, who then need the new
PIN to keep ordering.

If an order committed seconds before session expiry reaches its first
asynchronous CREATE only after expiry, it ends FAILED / SESSION_UNAVAILABLE
without a provider Checkout. A store manager with the existing void permission
can use the same unpaid Stripe void action (current version, reason and
idempotency key), then close the table. Recovery atomically records the normal
void audit and cancellation, revokes continuation and permits Close; it does not
record cash or invent a provider expiry. It requires the unique CREATE work to
be DONE after exactly one lease, with SESSION_UNAVAILABLE and its failure event,
and no provider bindings or payment observations. The work is locked and checked
again during void. Leased, reclaimed or ambiguous work is refused: a missing
Checkout ID alone does not prove that no provider request is still in flight.
Wait for worker/webhook evidence in those cases, reconcile once provider IDs
are bound, and escalate if they remain unknown; do not manually retry Checkout
creation. `TestEXT4GapOrderCommittedBeforeExpiryCreateAfter`
asserts recovery and Close, alongside an in-flight lease-reclaim refusal test.

日本語：Owner 決定（2026-10-26）で MVP 試店の TTL を承認。保存・共有方針は未決のため
本節に保存期間は書かない。session 4時間（開卓時刻から固定）、TABLE_JOIN 1時間（session 残り
時間が上限）、PIN 15分（試行3回・1分ロック、新規 PIN 参加のみ。期限切れはスタッフの
ResetPIN で復旧し、その際は既存の TABLE_JOIN も失効するため再参加が必要）、Checkout 証明 30分、
支払継続 30分（Stripe の URL 再表示のみ。決済ページ上で完了した支払いは webhook で一度だけ
記録され、session 終了後なら PAID＋店長確認になる）、ORDER_READ 24時間（閉卓・session 終了
後も有効）。1分〜7日の範囲は ORDER_READ のみ。長い会食が session を超えたら、開いている
Stripe ページで支払い→確認承認→現金精算→提供→期限切れ session を閉卓→新しい session を開く。
session 期限直前に確定した注文の初回 Checkout 作成が期限後に走り、provider 呼出し前に
FAILED／SESSION_UNAVAILABLE で終了した場合は、既存の取消権限を持つ店長が未払い Stripe
取消（現在の version・理由・冪等キーが必要）で復旧し、閉卓できる。通常の取消監査と注文取消、
支払継続の失効を同一トランザクションで記録し、現金精算や provider の失効記録は作らない。
CREATE work が初回 lease のみで DONE、失敗理由とイベントが一致し、provider ID・決済観測が
空であることをロック下で再確認する。lease 中・再取得済み・結果不明は拒否するため、先行する
provider 呼出しが残る注文を誤って取消しない。worker／webhook の証拠を待ち、provider ID
確定後に照合し、不明のままなら上位対応を依頼する。Checkout 作成は手動再試行しない。
EXT-4 テストで復旧・閉卓と lease 再取得時の拒否を検証する。

繁中：Owner 於 2026-10-26 核定 MVP 試店 TTL；保留／分享政策仍待決定，本節不寫保留期間。
session 4 小時（自開桌起固定）、TABLE_JOIN 1 小時（以 session 剩餘時間為上限）、PIN 15 分鐘
（3 次、鎖定 1 分鐘，只管新的 PIN 加入；PIN 過期須由店員 ResetPIN 復原，該動作同時撤銷既有
TABLE_JOIN，客人須重新加入）、Checkout 憑證 30 分鐘、付款延續 30 分鐘（只影響
重新顯示 Stripe 私有網址；已在 Stripe 頁面完成的付款仍由 webhook 記錄一次，session 結束後
付款記為 PAID＋店長覆核）、ORDER_READ 24 小時（關桌與 session 到期後仍有效）；1 分鐘至 7 天
範圍僅適用 ORDER_READ。長餐超過 session 時：請客人完成已開的 Stripe 頁面→核准覆核→現金結帳
→出餐→關閉過期 session→開新 session。session 到期前幾秒送出的訂單若首次 Checkout 建立
排在到期後，於呼叫 provider 前以 FAILED／SESSION_UNAVAILABLE 結束，具既有作廢權限的
店長可用原本的未付款 Stripe 作廢操作（目前 version、原因、冪等鍵），再關桌。一般作廢稽核、
訂單取消與付款延續撤銷在同一交易記錄，不記為現金付款，也不虛構 provider 到期結果。
作廢時鎖住並再次確認 CREATE work 只取得過一次 lease、已 DONE、失敗原因與事件一致，且無
provider ID 或付款觀測。仍持有 lease、已重新取得 lease 或結果不明者會被拒絕，避免先前的
provider 請求仍可能完成時誤作廢；這些情況須等待 worker／webhook 證據，provider ID 確定後
再核對，仍不明則上報，不要手動重試建立 Checkout。EXT-4 測試驗證復原與關桌，以及 lease
被重新取得時拒絕作廢。

## How to burst-test ingress (8h) / 入口 burst 試験 / 入口爆量測試

Owner decision D8 sets the pilot ingress limits: above 120 requests/min per
client IP on any `/v1/...` path, and above 10 requests/min per client IP on cart
commit (`POST /v1/guest/sessions/{sessionID}/cart/commit`) and payment creation
(`POST /v1/guest/sessions/{sessionID}/checkout-context`), the ingress returns 429.
Row 8h stays GAP until a deployed ingress passes these checks; the application
itself does not throttle, so a run against a plain local runtime is expected to
FAIL.

`cmd/bursttest` is a deployment-agnostic **count-threshold probe**: it assumes
the ingress counts requests in a fixed or sliding count window. A token bucket
(for example nginx `limit_req` without `burst`) may reject earlier than the count
window suggests, and nginx's default rejection is 503, which the probe does not
count as 429 — configure the ingress to return 429 for this test.

It sends empty `{}` bodies to a synthetic session ID, never sends cookies, auth
headers or secrets (a `-base-url` with userinfo, a query or a fragment is
refused), sets `User-Agent: tojima-bursttest/1`, and refuses non-loopback hosts
unless `-allow-remote` is given.

```sh
# Build once: the exit-code contract below holds for the compiled binary,
# not for `go run` (which reports exit status 2 as 1).
go build -o /tmp/bursttest ./cmd/bursttest

# Loopback (e.g. against a local proxy that enforces D8):
/tmp/bursttest -base-url http://127.0.0.1:8080 -profile v1

# Deployed pilot ingress, one profile at a time:
/tmp/bursttest -base-url https://<pilot-ingress> -profile v1 -allow-remote
/tmp/bursttest -base-url https://<pilot-ingress> -profile commit -allow-remote
/tmp/bursttest -base-url https://<pilot-ingress> -profile checkout -allow-remote
```

PASS is an exact boundary check: no transport error, and the first 429 is request
`-expect-limit` (0-based; 120 for `v1`, 10 for `commit`/`checkout`) — requests
before it were allowed and that request was rejected. A first 429 after that
point fails with "limit looser than expected"; an earlier one fails with "limit
tighter than expected". If the last request starts more than `-window` + 10%
(or 1s, whichever is larger) after the burst started, the result is
`INVALID (dispatch overran window ...)` and the run does not count. Exit
0 = PASS, 1 = FAIL/INVALID, 2 = usage error.

Before each profile, wait until this client IP's budget has fully reset: the
counting period plus any mitigation/block time (for example a Cloudflare
mitigation timeout) must have passed. For a fixed window, start right at the
beginning of a minute so the default 10s burst finishes without crossing a
window boundary. `-requests` (default limit + 20), `-window` (default 10s) and
`-concurrency` (default 1; higher values can reorder requests against the
index-based rule) adjust the burst.

A PASS covers only the probed path and profile. It does not prove the ingress
rule matches **all** `/v1/...` paths or that the counter is per client IP rather
than per IP plus path; confirm the deployed rule's path matching and key in the
ingress configuration by hand. Run every profile from the same client IP.

日本語：`cmd/bursttest` は固定／sliding の count window を前提とする count threshold probe。
token bucket（burst なしの nginx `limit_req` 等）は早めに拒否しうるため、429 を返す設定で試験する
（nginx 既定の 503 は 429 と数えない）。PASS は「transport error なし、かつ最初の 429 が
ちょうど `-expect-limit` 件目（0 始まり）」の完全一致。それより後なら "limit looser"、前なら
"limit tighter" で FAIL、最終要求の開始が window＋10% と 1 秒の大きい方を超えたら INVALID。
各 profile の前に IP 予算と mitigation/block 時間の経過を待ち、fixed window は分の先頭から
10 秒で走らせる。PASS は当該 path のみの証拠で、全 `/v1/...` の path 一致と per-IP（per-IP+path
でない）counter は ingress 設定で別途確認する。終了コードは build 済み binary で使う
（`go run` は exit 2 を 1 にする）。アプリ自体は制限しないため、素のローカル実行は FAIL が正常。

繁中：`cmd/bursttest` 是假設固定／滑動 count window 的「計數門檻」探測器；token bucket
（例如未設 burst 的 nginx `limit_req`）可能提早拒絕，nginx 預設的 503 不算 429，需改成回 429。
PASS 是精確邊界：無 transport error，且第一個 429 正好是第 `-expect-limit` 筆（0 起算）；
更晚為 "limit looser"、更早為 "limit tighter"，皆 FAIL；最後一筆開始時間超出 window＋10%
（或 1 秒，取大者）則為 INVALID。每個 profile 前要等 IP 預算與 mitigation/block 時間歸零，
fixed window 從整分鐘開頭起跑，讓預設 10 秒的 burst 不跨界。PASS 只證明該 path，
仍需人工確認 ingress rule 涵蓋全部 `/v1/...` 且 counter 是 per-IP（非 per-IP+path）。
exit code 契約只對編譯後的 binary 成立（`go run` 會把 2 變成 1）。
應用本身不做限流，直接對本地 runtime 跑出 FAIL 屬預期。

## Actual binary configuration / 実際の設定 / 實際設定

No executable loads `.env` automatically. Do not invent `DATABASE_URL`,
`STRIPE_SECRET_KEY` or IdP environment support. These are the application inputs
actually read; Go/toolchain and PostgreSQL client environment are separate.
各実行形式は `.env` を自動読込しない。表以外のDB・Stripe・IdP環境変数による配線は未実装。
各執行檔都不自動載入 `.env`；不要假定已支援其他 DB／Stripe／IdP 環境變數。

| Binary / バイナリ / 執行檔 | Inputs and behavior / 入力・動作 / 輸入與行為 | Code |
| --- | --- | --- |
| `go run ./cmd/server` | Guest API/SPA + opt-in Stripe test worker; required env and explicit health-only mode: [runtime configuration](ops/containers.md). Staff/admin deny pending MOD-08. / 顧客API・SPA、staff拒否。 / 客人API與SPA，員工拒絕。 | [main](../cmd/server/main.go), [config](../internal/config/config.go), [health](../internal/httpapi/health.go) |
| `go run ./cmd/guesttest` | Required `TEST_DATABASE_URL` to disposable **loopback** PostgreSQL. Optional `-web web/dist`. Fixed `127.0.0.1:8081`. Automatically creates/migrates/seeds a random schema; drops owned schema at normal shutdown. / loopback試験DB、専用schemaを作成・終了時削除。 / loopback測試DB，自建並於正常結束時刪除專屬schema。 | [main](../cmd/guesttest/main.go), [fixture](../internal/testguest/fixture.go) |
| `go run ./cmd/operationstest` | Same required `TEST_DATABASE_URL`; required `-credentials NEW_PATH` outside served web root; optional `-web-dist web/dist`, `-port 8082` (1024–65535), `-payments-fixture`, `-catalog-fixture`, `-demo-shironagasukujira`, `-guest-display-name`, `-stripe-sandbox`, `-stripe-tenant A\|B\|C`. Demo cannot combine with payments. Binds `127.0.0.1`; private 0600 credential file and random schema removed at normal shutdown. / 専用の試験IdP、公開領域外の新規credential file必須。 / 專屬測試IdP，必須新建公開目錄外的credential檔。 | [main](../cmd/operationstest/main.go), [payments fixture + worker](../cmd/operationstest/payment_fixture.go) |
| `go run ./cmd/demogateway` | Demo entrance only. Optional `-listen 127.0.0.1:8090`, `-upstream http://127.0.0.1:8082`; required for the public demo `TOJIMA_DEMO_PUBLIC_ORIGIN` and `TOJIMA_DEMO_GATE_SECRET` (at least 32 bytes, no line breaks). Loopback listener only. / デモ入口専用、loopbackのみ。 / 僅供 DEMO 入口，只綁 loopback。 | [main](../cmd/demogateway/main.go), [gateway](../internal/demogateway/gateway.go) |
| `go build ./cmd/bursttest` | Deployment-agnostic ingress burst probe for 8h. Flags `-base-url`, `-profile v1\|commit\|checkout`, `-requests`, `-window`, `-concurrency`, `-expect-limit`, `-allow-remote`; no environment variables, no credentials. / 8h の入口 burst 試験用。 / 8h 入口爆量測試用。 | [main](../cmd/bursttest/main.go) |

`-payments-fixture` creates random signing material and a synthetic merchant;
it reads no Stripe key and makes no Stripe network call. The fixture file holds
test staff credentials and the synthetic webhook secret: never publish it.
`-stripe-sandbox` is the only mode that reads Stripe variables — `TOJIMA_STRIPE_MODE`,
`TOJIMA_STRIPE_API_VERSION`, `TOJIMA_STRIPE_TEST_SECRET_KEY`,
`TOJIMA_STRIPE_TEST_WEBHOOK_SECRET`, `TOJIMA_STRIPE_TEST_ACCOUNT_A/B/C` and
`TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY`, all test-mode only and documented in
[Stripe Sandbox test runtime](p80-stripe-test-runtime.md).
`web` uses optional **build-time** `VITE_API_BASE_URL`, default same origin
([transport](../web/src/shared/http.ts)); it is not a Go runtime variable.

決済fixtureは署名鍵・架空merchantを生成し、Stripe鍵読込・外部通信はしない。
0600ファイルには試験credentialとwebhook secretが含まれるため公開しない。
`VITE_API_BASE_URL` はWebビルド時のみ、既定は同一origin。

付款fixture自行產生簽章材料與合成商家，不讀Stripe金鑰、不呼叫Stripe。
0600檔含測試員工憑證與webhook secret，不得公開。
`VITE_API_BASE_URL` 只在Web建置時讀取，預設同源。

Future production composition must explicitly provide constructor configuration:
[tablesession.Options](../internal/tablesession/types.go),
[ORDER_READ TTL](../internal/ordering/order_read.go),
[payments.Config and resolver](../internal/payments/types.go),
[Stripe Config](../internal/stripeprovider/provider.go),
[checkoutproof.Config](../internal/checkoutproof/proof.go),
[PaymentsHTTPConfig](../internal/httpapi/payments.go). These are Go API fields,
**not supported environment-variable names**. Keep credentials outside the DB;
merchant configuration stores account IDs/references, not secret values.
本番はこれらのGo設定・resolverを明示配線する必要がある。対応する環境変数は未定義。
正式組裝需明確接上這些Go設定與resolver；目前未定義相應環境變數。DB只存商家參照，不存機密。

## Migration plan / Migration 手順 / Migration 程序

1. Owner records the exact application SHA, target database/schema/search_path,
   current ledger, backup location, restore procedure and downtime owner. Take
   and verify a backup **before** migration; snapshot-only existence is not a
   restore receipt. Rehearse the candidate against a disposable restored copy.
2. Stop **all application writers**: guest/staff APIs, payment/refund/expiry
   workers, webhook ingress/draining processes and admin/automation writers.
   **008 must run with all writers stopped. No rolling upgrade across 008.**
   Keep pre-008 binaries stopped afterwards. Its backfill captures already-held
   Checkouts; old concurrent writers cannot maintain that invariant.
   **The same applies to 016: no rolling upgrade across 016.** Every pre-016
   payment worker must have exited before 016 runs and must never be restarted:
   it calls Stripe without setting `dispatched_at`, so a later worker could
   record SESSION_UNAVAILABLE and let staff void an order whose Checkout exists.
3. Run `postgres.Up` once on the explicitly selected schema; it takes advisory
   transaction lock `771904283`, creates the ledger and applies every missing
   version in one transaction. Any failure rolls back the entire pending set,
   including ledger changes. Inspect the cause; remove only an understood
   conflict, then rerun the same candidate. Do not mark failed versions applied.
4. Verify the ledger, objects, backfill/work and integrity; start only the
   compatible accepted runtime, then smoke-check it. Keep a sanitized receipt.

日本語：①SHA・DB/schema/search_path・ledger・backupと復元手順・停止担当を記録し、
**実行前にbackupを取得・検証**。復元した使い捨て環境で候補を予行する。
②API・webhook・決済／返金／期限切れworker・管理処理を含む**全writerを停止**。
**008・016を跨ぐrolling upgrade禁止**。以後も旧バイナリを再開しない（016前の決済workerは `dispatched_at` を記録しない）。
③選択schemaに `postgres.Up` を実行。advisory lockと全未適用版を一つのtransactionで扱う。
失敗時は全未適用版とledgerがrollbackされる。原因を確認・解消して同候補を再実行し、
適用済み扱いに書き換えない。④ledger・object・backfill／work・整合性を確認後、互換の
受入済みruntimeを起動してsmoke、機密を除いた証跡を保存。

繁中：①記錄SHA、DB/schema/search_path、ledger、備份位置／還原程序與停機負責人；
**migration前備份並驗證**，先在還原出的拋棄環境演練。②停止API、webhook、付款／退款／
到期worker及管理自動化等**所有writer**；**禁止跨008、016滾動升級**，之後也不得重啟舊版（016前的付款worker不會記錄 `dispatched_at`）。
③對指定schema呼叫 `postgres.Up`，同一transaction以advisory lock套用全部缺少版本；
失敗會回滾全部未提交版本及ledger。查明衝突並排除後重跑同候選版，不可手動標記已套用。
④核對ledger、物件、backfill／work及完整性後才啟動相容且驗收過的runtime，執行smoke並留去機密紀錄。

The exact ordered set and reverse SQL are in [migrate.go](../migrations/postgres/migrate.go).
`Down` removes **all installed versions**, not one step; it is for controlled
schema/test lifecycle. There is no `cmd/migrate`, `--version`, `--down` or
production migration Make target. Do not use `make migration-smoke` as a
PostgreSQL migration: it is the legacy disposable SQLite check.
`Down` は全適用版を逆順に除去する試験用API。単一版rollback用CLIはない。
`Down` 是反向移除全部已安裝版本的測試生命週期API，沒有單步rollback CLI；SQLite smoke不能代替PostgreSQL migration。

| Version / source (paired `.down.sql`) | Changes / 変更 / 變更 | Rollback notes / 戻しの注意 / 回退注意 |
| --- | --- | --- |
| [001 table_sessions](../migrations/postgres/001_table_sessions.up.sql) | Tenants/stores/tables/sessions/join grants / 店舗・卓・grant / 店別桌次與grant | Drops session/grant/tenant data / 全データ除去 / 刪除相關資料。 |
| [002 menu_catalog](../migrations/postgres/002_menu_catalog.up.sql) | Drafts/sealed publications/store overrides / 菜單版・店舗設定 / 菜單版本與店設定 | Drops menu data and guards / 菜單とguard除去 / 刪除菜單與保護。 |
| [003 tax_rates](../migrations/postgres/003_tax_rates.up.sql) | Tax schedule / 税率日程 / 稅率日程 | Drops tax schedule / 税率表除去 / 刪除稅率表。 |
| [004 shared_cart_orders](../migrations/postgres/004_shared_cart_orders.up.sql) | Shared cart, immutable orders, idempotency / 共有cart・注文 / 共享購物車與訂單 | Drops orders/cart/replay history / 注文履歴消失 / 訂單歷史消失。 |
| [005 operations](../migrations/postgres/005_operations.up.sql) | Cash, KDS, outbox, operations audit / 現金・厨房・監査 / 現金廚房稽核 | Drops operations and settlement history / 決済・運用履歴消失 / 營運與收款歷史消失。 |
| [006 payments](../migrations/postgres/006_payments.up.sql) | Merchants/attempts/webhook inbox/work / 決済試行 / 付款嘗試 | Drops payment/audit history and review flag; restores older guards/event constraint, which can reject newer events / 新eventがあると制約拒否し得る / 新事件可能不符舊限制；付款歷史會消失。 |
| [007 company_product_holds](../migrations/postgres/007_company_product_holds.up.sql) | HQ hold and replay audit / 本部hold / 總部停售 | Drops hold state and audit replay columns / hold・replay消失 / 刪除停售與重播資料。 |
| [008 held_checkout_expiry](../migrations/postgres/008_held_checkout_expiry.up.sql) | Retained expiry marker/guard/index + existing-hold backfill / 保持期限切れ要求 / 保留到期要求 | Drops marker/guard/index; does **not** undo backfilled continuation revocation, queued work or audit events. Old binaries cannot maintain retained obligations / backfillは逆変換しない / 不逆轉已撤銷的延續、work或事件。 |
| [009 order_read_grants](../migrations/postgres/009_order_read_grants.up.sql) | Independent read capabilities / 独立閲覧grant / 獨立查閱grant | Drops every read link's authority / 全閲覧link失効 / 全部查閱連結失效。 |
| [010 manager_review](../migrations/postgres/010_manager_review.up.sql) | Manager decision/audit/replay / 管理者判断 / 經理決策 | Restores pre-review guards; old action/event CHECKs refuse REVIEW_APPROVE / MANAGER_REVIEW_APPROVED history. Drops decision/reason/snapshot columns if allowed / 履歴で拒否 / 有該歷史即拒絕。 |
| [011 item_adjustments_refunds](../migrations/postgres/011_item_adjustments_refunds.up.sql) | Effective lines/tax, refunds/ledger/work / 調整・返金 / 調整退款 | Explicitly refuses when **any** `order_adjustments` or `operation_refunds` row exists, regardless of final status; restored CHECKs may also refuse newer history / 履歴破棄を拒否 / 有任何調整或退款即拒絕抹除。 |
| [012 stop_requests](../migrations/postgres/012_stop_requests.up.sql) | Kitchen-confirmed stop requests and close guard / 停止申請 / 停止申請 | Explicitly refuses when **any** `cancellation_requests` row exists (also finalized/rejected/withdrawn); restores C1 guard/action/event constraints / 全状態の履歴で拒否 / 任何狀態歷史皆拒絕。 |
| [013 stop_request_state_lock](../migrations/postgres/013_stop_request_state_lock.up.sql) | Locks operation state before accepting direct stop inserts; upgrades existing 012 databases / 停止申請の行ロック / 停止申請列鎖 | Restores the 012 request guard and reopens the direct-insert/fulfillment race; no history is removed / 競合が再発 / 重新引入競態。 |
| [014 stripe_unpaid_void](../migrations/postgres/014_stripe_unpaid_void.up.sql) | D9 manager void: `VOIDED` payment status, `STRIPE_VOID` action/idempotency and late-payment refund events / 未払いStripe注文の取消 / 未付款 Stripe 訂單作廢 | Explicitly refuses while any `VOIDED` state, `STRIPE_UNPAID_VOIDED`/`STRIPE_LATE_PAYMENT_REFUND` event or `STRIPE_VOID` action exists; restores pre-D9 constraints / void履歴があると拒否 / 有作廢歷史即拒絕。 |
| [015 store_guest_display_name](../migrations/postgres/015_store_guest_display_name.up.sql) | Optional public `stores.guest_display_name` (NULL omits the guest topbar name) / 店舗表示名 / 店家顯示名稱 | Drops the column; export non-NULL values first. Migration 015 must be deployed before the app; see [guest store display name](ops/store-guest-display-name.md) / 値の先出しが必要 / 須先匯出非空值。 |
| [016 payment_work_dispatch_fence](../migrations/postgres/016_payment_work_dispatch_fence.up.sql) | `stripe_payment_work.dispatched_at` fence; conservative backfill marks claimed CREATE work dispatched / 決済送信fence / 付款送出fence | Drops the column; pre-016 binaries must stay stopped (no rolling upgrade across 016) / 016前のworkerを再開しない / 不得重啟016前的worker |

Read the paired downs before any rollback. A failed `Down` rolls back the entire
transaction. Do not delete audit/history rows or disable guards to force it.
For data-bearing deployment failure, keep writers stopped and have the recovery
owner choose a forward repair or approved restore/reconciliation; DB restore
cannot undo external Stripe payments/refunds. Empty-schema round trips are not
permission to rewind a live ledger.
各down SQLを読む。失敗した `Down` は全体rollback。監査履歴削除やguard無効化で強行しない。
実データ障害は停止を維持し担当者が前進修正か承認済み復元・照合を選ぶ。DB復元で外部決済は戻らない。
執行前閱讀配對down；`Down`失敗會全體回滾，不得刪歷史或停用保護強制通過。
有資料的事故需保持停寫，由復原負責人選擇向前修復或核准的還原／核對；DB還原不會逆轉Stripe交易。

## Exact local API invocation / ローカル起動例 / 本地API執行例

**Disposable-schema rehearsal only; not a production command or backup receipt.**
Run from the repository root with Go from `go.mod` and PostgreSQL 17 (or compatible newer) client tools
on PATH. This temporary helper calls the real exported runner; it adds no
production hook/configuration. Its `-dsn`/`-schema` flags belong to this example,
not to an existing repository binary. Use a fresh local database for restore;
`pg_restore` does not remap schema names.

**使い捨てschemaでの予行のみ、本番コマンド・実backup証跡ではない。** repo rootで実行。
例示helperのflagは製品CLIではない。復元先は別の新規ローカルDB、schema名は変更されない。
**僅供拋棄schema演練，不是正式命令或備份驗收。** 在repo root執行；helper旗標不是現有產品CLI。
還原需另一個新建本地DB，`pg_restore`不會改寫schema名稱。

```sh
set -eu # stop on any failed step; run these rehearsal blocks in the same shell
# REHEARSAL: dedicated loopback database only. Never point this at business data.
rehearsal_dir=$(mktemp -d "${TMPDIR:-/tmp}/tojima-migration.XXXXXX")
cat > "$rehearsal_dir/main.go" <<'GO'
package main
import (
    "context"
    "flag"
    "fmt"
    "os"
    "time"
    "github.com/jackc/pgx/v5"
    "github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)
func main() {
    dsn := flag.String("dsn", "", "disposable loopback database URL")
    schema := flag.String("schema", "", "existing disposable schema")
    flag.Parse()
    cfg, err := pgx.ParseConfig(*dsn)
    if err != nil || *dsn == "" || *schema == "" { panic("explicit test DSN/schema required") }
    if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" { panic("loopback only") }
    cfg.RuntimeParams["search_path"] = pgx.Identifier{*schema}.Sanitize()
    ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
    defer cancel()
    conn, err := pgx.ConnectConfig(ctx, cfg)
    if err == nil { defer conn.Close(context.Background()); err = postgres.Up(ctx, conn) }
    if err != nil { fmt.Fprintln(os.Stderr, "migration rehearsal failed; inspect locally"); os.Exit(1) }
    fmt.Println("migration rehearsal Up succeeded")
}
GO
rehearsal_url='postgres://postgres@127.0.0.1:55439/tojima_test?sslmode=disable'
psql "$rehearsal_url" -X -v ON_ERROR_STOP=1 -c 'CREATE SCHEMA p84c_rehearsal'
go run "$rehearsal_dir/main.go" -dsn "$rehearsal_url" -schema p84c_rehearsal
```

Verification queries (select the intended schema explicitly):
検証SQL（対象schemaを明示）：驗證SQL（明確指定schema）：

```sh
psql "$rehearsal_url" -X -v ON_ERROR_STOP=1 <<'SQL'
SET search_path TO p84c_rehearsal;
SELECT current_database(), current_schema();
SELECT version, applied_at FROM schema_migrations ORDER BY version;
-- Exactly versions 1..16, not merely max(version)=16.
SELECT array_agg(version ORDER BY version)=ARRAY[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16] AS ledger_complete
FROM schema_migrations;
SELECT to_regclass('order_read_grants'), to_regclass('order_adjustments'),
       to_regclass('operation_refunds'), to_regclass('cancellation_requests'),
       to_regclass('stripe_payment_attempt_hold_candidates_idx');
SELECT to_regprocedure('protect_company_hold_expiry_request()'),
       to_regprocedure('protect_table_session_pending_stop_requests()');
SELECT count(*) AS retained_expiry_requests FROM stripe_payment_attempts
WHERE company_hold_expire_requested_at IS NOT NULL;
SELECT status, work_type, count(*) FROM stripe_payment_work GROUP BY status, work_type;
SELECT count(*) AS unvalidated_constraints FROM pg_constraint
WHERE connamespace=current_schema()::regnamespace AND NOT convalidated;
SQL
```

Empty counts are expected on the fresh rehearsal schema; for a restored copy,
compare retained requests/work/history with the pre-migration backup and 008's
eligibility SQL, and review outstanding READY/LEASED work. No query alone proves
provider outcomes, backup integrity or commercial readiness.
新規schemaでは0件が正常。復元コピーはbackup・008対象SQLと照合し、未処理workを確認する。
新schema零筆屬正常；還原副本須對照備份及008篩選SQL，檢視待處理work。SQL結果不等於外部付款或正式就緒驗收。

## Backup/restore rehearsal / 復元予行 / 備份還原演練

**Local disposable example only.** These commands create/drop only the named
rehearsal schema and a fresh restore database. Choose unused names; do not use
`--clean`, reuse a business DB or drop an unknown schema. The production recovery
owner must separately approve encrypted/off-site retention, roles/extensions,
PITR if required, RPO/RTO, full data checks and external payment reconciliation.

**ローカル使い捨て例のみ。** 未使用名を選び、本番DBへの流用禁止。実運用では暗号化・別拠点保存、
role/extension、必要なPITR、RPO/RTO、データ検証と外部決済照合を別途受入する。
**僅限本地拋棄環境。** 選用未使用名稱，不得套用業務DB。正式復原需另核准加密異地備份、
role/extension、必要PITR、RPO/RTO、資料完整性及外部付款核對。

```sh
# Same disposable schema created above. Insert a probe BEFORE taking its backup.
psql "$rehearsal_url" -X -v ON_ERROR_STOP=1 -c \
  "CREATE TABLE p84c_rehearsal.restore_probe AS SELECT 'p84c-disposable-only'::text AS marker"
pg_dump --dbname="$rehearsal_url" --format=custom --schema=p84c_rehearsal \
  --no-owner --no-privileges --file="$rehearsal_dir/schema.dump"
pg_restore --list "$rehearsal_dir/schema.dump" > "$rehearsal_dir/archive-list.txt"
createdb --host=127.0.0.1 --port=55439 --username=postgres p84c_restore_rehearsal
restore_url='postgres://postgres@127.0.0.1:55439/p84c_restore_rehearsal?sslmode=disable'
# Functions/triggers use unqualified names; match the original schema search_path.
PGOPTIONS='-c search_path=p84c_rehearsal' pg_restore --dbname="$restore_url" \
  --exit-on-error --single-transaction --no-owner --no-privileges "$rehearsal_dir/schema.dump"
psql "$restore_url" -X -v ON_ERROR_STOP=1 <<'SQL'
SET search_path TO p84c_rehearsal;
SELECT * FROM restore_probe; -- p84c-disposable-only, exactly one row
SELECT array_agg(version ORDER BY version) FROM schema_migrations; -- 1..16
SELECT to_regclass('cancellation_requests');
SQL
# Confirm Up is idempotent on the restored schema.
go run "$rehearsal_dir/main.go" -dsn "$restore_url" -schema p84c_rehearsal
# Only after verification, clean up resources created by this example.
dropdb --host=127.0.0.1 --port=55439 --username=postgres p84c_restore_rehearsal
psql "$rehearsal_url" -X -v ON_ERROR_STOP=1 -c 'DROP SCHEMA p84c_rehearsal CASCADE'
rm -r "$rehearsal_dir"
```

### Scripted restore drill (8n, local) / スクリプト復元訓練 / 腳本化還原演練

`scripts/restore-drill.sh` repeats the rehearsal automatically with two
disposable `postgres:17` containers (`--memory 1g --cpus 1`, removed on exit).
It applies all migrations to the source through the repository's own
`migrations/postgres.Up`, seeds a small synthetic tenant/store/table/session/
cart/menu/order set (marked as synthetic), takes `pg_dump -Fc`, `pg_restore`s it
into a fresh container, then compares the schema-only dumps, the migration-ledger
rows, the `pg_sequences` state and, for every `public` table, the row count plus
an order-independent content checksum. It prints dump/restore/verify/total
timings, dump size, table and sequence counts and `RESULT: PASS` (exit 0) or
`RESULT: FAIL <reason>` (exit 1).

```sh
lockf -k /tmp/tojima-local-test.lock sh scripts/restore-drill.sh
```

What it proves: the repository's migrations produce a PostgreSQL database that
dumps and restores losslessly in the same image, and the drill is repeatable.
It compares only the `public` schema and its sequences: roles and privileges are
deliberately excluded (`pg_restore --no-owner --no-privileges`), so a difference
in ownership or grants would not be detected.
What it does **not** prove: no hosting backup, retention, PITR/WAL archiving,
RPO/RTO, encryption, off-site copy, access control or provider/payment
reconciliation. Row 8n stays GAP until the Owner approves RPO/RTO and a real
hosting backup/restore is executed.

日本語：`scripts/restore-drill.sh` は使い捨て `postgres:17` コンテナ2つで
migrate→seed→`pg_dump`→`pg_restore`→schema／ledger／sequence／全 `public` テーブルの
checksum 比較を自動実行し、各時間と PASS/FAIL を出す。`public` と sequence のみで
role・権限は比較しない。ローカルでの可逆性と再現性だけを証明し、配備先の
backup・保持・PITR・RPO/RTO・暗号化・アクセス制御は含まない（8n は GAP のまま）。

繁中：`scripts/restore-drill.sh` 用兩個拋棄式 `postgres:17` 容器自動執行 migrate→seed→
`pg_dump`→`pg_restore`→schema／ledger／sequence／所有 `public` 資料表 checksum 比對，並印出
各段耗時與 PASS/FAIL。只比對 `public` 與 sequence，不含角色與權限。只證明本地可逆性與
可重複性，不含託管備份、保留政策、PITR、RPO/RTO、加密、
異地副本、存取控制；8n 仍為 GAP。

Local automated failure recovery is independently repeatable:
ローカル障害注入試験：本地注入失敗測試：

```sh
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55439/tojima_test?sslmode=disable' \
  go test -race -count=1 ./migrations/postgres -run '^TestP84CMigrationFailureAtomicRecovery$' -v
```

This induces a duplicate-function failure mid-008 after column/index changes,
checks exact prior ledger timestamps and schema objects/definitions (001–006),
checks 007 and partial 008 rollback, removes the conflict, reruns Up through 015
and verifies Down/Up. It does **not** establish production backup/restore or
RPO/RTO: matrix **8n remains partially GAP**, owned by database/hosting recovery.
008途中で失敗させ、既存001–006・ledger不変と007/008 rollback、原因除去後の015までの
再実行・Down/Upを確認。実backup/restore・RPO/RTOは未受入、**8nは一部GAP**。
在008中途注入失敗，檢查001–006與ledger不變、007／部分008回滾、排除衝突後重跑至015及
Down/Up；未驗收正式backup/restore或RPO/RTO，**8n維持部分GAP**。

## Smoke after an authorized deployment / 配備後確認 / 核准部署後smoke

- Check the selected runtime's `GET /healthz` (`curl --fail http://127.0.0.1:8080/healthz`
  for the local health binary), expecting `{"status":"ok"}`. Health proves HTTP
  serving only; verify DB/IdP/provider separately. / healthはHTTP生存のみ。
  / health只證明HTTP服務正常。
- On disposable local data, build with the pinned pnpm (`make web-check`) and
  run the existing guest runtime or operations runtime below. Guest join/menu,
  two-client cart, preview/commit, independent `/order` link, close/reopen
  privacy; cashier reception/settlement, kitchen ACCEPTED→PREPARING→READY→SERVED,
  manager review/adjustment/refund and final explicit close follow
  [P-79](p79-test-scope.md), [P-80](p80-test-scope.md), [P-81](p81-test-scope.md),
  [P-82](p82-test-scope.md), [P-83](p83-test-scope.md) and the matrix.
- 日本語：使い捨てDBで客の入店・菜單・2端末cart・注文・独立閲覧link・閉卓後privacy、
  店員受付・現金・厨房各段階・管理者確認／調整／返金・明示閉卓を既存runtimeで確認。
  READYはSERVEDではない。fixtureを本番に接続しない。
- 繁中：於拋棄DB用現有runtime檢查加入／菜單／雙客戶端cart／下單／獨立查閱／關桌隱私、
  員工接單／收款／廚房各階段／經理審核調整退款／明確關桌；READY不等於SERVED，不得把fixture接正式DB。

```sh
# Separate foreground terminals; disposable local database only.
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55439/tojima_test?sslmode=disable' \
  go run ./cmd/guesttest -web web/dist
# http://127.0.0.1:8081/?table=test-table
credentials_dir=$(mktemp -d "${TMPDIR:-/tmp}/tojima-credentials.XXXXXX")
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55439/tojima_test?sslmode=disable' \
  go run ./cmd/operationstest -catalog-fixture -payments-fixture \
    -credentials "$credentials_dir/tojima-p84c-staff.json" -web-dist web/dist -port 8082
# /?table=test-table, /staff, /kitchen, /catalog on http://127.0.0.1:8082
```

The credentials path must be new and outside the served web root; ensure the
temporary directory is outside that root. Enter fixture credentials locally from
the owner-only 0600 file; do not print/upload them. These two commands create different random
schemas; clients in a shared journey must use the same runtime. `.invalid`
Checkout handoff cannot accept payment and has no UI to fabricate success.
Signed provider outcome/refund transitions are covered by Go acceptance tests;
real Sandbox smoke remains EXT-1. On the eventual accepted deployed runtime,
repeat the applicable journeys with Owner-authorized test identities and data,
including denied staff access, HTTPS proof/fragment handling, direct `/order`
entry and proxy/collector log checks; do not reuse fixture identities.

credential pathは公開web root外の未使用パスが必須。一時ディレクトリも公開領域外であることを確認。
所有者のみ読み書き可能な0600ファイルをローカルで利用し公開しない。
2runtimeは別schemaなので同じjourneyは同runtimeを使用。架空Checkoutは決済不能、成功捏造UIなし。
将来の本番構成ではOwner承認の試験identity/dataで同じ確認、拒否・HTTPS・直接link・配備ログも検証する。

憑證路徑必須新建且位於公開web root之外；確認暫存目錄亦在公開範圍外。
僅擁有者可讀寫的0600檔僅於本地使用，不公開。兩個runtime各建不同schema，同一流程用同runtime。
合成Checkout不能付款也沒有偽造成功的UI。未來正式runtime需以Owner授權測試身分／資料重跑適用流程，
包含拒絕存取、HTTPS憑證／fragment、`/order`直入及入口／收集端日誌；不可沿用fixture身分。
