# P-77 — Published menu test implementation / 公開メニュー・菜單測試實作

Authority: [P-77](https://linear.app/higashijima-manage-system/issue/P-77), [flat options P-46](https://linear.app/higashijima-manage-system/issue/P-46), [server pricing P-47](https://linear.app/higashijima-manage-system/issue/P-47), [LINKED/DETACHED P-48](https://linear.app/higashijima-manage-system/issue/P-48), [store overrides P-49](https://linear.app/higashijima-manage-system/issue/P-49), [atomic publication P-50](https://linear.app/higashijima-manage-system/issue/P-50).

## Scope and acceptance / 範囲・驗收

日本語：P-76 は PR #2、merge `7b758fd1096f5145275296c902f691db61315f10`、独立 Oracle/Ego-lite review と PostgreSQL CI で受入済み。S2 は同じ Go サービス・共有 PostgreSQL のテスト実装として、会社／店舗 catalog、公開版、基礎売価、単層 options の境界を追加する。P-77 全体の完了判定には税務 gate の解消と、その候補 commit の検証が必要。

繁中：本包接續已驗收的 P-76，以同一 Go／PostgreSQL 工程建立菜單與價格資料基礎。這不是訂單、收款或正式上線驗收。P-47 的稅率／端數規則尚待 Owner 決定；非稅務部分的測試通過不能代替 P-77 完整驗收，也不會自動解除 P-78 的依賴。

## Catalog contracts / 菜單契約

| Boundary | Required behavior |
| --- | --- |
| Company and store ownership | One catalog model with explicit tenant and COMPANY/STORE owner; server resolves actual ownership before authorization |
| Draft | Mutable data with expected-version checks; stale writes are rejected; draft edits do not affect guest reads |
| Publication | Immutable complete revision and one current pointer; publication switches atomically; failed or stale publication leaves the previous revision available |
| LINKED | Default store mode; current published company revision, stable product keys, absolute store base-price override when active, company option prices |
| Price override | Nonnegative integer amount with version checks; master price changes retain the override; clear returns to the current master price; actor/time/before/after recorded transactionally |
| DETACHED | Explicit company-admin action copies current effective prices into a STORE-owned catalog with new category/product/group/option IDs; later master changes and old linked overrides do not affect it |
| Flat options | One level, required min/max, no duplicate selection, no nested groups or option quantities; active option surcharge is a nonnegative integer |
| Availability | Product and option state is store-scoped; absent readiness means PENDING_CONFIRMATION; paused/pending options cannot satisfy a required group |
| Public menu | Published names, prices, options and safe status only; no drafts, internal reasons, actor or administrative audit data |
| Shared price validation | Server resolves current published data and selections; reject invalid selection, negative amount and integer overflow; client effective price is never authority |

Removing an entry from a later draft must not rewrite an earlier published revision. Disabling an item preserves its historical snapshot. No order history is implemented by S2; later order creation must persist its own immutable names, selected options, prices, tax policy/result and publication identity.

DETACH copies menu data only. It does not clone tables, QR modes, staff, identity, payment settings, tax settings or order history. Administrative permissions use the existing MOD-08 adapter boundary and current membership: TENANT_ADMIN with MFA controls the company catalog and detachment; STORE_MANAGER with MFA controls its authorized store's prices, availability and detached draft. Another store's role cannot contribute privileges.

## Tax gate / 税務 gate・稅務待決

P-47 explicitly says: 「日本適用稅率/端數計算要在實作前確認，不可臆測。」 The PostgreSQL test approval does not select tax rates, tax classes, inclusive/exclusive prices, rounding grain or rounding method. No approved tax policy has been supplied for this implementation.

The tax port therefore returns unavailable. No Japanese rate or rounding default is inferred. Integer base/option price tests establish arithmetic safety only; they do not establish tax correctness or a customer-payable total. A table QR locator allows reading the public menu, not authorizing an order or a payment.

日本語：税区分・税込／税別・端数処理は未決。税計算を無効のままにし、Owner の回答を正式な範囲付き判断として記録した後に、必要な fixture と検証を追加する。

繁中：正式稅務規則未批准；不從範例售價猜稅率或捨入方式。若 Owner 僅允許測試替身，仍必須保留正式報價／收款停用，並明確記錄該測試授權的限制。

## API seam / API 境界

`httpapi.NewMenuHandler` is an injected integration handler, separate from the health-only executable. All administrative requests require verified identity and current scoped membership. Client JSON cannot supply an authoritative effective price or actor.

| Method and path | Contract |
| --- | --- |
| `GET /v1/guest/tables/{locator}/menu` | Published safe menu for the server-resolved table/store; never creates a table session |
| `GET /v1/admin/tenants/{tenantID}/menu/draft` | Company draft and its coherent version |
| `PUT /v1/admin/tenants/{tenantID}/menu/draft` | Replace draft using the supplied expected `version`; no change to current publication |
| `POST /v1/admin/tenants/{tenantID}/menu/publish` | Publish the supplied `expected_version` atomically |
| `POST /v1/admin/tenants/{tenantID}/stores/{storeID}/menu/detach` | Copy the supplied `expected_revision`; stale publication refuses the operation |
| `GET /v1/admin/tenants/{tenantID}/stores/{storeID}/menu` | Current effective store menu and versions needed for administrative writes |
| `GET/PUT …/stores/{storeID}/menu/draft` | Read/replace an independently owned detached draft |
| `POST …/stores/{storeID}/menu/publish` | Publish a detached draft with expected-version protection |
| `PUT …/stores/{storeID}/menu/prices/{productKey}` | Set an absolute linked-store base price using `price_minor` and `expected_version` |
| `POST …/stores/{storeID}/menu/prices/{productKey}/reset` | Deactivate the current override with `expected_version`; current master price applies |
| `PUT …/stores/{storeID}/menu/availability` | Change a scoped product/option status using its `expected_version`; retains audit |

There is no public order/checkout endpoint in this handler. The table locator exposes a public menu; it does not replace the separate TABLE_JOIN capability required for later guest actions.

## S6 and later boundaries / 後続範囲・後續工作

P-77 requires the same model to support these later rules; their full workflows belong to P-81/S6 and are not accepted exceptions:

- Step 13–17: new product/option readiness confirmation by each store, shared material readiness and staff workflow. Preserve pending defaults and existing states across publications; no real-time inventory automation.
- Step 18–19: publication differences for existing required groups, including new required groups, optional-to-required conversion, increasing minimum selection and removal of necessary options. Full cross-store publication safety remains a separate acceptance requirement before operational publication.
- Step 20–23: company emergency hold, safe customer display, release using the store's then-current state and concurrency with new sales. Never infer company emergency hold from a store pause.
- P-78 and later: cart-wide re-quote, explicit customer reconfirmation, immutable order creation, payment, refunds and kitchen release. A price calculation or public menu response is not an order admission decision.

Until those gates and real MOD-08 integration are accepted, the executable remains health-only. Integration handlers use explicit test adapters; no fixture identity or production mutation route is enabled by this work.

## Validation and rollback / 検証・還原

Run `make test-postgres` against its disposable loopback-only PostgreSQL container. CI uses its own PostgreSQL service. Tests must execute actual database publication, isolation, stale writes, overrides, detachment, options, immutable history and API authorization; a run with database integration tests skipped is not acceptance.

Migration 002 is paired with a down migration; the embedded test migration runner applies both S1 and S2 atomically and rolls them back in reverse order. This is destructive test-schema teardown, not a production data rollback procedure. Never point the test harness at a production or shared business database. The separate S0 SQLite smoke remains historical infrastructure evidence.
