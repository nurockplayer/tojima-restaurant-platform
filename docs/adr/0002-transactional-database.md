# ADR-0002 — Transactional DB / 共用資料庫

Date: 2026-10-18 JST. Status: **Proposed; production vendor NOT selected / 未承認・未批准**.

## 日本語

Go を唯一の業務・取引権威とし、単一の共有 transactional DB に tenant/store の境界と原子性を置く。P-37 の PostgreSQL 案は候補であり、P-17 の vendor ADR 承認を代替しない。最小比較は以下のとおり。費用・運用・復旧の実測は未実施で、製品能力の検証済み評価ではない。

| 候補 / 候選 | 検証すべき利点 / 待驗證優點 | 未解事項 / 待驗證風險 |
| --- | --- | --- |
| Managed PostgreSQL | 関係制約・複数行取引を一つの DB に集約 / 集中關聯約束與交易 | Host、backup/PITR、費用、隔離・接続上限、競合試験 |
| PostgreSQL BaaS (e.g. Supabase) | 管理された SQL 基盤 / 代管 SQL | Go が唯一の書込権威、公開 DB 直接書込を禁止、IdP を別決定、plan/復旧 |
| Other managed relational SQL | 同じ業務契約を満たす代替 / 同契約替代案 | active-session uniqueness、DDL差、lock/rollback、運用費 |
| Existing Firebase/document store | 既存アプリとの互換性評価 / 既存相容性 | 複数エンティティの原子性、重複拒否、source of truth、移行/復旧の実証 |

S0 では SQLite を **一時テスト専用** として portable baseline SQL の up/down を空 DB 上で検証する。採用 ADR、production migration runner、同時接続下の SQL 保証ではない。業務テーブルは作らない。DB 選定後、S1 の PR で正式 driver/runner・locking・migration ledger・transaction rollback・採用 DB 上の CI を実装／検証する。SQLite smoke の成功を PostgreSQL 検証と読み替えない。

## 繁體中文

固定的是「Go 業務權威＋單一共用交易 DB」及租戶／分店邊界，尚未固定資料庫供應商。上表是下一輪比較的驗證問題，不是供應商已驗收的能力宣稱；PostgreSQL 為建議方向，BaaS 亦不能讓客端直接改業務資料。費用、備份還原、離線需求、既有資料搬遷均尚無實測。

S0 只用測試專用暫存 SQLite 驗證基礎 SQL 的空庫 up/down/reapply，不建立產品資料表，也不把測試 DB 當正式選型。Owner 確認 vendor/host 後，S1 才落地正式 driver、migration runner、鎖定與交易策略，並在該 DB 驗證競爭及復原。

## Owner gate / Owner 待決

- DB engine/vendor/hosting、環境與費用負責人、backup/restore 需求與可接受的復原目標。
- Existing Firebase integration/migration inventory, if later authorized; it is not a prerequisite for this standalone scaffold.
- JPY tax inclusive/exclusive and rounding policy remains unapproved; no money-changing production use before decision and tests. No floating-point financial arithmetic.

See [schema contract](../contracts.md). Sources: [P-17](https://linear.app/higashijima-manage-system/issue/P-17), [P-37](https://linear.app/higashijima-manage-system/issue/P-37), [P-75](https://linear.app/higashijima-manage-system/issue/P-75).
