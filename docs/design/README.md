# Design system handoff / デザインシステム引継ぎ / 設計系統交接

Authority: Linear [D-DS-20261019 in doc 02](https://linear.app/higashijima-manage-system/document/02意思決定-商業決策記錄-53df2634251a) (Owner approved option D, 2026-10-19), [doc 08](https://linear.app/higashijima-manage-system/document/08-jazhデザインシステム方針設計系統方向-v01approved承認済已批准-efa1ff56f82a), [P-87](https://linear.app/higashijima-manage-system/issue/P-87) (approval), [P-88](https://linear.app/higashijima-manage-system/issue/P-88) (DS-1, merged as `3c47f7a`), [P-89](https://linear.app/higashijima-manage-system/issue/P-89) (DS-2 plan, the implementation contract). This folder holds the supporting files that were previously only on the director's machine. **If a file here and a Linear issue disagree, the Linear issue wins. If a research file and `web/src/shared/tokens.css` disagree, the code wins.**

## Contents

| Path | What it is | Status |
|---|---|---|
| `prototype/tojima-ds-proposal.html` | Offline copy of the approved proposal page: A–D comparison, a clickable Mobile Order prototype (UX-1…UX-7), previews of the four surfaces, and a live contrast audit. Open it directly in a browser. | Reference for DS-2 UX. The same page is published privately at https://claude.ai/artifact/4PskAPuWbrJPofbPfQjbST; this copy does not need access to that link. |
| `prototype/evidence/` | Screenshots of the guest UI before DS-1, used on the proposal page. | Historical |
| `research/tokens.css` | Proposed three-tier tokens, with guest/floor/office/kds `data-surface` blocks and the brand layer. | **Superseded** by `web/src/shared/tokens.css` (DS-1). The density blocks are added only when they have a consumer: guest in DS-2, floor/KDS in DS-3, office in DS-5. |
| `research/contrast-gate.mjs` | Node WCAG gate and store-brand derivation (2,000 random colours). Run with `node contrast-gate.mjs [tokens.css] [--json]`. Output is in `gate-output.txt`. | Reference for DS-4 (store brand) |
| `research/audit-commands.txt` | Commands that produced the DS-1 baseline numbers. | Historical |
| `ds-1-acceptance/capture.mjs` | Rendered acceptance harness. It drives headless Chrome over CDP with no npm dependencies. It captures screenshots, a computed-style contrast audit (text and form controls), overflow, focus-ring clipping and DEMO-notice presence across ja/zh × 375/1280 + 320. | **Reuse and extend for DS-2** (P-89 §5.4) |
| `ds-1-acceptance/acceptance-summary.md`, `report-*.json`, `shots/` | DS-1 before/after evidence (`78baf00` → `cebe5b3`). | Baseline for the DS-2 regression check on staff, KDS and catalog |
| `ds-2-baseline/measure-baseline.mjs` | DS-2 entry measurements: first-viewport rows, card heights, sticky elements, Tab count, ORDER_READ button size. | Starting point for the DS-2 harness |
| `ds-2-baseline/baseline-3c47f7a/` | `report.json` and first-viewport screenshots at `3c47f7a`. These are the numbers in P-89 §2. | DS-2 "before" |
| `ds-2-baseline/plan-reviews/r1…r5.txt` | Fresh read-only Codex reviews of the DS-2 plan: BLOCKED ×4, then PASS. All findings were applied in P-89. | Done; do not redo |

## Running the harnesses

Both scripts need:
- Google Chrome at `/Applications/Google Chrome.app`;
- Node 24 or later, which provides the built-in `WebSocket`;
- an isolated demo backed by a disposable PostgreSQL 17. Never point them at a shared or production database.

```sh
docker run -d --rm --name ds-pg -p 127.0.0.1:55471:5432 -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=tojima_test postgres:17
pnpm --dir web install --frozen-lockfile && pnpm --dir web build
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55471/tojima_test?sslmode=disable' \
  go run ./cmd/operationstest -demo-shironagasukujira -catalog-fixture -port 8095 -credentials /tmp/ds-after-creds.json
node docs/design/ds-1-acceptance/capture.mjs after http://127.0.0.1:8095 /tmp/ds-after-creds.json /tmp/ds-capture
```

- **Credentials file:** the demo writes a new owner-only credentials file and deletes it on Ctrl-C. The scripts read it only to log in to the staff, KDS and catalog surfaces. Never commit it or paste it.
- **Before vs after:** for a before/after pair, run a second demo from the base commit on a different port, and give each side a fresh schema by restarting its demo.
- **Native dialog:** the native `<dialog>` lets Tab leave the document after its last control, and the DS-1 report records this. DS-2 replaces this with React Aria per P-89.

## Process (from P-88/P-89)

- **Roles:** Opus plans and accepts, and writes no product code. GPT-6.1 Sol implements:

  ```sh
  codex exec -m gpt-6.1-sol -c model_reasoning_effort=high -s workspace-write --add-dir <repo>/.git -C <worktree>
  ```

  A **fresh** read-only Codex reviews the exact SHA:

  ```sh
  codex exec -m gpt-6.1-sol -s read-only
  ```
- **Patches:** Sol's sandbox may be unable to write `.git/worktrees/*/index.lock`. In that case ask for numbered patches, apply them in order with explicit commit messages, and verify that the result is byte-equal to Sol's tree.
- **Merging:** this repo has auto-merge disabled. Once CI is green, merge with `gh pr merge --merge --match-head-commit <reviewed SHA>`, then confirm main CI.
- **Reporting:** post bilingual JA/ZH checkpoint comments on the package issue and on P-84. P-84 7f (assistive technology) and 7g (WebKit and physical devices) stay human gates.

## 日本語

本フォルダは、これまで総監の端末にしかなかった DS-1/DS-2 の補助資料を集めたもの。仕様の正は Linear（P-89 が DS-2 の実装契約）、トークンの正は `web/src/shared/tokens.css`。

- **`prototype/`**：承認済み提案ページのオフライン版。UX-1〜7 の操作可能プロトタイプを含み、ブラウザで直接開ける。
- **`research/`**：研究時の提案トークンとコントラストゲート。DS-1 で置換済み。DS-4 の店舗ブランド導出の参考。
- **`ds-1-acceptance/`**：描画受入ハーネス（CDP、依存なし）と DS-1 の before/after 証跡。DS-2 ではこれを拡張して使う。
- **`ds-2-baseline/`**：P-89 §2 の基準値の計測スクリプト・結果・スクリーンショットと、計画レビュー 5 回の記録。PASS 済みのため再レビューは不要。

ハーネスは使い捨ての PostgreSQL と隔離 demo でのみ実行する。資格情報ファイルはコミットしない。

## 繁體中文

本資料夾收集原本只存在於總監電腦上的 DS-1/DS-2 輔助資料。規格以 Linear 為準（P-89 為 DS-2 的實作契約），token 以 `web/src/shared/tokens.css` 為準。

- **`prototype/`**：已批准提案頁的離線版，含 UX-1〜7 可操作原型，可直接用瀏覽器開啟。
- **`research/`**：研究時的提案 token 與對比度門檻。已由 DS-1 取代，可作為 DS-4 店家品牌推導的參考。
- **`ds-1-acceptance/`**：渲染驗收工具（CDP、無依賴）與 DS-1 的 before/after 證據。DS-2 請擴充沿用。
- **`ds-2-baseline/`**：P-89 §2 基準值的量測腳本、結果、截圖，以及 5 次規劃審查紀錄。已 PASS，不需重審。

工具只能在拋棄式 PostgreSQL 與隔離 demo 上執行；憑證檔不得提交。
