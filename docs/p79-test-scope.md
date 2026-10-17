# P-79 cash, fulfillment and manual close

P-79 builds on the accepted P-78 immutable orders. It adds store cash settings,
CASH-only whole-table settlement, staff reception, one order per kitchen card,
whole-order service confirmation, and explicit close. React/TypeScript/Vite
remain in the same repository with pnpm; Go and PostgreSQL own all decisions.

## Transaction boundaries

Cash enablement and PREPAID/PAY_LATER mode apply to new orders. Each CASH order
saves the selected policy at commit; changing settings does not change existing
orders or prevent collection of existing debt. Missing/disabled settings reject
new CASH orders. Payment status and fulfillment status are separate.

A cash preview identifies the exact unpaid, non-cancelled CASH set and gross
amount for the current table session. Confirmation rechecks that set under the
session lock. A new order or changed set requires reconfirmation. One immutable
receipt and its per-order allocations are committed together with payment
state, audit events and eligible KDS release. Idempotent retries return the
original receipt. Later orders use a second settlement. All Stripe orders are
excluded, regardless of their payment state. There is no partial payment,
split bill, payment-method conversion, cross-table collection or POS integration.

The invoice uses each included order's saved gross lines and historical tax
rules. It groups by normalized rate and floors once per rate for the receipt;
it does not add separately rounded order tax totals or consult today's schedule.
The receipt retains its original order snapshots, cashier, register-session
reference, tendered amount and change. The register-session reference is an
opaque shift trace, not a new cash-drawer subsystem. No issuer registration is
invented and no qualified invoice claim is made.

PREPAID CASH waits for staff-confirmed payment; PAY_LATER CASH may reach the
kitchen while unpaid. DIRECT_SCAN also requires explicit staff reception.
Kitchen roles advance ACCEPTED → PREPARING → READY; authorized serving staff
advance READY → SERVED only when the entire order has been delivered. Close is
explicit and checks the same session transaction. READY alone cannot close,
and unknown/missing payment or operational state blocks close. Order/payment
history survives close, while old table-join grants and PINs are revoked.

## Authorization and runtime

Every staff request uses the existing `IdentityResolver` and current-membership
`Authorizer` ports. A guest TABLE_JOIN token cannot authorize staff operations.
CASH/serve/close require STAFF_CASHIER or STORE_MANAGER; kitchen transitions
require KITCHEN; cash settings require STORE_MANAGER or TENANT_ADMIN with MFA.
A tenant-admin membership does not automatically grant cashier operations.
The production `cmd/server` remains health-only until the real MOD-08 adapter
and deployment gates are accepted.

The explicit `cmd/operationstest` command is the handoff R2/R3 local test adapter.
It uses a loopback PostgreSQL database, a random disposable schema, real P-76
join capabilities, real order/cash transactions and random test staff credentials.
It does not create production accounts, JWTs or memberships. A protected local
credentials file provides tenant/store and test bearer tokens for the `/staff`
form. Tokens never belong in URLs, screenshots, review bundles or logs.

Build with `pnpm --dir web install --frozen-lockfile` and `pnpm --dir web build`.
Run `TEST_DATABASE_URL=postgres://... go run ./cmd/operationstest -credentials
/absolute/private/path.json` against a disposable loopback database; open
`http://127.0.0.1:8082/?table=test-table` for guests and `/staff` for staff.
Use the current command's flag help for optional settings. Shutdown drops only
its owned schema and removes only its owned credentials file.

## Acceptance boundaries

Validation must include real PostgreSQL races for simultaneous cashiers,
late orders, close/commit, repeated outbox delivery and cross-store access;
HTTP authorization/projection; historical invoice rounding; React component
behavior; and an Ego-lite guest → CASH → reception → collection → KDS → SERVED
→ close journey. Local fixtures and desktop Chromium do not qualify real
MOD-08 identity, payment terminals, physical-device latency or pilot operations.

P-80 owns Stripe Checkout/webhooks and reconciliation, P-81 advanced menu/HQ
holds, P-82 cancellation/refunds/review, and P-83 protected read/resume flows.
P-79 adds no operator endpoint to mark a Stripe order paid. Its Stripe-paid
acceptance dummy is isolated test data only. Production hosting/backups,
operational TTL/rate limits and pilot-device acceptance remain external gates.
