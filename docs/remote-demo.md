# Temporary remote demo / 一時リモートデモ / 臨時遠端示範

An attended, disposable P-85 walkthrough run from the demo director's Mac. The owner can open the Pages URL on their own device. This is not production hosting, current-menu approval, or restaurant operations.

## Architecture

```text
Pages edge (passcode + signed cookie) -> temporary cloudflared URL
  -> per-run gate gateway 127.0.0.1:8090 -> operationstest 127.0.0.1:8082
  -> one random schema in a disposable loopback PostgreSQL database
```

The launcher builds the web app and Go binaries, starts the isolated runtime, gateway, and Quick Tunnel, updates the two per-run Pages secrets, then deploys `deploy/pages-demo` from that directory. It prints the stable Pages guide URL and private synthetic-credentials file path, never file contents. Ctrl-C stops children; `operationstest` receives SIGTERM to drop its schema and mode-`0600` credentials. The temporary directory is removed. Keep the terminal open and the director's Mac awake; `caffeinate -i` is used if present.

## Setup and start

Use the repository-pinned Go/Node versions. Install web dependencies with `pnpm --dir web install --frozen-lockfile`; the launcher installs no tools or packages. Install `cloudflared` (Quick Tunnel needs no login) and authenticate Wrangler. `cloudflared` retains `HOME` and may read `~/.cloudflared`; review its existing config. Create Pages project `tojima-mobile-order-demo` with production branch `main` and no build step. Set `DEMO_PASSCODE` and `DEMO_COOKIE_KEY` to at least 32 random bytes; share the passcode with intended guests only. The launcher rotates `DEMO_ORIGIN` and `DEMO_GATE_SECRET` each run.

Export `CLOUDFLARE_ACCOUNT_ID` with the target Cloudflare account ID (non-secret); the launcher fails before starting work if it is missing or empty, preventing interactive account selection for logins with multiple accounts.

Use only an explicitly disposable PostgreSQL database on a literal loopback IP. The launcher rejects DNS/multi-host URLs and all query parameters except one `sslmode`, and clears inherited `PG*` fallbacks. Never use production, shared business data, or a database tunnel. Set `TEST_DATABASE_URL` in the environment without echoing it, then run:

```sh
sh scripts/run-remote-demo.sh
```

The guide URL defaults to `https://tojima-mobile-order-demo.pages.dev/__demo/guide`; set `TOJIMA_DEMO_PUBLIC_ORIGIN` to another exact HTTPS origin if needed. Open the printed Pages URL and use the preconfigured passcode. The `trycloudflare.com` URL is temporary; do not circulate it as a durable address. Secrets must not appear in command arguments, source control, chat, or logs. The DB URL is read by a local validator and passed to `operationstest`, then removed from unrelated children. The new gate secret is passed only in the gateway process environment and to Wrangler over stdin. Wrangler output and `operationstest` diagnostics are discarded; private readiness/tunnel logs are removed at shutdown.

Every launcher invocation of Wrangler, including secret updates and deployment, forces `WRANGLER_LOG_SANITIZE=true` and `WRANGLER_WRITE_LOGS=false` and unsets inherited `WRANGLER_LOG` / `WRANGLER_LOG_PATH` overrides so gate secrets cannot enter Wrangler's disk debug logs.

## Security and walkthrough boundary

Pages checks the passcode and issues a signed 12-hour cookie. Failed login adds a fixed delay, but there is no global rate limiter. The per-run gate secret is also required at the loopback gateway, so the temporary tunnel URL alone cannot bypass Pages. Trust still rests on the director's Mac and Cloudflare.

