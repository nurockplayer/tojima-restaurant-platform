# P-80 isolated Stripe Sandbox runtime

S1 integration on `main@5d82bce`: merges PR #19 (`8d0af68`) and its
stacked review fixes PR #20 (`552dda4`). P-82 A–C2, P-83, P-84, migrations
001–012 and #32's injected-clock webhook verification are integrated.
Refunds, partial refunds, review clearance and hold-triggered Checkout expiry
are in scope for Sandbox acceptance. Production server/IdP deployment and
real merchant architecture remain separate. The approved charge mode is only
three synthetic JP/JPY individual test merchants with Stripe-managed defaults
and connected-direct test charges. Local validation is recorded in the handoff;
no real Stripe acceptance is claimed by this integration.

## What runs

`cmd/operationstest -stripe-sandbox -stripe-tenant A|B|C` is the existing
loopback-only disposable-schema operations runtime with the real existing
`stripeprovider` injected into the existing `payments.Service`. Its durable
attempts, immutable profile/account mapping, retry workers, signed webhook
inbox, original-browser continuation, S02 reconciliation and KDS gates are the
same implementation as current main’s keyless payment runtime. There is no second payment service or
manual mark-paid endpoint. The selected tenant gets a fixed synthetic identity;
A/B/C each uses its own disposable schema. Unknown refs/accounts fail closed.
Both modes call `configurePaymentGraph`: one `payments.Service` supplies the
session-close policy and ordering policy (including ORDER_READ grants).
`NewPaymentsRuntime` exposes the same review/adjustment/refund retry, kitchen
stop-request and reconciliation routes. The same 250ms `ProcessNext` worker
handles CREATE, EXPIRE, WEBHOOK, REFUND and REFUND_WEBHOOK; durable backoff and
reconciliation stay in the service. The sandbox provider embeds the existing
refund create/retrieve adapter. HQ hold obligations are created by the existing
catalog/DB path; the normal payment runtime does not expose catalog admin
routes. Controlled HQ-hold races remain covered by the P-84 harness.

The CLI forwards `checkout.session.completed`, `checkout.session.async_payment_succeeded`,
`checkout.session.async_payment_failed`, `checkout.session.expired`,
`payment_intent.succeeded`, `payment_intent.payment_failed`, `payment_intent.processing`,
`refund.updated`, `refund.failed`, and `charge.refunded`. The HTTP webhook handler
passes `time.Now().UTC()` to `VerifyWebhook`; #32 retains symmetric timestamp
validation before signature verification with the SDK's second clock check disabled.

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
   Stripe CLI 1.54.0. Installation and credentialed runs below are Owner actions.
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
- `TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY=10000` (maximum allowed ceiling; positive lower limits are allowed)
- `TOJIMA_STRIPE_TEST_ACCOUNT_A/B/C`: actual distinct synthetic merchant IDs; defaults are A `acct_1UUXcL1Hm4VOReK9`, B `acct_1UUXd51Hm4v7xrOu`, C `acct_1UUXdU1Hm4vaUfKX`. Environment values override these defaults.
- `TEST_DATABASE_URL`: disposable loopback PostgreSQL, never production/shared DB
- Keychain generic-password service `tojima-stripe-sandbox` stores the test key. The wrapper reads it with macOS `security`; `--keychain-service` selects another service. `--prompt-key` explicitly uses a hidden terminal prompt instead.

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

## Owner runbook: one-command startup check, then interactive acceptance

These are **Owner commands**, not actions performed during offline integration.
Run from the repository root with the pinned Go/Node/pnpm tools already available.
First install the official Homebrew tap formula and require exactly 1.54.0:

```sh
brew tap stripe/stripe-cli
brew install stripe/stripe-cli/stripe
stripe version
test "$(stripe version)" = 'stripe version 1.54.0'
```

The official tap tracks its current release; the version check is a hard stop if
it supplies a different version. Do not run the supervisor or weaken its pin in
that case; the Owner must arrange the approved 1.54.0 CLI. These installation
commands were not executed or checked online during S1.

