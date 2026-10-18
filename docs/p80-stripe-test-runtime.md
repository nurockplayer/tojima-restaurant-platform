# P-80 isolated Stripe Sandbox runtime

Source proposal against `main@46b2391d36a33b8a7298a81f9792bf15fdf7001c`.
Not built, Go-tested, browser-tested, dispatched or deployed in preparation.
Production server, IdP, ordinary keyless CI, P-82 refunds and hold clearance
remain outside this slice. The owner approved only three synthetic JP/JPY
individual test merchants, Stripe-managed defaults and connected-direct test
charges. This does not decide the future real merchant/payment architecture.

## What runs

`cmd/operationstest -stripe-sandbox -stripe-tenant A|B|C` is the existing
loopback-only disposable-schema operations runtime with the real existing
`stripeprovider` injected into the existing `payments.Service`. Its durable
attempts, immutable profile/account mapping, retry workers, signed webhook
inbox, original-browser continuation, S02 reconciliation and KDS gates are the
same implementation as keyless P-80. There is no second payment service or
manual mark-paid endpoint. The selected tenant gets a fixed synthetic identity;
A/B/C each uses its own disposable schema. Unknown refs/accounts fail closed.
Separate-schema Sandbox checks do not replace existing same-schema cross-tenant
relational tests.

The Python standard-library supervisor runs official Stripe CLI **1.54.0** with
an outbound connection and forwards Connect snapshot events only to the local
webhook route. No public backend, tunnel, DNS or certificate trust change is
needed. Its signing secret is captured directly into memory and never printed,
stored or included in errors. CLI stdout/stderr are discarded because normal
CLI startup otherwise prints its signing secret. API keys go only into a child
process environment, never argv, source or credential documents. No login is
persisted. Ctrl-C/termination stops both process groups and the Go runtime drops
only its owned schema and removes its owned local staff credential file.

## External prerequisites still required

1. Read back **three distinct connected merchant account IDs** and actual
   Sandbox onboarding/capability readiness. Creating an account alone is not
   payment readiness; a restricted account may reject Checkout. The platform
   `acct_1URlHu1Hm4CZe205` is never a merchant substitute. Account placeholders
   remain blank until all mappings are verified.
2. User safely supplies the authorized `sk_test_` key through the hidden terminal
   prompt or secure environment. The application only accepts this prefix,
   not live keys or unsupported restricted keys. The prompt does not save it.
3. Measured authorized runner, disposable loopback PostgreSQL, pinned Go,
   Node 24.15.x, repo-pinned pnpm 12.11.2, and an already installed official
   Stripe CLI 1.54.0. Do not install or run anything as part of source preparation.
4. Sandbox default snapshot API version equals `2026-09-30.endive`, matching
   `stripe-go/v87@87.0.0`. This was read back as both default and latest on
   2026-10-18; no version setting was changed. Recheck before acceptance if the
   account changes. Never weaken the adapter's exact version verification.
5. The separately published neutral static return page is reachable at
   `https://tojima-restaurant-platform-demo.pages.dev/stripe-test-return.html`.
   Source pins the exact origin/path and `?result=success` or `?result=cancel`.
   These are navigation labels only. The page makes no authoritative payment
   claim; payment status comes only from the existing local API and signed
   provider reconciliation. No secret or Stripe session ID is put in that URL.

## Configuration

Non-secret variables are listed in `docs/stripe-test.env.example`. The runtime
does not automatically load/source any file. Required settings:

- `TOJIMA_STRIPE_MODE=CONNECTED_DIRECT_TEST_FIXTURE`
- `TOJIMA_STRIPE_API_VERSION=2026-09-30.endive`
- `TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY=10000` (explicit isolated test ceiling)
- `TOJIMA_STRIPE_TEST_ACCOUNT_A/B/C`: actual distinct synthetic merchant IDs
- `TEST_DATABASE_URL`: disposable loopback PostgreSQL, never production/shared DB
- `TOJIMA_STRIPE_TEST_SECRET_KEY`: secure environment, or use `--prompt-key`

The wrapper supplies `TOJIMA_STRIPE_TEST_WEBHOOK_SECRET` directly to Go from the
CLI's local listener secret. It is not the secret of an unrelated Dashboard
endpoint. The direct Go command requires it but the wrapper is the preferred
route. No Sandbox webhook endpoint needs to be created in the Dashboard.
Keep production credentials, databases and merchants out of this environment.

Database merchant rows contain only the selected account ID, credential ref
`stripe-test:a:v1`/`b`/`c`, endpoint ref `stripe-test-cli`, and pinned API version.
The profile includes exact return URLs and amount ceiling, excluding rotatable
keys. Attempts snapshot the existing mapping/profile. Request timeout 10s,
retry 20h, continuation 30m and lease 30s are named test-only settings.

## Runnable local sequence after prerequisites

