# P-80 keyless payment implementation

This slice implements the P-80 payment boundary on accepted P-79 orders. It is
limited to isolated test infrastructure. The production command remains the
health server; real Stripe Test Mode and production activation are not claimed.

## Transaction and identity boundaries

An immutable STRIPE order creates one durable payment attempt and create-work
item in the same database transaction. The merchant account, credential
reference, request profile, JPY amount, historical tax snapshot, provider key,
and original browser proof binding are fixed for that attempt. Merchant
configuration is explicit per tenant/store. Missing configuration fails closed.
Credentials are external to the database and repository.

Provider network calls take place after a work lease is committed. Repeated
create work uses the same snapshotted request and idempotency key within a
configured window shorter than 24 hours. An ambiguous outcome after that window
requires reconciliation and cannot issue another Checkout. A changed provider
request profile cannot silently change an existing attempt. EXPIRE work shares
the immutable retry deadline and checks database time before each provider call.
After exhaustion it stops automated retries and leaves unresolved state UNKNOWN
for staff reconciliation, preserving existing PAID or REVIEW_REQUIRED states.

The browser context is signed and random, established before STRIPE commit in
an HttpOnly, Secure, SameSite cookie. The winning order stores only the token
hash and a fixed expiry no later than the browser proof expiry. TABLE_JOIN and
ORDER_READ do not create continuation authority. A lost cookie cannot be
replaced to claim an old attempt. Resume retrieves the existing Checkout and
rechecks ownership, session state, expiry, payment state and holds before
returning its private URL. Shared state and staff APIs contain no Checkout URL.

Raw webhook bytes are size bounded and signature verified before a durable
account/event inbox entry is acknowledged. Payment processing checks the
immutable merchant, object, scope, amount and currency relationships. Checkout
completion alone is not proof of payment. Verified PAID is monotone against
later failed/expired events; a late payment under a pre-release hold requires
review and cannot automatically release KDS. Payment, reception and fulfillment
remain separate, and no payment failure cancels the immutable order.

Staff S02 reconciliation retrieves provider state under current membership and
role checks. It cannot create a new Checkout, transfer a payment URL, switch an
order to CASH, or mark a payment successful by assertion. Broad hold management,
review resolution and refunds remain P-82 work.

The guest Checkout view refreshes on selection and when the page becomes visible,
then polls every 20 seconds while visible and unresolved. Periodic polling stops
for PAID, FAILED, EXPIRED and REVIEW_REQUIRED; those states remain visible.

## Isolated browser fixture

After building the web bundle, run the existing loopback test command with an
explicit disposable PostgreSQL URL and the additional `-payments-fixture` flag:

```sh
make web-check
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:5432/tojima_test?sslmode=disable' \
  go run ./cmd/operationstest -payments-fixture \
  -credentials /tmp/tojima-payment-fixture.json
```

The fixture creates an isolated schema, random test staff credentials and proof
keys, one explicit synthetic merchant mapping, and an in-process provider. It
never reads Stripe API keys or makes a Stripe request. Its private Checkout URLs
use the reserved `checkout.example.invalid` domain and **cannot accept payment**.
Provider outcomes can be driven by Go tests; there is no customer or staff HTTP
button that fabricates PAID. The 0600 credentials file also holds the synthetic
webhook signing value and is outside the served web directory. Do not print it,
commit it, or upload it with review evidence. Shutdown removes the owned file
and disposable schema. HTTP cookies are explicitly restricted to the literal
loopback fixture origin; production cookie configuration requires HTTPS.

Local fixture values such as the 30-minute continuation and 20-hour create-retry
windows are test settings. They do not decide production TTL policy.

## Configuration and remaining acceptance

The owner approved Git-ignored `.env.local` or secure environment variables for
local test credentials. Credentialed CI must use GitHub Actions **Environment
Secrets** in `stripe-test`; that environment exists with no credentials set.
Ordinary CI uses no Stripe credentials. Production uses a separate Secret
Manager after the hosting platform is selected.

Real Test Mode remains gated on an approved tenant/store-to-Stripe-test-account
mapping, the Connect charge mode, authorized API test keys, and endpoint signing
secrets. No Live key is accepted by the current provider adapter. Real paid and
failed browser flows, asynchronous payment behavior against Stripe, provider
dashboard reconciliation and physical pilot acceptance remain unclaimed until
those inputs are provided and the actual flows are verified.

Migration 006 is paired with a down migration for disposable test databases.
Do not run rollback against live payment history. Test teardown discards only
the schema created by that test. P-79 cash and tax history remains unchanged.

Sources: [P-80](https://linear.app/higashijima-manage-system/issue/P-80),
[P-34](https://linear.app/higashijima-manage-system/issue/P-34),
[P-64](https://linear.app/higashijima-manage-system/issue/P-64),
[P-72](https://linear.app/higashijima-manage-system/issue/P-72),
[P-73](https://linear.app/higashijima-manage-system/issue/P-73).