In one shell, prepare a dedicated disposable PG 17 and build **before key entry**:

```sh
docker run -d --rm --name tojima-owner-stripe-pg \
  -p 127.0.0.1:55444:5432 \
  -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=tojima_test postgres:17
until docker exec tojima-owner-stripe-pg pg_isready -U postgres -d tojima_test; do sleep 1; done
export TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55444/tojima_test?sslmode=disable'
export TOJIMA_STRIPE_MODE=CONNECTED_DIRECT_TEST_FIXTURE
export TOJIMA_STRIPE_API_VERSION=2026-09-30.endive
export TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY=10000
# Optional: override only the non-secret default merchant account IDs.
# export TOJIMA_STRIPE_TEST_ACCOUNT_A='acct_...'
# export TOJIMA_STRIPE_TEST_ACCOUNT_B='acct_...'
# export TOJIMA_STRIPE_TEST_ACCOUNT_C='acct_...'
pnpm --dir web install --frozen-lockfile
pnpm --dir web check
pnpm --dir web build
STRIPE_RUN_DIR=$(mktemp -d /tmp/tojima-stripe-owner.XXXXXX)
chmod 700 "$STRIPE_RUN_DIR"
go build -o "$STRIPE_RUN_DIR/operationstest" ./cmd/operationstest
```

The IDs above are placeholders, not verified merchants. Confirm the three actual
mappings/capabilities and the Sandbox default snapshot API version before running.
Do not put the key in shell commands, files, chat or logs. The following single
command prompts without echo, starts the CLI/runtime, checks Host spoofing → 403
and unsigned webhook → 400, then stops both children and removes its schema and
local credential file:

```sh
python3 scripts/run-stripe-sandbox.py --tenant A --port 8083 \
  --runtime-binary "$STRIPE_RUN_DIR/operationstest" \
  --credentials "$STRIPE_RUN_DIR/tenant-a.json" --startup-check
```

A startup PASS is not signed delivery, merchant capability or payment acceptance.
Then run interactively (Keychain is read again and a disposable schema is used):

```sh
python3 scripts/run-stripe-sandbox.py --tenant A --port 8083 \
  --runtime-binary "$STRIPE_RUN_DIR/operationstest" \
  --credentials "$STRIPE_RUN_DIR/tenant-a.json"
```

Open `http://127.0.0.1:8083/?table=stripe-test-table-a`. Staff/kitchen URLs and
local fixture credentials are in the owner-only 0600 file; do not print/upload it.
Use literal `127.0.0.1`, not `localhost`. Do not combine Sandbox with fixture/demo/
catalog flags. Checkout is HTTPS on exactly `checkout.stripe.com`; the neutral
pinned return page only navigates back to the local app. It cannot prove payment.

