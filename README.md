# Tojima Restaurant Platform / 東島飲食店管理システム

MOD-05 P-75/P-76 foundation and P-77 menu candidate: Go modular monolith with PostgreSQL-backed table sessions, menu publications, store prices and versioned Japanese tax calculation. This is not an implemented ordering system or a production release.

Authority: [P-74 entry](https://linear.app/higashijima-manage-system/issue/P-74), [P-75 scope and acceptance](https://linear.app/higashijima-manage-system/issue/P-75), [Codex engineering handoff v1.0 (JA/ZH)](https://linear.app/higashijima-manage-system/document/mod-05-jazhcodex-工程交接step02-34-實作地圖與驗收-v10-7ce203c6cfb5).

## Local development

Requirements: Go matching `go.mod`, Python 3 with standard-library `sqlite3`, and Make. Database validation also requires a running Docker engine. Go dependencies are pinned in `go.mod`/`go.sum`; there is no frontend package.

```sh
make test-postgres
APP_ADDR=127.0.0.1:8080 go run ./cmd/server
```

In another terminal:

```sh
curl --fail http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

`APP_ADDR` is required; see `.env.example`. The process does not automatically load `.env`. Health only reports that HTTP is serving; it makes no DB/IdP/payment-readiness claim. SIGINT/SIGTERM stops the process. This executable exposes only health. `httpapi.NewSessionHandler` and `httpapi.NewMenuHandler` supply integration-test endpoints with explicit identity/membership adapters; no fixture identity is wired into the executable.

## Checks and migration boundary

`make test-postgres` starts its own temporary PostgreSQL 17 container on a random loopback port, runs `make ci`, and removes the container. It ignores any caller-supplied DB URL. `make ci` checks gofmt, race-enabled Go tests, `go vet`, build, and the legacy SQLite migration smoke. Without `TEST_DATABASE_URL`, Go integration tests explicitly skip; that result alone is not P-76 acceptance. GitHub Actions provides a disposable PostgreSQL service and requires this variable, with no deployment permissions.

PostgreSQL integration tests create isolated schemas. S1 covers migration up/down/reapply, scoped constraints, concurrent seating/join, all three QR modes, expiry/revocation, PIN brute-force limits, and staff authorization. S2 adds published-menu isolation, draft versions, linked/detached catalogs, store prices, option validation and real tax calculation using dated central rules. Never use a production or shared business database for `TEST_DATABASE_URL`. See [P-76 test scope and gates](docs/p76-test-scope.md) and [P-77 scope and S6 deferrals](docs/p77-test-scope.md).

The migration smoke executes `migrations/test_only/001_smoke.up.sql` in a temporary empty SQLite database, verifies its probe and version ledger, applies the paired `.down.sql`, verifies removal, then reapplies. Its temporary directory is deleted on exit. It does not read a DB URL or touch any external database. Rollback is the paired down migration; `make migration-smoke` verifies the cycle reproducibly.

SQLite is a **historical S0 test harness only**. Owner approved PostgreSQL test implementation on 2026-10-18. P-76 adds the PostgreSQL driver, transactional migration runner and table-session schema. Production hosting and configuration remain undecided.

## Structure and decisions

- `cmd/server`, `internal/config`, `internal/httpapi`: process configuration and health.
- `internal/authz`: default-deny MOD-08 adapter boundary and scope contracts.
- `internal/tablesession`, `migrations/postgres`: PostgreSQL table/session/grant lifecycle and paired migrations.
- `internal/httpapi/sessions.go`: injected guest/staff JSON API, tested with real PostgreSQL and test-only identity adapters.
- `internal/catalog`, `internal/httpapi/menu.go`: company/store drafts and sealed publications, effective tax-inclusive store prices, option validation and server-resolved dine-in quotes.
- `internal/tax`: exact inclusive JPY calculation, one floor per normalized rate and supplied calculation boundary, effective legal rule reader and persistable historical snapshots.
- `internal/contracts`: version and scoped order/payment identity contracts. These do not enforce persistence uniqueness or implement payment processing.
- [ADR-0001](docs/adr/0001-repository-and-go.md): verified repository, Go, branch/review policy and existing-app boundary.
- [ADR-0002](docs/adr/0002-transactional-database.md): PostgreSQL test approval and remaining production gates.
- [ADR-0003](docs/adr/0003-identity-ui-and-external-gates.md): unresolved IdP/UI and later Stripe gates.
- [ADR-0004](docs/adr/0004-japan-tax-calculation.md): approved Japanese tax defaults, effective schedules, rounding and later invoice boundaries.
- [Relational contracts](docs/contracts.md): source-of-truth entities and required constraints for later stages.

日本語：Go と PostgreSQL テスト実装は Owner 承認済み。P-75／P-76 は PR #1／#2 で統合・受入済み。P-77 の税込 JPY・内用 10%・税率別一回切捨ては最新 Owner 決定で承認され、実計算と日付付き法定データで検証する。P-78 は P-77 受入後に進める。IdP/UI・本番 hosting は未選定。既存 Flutter/Firebase 原価アプリは変更しない。

繁體中文：Go 與 PostgreSQL 測試實作已批准，P-75／P-76 已整合驗收。P-77 稅務決策已明確，使用真正公式與版本化法定資料；通過本包驗收後依依賴進入 P-78。正式託管／IdP／UI 仍待決，未改動既有 Flutter/Firebase 成本 App；測試成功不代表正式上線。每個 PR 回報確切 SHA、CI、review、rollback 與剩餘阻礙。
