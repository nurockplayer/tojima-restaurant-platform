# P-84 lane A — MOD-05 S9 integration acceptance matrix

Source: `46b2391d36a33b8a7298a81f9792bf15fdf7001c` (`main`, abbreviated `46b2391`), 2026-10-18. Lane: `codex/p84-a-matrix`. This is a test and documentation handoff, not a release or pilot approval. No production code, existing test, migration, feature, or external account configuration is changed.

Read against README, `docs/contracts.md`, all P-76–P-81 test scopes, P-80 provider boundaries, existing PostgreSQL helpers, and migrations 001–007 (up/down). The eight quoted requirements below are the authoritative P-84 text supplied for this lane. Pending PR numbers are supplied handoff dependencies; their branch contents and live disposition are not evaluated by this source snapshot.

**Status rules:** PASS means the named executable assertions passed locally in their stated boundary. Payment PASS uses keyless synthetic provider fixtures unless explicitly stated otherwise. GAP means absent or incomplete acceptance evidence, including manual gates; it is not an observed defect. FAIL means a reproducible assertion contradicting merged behavior or the required acceptance contract. One HTTP denial-status defect was observed (5f); its two affected subtests use the required defect skip, which never counts as PASS. A source PASS is not evidence that a later PR passes. Each numbered sub-claim counts once; combined claims receive PASS only within the qualification in their description. Further external acceptance is explicitly separate.

`Source` is the corrected coverage disposition using tests already present at `46b2391`, plus any defect reproduced against its unchanged production code; `Lane A` includes the new files in the commit containing this document. The earlier `fe6058e` matrix overstated several source claims; this revision corrects those dispositions as well as the candidate evidence. `P84::TestName` always expands to `internal/httpapi/p84_acceptance_integration_test.go::TestName`. Every other evidence reference below is also `file::TestName` (Vitest uses `file::describe > it`). Candidate counts do not promote pending implementations or manual pilot decisions.

| Mandatory row | Source PASS / FAIL / GAP | Lane A PASS / FAIL / GAP | Disposition |
| --- | --- | --- | --- |
| 1. Tenant/store, catalog, RBAC, IDOR, HQ scale | 3 / 0 / 2 | **5 / 0 / 0** | Covered in local integration boundary |
| 2. Shared cart and atomic commit | 4 / 0 / 2 | **6 / 0 / 0** | Full keyless policy race added |
| 3. QR and next-party privacy | 3 / 0 / 2 | **4 / 0 / 1** | Fixed-QR remote risk requires pilot acknowledgement |
| 4. Cash, KDS and closing | 6 / 0 / 0 | **6 / 0 / 0** | Existing coverage reused |
| 5. Stripe isolation, webhook and resume | 3 / 1 / 5 | **4 / 1 / 4** | HTTP denial-status defect; Sandbox / capability dependencies |
| 6. Hold, review, cancellation and refunds | 1 / 0 / 8 | **3 / 0 / 6** | Quote race / confirmed expiry evidence and HQ/review/refund dependencies |
| 7. Guest refresh, receipt, accessibility/mobile | 2 / 0 / 5 | **2 / 0 / 5** | P-83 and browser/pilot acceptance |
| 8. Recovery, idempotency and security basics | 4 / 0 / 10 | **10 / 0 / 4** | Recovery/XSS added; perimeter/log/migration-failure gaps |
| **Total (61 sub-claims)** | **26 / 1 / 34** | **40 / 1 / 20** | **14 coverage gaps filled; 1 defect remains FAIL** |

## 1. Tenant/store isolation and company catalog

> 3 tenants × multiple stores; LINKED/DETACHED price/published master; staff per-store RBAC and unauthorized cross-tenant IDOR; company HQ hold affects 50 linked but not detached.

| ID and sub-claim | Source → Lane A | Concrete evidence and limit |
| --- | --- | --- |
| 1a. Three tenants, two stores each, repeated store/table/product keys in one schema | GAP → PASS | `P84::TestP84ThreeTenantsTwoStoresHTTPIsolation`: all six targets exist, hold committed orders, and have positive own-scope reads; every caller visits every target. Separate schemas are not used to manufacture tenant isolation. |
| 1b. LINKED master publication, absolute store price override, independent DETACHED snapshot | PASS → PASS | `internal/catalog/postgres_integration_test.go::TestPostgresCatalogAcceptance/draft publish and linked detached price semantics`; `internal/catalog/p81_scale_integration_test.go::TestPostgresP81CompanyCatalogScaleAcceptance` verifies 50 linked stores, detached store, other tenant, and historical order snapshot. |
| 1c. Store staff roles and current membership | PASS → PASS | `internal/httpapi/operations_integration_test.go::TestOperationsHTTPPermissionsAndCashJourney`; `internal/httpapi/menu_integration_test.go::TestMenuHTTPIntegration`; `P84::TestP84ThreeTenantsTwoStoresHTTPIsolation` adds each store's cashier/kitchen/manager scope, kitchen settings denial and immediate membership revocation. Fixture memberships do not qualify production MOD-08. |
| 1d. Cross-tenant and cross-store IDOR: session menu/cart/orders/operations and staff menu/operations | GAP → PASS | `P84::TestP84ThreeTenantsTwoStoresHTTPIsolation`: exact HTTP 403 for foreign existing resources, including denied settings writes; audit count proves no extra settings writes. Public locator menus are intentionally public; this claim concerns protected session/admin endpoints. |
| 1e. Company hold reaches 50 LINKED stores, excluding DETACHED and another tenant | PASS → PASS | `internal/catalog/company_product_hold_integration_test.go::TestPostgresCompanyProductEmergencyHoldCASReplayPublishAndDetach` checks all 50 linked projections, detached/foreign projections and unchanged price/options/availability. |

