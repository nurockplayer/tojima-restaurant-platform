# P-82 A

Lane A (MOD-05 S7) adds best-effort expiration of existing unpaid Stripe
Checkouts when HQ activates a company product emergency hold. The same hold
transaction selects same-tenant LINKED stores and immutable order quote lines,
revokes original-browser continuation, retains an attempt marker, and writes
EXPIRE work plus an attempt audit event to the existing payment tables. CASH,
DETACHED stores, other tenants, unrelated products and already-paid attempts
are excluded. Attempts not provably closed or paid are included, including
completed-but-unpaid and FAILED attempts without a bound Checkout: a decline
does not close the Checkout. The scan and migration 008's partial index both
exclude EXPIRED, PAID, REVIEW_REQUIRED, checkout_status='expired', and
provider_payment_status='paid'.
No network call occurs inside that transaction.

The existing worker retrieves before expiring an open/unpaid session and again
after an ambiguous expire response. An open result retries; expired becomes
EXPIRED; complete/unpaid remains PROCESSING and is retrieved again; verified
payment remains PAID operationally and requires payment review, blocking KDS.
Unordered and duplicate webhooks cannot replace PAID with EXPIRED or duplicate
the payment/review effects. All paid writers lock company catalog SHARE,
session, attempt and operation in that order, matching HQ's parent UPDATE.

CREATE_PENDING and in-flight creations retain expiry work while their session
ID is unknown. Same-key CREATE recovery retains the existing immutable retry
window; it never issues a fresh Checkout key. Once bound, the existing session
is expired/reconciled and its URL remains unavailable. Hold OFF neither removes
pending work nor clears the retained marker or revocation. HQ expiry retrieval
and retries remain pending independently of the CREATE idempotency deadline;
legacy non-company expiry retains its P-80 cutoff. An unresolvable provider or
an unknown creation beyond the safe CREATE window remains pending with a safe
error code and blocked continuation, requiring later reconciliation. Retained
EXPIRE retries (including missing bindings and completed/unpaid observations)
use durable attempt counts for exponential backoff: 5, 10, 20, 40, 80, 160 seconds,
then a 5-minute cap. Beyond the CREATE deadline, pending work records
`HOLD_RECONCILIATION_NEEDED`; existing staff status/reconciliation responses expose
it as `safe_error_code`. Guest checkout status omits this field entirely. Recovery
completes the work and removes that active error projection, without clearing the
hold marker or continuation revocation.
Late CREATE persistence determines the EXPIRE obligation from the observed
open/unpaid Checkout and hold/review/session availability, including preserved
FAILED/EXPIRED attempts, independently of the resulting attempt state. Without
a company hold it uses the legacy dedupe key; company holds use their existing
retained work without a second enqueue. Regression coverage includes lost CREATE
response → payment_failed before binding → same-key recovery → table session
expiry before persistence → EXPIRE queued and executed. The active-session
variant deliberately expires the recovered Checkout too: FAILED is preserved
at CREATE persistence and cannot offer original-browser continuation, so its
Checkout must not remain silently payable. Confirmed provider expiry then
reconciles the attempt to EXPIRED under the existing worker rules.
Eligible CREATE/webhook work takes priority until EXPIRE is 30 seconds overdue
relative to available_at. Aged EXPIRE then competes at equal priority in timestamp
order, so newer arrivals cannot keep overtaking it. Retry backoff resets that age;
this bounds priority preference, not elapsed processing time behind older work.

Real PostgreSQL tests use the existing in-process provider only. They cover
scope exclusions, immutable snapshots, hold OFF, unknown expire outcomes,
complete/unpaid versus paid, both webhook arrival orders and duplicates,
concurrent expiry/payment, pending and in-flight creation including lost create
responses, and deterministic lock waits for create/webhook/staff-reconcile paid
paths. Stripe orders are staff-accepted before payment so the tests demonstrate
the review gate, independently of the staff-acceptance gate. Migration 008 is
paired, and the migration suite checks up/down/reapply and removal of its
retention function. Regressions explicitly process complete/unpaid and decline
webhooks before ON: complete/unpaid → ON → OFF → paid and decline → ON → OFF →
paid remain review-required with no KDS; decline → ON expires the still-open
Checkout. Retrieval failures, missing bindings, unavailable configuration and
completed/unpaid polling are tested beyond the real immutable CREATE deadline,
through the backoff cap and subsequent recovery, with no new Checkout creation.
Regressions also cover failed-before-binding → ON → OFF → late CREATE persistence
→ paid, requiring review with no KDS. A continuously replenished CREATE/webhook
queue verifies fresh-work priority and expiry progress after the 30-second aging
threshold (eligibility timestamps advance without sleeps). HTTP route tests verify
that guest status omits internal codes while staff status and reconciliation
retain them.

