# MVP done vs go-live (pilot) / MVP 完了と試店開始 / MVP 完成與試營運

Use these as separate gates. MVP done means engineering is complete enough to demonstrate safely; it does not authorize a real pilot. Go-live requires the Owner-marked decisions and the named operator/store responsibilities below. See [Owner decisions D12–D17](mvp-owner-decisions.md#owner-decisions-2026-10-26-d12d17) and the [deploy runbook](deploy-runbook.md).

## MVP done — engineering complete and demo-able

- [ ] Guest ordering, staff operations, payment state transitions, table lifecycle and tenant/store authorization are integrated and demonstrable with isolated demo/test data.
- [ ] Company/store isolation and payment correctness are demonstrated; no demo identity, credential or test payment can cross into a real store.
- [ ] Guest session/grant/PIN/Checkout/`ORDER_READ` expiry behavior matches adopted D12 values. Record the separately tracked asynchronous Checkout-after-session-expiry gap; demo evidence does not claim this path is covered.
- [ ] IdP outage behavior follows D17: existing sessions use current Tojima PostgreSQL authorization state; new login and expired step-up fail closed; revocation commits synchronously to Tojima DB.
- [ ] Demo environment can be opened by its intended audience with a private demo passcode; it uses non-production data and resets as documented.
- [ ] Engineering records known gaps and demo limits. No pilot approval, live Stripe mode, production SLA or RPO/RTO claim is implied.

## Go-live (pilot) — all required before serving a real store

- [ ] **[Owner]** Confirm final hosting provider after 東島さん feedback on 2026-11-02; confirm fixed monthly cost ¥0 and explicitly approve any exception before enabling a paid plan or billable resource.
- [ ] Provision the chosen host, ingress, DNS/domain and production-like runtime; confirm quotas, charge boundaries, monitoring, access control and restore access. Record named platform operator.
- [ ] Close the separately tracked gap where asynchronous Checkout creation starts after session expiry, and record exact-head acceptance evidence.
- [ ] **[Owner]** Accept the IdP and MOD-08 evidence, including Keycloak R01 breached-password screening closure, staff Passkey/MFA journeys, current membership checks, immediate revocation and recovery behavior.
- [ ] Complete a restore drill against the provisioned hosting backups, with integrity checks and recorded recovery time. RPO 5 minutes / RTO 1 hour remain future production targets, not pilot requirements.
- [ ] Complete P-84 7f assistive-technology journeys and 7g iPhone/iOS Safari plus Android/Chrome device checks. Record device/OS/browser, date with zone and evidence; do not infer PASS from desktop tests.
- [ ] Name the store owner and staff owners for outage stop/switch decisions, cash handling, KDS continuity, order/refund verification, incident reporting and restart authorization. Train each person on the procedures.
- [ ] **[Owner / payments owner]** Complete EXT-1 Sandbox acceptance and separately authorize Stripe live charge mode, account mapping, webhook ownership, reconciliation and refund handling. Live mode is an explicit Owner decision; test-mode evidence is not authorization.
- [ ] **[Owner]** Confirm the retention schedule with a 税理士/社労士 as applicable, approve the operational policy and deletion/hold workflow, and confirm the selected storage can meet it. See [draft retention policy](ops/retention.md); no blanket seven-year period applies.
- [ ] Split database roles: Compose uses one PostgreSQL superuser for migrate and server; production runs the server as a separate least-privilege role (no DDL, no role management).
- [ ] Verify ingress abuse controls, HTTPS/cookies/origin, secret storage/rotation, monitoring/alerts, security-event retention and incident contacts.
- [ ] **[Owner]** Review known gaps and explicitly authorize the pilot start. Keep scope to one store and document stop criteria and escalation contacts.

## 日本語

### MVP 完了（開発完了・デモ可能）

- [ ] 客注文、店員操作、決済状態、卓 lifecycle、tenant/store 認可を統合し、隔離された demo/test データで実演できる。
- [ ] 会社・店舗分離と決済正確性を確認し、demo identity／認証情報／test 決済が実店舗へ混入しない。
- [ ] session/grant/PIN/Checkout/`ORDER_READ` の期限が D12 に一致する。session 失効後の非同期 Checkout 作成 gap を別途記録し、demo 証跡でこの経路まで確認済みとしない。
- [ ] D17 の IdP 障害時動作を満たす。既存 session は Tojima PostgreSQL の現在の認可状態を使い、新規ログインと期限切れ step-up は fail-closed。失効は Tojima DB に同期 commit する。
- [ ] 対象者は非公開 passcode で demo を開ける。production data を使わず、リセット動作を文書化する。
- [ ] 既知 gap と demo の制限を記録する。MVP 完了は試店承認、Stripe live mode、本番 SLA、RPO/RTO の達成を意味しない。

### 試店開始（実店舗で提供する前に完了）

- [ ] **[Owner]** 東島さんの 2026-11-02 の反応後に hosting provider を決定し、固定月額 ¥0 と課金境界を確認する。有料 plan／課金資源は事前に Owner が明示承認する。
- [ ] host、ingress、DNS/domain、本番相当 runtime を用意し、quota、課金境界、監視、アクセス制御、復元権限を確認して platform operator を指名する。
- [ ] session 失効後に非同期 Checkout 作成が始まる別途追跡中の gap を閉じ、exact-head の受入証跡を記録する。
- [ ] **[Owner]** IdP と MOD-08 証跡を受入。Keycloak R01 漏えいパスワード検査、スタッフ Passkey/MFA、現所属確認、即時失効、復旧動作を含む。
- [ ] 配備先 backup から restore drill を行い、整合性確認と復元時間を記録する。RPO 5分／RTO 1時間は将来の本番目標であり、試店要件ではない。
- [ ] P-84 7f 支援技術経路と 7g iPhone/iOS Safari、Android/Chrome 実機確認を完了。端末／OS／browser、zone付き日付、証跡を記録し、desktop 試験を PASS の代用にしない。
- [ ] 障害時の停止・切替、現金、KDS 継続、注文／返金確認、事故報告、営業再開判断の店主・店員担当を実名で決め、手順を訓練する。
- [ ] **[Owner／決済責任者]** EXT-1 Sandbox 受入を完了し、Stripe live charge mode、account 対応、webhook 管理、照合・返金を別途承認する。test mode の証跡は live 承認ではない。
- [ ] **[Owner]** 税理士／社労士等に保存期間案を確認し、運用方針、削除／hold 手順を承認し、選定 storage が要件を満たすことを確認する。[保存期間案](ops/retention.md)を参照。全記録一律7年とはしない。
- [ ] 本番は migrate と runtime の DB role を分け、runtime は最小権限（DDL・role 管理なし）にする。
- [ ] ingress abuse controls、HTTPS/cookie/origin、secret 保存・rotation、監視・alert、安全ログ保存、incident 連絡先を確認する。
- [ ] **[Owner]** 既知 gap を確認して試店開始を明示承認する。1店舗に限定し、中止条件と連絡先を記録する。

## 繁體中文

### MVP 完成（工程完成且可展示）

- [ ] 整合顧客點餐、店員操作、付款狀態轉換、桌次生命週期及 tenant/store 授權，並以隔離的 demo/test 資料展示。
- [ ] 證明公司／店舖隔離與付款正確性；demo 身分、憑證及測試付款不可進入真實店舖。
- [ ] session/grant/PIN/Checkout/`ORDER_READ` 到期行為符合 D12。另行記錄 session 到期後非同步建立 Checkout 的缺口；demo 證據不宣稱此路徑已驗證。
- [ ] IdP 中斷符合 D17：既有 session 使用 Tojima PostgreSQL 的目前授權狀態；新登入及過期 step-up fail-closed；撤銷同步提交 Tojima DB。
- [ ] 目標使用者可用私下提供的 passcode 開啟 demo；使用非正式資料並記錄重置方式。
- [ ] 記錄已知缺口與 demo 限制。MVP 完成不代表核准試營運、Stripe live mode、正式 SLA 或已達成 RPO/RTO。

### 試營運（真實店舖服務前全部完成）

- [ ] **[Owner]** 待東島先生 2026-11-02 回饋後決定 hosting provider，確認固定月費 ¥0 與計費邊界。付費方案或計費資源須事先由 Owner 明確核准。
- [ ] 建置 host、ingress、DNS/domain 及正式相當 runtime；確認 quota、計費邊界、監控、存取控制及還原權限，並指定平台營運者。
- [ ] 關閉另行追蹤的 session 到期後才非同步建立 Checkout 缺口，並記錄 exact-head 驗收證據。
- [ ] **[Owner]** 驗收 IdP 與 MOD-08 證據，包括 Keycloak R01 外洩密碼檢查完成、員工 Passkey/MFA、目前 membership 檢查、即時撤銷及復原行為。
- [ ] 從實際 hosting 備份執行 restore drill，進行完整性檢查並記錄復原時間。RPO 5 分鐘／RTO 1 小時是未來正式環境目標，不是試營運要求。
- [ ] 完成 P-84 7f 輔助科技流程及 7g iPhone/iOS Safari、Android/Chrome 實機測試。記錄裝置／OS/browser、含時區日期及證據；桌面測試不能代替實機 PASS。
- [ ] 指名店主與店員負責故障停止／切換、現金處理、KDS 持續、訂單／退款核對、事件通報及恢復營業核准，並完成流程訓練。
- [ ] **[Owner／付款負責人]** 完成 EXT-1 Sandbox 驗收，另行核准 Stripe live charge mode、account 對應、webhook 管理、對帳與退款。test mode 證據不等於 live 核准。
- [ ] **[Owner]** 請稅理士／社勞士等確認保存期限草案，核准營運政策與刪除／hold 流程，並確認 storage 可符合要求。參閱[保存期限草案](ops/retention.md)；不可一律保存七年。
- [ ] 正式環境須分開 migrate 與 runtime 的 DB 角色，runtime 僅最小權限（不可 DDL、不可管理角色）。
- [ ] 確認 ingress abuse controls、HTTPS/cookie/origin、secret 儲存與輪替、監控／告警、安全日誌保存及事故聯絡人。
- [ ] **[Owner]** 審閱已知缺口並明確核准開始試營運。範圍限一間店，記錄停止條件及升級聯絡人。