## 2. Three guests, cart editing and one immutable order

> Same table 3 phones one OPEN session + shared cart add/delete/qty/options; simultaneous A=Stripe, B=CASH commit = exactly 1 immutable order + 1 payment method; stale quote rejection and generation rollover.

| ID and sub-claim | Source → Lane A | Concrete evidence and limit |
| --- | --- | --- |
| 2a. Three distinct guests share one OPEN seating | PASS → PASS | `internal/tablesession/postgres_integration_test.go::TestPostgresSessionAcceptance/concurrent staff opens have one session and three clients join it` and `/direct scan creates one staff acceptance session atomically`; the P84 race also checks three distinct grants for one session. Guests are service clients, not physical phones. |
| 2b. Shared add/delete/absolute quantity/options, CAS and safe replay | GAP → PASS | `internal/ordering/postgres_integration_test.go::TestPostgresSharedCartAndOrderAcceptance` proves add/quantity/removal and replay but preserves the existing option selection during quantity edits. `P84::TestP84SharedOptionEditAndHeldLineRemoval` now changes yuzu to ginger using another guest, checks absolute quantity and the third guest's persisted read, rejects a stale edit, and replays without mutation. `web/src/ui/App.test.tsx::guest app mount > switches single-select options, clears optional choices, protects required groups, and replays a 503 with the same request` covers the mocked UI boundary. |
| 2c. Concurrent STRIPE/CASH commit with real keyless payment policy leaves one method and no orphan attempt | GAP → PASS | `P84::TestP84ThreeGuestsStripeCashCommitRace`: six generations; barrier-started contenders converge on one order/method, one operational state, exactly one attempt/CREATE work for a STRIPE winner and zero for a CASH winner. Does not prescribe the scheduling winner or call Stripe. |
| 2d. Committed order and winning method immutable | PASS → PASS | `internal/ordering/postgres_integration_test.go::TestPostgresSharedCartAndOrderAcceptance/concurrent add CAS, idempotency, immutable order, and next generation` rejects order UPDATE/DELETE and conflicting replay; preserves copied price/options/tax. |
| 2e. Stale cart/price/publication/availability/tax quote requires fresh consent | PASS → PASS | Same ordering test; `internal/ordering/company_hold_integration_test.go::TestPostgresCompanyEmergencyHoldBlocksQuoteAndCommitWithoutLosingCart` adds hold generation changes. |
| 2f. Consumed generation clears atomically; next generation visible to third guest | PASS → PASS | Existing ordering test above; `P84::TestP84ThreeGuestsStripeCashCommitRace` checks third guest sees generation + 1, version 1 and no lines after every race. |

## 3. QR entry, acceptance and next-party privacy

> Staff QR modes STAFF_OPEN/SESSION_PIN/DIRECT_SCAN, PIN limit and fixed QR remote risk, staff kitchen acceptance; close/reopen new guest same physical QR cannot access previous private order.

| ID and sub-claim | Source → Lane A | Concrete evidence and limit |
| --- | --- | --- |
| 3a. STAFF_OPEN / SESSION_PIN / DIRECT_SCAN entry contracts | PASS → PASS | `internal/tablesession/postgres_integration_test.go::TestPostgresSessionAcceptance`; `internal/httpapi/sessions_integration_test.go::TestSessionHTTPIntegration`. Mode snapshot and future-session settings are covered. |
| 3b. Persistent PIN attempt/lockout/reset and grant limits | PASS → PASS | `internal/tablesession/postgres_integration_test.go::TestPostgresSessionAcceptance/PIN lockout persists across service instances and reset revokes grants`, `/join grant cap is persistent per table and shared by direct and session joins`. Operating thresholds remain pilot decisions. |
| 3c. Fixed-QR remote-entry risk understood and accepted | GAP → GAP | `docs/p76-test-scope.md` explains that possession of a photographed locator does not prove physical presence. No automated test can establish guest presence or approve the operational risk. Owner must select mode, PIN/TTL/rate policy and staff acceptance workflow. |
| 3d. DIRECT_SCAN requires staff reception before eligible KDS release | PASS → PASS | `internal/operations/service_integration_test.go::TestPostgresCashSnapshotSettlementConcurrencyAndCloseGate`; `internal/httpapi/operations_integration_test.go::TestOperationsHTTPPermissionsAndCashJourney`; `internal/httpapi/payments_lifecycle_integration_test.go::TestPaymentsHTTPProofWebhookAndStaffLifecyclePostgres` checks paid-but-unaccepted Stripe exclusion. |
| 3e. Same physical QR after explicit close creates private new party | GAP → PASS | `P84::TestP84SameQRNextPartyPrivacy`, subtests `STAFF_OPEN`, `SESSION_PIN`, `DIRECT_SCAN`: commit/pay/serve/close old order, leave an abandoned cart line, reopen at unchanged locator, assert new session ID and empty cart/orders; deny old and new tokens against old session and old token against new session menu/cart/orders/operations. History remains staff-owned. |

## 4. Cash, fulfillment and explicit close

> Cash PREPAID/PAY_LATER and one cash settlement multiple unpaid CASH orders; Stripe previously paid excluded from cash; every committed order -> only one KDS card, READY isn't SERVED, S02 explicit closing gate.

