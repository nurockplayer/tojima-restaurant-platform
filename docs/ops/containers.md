# Container runtime / コンテナ実行環境 / 容器執行環境

Updated 2026-10-27 JST. `cmd/server` is the deployable guest composition.
This change does not approve a hosting vendor, production DB policy, staff IdP
integration or Stripe live activation; [ADR-0002](../adr/0002-transactional-database.md)
and [ADR-0003](../adr/0003-identity-ui-and-external-gates.md) remain binding.

## English

### Image and local stack

The multi-stage `Dockerfile` builds `web/dist` with Node 24.15 and the pinned
pnpm (`--frozen-lockfile`), then static `/server`, `/migrate` and `/staffadmin` binaries with
Go 1.27.1. The distroless final image runs as `nonroot`, with no shell or curl.
`/server` serves the built SPA at `/app/web/dist` and the guest API.

```sh
cp .env.example .env
# Replace POSTGRES_PASSWORD, APP_DB_PASSWORD and PIN_PEPPER; review the local policy.
# Generate PIN_PEPPER with: openssl rand -base64 32
# Never commit .env or print resolved compose config containing secrets.
docker compose up -d --build --wait
docker compose down -v  # deletes local database data
```

Compose starts PostgreSQL 17 (no host DB port, `pgdata` volume), runs `/migrate`
once, then starts `/server` only after migration success. `/migrate` keeps the
owner `POSTGRES_USER` credentials; `/server` (and `staffadmin`, which reuses the
same service) connects as the DML-only runtime role via `PGUSER`/`PGPASSWORD`.
Passwords go through libpq variables, so no URL encoding is needed. The app
itself does not migrate, seed tenants/tables, publish menus, enable cash,
configure merchants or create identities. Use separately authorized
provisioning; an empty migrated DB has no usable restaurant. Existing
`cmd/guesttest`, `cmd/operationstest` and `cmd/demogateway` remain unchanged
loopback test harnesses.

- Migrations run as the owner role and, with `APP_DB_ROLE` set, apply
  `GrantRuntime` (DML on tables/sequences, `EXECUTE` on functions, `USAGE` on the
  schema; no DDL, no `TRUNCATE`, no writes to `schema_migrations`). The server
  and `staffadmin` connect as `tojima_app` (`APP_DB_USER`), which is a login role
  created by initdb with no superuser/createdb/createrole and no replication or
  `BYPASSRLS` attributes. Production must keep this split and grant the role
  itself, not a superuser.

The HTTP bind inside the image is `APP_ADDR=0.0.0.0:8080`; compose publishes
`${APP_BIND:-127.0.0.1}:${APP_PORT:-8080}`. TLS/ingress, backups, secret management
and production hosting remain separate deployment work. Compose's
`PGSSLMODE=disable` is for its private local DB network; configure verified DB
TLS for a remote deployment.

### Environment contract

The process reads environment variables only; it does not load `.env` itself.
Compose reads `.env` for interpolation. Missing/invalid required values refuse
startup with a variable-specific error; DSNs, secrets and provider errors are
not printed. No config value enables payments by default.

| Variables | Contract |
| --- | --- |
| `APP_ADDR` | Required `host:port`. |
| `DATABASE_URL` or `PGHOST`, `PGDATABASE`, `PGUSER` | Required in normal mode. A nonempty URL wins; otherwise pgx reads libpq PG* including `PGPORT`, `PGPASSWORD`, `PGSSLMODE`, TLS options. Startup pings the DB with a 10s deadline; the pool is bounded to four connections. Run `/migrate` separately first. |
| `APP_WEB_DIST` | Defaults to `/app/web/dist`; must contain a readable regular `index.html`. |
| `SESSION_TTL`, `GRANT_TTL`, `GRANT_WINDOW`, `PIN_TTL`, `PIN_LOCKOUT` | All required Go durations, at least 1ms. No test-harness TTL defaults. |
| `GRANT_WINDOW_MAX`, `PIN_MAX_ATTEMPTS`, `PIN_LENGTH` | Required integers: 1–10000, 1–20, 4–8 respectively. |
| `PIN_PEPPER` | Required standard base64 of at least 32 secret bytes. Keep stable across restarts; no automatic random replacement. |
| `PAYMENT_CREATE_RETRY_WINDOW`, `PAYMENT_CONTINUATION_TTL`, `PAYMENT_WORK_LEASE_TTL` | Required durations, at least 1ms; create retry must be less than 24h. Required even with Stripe disabled because the shared payment policy is constructed for cash and fail-closed Stripe ordering. |
| `APP_HEALTH_ONLY` | Default `false`. Explicit `true` serves only `/healthz`, needs only `APP_ADDR`, and rejects DB/Stripe/staff OIDC configuration. No DB never silently becomes a healthy empty application. |
| `APP_DB_USER`, `APP_DB_PASSWORD` | Compose interpolation only, not read by the binaries: initdb creates this login role on a fresh volume and the `server` service passes them as `PGUSER`/`PGPASSWORD`. Defaults to `tojima_app`; the password is required. |
| `APP_DB_ROLE` | `cmd/migrate` only. When set, `/migrate` applies `GrantRuntime` to that role after `Up`; unset skips the grants (tests and CI unchanged). Compose sets it from `APP_DB_USER`. |

`.env.example` contains **local example policy**, not accepted production TTLs.
ORDER_READ retains the existing Owner-approved 24h default. Health is process
liveness, not DB/IdP/Stripe readiness. `/server -healthcheck` reads only
`APP_ADDR` and requests `/healthz`, so the distroless compose probe still works.
SIGINT/SIGTERM cancels the worker and stops accepting HTTP, allows up to 10s
for requests, waits up to 15s for the worker, then closes the DB pool. If the
worker misses its deadline, durable work will be reclaimed after its lease.