The walkthrough covers guest shared-cart changes, synthetic cash orders, staff reception, virtual kitchen display, synthetic cash settlement/table close, and availability pause/restore. Data resets whenever the demo restarts; cash and receipts are synthetic. The app keeps its DEMO / 参考価格 / 現行店内価格未確認 labels. See [the five-step walkthrough](p85-demo.md#walkthrough-and-verification), also presented in the authenticated guide. Stripe, webhooks, refunds/adjustments, catalog publication, price editing, real IdP, receipt printing, production data/hosting, and physical-device QA are out of scope.

## Stop and rollback

Press Ctrl-C and wait for cleanup. Pages remains deployed but fails closed when the local gateway stops. For a longer pause, use the Cloudflare dashboard to restore a prior deployment or disable the project, then remove the per-run `DEMO_ORIGIN` and `DEMO_GATE_SECRET`. To retire the demo, delete the Pages project and demo secrets. The locally installed Wrangler 4.131.1 registers `pages deployment list/create/tail/delete`, but no `pages deployment rollback` command (verified by reading its source without running it). The director can identify the prior deployment with `wrangler pages deployment list --project-name tojima-mobile-order-demo`, then roll back in the dashboard. A rollback may restore older code/configuration; keep the tunnel stopped until the gate is verified.

## 日本語

デモ担当者の Mac から立ち会いのもと起動する P-85 の使い捨てデモです。店舗オーナーは自分の端末から Pages URL を開けます。本番、現行メニューの承認、店舗運用を示すものではありません。Pages edge が passcode を確認し、署名付き12時間 cookie を発行します。失敗時の固定遅延はありますが、全体 rate limit はありません。一時 tunnel を知るだけでは通れず、loopback gateway でも起動ごとの gate secret が必要です。信頼境界は担当者の Mac と Cloudflare です。

固定 Go/Node を使い、事前に `pnpm --dir web install --frozen-lockfile` を実行してください。Quick Tunnel にログインは不要ですが、Wrangler は認証が必要です。`cloudflared` は既存の `~/.cloudflared` 設定を読む場合があるため確認してください。Cloudflare Pages に32バイト以上のランダム `DEMO_PASSCODE` と `DEMO_COOKIE_KEY` を設定し、passcode は招待者だけに共有します。`TEST_DATABASE_URL` は文字通りの loopback IP の破棄可能 DB に限り、起動器は DNS／多ホスト URL と `sslmode` 以外の query を拒否し、`PG*` fallback を消去します。入力を表示せず環境に設定し、`sh scripts/run-remote-demo.sh` を実行します。

ゲスト共用カート、合成現金注文、スタッフ受付、仮想厨房、精算／閉卓、供給停止／復帰のみ確認します。再起動でデータは初期化されます。Stripe、webhook、返金、商品公開／価格編集、実 IdP、印刷、本番、実機 QA は対象外です。Ctrl-C で停止し、schema・権限 `0600` の認証情報と一時ファイルを削除します。長期停止／退役は Cloudflare dashboard で以前のデプロイへ戻すか無効化し、今回の origin/gate secrets を削除してください。退役時は Pages project と demo secrets も削除します。

## 繁體中文

由示範負責人的 Mac 在有人看管下啟動的 P-85 一次性示範；店主可用自己的裝置開啟 Pages 網址。這不是正式託管、現行菜單核准或餐廳營運。Pages edge 驗證 passcode 並簽發12小時簽章 cookie；登入失敗會固定延遲，但沒有全域 rate limit。僅知道臨時 tunnel 網址仍無法繞過 Pages，loopback gateway 也要求每次 gate secret。信任邊界是負責人的 Mac 與 Cloudflare。

使用 repo 固定 Go/Node，先執行 `pnpm --dir web install --frozen-lockfile`。Quick Tunnel 不需登入，Wrangler 須先認證。`cloudflared` 可能讀取 `~/.cloudflared`，請檢查既有設定。在 Pages 設定至少32個隨機位元組的 `DEMO_PASSCODE` 與 `DEMO_COOKIE_KEY`，passcode 只交給受邀者。`TEST_DATABASE_URL` 僅能指向字面 loopback IP 的可丟棄資料庫；啟動器拒絕 DNS／多主機 URL 和除單一 `sslmode` 外的 query，並清除 `PG*` fallback。以不回顯方式設定後執行 `sh scripts/run-remote-demo.sh`。

僅展示訪客共用購物車、合成現金訂單、店員接單、虛擬廚房、結帳／關桌及供應暫停／恢復。重啟會清空資料。Stripe、webhook、退款、商品發布／改價、正式 IdP、列印、正式服務或實體裝置 QA 均不在範圍。按 Ctrl-C 停止並清除 schema、`0600` 憑證及暫存檔。長期暫停或退役時，在 Cloudflare 控制台還原舊部署或停用專案，移除本次 origin/gate secrets；退役時另刪除 Pages 專案與示範 secrets。