| ID and sub-claim | Source → Lane A | Concrete evidence and limit |
| --- | --- | --- |
| 4a. PREPAID/PAY_LATER snapshots and release gates | PASS → PASS | `internal/operations/service_integration_test.go::TestPostgresCashSnapshotSettlementConcurrencyAndCloseGate`; `internal/operations/service_integration_test.go::TestPostgresLegacyStripeStateFailsClosedAndCashWithoutPolicyIsDenied` in that same file. |
| 4b. One receipt allocates multiple unpaid CASH orders; concurrent cashiers/replay/late-order reconfirmation | PASS → PASS | `internal/operations/service_integration_test.go::TestPostgresCashSnapshotSettlementConcurrencyAndCloseGate` and `internal/operations/service_integration_test.go::TestPostgresStripeGateAndExactCashInvoice` (historical inclusive-tax receipt rounding). |
| 4c. Previously PAID Stripe excluded from cash | PASS → PASS | `internal/operations/service_integration_test.go::TestPostgresStripeGateAndExactCashInvoice`. Its Stripe PAID setup is explicit isolated fixture data, not proof of a Stripe payment. |
| 4d. At most one KDS card per eligible committed order; retries safe | PASS → PASS | Same operations tests; migration 005 unique order key and gating trigger; `P84::TestP84RestartOutboxRetryAndMissedEventResync` proves one card/event across failed delivery and restart. Unpaid PREPAID/Stripe and unaccepted DIRECT_SCAN intentionally wait; commitment alone does not authorize release. |
| 4e. READY is not SERVED and blocks close | PASS → PASS | `internal/operations/service_integration_test.go::TestPostgresStripeGateAndExactCashInvoice`; `internal/httpapi/operations_integration_test.go::TestOperationsHTTPPermissionsAndCashJourney`. |
| 4f. S02 explicit role-checked close, paid/served/hold gates, close/commit serialization | PASS → PASS | `internal/operations/close_race_integration_test.go::TestPostgresGuestCommitSerializesWithClose`, `internal/operations/close_race_integration_test.go::TestPostgresCashSettlementStillRequiresServedBeforeClose`; operations HTTP test above. No automatic table closing is accepted. |

## 5. Stripe merchant isolation and private continuation

> Stripe Connect per-merchant isolation; webhook signature, async payment true PAID, replay/out-of-order, expired vs paid vs unknown, submitter-only Checkout resume with ORDER_READ refusal and original lost grant staff verification.

For repeated references below, `PaymentService` = `internal/payments/service_integration_test.go::TestPostgresStripeCommitResumeWebhookAndReviewGate`; `PaymentHTTP` = `internal/httpapi/payments_lifecycle_integration_test.go::TestPaymentsHTTPProofWebhookAndStaffLifecyclePostgres`. Both execute PostgreSQL but use an in-process synthetic provider. This qualification applies to every payment PASS below.

| ID and sub-claim | Source → Lane A | Concrete evidence and limit |
| --- | --- | --- |
| 5a. End-to-end merchant-to-merchant isolation | GAP → GAP | `internal/stripeprovider/provider_test.go::TestCreateCheckoutUsesScopedTestModeJPYAndStableIdempotency` checks the account header; `PaymentService` rejects a wrong-account webhook. One synthetic merchant is not a multi-merchant Connect acceptance journey. Pending P-80 Sandbox. |
| 5b. Exact raw webhook signatures verified before durable acknowledgement | PASS → PASS | `internal/stripeprovider/provider_test.go::TestVerifyWebhookExactBytesRotationAndSafeProjection`, `internal/stripeprovider/provider_test.go::TestVerifyWebhookRejectsOldFutureLiveWrongVersionAndOversize`; `PaymentHTTP` checks signed HTTP ingress and durable inbox/work before processing. |
| 5c. Completed-unpaid is PROCESSING; verified paid outcome is PAID | PASS → PASS | `PaymentService` processing/cutoff and paid retry assertions; `PaymentHTTP` signed verified-paid completion processing. This proves fixture state transitions, not a true external asynchronous payment. |
| 5d. Duplicate events and out-of-order failure/expiry/paid do not duplicate or downgrade payment | GAP → PASS | `PaymentService` covers failure → delayed expiry → paid; `PaymentHTTP` covers duplicate signed ingress. `P84::TestP84PaidCheckoutIgnoresStaleFailureAndExpiry` adds PAID → older `payment_intent.payment_failed`, `checkout.session.async_payment_failed`, and `checkout.session.expired`, processing each after PAID. Complete attempt, original order, operational state, payment/operation history, KDS and outbox rows remain identical; each inbox event is processed and work DONE without error/lease; replay creates no new work. Source lacked this post-PAID evidence. |
| 5e. Expired/paid/unknown distinctions and bounded same-attempt reconciliation | PASS → PASS | `PaymentService`: expiry cutoff cases preserve confirmed states; unknown cutoff stops provider retries; explicit reconciliation retrieves the same Checkout. |
| 5f. Original submitter proof alone resumes existing Checkout; shared guest refused with HTTP 403 and no URL | FAIL → FAIL | `P84::TestP84OpenCheckoutResumeRefusesOtherBrowser` first commits and processes CREATE, confirms provider/local OPEN and unpaid, then uses a second guest's independently issued cookie. With and without that guest's bearer, actual response is **409 `payment_unavailable`, expected 403**; no URL/Location is exposed. These two subtests have conditional `t.Skip("P-84 defect: ...")` only for that exact known response. Original cookie resumes the same Checkout; missing original cookie is 403/no URL. `PaymentHTTP` only denies context issuance on closed/expired sessions, not resume of an existing OPEN Checkout after session expiry. Lost-grant recovery remains 5h; do not infer it from missing-cookie refusal. Repro below. |
| 5g. ORDER_READ explicitly refused as CHECKOUT_RESUME authority | GAP → GAP | Protected ORDER_READ flow is absent at this source; ordinary TABLE_JOIN refusal is not this claim. Pending P-83 and P-82 continuation seams. |
| 5h. Original lost grant recovery via staff verification | GAP → GAP | Current staff reconciliation is tested, but approved lost-grant verification/receipt recovery is not. Pending P-83/P-82; no replacement identity or checkout transfer is invented. |
| 5i. Actual Sandbox Connect mapping, signed async payment, expiry/reconciliation journey | GAP → GAP | P-80 Sandbox #19/#20, credential/mapping and actual provider acceptance. Keyless tests cannot fill this gate. |

