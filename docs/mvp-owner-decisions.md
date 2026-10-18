# MVP Owner decisions D1–D11 (2026-10-19)

Authority: Owner accepted all director defaults on 2026-10-19 ([P-74](https://linear.app/higashijima-manage-system/issue/P-74)). Base: main `907e6ce`. These settle settings and scope only; they do not approve production hosting, a real IdP or live payments.

| # | Decision | Effect on acceptance |
|---|---|---|
| D1 | Remote demo for 東島さん: `https://tojima-mobile-order-demo.pages.dev`, gated by a shared passcode the Owner hands over privately. It runs only while the director's Mac runs it. Each restart resets the data. Staff test codes are handed over separately and never shown on a page. | Operates [remote demo](remote-demo.md) |
| D2 | Stripe Sandbox merchants: reuse A (`acct_1URmck1Hm4yzytJz`, tagged `synthetic_merchant_a`) and create B and C. The Owner completes Stripe-hosted onboarding with Stripe test data. The two JP accounts that hold jabiko.app details and personal data are **not** used. | Prerequisite for P-84 5a/5i |
| D3 | The Owner installs Stripe CLI 1.54.0 and types the `sk_test_` key at the hidden prompt. Agents never handle the key. GitHub `stripe-test` environment secrets are deferred until local Sandbox acceptance passes. | Prerequisite for 5a/5i |
| D4 | Remote test-card payment for 東島さん is phase 2, after local Sandbox E2E passes. | None yet |
| D5 | ORDER_READ link lifetime stays 24 hours. | EXT-4 decided |
| D6 | MVP supports guests' own phones only; clearing "My orders" on a shared store tablet is post-MVP. | Scope |
| D7 | Keep the STAFF_OPEN default with a fixed QR per table; accept the photographed-QR risk (staff open the table; kitchen release needs staff acceptance). | 3c → PASS |
| D8 | No new in-app throttling. Pilot ingress returns 429 above 120 req/min/IP on `/v1` and 10 req/min/IP on cart commit and payment creation. | 8h stays GAP until deployed and burst-tested |
| D9 | Lost original Checkout grant: no identity recovery in the MVP; staff use S02 reconciliation plus cash or refund. P-73 is post-MVP. | 5h stays GAP (out of MVP scope) |
| D10 | Production hosting, IdP and backup (EXT-3) are proposed separately after 東島さん's demo feedback; they do not block the demo or Sandbox. | 8m/8n unchanged |
| D11 | PRs #18 and #20 closed as superseded by #35 (#19 shows merged because #35 contains its head). | Done |

## D2 Owner steps (Stripe Dashboard, Sandbox "Tachiko Choko 沙盒")

The Stripe connector available to agents cannot create connected accounts or onboarding links, so these steps are the Owner's.

1. Connect → Connected accounts → open `acct_1URmck1Hm4yzytJz` → continue/resume onboarding → fill every field with Stripe's test data.
2. Create two more JP individual test accounts the same way (merchant B and C), with metadata `tojima_test_fixture=synthetic_merchant_b` and `synthetic_merchant_c` if the form allows it.
3. Tell the director. The director reads back `charges_enabled=true` and `card_payments=active` for all three and records the IDs. Then follow [the Sandbox runbook](p80-stripe-test-runtime.md) with D3.

## 日本語

Owner は 2026-10-19 に D1〜D11 の既定案を一括承認。遠隔 DEMO はパスコード制・Mac 稼働時のみ・再起動でリセット。Stripe テスト商戸は A を再利用し B/C を新規作成、Stripe ホスト型 onboarding をテストデータで Owner が実施（エージェント用 Stripe 連携では作成不可）。jabiko.app 由来の個人情報入りアカウントは使わない。CLI と `sk_test_` は Owner 本人。遠隔カード決済は第2段階。ORDER_READ は24時間。共用タブレットは Post-MVP。STAFF_OPEN＋固定 QR のリスクを受容（3c は PASS）。レート制限は入口で 120/分・10/分（8h は GAP のまま）。紛失 grant は S02 照合＋現金/返金、P-73 は Post-MVP（5h は GAP のまま）。hosting/IdP/backup は試用後に別提案。#18/#20 は close。

## 繁體中文

Owner 於 2026-10-19 一次採用 D1～D11。遠端 DEMO 採通行碼、僅 Mac 運行時可用、重啟即重置。Stripe 測試商戶 A 沿用、新建 B/C，由 Owner 以 Stripe 託管 onboarding 的測試資料完成（代理可用的 Stripe 連接器無法建立帳戶）；不使用含 jabiko.app 個資的兩個帳戶。CLI 與 `sk_test_` 由 Owner 本人處理。遠端刷卡為第二階段。ORDER_READ 維持 24 小時。共用平板列 Post-MVP。接受 STAFF_OPEN＋固定 QR 風險（3c 改為 PASS）。速率限制於入口設定 120/分、10/分（8h 仍為 GAP）。遺失續付憑證以 S02 對帳＋現金或退款處理，P-73 列 Post-MVP（5h 仍為 GAP）。託管／IdP／備份待試用回饋後另提。#18/#20 已關閉。
