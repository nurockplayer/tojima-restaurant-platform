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