## 6. HQ hold races, review and refunds

> HQ emergency hold during quote/order/Checkout races, retained blocked cart line, confirmed Stripe expiry, paid but pending manager KDS review, approve/cancel/partial refund with refund fail/timeout. Immutable order/amount/ledger/history.

| ID and sub-claim | Source → Lane A | Concrete evidence and limit |
| --- | --- | --- |
| 6a. HQ hold serializes with quote/order admission | GAP → GAP | `internal/ordering/company_hold_integration_test.go::TestPostgresCompanyEmergencyHoldBlocksQuoteAndCommitWithoutLosingCart` proves quote refusal after an active hold and stale-consent refusal after ON/OFF. `internal/ordering/company_hold_integration_test.go::TestPostgresCompanyHoldSerializesWithCommitInBothOrders` forces both hold/commit lock orders; `internal/catalog/company_product_hold_operations_integration_test.go::TestPostgresCompanyHoldReadFailureRejectsCommitWithoutMutation` proves failure closure. None forces a simultaneous hold/quote-preview race. That concurrency portion remains GAP; the combined claim cannot be PASS. |
| 6b. HQ hold reaches issued Checkout across create/persist races | GAP → GAP | `PaymentService` models an operational hold in provider create hooks; this does not prove complete company-HQ-to-Checkout propagation. Pending P-82 #22/#25 + C. |
| 6c. Blocked cart line retained/removable; new consent after hold | GAP → PASS | `P84::TestP84SharedOptionEditAndHeldLineRemoval` reads the retained `COMPANY_EMERGENCY_HOLD` line, removes it while the company hold is still active, and checks another guest sees the empty cart. The company-hold ordering test in 6a proves fresh consent after ON/OFF. Previously, service removal covered store PAUSED and company-hold removal was only a mocked UI assertion (`web/src/ui/App.test.tsx::guest app mount > shows a company hold in both menu and retained cart, blocks additions and quote, and still allows removal`). |
| 6d. Provider-confirmed expiry and bounded ambiguous expiry retries | GAP → GAP | `PaymentService` covers synthetic operational-hold expiry retries, cutoff with no more provider calls, and payment completion racing expiry. Its EXPIRED case is injected via a verified webhook projection; it does not assert that an unpaid provider-confirmed `ExpireCheckout` result reaches local EXPIRED with work completed. `internal/stripeprovider/provider_test.go::TestRetrieveExpireAndPaymentIntentUseExplicitAccount` tests only the adapter. The combined worker-confirmation claim remains GAP. Actual Stripe confirmation also remains 5i. |
| 6e. Paid under hold requires review and cannot release KDS | GAP → PASS | `P84::TestP84AcceptedPaidReviewOrderExcludedFromKDS` processes payment under a fixture operational hold, clears only that hold in the fixture, preserves REVIEW_REQUIRED on another paid event, then performs real staff acceptance with hold=false/review=true. The returned order is PAID, staff-accepted, WAITING and review-required; authorized kitchen HTTP returns no card and DB has no card, KDS_RELEASED history or outbox entry. This exercises the release path with reception satisfied and review as the remaining gate. Source tests covered review and reception in separate scenarios. HQ propagation and manager disposition remain 6b/6f. |
| 6f. Manager approve/cancel decisions with scoped authorization | GAP → GAP | Pending P-82 #22/#25 + C; no new tests against unmerged behavior. |
| 6g. Partial refund including failure/timeout | GAP → GAP | Pending P-82 #22/#25 + C; no synthetic REFUNDED assertion is substituted. |
| 6h. Original order/amount/tax/payment identity immutable | PASS → PASS | Shared-cart ordering test rejects committed-order UPDATE/DELETE and preserves copied price/options/tax; `internal/catalog/company_product_hold_operations_integration_test.go::TestPostgresCompanyHoldPreservesExistingCashOrderAndKDSCard` compares historical snapshots across HQ hold ON/OFF. `PaymentService` rejects attempt ID, Checkout/PaymentIntent binding and paid-state downgrade mutations. `P84::TestP84PaidCheckoutIgnoresStaleFailureAndExpiry` also compares complete order/attempt/history rows across stale negative events. These assertions do not qualify a refund ledger or manager review history (6i GAP). |
| 6i. Refund ledger and review/cancellation history invariant | GAP → GAP | Pending P-82; current original-order immutability does not qualify the unmerged ledger/refund lifecycle. |

## 7. Guest synchronization, private receipts and mobile access

> Guest 30sec status vs shared cart near-realtime distinct, background stops, stale state and receipt link/capability isolation, accessibility Japanese/Traditional Chinese and WebKit/Chromium mobile path.