Use these published [Stripe test cards](https://docs.stripe.com/testing#cards)
in hosted test Checkout, with a future expiry and any valid CVC:

| Journey | Test card | Expected observation |
| --- | --- | --- |
| Paid | `4242 4242 4242 4242` | Successful payment; local PAID only after verified provider outcome |
| Declined | `4000 0000 0000 9995` | Insufficient funds; no false PAID/KDS release |
| 3DS challenge | `4000 0000 0000 3220` | Required 3DS authentication; exercise completion and failure/cancel |

See also [Stripe authentication test cards](https://docs.stripe.com/testing#regulatory-cards).
3DS is an authentication test, **not proof of completed-unpaid → asynchronous
settlement**. Row 5i still needs an actual supported delayed-payment Sandbox
journey producing signed async events; do not relabel a 3DS or CLI-triggered
synthetic event as that evidence. Card references were not fetched during the
offline integration; the Owner should check them before acceptance.

Repeat startup and interactive commands for B/C with their own filenames and
`?table=stripe-test-table-b` / `c` entry links. For two-merchant isolation (5a),
keep A and B running concurrently in separate terminals on ports 8083/8084,
with the same non-secret environment and distinct credential paths. Do not
swap a running schema's mapping. Reconcile ambiguous outcomes against the same
attempt; do not submit a replacement order/Checkout to manufacture success.

Teardown: Ctrl-C each supervisor and wait for both children to stop. Only after
acceptance evidence is recorded without secrets, in the shell holding STRIPE_RUN_DIR:

```sh
docker stop tojima-owner-stripe-pg
rm -f "$STRIPE_RUN_DIR/operationstest" "$STRIPE_RUN_DIR/tenant-a.json" \
  "$STRIPE_RUN_DIR/tenant-b.json" "$STRIPE_RUN_DIR/tenant-c.json"
rmdir "$STRIPE_RUN_DIR"
unset STRIPE_RUN_DIR TEST_DATABASE_URL TOJIMA_STRIPE_MODE TOJIMA_STRIPE_API_VERSION
unset TOJIMA_STRIPE_TEST_MAX_AMOUNT_JPY TOJIMA_STRIPE_TEST_ACCOUNT_A
unset TOJIMA_STRIPE_TEST_ACCOUNT_B TOJIMA_STRIPE_TEST_ACCOUNT_C
```

## Sandbox acceptance checklist (P-84 EXT-1)

Record exact SHA, tenant/account mapping, command and redacted observed outcome;
never include Checkout URLs, bearer/proof tokens or secrets. The existing
[P-84 matrix](p84-acceptance-matrix.md) keeps automated PASS separate from external GAP.

| Rows | Required real Sandbox evidence |
| --- | --- |
| 5a | Two distinct connected merchants A/B: scoped Checkout/payment/refund objects; cross-account/scope/amount delivery cannot mutate the other merchant. Repeat ordinary flow for C. |
| 5i (5b–5e) | Paid, declined, completed-unpaid/async success/failure, expiry, ambiguous response and same-attempt reconciliation; signed duplicate/out-of-order events cannot duplicate payment or downgrade PAID. Return navigation is not evidence. |
| 5f–5h | Original-browser resume only; another guest/ORDER_READ cannot obtain Checkout authority. Lost-cookie recovery stays the separate Owner procedure gate; no second Checkout. |
| 6a–6e | HQ hold rejects new admission and expires the issued Checkout, retaining ambiguous expiry/backoff; a late paid outcome requires manager review and cannot release KDS. Exercise hold via the authorized catalog/service acceptance harness; this supervisor has no HQ admin mode. |
| 6f–6g | Authorized manager review vs full/partial cancellation, including kitchen-confirmed stop → finalize where preparation started. Refund REQUESTED/PROCESSING/FAILED are not success; show failed/timeout recovery, authorized retry, stable refund identity and provider-confirmed SUCCEEDED through signed refund events/reconciliation. |
| 6h–6i | Preserve original order/JPY/tax/payment identity; effective reduced totals and refund state appear in staff and ORDER_READ views. Check immutable refund/review/cancellation history and no duplicate refund on replay. |

Controlled timeout/race/malformed-event tests remain supporting local evidence.
Do not claim these happened at Stripe unless observed. No mark-paid endpoint,
manual refund-success shortcut or live charge is authorized by this runbook.

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

## Rollback and review

Stop the supervisor; only its owned disposable schema is dropped. Never roll
back payment history in a shared/live database. Exact-SHA validation and fresh
independent review remain promotion/dispatch gates. S1 does not push, open PRs,
rewrite PR #19/#20, call Stripe, handle keys or dispatch credentialed CI.
See the [integration handoff](p80-stripe-sandbox-handoff.md) for local test results
and remaining Owner onboarding, CLI installation and key-entry gates.

Sources: `docs/p80-stripe-provider.md`, `docs/p80-test-scope.md`,
https://docs.stripe.com/cli/listen,
https://github.com/stripe/stripe-cli/wiki/using-stripe-api-keys,
https://github.com/stripe/stripe-cli/releases/tag/v1.54.0,
https://linear.app/higashijima-manage-system/issue/P-80#comment-d9cf7523,
https://github.com/nurockplayer/tojima-restaurant-platform/pull/18.
