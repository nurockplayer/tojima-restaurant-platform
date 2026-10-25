# Container images and local compose / コンテナイメージとローカル compose / 容器映像與本機 compose

Added 2026-10-26 JST. This page covers the `Dockerfile`, `compose.yaml` and
`scripts/compose-smoke.sh` at the repository root. It is an MVP for moving the
platform between hosts; hosting provider, TLS and production composition are
still Owner decisions (see [deploy runbook](../deploy-runbook.md)).
2026-10-26 JST 追加。リポジトリ直下の `Dockerfile`、`compose.yaml`、
`scripts/compose-smoke.sh` の説明。ホスト間の移動を可能にする MVP であり、
ホスティング事業者・TLS・本番構成は未決定（Owner 判断）。
2026-10-26 JST 新增。說明 repo 根目錄的 `Dockerfile`、`compose.yaml` 與
`scripts/compose-smoke.sh`；這是讓平台可搬移主機的 MVP，主機商、TLS 與正式
組裝仍待 Owner 決定。

## English

### What the image contains

- Stage `web`: Node 24.15 + the pnpm version pinned in `web/package.json`
  (corepack, `--frozen-lockfile`) builds `web/dist`.
- Stage `build`: `golang:1.27.1-bookworm` (matching `go.mod`) builds two static
  binaries with `CGO_ENABLED=0 -trimpath`: `/server` (`cmd/server`) and
  `/migrate` (`cmd/migrate`).
- Final stage: `gcr.io/distroless/static-debian12:nonroot`, runtime user
  `nonroot`. No shell, package manager or `curl`.
- Paths: `/server`, `/migrate`, and the built SPA at `/app/web/dist`. The
  current health-only server does not serve `/app/web/dist` yet; it is baked in
  so a later composition can serve it without rebuilding.
- Defaults: `APP_ADDR=0.0.0.0:8080`, `EXPOSE 8080`.

### Build and run

```sh
docker build -t tojima-server:local .
cp .env.example .env      # then replace POSTGRES_PASSWORD
docker compose up -d --build --wait
docker compose down -v    # also removes the pgdata volume
```

`compose.yaml` runs three services:

| Service | Purpose |
| --- | --- |
| `postgres` | Official `postgres:17`, healthcheck `pg_isready`, no host port. Data in the `pgdata` volume. |
| `migrate` | One-shot `/migrate`; applies `migrations/postgres` through the same exported runner (`postgres.Up`) as the test scripts, so the `schema_migrations` ledger and advisory lock match. Exits 0 before `server` starts. Re-running it is a no-op. |
| `server` | Health-only `cmd/server`. Published on `${APP_BIND:-127.0.0.1}:${APP_PORT:-8080}`. Healthcheck runs `/server -healthcheck`, which requests `/healthz` itself because the image has no shell. |

`docker compose` reads `.env` for interpolation. Nothing in `compose.yaml` is a
secret; the password placeholder lives in `.env.example` and `.env` is
git-ignored. The migrate service receives the password as `PGPASSWORD` (not
inside a URL), so any character works without percent-encoding. Per the current
scope, the server container does not connect to the database yet, so its only
configuration is `APP_ADDR`.

### Smoke test

```sh
sh scripts/compose-smoke.sh
```

It builds the stack under a unique project name (`tojima-smoke-<pid>`), waits for
`/healthz`, checks that `migrate` exited 0 and that `schema_migrations` holds one
row per `migrations/postgres/*.up.sql` file with `table_sessions` present, then
always runs `docker compose down -v --rmi local` (trap) and fails if any
container, volume or image of that project survives. Base images (`postgres:17`,
Go/Node builders, distroless) and the BuildKit cache are host-wide and stay.
It reads no real `.env` (`--env-file /dev/null`), publishes the server on a
temporary port and forces `APP_BIND=127.0.0.1`.

On this Mac, run it through the host queue as required by the Owner rule:

```sh
orb-test --heavy --host -- sh scripts/compose-smoke.sh
```

### CI

