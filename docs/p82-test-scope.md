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