### Guest and payment surface

Normal mode mounts the existing table status/join, session status/join, menus,
shared cart/preview/commit, session order list and operations projection,
ORDER_READ status and read-grant APIs under `/v1/guest/`. Existing grants and
tenant/store checks remain authoritative. Cash ordering needs an already
provisioned cash-enabled store; staff settlement requires the opt-in staff identity and current membership below.

`STRIPE_MODE` is empty/`disabled` by default: Stripe commits fail closed;
checkout context/status/resume and the webhook route are unmounted, and no
payment worker runs. Supplying payment-specific settings while disabled refuses
startup. To opt into the existing **test-only** Stripe adapter, supply all of:

| Variables | Required test-mode values |
| --- | --- |
| `STRIPE_MODE` | `test`. `live` is explicitly rejected: the current adapter/webhook verifier support test mode only. |
| `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET` | `sk_test_…` and `whsec_…` secrets from env only. Live keys are rejected. |
| `STRIPE_API_VERSION` | `2026-09-30.endive`. |
| `STRIPE_ACCOUNT_ID`, `STRIPE_CREDENTIAL_REF` | Explicit `acct_…` connected account and exact credential reference; no fallback merchant. |
| `APP_PUBLIC_ORIGIN` | HTTPS origin, no path/query/fragment/trailing slash; must match the external request Host and browser Origin. |
| `STRIPE_SUCCESS_URL`, `STRIPE_CANCEL_URL` | Explicit trusted HTTPS return URLs. |
| `STRIPE_MAX_AMOUNT_JPY` | Explicit test cap, 1–10000 JPY. |
| `CHECKOUT_PROOF_TTL`, `CHECKOUT_SIGNING_KEYS` | Positive duration (at least 1ms), and comma-separated standard-base64 secret keys of at least 32 bytes each, current first; previous keys verify during rotation. Preserve across restarts. |

This enables `POST /v1/guest/sessions/{sessionID}/checkout-context`,
`GET /v1/guest/sessions/{sessionID}/orders/{orderID}/checkout`,
`POST /v1/guest/sessions/{sessionID}/orders/{orderID}/checkout/resume`,
`POST /v1/payments/stripe/webhook`, and one 250ms payment worker loop.
`__Host-` Secure/HttpOnly/SameSite=Strict cookies, origin/Host checks and webhook
signature/API-version/test-mode verification stay enabled; no loopback HTTP
exception is used. Put HTTPS termination in front of this HTTP listener.

The resolver only accepts the configured credential reference/account. Existing
`store_stripe_merchants` records must already have the matching enabled mapping,
provider mode `CONNECTED_DIRECT_TEST_FIXTURE`, API version and an approved
webhook endpoint reference. Startup does not write merchant records or contact
Stripe. Changing return URLs, API version or amount cap changes the immutable
provider profile; drain/reconcile existing attempts before such a change.
Test configuration does not authorize live payments or external account changes.

### Staff boundary and SPA behavior

With `STAFF_AUTH_MODE` unset/empty/`disabled`, every method on `/v1/staff`,
`/v1/staff/**`, `/v1/admin` and `/v1/admin/**` returns 403 before DB access,
including encoded namespace variants. `/staff/auth/*` retains SPA fallback.
With `STAFF_AUTH_MODE=oidc`, the explicit OIDC adapter and current-membership
policy enable the staff/operations and menu APIs. The outer session check runs
before resource lookups; unsafe staff/admin methods require the exact configured
Origin. Ambiguous protected paths (escapes, backslashes, dot/repeated separators
or namespace case changes) are rejected with 403, rather than interpreted
differently by nested routers. Resource identifiers keep their original case.
No header or env grants an identity: a verified live DB session is required.
`/staff`, `/kitchen`, `/catalog` can load the SPA shell; the web login UX is PR E.
A provisioned guest journey does not establish staff readiness.

GET/HEAD navigation paths fall back to `index.html`; unknown `/v1` APIs and
missing asset/file paths return 404. Directories are never listed. Dotfiles,
traversal and symlink escapes are denied using `os.Root`. HTML and ordinary
files use `no-cache`; hashed Vite files under `/assets/` use one-year immutable
caching; APIs use `no-store`, with `nosniff` and `no-referrer` headers.

### Staff sign-in (Keycloak)

1. Keep `STAFF_AUTH_MODE=disabled` for the ordinary stack. To enable staff, set
   `STAFF_AUTH_MODE=oidc`, `STAFF_OIDC_CLIENT_ID=tojima-staff`, and the variables
   below in your private `.env`. Generate a distinct DB password, bootstrap admin
   password and client secret; `openssl rand -base64 48` is suitable. Set
   `TOJIMA_STAFF_CLIENT_SECRET` and `STAFF_OIDC_CLIENT_SECRET` to the same secret
   (at least 32 characters). Set `KC_BOOTSTRAP_ADMIN_USERNAME` too. No real
   credentials or imported users belong in the realm JSON.
