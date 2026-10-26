# Retention policy — DRAFT / 保存方針 — 草案 / 保存政策 — 草案

**DRAFT — pending review and confirmation by a Japanese 税理士/社労士 as applicable, and Owner approval.** These are proposed product defaults, not legal advice or an adopted policy. Keep record categories separate; there is no blanket seven-year retention rule. Calculate statutory periods from the applicable start date, preserve required electronic originals and correction history, and apply holds before deletion.

## English

| Record category | Grade and legal basis to confirm | Proposed Tojima period / handling |
|---|---|---|
| Orders, sales books, receipts and supporting transaction records | **A —** NTA corporation-tax baseline: seven years from the day after the return filing deadline; specified blue-return loss/disaster-loss years require ten years. Not every carried-forward loss year automatically has the ten-year period. [NTA No.5930](https://www.nta.go.jp/taxes/shiraberu/taxanswer/hojin/5930.htm) | Ten years using the applicable legal start date. Preserve corrected/cancelled versions and accounting links. Confirm entity and record classification with tax adviser. |
| Corporate account books and important business materials | **B —** Companies Act Art. 432(2) separately provides ten years for stock companies from closing the books; applicability depends on entity and record classification. [Official government translation](https://www.cas.go.jp/jp/seisaku/hourei/data/Corp_2.pdf) | Apply the longer applicable period. Ten years is a proposed financial archive policy, not a universal rule. |
| Electronic receipts, invoices and transaction data | **A/B —** Electronic Bookkeeping Act governs preservation method; it does not make every electronic record “seven years.” Electronic transaction data needs compliant preservation, authenticity and accessibility. [NTA guidance](https://www.nta.go.jp/about/introduction/torikumi/week/dennsityoubo.htm) | Same proposed ten-year financial archive. Preserve original electronic records, searchable metadata and correction history; screenshots alone are insufficient. |
| Payment, refund, settlement and reconciliation records | **B —** Retention follows applicable tax/company accounting evidence rules. A universal separate Stripe-specific statutory period was not verified. | Ten years for financial facts and necessary provider references, subject to adviser confirmation. Exclude secrets and unnecessary payload fields. |
| Attendance, actual work time and payroll-supporting shift records | **A —** Labour Standards Act Art. 109 states five years; Art. 143 currently applies three years “for the time being.” [MHLW explanation](https://www.startup-roudou.mhlw.go.jp/qa/zigyonushi/syuugyoukisoku/q6.html) | Five years from the applicable record-specific start date. Payroll/tax records may require longer. Do not automatically retain irrelevant draft schedules. |
| Authentication, security and authorization logs | **C —** No universal fixed Japanese period for ordinary system logs was verified. APPI requires appropriate security measures. [PPC guidelines](https://www.ppc.go.jp/personalinfo/legal/guidelines_tsusoku/) | At least 90 days searchable (R11); consider one year for minimized privileged-security events. Financial audit events follow the financial archive. |
| Guest personal data | **A/C —** APPI has no general fixed retention period; make efforts to erase personal data without delay once no longer needed, subject to other legal retention duties. [PPC FAQ](https://www.ppc.go.jp/all_faq_index/faq1-q5-2/) | Proposed default: remove unnecessary contact/session data 30 days after completed service, subject to disputes/support needs. Retain required financial records separately with minimized identity data. |
| Departed staff IdP accounts | **C —** No universal IdP-account period verified. Credential access and employment-record retention are separate purposes. | Revoke immediately at effective departure. Remove credentials/profile after a documented 30–90-day administrative window; preserve only a minimal, non-reusable subject reference where lawful and needed for audit/employment records. |
| Backups | **C —** Recovery copies have a different purpose from statutory archives; restoration must not undo revocations or deletion decisions. | Set a separate expiry. A 30-day recovery backup is not a ten-year legal archive. Reapply holds, revocations and deletion decisions before restored service resumes. |

A free-storage quota is a capacity limit, not an exemption from retention duties. Measure growth before pilot; if the approved schedule cannot fit free resources and existing owner-controlled storage, the deployment cannot remain an accepted ¥0 pilot.

## 日本語

**草案 — 該当する税理士／社労士等の確認と Owner 承認が必要。** 法的助言でも確定した社内規程でもない。記録区分を分け、一律7年保存にしない。法定期間は各記録に適用される起算日から計算し、電子記録の原本・訂正履歴を保全し、削除前に保留（hold）を適用する。

| 記録区分 | 等級・確認する法的根拠 | Tojima の保存案／取扱い |
|---|---|---|
| 注文、売上帳簿、領収書、取引証憑 | **A —** 法人税の原則は申告期限翌日から7年。特定の青色欠損金・災害損失事業年度は10年。繰越欠損がある全年度が自動的に10年となるわけではない。[国税庁 No.5930](https://www.nta.go.jp/taxes/shiraberu/taxanswer/hojin/5930.htm) | 該当する法定起算日から10年を提案。訂正／取消履歴と会計リンクを保存。法人と記録の分類を税理士に確認する。 |
| 会社の会計帳簿・重要資料 | **B —** 会社法432条2項は株式会社の会計帳簿閉鎖時から10年を別途定める。適用は法人形態・記録分類による。[政府訳](https://www.cas.go.jp/jp/seisaku/hourei/data/Corp_2.pdf) | 該当する長い期間を適用。10年は財務アーカイブの提案であり、一律の法定期間ではない。 |
| 電子領収書・請求書・取引データ | **A/B —** 電子帳簿保存法は保存方法を規律するが、すべての電子記録を「7年」とする法律ではない。電子取引データには適切な保存、真実性・可視性が必要。[国税庁案内](https://www.nta.go.jp/about/introduction/torikumi/week/dennsityoubo.htm) | 上記の10年財務アーカイブ案。電子原本、検索可能な metadata、訂正履歴を保存し、スクリーンショットのみで代替しない。 |
| 決済、返金、精算、照合記録 | **B —** 税務／会社法上の会計証憑規則に従う。Stripe に一律適用される独立した法定期間は確認できていない。 | 財務事実と必要な provider reference は10年を提案し、専門家に確認。秘密情報や不要な payload は除外する。 |
| 出勤、実労働時間、給与根拠となるシフト記録 | **A —** 労基法109条は5年、143条により当分の間3年。[厚労省説明](https://www.startup-roudou.mhlw.go.jp/qa/zigyonushi/syuugyoukisoku/q6.html) | 記録ごとの適用起算日から5年を提案。給与／税務記録はより長い場合がある。不要な下書きシフトを自動保存しない。 |
| 認証・安全・認可ログ | **C —** 通常のシステムログに一律の法定期間は確認できていない。APPI は適切な安全管理措置を求める。[個人情報保護委員会ガイドライン](https://www.ppc.go.jp/personalinfo/legal/guidelines_tsusoku/) | R11 に従い検索可能な状態で最低90日。最小化した特権操作ログは1年を検討。財務監査記録は財務アーカイブに従う。 |
| ゲスト個人データ | **A/C —** APPI に一般的な固定期間はなく、不要となった個人データは遅滞なく消去するよう努める。他の法定保存義務は別途優先。[個人情報保護委員会 FAQ](https://www.ppc.go.jp/all_faq_index/faq1-q5-2/) | 完了したサービスから30日後に不要な連絡先／session データを削除する案。紛争・サポート需要を考慮し、必要な財務記録は識別情報を最小化して分離保存する。 |
| 退職スタッフの IdP アカウント | **C —** IdP アカウントに一律の法定保存期間は確認できていない。認証情報と雇用記録は目的が異なる。 | 退職効力発生時に即時 revoke。記録した30〜90日の管理期間後に credentials/profile を削除。監査／雇用記録に必要かつ適法なら再利用不能な最小 subject reference のみ残す。 |
| バックアップ | **C —** 復旧用コピーと法定アーカイブは目的が異なる。復元で失効・削除判断を巻き戻してはならない。 | 独立した期限を定める。30日バックアップは10年の法定アーカイブではない。復旧後の再開前に hold、revoke、削除判断を再適用する。 |

無料 storage quota は容量制限であり、保存義務の免除ではない。試店前に増加量を測定し、承認済み期間を無料資源と既存の Owner 管理 storage で満たせない場合、¥0 試店として受け入れない。

## 繁體中文

**草案 — 須由適用的稅理士／社勞士等確認並經 Owner 核准。** 本文不是法律意見或已採納的內規。記錄類別須分開，不得一律保存七年。法定期限按各記錄適用的起算日計算；保全電子原件及更正歷程，刪除前套用保留（hold）。

| 記錄類別 | 等級與待確認法律依據 | Tojima 保存提案／處理方式 |
|---|---|---|
| 訂單、銷售帳簿、收據及交易佐證 | **A —** 國稅廳法人稅基準為申報期限翌日起7年；特定藍色申報虧損／災害損失年度為10年。並非所有有結轉虧損的年度都自動適用10年。[國稅廳 No.5930](https://www.nta.go.jp/taxes/shiraberu/taxanswer/hojin/5930.htm) | 提案按適用法定起算日保存10年。保留更正／取消版本及會計連結，並請稅理士確認實體與記錄分類。 |
| 公司帳簿及重要業務文件 | **B —** 公司法第432條第2項另規定股份有限公司自帳簿結帳日起10年；適用性視實體與記錄分類而定。[政府英文譯本](https://www.cas.go.jp/jp/seisaku/hourei/data/Corp_2.pdf) | 適用較長的相關期限。10年是財務封存政策提案，不是所有記錄的統一規則。 |
| 電子收據、發票及交易資料 | **A/B —** 電子帳簿保存法規範保存方式，並未將每份電子記錄一律定為「七年」。電子交易資料須適當保存並維持真實性與可讀性。[國稅廳說明](https://www.nta.go.jp/about/introduction/torikumi/week/dennsityoubo.htm) | 同上，提案納入10年財務封存。保存電子原件、可搜尋 metadata 及更正歷程；截圖不足以取代原件。 |
| 付款、退款、結算及對帳記錄 | **B —** 依適用稅務／公司會計佐證規則保存。未查證 Stripe 專屬且普遍適用的法定期限。 | 提案對財務事實及必要 provider reference 保存10年，待專家確認。不得保留秘密或不必要 payload。 |
| 出勤、實際工時及薪資佐證班表 | **A —** 勞動基準法第109條規定5年；第143條目前暫行3年。[厚生勞動省說明](https://www.startup-roudou.mhlw.go.jp/qa/zigyonushi/syuugyoukisoku/q6.html) | 提案從各記錄適用起算日保存5年。薪資／稅務記錄可能需更久；不要自動保存無關草稿班表。 |
| 認證、安全及授權日誌 | **C —** 未確認一般系統日誌有統一日本法定期限。APPI 要求適當安全管理措施。[個人資訊保護委員會指引](https://www.ppc.go.jp/personalinfo/legal/guidelines_tsusoku/) | 依 R11 至少保留可搜尋90日；可考慮保存最小化的特權安全事件一年。財務稽核記錄依財務封存期限。 |
| 顧客個人資料 | **A/C —** APPI 沒有一般固定保存期限；不再需要時應努力儘速刪除，其他法定保存義務另計。[個人資訊保護委員會 FAQ](https://www.ppc.go.jp/all_faq_index/faq1-q5-2/) | 提案在服務完成30日後刪除不必要聯絡／session 資料，並考量爭議及支援需求。必要財務記錄應另存並盡量減少身分資訊。 |
| 離職員工 IdP 帳戶 | **C —** 未確認 IdP 帳戶有統一法定期限。憑證存取與雇用記錄的目的不同。 | 離職生效時立即 revoke。記錄管理理由後，在30至90日內刪除 credentials/profile；若稽核／雇用記錄依法需要，只保留不可重用的最少 subject reference。 |
| 備份 | **C —** 復原副本與法定封存目的不同；還原不可恢復已撤銷權限或刪除決定。 | 設獨立到期日。30日復原備份不等於10年法定封存。服務恢復前重新套用 hold、revoke 及刪除決定。 |

免費 storage quota 是容量限制，不是免除保存責任。試營運前測量成長量；若核准期限無法由免費資源及既有 Owner 管理儲存滿足，該部署不可視為符合 ¥0 試營運條件。