| ID and sub-claim | Source → Lane A | Concrete evidence and limit |
| --- | --- | --- |
| 7a. 30-second guest order-status cadence | GAP → GAP | `web/src/ui/App.test.tsx::guest app mount > uses a 20-second visible cadence and refreshes immediately when the page becomes visible again` proves **20 seconds** at this source, as documented in P-78/P-80. The P-84 target says 30 seconds; pending P-83 must resolve/retest, not silently relabel this PASS. This is a pending contract difference, not a newly discovered merged regression. |
| 7b. Shared cart near-realtime distinct from order status, including visibility | GAP → GAP | Implemented 2.5-second visible cart-tab reads are described by P-78 and visible in `web/src/ui/App.tsx`; the three-client service tests prove shared state, but do not measure propagation between mounted guests. Pending P-83/UI acceptance. |
| 7c. Background periodic menu/payment-status reads stop | PASS → PASS | `web/src/ui/App.test.tsx::guest app mount > revalidates the menu only while visible and reflects ON then OFF without clearing store PAUSED state`; `web/src/ui/App.test.tsx::guest app mount > uses a 20-second visible cadence and refreshes immediately when the page becomes visible again`; terminal-state polling tests. Cart timing remains 7b. |
| 7d. Late results cannot overwrite newer generation/selected order | PASS → PASS | `web/src/ui/cart-state.test.ts::guest cart UI state > does not let a slow poll overwrite a newer mutation or cart generation`; `web/src/ui/App.test.tsx::guest app mount > ignores a delayed payment status after the guest selects another Stripe order`, `web/src/ui/App.test.tsx::guest app mount > does not navigate or change the newer payment view when an old resume request finishes late`. |
| 7e. Private receipt link and capability isolation | GAP → GAP | Pending P-83 #21/#24; current TABLE_JOIN and original-browser proof tests do not establish ORDER_READ receipt-link isolation. |
| 7f. Japanese/Traditional Chinese accessible journey | GAP → GAP | `web/src/ui/CatalogApp.test.tsx::catalog admin UI > keeps the document language aligned with the Japanese and Traditional Chinese toggle` and StaffApp locale tests support locale behavior only. No complete assistive-technology/guest receipt journey is recorded. Pending P-83 plus pilot acceptance. |
| 7g. WebKit and Chromium mobile paths | GAP → GAP | No new browser infrastructure or browser journey claimed in lane A. Pending P-83 and manual device/browser receipts. |

## 8. Operational failures and security basics

> Operational failures: server restart/outbox retry, DB transaction rollback/migration, double-click/double webhook, failed payment vs new order, missed events, basic rate limit/secret leakage/XSS/CSRF protection.

| ID and sub-claim | Source → Lane A | Concrete evidence and limit |
| --- | --- | --- |
| 8a. Service restart, outbox retry and expired payment work lease | GAP → PASS | `P84::TestP84RestartOutboxRetryAndMissedEventResync` recreates operations service over the same schema; failed delivery retains attempts; acknowledgment removes only pending notification. `P84::TestP84LateStripeCommitFailureRollsBackAllSideEffects` recreates payment workers, loses create response, models an expired crashed-worker lease, and recovers with two requests/one provider Checkout. This simulates services, not OS reboot or provider outage. |
| 8b. Failure late in commit rolls back every transactional side effect | GAP → PASS | `P84::TestP84LateStripeCommitFailureRollsBackAllSideEffects` injects a test-only trigger failure at cart rollover after order/operational state/attempt/work/replay writes. Cart is byte-for-byte equivalent as a Go value; eight side-effect tables remain empty. Remove trigger, retry identical input successfully. Existing ordering rollback tests fail earlier at order insertion; this extends the payment boundary. |
| 8c. Paired migrations up/down/reapply, repeat-up idempotency | PASS → PASS | `migrations/postgres/migrate_integration_test.go::TestPostgresMigrationsUpDownReapply`; `internal/ordering/postgres_integration_test.go::TestPostgresOrderingMigrationDownUp`; `make migration-smoke` is explicitly only the legacy isolated SQLite harness. No new migration. |
| 8d. Double-click and double webhook idempotency | PASS → PASS | Shared-cart ordering test (add/commit replay), cash snapshot settlement test (receipt replay), `PaymentHTTP` (raw signed duplicate webhook), `PaymentService` (dedupe/concurrent work). |
| 8e. Failed Stripe payment stays distinct from a new CASH order | GAP → PASS | `P84::TestP84FailedStripeOrderDoesNotBlockNextCashGeneration`: process failure on generation 1, commit generation 2 as CASH, preserve two orders/two methods, one Stripe attempt and old FAILED state. Payment failure neither converts nor deletes the old order. |
| 8f. Missed event recovers authoritative state | GAP → PASS | `P84::TestP84RestartOutboxRetryAndMissedEventResync`: after notification acknowledgment, a fresh authorized kitchen HTTP list still returns exactly one card. Payment reconciliation of same Checkout is separately covered by `PaymentService`; no websocket or automatic event-stream reconnect claim. |
| 8g. Basic PIN/join rate bounds | PASS → PASS | Session acceptance PIN lockout and persistent table grant-cap subtests cited in 3b. |
| 8h. Payment/API perimeter abuse and operational rate policy | GAP → GAP | No acceptance test for broad payment/staff/API throttling or approved pilot thresholds; request-size bounds and PIN limits alone do not establish this. Owning follow-up must select limits and test burst behavior without new infrastructure in this lane. |
| 8i. Secret-free public DTOs and safe provider/network errors | GAP → PASS | `internal/httpapi/payments_test.go::TestPaymentStatusProjectionContainsNoProviderURLOrIdentifiers` asserts an explicit forbidden-field list; `PaymentHTTP` checks real populated fixture responses; `internal/httpapi/ordering_integration_test.go::TestOrderingGuestHTTP` checks quote/list privacy. `internal/stripeprovider/p84_acceptance_test.go::TestP84ReturnedProviderErrorRedactsSyntheticSecrets` inspects the **actual returned error**, including wrapper text, for synthetic API-key, webhook-secret, Checkout-URL and private-detail markers supplied through HTTP 500 and transport errors; one provider request and no returned URL. Source only inspected `ErrOutcomeUnknown.Error()` and lacked this evidence. `web/src/shared/http.test.ts::shared HTTP core > does not expose the URL in network failure messages` separately asserts the fixed client error message. This finite set of projections/error paths does not qualify logs or every possible provider failure (8j). |
| 8j. Runtime logs do not leak credentials, browser proof or private checkout data | GAP → GAP | No log-capture acceptance test found. SDK logging is disabled in provider construction, but configuration/source inspection is not executable log evidence. Follow-up should capture one provider error and webhook/retry lifecycle with synthetic secret markers. |
| 8k. Untrusted menu category/product/description rendered as text | GAP → PASS | `web/src/ui/App.p84.test.tsx::P84 renders untrusted menu category, product and description as literal text`: mounts real App with script/img/svg payloads; asserts literal text and absence of injected elements/event attributes. This is a DOM-structure test, not reliance on jsdom executing attacks or a full security audit. |
| 8l. Checkout mutation CSRF basics: exact Origin/Host and independent proof | PASS → PASS | `internal/httpapi/payments_test.go::TestPaymentsHTTPOriginMustMatchExactConfiguredOrigin`, `internal/httpapi/payments_test.go::TestPaymentsHTTPOriginMustMatchRequestHost`, `internal/httpapi/payments_test.go::TestPaymentsAPIGuestMutationsRequireOriginAndProof`; `PaymentHTTP` checks foreign Origin/Host and HttpOnly/SameSite fixture cookie. HTTPS Secure-cookie behavior remains production configuration. |
| 8m. Whole deployed application CSRF/CORS/session boundary | GAP → GAP | Checkout-specific tests do not qualify every deployed route, real MOD-08 cookie adapter, ingress or CORS configuration. Pilot/security owner must review the actual deployment. |
| 8n. Injected migration failure recovery and real backup/restore | GAP → GAP | Existing migration cycle tests cover a successful round trip; no induced mid-migration failure or production restoration rehearsal. Transactional migration runner inspection is not a substitute. Hosting/recovery owner must approve backup/RPO/RTO and execute recovery separately. |