First, with **no Stripe secrets in the build process**, on an authorized measured
runner, use the pinned repo toolchain:

```sh
pnpm --dir web install --frozen-lockfile
pnpm --dir web check
pnpm --dir web build
go build -o /tmp/tojima-operationstest ./cmd/operationstest
```

Set only the non-secret configuration above. Start a fresh local test DB using
the existing README procedure. Then the user enters the test key without echo:

```sh
python3 scripts/run-stripe-sandbox.py --tenant A --port 8083 \
  --runtime-binary /tmp/tojima-operationstest --prompt-key \
  --credentials /tmp/tojima-stripe-a.json
```

Open `http://127.0.0.1:8083/?table=stripe-test-table-a`. Staff/kitchen URLs and
local staff credentials are in the requested 0600 file. Do not print/upload that
file. Do not use the keyless `-payments-fixture` flag with Sandbox mode.
The runtime rejects non-literal-loopback Host values; use `127.0.0.1`, not a
localhost alias. Provider Checkout URLs must be HTTPS on exactly
`checkout.stripe.com`. All return URLs are compile-time pinned. On the neutral
return page, return to the local app for authoritative status.

After stopping A, repeat the command for B then C with their own credential
paths and the corresponding `?table=stripe-test-table-b`/`c`. Do not reuse a
schema, merchant mapping or credential file from another run. A failed/ambiguous
provider outcome stays visible for reconciliation; do not retry by submitting
another order or creating another Checkout for the same order.

For startup guards only, add `--startup-check`. It requires the configured
runtime to come up, confirms Host spoofing returns 403 and unsigned webhook
returns 400, then tears down. This checks CLI authentication and runtime/guard
startup, **not** merchant capability readiness, real signed delivery, paid or
failed Checkout, or P-80 acceptance.

## Manual credentialed CI

`.github/workflows/stripe-test.yml` accepts only a manually dispatched exact
independently reviewed **main SHA** equal to the triggering SHA. There is no
PR/push trigger or arbitrary checkout ref. An initial separate job runs keyless
tests before the job using environment `stripe-test`. Configure that environment
to allow only trusted main deployments before dispatch. Read back actual
environment protections before activation; reviewed_sha equality proves commit
identity, not independent review. Apply the existing repository Oracle and owner
acceptance gates before promoting this code to main or dispatching it; this slice
does not require a new second human reviewer. It must not expose
secrets to PR or unreviewed code. Ordinary `ci.yml` is unchanged.

Environment Secret: `TOJIMA_STRIPE_TEST_SECRET_KEY` only. Environment Variables:
`TOJIMA_STRIPE_TEST_ACCOUNT_A`, `B`, `C`. API version/mode and 10000 JPY ceiling
are fixed in the reviewed workflow. The CLI secret is ephemeral and stays in
memory. Dependencies/build and official CLI installation occur before the one
secret-scoped command step. CLI v1.54.0 Linux x86_64 archive is pinned to the
SHA256 digest reported by its official release API. No raw outputs or credential
artifacts are uploaded. The workflow performs startup smoke for A/B/C only and
labels this boundary explicitly; it cannot claim interactive browser E2E.

## Real acceptance still owed

For each merchant, capture redacted exact SHA/command/run evidence for real
paid, failed, completed+unpaid/async and ambiguous/timeout behavior; signed
duplicate and reverse-order webhooks; wrong merchant/scope/amount rejection;
JPY/historical tax totals; original submitter resume vs another guest/ORDER_READ;
lost proof with S02 reconciliation; no second Checkout; KDS only after confirmed
payment and late-payment review holds. Keep private Checkout URLs, bearer/proof
tokens and secrets out of all reports. Use the actual browser and provider
outcomes, with existing keyless tests for controlled races and malformed data.
No fabricated success HTTP endpoint is added. Refunds/review clearance remain
P-82, outside this P-80 gate.

## Rollback and review

Remove this isolated source slice or stop the supervisor. Never roll back live
payment history; only the runtime-owned test schema is dropped. Do not dispatch
this source proposal until exact-SHA keyless CI and independent review pass.
No Go builds/tests or provider calls occurred while preparing it. Lightweight
keyless Python checks are recorded separately in the manifest. Source-only checks
and their omissions are listed in the accompanying manifest. Existing PR #18
is not closed/rewritten; the corrected handoff supersedes only its docs-only
instructions once this separate package is accepted.

Sources: `docs/p80-stripe-provider.md`, `docs/p80-test-scope.md`,
https://docs.stripe.com/cli/listen,
https://github.com/stripe/stripe-cli/wiki/using-stripe-api-keys,
https://github.com/stripe/stripe-cli/releases/tag/v1.54.0,
https://linear.app/higashijima-manage-system/issue/P-80#comment-d9cf7523,
https://github.com/nurockplayer/tojima-restaurant-platform/pull/18.
