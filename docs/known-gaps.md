# Known gaps and demo limits

As of 2026-10-27 JST, main `db13a8e`; MVP done does not mean pilot approval.

## Payments (EXT-1)

- **Owner: P-80 payments/merchant owner.** Real Sandbox evidence covers tenant A paid / declined / 3DS only (2026-10-27 JST). Open: concurrent A+B isolation ([P-84 5a](p84-acceptance-matrix.md)), an actual asynchronous payment method, Checkout expiry and reconciliation, and refund journeys (5i).
- Stripe Checkout custom domains are not accepted: the provider allows exactly `checkout.stripe.com` ([PR #65](https://github.com/nurockplayer/tojima-restaurant-platform/pull/65)). Enabling a custom domain needs an explicit allowlist change.
- Live mode is not authorised; test-mode evidence is not authorisation.
- A lost original Checkout grant has no identity recovery in MVP (D9, [P-84 5h](p84-acceptance-matrix.md)); manager void exists for expired-unpaid orders.
- [`scripts/run-stripe-sandbox.py`](../scripts/run-stripe-sandbox.py) interactive launch from the repo root intermittently reports "Sandbox startup failed" (OSError, undiagnosed); importing the module and calling `run()` from `scripts/` worked. Startup check is unaffected.

## Staff sign-in (ADR-0008)

- Passkey journey F2 is not yet accepted; password + OTP F1 is accepted.
- Back-channel logout is a supplementary feed: a lost notice leaves the session until its 30-day absolute expiry. Operators revoke with `staffadmin revoke-sessions` / `hold`, never by disabling the user in Keycloak.
- `--import-realm` does not update an existing realm; changes on an existing Keycloak database need `kcadm.sh` ([containers](ops/containers.md)).
- Staff sign-in fails closed until OIDC discovery succeeds once per process.
- R01 breached-password screening is PARTIAL (D16); self-service credential changes are disabled for the pilot.

## Demo

- The [remote demo](remote-demo.md) uses the synthetic runtime (token staff login, simulated cash/kitchen, no Stripe, no real IdP); data resets on restart; DEMO/参考価格 labels stay.
- Guest Checkout status polls every 2 s while Checkout is being prepared, then every 20 s ([PR #66](https://github.com/nurockplayer/tojima-restaurant-platform/pull/66)).

## Operations / go-live

See [MVP done vs go-live](mvp-done-vs-go-live.md).

- Hosting provider decision follows 東島さん's feedback on 2026-11-02 JST; ¥0 target; no paid plan without **Owner** approval.
- Compose uses one PostgreSQL superuser; production needs a least-privilege server role.
- Restore drill against real hosting backups, P-84 7f/7g device and assistive-technology checks, retention policy confirmation, and named store/staff owners remain open.
- RPO 5 min / RTO 1 h are future production targets, not pilot requirements.

## 日本語

2026-10-27 JST 時点、main `db13a8e`。MVP 完了は試店承認ではない。

### 決済（EXT-1）

- **担当: P-80 決済／merchant 責任者。** 実 Sandbox 証跡は tenant A の paid / declined / 3DS のみ（2026-10-27 JST）。A+B 同時分離（[P-84 5a](p84-acceptance-matrix.md)）、実際の非同期決済手段、Checkout 期限切れと照合、返金経路（5i）は未完了。
- Stripe Checkout custom domain は未対応。provider は `checkout.stripe.com` のみ許可する（[PR #65](https://github.com/nurockplayer/tojima-restaurant-platform/pull/65)）。有効化には明示的な allowlist 変更が必要。
- live mode は未承認。test mode の証跡は承認ではない。
- 元の Checkout grant を紛失した場合、MVP に identity recovery はない（D9、[P-84 5h](p84-acceptance-matrix.md)）。期限切れ未払注文には manager void がある。
- [`scripts/run-stripe-sandbox.py`](../scripts/run-stripe-sandbox.py) を repo root から対話起動すると断続的に「Sandbox startup failed」（OSError、原因未診断）。`scripts/` から module を import して `run()` を呼ぶ方法は動作した。startup check への影響はない。

### 店員 sign-in（ADR-0008）

- Passkey 経路 F2 は未受入。password + OTP の F1 は受入済み。
- back-channel logout は補助 feed。通知を失うと session は絶対期限の 30 日まで残る。運用者は `staffadmin revoke-sessions` / `hold` で失効し、Keycloak のユーザー無効化では行わない。
- `--import-realm` は既存 realm を更新しない。既存 Keycloak database の realm 変更には `kcadm.sh` が必要（[containers](ops/containers.md)）。
- staff sign-in は process ごとに OIDC discovery が一度成功するまで fail-closed。
- R01 漏えいパスワード検査は PARTIAL（D16）。試店では自己操作の credential 変更を無効化。

### Demo

- [remote demo](remote-demo.md) は synthetic runtime（token staff login、現金／厨房は simulation、Stripe と実 IdP なし）。再起動時にデータ reset、DEMO／参考価格表示は維持。
- Guest Checkout status は準備中 2 秒ごと、その後 20 秒ごとに poll（[PR #66](https://github.com/nurockplayer/tojima-restaurant-platform/pull/66)）。

### 運用／試店開始

[MVP 完了と試店開始](mvp-done-vs-go-live.md)を参照。

- hosting provider は東島さんの 2026-11-02 JST の反応後に決定。¥0 目標、有料 plan は **Owner** 承認なしに使わない。
- Compose は PostgreSQL superuser 1つを使用。本番には最小権限の server role が必要。
- 実 hosting backup の restore drill、P-84 7f/7g の支援技術・実機確認、保存期間方針の確認、店主／店員担当の指名は未完了。
- RPO 5分／RTO 1時間は将来の本番目標であり、試店要件ではない。

## 繁體中文

截至 2026-10-27 JST，main `db13a8e`；MVP 完成不等於試營運核准。

### 付款（EXT-1）

- **負責人：P-80 付款／商戶負責人。** 真實 Sandbox 證據僅涵蓋 tenant A paid / declined / 3DS（2026-10-27 JST）。A+B 並行隔離（[P-84 5a](p84-acceptance-matrix.md)）、實際非同步付款方式、Checkout 到期與對帳、退款流程（5i）仍未完成。
- 不接受 Stripe Checkout custom domain：provider 僅允許 `checkout.stripe.com`（[PR #65](https://github.com/nurockplayer/tojima-restaurant-platform/pull/65)）。啟用 custom domain 須明確修改 allowlist。
- live mode 尚未授權；test mode 證據不等於授權。
- 原始 Checkout grant 遺失時，MVP 無身分復原（D9、[P-84 5h](p84-acceptance-matrix.md)）；已過期未付款訂單可由 manager void。
- 從 repo root 互動啟動 [`scripts/run-stripe-sandbox.py`](../scripts/run-stripe-sandbox.py) 偶爾出現「Sandbox startup failed」（OSError，原因未診斷）；從 `scripts/` import module 並呼叫 `run()` 可運作。startup check 不受影響。

### 員工 sign-in（ADR-0008）

- Passkey 流程 F2 尚未驗收；password + OTP 的 F1 已驗收。
- back-channel logout 是補充 feed：通知遺失時 session 保留至 30 天絕對期限。操作員須以 `staffadmin revoke-sessions` / `hold` 撤銷，切勿只在 Keycloak 停用使用者。
- `--import-realm` 不會更新既有 realm；既有 Keycloak database 的 realm 變更須使用 `kcadm.sh`（[containers](ops/containers.md)）。
- 每個 process 的 OIDC discovery 首次成功前，staff sign-in fail-closed。
- R01 外洩密碼檢查為 PARTIAL（D16）；試營運停用自助憑證變更。

### Demo

- [remote demo](remote-demo.md) 使用 synthetic runtime（token staff login、模擬現金／廚房、無 Stripe、無真實 IdP）；重啟時資料重置；保留 DEMO／参考価格 標示。
- Guest Checkout status 在準備期間每 2 秒 poll，之後每 20 秒（[PR #66](https://github.com/nurockplayer/tojima-restaurant-platform/pull/66)）。

### 營運／試營運

參閱 [MVP 完成與試營運](mvp-done-vs-go-live.md)。

- hosting provider 待東島先生 2026-11-02 JST 回饋後決定；目標 ¥0；未經 **Owner** 核准不使用付費方案。
- Compose 使用單一 PostgreSQL superuser；正式環境需要最小權限 server role。
- 實際 hosting 備份 restore drill、P-84 7f/7g 裝置及輔助科技檢查、保存政策確認、指名店主／店員負責人均未完成。
- RPO 5 分鐘／RTO 1 小時是未來正式環境目標，非試營運要求。
