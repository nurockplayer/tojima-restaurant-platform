# ADR-0003 — MOD-08 Identity / UI / External gates

Date: 2026-10-18 JST. Status: **IdP undecided; guest UI accepted in ADR-0005 / IdP未決・顧客UI承認済み**.

## 日本語

スタッフ identity は MOD-08 共通 adapter に委譲する。MOD-05 専用 user table、パスワード、MFA、Passkey、復旧コード、別ログインを実装しない。S0 は port と deny の契約だけで、実在アカウントの認証は提供しない。顧客 TABLE_JOIN / ORDER_READ / CHECKOUT_RESUME は互いにも staff credential にも代用できない。

P-17/P-58 の最新版（2026-10-18、Step35 まで確認）に基づく vendor 受入項目：個人 Email＋password、任意 native Passkey（Web/Flutter）、招待制と固定 role、管理職/財務 MFA、native 単回復旧コード、即時失効・最新 membership、30日絶対 Session、300秒固定 step-up、安全な Email/認証要素変更、90日一般安全ログ参照。全 ID の停止/復旧は platform-only、Go は不確実な状態で fail-closed。復旧後も旧 Session/token/step-up は復活せず、新しい完全ログインと現在の membership のみ利用する。

既存 Firebase Auth 継続、Supabase Auth、他の managed IdP を比較対象とし、候補や有料 plan が全要件を満たすとはまだ判断しない。資格が実証できなければ候補/plan を再評価し、自作認証で不足を埋めない。S1 の fixture は test-only。IdP 未決だけで QR の契約テストを止めないが、正式 staff 環境を PASS としない。

Web/UI は後続Owner決定でReact＋TypeScript＋Viteを採用した（[ADR-0005](0005-typescript-guest-web.md)）。pnpmとfrozen lockfile CIを使用し、不要なlifecycle scriptを加えない。S0の最小構成からP-78で顧客UIを追加し、既存Flutterの変更は含めない。

Stripe は後続 S5。merchant/Connect の責任、test account、Webhook signing secret、test credentials、live activation は別 gate。S0 は secret を取得・埋込せず、決済 API を呼ばない。顧客 grant TTL、試験店舗・監視/backup の商用受入は未解決。初期税率・丸めは後続の [ADR-0004](0004-japan-tax-calculation.md) で承認済み。

## 繁體中文

員工認證共用 MOD-08，以可替換 adapter 表達邊界；不另建帳號／密碼／MFA／Passkey／復原服務。匿名桌次、個人唯讀訂單與原送單者續付 grant 分離。候選 Firebase、Supabase 或其他代管 IdP，尚未證實任何方案符合所有要求，不能宣稱選型或原生能力已通過。

驗收須包含 Web/Flutter 原生 Passkey、原生一次性 MFA 備援碼、管理高權限 MFA、邀請與固定角色、逐請求現行 membership、撤銷後舊 token 即拒、30天絕對 Session、5分鐘固定 step-up、安全信箱變更與90天安全日誌。平台全帳號安全停用狀態不明即拒；復原不復活舊 token 或已撤銷權限。這些是既有規格的記錄，不是本次擴張實作。

UI stack 與 IdP 由 Owner 分別決策；顧客Web已核准React＋TypeScript＋Vite與pnpm（[ADR-0005](0005-typescript-guest-web.md)），IdP仍待選型。Stripe 測試帳戶／商家與 webhook 機密留 S5 安全設定，live 投產另行批准。具體 guest TTL 與試店條件仍待決；稅務初始規則已由 [ADR-0004](0004-japan-tax-calculation.md) 承接批准。身分測試替身不可帶入正式環境。

Sources: [P-17](https://linear.app/higashijima-manage-system/issue/P-17), [P-58](https://linear.app/higashijima-manage-system/issue/P-58), [P-75](https://linear.app/higashijima-manage-system/issue/P-75).