The `container` job in `.github/workflows/ci.yml` only runs `docker build`. It
uses no registry, no push and no secrets.

### Not included yet

- Real staff identity / IdP (ADR-0003 is still open) and any authentication
  inside the container.
- The payment worker composition and the guest/staff HTTP handlers: `cmd/server`
  is health-only, and `cmd/guesttest` / `cmd/operationstest` / `cmd/demogateway`
  stay loopback-only test harnesses and are deliberately not containerized.
- Serving `web/dist` over HTTP, TLS/ingress termination, and any reverse proxy.
- Production database provisioning, backups/restore in containers, secret
  management, and multi-node or k3s scheduling.

## 日本語

### イメージの内容

- `web` ステージ: Node 24.15 と `web/package.json` で固定した pnpm（corepack、
  `--frozen-lockfile`）で `web/dist` を生成。
- `build` ステージ: `golang:1.27.1-bookworm`（`go.mod` と一致）で
  `CGO_ENABLED=0 -trimpath` の静的バイナリ `/server`（`cmd/server`）と
  `/migrate`（`cmd/migrate`）を生成。
- 最終ステージ: `gcr.io/distroless/static-debian12:nonroot`、実行ユーザーは
  `nonroot`。シェル・パッケージマネージャ・`curl` なし。
- パス: `/server`、`/migrate`、ビルド済み SPA は `/app/web/dist`。現行の
  health 専用サーバーはまだ配信しないが、再ビルドなしで後から配信できるよう
  同梱している。
- 既定値: `APP_ADDR=0.0.0.0:8080`、`EXPOSE 8080`。

### ビルドと起動

```sh
docker build -t tojima-server:local .
cp .env.example .env      # POSTGRES_PASSWORD を置き換える
docker compose up -d --build --wait
docker compose down -v
```

`compose.yaml` の 3 サービス:

| サービス | 役割 |
| --- | --- |
| `postgres` | 公式 `postgres:17`、`pg_isready` healthcheck、ホストポート公開なし。データは `pgdata` ボリューム。 |
| `migrate` | 一回限りの `/migrate`。テスト用スクリプトと同じ公開ランナー（`postgres.Up`）で `migrations/postgres` を適用するため、`schema_migrations` 台帳と advisory lock の挙動が一致する。成功後に終了し、その後 `server` が起動。再実行しても差分なし。 |
| `server` | health 専用 `cmd/server`。`${APP_BIND:-127.0.0.1}:${APP_PORT:-8080}` で公開。イメージにシェルがないため healthcheck は `/server -healthcheck` が自身で `/healthz` を叩く。 |

`docker compose` は補間のために `.env` を読む。`compose.yaml` に秘密情報は
なく、パスワードのプレースホルダは `.env.example`、`.env` は git 管理外。
migrate サービスはパスワードを URL ではなく `PGPASSWORD` で受け取るため、
任意の文字をパーセントエンコードなしで使える。

### スモークテスト

```sh
sh scripts/compose-smoke.sh
```

一意なプロジェクト名でスタックを起動し、`/healthz`、`migrate` の終了コード 0、
`schema_migrations` の行数（`migrations/postgres/*.up.sql` と同数）と
`table_sessions` の存在を確認した後、必ず `docker compose down -v --rmi local`
（trap）を実行し、そのプロジェクトのコンテナ・ボリューム・イメージが残って
いれば失敗する。ベースイメージ（`postgres:17`、Go/Node ビルド、distroless）と
BuildKit キャッシュはホスト共通のため残る。実際の `.env` は読まず
（`--env-file /dev/null`）、サーバーは一時ポートかつ `APP_BIND=127.0.0.1` で公開する。

Owner ルールに従い、この Mac ではホストのキュー経由で実行する:

```sh
orb-test --heavy --host -- sh scripts/compose-smoke.sh
```

### 未対応

- 実スタッフ IdP（ADR-0003 未決）とコンテナ内の認証。
- 決済ワーカーの構成と客・店員 HTTP ハンドラの組込。`cmd/server` は health
  のみ、`cmd/guesttest` / `cmd/operationstest` / `cmd/demogateway` は
  loopback 専用テスト用で、意図的にコンテナ化しない。