2. For local Compose, use `KEYCLOAK_PUBLIC_URL=http://127.0.0.1:8081`,
   `APP_PUBLIC_ORIGIN=http://127.0.0.1:8080`,
   `STAFF_OIDC_ISSUER=http://127.0.0.1:8081/realms/tojima`, and
   `STAFF_OIDC_DISCOVERY_URL=http://keycloak:8080/realms/tojima`.
   The issuer must equal the token issuer derived from `KC_HOSTNAME`, which
   Compose sets to `KEYCLOAK_PUBLIC_URL`. **Do not use the internal discovery
   URL as the public issuer.** For HTTPS deployments use the external HTTPS
   origins instead. The issuer accepts HTTP only on `keycloak`, `localhost` or
   `127.0.0.1`; the app origin accepts HTTP only on `127.0.0.1:<port>`.
   Keycloak sends back-channel logout to
   `KEYCLOAK_BACKCHANNEL_LOGOUT_URL=http://server:8080/staff/auth/backchannel-logout`
   over the Compose network. If the BFF runs elsewhere, set this URL to an
   endpoint reachable from the Keycloak container; browser redirects continue
   to use `APP_PUBLIC_ORIGIN`.
   `--import-realm` skips a realm that already exists, so on an existing
   Keycloak database apply the URL (and any other realm change) explicitly:
   `docker compose exec keycloak /opt/keycloak/bin/kcadm.sh` (log in with the
   bootstrap admin, then `update clients/<id> -r tojima -s
   'attributes."backchannel.logout.url"=<url>'`). A back-channel notice the BFF
   cannot apply (503 `logout_not_applied`, logged) is not retried by Keycloak;
   the session would otherwise live to its 30-day absolute expiry, so never use
   Keycloak disable/logout as the revocation step: always run `staffadmin
   revoke-sessions` or `hold`.
