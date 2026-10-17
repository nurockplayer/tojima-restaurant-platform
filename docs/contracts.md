# S0 contracts / 契約・契約邊界

This is a proposed relational contract and executable contract seed, not a production schema or an implemented ordering/payment service. Exact DDL and transaction strategy belong to each dependent stage after the DB ADR is approved.

| Source of truth / 權威資料 | Required constraint / 必要制約 | Stage |
| --- | --- | --- |
| tenant → store → table | Scoped foreign keys; tenant/store indexes; resource scope comes from server-owned data | S1 / P-76 |
| table_session | At most one OPEN session per physical table; CLOSED grants invalid; new seating gets a new ID | S1 |
| shared cart | One active generation per session; expected version CAS; operation idempotency | S3 / P-78 |
| committed order/items | Unique (tenant, store, session, generation); immutable price/options/tax/publication/payment-method snapshot; order commit and generation advance in one transaction | S3 |
| payment attempt | Scoped order + attempt identity uniqueness; retry cannot create another charge; reconciliation determines effective attempt | S5 / P-80 |
| cash settlement/allocation | Atomic allocation to unpaid CASH orders; cannot collect Stripe payment again | S4 / P-79 |
| webhook | Provider event identity deduplication; verify signature before processing | S5 |
| KDS/outbox | Unique card per committed order; release policy and outbox in transaction; delivery retry safe | S4 |
| refunds/audit | Immutable original payment reference; pending/failed not REFUNDED; scoped actor and outcome, no secrets | S7 / P-82 |
| company catalog/store overrides/hold | Atomic publication; tenant/store isolation; old order snapshots unchanged | S2/S6 |
| grants | TABLE_JOIN, ORDER_READ and CHECKOUT_RESUME are separate capabilities; bounded expiry/revocation; no checkout secret in shared summaries | S1/S5/S8 |

日本語：Go が認可・価格・注文確定・決済状態の権威。scope 判定は実データと最新 membership から行い、client の tenant/store/price/paid を信用しない。S0 の型・fixture テストは将来の DB race/IDOR/transaction 検証の代替ではない。

繁體中文：Go 是授權、定價、送單與付款狀態權威。必須用服務端資料所屬與目前 membership 驗權；S0 的型別／契約測試只是後續實作的起點，尚未證明真實 DB 併發與交易安全。

P-76 follows P-75 acceptance. It remains blocked while P-75 review/acceptance and the DB implementation gate are unresolved. Do not start P-77–84 by using historical specification issue hierarchy as authorization.
