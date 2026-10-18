# P-80 — Stripe Connect sandbox handoff / Stripe テスト接続 / Stripe 沙盒接手

Historical inspection on 2026-10-18 (JST) through the connected Stripe integration; not refreshed during offline S1 integration. **This document contains no credentials.**

## Verified / 確認済み / 已確認

| Item | Actual result |
| --- | --- |
| Sandbox label | Tachiko Choko 沙盒 |
| Platform Stripe account | `acct_1URlHu1Hm4CZe205` |
| Country / currency | JP / JPY |
| Mode | Sandbox only (`livemode=false`) |
| Stripe API account lookup | Successful |
| PaymentIntent list | Successful, no records at inspection |
| Connected-account list | Successful, **zero** accounts at inspection |
| Enable Connect | Successful (`enabled=true`) |
| Live charges / payouts | Disabled / not verified |
| Real Connect charge-mode acceptance | **Not yet performed** |
| Actual Stripe Checkout and signed webhooks E2E | **Not yet performed** |

The ChatGPT Stripe connector's sandbox access does **not** provision the Go application's `sk_test_` key, `whsec_` signing secret, or GitHub Actions Environment Secrets. Never treat connector authorization as a server API credential.

## Required secure handoff / 安全な次手順 / 安全後續

1. Create **three synthetic connected merchant accounts** for tenant A/B/C using Stripe **Sandbox → Connect** (or an authorized account-creation API). Record their returned distinct `acct_...` IDs here only after verified readback. Do not reuse the platform account as all three merchants; do not register real merchants without approval.
2. The owner explicitly approved **Connected Direct Charges in test mode** for these three JP/JPY synthetic merchants on 2026-10-18, using Stripe-managed defaults and synthetic individual test data only. This is compatible with the existing `CONNECTED_DIRECT_TEST_FIXTURE` adapter. If the chosen Connect charge model differs, do **not** silently flip the charge mode: update the ADR and adapter with independent review first.
3. For local runs, the Owner enters the authorized `sk_test_` key through the hidden prompt; the supervisor keeps it and the CLI `whsec_` only in memory/child environments. Never save the CLI signing secret. The separately gated credentialed CI uses only its existing `stripe-test` Environment API-key secret. Do not put tokens in code, docs, issues, logs or chat; unsupported `rk_test_` keys remain rejected.
4. Map each synthetic tenant/store to its verified connected account ID and a reference to the secure key, not the secret value. Keep checkout test traffic restricted to disposable tenants, with no live payment or production merchant configuration.
5. Run a dedicated test-mode verification for successful/failed/async or ambiguous Checkout; signed duplicate/out-of-order webhooks; per-merchant scope, JPY totals and tax snapshots; original-submitter resume; KDS release only after confirmed payment; P-82 hold expiry, manager review, full/partial refund creation and durable reconciliation, including failed/ambiguous outcomes and P-83 ORDER_READ adjustment/refund display. Real refund acceptance is still owed; refunds are now in scope, mapped to P-84 6f–6i. Collect reproducible command/CI evidence and independent review; do not mark P-80 Done until these gates pass.
6. Keep the normal keyless CI unchanged. If credentialed CI is approved, restrict it to a clearly separated manually authorized or tightly scoped workflow/environment. Never share test credentials with untrusted PR code.

## Technical constraints / 開発契約 / 工程邊界

Existing `internal/stripeprovider` explicitly requires a `sk_test_` key, `Stripe-Account: acct_...` on every request, JPY and `livemode=false`. `cmd/server` currently exposes health only and **does not load Stripe credentials or serve the production payment workflow**. Do not claim API E2E until the real test runtime is wired and exercised. Existing fake adapters remain keyless.

The Stripe integration available in the historical preparation session was able to enable Connect but did not expose a connected-account *creation* endpoint, and the GitHub connector did not expose Actions Secrets management. The tasks above remain open, not simulated as completed.

## Authority / 参照 / 依據

- Linear: https://linear.app/higashijima-manage-system/issue/P-80
- Existing provider contract: `docs/p80-stripe-provider.md`
- Existing test scope: `docs/p80-test-scope.md`
- Stripe official Connect account guidance: https://docs.stripe.com/connect/accounts-v2
- Never authorize live account onboarding or real transactions based on these sandbox checks alone.


## Follow-on source provenance