3. Run `docker compose --profile staff up -d --build --wait`. The profile adds
   Keycloak 26.8.1 pinned by digest, roughly **700 MB** of memory, a separate
   `keycloak` DB/role in the same PostgreSQL, and a read-only realm import.
   The listener is loopback-only on `${KEYCLOAK_PORT:-8081}`. Keycloak's
   management port 9000 stays private; the healthcheck uses bash `/dev/tcp` and
   an HTTP HEAD request to `/health/ready`, because the image has no curl
   ([Keycloak health documentation](https://www.keycloak.org/observability/health)).
   Secrets are required at Keycloak **container startup**: Compose interpolates
   inactive profiles too, so `${VAR:?…}` would break the default stack.
4. Open `KEYCLOAK_PUBLIC_URL/admin/`, sign in with the bootstrap credentials,
   create a permanent admin and retire the bootstrap admin. Select realm
   `tojima`, configure approved SMTP for verification/invitation, and create
   each invited user with a verified email workflow, temporary password and
   required actions `UPDATE_PASSWORD`, `VERIFY_EMAIL`, and `CONFIGURE_TOTP`
   for manager/admin/finance. Configure factors before granting privileged
   membership. There is no public registration or password reset. Use the
   user's Keycloak subject with the existing [staffadmin CLI](../../cmd/staffadmin/main.go)
   (PR D): `grant`, `revoke`, `revoke-sessions`, `hold`, `restore`, `status`.
   The server image includes `/staffadmin`; use the server service's DB settings
   (`PG*` in this Compose stack, or `DATABASE_URL` when configured on the service):

   ```sh
   docker compose run --rm --no-deps --entrypoint /staffadmin server grant --actor operator --subject KEYCLOAK_SUBJECT --tenant TENANT --store STORE --role STAFF_CASHIER
   ```

   Run this only after PostgreSQL and migrations are ready (`--no-deps` does not
   start them). Replace `grant` and its flags for the other commands.
   Credential/email/factor changes require `hold`, the Keycloak change, then
   `restore`; old sessions never revive.

Private discovery uses Keycloak's
[`KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`](https://www.keycloak.org/server/hostname)
to advertise private token/JWKS endpoints while keeping browser endpoints public.
The optional `STAFF_OIDC_DISCOVERY_URL` is restricted to `http://keycloak:8080`
with the **same realm path**. Go uses `oidc.InsecureIssuerURLContext` solely to
select that discovery address, explicitly checks the discovered issuer against
`STAFF_OIDC_ISSUER`, and leaves ID/logout token issuer/signature/audience checks
on. Omit this override when the public issuer is reachable inside the server
container. No TLS verification is disabled. If discovery fails, the server still
starts and retries after 1, 2, 4, …, 30 seconds. Until discovery has succeeded
once in this process, all staff auth routes, including session inspection and
logout, return no-store 503, and existing staff sessions fail closed: the BFF
cannot yet verify and apply Keycloak back-channel revocations. Received logout
requests are kept in a bounded in-memory queue and verified/applied before any
session becomes usable; transient verification-key or database failures keep
activation blocked and retry. If that queue overflows, the handler stays
unpublished and all staff access remains blocked. Operators must reconcile
undelivered logout notices and revoke affected sessions before restarting the
server, since the pending queue is held in memory. This trades availability for
revocation safety, per Owner rule 2026-10-26 JST. After first discovery, session
resolution and current membership checks use Tojima DB only and survive later
IdP outages. `/healthz` remains liveness.

The explicit `tojima-browser` forms flow has conditional LoA 1 password and
LoA 2 OTP, both max age 0; no Cookie or Passkey alternative. Required actions
are enabled but not defaulted. The realm's default-role composite is empty;
`account` and `account-console` clients are disabled. An unassigned realm-level
`offline_access` role/scope is declared only to suppress Keycloak's automatic
offline default-role grant during import; it is never assigned to `tojima-staff`.
Built-in OIDC scopes are created explicitly via `CreateDefaultClientScopes`;
the staff client's defaults are only `acr`, `basic`, `profile`, `email`, with
no optional scopes or refresh-token use. Realm import runs only for a missing
realm: editing JSON or changing the secret does **not** update an existing realm.
Reconcile changes explicitly through an approved admin procedure; never delete
production data to force a reimport. Verify effective flow/roles/scopes in PR F.

Fresh PostgreSQL volumes run `deploy/postgres/initdb/10-keycloak.sh`; an empty
`KEYCLOAK_DB_PASSWORD` prints a skip message, so the default profile needs no new
secret. Existing volumes never rerun initdb. After setting the password in
`.env`, create the missing role/database once (back up first; omit `CREATE ROLE`
if it already exists and use an approved password rotation instead):

```sh
docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U tojima -d tojima <<'SQL'
\getenv keycloak_password KEYCLOAK_DB_PASSWORD
CREATE ROLE keycloak LOGIN PASSWORD :'keycloak_password';
CREATE DATABASE keycloak OWNER keycloak;
SQL
```

Use your actual `POSTGRES_USER`/`POSTGRES_DB` if changed. Recreate the postgres
container after adding the env value so `\getenv` sees it; this preserves the
volume. The script/SQL never prints the password (do not enable SQL echo or shell
tracing). Runtime and Keycloak DB roles must stay separate in production.

`orb-test --heavy --host -- sh scripts/staff-auth-smoke.sh` generates disposable
secrets in a private temporary `.env`, verifies strong ACR discovery, assigned
client scopes via admin REST (global discovery may still advertise
`offline_access`), an empty default-role composite, the PKCE S256 / `prompt=login`
/ `acr_values=basic` redirect, anonymous staff 403 and wrong-Origin POST 403.
Its trap removes the stack, volumes, local images and temporary credentials,
then reports dangling volumes. This is wiring evidence.

`orb-test --heavy --host -- sh scripts/staff-auth-acceptance.sh` covers ADR-0008
F1 with a disposable real Keycloak and an embedded Python 3 stdlib driver (no
pip or browser). Run only in the heavy window, **weekdays 03:00–15:00 JST**;
allow roughly **1 GB RAM** for the running stack, plus build overhead. It creates
`accept-manager`, enrolls OTP through the `CONFIGURE_TOTP` manual-entry form,
and provisions STORE_MANAGER through the image's `/staffadmin`. Minimal synthetic
tenant/store rows support the read-only operations access check. It prints
`PASS <name>` for password-only login, central MFA denial (ADR decision 5),
OTP step-up and cookie rotation, a second fresh OTP ceremony, prompt-login,
back-channel revocation within 10 seconds, hold/restore without resurrection,
and empty offline sessions (also checking assigned scopes). Failure output names
the step, HTTP status, URL path and form action path, without provider bodies,
query strings or credentials. Times are JST even under `TZ=UTC`.

The private temporary `.env`, random loopback ports, project-specific stack,
volumes and local images are removed by the trap; leftover checks must succeed.
Secure `__Host-` cookies stay enabled. The driver deliberately retains/resends
these and Keycloak cookies over disposable loopback HTTP; it does not prove
browser cookie enforcement. **F2 Passkey still requires a real browser / virtual
authenticator and HTTPS browser acceptance remains a separate gate.**

### Database roles and existing volumes

Compose now splits the database roles. On a fresh volume,
`deploy/postgres/initdb/20-app-role.sh` creates the login role
`${APP_DB_USER:-tojima_app}` (empty `APP_DB_PASSWORD` prints a skip message);
`/migrate` then applies `GrantRuntime` to `APP_DB_ROLE` after every `Up`, so
tables added by later migrations are covered without default privileges. The
server and `staffadmin` connect as that role through `PGUSER`/`PGPASSWORD`.

initdb never reruns on an existing `pgdata` volume, so the role and its grants
must be created once by hand, then applied by rerunning `/migrate` (back up
first; omit `CREATE ROLE` if it already exists and use an approved password
rotation instead):

```sh
docker compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U tojima -d tojima <<'SQL'
\getenv app_password APP_DB_PASSWORD
\getenv app_user APP_DB_USER
CREATE ROLE :"app_user" LOGIN PASSWORD :'app_password' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
SQL
docker compose run --rm migrate
```

Use your actual `POSTGRES_USER`/`POSTGRES_DB`/`APP_DB_USER` if changed. The SQL
never prints the password (do not enable SQL echo or shell tracing). Re-running
`/migrate` is safe: `Up` and `GrantRuntime` are both idempotent.

### Verification

```sh
orb-test --image golang:1.27-bookworm -- make fmt-check vet
orb-test --heavy --image golang:1.27-bookworm -- make test
orb-test --heavy --host -- sh scripts/compose-smoke.sh
```

The smoke script ignores real `.env` and exported Stripe settings, forces
loopback on a temporary port, checks `/healthz`, migration exit/ledger/schema,
seeds a synthetic table in its disposable DB, and reads that known table through
`GET /v1/guest/tables/compose-smoke-locator`. Its trap runs compose `down -v
--rmi local --remove-orphans`, checks project resources, and lists dangling
volumes before/after. The `orb-test` wrapper handles shared build-cache cleanup;
the script itself removes project images, not shared base images. On this Mac,
heavy jobs use the Owner's weekday 03:00–15:00 JST window. CI's container job
builds only; no registry push, deployment or secrets.

## 日本語

### 起動・設定

`cmd/server` は PostgreSQL を使う顧客 API と SPA の本番用組込を提供する。
イメージは Node 24.15＋固定 pnpm（frozen lockfile）、Go 1.27.1 で生成し、
distroless/nonroot で実行。Compose は PostgreSQL 17 → `/migrate` 成功 →
`/server` の順で起動する。migrate は owner の `POSTGRES_USER`、server と
`staffadmin` は DML のみの runtime role（`APP_DB_USER`）で接続し、DB の
ホストポートは公開しない。`.env.example` を `.env` にコピーし、
`POSTGRES_PASSWORD`・`APP_DB_PASSWORD` と `PIN_PEPPER`（`openssl rand -base64 32`）
を置換する。例示 TTL はローカル用で、本番ポリシーの承認ではない。

- migrate は owner role で `GrantRuntime` を `APP_DB_ROLE` に適用し、server/
  `staffadmin` は `tojima_app`（DDL・TRUNCATE・`schema_migrations` 書込みなし）で
  接続する。既存 volume では initdb が再実行されないため、English 節
  「Database roles and existing volumes」の SQL で role を作成してから
  `/migrate` を再実行する。本番もこの分離を維持する。

上の環境変数表が共通仕様。通常起動には `APP_ADDR`、`DATABASE_URL` または
`PGHOST`/`PGDATABASE`/`PGUSER`、全 SESSION/GRANT/PIN 設定、全 PAYMENT 期間が
必須。URL 優先、それ以外は pgx の PG* 設定を使う。接続確認は10秒、pool は
4接続。PIN pepper は32バイト以上の標準 base64 シークレットを再起動後も維持。
サーバーは `.env` を直接読まず、不足・不正設定で起動を拒否し、秘密値を出力
しない。マイグレーションや店舗・メニュー・現金・merchant・identity の作成は
別途承認された provisioning が必要。空 DB に営業可能な店舗は存在しない。

DB なしで health のみ使う場合は `APP_HEALTH_ONLY=true` を明示する。
DB/Stripe/staff OIDC 設定との併用は拒否。通常起動が DB 不足で health のみに退行することは
ない。`/healthz` はプロセス生存のみで、DB/IdP/決済 readiness を示さない。
`-healthcheck` は `APP_ADDR` だけで動く。SIGINT/SIGTERM は HTTP を最大10秒
待ち、worker 終了を最大15秒待ってから pool を閉じる。期限内に終了しない場合も
未処理決済は lease の期限後に DB から再取得される。

### 決済・未提供機能

既存の `/v1/guest/` テーブル・セッション参加、メニュー、カート・見積・送信、
注文一覧・operations 表示、ORDER_READ と grant API を提供する。ORDER_READ は
承認済み24時間設定を維持。現金送信は既に設定済みの店舗だけで、スタッフ会計は下記の staff 認証・権限が
必要。既存の loopback harness の動作は変更しない。

Stripe は既定で無効。無効時は Stripe 送信を拒否し、checkout context/status/
resume・webhook は未マウント、worker も停止。設定の一部だけ渡すと起動を拒否。
有効化は上の全 Stripe 環境変数が必要。`STRIPE_MODE=test`、test key、webhook
secret、固定 API version、account/ref、HTTPS origin/return URL、1–10000 JPY の
上限、proof TTL と32バイト以上の base64 署名鍵（現行を先頭）が必須。
`live` と live key は既存 adapter の制約により拒否する。設定は外部口座操作や
live 決済の許可ではない。

test 有効時は上記4つの checkout/webhook route と250ms worker を使う。
HTTPS、Host/Origin 検証、Secure/HttpOnly/SameSite=Strict の `__Host-` cookie、
webhook 署名/API version/test mode 検証を維持し、loopback HTTP 例外は使わない。
DB に account/ref、`CONNECTED_DIRECT_TEST_FIXTURE`、API version、承認済み
webhook endpoint ref の merchant 設定が必要だが起動時には作らない。URL/version/
金額上限変更は既存 attempt の profile を変えるため、事前に処理・照合する。

`STAFF_AUTH_MODE` が未設定・空・`disabled` の場合、全メソッドの `/v1/staff`・配下、
`/v1/admin`・配下は encoded path を含め DB アクセス前に403。`/staff/auth/*` は
従来通り SPA fallback。有効化は `oidc` の明示設定のみ。検証済み session と毎回の
membership 判定を使い、staff/operations と menu API を提供する。resource lookup
前に session を確認し、unsafe method は設定済み Origin の完全一致が必要。
escape・逆斜線・dot/重複区切り・namespace 大小文字変更で曖昧になる保護 path は403。
resource ID の大小文字は維持。header や env 自体が identity を与えることはない。
SPA のログイン UX は PR E、ブラウザでの実認証受入は PR F。

### Staff sign-in (Keycloak) / スタッフ認証

通常の stack は `STAFF_AUTH_MODE=disabled` のまま。上の英語手順と同じ変数を
private `.env` に設定し、`STAFF_AUTH_MODE=oidc` と
`docker compose --profile staff up -d --build --wait` で有効化する。DB・bootstrap
admin・client に別々の secret を生成（`openssl rand -base64 48`）。2つの client
secret 変数は同一値・32文字以上。Keycloak は固定26.8.1/digest、約700 MB、専用
DB/role、loopback port 8081。管理 port 9000 の ready check は curl を使わず bash
`/dev/tcp`。必須 secret は container 起動時に検証するため既定 profile は追加設定不要。

初回は `KEYCLOAK_PUBLIC_URL/admin/` の bootstrap admin で入り、恒久 admin を作成して
bootstrap を廃止。realm `tojima` に承認済み SMTP を設定し、招待 user に一時 password、
`UPDATE_PASSWORD`・`VERIFY_EMAIL`、管理者/経理には `CONFIGURE_TOTP` を指定する。
factor 設定後に image 内の `/staffadmin` の `grant` で subject と membership を結ぶ。
server service と同じ DB 環境（この Compose では PG*、設定時は `DATABASE_URL`）を使う。
PostgreSQL と migration の完了後に実行する（`--no-deps` は起動しない）。

```sh
docker compose run --rm --no-deps --entrypoint /staffadmin server grant --actor operator --subject KEYCLOAK_SUBJECT --tenant TENANT --store STORE --role STAFF_CASHIER
```

他の command は `grant` と flag を置換する。資格情報・email・factor 変更は必ず `hold` → Keycloak 変更 → `restore`。登録・自己
password reset・account console は無効、default role は空、offline scope は staff
client に付与しない。flow は password LoA 1＋OTP LoA 2（max age 0）、Cookie/Passkey
代替なし。JSON import は realm が無い時のみで、既存 realm/secret は自動更新しない。

local は公開 issuer `http://127.0.0.1:8081/realms/tojima` を `STAFF_OIDC_ISSUER` に設定し、
`KC_HOSTNAME` と一致させる。内部 `http://keycloak:8080/realms/tojima` は
`STAFF_OIDC_DISCOVERY_URL` 専用。dynamic backchannel と discovery URL context は接続先
だけを変え、公開 issuer の完全一致検証を維持。到達可能なら override は不要。
app origin の HTTP 例外は `http://127.0.0.1:<port>` のみ。Keycloak の
`KEYCLOAK_BACKCHANNEL_LOGOUT_URL` は既定で Compose 内の
`http://server:8080/staff/auth/backchannel-logout`。BFF が別の場所なら Keycloak container
から到達可能な URL に変更する（ブラウザ redirect は引き続き `APP_PUBLIC_ORIGIN`）。
既存の Keycloak DB では `--import-realm` が realm を上書きしないため、URL などの realm 変更は
`kcadm.sh` で明示的に適用する。BFF が適用できなかった back-channel 通知（503
`logout_not_applied`、ログ出力）は Keycloak が再送しないので、確実に失効させる場合は
`staffadmin revoke-sessions` / `hold` を使う（未適用なら session は30日の絶対期限まで残る。Keycloak 側の無効化／ログアウトを失効手段にしない）。
IdP 未起動でも server は起動し1秒→30秒で再試行する。初回 discovery 成功前は全 staff auth
route（session 参照と logout を含む）が no-store 503、既存 session も fail-closed。
この間に受信した back-channel logout は bounded memory queue に置き、session を有効化する前に
検証・適用する。検証鍵または DB の一時障害では activation を保留して再試行する。queue overflow
時は handler を未公開のまま保ち、operator が未配信 logout を照合し対象 session を失効するまで
staff access を止める。queue は memory 上にあるため、reconcile 前に server を再起動しない。
back-channel revoke を検証できない間の停止を許容する Owner rule 2026-10-26 JST に基づく。
初回成功後の IdP 障害時は既存 DB session を継続し、新規 login/step-up は失敗閉鎖。

既存 volume は initdb を再実行しない。backup 後、上の一回限りの SQL で専用 role/DB
を作る。`.env` 追加後 postgres container を再作成して env を反映（volume は保持）。
空 password 時は initdb が skip。SQL/trace で password を出力しない。上の staff smoke
は使い捨て secret/DB を使い、ACR・scope・default role・PKCE redirect・匿名/Origin 403
を確認し全資源を削除。

`orb-test --heavy --host -- sh scripts/staff-auth-acceptance.sh` は実 Keycloak と
Python 3 標準 library のみで ADR-0008 F1 を確認する。重い検証は平日03:00–15:00 JST、
実行 stack は約1 GB RAM（build は別途）。使い捨て user を `CONFIGURE_TOTP` の手入力画面で
登録し、image 内 CLI で STORE_MANAGER を付与、最小 tenant/store を SQL で作成。
password のみの login、MFA 必須 role の403、OTP step-up/連続 ceremony/cookie rotation、
prompt-login、10秒以内の back-channel revoke、hold/restore、offline session 不在を確認する。
各項目は `PASS <name>`、失敗は step/HTTP status/path/form action path のみを表示し、
secret・query・response body は出さない。時刻は JST。trap は private `.env`、container、
volume、local image を削除し残存確認。Secure cookie を loopback HTTP 上で手動再送するため、
ブラウザの cookie enforcement は対象外。F2 Passkey（実 browser/virtual authenticator）と
HTTPS ブラウザ受入は引き続き別 gate。

### SPA・検証・境界

`APP_WEB_DIST` は既定 `/app/web/dist`。通常の GET/HEAD ナビゲーションは
index fallback、未知 API・欠落 asset は404。directory listing、dotfile、
traversal、symlink の root 外参照は禁止。HTML/通常ファイルは no-cache、
Vite hash asset は1年 immutable、API は no-store。nosniff/no-referrer を付与。

上の orb-test コマンドで検証する。smoke は実 `.env` と Stripe 設定を使わず、
loopback/一時ポートで health、migration 台帳、使い捨て DB に作ったテーブルの
実 guest API 応答を確認する。trap は `down -v --rmi local --remove-orphans`、
project 残存資源チェック、前後の dangling volume 一覧を実行。共有 build cache
の掃除は orb-test に委ねる。重い検証は平日03:00–15:00 JST。終了時にローカル
DB を削除する場合は `docker compose down -v`。TLS/ingress、本番 DB hosting/
backup、secret 運用、staff のブラウザ認証受入、live activation は別 gate。ADR-0002/0003 は維持。

## 繁體中文

### 啟動與設定

`cmd/server` 現在組裝 PostgreSQL 客人 API 與 SPA。映像由 Node 24.15、固定 pnpm
（frozen lockfile）、Go 1.27.1 建置，最後用 distroless/nonroot 執行。Compose
依序啟動 PostgreSQL 17、成功完成 `/migrate`、再啟動 `/server`。migrate 以 owner
的 `POSTGRES_USER` 連線；server 與 `staffadmin` 以僅 DML 的 runtime role
（`APP_DB_USER`）連線，DB 不公開宿主機埠。複製 `.env.example` 為 `.env`，替換
`POSTGRES_PASSWORD`、`APP_DB_PASSWORD` 與 `PIN_PEPPER`（`openssl rand -base64 32`）。
TTL 範例只供本機使用，不代表正式政策獲准。

- migrate 以 owner role 對 `APP_DB_ROLE` 套用 `GrantRuntime`；server／`staffadmin`
  以 `tojima_app` 連線（無 DDL、不可 TRUNCATE、不可寫 `schema_migrations`）。
  既有 volume 不會重跑 initdb，須先依 English 節「Database roles and existing
  volumes」的 SQL 建立 role，再重跑 `/migrate`。正式環境同樣維持此分離。

上方環境變數表是共同契約。正常啟動必須有 `APP_ADDR`、`DATABASE_URL` 或
`PGHOST`/`PGDATABASE`/`PGUSER`、完整 SESSION/GRANT/PIN 設定及 PAYMENT 時長。
URL 優先，否則 pgx 使用 PG*；啟動連線檢查限10秒，pool 上限4條。PIN pepper
必須為至少32 bytes 的標準 base64 機密，重啟後保持穩定。程式不自行讀 `.env`；
缺漏或錯誤設定會拒絕啟動，錯誤不輸出機密。Migration、商家/桌位/菜單/現金/
merchant/identity 建置須另有授權流程；空 DB 不會自動產生可營業門店。

無 DB 時須明確設定 `APP_HEALTH_ONLY=true` 才只提供 health；不能混用 DB/Stripe/staff OIDC
設定。正常模式缺 DB 不會靜默降為 health-only。`/healthz` 只表示程序存活，不是
DB/IdP/付款 readiness。`-healthcheck` 只讀 `APP_ADDR`。SIGINT/SIGTERM 最多等待
HTTP 10秒，等待 worker 最多15秒後關閉 pool；若 worker 未及時停止，待辦付款會在
lease 到期後由 DB 重新領取。

### 付款與未開放功能

提供既有 `/v1/guest/` 桌位/桌次加入、菜單、購物車/預覽/送單、訂單清單與
operations 顯示、ORDER_READ 與 grant API。ORDER_READ 維持已批准24小時期限。
現金送單需門店預先啟用現金，員工結帳須啟用下方 staff 認證與權限。Loopback harness 行為不變。

Stripe 預設關閉：Stripe 送單拒絕，checkout context/status/resume 與 webhook
未掛載，不啟動 worker。關閉時若傳入付款專屬設定會拒絕啟動。開啟須完整提供
上表全部 Stripe 變數：`STRIPE_MODE=test`、test key/webhook secret、固定 API
version、account/ref、HTTPS origin/return URL、1–10000 JPY 上限、proof TTL、
至少32 bytes base64 簽章金鑰（現行在前，可附舊鍵驗證）。既有 adapter 僅支援
測試，明確拒絕 `live` 與 live key；設定不授權操作外部帳號或啟用真實付款。

Test 模式掛載上述4條 checkout/webhook route 與250ms worker，保留 HTTPS、
Host/Origin 檢查、`__Host-` Secure/HttpOnly/SameSite=Strict cookie，以及 webhook
簽章/API version/test-mode 驗證，不開 loopback HTTP 例外。DB merchant 須已具備
對應 account/ref、`CONNECTED_DIRECT_TEST_FIXTURE`、API version 及獲准 webhook
endpoint ref，啟動不建立它。修改 return URL/version/金額上限會改變既有 attempt
profile，須先排空或對帳。

`STAFF_AUTH_MODE` 未設定、空值或 `disabled` 時，所有方法的 `/v1/staff` 及子路徑、
`/v1/admin` 及子路徑（含編碼變體）皆在 DB 存取前回403；`/staff/auth/*` 維持 SPA
fallback。明確設成 `oidc` 才掛載員工/operations 與 menu API，以已驗證 session 及
每次查詢的 membership 授權。resource lookup 前先驗證 session，unsafe method 必須
精確符合設定的 Origin。含 escape、反斜線、dot/重複分隔符或 namespace 大小寫變化的
歧義保護路徑回403，resource ID 大小寫保持原樣。header/env 本身不授予 identity。
網頁登入 UX 屬 PR E；真實瀏覽器登入驗收屬 PR F。

### Staff sign-in (Keycloak) / 員工登入

一般 stack 維持 `STAFF_AUTH_MODE=disabled`。在 private `.env` 設定與英文步驟相同的
變數，改為 `STAFF_AUTH_MODE=oidc`，執行
`docker compose --profile staff up -d --build --wait`。為 DB、bootstrap admin、client
各別產生 secret（`openssl rand -base64 48`）；兩個 client secret 變數必須相同且至少
32字元。Keycloak 固定26.8.1/digest，約700 MB，獨立 DB/role，僅公開 loopback 8081。
管理 port 9000 以 bash `/dev/tcp` 檢查 ready，映像不含 curl。必填機密於 container 啟動
時驗證，因此預設 profile 不需新增設定。

首次至 `KEYCLOAK_PUBLIC_URL/admin/` 使用 bootstrap admin，建立永久 admin 後停用
bootstrap。於 realm `tojima` 設定核准的 SMTP，邀請使用者並指定 temporary password、
`UPDATE_PASSWORD`、`VERIFY_EMAIL`；經理/管理員/財務另加 `CONFIGURE_TOTP`。先完成
factor，再用映像內 `/staffadmin` 的 `grant` 將 Keycloak subject 對應 membership。
沿用 server service 的 DB 環境（此 Compose 為 PG*；若設定則為 `DATABASE_URL`）。
須先啟動 PostgreSQL 並完成 migration；`--no-deps` 不會啟動它們。

```sh
docker compose run --rm --no-deps --entrypoint /staffadmin server grant --actor operator --subject KEYCLOAK_SUBJECT --tenant TENANT --store STORE --role STAFF_CASHIER
```

其他命令替換 `grant` 及其 flags。修改密碼/email/factor 必須 `hold` → Keycloak 變更 → `restore`，舊 session 不復活。
停用公開註冊、自助 password reset、account console；default role 為空，staff client
不授予 offline scope。明確 flow 使用 password LoA 1＋OTP LoA 2（max age 0），無
Cookie/Passkey 捷徑。JSON 僅在 realm 不存在時匯入，既有 realm/secret 不會自動更新。

local 的公開 issuer `http://127.0.0.1:8081/realms/tojima` 必須設定為 `STAFF_OIDC_ISSUER`，
與 `KC_HOSTNAME` 一致。內部 URL `http://keycloak:8080/realms/tojima` 只用於
`STAFF_OIDC_DISCOVERY_URL`；dynamic backchannel 與 discovery URL context 僅變更連線位址，
仍精確驗證公開 issuer。公開位址可從 server 存取時省略 override。app origin 只允許
`http://127.0.0.1:<port>` 的 HTTP 例外。Keycloak 的
`KEYCLOAK_BACKCHANNEL_LOGOUT_URL` 預設為 Compose 網路中的
`http://server:8080/staff/auth/backchannel-logout`；若 BFF 在其他位置，須改為 Keycloak
container 可到達的 URL（瀏覽器 redirect 仍使用 `APP_PUBLIC_ORIGIN`）。既有 Keycloak DB 上
`--import-realm` 不會覆寫已存在的 realm，URL 等 realm 變更須以 `kcadm.sh` 明確套用。BFF 無法套用的
back-channel 通知（503 `logout_not_applied` 並記錄）Keycloak 不會重送，必須確實撤銷時請用
`staffadmin revoke-sessions` / `hold`（未套用時 session 會存活至 30 天絕對期限；不得以 Keycloak 停用／登出作為撤銷手段）。IdP 停機時 server
仍可啟動並每1秒至30秒重試。此程序首次 discovery 成功前，所有 staff auth route（含 session
查詢與 logout）回 no-store 503，既有 session 也 fail-closed。期間收到的 back-channel logout
會放入有界記憶體佇列，在任何 session 恢復使用前先驗證並套用；驗證鍵或 DB 暫時失敗時會阻止
activation 並重試。佇列溢位時 handler 保持未公開、staff access 持續封鎖。operator 須先核對未送達
的 logout 並撤銷受影響 session，再重啟 server；pending queue 存在記憶體中，核對前不可重啟。
依 Owner rule 2026-10-26 JST，以可用性換取無法驗證 back-channel revoke 時的安全。
首次成功後，後續 IdP 停機仍沿用 DB session，新 login/step-up 失敗關閉。

既有 volume 不重跑 initdb。先備份，再執行上方一次性 SQL 建立獨立 role/DB；新增 `.env`
後須重建 postgres container 以載入 env，保留 volume。空密碼時 initdb 會 skip，不可用
SQL echo/shell trace 輸出密碼。staff smoke 使用臨時機密及 DB，檢查 ACR、scope、空
預設角色、PKCE redirect、匿名/Origin 403 後清理資源。

`orb-test --heavy --host -- sh scripts/staff-auth-acceptance.sh` 以真實 Keycloak 與
Python 3 標準函式庫驗證 ADR-0008 F1，不需 pip 或瀏覽器。重型時段限平日03:00–15:00 JST，
執行中的 stack 約1 GB RAM，另需建置空間。以 `CONFIGURE_TOTP` 手動輸入頁註冊一次性使用者，
透過映像 CLI 授予 STORE_MANAGER，SQL 僅建立最少 tenant/store。確認 password-only login、
MFA 角色403、OTP step-up/連續 ceremony/cookie rotation、prompt-login、10秒內 back-channel
撤銷、hold/restore 舊 session 不復活、offline session 為空。各項輸出 `PASS <name>`；
失敗僅顯示 step、HTTP status、path、form action path，不輸出機密、query 或 response body。
時間一律 JST。trap 刪除 private `.env`、container、volume、local image 並確認無殘留。
Secure cookie 於 loopback HTTP 手動重送，不驗證瀏覽器 cookie enforcement。
F2 Passkey（真實瀏覽器/virtual authenticator）及 HTTPS 瀏覽器驗收仍為獨立 gate。

### SPA、驗證與邊界

`APP_WEB_DIST` 預設 `/app/web/dist`。GET/HEAD 導覽走 index fallback；未知 API、
缺漏 asset 回404。禁止目錄清單、dotfile、traversal、symlink 逃出 root。HTML/
一般檔案用 no-cache，Vite hash asset 用一年 immutable，API 用 no-store，並加上
nosniff/no-referrer。

使用上方 orb-test 指令驗證。smoke 不讀真實 `.env` 或付款設定，強制 loopback/
臨時埠，檢查 health、migration 台帳，再於一次性 DB 建立合成桌位並用真正 guest
API 讀取。trap 執行 `down -v --rmi local --remove-orphans`、專案資源殘留檢查，
列出前後 dangling volumes；共用 build cache 由 orb-test 清理。重型驗證限平日
03:00–15:00 JST。刪除本機資料用 `docker compose down -v`。TLS/ingress、正式 DB
主機/備份、機密管理、staff 瀏覽器認證驗收、live activation 仍是分別的 gate，ADR-0002/0003
持續有效。
