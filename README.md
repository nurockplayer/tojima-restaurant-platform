# Tojima Restaurant Platform / 東島飲食店管理システム

MOD-05 P-75: minimal Go modular monolith foundation. This is an engineering scaffold, not an implemented ordering system or a production release.

Authority: [P-74 entry](https://linear.app/higashijima-manage-system/issue/P-74), [P-75 scope and acceptance](https://linear.app/higashijima-manage-system/issue/P-75), [Codex engineering handoff v1.0 (JA/ZH)](https://linear.app/higashijima-manage-system/document/mod-05-jazhcodex-工程交接step02-34-實作地圖與驗收-v10-7ce203c6cfb5).

## Local development

Requirements: Go matching `go.mod`, Python 3 with standard-library `sqlite3`, and Make. There are no external Go modules or frontend dependencies.

```sh
make ci
APP_ADDR=127.0.0.1:8080 go run ./cmd/server
```

In another terminal:

```sh
curl --fail http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

`APP_ADDR` is required; see `.env.example`. The process does not automatically load `.env`. Health only reports that HTTP is serving; it makes no DB/IdP/payment-readiness claim. SIGINT/SIGTERM stops the process. No business or login endpoints are exposed.

## Checks and migration boundary

`make ci` checks gofmt, race-enabled Go tests, `go vet`, build, and `make migration-smoke`. GitHub Actions runs these checks on pushes and PRs without deployment permissions.

The migration smoke executes `migrations/test_only/001_smoke.up.sql` in a temporary empty SQLite database, verifies its probe and version ledger, applies the paired `.down.sql`, verifies removal, then reapplies. Its temporary directory is deleted on exit. It does not read a DB URL or touch any external database. Rollback is the paired down migration; `make migration-smoke` verifies the cycle reproducibly.

SQLite is a **test harness only**. No production DB driver, runner, product schema or migration has been selected. Real migrations, locking, transaction failure tests and rollback on the approved engine are required in the owning stage after ADR approval.

## Structure and decisions

- `cmd/server`, `internal/config`, `internal/httpapi`: process configuration and health.
- `internal/authz`: default-deny MOD-08 adapter boundary and scope contracts.
- `internal/contracts`: version and scoped order/payment identity contracts. These do not enforce persistence uniqueness or implement payment processing.
- [ADR-0001](docs/adr/0001-repository-and-go.md): verified repository, Go, branch/review policy and existing-app boundary.
- [ADR-0002](docs/adr/0002-transactional-database.md): proposed DB alternatives and explicit Owner gate.
- [ADR-0003](docs/adr/0003-identity-ui-and-external-gates.md): unresolved IdP/UI and later Stripe gates.
- [Relational contracts](docs/contracts.md): source-of-truth entities and required constraints for later stages.

日本語：Go は Owner 確定。DB/IdP/UI は未選定で、テスト成功は承認や本番認証の証拠ではない。既存 Flutter/Firebase 原価アプリは変更しない。P-75 の受入後、依存関係に従って [P-76](https://linear.app/higashijima-manage-system/issue/P-76) へ進む。レビュー・DB gate が未解決の間は後続実装を開始しない。

繁體中文：Go 已批准，DB／IdP／UI 仍待決。此處沒有改動既有 Flutter/Firebase 成本 App；測試成功不代表架構批准或正式上線。P-75 驗收後才依依賴進入 P-76，未解除 review 與 DB gate 前不開後續實作。每個 PR 回報確切 SHA、CI、review、rollback 與剩餘阻礙，不提前合併或部署。
