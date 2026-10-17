# P-76 — PostgreSQL test foundation / テスト実装・測試地基

Authority: [P-76](https://linear.app/higashijima-manage-system/issue/P-76), [QR modes P-39](https://linear.app/higashijima-manage-system/issue/P-39), [session lifecycle P-41](https://linear.app/higashijima-manage-system/issue/P-41), [scope P-37](https://linear.app/higashijima-manage-system/issue/P-37), [MOD-08 P-58](https://linear.app/higashijima-manage-system/issue/P-58).

## Decision and scope / 決定・範囲

日本語：2026-10-18、Owner は PostgreSQL のテスト実装を承認した。P-75 は PR #1、merge `5d2865a0b6dbd077f8c5e39f2716376d209b6074` と main CI で整合済み。S1 は店舗・卓・セッション・入場 grant とスタッフ adapter の地盤だけを実装する。現行 executable に test identity を組み込まない。IdP 本番資格、hosting、UI、deployment はこの承認に含まれない。

繁中：本包承接已整合的 P-75，只實作 PostgreSQL 的租戶／分店／桌位／桌次與 QR 入場測試地基。測試替身用於注入式 API 驗證，不提供假員工登入入口。正式 IdP、託管供應商、UI 與投產仍另行驗收；不新增菜單、訂單、付款、KDS 或跨模組移轉功能。

## Contracts / 必守契約

| Boundary | S1 behavior / S1 行為 |
| --- | --- |
| Scope | Database foreign keys and server-loaded tenant/store/table scope; fresh MOD-08 membership on staff requests |
| Seating | At most one OPEN session per physical table; concurrent staff opens have one winner; direct joins converge; expired sessions deny guests and remain occupied until policy-approved close |
| Mode | Store defaults to STAFF_OPEN; each session freezes its entry mode; settings changes affect later sessions |
| STAFF_OPEN | A staff-opened, unexpired current session is required before a guest receives TABLE_JOIN |
| DIRECT_SCAN | Explicit POST scan may find/create a session; persisted throttling; staff-acceptance requirement preserved for later KDS |
| SESSION_PIN | Server-issued short-lived PIN protected at rest; bounded attempts; reset invalidates prior PIN and prior grants |
| Guest capability | Random, short-lived, scoped TABLE_JOIN; every protected read verifies session state, expiry and revocation |
| Staff identity | Resolver and current membership ports; default deny; canonical MOD-08 roles; fixture implementations only in tests |
| Close | Explicit authorized action; reject when closure policy is unavailable; future order/payment/KDS policy cannot be assumed satisfied |

QR contains a public locator, never a permanent grant or PIN. A photographed fixed QR cannot prove physical presence, even in STAFF_OPEN mode. Revoking an old seating's token prevents using that token for another seating; it does not make the public QR a personal identity credential. No personal receipts or checkout details are served by S1.

固定 QR は現地存在の証明ではない。舊桌次 token 失效與「持固定 QR 的人再掃碼」是不同問題；後者仍受當前入場模式與限流限制，DIRECT_SCAN 的後續廚房放行還要驗店員接單，不能直接出餐。

## API seam / API 境界

`httpapi.NewSessionHandler` accepts a PostgreSQL session service and explicit MOD-08 identity/authorization adapters. Integration tests exercise this HTTP handler; `cmd/server` continues to expose health only. There is no seeded staff login, identity-header shortcut or production auth implementation.

| Method and path | Contract |
| --- | --- |
| `GET /v1/guest/tables/{locator}` | Read safe entry mode/current session status; never creates a session |
| `POST /v1/guest/tables/{locator}/join` | Explicit DIRECT_SCAN entry; optional `expected_session_id` refuses stale seating |
| `POST /v1/guest/sessions/{sessionID}/join` | Mode-specific entry, including PIN; never substitutes a new seating for the requested ID |
| `GET /v1/guest/sessions/{sessionID}` | Require the live, scoped TABLE_JOIN bearer capability |
| `POST /v1/staff/tenants/{tenantID}/stores/{storeID}/tables/{tableID}/sessions` | Open a new seating; return a new PIN only to authorized staff when required |
| `GET /v1/staff/tenants/{tenantID}/stores/{storeID}/tables/{tableID}/sessions/{sessionID}` | Current membership and exact resource scope required |
| `POST …/sessions/{sessionID}/close` | Explicit close through the transaction-bound close policy; default deny |
| `POST …/sessions/{sessionID}/pin` | Rotate PIN and revoke old grants atomically |
| `PUT /v1/staff/tenants/{tenantID}/stores/{storeID}/qr-mode` | Change the store default for future sessions |

Staff actions use `STAFF_CASHIER`/`STORE_MANAGER`; kitchen has scoped read access, and tenant admins can read and change store QR settings. Manager/admin grants require verified MFA. FINANCE_VIEWER receives no S1 table-control permission. Same-store role grants are evaluated independently of membership order; another store's role cannot contribute privileges.

The MOD-08 resolver must verify the session, absolute expiry, revocation, platform security hold and authentication strength before returning an actor. Current membership is loaded separately on every request. These are adapter requirements, not a claim that a real provider has been integrated.

## Test environment and rollback / テスト環境・還原

`make test-postgres` starts a disposable PostgreSQL 17 container bound to a random loopback port, runs `make ci` with `TEST_DATABASE_URL`, and removes only that container on exit. It does not reuse a caller's database URL. Docker must already be available. The container uses trust authentication only for this isolated test harness; it is not deployment configuration.

GitHub CI supplies its own disposable PostgreSQL service and a mandatory `TEST_DATABASE_URL`. Go integration tests create unique schemas, run migrations and scenarios there, and clean up only those schemas. They do not drop a caller's database. Never point `TEST_DATABASE_URL` at a production or shared business database.

The paired PostgreSQL migrations and their ledger are tested for apply, rollback, and reapply. The S0 SQLite smoke remains historical infrastructure evidence; it does not establish PostgreSQL correctness. Only actual PostgreSQL integration results count for S1.

## Remaining gates / 未解除・待決

- Session/grant/PIN lifetimes and abuse thresholds are validated test parameters, not Owner-approved operating numbers. Production acceptance must select and measure those values.
- Staff authentication is still a MOD-08 integration gate. No test fixture qualifies a real IdP or production account.
- Close checks for unpaid cash, unresolved payment/refund/settlement, manager review and unserved orders remain with the owning S4/S7 stages. S1 exposes a fail-closed policy boundary, not forced close.
- Hosting, backup/restore objectives, deployment configuration and UI are not approved by the PostgreSQL test decision. Initial Japanese tax/rounding was separately approved in the subsequent [ADR-0004](adr/0004-japan-tax-calculation.md).
- P-77 depends on P-76 acceptance. P-78 also depends on P-77. No menu, payment or cart implementation is implied by an S1 test PASS.
