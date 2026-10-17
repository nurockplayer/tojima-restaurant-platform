# P80 Stripe provider adapter

`internal/stripeprovider` is a small Stripe API transport adapter. It provides
Checkout Session create/retrieve/expire, PaymentIntent retrieve, and verified
webhook projection. It does not persist payment attempts, choose a merchant,
decide whether money is paid, or release an order.

## API and configuration

Construct it with `stripeprovider.New(Config)`. The constructor rejects an
empty or `UNCONFIGURED` mode, every mode except the explicit
`CONNECTED_DIRECT_TEST_FIXTURE`, any key other than a `sk_test_` API key, a
non-positive request timeout or JPY amount ceiling, and non-HTTPS trusted
success/cancel URLs. The adapter applies the configured HTTP timeout, disables
SDK retries and telemetry, and does not follow redirects. `HTTPClient` is
injectable for `httptest` only; the adapter contains no live-network test. The
caller resolves the exact test key from its secret configuration and passes it
at construction; this package defines no environment variable or secret store.

Every operation requires an `AccountContext` with that same fixture mode and
an explicit `acct_...` account ID. The ID is sent as Stripe's
`Stripe-Account` header on each API request. This is only explicit account
header plumbing under test-mode keys. It is not an accepted Connect funding,
merchant-of-record, payout, or real connected-account operating decision.
Production remains unconfigured until the owner supplies the concrete
merchant/account mapping, approved Stripe charge mode, authorized test
credentials, and endpoint signing secrets. No credential or merchant mapping
for a real merchant is checked into this repository. Synthetic mappings exist
only in the isolated test fixtures.

`CreateCheckout` accepts server-calculated positive integer JPY up to
`Config.MaxAmountJPY`, server-owned order/attempt/scope references, a stable
idempotency key, and a description. It sends one payment-mode line item with
quantity one, uses the configured trusted redirect URLs, and does not enable
Stripe Automatic Tax. Tenant/store/order/attempt correlation metadata is copied
to both the Checkout Session and the resulting PaymentIntent. The response is
checked for test mode, payment-mode Checkout, JPY, amount, correlation
metadata, valid Checkout status, and an HTTPS Checkout URL when present.
Retrieve and expire responses must retain the requested Stripe object ID. The
result contains a private Checkout URL; callers must only return
it to the authorized original submitter and must keep it out of shared order,
table, and list responses.

The public methods are:

- `CreateCheckout(ctx, CreateCheckoutRequest) (CheckoutResult, error)`
- `RetrieveCheckout(ctx, AccountContext, sessionID) (CheckoutResult, error)`
- `ExpireCheckout(ctx, AccountContext, sessionID, idempotencyKey) (CheckoutResult, error)`
- `RetrievePaymentIntent(ctx, AccountContext, paymentIntentID) (PaymentIntentResult, error)`
- `ReadWebhookBody(io.Reader) ([]byte, error)` for bounded HTTP ingress reads
- `VerifyWebhook(rawBody, signature, activeSecrets, trustedNow) (VerifiedEvent, error)`

`ReadWebhookBody` reads at most 1 MiB plus one detection byte before rejecting
oversized ingress. `VerifyWebhook` verifies the exact raw bytes through Stripe's pinned Go SDK,
accepts current plus previous endpoint secrets during rotation, enforces a
1 MiB body ceiling and a symmetric five-minute timestamp window (including
future timestamps), and requires the SDK API version and `livemode=false`.
Only Checkout Session and PaymentIntent events are projected. The webhook
projection omits Checkout URLs. Event account, object, amount, currency,
metadata, and payment state are data for the caller to compare to its durable
attempt; they do not establish tenant/store authority by themselves.

Provider errors are static typed sentinels and never wrap SDK responses. Checkout creation transport errors and unsafe/mismatched create responses
return `ErrOutcomeUnknown`; expiration transport or response errors use the
same classification. The adapter does not retry either mutation. The caller
must reconcile the already-persisted attempt rather than creating another
Checkout. `ErrNotFound` is reserved for a Stripe 404 on retrieval/expiration.
No errors or adapter logs contain the API key, webhook
secret, request body, or Checkout URL.

## Service responsibilities and limits

The `internal/payments` service owns the durable one-attempt-per-order invariant,
tenant/store-to-Stripe-account mapping, immutable attempt and request
fingerprint, original-submitter capability, provider account/scope/amount
checks, retry/idempotency horizon, webhook event deduplication, monotone
payment-state transitions, late-payment holds, and safe order-release policy.
Stripe idempotency retention is finite (at least 24 hours); the service requires
a configured retry window shorter than that and never issues a fresh
create request after an ambiguous result has passed its safe retry horizon.
The adapter alone makes no exactly-once or payment-fulfillment guarantee.

## Verification

`go test ./internal/stripeprovider` uses only `httptest` and an injected
round-tripper; it checks request headers and JPY form values, absence of
Automatic Tax, no retry after an ambiguous response, retrieve/expire behavior,
and signature rotation, exact-body verification, timestamp bounds, API
version, live-mode, size, and redaction boundaries. It does not use Stripe
keys, contact Stripe, or claim a real Stripe Test Mode E2E.

## Approved secret placement (2026-10-18)

The owner authorized local test credentials only in Git-ignored `.env.local` or
secure process environment variables. The current executable does not load
Stripe secrets or call Stripe. Do not commit populated files or print secrets.
Future credentialed GitHub Actions tests must consume **Environment Secrets**
from the repository environment `stripe-test`, not source files. That empty
environment has been created; no credentials are currently configured. Ordinary
CI runs the keyless test doubles and needs no Stripe secret.

Before real Test Mode can be enabled, the owner must supply the approved
tenant/store-to-test-account mapping, Connect charge mode, authorized test API
key configuration reference and endpoint signing secret configuration reference.
The backend merchant configuration stores the explicit per-store account and
references, while each attempt snapshots them so later configuration cannot
reroute an existing order. Credentials themselves stay out of the database.
Production Secret Manager selection remains dependent on the deployment
platform and is not decided by this test adapter.

The original submitter context is a separate signed random token from
`internal/checkoutproof`, established before commit by the payment HTTP adapter.
Each winning order binds only its hash and fixed expiry. This primitive does
not itself authorize a payment, issue an HTTP cookie, or replace TABLE_JOIN,
ORDER_READ, or MOD-08 staff identity.