## New tests and defects

Ten new PostgreSQL test functions in `internal/httpapi/p84_acceptance_integration_test.go` reuse the existing fixtures:

- `TestP84ThreeTenantsTwoStoresHTTPIsolation` — 1a/1d, reinforces 1c.
- `TestP84ThreeGuestsStripeCashCommitRace` — 2c, reinforces 2a/2f; grant distinctness now checks every pair.
- `TestP84SameQRNextPartyPrivacy` — 3e across all three QR modes.
- `TestP84RestartOutboxRetryAndMissedEventResync` — 8a/8f, reinforces 4d.
- `TestP84LateStripeCommitFailureRollsBackAllSideEffects` — 8a/8b.
- `TestP84FailedStripeOrderDoesNotBlockNextCashGeneration` — 8e.
- `TestP84OpenCheckoutResumeRefusesOtherBrowser` — 5f FAIL; original-cookie and missing-cookie controls pass, two known-defect subtests skip.
- `TestP84PaidCheckoutIgnoresStaleFailureAndExpiry` — 5d.
- `TestP84AcceptedPaidReviewOrderExcludedFromKDS` — 6e.
- `TestP84SharedOptionEditAndHeldLineRemoval` — 2b/6c.

One new provider test file, `internal/stripeprovider/p84_acceptance_test.go`, contains `TestP84ReturnedProviderErrorRedactsSyntheticSecrets` (8i). The existing lane-created Vitest file `web/src/ui/App.p84.test.tsx` covers 8k. No pre-lane test files or production code were edited.

**Observed defect (5f):** production remains identical to main `46b2391d36a33b8a7298a81f9792bf15fdf7001c`. Initial strict run on the revised tests failed both `shared_guest_own_cookie` and `shared_guest_cookie_only`: `HTTP 409 want 403: {"error":"payment_unavailable"}`. Minimal repro: join one DIRECT_SCAN session in browser A and B; issue independent checkout-context cookies; A commits STRIPE; process CREATE and confirm OPEN/unpaid with a URL; POST `/v1/guest/sessions/{session}/orders/{order}/checkout/resume` using B's cookie. Expected 403/no URL; observed 409/no URL. `internal/payments/merchant_resume.go::resume` denies the proof, but `internal/httpapi/payments.go::writePaymentError` maps `payments.ErrProofDenied` to 409. This is a denial-status contract defect; no unauthorized URL was observed. No production repair or external issue publication is part of this revision.

Only the two affected subtests conditionally call `t.Skip("P-84 defect: another guest's cookie against an OPEN Checkout returns HTTP 409 payment_unavailable; require HTTP 403 (matrix 5f)")`. They first reject URL/Location leakage; any other unexpected response fails normally. After the status mapping is repaired elsewhere, 403 automatically executes the strict assertion. A green Go suite with these skips does **not** turn 5f PASS. Environment-only DB skips also never establish acceptance; all integration fixtures were exercised with the required DB URL. Injected rollback failures are expected test input, not product defects.

