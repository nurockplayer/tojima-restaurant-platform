# Container runtime / コンテナ実行環境 / 容器執行環境

Updated 2026-10-26 JST. `cmd/server` is the deployable guest composition.
This change does not approve a hosting vendor, production DB policy, staff IdP
integration or Stripe live activation; [ADR-0002](../adr/0002-transactional-database.md)
and [ADR-0003](../adr/0003-identity-ui-and-external-gates.md) remain binding.

## English

### Image and local stack

The multi-stage `Dockerfile` builds `web/dist` with Node 24.15 and the pinned
pnpm (`--frozen-lockfile`), then static `/server` and `/migrate` binaries with
Go 1.27.1. The distroless final image runs as `nonroot`, with no shell or curl.
`/server` serves the built SPA at `/app/web/dist` and the guest API.

```sh
cp .env.example .env
# Replace POSTGRES_PASSWORD and PIN_PEPPER; review the local example policy.
# Generate PIN_PEPPER with: openssl rand -base64 32
# Never commit .env or print resolved compose config containing secrets.
docker compose up -d --build --wait
docker compose down -v  # deletes local database data
```

Compose starts PostgreSQL 17 (no host DB port, `pgdata` volume), runs `/migrate`
once, then starts `/server` only after migration success. Both binaries receive
the same PG* variables, so passwords need no URL encoding. The app itself does
not migrate, seed tenants/tables, publish menus, enable cash, configure merchants
or create identities. Use separately authorized provisioning; an empty migrated
DB has no usable restaurant. Existing `cmd/guesttest`, `cmd/operationstest` and
`cmd/demogateway` remain unchanged loopback test harnesses.

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
| `APP_HEALTH_ONLY` | Default `false`. Explicit `true` serves only `/healthz`, needs only `APP_ADDR`, and rejects DB/Stripe configuration. No DB never silently becomes a healthy empty application. |

`.env.example` contains **local example policy**, not accepted production TTLs.
ORDER_READ retains the existing Owner-approved 24h default. Health is process
liveness, not DB/IdP/Stripe readiness. `/server -healthcheck` reads only
`APP_ADDR` and requests `/healthz`, so the distroless compose probe still works.
SIGINT/SIGTERM cancels the worker and stops accepting HTTP, allows up to 10s
for requests, joins the worker, then closes the DB pool. Pending payment work remains durable.

### Guest and payment surface

Normal mode mounts the existing table status/join, session status/join, menus,
shared cart/preview/commit, session order list and operations projection,
ORDER_READ status and read-grant APIs under `/v1/guest/`. Existing grants and
tenant/store checks remain authoritative. Cash ordering needs an already
provisioned cash-enabled store; staff settlement is unavailable in this release.

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

**Every method on `/v1/staff`, `/v1/staff/**`, `/v1/admin`, `/v1/admin/**`
returns 403 before touching the DB.** This includes session open/read/close/PIN,
QR mode, table/KDS/review views, cash settings/preview/settle, acceptance and
fulfillment, stop requests, adjustments/refunds/retries, update acknowledgement,
payment read/reconcile/unpaid-void, and all menu/catalog administration and holds.
The shared `httpapi.IdentityResolver` + `authz.Authorizer` interfaces are the
future MOD-08 Keycloak seam; production passes `DenyIdentity` + `DenyAll` and
adds the outer namespace denial. No env flag, bearer fixture or role header can
turn it on. `/staff`, `/kitchen`, `/catalog` can load the SPA shell but cannot
operate their APIs. A provisioned guest journey does not establish staff readiness.

GET/HEAD navigation paths fall back to `index.html`; unknown `/v1` APIs and
missing asset/file paths return 404. Directories are never listed. Dotfiles,
traversal and symlink escapes are denied using `os.Root`. HTML and ordinary
files use `no-cache`; hashed Vite files under `/assets/` use one-year immutable
caching; APIs use `no-store`, with `nosniff` and `no-referrer` headers.

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
`/server` の順で起動する。migrate と server は同じ PG* を受け取り、DB の
ホストポートは公開しない。`.env.example` を `.env` にコピーし、
`POSTGRES_PASSWORD` と `PIN_PEPPER`（`openssl rand -base64 32`）を置換する。
例示 TTL はローカル用で、本番ポリシーの承認ではない。

上の環境変数表が共通仕様。通常起動には `APP_ADDR`、`DATABASE_URL` または
`PGHOST`/`PGDATABASE`/`PGUSER`、全 SESSION/GRANT/PIN 設定、全 PAYMENT 期間が
必須。URL 優先、それ以外は pgx の PG* 設定を使う。接続確認は10秒、pool は
4接続。PIN pepper は32バイト以上の標準 base64 シークレットを再起動後も維持。
サーバーは `.env` を直接読まず、不足・不正設定で起動を拒否し、秘密値を出力
しない。マイグレーションや店舗・メニュー・現金・merchant・identity の作成は
別途承認された provisioning が必要。空 DB に営業可能な店舗は存在しない。

