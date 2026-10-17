# P-81 A — Company catalog publication scale acceptance / 本部菜單發布規模驗收

Authority: [P-81](https://linear.app/higashijima-manage-system/issue/P-81), [P-77](https://linear.app/higashijima-manage-system/issue/P-77) (sequence A accepted scope).

This slice covers **sequence A only**. It adds PostgreSQL acceptance evidence for a company catalog serving 50 LINKED stores while one store remains DETACHED and another tenant has its own catalog. It uses the existing public `catalog.Service`, `ordering.Service`, and isolated `testguest` fixture; it adds no production endpoint, schema, service, or identity adapter.

The test proves that a valid company draft stays invisible before publication, the published revision converges across all 50 linked stores, active absolute store price overrides survive, the detached copy and other tenant remain unchanged, and an already committed order retains its quote and tax snapshot after publication. Existing catalog tests continue to cover revision sealing, failed-publication rollback, stale writes, and detach/publication concurrency.

Run the focused acceptance test against a disposable loopback PostgreSQL database:

```sh
TZ=UTC TEST_DATABASE_URL='postgres://postgres@127.0.0.1:32768/tojima_test?sslmode=disable' go test ./internal/catalog -run '^TestPostgresP81CompanyCatalogScaleAcceptance$' -count=1
```

This does not claim the P-81 D emergency-hold scale/race requirement, any of P-81 B/C/E, or production MOD-08 integration. P-81 D remains a separate authoritative-commit-gate implementation and acceptance slice; ingredient mapping in C still depends on the shared MOD-01 ingredient/recipe identity contract.
