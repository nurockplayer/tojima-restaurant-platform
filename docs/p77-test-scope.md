# P-77 — Published menu test implementation / 公開メニュー・菜單測試實作

Authority: [P-77](https://linear.app/higashijima-manage-system/issue/P-77), [flat options P-46](https://linear.app/higashijima-manage-system/issue/P-46), [server pricing P-47](https://linear.app/higashijima-manage-system/issue/P-47), [LINKED/DETACHED P-48](https://linear.app/higashijima-manage-system/issue/P-48), [store overrides P-49](https://linear.app/higashijima-manage-system/issue/P-49), [atomic publication P-50](https://linear.app/higashijima-manage-system/issue/P-50).

## Scope and acceptance / 範囲・驗收

日本語：P-76 は PR #2、merge `7b758fd1096f5145275296c902f691db61315f10`、独立 Oracle/Ego-lite review と PostgreSQL CI で受入済み。S2 は同じ Go サービス・共有 PostgreSQL として、会社／店舗 catalog、公開版、税込売価、単層 options と実税計算を追加する。2026-10-18 の最新 Owner 決定により初期税務ルールは承認済み。

繁中：本包接續已驗收的 P-76，以同一 Go／PostgreSQL 工程建立菜單、含稅價格及真實稅額計算。最新稅務規格 v1.0 已解除稅務待決，不再要求逐店批准法定稅率。P-77 仍須完成確切候選版本的測試及獨立審查；通過後才解除 P-78 依賴。

## Catalog contracts / 菜單契約

| Boundary | Required behavior |
| --- | --- |
| Company and store ownership | One catalog model with explicit tenant and COMPANY/STORE owner; server resolves actual ownership before authorization |
| Draft | Mutable data with expected-version checks; stale writes are rejected; draft edits do not affect guest reads |
| Publication | Complete revision sealed before the current pointer changes; sealed revisions reject later INSERT/UPDATE/DELETE of their contents; failed or stale publication leaves the previous revision available |
| LINKED | Default store mode; current published company revision, stable product keys, absolute store base-price override when active, company option prices |
| Price override | Nonnegative integer amount with version checks; master price changes retain the override; clear returns to the current master price; actor/time/before/after recorded transactionally |
| DETACHED | Explicit company-admin action copies current effective prices into a STORE-owned catalog with new category/product/group/option IDs; later master changes and old linked overrides do not affect it |
| Flat options | One level, required min/max, no duplicate selection, no nested groups or option quantities; active option surcharge is a nonnegative integer |
| Availability | Product and option state is store-scoped; absent readiness means PENDING_CONFIRMATION; paused/pending options cannot satisfy a required group |
| Public menu | Published visible names, inclusive JPY prices, options and safe status only; no drafts, internal reasons, actor, management topology/version or audit data |
| Shared price validation | Server resolves current published data and selections; reject invalid selection, negative amount and integer overflow; client effective price is never authority |

Removing an entry from a later draft must not rewrite an earlier published revision. Disabling an item preserves its historical snapshot. No order history is implemented by S2; later order creation must persist its own immutable names, selected options, prices, tax policy/result and publication identity.

DETACH copies menu data only. It does not clone tables, QR modes, staff, identity, payment settings, tax settings or order history. Administrative permissions use the existing MOD-08 adapter boundary and current membership: TENANT_ADMIN with MFA controls the company catalog and detachment; STORE_MANAGER with MFA controls its authorized store's prices, availability and detached draft. Another store's role cannot contribute privileges.

## Draft input bounds / 草稿入力上限・草稿容量

Whole-catalog replacement has an explicit 1 MiB canonical `CatalogData` JSON budget, reserving the full 19 decimal digits for each product base price. This keeps the budget unchanged when DETACH copies any valid int64 store override into the base price, so the resulting detached draft remains editable. Additional aggregate bounds are 500 products, 1,000 option groups, 5,000 options and 256 KiB of description text, alongside the existing per-field/per-collection limits. These are bounded MVP request limits, not pricing or tax policy. Company and detached-store draft PUTs allow up to 6 MiB + 16 KiB on the wire to accommodate JSON Unicode escaping and the version envelope; larger requests return HTTP 413. Other small control/session requests retain the 16 KiB limit. Structurally invalid or decoded catalogs exceeding the catalog budget return HTTP 400 before database writes.

Category/product/group/option keys contain 1–100 Unicode characters, matching PostgreSQL's character limit. Invalid UTF-8 and control characters are rejected by service validation. The catalog-key length cap does not impose a new length limit on MOD-08 subject or tenant/store scope identifiers. HTTP and service tests cover realistic drafts larger than 16 KiB, oversized fixed-length/chunked requests, and 100/101-character ASCII and multibyte keys.

## Tax implementation / 税計算・稅額計算

The latest P-77/P-47 and [ADR-0004](adr/0004-japan-tax-calculation.md) supersede the earlier missing-policy hold. All product and option selling prices are inclusive integer JPY. Current dine-in uses the central effective standard rule, initially 10%; eligible reduced food 8% is represented for future classification without adding a takeaway flow.

The real Go engine groups gross amounts by normalized rate, then computes exact inclusive tax with floor once per group. Rules have effective dates and legal status; missing/ambiguous/unsupported data is an explicit error. Persistable copied snapshots retain calculation inputs, rule versions and amounts without consulting later schedules. These are real formulas and approved initial data, not test-double tax values.

`catalog.Service.QuoteDineIn` accepts product/option keys and quantities, reads one repeatable database snapshot, applies the current published prices and store overrides, validates availability and selections, and resolves the effective central dine-in rule for the trusted server instant's Japanese date. It returns copied item/option names and prices together with the real tax snapshot. Guests cannot supply prices, rates or an arbitrary tax class through this contract. S3 must perform its order-admission revalidation and persist the accepted snapshot in the order transaction; this read-only quote does not reserve a price or admit an order.

日本語：P-78 で注文確定と同時に税 snapshot を保存する。P-79/P-80 で Stripe 注文別領収と CASH まとめ収書の invoice 境界・税額・返金 E2E を確認する。cart quote を正式 invoice と見なさない。

繁中：法定稅率及首版捨入規則已明確，不再列為阻礙。後續訂單保存當時快照，未來法改不回算歷史金額；金流及收據仍依既定工作包驗收。

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

Migrations 002 (menu) and 003 (central legal rates) have paired down migrations; the embedded test migration runner applies the complete set atomically and rolls it back in reverse order. This is destructive test-schema teardown, not a production data rollback procedure. Never point the test harness at a production or shared business database. The separate S0 SQLite smoke remains historical infrastructure evidence.
