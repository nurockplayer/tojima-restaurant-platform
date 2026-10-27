# MVP done vs go-live (pilot) / MVP 完了と試店開始 / MVP 完成與試營運

Use these as separate gates. MVP done means engineering is complete enough to demonstrate safely; it does not authorize a real pilot. Go-live requires the Owner-marked decisions and the named operator/store responsibilities below. See [Owner decisions D12–D17](mvp-owner-decisions.md#owner-decisions-2026-10-26-d12d17) and the [deploy runbook](deploy-runbook.md).

## MVP done — engineering complete and demo-able

- [x] Guest ordering, staff operations, payment state transitions, table lifecycle and tenant/store authorization are integrated and demonstrable with isolated demo/test data. Evidence: [P-84 matrix](p84-acceptance-matrix.md), [remote demo](remote-demo.md), [P-85 demo](p85-demo.md), and tenant A real Stripe Sandbox journeys on 2026-10-27 JST (P-84 5i; [PR #64](https://github.com/nurockplayer/tojima-restaurant-platform/pull/64), [#65](https://github.com/nurockplayer/tojima-restaurant-platform/pull/65), [#66](https://github.com/nurockplayer/tojima-restaurant-platform/pull/66)).
- [ ] Company/store isolation and payment correctness are partially demonstrated: integration tests cover isolation and payment transitions ([P-84 5b–5g](p84-acceptance-matrix.md)); tenant A paid/declined/3DS passed in real Sandbox on 2026-10-27 JST; concurrent two-merchant isolation (5a) and async/expiry/refund journeys (5i) remain open (EXT-1).
- [x] Guest session/grant/PIN/Checkout/`ORDER_READ` expiry behavior matches D12. Evidence: [PR #51](https://github.com/nurockplayer/tojima-restaurant-platform/pull/51) TTL verification and [PR #54](https://github.com/nurockplayer/tojima-restaurant-platform/pull/54) dispatch fence closing Checkout after session expiry (migration 016, [deploy runbook](deploy-runbook.md)).
- [x] IdP outage behavior follows D17. Evidence: [ADR-0008](adr/0008-staff-sign-in.md) D17 and Consequences, PRs [#58](https://github.com/nurockplayer/tojima-restaurant-platform/pull/58), [#59](https://github.com/nurockplayer/tojima-restaurant-platform/pull/59), [#60](https://github.com/nurockplayer/tojima-restaurant-platform/pull/60), [#61](https://github.com/nurockplayer/tojima-restaurant-platform/pull/61), [#62](https://github.com/nurockplayer/tojima-restaurant-platform/pull/62), [#63](https://github.com/nurockplayer/tojima-restaurant-platform/pull/63), and [staff auth acceptance](../scripts/staff-auth-acceptance.sh): real Keycloak login, OTP step-up, back-channel and hold/restore, 8/8 PASS on 2026-10-27 10:40 JST. Before first discovery in a process, staff access fails closed, trading availability for revocation.
- [x] The intended audience can open the passcode demo. Evidence: [remote demo](remote-demo.md) Pages gate and reset on restart; it uses a synthetic local runtime, not real staff sign-in or Stripe.
- [x] Engineering records [known gaps and demo limits](known-gaps.md). No pilot approval, live Stripe mode, production SLA or RPO/RTO claim is implied.

## Go-live (pilot) — all required before serving a real store

- [ ] **[Owner]** Confirm final hosting provider after 東島さん feedback on 2026-11-02; confirm fixed monthly cost ¥0 and explicitly approve any exception before enabling a paid plan or billable resource.
- [ ] Provision the chosen host, ingress, DNS/domain and production-like runtime; confirm quotas, charge boundaries, monitoring, access control and restore access. Record named platform operator.
- [ ] Close the separately tracked gap where asynchronous Checkout creation starts after session expiry, and record exact-head acceptance evidence — closed by PR #54 (2026-10-26 JST); exact-head acceptance evidence still to record.
- [ ] **[Owner]** Accept the IdP and MOD-08 evidence, including Keycloak R01 breached-password screening closure, staff Passkey/MFA journeys, current membership checks, immediate revocation and recovery behavior.
- [ ] Complete a restore drill against the provisioned hosting backups, with integrity checks and recorded recovery time. RPO 5 minutes / RTO 1 hour remain future production targets, not pilot requirements.
- [ ] Complete P-84 7f assistive-technology journeys and 7g iPhone/iOS Safari plus Android/Chrome device checks. Record device/OS/browser, date with zone and evidence; do not infer PASS from desktop tests.
- [ ] Name the store owner and staff owners for outage stop/switch decisions, cash handling, KDS continuity, order/refund verification, incident reporting and restart authorization. Train each person on the procedures.
- [ ] **[Owner / payments owner]** Complete EXT-1 Sandbox acceptance and separately authorize Stripe live charge mode, account mapping, webhook ownership, reconciliation and refund handling. Live mode is an explicit Owner decision; test-mode evidence is not authorization.
- [ ] **[Owner]** Confirm the retention schedule with a 税理士/社労士 as applicable, approve the operational policy and deletion/hold workflow, and confirm the selected storage can meet it. See [draft retention policy](ops/retention.md); no blanket seven-year period applies.
- [ ] Split database roles: Compose now runs migrate as the owner role, applies `GrantRuntime` to `APP_DB_ROLE` and starts server/`staffadmin` as the DML-only `tojima_app` role. Production must still create that role, apply the same split and verify it on its host before go-live.
- [ ] Verify ingress abuse controls, HTTPS/cookies/origin, secret storage/rotation, monitoring/alerts, security-event retention and incident contacts.
- [ ] **[Owner]** Review known gaps and explicitly authorize the pilot start. Keep scope to one store and document stop criteria and escalation contacts.

## 日本語

### MVP 完了（開発完了・デモ可能）

- [x] 客注文、店員操作、決済状態、卓 lifecycle、tenant/store 認可を統合し、隔離された demo/test データで実演できる。証跡: [P-84 matrix](p84-acceptance-matrix.md)、[remote demo](remote-demo.md)、[P-85 demo](p85-demo.md)、2026-10-27 JST の tenant A 実 Stripe Sandbox 経路（P-84 5i、[PR #64](https://github.com/nurockplayer/tojima-restaurant-platform/pull/64)、[#65](https://github.com/nurockplayer/tojima-restaurant-platform/pull/65)、[#66](https://github.com/nurockplayer/tojima-restaurant-platform/pull/66)）。
- [ ] 会社・店舗分離と決済正確性は一部確認済み。統合テストで分離と決済遷移を確認（[P-84 5b–5g](p84-acceptance-matrix.md)）、tenant A の paid/declined/3DS は実 Sandbox で 2026-10-27 JST に PASS。2 merchant 同時分離（5a）と async/expiry/refund（5i）は未完了（EXT-1）。
- [x] session/grant/PIN/Checkout/`ORDER_READ` の期限は D12 に一致する。証跡: [PR #51](https://github.com/nurockplayer/tojima-restaurant-platform/pull/51) TTL 検証、[PR #54](https://github.com/nurockplayer/tojima-restaurant-platform/pull/54) の dispatch fence で session 失効後の Checkout 作成 gap を解消（migration 016、[deploy runbook](deploy-runbook.md)）。
- [x] IdP 障害時動作は D17 に従う。証跡: [ADR-0008](adr/0008-staff-sign-in.md) D17 と Consequences、PR [#58](https://github.com/nurockplayer/tojima-restaurant-platform/pull/58), [#59](https://github.com/nurockplayer/tojima-restaurant-platform/pull/59), [#60](https://github.com/nurockplayer/tojima-restaurant-platform/pull/60), [#61](https://github.com/nurockplayer/tojima-restaurant-platform/pull/61), [#62](https://github.com/nurockplayer/tojima-restaurant-platform/pull/62), [#63](https://github.com/nurockplayer/tojima-restaurant-platform/pull/63)、[staff auth acceptance](../scripts/staff-auth-acceptance.sh) の実 Keycloak login・OTP step-up・back-channel・hold/restore、2026-10-27 10:40 JST に 8/8 PASS。process 初回 discovery 前は失効を優先し staff access を fail-closed にする。
- [x] 対象者は非公開 passcode で demo を開ける。証跡: [remote demo](remote-demo.md) の Pages gate と再起動時 reset。synthetic local runtime を使用し、実 staff sign-in や Stripe は使わない。
- [x] [既知 gap と demo の制限](known-gaps.md)を記録する。MVP 完了は試店承認、Stripe live mode、本番 SLA、RPO/RTO の達成を意味しない。

### 試店開始（実店舗で提供する前に完了）

- [ ] **[Owner]** 東島さんの 2026-11-02 の反応後に hosting provider を決定し、固定月額 ¥0 と課金境界を確認する。有料 plan／課金資源は事前に Owner が明示承認する。
- [ ] host、ingress、DNS/domain、本番相当 runtime を用意し、quota、課金境界、監視、アクセス制御、復元権限を確認して platform operator を指名する。
- [ ] session 失効後に非同期 Checkout 作成が始まる別途追跡中の gap を閉じ、exact-head の受入証跡を記録する。 — PR #54 で解消済み（2026-10-26 JST）。exact-head の受入証跡は未記録。
- [ ] **[Owner]** IdP と MOD-08 証跡を受入。Keycloak R01 漏えいパスワード検査、スタッフ Passkey/MFA、現所属確認、即時失効、復旧動作を含む。
- [ ] 配備先 backup から restore drill を行い、整合性確認と復元時間を記録する。RPO 5分／RTO 1時間は将来の本番目標であり、試店要件ではない。
- [ ] P-84 7f 支援技術経路と 7g iPhone/iOS Safari、Android/Chrome 実機確認を完了。端末／OS／browser、zone付き日付、証跡を記録し、desktop 試験を PASS の代用にしない。
- [ ] 障害時の停止・切替、現金、KDS 継続、注文／返金確認、事故報告、営業再開判断の店主・店員担当を実名で決め、手順を訓練する。
- [ ] **[Owner／決済責任者]** EXT-1 Sandbox 受入を完了し、Stripe live charge mode、account 対応、webhook 管理、照合・返金を別途承認する。test mode の証跡は live 承認ではない。
- [ ] **[Owner]** 税理士／社労士等に保存期間案を確認し、運用方針、削除／hold 手順を承認し、選定 storage が要件を満たすことを確認する。[保存期間案](ops/retention.md)を参照。全記録一律7年とはしない。
- [ ] migrate と runtime の DB role 分離：Compose は migrate を owner role で実行し `APP_DB_ROLE` に `GrantRuntime` を適用、server／`staffadmin` は DML のみの `tojima_app` で接続する。本番では role 作成・同分離の適用・確認が未完了。
- [ ] ingress abuse controls、HTTPS/cookie/origin、secret 保存・rotation、監視・alert、安全ログ保存、incident 連絡先を確認する。
- [ ] **[Owner]** 既知 gap を確認して試店開始を明示承認する。1店舗に限定し、中止条件と連絡先を記録する。

## 繁體中文

### MVP 完成（工程完成且可展示）

- [x] 整合顧客點餐、店員操作、付款狀態轉換、桌次生命週期及 tenant/store 授權，並以隔離的 demo/test 資料展示。證據：[P-84 matrix](p84-acceptance-matrix.md)、[remote demo](remote-demo.md)、[P-85 demo](p85-demo.md)，以及 2026-10-27 JST tenant A 真實 Stripe Sandbox 流程（P-84 5i、[PR #64](https://github.com/nurockplayer/tojima-restaurant-platform/pull/64)、[#65](https://github.com/nurockplayer/tojima-restaurant-platform/pull/65)、[#66](https://github.com/nurockplayer/tojima-restaurant-platform/pull/66)）。
- [ ] 公司／店舖隔離及付款正確性部分驗證：整合測試涵蓋隔離及付款狀態轉換（[P-84 5b–5g](p84-acceptance-matrix.md)）；tenant A paid/declined/3DS 於 2026-10-27 JST 真實 Sandbox PASS；雙商戶並行隔離（5a）及 async/expiry/refund（5i）仍未完成（EXT-1）。
- [x] session/grant/PIN/Checkout/`ORDER_READ` 到期行為符合 D12。證據：[PR #51](https://github.com/nurockplayer/tojima-restaurant-platform/pull/51) TTL 驗證及 [PR #54](https://github.com/nurockplayer/tojima-restaurant-platform/pull/54) dispatch fence 關閉 session 到期後建立 Checkout 的缺口（migration 016、[deploy runbook](deploy-runbook.md)）。
- [x] IdP 中斷行為符合 D17。證據：[ADR-0008](adr/0008-staff-sign-in.md) D17 與 Consequences、PR [#58](https://github.com/nurockplayer/tojima-restaurant-platform/pull/58), [#59](https://github.com/nurockplayer/tojima-restaurant-platform/pull/59), [#60](https://github.com/nurockplayer/tojima-restaurant-platform/pull/60), [#61](https://github.com/nurockplayer/tojima-restaurant-platform/pull/61), [#62](https://github.com/nurockplayer/tojima-restaurant-platform/pull/62), [#63](https://github.com/nurockplayer/tojima-restaurant-platform/pull/63)、[staff auth acceptance](../scripts/staff-auth-acceptance.sh) 真實 Keycloak login、OTP step-up、back-channel、hold/restore，2026-10-27 10:40 JST 8/8 PASS。每個 process 首次 discovery 前 staff access fail-closed，以可用性換取撤銷保障。
- [x] 目標使用者可用私下提供的 passcode 開啟 demo。證據：[remote demo](remote-demo.md) Pages gate 與重啟時重置；使用 synthetic local runtime，沒有真實 staff sign-in 或 Stripe。
- [x] 記錄[已知缺口與 demo 限制](known-gaps.md)。MVP 完成不代表核准試營運、Stripe live mode、正式 SLA 或已達成 RPO/RTO。

### 試營運（真實店舖服務前全部完成）

- [ ] **[Owner]** 待東島先生 2026-11-02 回饋後決定 hosting provider，確認固定月費 ¥0 與計費邊界。付費方案或計費資源須事先由 Owner 明確核准。
- [ ] 建置 host、ingress、DNS/domain 及正式相當 runtime；確認 quota、計費邊界、監控、存取控制及還原權限，並指定平台營運者。
- [ ] 關閉另行追蹤的 session 到期後才非同步建立 Checkout 缺口，並記錄 exact-head 驗收證據。 — 已由 PR #54 關閉（2026-10-26 JST）；exact-head 驗收證據尚待記錄。
- [ ] **[Owner]** 驗收 IdP 與 MOD-08 證據，包括 Keycloak R01 外洩密碼檢查完成、員工 Passkey/MFA、目前 membership 檢查、即時撤銷及復原行為。
- [ ] 從實際 hosting 備份執行 restore drill，進行完整性檢查並記錄復原時間。RPO 5 分鐘／RTO 1 小時是未來正式環境目標，不是試營運要求。
- [ ] 完成 P-84 7f 輔助科技流程及 7g iPhone/iOS Safari、Android/Chrome 實機測試。記錄裝置／OS/browser、含時區日期及證據；桌面測試不能代替實機 PASS。
- [ ] 指名店主與店員負責故障停止／切換、現金處理、KDS 持續、訂單／退款核對、事件通報及恢復營業核准，並完成流程訓練。
- [ ] **[Owner／付款負責人]** 完成 EXT-1 Sandbox 驗收，另行核准 Stripe live charge mode、account 對應、webhook 管理、對帳與退款。test mode 證據不等於 live 核准。
- [ ] **[Owner]** 請稅理士／社勞士等確認保存期限草案，核准營運政策與刪除／hold 流程，並確認 storage 可符合要求。參閱[保存期限草案](ops/retention.md)；不可一律保存七年。
- [ ] 分開 migrate 與 runtime 的 DB 角色：Compose 已讓 migrate 以 owner role 執行並對 `APP_DB_ROLE` 套用 `GrantRuntime`，server／`staffadmin` 以僅 DML 的 `tojima_app` 連線。正式環境仍須建立角色、套用並驗證同樣分離。
- [ ] 確認 ingress abuse controls、HTTPS/cookie/origin、secret 儲存與輪替、監控／告警、安全日誌保存及事故聯絡人。
- [ ] **[Owner]** 審閱已知缺口並明確核准開始試營運。範圍限一間店，記錄停止條件及升級聯絡人。
