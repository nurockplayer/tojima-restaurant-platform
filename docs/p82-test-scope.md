# P-82 A

Lane A (MOD-05 S7) adds best-effort expiration of existing unpaid Stripe
Checkouts when HQ activates a company product emergency hold. The same hold
transaction selects same-tenant LINKED stores and immutable order quote lines,
revokes original-browser continuation, retains an attempt marker, and writes
EXPIRE work plus an attempt audit event to the existing payment tables. CASH,
DETACHED stores, other tenants, unrelated products and already-paid attempts
are excluded. Completed-but-unpaid attempts and FAILED attempts with a bound,
potentially open/unpaid Checkout are included: a decline does not close the
Checkout. Migration 008's candidate index includes these bound FAILED attempts.
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
it as `safe_error_code`. Recovery completes the work and removes that active
error projection, without clearing the hold marker or continuation revocation.
Eligible CREATE/webhook work takes priority over EXPIRE work.

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
Tests also verify staff-visible reconciliation codes and CREATE/webhook priority.

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