- `web/dist` の配信、TLS/イングレス終端、リバースプロキシ。
- 本番 DB の用意、コンテナ内バックアップ/復元、シークレット管理、
  マルチノードや k3s スケジューリング。

## 繁體中文

### 映像內容

- `web` 階段：Node 24.15 加上 `web/package.json` 固定的 pnpm（corepack、
  `--frozen-lockfile`），產出 `web/dist`。
- `build` 階段：`golang:1.27.1-bookworm`（與 `go.mod` 一致）以
  `CGO_ENABLED=0 -trimpath` 產生靜態執行檔 `/server`（`cmd/server`）與
  `/migrate`（`cmd/migrate`）。
- 最終階段：`gcr.io/distroless/static-debian12:nonroot`，執行身分
  `nonroot`；沒有 shell、套件管理員或 `curl`。
- 路徑：`/server`、`/migrate`，建置好的 SPA 在 `/app/web/dist`。目前的
  health-only 伺服器尚未提供它，先放進映像，之後組裝時不必重建。
- 預設：`APP_ADDR=0.0.0.0:8080`、`EXPOSE 8080`。

### 建置與啟動

```sh
docker build -t tojima-server:local .
cp .env.example .env      # 換掉 POSTGRES_PASSWORD
docker compose up -d --build --wait
docker compose down -v
```

`compose.yaml` 三個服務：

| 服務 | 用途 |
| --- | --- |
| `postgres` | 官方 `postgres:17`、`pg_isready` healthcheck、不對外開宿主機埠；資料在 `pgdata` volume。 |
| `migrate` | 一次性的 `/migrate`，用與測試腳本相同的匯出 runner（`postgres.Up`）套用 `migrations/postgres`，因此 `schema_migrations` 帳本與 advisory lock 行為一致；成功退出後 `server` 才啟動，重跑不會有變化。 |
| `server` | health-only 的 `cmd/server`，發佈在 `${APP_BIND:-127.0.0.1}:${APP_PORT:-8080}`。映像沒有 shell，所以 healthcheck 由 `/server -healthcheck` 自己請求 `/healthz`。 |

`docker compose` 會讀 `.env` 做變數替換；`compose.yaml` 不含任何機密，密碼
佔位只出現在 `.env.example`，`.env` 已被 git 忽略。migrate 服務以 `PGPASSWORD`
（不是 URL）接收密碼，任何字元都不必做百分比編碼。

### 冒煙測試

```sh
sh scripts/compose-smoke.sh
```

用唯一專案名稱（`tojima-smoke-<pid>`）建置並啟動，確認 `/healthz`、
`migrate` 退出碼 0、`schema_migrations` 列數等於 `migrations/postgres/*.up.sql`
檔數且 `table_sessions` 存在，最後一定執行 `docker compose down -v --rmi local`
（trap），並在該專案的容器、volume 或映像仍有殘留時失敗。基底映像
（`postgres:17`、Go/Node 建置映像、distroless）與 BuildKit 快取屬主機共用，
會保留。它不讀真正的 `.env`（`--env-file /dev/null`），伺服器用臨時埠且強制
`APP_BIND=127.0.0.1` 發佈。

依 Owner 規則，在這台 Mac 上要經主機佇列執行：

```sh
orb-test --heavy --host -- sh scripts/compose-smoke.sh
```

### 尚未包含

- 真實員工 IdP（ADR-0003 未決）與容器內認證。
- 付款 worker 組裝與客人／員工 HTTP handler：`cmd/server` 只有 health，
  `cmd/guesttest` / `cmd/operationstest` / `cmd/demogateway` 是僅限 loopback
  的測試工具，刻意不容器化。
- 提供 `web/dist`、TLS／ingress 終止與反向代理。
- 正式資料庫建置、容器內備份／還原、機密管理、多節點或 k3s 排程。
