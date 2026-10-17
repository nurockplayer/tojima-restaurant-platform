# P-78 shared cart and atomic order acceptance

Authority: [P-78](https://linear.app/higashijima-manage-system/issue/P-78), P-69/P-70/P-71/P-47 and the latest tax specification. P-77 merged as `909e16f5d217d7767f4274b31d945410b3a33853`; its exact candidate `ec6bfb1df52905d49dfb5ffcebc317f26fcffd48` passed Oracle/Ego-lite and main CI. React + TypeScript + Vite was expressly approved by the Owner; see ADR-0005.

## Transaction contract

Each OPEN table session has one server-owned cart, stable line IDs and an increasing generation/version. ADD/absolute SET/REMOVE requires the current generation/version and a scoped idempotency key. Conflicts return HTTP 409 with a freshly authorized current cart. Retrying the same operation cannot add twice or resurrect a removed line; reusing its key for different input is rejected. Current bounds are 100 lines, quantity 1–99 and 5,000 selected options per cart (and per line), matching the existing catalog option ceiling. Cart-edit bodies allow 12 MiB + 64 KiB to cover worst-case escaped 100-rune Unicode keys; other control requests remain 16 KiB. These bound validation work and do not implement restaurant quantity policy.

Preview is advisory and holds no lock while the guest reads. Commit revalidates the capability, cart, publication, prices/options/availability and dated tax schedule in a short transaction, comparing the preview fingerprint before inserting anything. A unique session/generation creates one immutable order and winning STRIPE/CASH method, clears the consumed cart and advances to the next generation atomically. Another valid guest submitting that generation receives the existing order. No Checkout or cash settlement is created in P-78.

Lock order is company catalog → tenant/store → detached binding/catalog when applicable → tax schedule SHARE → physical table → session/grant → cart → order. Enabled parent locks conflict with normal UPDATE. The tax table lock excludes updates and overlapping schedule INSERTs. Capability expiry is checked with trusted database time again after waits; final quote time is never client-supplied. Existing session close remains default-deny until P-79 supplies its transactional order/payment gates.

The private order records catalog provenance, immutable product/option names and integer prices, quantity, the real tax snapshot record, payment method and DIRECT_SCAN staff-acceptance requirement. Restoring historical tax uses its embedded rules, not the current schedule. Shared customer DTOs omit administrative menu mode/revision, tenant/store IDs, submitter capabilities and payment URLs. All customer monetary fields in P-78 are decimal strings to preserve int64 precision in JavaScript; the existing public menu opts into this representation with `?amounts=string`.

Unavailable or deleted menu selections remain in the cart with captured display names and current unavailability. They cannot commit and must be explicitly changed or removed. P-81 still owns advanced readiness, required-group publication differences and HQ emergency hold; no separate menu model is introduced.

## Validation and test environment

- PostgreSQL tests cover CAS conflicts, idempotency, commit convergence, snapshot persistence/immutability, menu and tax changes, scope/expiry/revocation and migration up/down/reapply. HTTP tests exercise real P-76 grants, exact money beyond 2^53, safe public projections and the DIRECT_SCAN acceptance flag.
- Frontend tests cover authoritative state adoption, slow responses, preview invalidation, retry identity and loading/join rendering. A real built-app run in Ego-lite covers three guest clients; final receipts record actual measured behavior and any browser not exercised.
- Cart synchronization runs only on visible cart pages every 2.5 seconds. Existing-order polling uses 20 seconds; this does not promise subsecond synchronization. Hidden/unmounted pages stop their timers.
- `cmd/guesttest` requires an explicit loopback test database, creates a random schema and seeds only isolated menu/table fixtures. Browser guests obtain real session grants through the normal join API. `cmd/server` keeps its health-only production boundary; no fixture staff authentication is installed.

Run `make test-postgres` and `make web-check`. CI installs only the committed pnpm lock with `--frozen-lockfile`. Oracle/Ego-lite reviews the exact completed commit after integration validation; local audit is not that final verdict.

Rollback is the isolated test schema or paired PostgreSQL migration 004 down. No deployment is part of this package. Remaining production gates are MOD-08 IdP, hosting, final guest TTL policy, Stripe configuration and pilot acceptance; P-79/P-80 own actual receipt/payment boundaries, P-81 owns S6 readiness.

日本語：共有cartをserver CASで保護し、一世代一注文と当時の税込価格・税snapshotを同transactionで固定する。正式決済成功やIdPの受入は宣言しない。

繁中：以伺服器版本與冪等操作保護共編；送單重驗後原子保存唯一訂單與歷史稅額。介面只呈現權威結果，正式付款及部署留後續受入。