## Strict PASS re-audit

All earlier PASS entries were checked against executable assertions, including fixtures and ordering of events. The current PASS scope is enumerated here; source code, schema constraints, separate scenarios or test names alone do not extend it:

| PASS IDs | Asserted boundary retained after re-audit |
| --- | --- |
| 1a–1e | Six real store targets in one schema, scoped positive/403 requests and denied-write audit count; published/detached price assertions; all 50 linked hold projections, detached/foreign controls. Fixture identities only. |
| 2a–2f | One seating/three distinct grants; persisted cross-guest option and quantity edit/removal/replay; six simultaneous method races and durable row counts; rejected order mutation and stale consent; third-guest empty next generation. Physical phones and mounted propagation are separate gaps. |
| 3a/3b/3d/3e | Mode snapshots, persisted lockout/reset/cap, paid-but-unreceived exclusion plus reception release, and actual closed/reopened QR identities with protected reads denied. Fixed-QR presence remains 3c GAP. |
| 4a–4f | Snapshot cash modes, settlement races/replay/late sets, mixed-method due, exact eligible card counts/retry, explicit READY close denial in the operations HTTP test, paid/served/hold close gates and close/commit races. Stripe PAID setup in the cash-only test is fixture data. |
| 5b–5e | Exact-byte signature tampering rejection/durable ingress, unpaid-completed vs paid projections, both negative-before-paid and stale-negative-after-paid sequences, cutoff preserving confirmed states and same-Checkout reconciliation. 5f is FAIL; protected capabilities and actual Sandbox remain gaps. |
| 6c/6e/6h | Active company-hold line removal plus stale-consent rejection; paid/reception/review KDS exclusion in one release-path scenario; historical order/quote/tax preservation, rejected order writes and immutable attempt/Checkout/PaymentIntent binding or paid downgrade. 6a and 6d are now GAP for unexercised portions. |
| 7c/7d | Mocked mounted-App periodic menu/payment reads while hidden, terminal polling, stale selected-order/resume results; reducer rejects older generation/version. This does not measure shared-cart propagation or receipts. |
| 8a–8g/8i/8k/8l | Same-schema service/worker recreation, failed notification and expired lease, late rollback with eight empty side-effect tables, migration cycle, replay/failed-order independence, fresh KDS read, persistent PIN/grant limits, specific public projections and actual returned errors, DOM text rendering, exact checkout Origin/Host/proof checks. Log/perimeter/deployment/restore evidence remains GAP. |

Besides the four review findings, this revision fills the missing shared-option edit and active-company-hold removal assertions (2b/6c), marks the untested simultaneous quote/hold and worker-confirmed expiry portions GAP (6a/6d), narrows expired-session/lost-cookie wording in 5f, and corrects both source and candidate counts. No external chat or review URL is acceptance evidence.

## Pending on open PRs

These are dependencies from the authoritative task, not reviewed contents or a live assertion that a PR is still open. Retest the eventual integrated SHA; never carry these source-snapshot results forward automatically.

| Dependency | Matrix claims awaiting evidence | Re-entry requirement |
| --- | --- | --- |
| P-80 Sandbox #19 / #20 | 5a, 5i; external portions of 5b–5f and 6d | Approved per-merchant test-account mapping/charge mode, credential and endpoint owner authorization, actual signed Sandbox paid/failed/async/expired/unknown flows and isolated merchant receipts. No keys or dashboard secrets in evidence. |
| P-82 #22 / #25 + C | 6b, 6f, 6g, 6i; continuation/recovery seams in 5g/5h | Integrated HQ hold-to-Checkout races, confirmed expiry, review disposition, cancel/partial refund, timeout/failure and immutable ledger/history; fresh exact-source tests. |
| P-83 #21 / #24 | 5g/5h, 7a/7b/7e/7f/7g and complete visibility behavior | ORDER_READ vs CHECKOUT_RESUME, lost-grant staff verification, private receipt capability, agreed 30-second cadence vs existing 20 seconds, mounted guest synchronization, locale/accessibility and mobile browser path. |

## Pilot gates (not automatic)

- Owner approves fixed-QR remote-entry risk, store entry mode, staff reception procedure, session/grant/PIN TTLs and abuse thresholds. A copied locator cannot prove presence.
- Real MOD-08 authentication/current membership/MFA and cashier/kitchen/manager duties are accepted with real authorized accounts. Test credentials qualify no production identity.
- Merchant/Connect charge mode, legal payment responsibilities, account mapping, Stripe Test Mode and webhook endpoints are accepted; production secrets remain in the approved secret store. No Live payment is authorized by this matrix.
- P-82/P-83 dependencies are integrated and retested at their actual SHA; unresolved approval/cancel/refund/receipt decisions receive named owner disposition. P-81 C ingredient/recipe identity remains its separate MOD-01 dependency.
- Human operator verifies CASH PREPAID/PAY_LATER, multi-order collection, reception, KDS READY versus SERVED and explicit S02 close at a pilot store. Device latency, reconfirmation, receipt handling and staff training require actual operational acceptance.
- Japanese and Traditional Chinese guest/staff journeys receive accessibility and WebKit/Chromium mobile evidence. Desktop/jsdom tests are not physical-device or screen-reader acceptance.
- Hosting/ingress/cookies/CORS/CSRF, perimeter rate policy, log redaction, backups and recovery/RPO/RTO are accepted by their owners. Successful migration teardown is not a business-data rollback strategy.
- Independent Oracle with Ego-lite review is a separate integration handoff gate and must identify its exact candidate. Local automated checks do not supply that verdict, a browser/mobile journey or merge authorization.