Late verified Checkout webhooks persist a missing binding even for terminal
attempts, preserving their state and existing correlation checks. Regressions
cover lost CREATE response → payment_failed before binding → ON → OFF → CREATE
deadline → delayed completed/unpaid webhook: FAILED remains unchanged at binding,
then retained EXPIRE retrieves complete/unpaid through eventual paid/review, or
retrieves and expires a still-open Checkout, without creating another session.

```sh
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55439/tojima_test?sslmode=disable' \
  go test -race -count=1 ./internal/... ./migrations/postgres
make fmt-check vet build migration-smoke
```

Manager approval, cancellation, refunds and review resolution belong to lanes
B/C and are not implemented. No frontend changes, real Stripe calls, live keys,
production deployment or independent final-review acceptance are claimed.
Payments already locally confirmed before HQ obtains its lock retain their
existing outcome; lane A does not recall previously released KDS cards.

日本語：P-82 A は本部保留時の未払い Stripe Checkout 失効と結果照合を実装する。
対象は同一テナントの LINKED 店舗と注文スナップショットの商品に限定する。
作成中・応答不明・保留解除後も処理を保持し、支払済みは失効へ戻さず要確認として
KDS を停止する。実 PostgreSQL と既存 fake provider で検証する。承認・取消・返金、
実 Stripe・本番受入は対象外。保留前に完了した未払い Checkout と、決済失敗後も
開いている Checkout を対象に含め、008 の部分インデックスも更新する。
完了・未払い → 保留 ON → OFF → 支払成功、および失敗 → ON → OFF → 支払成功は
要確認・KDS 未放行を維持し、失敗 → ON では開いた Checkout の失効を検証する。
取得失敗・未確定の作成・設定不備・未払い処理中は 5 秒から倍増し最大 5 分で再試行する。
CREATE 期限後も義務と撤回・保留マーカーを保持し、職員向け既存照会の
safe_error_code に HOLD_RECONCILIATION_NEEDED を返す。期限後の復旧、Checkout の
重複作成がないこと、CREATE と webhook の優先処理も検証する。

追記：Checkout 未紐付けの FAILED も、失効・支払済みが確定していなければ保留対象とし、
008 の部分インデックスを同じ条件にする。未紐付け失敗 → ON → OFF → 遅延 CREATE 保存
→ 支払成功でも要確認・KDS 未放行を検証する。EXPIRE は available_at から 30 秒経過後、
CREATE／webhook と同順位の時刻順で処理し、継続投入されるキューでも進むことを検証する。
再試行の待機で経過時間をリセットする。ゲスト応答から safe_error_code を完全に除き、
職員向け状態照会・照合では保持することを HTTP 経由で検証する。
遅延した検証済み webhook は、終端状態を変えずに未紐付けの Checkout ID を保存する。
CREATE 応答喪失 → 紐付け前の決済失敗 → ON → OFF → CREATE 期限経過 → 遅延した
completed／未払い webhook を検証する。保持した EXPIRE が取得を再開し、完了・未払いから
支払成功・要確認へ照合する場合と、まだ開いている Checkout を失効する場合を確認する。

繁體中文：P-82 A 實作本部停售後未付款 Stripe Checkout 的失效及結果核對，僅涵蓋
同租戶 LINKED 分店與訂單快照內的指定商品。建立中、回應不明及解除停售後仍保留
工作；已付款不得改成失效，改以待覆核旗標阻止 KDS 放行。使用真實 PostgreSQL
及既有 fake provider 驗證；主管核准、取消、退款、真實 Stripe 與正式上線不在本線範圍。
範圍包含停售前已完成但未付款，以及付款失敗後仍開放的 Checkout，並更新 008 的部分索引。
回歸測試明確涵蓋「完成未付款 → ON → OFF → 付款成功」與「拒付 → ON → OFF →
付款成功」仍待覆核且不放行 KDS，以及「拒付 → ON → Checkout 失效」。取得失敗、
尚無綁定、設定不可用與未付款處理中的工作，從 5 秒倍增退避至最多 5 分鐘；超過 CREATE
期限仍保留義務、撤銷與標記，既有員工查詢以 safe_error_code 回傳
HOLD_RECONCILIATION_NEEDED。另驗證期限後恢復、不重建 Checkout，以及 CREATE／webhook
優先處理。

