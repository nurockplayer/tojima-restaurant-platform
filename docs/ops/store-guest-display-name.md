# Guest store display name / 店舗表示名 / 店家顯示名稱

Authority: [MOD-05 P-90](https://linear.app/higashijima-manage-system/issue/P-90),
Owner decision `D-MOD05-STORE-NAME-20261021` (2026-10-21).

## English

Only the Owner, or an operator explicitly authorised by the Owner with production
DB access, may set `stores.guest_display_name` through controlled SQL. There is no
app, guest, staff, catalog or admin API writer. Migration 015 must be deployed
first. This public name is separate from the internal `stores.name`. NULL omits
`store_display_name`; it never falls back to a store or tenant internal name.
The database accepts 1–80 characters, trimmed by `btrim`, with no control
characters. Existing and DEMO stores stay NULL; the DEMO constant still wins.

Use a SQL client that binds `$1` (tenant ID), `$2` (store ID), and `$3` (public name)
as parameters; do not interpolate values into SQL. In **one transaction**, confirm
the intended tenant/store and record its current value. The internal `name` in
this operator-only pre-check must never be copied into the public field by default.

```sql
BEGIN;
SELECT tenant_id, store_id, name, guest_display_name
FROM stores WHERE tenant_id = $1 AND store_id = $2 FOR UPDATE;
-- Confirm exactly one intended row; otherwise ROLLBACK.
UPDATE stores SET guest_display_name = $3
WHERE tenant_id = $1 AND store_id = $2
RETURNING tenant_id, store_id, guest_display_name;
-- Expect exactly one row and verify the public value; otherwise ROLLBACK.
COMMIT;
```

To unset, use the same pre-check and transaction, replacing the UPDATE with:

```sql
UPDATE stores SET guest_display_name = NULL
WHERE tenant_id = $1 AND store_id = $2
RETURNING tenant_id, store_id, guest_display_name;
```

Post who performed the change, when, both tenant/store IDs, and the old and new
values on P-90 (or the ops log). Never post DB credentials. Confirm the three guest
reads (table status, locator menu, session menu) for the intended store. Guests
see an updated or removed name on the next read. Public names must not appear on
other guest responses or staff/admin menu projections.

Deploy migration 015 alone (no other pending migrations), then the app, in a
low-traffic window. ADD COLUMN is metadata-only without a default, but takes an
ACCESS EXCLUSIVE lock and the CHECK scans `stores`. The migration bounds lock
waits to 3 seconds and statements to 30 seconds, then resets the transaction-local
settings. A timeout rolls back the entire pending migration transaction; retry
later in a low-traffic window.

Rollback levels:

1. **Data:** set the scoped value to NULL above. No deploy; next guest read removes
   the name.
2. **App:** use an app-only rollback commit. Revert the Go (except migrations), web
   and feature docs, but **keep both 015 SQL files, migration tests and the explicit
   `{version: 15, …}` registry entry in `migrations/postgres/migrate.go`**. Version
   015 is permanently reserved. The column stays and old binaries ignore it.
3. **Schema:** only after level 2 is deployed everywhere and no running binary
   reads the column. In **one psql session and transaction**, export non-NULL
   values to a protected operator-owned file before dropping. Do not use a
   separate export connection. The shared advisory lock serializes migrations;
   the table lock prevents controlled updates between export and drop.

```sql
BEGIN;
SET LOCAL lock_timeout = '3s';
SET LOCAL statement_timeout = '30s';
SELECT pg_advisory_xact_lock(771904283);
LOCK TABLE stores IN SHARE ROW EXCLUSIVE MODE;
COPY (SELECT tenant_id, store_id, guest_display_name FROM stores
      WHERE guest_display_name IS NOT NULL) TO STDOUT;
-- Save COPY output, preserving PostgreSQL COPY text escaping. If saving fails,
-- ROLLBACK; do not drop the column without a usable export.
ALTER TABLE stores DROP COLUMN guest_display_name;
DELETE FROM schema_migrations WHERE version = 15;
COMMIT;
```

In psql, use `\o '/absolute/operator-owned/path/p90-export.txt'` immediately before
COPY and `\o` immediately after it to save just its output. Verify the export
before continuing with ALTER; on an error, ROLLBACK. Keep it with the ops record.
A later `Up()` from the retained registry re-adds 015 as NULL. Restore the export's
values using parameter-bound updates above, keyed by **both IDs**, decoding COPY
text escaping first. Confirm one row per update and compare all restored values.
Levels 1–2 preserve the schema; level 3 is restorable from the export.

**Never call `postgres.Down()` against a real database: it tears down every
applied migration.** Do not fully revert the feature PR: removing registry entry
015 prevents safe reapplication and risks version reuse.

## 日本語

本番 DB へのアクセス権がある Owner、または Owner が明示的に承認した運用者だけが、
管理された SQL で `stores.guest_display_name` を更新できる。アプリ・顧客・スタッフ・
catalog・admin API からの更新はない。先に migration 015 を適用する。内部名
`stores.name` とは別の公開名であり、NULL の場合は `store_display_name` を省略する。
店舗・tenant の内部名へのフォールバックは禁止。DB は `btrim` 済みの1〜80文字、
制御文字なしを要求する。既存 fixture と DEMO は NULL のまま、DEMO 定数が優先する。

上記のパラメータ付き SQL を **同じトランザクション** で実行する。`$1` は tenant ID、
`$2` は store ID、`$3` は公開名。文字列を SQL に連結しない。SELECT の内部名は
運用者による対象確認専用で、公開名へ自動コピーしない。SELECT と UPDATE RETURNING
がそれぞれ意図した1行であることを確認し、それ以外は ROLLBACK、確認後だけ COMMIT。
解除は同じ複合キーで `SET guest_display_name = NULL` を使う。

実施者・日時・tenant/store 両 ID・変更前後の値を P-90 または運用ログへ記録し、
DB 認証情報は記録しない。対象店舗の table status・locator menu・session menu の
3つの顧客 read を確認する。表示変更・削除は次回 read に反映される。他の顧客レスポンスや
スタッフ・admin の menu に公開名を含めない。

低トラフィック時間帯に、未適用 migration が015だけの状態で015、アプリの順に展開する。
デフォルトなしの ADD COLUMN はメタデータ変更だが ACCESS EXCLUSIVE lock と CHECK の
走査を伴う。lock 待ち3秒、statement 30秒で制限し、設定は migration 内でリセットする。
タイムアウト時は未適用 migration 全体のトランザクションがロールバックするため、後で再試行する。

ロールバック：①データだけなら対象を NULL にする。②アプリだけ戻すコミットでは Go
（migration 以外）・web・機能 docs を戻し、**015 の up/down SQL・migration tests・
migrate.go の version 15 登録を必ず残す**。015 は永久予約で、旧バイナリは列を無視する。
③全環境で②を展開し、列を読むバイナリがなくなった後だけ、上記 level-3 SQL を使う。
**同じ psql 接続・同じトランザクション** で advisory lock と table lock を取得し、
非 NULL 値の COPY を保存してから列と ledger の version 15 だけを削除する。
COPY の直前に `\o '/absolute/operator-owned/path/p90-export.txt'`、直後に `\o` を使い、
運用者管理のファイルへ保存する。保存に失敗したら ROLLBACK し、DROP しない。
COPY text のエスケープを保持する。再展開時は登録を残した `Up()` で列を再作成し、
COPY text を復号して、両 ID のパラメータ付き UPDATE で1行ずつ復元・照合する。
①②は schema を保持し、③は保存済み export から復元できる。
**本番 DB で `postgres.Down()` を呼ばない。全 migration を破壊する。**
015 の登録を消す PR 全体の revert も禁止（再適用不能・番号再利用の危険）。

## 繁體中文

只有具備正式 DB 存取權的 Owner，或經 Owner 明確授權的操作人員，可以透過受控 SQL
設定 `stores.guest_display_name`。沒有 app、顧客、員工、catalog 或 admin API 寫入入口。
先套用 migration 015。公開名稱與內部 `stores.name` 分開；NULL 時省略
`store_display_name`，絕不退回店家或 tenant 內部名稱。DB 要求經 `btrim` 去除前後空白的
1–80字且不含控制字元。既有 fixture 與 DEMO 保持 NULL，DEMO 常數仍優先顯示。

請在 **同一交易** 執行上面的參數化 SQL：`$1` 為 tenant ID、`$2` 為 store ID、`$3` 為公開
名稱，不可把值直接串接成 SQL。SELECT 的內部名稱只供操作人員核對目標，不可自動複製為公開名。
SELECT 與 UPDATE RETURNING 都必須恰好回傳一筆正確資料；否則 ROLLBACK，確認後才 COMMIT。
取消設定時使用同一複合鍵及 `SET guest_display_name = NULL`。

將操作者、時間、tenant/store 兩個 ID、舊值與新值記錄於 P-90 或操作日誌，勿記錄 DB 憑證。
確認目標店家的 table status、locator menu、session menu 三個顧客 read。修改或移除會在
下次 read 反映；其他顧客回應及員工／admin menu 不可包含公開名稱。

在低流量時段、僅015待套用的狀態下，先 migration 015 再部署 app。無預設值的 ADD COLUMN
是 metadata 變更，但仍需 ACCESS EXCLUSIVE lock 並掃描 CHECK。lock 等待限制3秒、statement
限制30秒，migration 結束前重設；逾時會回滾整個待套用 migration 交易，稍後再重試。

回滾層級：①僅資料：依上方流程將指定值設為 NULL。②僅 app：以 app-only rollback commit
退回 Go（migration 除外）、web 與功能文件，**必須保留015 up/down SQL、migration tests 及
migrate.go 的 version 15 登錄**。015 永久保留，舊程式忽略此欄。③僅在所有環境完成②且無任何
執行中程式讀此欄後，使用上方 level-3 SQL。於 **同一 psql 連線、同一交易** 取得 migration
advisory lock 與 table lock，先將非 NULL 值 COPY 匯出並保存，再刪欄及 ledger 的 version 15。
在 COPY 前使用 `\o '/absolute/operator-owned/path/p90-export.txt'`，後面使用 `\o`，將輸出存至
操作人員管理的檔案。保存失敗即 ROLLBACK，禁止 DROP。保留 COPY text 跳脫格式。
日後用保留登錄的 `Up()` 重建欄位，解碼 COPY text，透過同時帶兩個 ID 的參數化 UPDATE
逐筆還原並核對。①②保留 schema，③可由匯出檔還原。
**不可在真實 DB 執行 `postgres.Down()`，它會拆除所有 migration。**
不可完整 revert 此功能 PR 而移除015登錄，否則無法安全重建且可能重用版本號。
