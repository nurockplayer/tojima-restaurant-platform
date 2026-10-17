# P-81 A — Company catalog publication scale acceptance / 本部菜單發布規模驗收

Authority: [P-81](https://linear.app/higashijima-manage-system/issue/P-81), [P-77](https://linear.app/higashijima-manage-system/issue/P-77) (sequence A accepted scope).

This slice covers **sequence A only**. It adds PostgreSQL acceptance evidence for a company catalog serving 50 LINKED stores while one store remains DETACHED and another tenant has its own catalog. It uses the existing public `catalog.Service`, `ordering.Service`, and isolated `testguest` fixture; it adds no production endpoint, schema, service, or identity adapter.

The test proves that a valid company draft stays invisible before publication, the published revision converges across all 50 linked stores, active absolute store price overrides survive, the detached copy and other tenant remain unchanged, and an already committed order retains its quote and tax snapshot after publication. Existing catalog tests continue to cover revision sealing, failed-publication rollback, stale writes, and detach/publication concurrency.

Run the focused acceptance test against a disposable loopback PostgreSQL database:

```sh
TZ=UTC TEST_DATABASE_URL='postgres://postgres@127.0.0.1:32768/tojima_test?sslmode=disable' go test ./internal/catalog -run '^TestPostgresP81CompanyCatalogScaleAcceptance$' -count=1
```

This does not claim the P-81 D emergency-hold scale/race requirement, any of P-81 B/C/E, or production MOD-08 integration. P-81 D remains a separate authoritative-commit-gate implementation and acceptance slice; ingredient mapping in C still depends on the shared MOD-01 ingredient/recipe identity contract.

## B1 — Publication safety and availability permissions

B1 adds company publish guards under the existing LINKED-store row locks before
writing the new revision: existing products cannot acquire a new required group
or turn an optional group into a required group, existing required minima cannot
increase, and retiring an old available option cannot make a currently sellable
LINKED store lose its required choices. New options do not count as available.
The same transaction retains the prior publication on failure; successful new
keys stay PENDING_CONFIRMATION and old availability stays unchanged.

The authenticated publish API returns `publication_blocked` with stable issue
codes and the affected product/group/store. STAFF can immediately PAUSE a
product or option in its own store; only a STORE_MANAGER with MFA can confirm
or reactivate it. Existing version, reason, membership and audit checks apply.
No migration or new approval workflow is introduced.

PostgreSQL tests exercise unsafe/safe publication differences, a store pause
racing publication, pending choices, detached/tenant isolation, structured error
authorization, role/MFA/scope enforcement, and stale availability writes.
B1 does not deliver the M01/M02 Web surface or read-only publication preview;
those remain the next B2 slice before moving to C. Production IdP and deployment
acceptance remain external gates.

Authorities: [Step 18](https://linear.app/higashijima-manage-system/document/mod-05-jazhstep-18-既有商品禁止新增必選群組發布驗證-v01-789e19f3e339)
and [Step 19](https://linear.app/higashijima-manage-system/document/mod-05-jazhstep-19-既有必選群組安全擴充發布檢查-v01-b341bf8c1033).

## B2 — Menu administration and reviewed publication

The `/catalog` React/TypeScript surface uses the existing tenant/store menu
APIs. Company administrators read published content separately from a mutable
draft, edit structured categories/products/single-layer option groups, save
with a version, and review a read-only publication preview before publishing.
The preview fingerprint binds the draft, publication and relevant LINKED store
state; checked publication revalidates under the same locks and returns a
conflict when reviewed inputs changed. Preview itself writes no publication,
availability or audit data. Correcting an old publication still requires a new
draft and publication, with no rollback action.

The store surface displays source/mode/revision, source base price, absolute
store override and effective price. Server capabilities determine visible
controls; every write independently checks current identity/membership, scope,
role, MFA, version and reason. Staff can pause; managers can confirm/reopen,
set/reset prices and edit an already detached store snapshot. Only a tenant
administrator with MFA can explicitly detach a store. A stale
write requires an authoritative reload instead of silently retrying changed
intent. Japanese and Traditional Chinese use the same API behavior.

Local acceptance uses the explicit isolated fixture only:

```sh
cd web
pnpm install --frozen-lockfile
pnpm build
cd ..
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:32768/tojima_test?sslmode=disable' \
  go run ./cmd/operationstest -catalog-fixture -credentials /tmp/tojima-catalog-credentials.json
```

Open `http://127.0.0.1:8082/catalog`. Manually enter the test tenant/store and
one opaque bearer credential from the requested owner-only file:
`credentials.catalog_admin`, `credentials.manager`, or `credentials.cashier`.
The browser does not read the credential file or let callers choose their role.
Only `-catalog-fixture` mints the test tenant administrator and mounts admin
menu routes. The file must not already exist, stays outside the web root, and
is removed on orderly shutdown; its credentials are never committed or logged.
The PostgreSQL schema is disposable and isolated. These memberships are test
fixtures, not production MOD-08 identities.

B2 leaves P-81 C/D/E, real IdP, production runtime and deployment acceptance
outstanding. The production `cmd/server` remains health-only. Ingredient
identity still comes from MOD-01; this slice adds no ingredient master,
inventory, payment behavior or approval workflow.

## D1 — Company emergency-hold authority

D1 adds one versioned emergency-hold record per company product and an immutable
audit result for each accepted request. A tenant administrator with current
membership and MFA can read or transition the hold through
`/v1/admin/tenants/{tenantID}/menu/products/{productKey}/emergency-hold`.
Writes require `active`, a reason, the expected version and an idempotency key.
A repeated identical request returns its original result without changing a
newer hold; conflicting reuse, stale versions and redundant transitions fail.
Clients must reload current state after a replay instead of treating the saved
result as the current generation.

Hold transitions lock the existing company catalog parent before reading or
writing the product hold. Order admission, publication and detachment share
that parent lock, including the first hold when no product row exists. An
active hold blocks new LINKED-store quotes and CASH/STRIPE order commitments,
preserves the cart, and prevents detaching the held product into an independent
copy. Already detached stores and other tenants remain independent. Replaying
an already committed order still returns that historical order.

Turning a hold off does not change any store product/option availability or
price override. PAUSED, PENDING_CONFIRMATION and unsatisfied required options
remain unavailable. The hold version participates in quote consent even while
inactive, so an ON/OFF cycle requires a new preview. Publishing a corrected
draft does not clear the hold; retiring a product preserves its hold history.
Database read errors fail closed before order or payment side effects.

Migration 007 adds the indexed hold table and nullable replay fields to the
existing immutable menu audit. No per-store fanout, new ingredient master,
inventory service or approval engine is introduced. The own-store admin menu
projection exposes only a safe active flag; internal hold reasons, actors and
tenant-wide counts require the company-admin endpoint.

D1 acceptance covers real PostgreSQL scale/isolation, CAS and replay, quote
generation changes, admission/detachment locking, store-state preservation,
existing-order immutability and HTTP authorization. The full suite runs with
the same disposable loopback `TEST_DATABASE_URL` used above. Hosted CI applies
and exercises the migration in an isolated PostgreSQL service.

D1 does not complete P-81. C remains blocked on the approved MOD-01 identity and
lookup contract. D2/E adds the company control and explicit guest hold display;
S7 owns expiry/reconciliation of already-issued unpaid Stripe checkouts. Real
IdP, production runtime, deployment and pilot-store acceptance remain separate.
