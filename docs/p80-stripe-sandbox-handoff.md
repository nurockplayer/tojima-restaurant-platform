# P-80 — Stripe Connect sandbox handoff / Stripe テスト接続 / Stripe 沙盒接手

Verified on 2026-10-18 (JST) through the connected Stripe integration. **This document contains no credentials.**

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
| Actual Stripe Checkout, signed webhooks, refund E2E | **Not yet performed** |

The ChatGPT Stripe connector's sandbox access does **not** provision the Go application's `sk_test_` key, `whsec_` signing secret, or GitHub Actions Environment Secrets. Never treat connector authorization as a server API credential.

## Required secure handoff / 安全な次手順 / 安全後續

1. Create **three synthetic connected merchant accounts** for tenant A/B/C using Stripe **Sandbox → Connect** (or an authorized account-creation API). Record their returned distinct `acct_...` IDs here only after verified readback. Do not reuse the platform account as all three merchants; do not register real merchants without approval.
2. Confirm **Connected Direct Charges in test mode** are appropriate for the existing `CONNECTED_DIRECT_TEST_FIXTURE` adapter. If the chosen Connect charge model differs, do **not** silently flip the charge mode: update the ADR and adapter with independent review first.
3. Store authorized `sk_test_...` and `whsec_...` only in a secure secret store or the existing GitHub Actions `stripe-test` **Environment Secrets**. Do not put tokens in code, docs, issues, CI logs, or chat. Avoid using `rk_test_` keys if unsupported by the current `sk_test_`-only adapter.
4. Map each synthetic tenant/store to its verified connected account ID and a reference to the secure key, not the secret value. Keep checkout test traffic restricted to disposable tenants, with no live payment or production merchant configuration.
5. Run a dedicated test-mode verification for successful/failed/async or ambiguous Checkout; signed duplicate/out-of-order webhooks; per-merchant scope, JPY totals and tax snapshots; original-submitter resume; KDS release only after confirmed payment; safe refund paths. Collect reproducible command/CI evidence and independent review; do not mark P-80 Done until these gates pass.
6. Keep the normal keyless CI unchanged. If credentialed CI is approved, restrict it to a clearly separated manually authorized or tightly scoped workflow/environment. Never share test credentials with untrusted PR code.

## Technical constraints / 開発契約 / 工程邊界

Existing `internal/stripeprovider` explicitly requires a `sk_test_` key, `Stripe-Account: acct_...` on every request, JPY and `livemode=false`. `cmd/server` currently exposes health only and **does not load Stripe credentials or serve the production payment workflow**. Do not claim API E2E until the real test runtime is wired and exercised. Existing fake adapters remain keyless.

The Stripe integration available in this ChatGPT session was able to enable Connect but did not expose a connected-account *creation* endpoint, and the GitHub connector did not expose Actions Secrets management. The tasks above remain open, not simulated as completed.

## Authority / 参照 / 依據

- Linear: https://linear.app/higashijima-manage-system/issue/P-80
- Existing provider contract: `docs/p80-stripe-provider.md`
- Existing test scope: `docs/p80-test-scope.md`
- Stripe official Connect account guidance: https://docs.stripe.com/connect/accounts-v2
- Never authorize live account onboarding or real transactions based on these sandbox checks alone.