DB なしで health のみ使う場合は `APP_HEALTH_ONLY=true` を明示する。
DB/Stripe 設定との併用は拒否。通常起動が DB 不足で health のみに退行することは
ない。`/healthz` はプロセス生存のみで、DB/IdP/決済 readiness を示さない。
`-healthcheck` は `APP_ADDR` だけで動く。SIGINT/SIGTERM は HTTP を最大10秒
待ち、worker をキャンセル・join してから pool を閉じる。未処理決済は DB に残る。

### 決済・未提供機能

既存の `/v1/guest/` テーブル・セッション参加、メニュー、カート・見積・送信、
注文一覧・operations 表示、ORDER_READ と grant API を提供する。ORDER_READ は
承認済み24時間設定を維持。現金送信は既に設定済みの店舗だけで、スタッフ会計は
利用不可。既存の loopback harness の動作は変更しない。

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

**全メソッドの `/v1/staff`・配下、`/v1/admin`・配下は DB アクセス前に403。**
セッション開始/参照/終了/PIN/QR mode、table/KDS/review、現金設定/会計、注文承認/
調理進捗、停止要求、調整/返金/retry、更新確認、決済参照/照合/未払い無効化、
メニュー/catalog 管理/hold は全て未提供。将来の MOD-08 Keycloak は
`httpapi.IdentityResolver`＋`authz.Authorizer` を実装する。現状は DenyIdentity/
DenyAll と外側の namespace deny を使い、環境変数や fixture token で解除不可。
`/staff`・`/kitchen`・`/catalog` の SPA は読めても操作 API は利用できない。

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
backup、secret 運用、staff IdP、live activation は別 gate。ADR-0002/0003 は維持。

## 繁體中文

### 啟動與設定

`cmd/server` 現在組裝 PostgreSQL 客人 API 與 SPA。映像由 Node 24.15、固定 pnpm
（frozen lockfile）、Go 1.27.1 建置，最後用 distroless/nonroot 執行。Compose
依序啟動 PostgreSQL 17、成功完成 `/migrate`、再啟動 `/server`。migrate/server
取得相同 PG*，DB 不公開宿主機埠。複製 `.env.example` 為 `.env`，替換
`POSTGRES_PASSWORD` 與 `PIN_PEPPER`（`openssl rand -base64 32`）。TTL 範例只供
本機使用，不代表正式政策獲准。

上方環境變數表是共同契約。正常啟動必須有 `APP_ADDR`、`DATABASE_URL` 或
`PGHOST`/`PGDATABASE`/`PGUSER`、完整 SESSION/GRANT/PIN 設定及 PAYMENT 時長。
URL 優先，否則 pgx 使用 PG*；啟動連線檢查限10秒，pool 上限4條。PIN pepper
必須為至少32 bytes 的標準 base64 機密，重啟後保持穩定。程式不自行讀 `.env`；
缺漏或錯誤設定會拒絕啟動，錯誤不輸出機密。Migration、商家/桌位/菜單/現金/
merchant/identity 建置須另有授權流程；空 DB 不會自動產生可營業門店。

無 DB 時須明確設定 `APP_HEALTH_ONLY=true` 才只提供 health；不能混用 DB/Stripe
設定。正常模式缺 DB 不會靜默降為 health-only。`/healthz` 只表示程序存活，不是
DB/IdP/付款 readiness。`-healthcheck` 只讀 `APP_ADDR`。SIGINT/SIGTERM 最多等待
HTTP 10秒，取消並等待 worker 結束，再關閉 pool；待辦付款持久保留於 DB。

### 付款與未開放功能

提供既有 `/v1/guest/` 桌位/桌次加入、菜單、購物車/預覽/送單、訂單清單與
operations 顯示、ORDER_READ 與 grant API。ORDER_READ 維持已批准24小時期限。
現金送單需門店預先啟用現金，員工結帳目前不可用。Loopback harness 行為不變。

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

**所有方法的 `/v1/staff`、其子路徑及 `/v1/admin`、其子路徑在存取 DB 前回403。**
包含桌次開啟/讀取/關閉/PIN/QR mode、table/KDS/review、現金設定/預覽/結帳、
接單/出餐、停單、調整/退款/retry、更新確認、付款讀取/對帳/未付款作廢，以及
全部 menu/catalog 管理與 hold。未來 MOD-08 Keycloak adapter 使用
`httpapi.IdentityResolver`＋`authz.Authorizer`；目前固定 DenyIdentity/DenyAll 加上
外層 namespace deny，環境變數、fixture token、role header 都不能解除。
`/staff`、`/kitchen`、`/catalog` 可載入 SPA 外殼，但無法操作 API。

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
主機/備份、機密管理、staff IdP、live activation 仍是分別的 gate，ADR-0002/0003
持續有效。