補充：尚未綁定 Checkout 的 FAILED，只要未確認失效或付款，仍保留停售標記，008 部分索引
採用相同條件。驗證「未綁定即失敗 → ON → OFF → 延遲 CREATE 寫入 → 付款成功」仍須覆核、
不放行 KDS。EXPIRE 超過 available_at 30 秒後與 CREATE／webhook 同優先級，依時間排序；
持續補入佇列的測試驗證其仍能前進，重試退避會重設等待時間。HTTP 測試確認訪客回應完全不含
safe_error_code，員工狀態及核對回應則保留該欄位。
延遲的已驗證 webhook 會保存尚未綁定的 Checkout ID，且不改變終態。回歸涵蓋
CREATE 回應遺失 → 綁定前付款失敗 → ON → OFF → CREATE 期限已過 → 延遲的
completed／未付款 webhook；保留的 EXPIRE 恢復取得，核對完成未付款後的付款成功與
待覆核結果，或讓仍開放的 Checkout 失效，全程不建立新 Checkout。

# P-82 B — Manager review / 店長による提供承認 / 店長出餐審核

Lane B (MOD-05 S7) builds on accepted lane A `311a7ba` (PR #22). This
local-only candidate adds explicit fulfillment approval for a PAID order whose
existing payment-review flag is set, whose fulfillment is WAITING, and which
has no KDS card. Only a current STORE_MANAGER membership in the exact tenant
and store, with verified MFA, authorizes the action. STAFF_CASHIER, KITCHEN,
TENANT_ADMIN alone, guest capabilities, revoked memberships and cross-store or
cross-tenant memberships cannot approve. Read access uses the existing staff
operations policy; no client-supplied actor or role is trusted.

`POST /v1/staff/tenants/{tenantID}/stores/{storeID}/tables/{tableID}/sessions/{sessionID}/orders/{orderID}/approve-fulfillment`
requires `reason` (1–500 Unicode characters after trimming), `expected_version`
and `Idempotency-Key`. A committed key
replays the saved original response, even after later fulfillment, while
conflicting reuse is rejected. Authorization is checked again on every replay.
Stable 409 codes distinguish `stale_version`, `idempotency_conflict`,
`company_hold_active` and `review_not_ready`.

The decision lives in `operation_order_state.review_decision`, initially
UNRESOLVED; approval CASes that value plus the existing operation version to
APPROVED. CANCELLED is reserved for lane C; there is no cancellation/refund
endpoint or implemented cancellation transition. Lane C must lock the same
rows and CAS UNRESOLVED plus the expected operation version, so an approval
and cancellation cannot both win. Existing decisions are terminal in lane B.

The transaction lock order is company catalog SHARE → table session UPDATE →
Stripe payment attempt UPDATE → operation order state UPDATE → audit,
idempotency and release writes. This matches lane A/payment workers and HQ's
company catalog UPDATE, including the first hold when no hold row yet exists.
No provider call occurs in this transaction. Active HQ holds on ordered
product keys in the immutable quote block approval; turning the hold OFF only
makes explicit approval possible and never releases the order automatically.
Product/option PAUSED availability does not block this explicit fulfillment
decision: the manager checks with the kitchen. Store/tenant/table enablement
and current authorization still apply. Closed or expired sessions can be
reviewed without reopening them or restoring guest capabilities. The existing
fulfillment action permits kitchen/service transitions for APPROVED review
orders in closed sessions; all other closed-session actions remain blocked.

Approval clears both the order and payment-attempt review projections and the
order's operational hold. It also satisfies required staff reception, since
this is an explicit manager acceptance of fulfillment. It preserves paid
status, immutable order/amount/tax snapshots, the retained HQ expiry marker
and original-browser revocation. The existing release path creates one KDS
card, one KDS outbox row and its normal release audit in the same transaction.
One immutable manager decision event records actor, reason and before/after
state; KDS release remains a separate existing operational event. Duplicate
requests or concurrent managers cannot duplicate these effects. The existing
operations and payment `CheckClose` gates remain authoritative: unresolved
reviews block close; approval alone still requires service completion before
close can succeed.

Paired migration 010 extends existing state, event and idempotency tables and
restores previous transition guards on rollback. Number 009 is left untouched
for the parallel lane. Downgrade is for an empty/disposable schema: it refuses
to silently discard accepted manager history in a populated schema.

`GET .../operations/reviews` exposes a store-wide queue, including historical
sessions absent from the active table list. Staff see immutable amount/item
snapshots and safe reason codes, never Checkout/PaymentIntent IDs, provider
credentials or internal audit reasons. The existing `/staff` console adds
「要確認 / 待審核」 and a manager-only native approval dialog, a required reason,
44px controls, Japanese/Traditional Chinese copy and Asia/Tokyo timestamps.
A stale decision reloads the queue and requires renewed confirmation; an
uncertain response retains the frozen body and key for explicit retry. No
write is retried automatically. Identity changes discard prior queue/results.
This extends the P-79 console's own state under ADR-0006; no new frontend
framework, server-state library or guest wording is introduced.

Validation uses disposable PostgreSQL schemas and the existing in-process
fake provider only. Coverage includes HTTP role/MFA/current-membership/scope
checks, snapshot projection, CAS, exact-result replay and conflicting reuse,
concurrent approval, HQ hold ON/OFF, product PAUSED, non-review/unpaid rejection,
closed/expired sessions, close gates, immutable audit, and late/duplicate
payment observations after approval. Web Vitest cases cover queue rendering,
read-only cashier access, approval, stale reload/reconfirmation, uncertain
same-key/body retry and obsolete identity responses. The director runs Vitest;
this sandbox does not claim those tests executed.

```sh
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55439/tojima_test?sslmode=disable' \
  go test -race -count=1 ./internal/... ./migrations/postgres
make fmt-check vet build migration-smoke
pnpm --dir web lint
pnpm --dir web exec tsc -b --pretty false
# Director, outside this sandbox:
pnpm --dir web test
```

日本語：P-82 B は支払済み・要確認・KDS 未送信の注文に対し、同一店舗の現在の
STORE_MANAGER 権限と MFA を持つ店長だけが、理由・状態バージョン・冪等キーを
用いて提供を承認する。承認は注文と決済試行の確認フラグ、運営保留を解除し、必要な
スタッフ受付も満たして、既存経路で KDS カードと outbox を一件だけ作成する。
同一トランザクションで既存の変更不可イベントに担当者・理由・前後状態を記録する。
本部の商品保留が有効な間は company_hold_active で拒否し、解除後も自動承認しない。
店舗の商品 PAUSED は承認を妨げず、店長が厨房と確認する。終了・期限切れの卓も
店舗全体の要確認一覧から処理でき、ゲスト参加権限は復活しない。
ロック順は本部カタログ SHARE → 卓セッション → 決済試行 → 注文状態。決定列と
既存バージョンの CAS を将来の取消レーンと共有する。未解決は閉卓不可、承認後も
提供完了が必要。同一キーは保存済み結果を返し、競合は拒否する。画面は理由入力付き
標準 dialog、44px 操作領域、日繁中対応。古い状態は再読込・再確認、応答不明は
同じ内容・キーで明示再試行する。PostgreSQL と fake provider のみを使用し、Vitest
実行は director に引き継ぐ。取消・返金、ゲスト文言、実 Stripe、本番受入は対象外。

繁體中文：P-82 B 讓同租戶、同分店且具現行 STORE_MANAGER 權限及 MFA 的店長，
以原因、預期狀態版本與冪等鍵，明確核准已付款、待審核且尚無 KDS 卡片的訂單。
同一交易清除訂單與付款嘗試的覆核旗標、營運保留，完成必要的員工接單，透過既有
流程建立唯一 KDS 卡片及 outbox，並在既有不可變事件記錄人員、原因與前後狀態。
本部商品保留仍有效時回傳 company_hold_active；解除後仍需明確核准。分店商品
PAUSED 不阻止核准，由店長向廚房確認。已關閉或過期桌次也能從全店待審核佇列處理，
不恢復訪客權限。鎖定順序為本部菜單 SHARE → 桌次 → 付款嘗試 → 訂單狀態；決定欄
與既有版本 CAS 供未來取消線共用。未解決不得關桌，核准後仍須完成送餐。
相同鍵回傳原結果，衝突重用拒絕。畫面採原生 dialog、必填原因、44px 操作目標與日文／
繁中；過期版本重新載入並確認，回應不明則以固定內容與相同鍵明確重試。
僅用 PostgreSQL 及 fake provider；Vitest 交 director 執行。取消／退款、訪客文案、
真實 Stripe 與正式上線不屬於本線。
