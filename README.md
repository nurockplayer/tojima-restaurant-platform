# Tojima Restaurant Platform / 東島飲食店管理システム

MOD-05 Go modular monolith with PostgreSQL table sessions, published menus, store prices and versioned Japanese tax. P-75 through P-79 are accepted. P-80 adds a keyless Stripe integration boundary with durable attempts, verified webhooks and original-browser Checkout continuation. Actual Stripe Test Mode and production release remain external acceptance gates.

Authority: [P-74 entry](https://linear.app/higashijima-manage-system/issue/P-74), [P-75 scope and acceptance](https://linear.app/higashijima-manage-system/issue/P-75), [Codex engineering handoff v1.0 (JA/ZH)](https://linear.app/higashijima-manage-system/document/mod-05-jazhcodex-工程交接step02-34-實作地圖與驗收-v10-7ce203c6cfb5).

## Local development

Requirements: Go matching `go.mod`, Python 3 with standard-library `sqlite3`, and Make. Database validation also requires a running Docker engine. Go dependencies are pinned in `go.mod`/`go.sum`. Guest web requires Node 24.15.x and the pnpm version pinned in `web/package.json`; its lockfile is under `web/`.

```sh
make test-postgres
APP_HEALTH_ONLY=true APP_ADDR=127.0.0.1:8080 go run ./cmd/server
```

In another terminal:

```sh
curl --fail http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

`APP_ADDR` is required. Normal mode also requires PostgreSQL and explicit guest policy; see [.env.example](.env.example) and the [container runtime guide](docs/ops/containers.md) for the full configuration. `cmd/server` serves the guest API and built SPA, with optional explicit Stripe test mode and a payment worker. All staff/admin API routes return 403 pending the MOD-08 Keycloak adapter. `APP_HEALTH_ONLY=true` explicitly retains DB-free health-only operation. The process does not load `.env` itself; health reports process liveness only. SIGINT/SIGTERM shuts down HTTP and the worker. The separate `cmd/guesttest` below creates disposable local acceptance data.

## Checks and migration boundary

`make test-postgres` starts its own temporary PostgreSQL 17 container on a random loopback port, runs `make ci`, and removes the container. It ignores any caller-supplied DB URL. `make ci` checks gofmt, race-enabled Go tests, `go vet`, build, and the legacy SQLite migration smoke. Without `TEST_DATABASE_URL`, Go integration tests explicitly skip; that result alone is not P-76 acceptance. GitHub Actions provides a disposable PostgreSQL service and requires this variable, with no deployment permissions.

PostgreSQL integration tests create isolated schemas. S1 covers migration up/down/reapply, scoped constraints, concurrent seating/join, all three QR modes, expiry/revocation, PIN brute-force limits, and staff authorization. S2 adds published-menu isolation, draft versions, linked/detached catalogs, store prices, option validation and real tax calculation using dated central rules. Never use a production or shared business database for `TEST_DATABASE_URL`. S3 adds cart CAS/idempotency, commit races, historical snapshots and browser-safe exact money DTOs. S4 adds historical invoice aggregation, cash settlement races, KDS gates and role-checked manual close. S5 adds one scoped payment attempt, bounded same-key retries, raw signed webhook processing, original-browser continuation and payment/KDS review gates. See [P-76](docs/p76-test-scope.md), [P-77](docs/p77-test-scope.md), [P-78](docs/p78-test-scope.md), [P-79](docs/p79-test-scope.md) and [P-80](docs/p80-test-scope.md) test scopes.

The migration smoke executes `migrations/test_only/001_smoke.up.sql` in a temporary empty SQLite database, verifies its probe and version ledger, applies the paired `.down.sql`, verifies removal, then reapplies. Its temporary directory is deleted on exit. It does not read a DB URL or touch any external database. Rollback is the paired down migration; `make migration-smoke` verifies the cycle reproducibly.

SQLite is a **historical S0 test harness only**. Owner approved PostgreSQL test implementation on 2026-10-18. P-76 adds the PostgreSQL driver, transactional migration runner and table-session schema. Production hosting and configuration remain undecided.

## Structure and decisions

- `cmd/server`, `internal/config`, `internal/httpapi`: process configuration and health.
- `internal/authz`: default-deny MOD-08 adapter boundary and scope contracts.
- `internal/tablesession`, `migrations/postgres`: PostgreSQL table/session/grant lifecycle and paired migrations.
- `internal/httpapi/sessions.go`: injected guest/staff JSON API, tested with real PostgreSQL and test-only identity adapters.
- `internal/catalog`, `internal/httpapi/menu.go`: company/store drafts and sealed publications, effective tax-inclusive store prices, option validation and server-resolved dine-in quotes.
- `internal/tax`: exact inclusive JPY calculation, one floor per normalized rate and supplied calculation boundary, effective legal rule reader and persistable historical snapshots.
- `internal/ordering`, `internal/httpapi/ordering.go`: shared cart generations, idempotency and atomic immutable orders with an injected transactional payment policy.
- `internal/operations`, `internal/httpapi/operations.go`: cash settings and settlement, separate payment/fulfillment state, KDS release, staff operations and close gates.
- `internal/payments`, `internal/stripeprovider`, `internal/checkoutproof`: durable scoped attempts and work, test-only Stripe adapter, raw webhook verification and independent browser continuation proof. See [P-80 test scope](docs/p80-test-scope.md) and [provider configuration](docs/p80-stripe-provider.md).
- `web/`: React + TypeScript + Vite guest, staff and kitchen UI, using the same Go REST API.
- `cmd/guesttest`, `internal/testguest`: loopback-only isolated-schema guest acceptance server.
- `cmd/operationstest`, `internal/testoperations`: explicit local cash/KDS acceptance runtime with isolated test credentials; never a production IdP.
- `internal/contracts`: version and scoped order/payment identity contracts. These do not enforce persistence uniqueness or implement payment processing.
- [ADR-0001](docs/adr/0001-repository-and-go.md): verified repository, Go, branch/review policy and existing-app boundary.
- [ADR-0002](docs/adr/0002-transactional-database.md): PostgreSQL test approval and remaining production gates.
- [ADR-0003](docs/adr/0003-identity-ui-and-external-gates.md): unresolved IdP and later Stripe gates; guest UI now accepted in ADR-0005.
- [ADR-0004](docs/adr/0004-japan-tax-calculation.md): approved Japanese tax defaults, effective schedules, rounding and later invoice boundaries.
- [ADR-0005](docs/adr/0005-typescript-guest-web.md): Owner-approved React + TypeScript + Vite, pnpm and guest UI boundary.
- [ADR-0006](docs/adr/0006-frontend-stack.md): MVP frontend stack — adopt-on-first-consumer libraries (React Aria, TanStack Query/Router, React Hook Form), shared HTTP transport, lazy surfaces and the oxlint hooks gate.
- [Relational contracts](docs/contracts.md): source-of-truth entities and required constraints for later stages.

## Local guest acceptance UI

Build the frontend with `make web-check`. In a separate terminal, start a disposable database:

```sh
docker run --rm --name tojima-guest-test -p 127.0.0.1:55432:5432 -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=tojima_test postgres:17
```

Then run the guest acceptance server from this repository:

```sh
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/tojima_test?sslmode=disable' go run ./cmd/guesttest
```

Open `http://127.0.0.1:8081/?table=test-table` in separate browser tabs or clients. Each client enters through the real P-76 DIRECT_SCAN join service and receives its own scoped capability. Fixture TTLs are explicit local values, and no staff identity or payment success is simulated. The server binds only loopback, creates a randomly named schema and drops that schema on normal shutdown. Stop the database with `docker stop tojima-guest-test`. For frontend development, `pnpm --dir web dev` proxies `/v1` to this local server.

## Local staff and kitchen acceptance

With the same disposable database and built frontend, run:

```sh
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/tojima_test?sslmode=disable' go run ./cmd/operationstest -credentials /tmp/tojima-local-staff.json
```

The credentials path must be new and outside `web/dist`; the command creates it
with mode `0600` and removes its own file at shutdown. Open
`http://127.0.0.1:8082/staff` and enter the tenant/store and one test credential
from that file. Use separate tabs for the cashier and kitchen roles, and
`http://127.0.0.1:8082/?table=test-table` for guests. The test store starts with
PAY_LATER cash; a manager test credential can select PREPAID for subsequent
orders. No real cash or Stripe charge is performed. The test adapter qualifies
only the local flow, not production MOD-08 identity. See the
[P-79 boundary](docs/p79-test-scope.md) for receipt and acceptance details.

For a temporary, attended P-85 walkthrough, see the [remote demo guide](docs/remote-demo.md).
Settings and scope decisions accepted by the Owner on 2026-10-19 are in [MVP Owner decisions D1–D11](docs/mvp-owner-decisions.md).

日本語：P-75〜P-79は統合・受入済み。P-80は鍵なしの決済境界を追加する。実Stripe Test Mode・IdP・本番hostingの受入は別であり、既存Flutter/Firebase原価アプリを変更しない。

繁中：P-75至P-79已驗收，P-80加入無金鑰的交易邊界。真實Stripe Test Mode、正式IdP與託管仍有獨立gate；測試成功不代表上線。每包回報確切SHA、CI、獨立審查、rollback與剩餘阻礙。
