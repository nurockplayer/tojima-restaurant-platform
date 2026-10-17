# ADR-0002 — Transactional DB / 共用資料庫

Date: 2026-10-18 JST. Status: **PostgreSQL test implementation approved; production hosting/configuration NOT selected / PostgreSQL テスト実装承認・測試實作已批准**.

## Owner decision update / 決策更新

2026-10-18、P-75 の PR #1 / exact candidate `c64b640fa8eb6d0c49bcabef7bae1cd4d185e85d` の CI・独立 review 完了後、Owner が「核准 PostgreSQL 的測試實作」と回答した。P-76 の PostgreSQL schema/driver/rollback とテストを許可する。正式 hosting provider、production 配置、商用運用・backup 契約は別判断。以下の比較表は選定前の記録であり、この最新決定を優先する。

繁中：Owner 已明確批准以 PostgreSQL 進行共用交易 DB 的測試實作，解除 P-76 測試 schema 的選型阻礙。正式託管供應商、投產配置與營運承諾仍另行確認；不得把測試批准擴大為正式環境批准。P-75 已以 merge commit `5d2865a0b6dbd077f8c5e39f2716376d209b6074` 整合，原候選程式碼未變更。

## 選定前の比較記録 / 選型前比較紀錄

Go を唯一の業務・取引権威とし、単一の共有 transactional DB に tenant/store の境界と原子性を置く。P-37 の PostgreSQL 案は候補であり、P-17 の vendor ADR 承認を代替しない。最小比較は以下のとおり。費用・運用・復旧の実測は未実施で、製品能力の検証済み評価ではない。

| 候補 / 候選 | 検証すべき利点 / 待驗證優點 | 未解事項 / 待驗證風險 |
| --- | --- | --- |
| Managed PostgreSQL | 関係制約・複数行取引を一つの DB に集約 / 集中關聯約束與交易 | Host、backup/PITR、費用、隔離・接続上限、競合試験 |
| PostgreSQL BaaS (e.g. Supabase) | 管理された SQL 基盤 / 代管 SQL | Go が唯一の書込権威、公開 DB 直接書込を禁止、IdP を別決定、plan/復旧 |
| Other managed relational SQL | 同じ業務契約を満たす代替 / 同契約替代案 | active-session uniqueness、DDL差、lock/rollback、運用費 |
| Existing Firebase/document store | 既存アプリとの互換性評価 / 既存相容性 | 複数エンティティの原子性、重複拒否、source of truth、移行/復旧の実証 |

S0 では SQLite を **一時テスト専用** として portable baseline SQL の up/down を空 DB 上で検証した。S1 は承認済み PostgreSQL の driver/runner・locking・migration ledger・transaction rollback・DB 付き CI を実装／検証する。SQLite smoke の成功を PostgreSQL 検証と読み替えない。

## 繁體中文

固定的是「Go 業務權威＋單一共用交易 DB」及租戶／分店邊界，正式託管供應商尚未確定。上表保存選型前待驗證問題，不是供應商已驗收的能力宣稱；PostgreSQL 測試實作已批准，BaaS 亦不能讓客端直接改業務資料。費用、備份還原、離線需求、既有資料搬遷均尚無實測。

S0 以暫存 SQLite 驗證基礎 SQL 的空庫 up/down/reapply，未建立產品資料表。Owner 已批准 S1 PostgreSQL driver、migration runner、測試 schema、鎖定與交易策略；正式 vendor/host 決策仍是投產前 gate，不再阻擋本包的測試實作。

## Owner gate / Owner 待決

- Production hosting/vendor/configuration、環境與費用負責人、backup/restore 需求與可接受的復原目標。PostgreSQL 測試 engine 已批准，以下未決項目不阻擋測試環境 schema。
- Existing Firebase integration/migration inventory, if later authorized; it is not a prerequisite for this standalone scaffold.
- Initial Japanese inclusive-JPY tax and per-rate floor rounding are now approved by the 2026-10-18 Owner update: see [ADR-0004](0004-japan-tax-calculation.md). Tax is not a P-77 blocker. No floating-point financial arithmetic; receipt/payment integration retains its S4/S5 acceptance.

See [schema contract](../contracts.md). Sources: [P-17](https://linear.app/higashijima-manage-system/issue/P-17), [P-37](https://linear.app/higashijima-manage-system/issue/P-37), [P-75](https://linear.app/higashijima-manage-system/issue/P-75).