## Reproducible commands, migration and recovery

Run from the repository root against the supplied disposable loopback PostgreSQL database. Go must match `go.mod` (1.27.1); Node is 24.15.0 and pnpm is the package pin (12.11.2). The PATH line selects the already-installed Node on this workstation; elsewhere provide Node 24.15.x on PATH. This is the single command list for this receipt:

```sh
export TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55439/tojima_test?sslmode=disable'
export PATH="$HOME/.local/share/fnm/node-versions/v24.15.0/installation/bin:$PATH"
gofmt -w internal/httpapi/p84_acceptance_integration_test.go internal/stripeprovider/p84_acceptance_test.go
go test -race -count=1 ./...
make fmt-check vet build migration-smoke
pnpm --dir web install --frozen-lockfile --store-dir /private/tmp/tojima-p84-pnpm-store
pnpm --dir web check
pnpm --dir web build
# Focused defect receipt (two explicit P-84 skips, never acceptance PASS):
# go test -race -count=1 -v ./internal/httpapi -run '^TestP84OpenCheckoutResumeRefusesOtherBrowser$'
# Optional focused recovery drill (already exercised by ./... above):
# go test -race -count=1 ./internal/httpapi -run '^TestP84(RestartOutboxRetryAndMissedEventResync|LateStripeCommitFailureRollsBackAllSideEffects)$'
# Optional migration up/down/reapply drill (also included in ./...):
# go test -race -count=1 ./migrations/postgres -run '^TestPostgresMigrationsUpDownReapply$'
```

Integration helpers create random schemas, apply all paired PostgreSQL migrations transactionally, and drop only their owned schema on normal cleanup. Operational recovery here means re-create the service/worker over **the same schema**, retry durable work with the same provider identity, and reload authorized KDS state after missed notifications. No payment method conversion, table generation rewind or new provider key is a recovery action. Test-trigger failure injection stays inside the owned schema and is removed before retry; teardown removes it even if the test aborts. Crash residue should be identified by exact owned schema before any manual teardown; never reset a shared database or execute destructive down migrations against payment history.

Local validation receipt: PostgreSQL race suite exits 0 with the required DB URL; the focused verbose receipt records exactly two known-defect subtest skips for 5f, which remains FAIL (no missing-DB skip accepted); `make fmt-check vet build migration-smoke` exits 0 (Go build emitted a non-fatal module stat-cache write permission warning in this sandbox); pinned frozen pnpm install PASS; web check PASS (10 files, 120 tests including the new XSS case); web production build PASS. The final handoff names the containing commit SHA. No push, PR, deployment or migration mutation is part of lane A.

## 日本語サマリー

基準は `46b2391`。8行を61項目に分け、Lane Aは **PASS 40 / FAIL 1 / GAP 20**。既存テストを再利用し、3テナント×2店舗の認可、鍵なしSTRIPE/CASH同時確定、3方式の同じQRで次の客への履歴遮断、サービス再生成後のoutbox/期限切れlease復旧、決済処理後段のtransaction rollback、決済失敗後の別世代CASH注文、メニュー文字列XSSを追加した。根拠を再監査し、基準側はPASS 26 / FAIL 1 / GAP 34に訂正、14件のcoverage GAPを解消した。

本番コード・既存テスト・migrationは変更しない。別ゲストのcookieでOPEN Checkoutを再開すると要求403に対し409となる不具合を記録した（URL漏洩なし）。5fはFAIL、該当2 subtestのみdefect skip。6aのquote競合と6dのworker経由のexpiry確認はGAP。P-80 Sandbox #19/#20、P-82 #22/#25 + C、P-83 #21/#24は未統合内容として扱い、実Stripe・返金・ORDER_READ・モバイル/アクセシビリティ・運用判断はPASSにしない。現行statusは20秒で、要求の30秒との整合はP-83で確認する。上記単一コマンド列で再現し、Oracle/Ego-lite独立審査・実IdP・pilot・本番復旧は別gate。

## 繁體中文摘要

來源為 `46b2391`，8列拆成61項，Lane A為 **PASS 40 / FAIL 1 / GAP 20**。重用既有測試，新增3租戶×2分店隔離、無金鑰STRIPE/CASH競爭、三種QR模式換桌次的隱私、重建服務後outbox與逾期lease復原、付款資料寫入後段的交易rollback、付款失敗後新世代CASH訂單，以及菜單文字XSS檢查，嚴格重審後來源側修正為PASS 26 / FAIL 1 / GAP 34，共補足14項測試證據。

未更動正式程式、既有測試或migration；發現其他客人的cookie對OPEN Checkout回傳409，與要求403不符（無URL洩漏），5f列為FAIL，僅兩個受影響subtest有defect skip。6a的quote競爭與6d經worker確認expiry的部分改列GAP。P-80 Sandbox #19/#20、P-82 #22/#25 + C、P-83 #21/#24仍須以整合後SHA重驗。實際Stripe、退款、ORDER_READ、無障礙、手機瀏覽器與pilot風險不能由本次測試自動通過；現行status為20秒，與要求30秒的差異交P-83確認。復原命令與migration循環見上方唯一命令清單；Oracle/Ego-lite獨立審查、真實IdP與正式備援復原仍是獨立gate。
