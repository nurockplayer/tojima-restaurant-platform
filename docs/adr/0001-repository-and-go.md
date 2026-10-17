# ADR-0001 — Repo / Go / 境界・邊界

Date: 2026-10-18 JST. Status: verified repository; Go is Owner-approved. Other decisions below remain separate.

## 日本語

P-74/P-75 と工程交接 v1.0 の正式名 `tojima-restaurant-platform` に一致する、この新規専用 repository を使用する。`origin` と GitHub API の両方で `nurockplayer/tojima-restaurant-platform`、private、default branch `main`、現在の利用者 `nurockplayer` の `ADMIN` 権限を確認した。開始時 main HEAD は `3b8989e10fd0254ab7472e58a5e7d84db7030aed`、tracked 内容は README のみ。既存 Go/Flutter/Firebase 実装はこの repo に存在しない。別 repo の原価アプリの実装棚卸し・移行は未実施であり、変更しない。将来の連携は MOD-08 の共通 identity と Go API の境界で評価する。

Go App Server の modular monolith は P-17/P-58 の Owner 決定。S0 は標準ライブラリの HTTP health と実行可能な契約までとする。業務の永続化、認証サービス、UI、支払いは後続 stage に委ねる。

作業 branch は `codex/p75-go-foundation`、main 宛の有界 PR を一つ作成する。main は現時点で非保護。rulesets API は private repo のプラン制限で HTTP 403 を返した。これを保護済みと報告せず、権限設定や公開範囲を変更しない。CI と独立 review の証拠を残し、P-75 の「premature merge/production deployment 禁止」を守る。合意・受入前に main へ直接 push/merge しない。

## 繁體中文

採用符合 P-74/P-75 與交接 v1.0 正式命名的專用新 Repo。已用本地 origin 與 GitHub API 核對上述 owner、私有狀態、main、ADMIN 寫入權限與完整初始 SHA；目前只有 README，沒有既存 Go／Flutter／Firebase 原始碼。沒有盤點或修改其他 Repo 的成本 App，也沒有宣稱已完成其遷移評估；未來僅透過 MOD-08 共用身分與 Go API 評估連接。

Go 模組化單體是已批准決策，S0 僅建立 health、測試和工程契約。此分支只交付一個 PR；main 尚無分支保護，rulesets 查詢受私有 Repo 方案限制，不能假稱已有保護。保留 CI／獨立 review 證據，未驗收前不直接推 main、合併或部署。

Sources: [P-74](https://linear.app/higashijima-manage-system/issue/P-74), [P-75](https://linear.app/higashijima-manage-system/issue/P-75), [P-17](https://linear.app/higashijima-manage-system/issue/P-17), [P-58](https://linear.app/higashijima-manage-system/issue/P-58).