This file follows PR #18 `b1556ee9740a051f6b3a6d34ab0a91c9bb6ec407`
with isolated test-mode approval recorded; P-82 is now merged and its refund acceptance is included.
A separately reviewed source package adds the actual `operationstest`
`-stripe-sandbox` composition and local Stripe CLI forwarding; see
`docs/p80-stripe-test-runtime.md`. This supersedes only the docs-only handoff
once accepted. It does not close, rewrite, merge or claim the original PR #18,
release another writer, or mark P-80 Done. Merchant onboarding/capability
readiness, secure user key entry and real E2E acceptance remain separate.

## S1 current-main integration / 現行main統合 / 現行main整合

Base `5d82bce`; PR #20 `552dda4` includes PR #19 `8d0af68`. Local no-ff merge
completed with **no textual conflicts**. Main's migrations 001–012 and P-82/P-83/
P-84/#32 behavior are retained. Shared construction now binds both modes to
one payment service/session-close/order graph; the common HTTP runtime and
ProcessNext worker expose/consume review, stop, adjustment, refund and ORDER_READ
paths. CLI forwarding includes all ten handled events, including refund events;
both loaders enforce a positive ceiling no greater than ¥10,000.

JA: P-82の全額・部分返金、保留によるCheckout失効、店長審査とP-83表示は統合済みで、Sandbox受入対象。実Stripe受入は未実施。OwnerのCLI導入・加盟店onboarding・非表示key入力が必要。

ZH: P-82整筆／部分退款、hold導致Checkout失效、店長審核及P-83顯示均已整合，屬Sandbox驗收範圍。尚未執行真實Stripe驗收；Owner仍須安裝CLI、完成商戶onboarding及隱藏key輸入。

[Owner commands, cards and row-mapped acceptance checklist](p80-stripe-test-runtime.md#owner-runbook-one-command-startup-check-then-interactive-acceptance).
Local verification (2026-10-19, Go 1.27.1; no installs/downloads, provider calls
or real secrets). All commands below exited **0**. `--pull=never` used the
already present PG image to respect the offline constraint. Only this owned
container was started/stopped. The final commit carrying this receipt identifies
the integrated candidate; the merge parent is `9e1cce7`.

```sh
docker run -d --rm --pull=never --name tojima-astra-pg -p 127.0.0.1:55443:5432 -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=tojima_test postgres:17
export GOCACHE=/private/tmp/tojima-astra-go-cache GOPROXY=off GOTOOLCHAIN=local
gofmt -l $(rg --files cmd internal migrations -g '*.go')
go vet ./...
go build -o /private/tmp/tojima-astra-operationstest ./cmd/operationstest
TZ=UTC TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55443/tojima_test?sslmode=disable' go test -race -count=1 ./...
TZ=Asia/Tokyo TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55443/tojima_test?sslmode=disable' go test -race -count=1 ./...
python3 -m unittest discover -s scripts -p 'test_*.py'
make migration-smoke
docker stop tojima-astra-pg
```

Formatting: no filenames; vet/build: no errors. Both full race suites passed
all 19 test-bearing packages (server has no tests); payments took **84.333s UTC /
102.928s Tokyo**, HTTP **34.507s / 39.287s**, PostgreSQL migrations **4.934s /
7.407s**. Python: **8 tests OK**, including forwarding-list parity with service/
provider projections and the actual Connect CLI argv. Migration smoke: isolated
SQLite PASS; the Go runs separately exercised PostgreSQL migrations.

The new command-composition test passed in both timezones for keyless and
Sandbox modes: pending order/ORDER_READ, close gate, protected P-82 routes,
real-clock signed ingress, old/future signature rejection and durable duplicate
deduplication. Provider work remains queued in that test, so no Stripe call or
real paid/refund acceptance is inferred. Existing P-84 tests exercise processing,
hold/review/partial-refund/backoff paths. `.pnpm-store/` is absent; web source was
unchanged and no frontend install/test is claimed.

Fresh independent Oracle/Ego-lite review is still a Director gate; it was not
attempted under this session's no-network restriction. Owner must install/check
CLI 1.54.0, verify merchant onboarding/capabilities/API version, enter the key and
execute the signed Sandbox acceptance checklist (including refunds). No push,
PR, credentialed workflow dispatch, merchant mutation or production acceptance.
