# Free-hosting and IdP research

Source: Codex Sol research report, 2026-10-26 05:25 JST. Copied verbatim from the source report below. Web facts may change; re-verify before signup.

---

1. **2026-10-26 05:25 JST — Recommend one OCI Always Free A1 VM, Docker Compose, PostgreSQL, Keycloak, Go API/worker, and Cloudflare Pages/Tunnel.**
2. Size the Free tenancy to **2 OCPUs / 12 GB**; Oracle’s current documentation no longer promises 4 / 24. [OCI limits](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm)
3. Oracle’s October PAYG price list retains a larger free A1 allowance, but PAYG permits charges and conflicts with the owner’s present rule. [Oracle price list, p.55](https://www.oracle.com/il/a/ocom/docs/corporate/pricing/oracle-paas-and-iaas-global-price-list.pdf)
4. Tokyo and Osaka exist; available free A1 capacity at signup or recovery is **UNVERIFIED**. [OCI regions](https://www.oracle.com/cloud/public-cloud-regions/)
5. Fixed hosting subscriptions can total **¥0**; electricity, domains, labour, and real Stripe transactions are separate costs.
6. Keep Keycloak as the preferred candidate, with breached-password screening and the ADR’s outstanding acceptance tests completed before staff use.
7. Validate established Tojima sessions against local PostgreSQL authorization state; fail closed for new login and expired step-up during an IdP outage.
8. Immediate revocation requires a synchronous Tojima database commit; asynchronous Keycloak events alone cannot guarantee it.
9. Adopt ten-year financial archives, five-year attendance records, and at least 90 days of searchable security logs, with legal categories kept distinct.
10. The dependable fallback is existing owned hardware; a second permanent, card-safe ¥0 cloud architecture meeting every requirement is **UNVERIFIED**.

**Scope and evidence**

This is read-only advice. I read all three requested files; no project files were modified and no accounts were created. Official public pages were retrieved live. **UNVERIFIED** means public evidence did not establish the fact or account-specific entitlement. Recommendations and sizing estimates are engineering judgments, not vendor guarantees.

ADR-0003 remains controlling project context: the PoC’s “12 PASS / 1 PARTIAL” is historical test evidence, not adoption approval. Mobile devices, offline-token non-resurrection, Step36 notifications, and the availability design still need acceptance.

**1. Hosting decision and free quotas**

| Option | Current free allowance / conditions | Decision for Tojima |
|---|---|---|
| **OCI A1 — Free tenancy** | **1,500 OCPU-hours + 9,000 GB-hours/month**, equivalent to **2 OCPUs / 12 GB** continuously. Home region only. | Best cloud candidate for the complete container stack. [Official limits](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm) |
| **OCI A1 — PAYG** | October 8, 2026 price list gives A1 **3,000 OCPU-hours + 18,000 GB-hours/month** free: approximately 4 OCPUs / 24 GB continuously. Account-specific eligibility is **UNVERIFIED**; excess usage is billable. | Do not upgrade under the current prohibition on billable resources. [Price list, p.55](https://www.oracle.com/il/a/ocom/docs/corporate/pricing/oracle-paas-and-iaas-global-price-list.pdf) |
| **OCI disks / backup / transfer** | **200 GB** boot+block storage combined; **five** volume backups; Free-only Object Storage **20 GB combined**, **50,000 requests/month**; outbound **10 TB/month**. | Enough to begin; account-wide quotas include every environment. [Official limits](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm) |
| **Google Compute Engine** | One e2-micro, **30 GB-month standard persistent disk**, **1 GB/month outbound**. Eligible regions: Oregon, Iowa, South Carolina. **Tokyo is excluded.** | Too small for this complete stack; also requires ongoing billing eligibility. [Free-tier terms](https://docs.cloud.google.com/free/docs/free-cloud-features) |
| **Cloud Run** | Request-based allowance: **180,000 vCPU-seconds, 360,000 GiB-seconds, 2 million requests/month**; applied using us-central1 prices. Instance-based has separate **240,000 / 450,000** compute allowances. | Useful later for Go HTTP workloads. Not a permanent PostgreSQL host; continuous workers/IdP consume compute beyond intermittent-request economics. [Pricing](https://cloud.google.com/run/pricing) |
| **Cloud SQL / AlloyDB** | No permanent free transactional PostgreSQL allowance established. Published free offers are trials. | Excluded now. [Google free offers](https://docs.cloud.google.com/free/docs/free-cloud-features) |
| **AWS new-account Free plan** | **$100 credits initially**, up to **$100 earned**; closes after **six months or credit exhaustion**, whichever comes first. No charges unless converted to Paid or paid-only services activated. | Trial/credit-funded hosting; excluded as the permanent architecture. [AWS Free Tier](https://aws.amazon.com/free/) |
| **Cloudflare Workers Free** | **100,000 requests/day**, **10 ms CPU/request**, **128 MB memory**. | Edge logic, not a replacement for the existing Go container, PostgreSQL, and Keycloak. [Limits](https://developers.cloudflare.com/workers/platform/limits/) |
| **Cloudflare Pages Free** | **500 builds/month**, one concurrent build, **20,000 files/site**, **25 MiB/file**. Functions consume Workers allowances. | Good for the Vite SPA. [Limits](https://developers.cloudflare.com/pages/platform/limits/), [billing](https://developers.cloudflare.com/workers/platform/pricing/) |
| **Cloudflare Tunnel** | Available on all plans; exposes an origin through outbound connections. | Suitable free ingress to the VM. Use a named stable tunnel and existing domain. [Official documentation](https://developers.cloudflare.com/tunnel/) |
| **Cloudflare D1 Free** | **5 million rows read/day**, **100,000 written/day**, **5 GB/account**; **500 MB/database**, ten databases; seven-day Time Travel. | SQLite semantics, so it does not satisfy the selected shared PostgreSQL design. [Pricing](https://developers.cloudflare.com/d1/platform/pricing/), [limits](https://developers.cloudflare.com/d1/platform/limits/) |
| **Hyperdrive Free** | **100,000 database queries/day**. Supplies connection pooling/caching, not the underlying database. | Unnecessary for Go connecting directly to its local PostgreSQL. [Pricing](https://developers.cloudflare.com/hyperdrive/platform/pricing/) |
| **Cloudflare WAF rate limiting** | **One free rule per zone**, IP counting, ten-second counting/mitigation periods. Enforcement can lag. | Put it on login-sensitive paths; retain Keycloak and application rate controls. [Rule availability and limitations](https://developers.cloudflare.com/waf/rate-limiting-rules/) |

**OCI capacity and account risks**

Tokyo (`ap-tokyo-1`) and Osaka (`ap-osaka-1`) are live regions, each with one availability domain. A live region does not establish free-instance capacity. “Out of host capacity” can prevent initial deployment and replacement. [Region identifiers](https://docs.oracle.com/en-us/iaas/Content/General/Concepts/regions.htm)

Oracle may reclaim idle Always Free instances when, over seven days, CPU p95, network utilization, and—on A1—memory utilization are all below 20%. Do not manufacture load to evade reclamation. [Reclamation policy](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm)

Free Tier has no SLA. Oracle also permits suspension/termination for abandoned accounts idle for at least 30 days and for terms violations. Staying Free avoids paid-resource access after the trial; upgrading retains applicable free allowances but enables overage billing. **PAYG exemption from idle reclamation is UNVERIFIED.** [Oracle FAQ](https://www.oracle.com/cloud/free/faq/)

**Free managed PostgreSQL**

| Provider | Free resources | Region near Japan | Recovery / inactivity | Fit |
|---|---|---|---|---|
| **Neon** | **1 GB/project**, **100 CU-hours/project/month**, up to 2 CU, **5 GB/project egress**; no card required. Current pricing lists 100 projects. | Singapore verified; Tokyo absent from the published region list. | Scales to zero after five idle minutes. Free instant restore: **up to six hours or 1 GB of changes**. | Strong dev/demo option. Constant Keycloak/worker activity can exhaust compute: even 0.25 CU continuously needs about 183 CU-hours/month. [Pricing](https://neon.com/pricing), [regions](https://neon.com/docs/introduction/regions) |
| **Supabase** | **500 MB DB**, shared CPU/**500 MB RAM**, two active projects, **5 GB egress**, separate cached-egress allowance. | Tokyo available. | Pauses after one inactive week. Free-tier users are instructed to export off-site backups; no free PITR entitlement established. | Good small test DB; limited headroom for application data plus IdP/audit growth. [Pricing](https://supabase.com/pricing), [regions](https://supabase.com/docs/guides/platform/regions), [backups](https://supabase.com/docs/guides/platform/backups) |
| **Aiven** | One node, **1 CPU / 1 GB RAM / 1 GB disk**, 20 connections, no pooling; no card or time limit. | Free-plan Tokyo/Singapore selection **UNVERIFIED**; Aiven reserves the right to change region. | Backups included; free PITR/retention **UNVERIFIED**. Inactive services may be powered off after notification. | Worth evaluating for dev; not assumed to satisfy recovery requirements. [Official free-plan terms](https://aiven.io/docs/products/postgresql/concepts/pg-free-tier) |
| **Render** | Free PostgreSQL expires after **30 days**. | Irrelevant to permanent selection. | Paid databases get ongoing recovery features. | Exclude: a time-limited offer. [Official service terms](https://render.com/docs/service-types) |

Keep Go as the sole business-write authority with any BaaS. Do not expose business tables through client-side database credentials or treat provider Auth as tenant/store authorization.

**Recommended ¥0 architecture**

```text
Vite SPA → Cloudflare Pages
Staff/browser → Cloudflare → named Tunnel → reverse proxy
                                                ├─ Go API
                                                ├─ Keycloak
Stripe webhooks ────────────────────────────────→ Go API
                                                └─ PostgreSQL
Go payment worker ─────────────────────────────→ PostgreSQL / Stripe

Encrypted backups → OCI Object Storage
                  → second copy on existing owner-controlled storage
```

Use **Compose first**, with ARM64 images:

- One A1 VM at **2 OCPUs / 12 GB**, with an explicitly limited Keycloak container.
- Separate PostgreSQL databases and roles for Keycloak and Tojima; separate credentials/databases/realms for staging. Do not let the IdP database role access business tables.
- Private container networking for PostgreSQL; only the reverse proxy accepts application ingress. Restrict administrative interfaces.
- Preserve tenant/store constraints, current-membership checks, durable payment inbox/outbox, and idempotency.
- Budget backup storage explicitly: daily encrypted backups plus WAL where capacity permits, monitored against the 20 GB object allowance. Keep encryption keys and a second recoverable copy outside the OCI account.
- Retain 90 days of security events, with bounded operational logs and disk alerts.

These are proposed controls, not validated deployment results.

**Compose versus k3s:** both can run on this machine. Compose leaves more memory and operational attention for PostgreSQL and identity. Learn single-node k3s locally using the same container images; it adds no host-failure availability. Migration to k3s does not itself supply database replication, fencing, backups, or recovery.

**Fallback:** restore the same Compose stack onto an existing owner-controlled Linux machine with approximately 8–12 GB available RAM, existing broadband, and Cloudflare Tunnel. Incremental cloud subscription cost can remain ¥0; hardware availability is **UNVERIFIED**, and electricity/ISP costs remain real. Maintain the public IdP hostname/RP ID to preserve Passkey usability.

Osaka is an alternative **initial home-region choice**, not an assumed second free deployment. No compliant, universally available second-cloud fallback was verified. If neither OCI capacity nor suitable existing hardware exists, retain a local demo until the constraint changes.

**2. Cost and protection against charges**

**Expected fixed infrastructure subscription: ¥0/month**, conditional on remaining within the selected free entitlements. This excludes domain renewal, labour, electricity, and payment processing. Stripe transactions incur commercial fees even when hosting is free. [Stripe Japan pricing](https://stripe.com/jp/pricing)

| Service/account | Card and charge boundary | Recommended control |
|---|---|---|
| **OCI Free-only** | Card required for identity verification; temporary authorization holds can occur. Oracle disallows prepaid/virtual/single-use cards. | Remain Free-only; never upgrade or activate paid services. MFA-protect tenancy ownership. Monitor allowance use and account notices. [FAQ](https://www.oracle.com/cloud/free/faq/) |
| **OCI PAYG** | Chargeable above free allowances. A budget is not a universal hard stop. | Excluded now. Later use compartment quotas, restrictive IAM, resource inventory and alerts; these cannot establish “card never charged.” [OCI allowance controls](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm) |
| **Google Cloud** | Continued Free Tier use requires billing eligibility; paid billing charges overages. Trial-only accounts stop when the trial expires. | Excluded from the strict non-billable architecture. [Free-program terms](https://docs.cloud.google.com/free/docs/free-cloud-features) |
| **Google budgets** | Alerts-only budgets do not cap spending. New spend-cap budgets cover selected services, including Cloud Run, but enforcement is delayed and overages remain billable; persistent costs continue. | Useful later, not a ¥0 guarantee. [Alerts](https://docs.cloud.google.com/billing/docs/how-to/budgets), [spend-cap limitations](https://docs.cloud.google.com/billing/docs/how-to/budgets-spend-caps) |
| **Cloudflare** | Use Free Pages, Tunnel and WAF. Exact current card requirement for every Zero Trust onboarding path is **UNVERIFIED**. | Do not activate Workers Paid, R2 billing or paid add-ons. Treat any required paid enrollment as outside the recommendation. [Plans](https://www.cloudflare.com/plans/) |
| **Neon / Aiven** | Officially offer no-card free PostgreSQL. | Keep Free; accept suspension at limits rather than upgrading. [Neon](https://neon.com/pricing), [Aiven](https://aiven.io/docs/products/postgresql/concepts/pg-free-tier) |
| **AWS Free plan** | Official terms provide a no-charge boundary until paid conversion/paid-only activation, but hosting expires. | Suitable only for temporary learning, excluded from the permanent plan. [AWS terms](https://aws.amazon.com/free/) |

Possible accidental costs include oversized disks, extra snapshots, object versions, external IPv4, NAT/load balancers, container registries, log ingestion, mail/SMS, and cross-region traffic. For example, Google’s standard VM external IPv4 price is **$0.005/hour**, independently of compute eligibility. [VPC pricing](https://cloud.google.com/vpc/pricing)

For free-resource monitoring, alert at approximately **50%, 75%, and 90%** of storage, backup-request and transfer allowances. At capacity, stop nonessential jobs and fail new writes safely; do not delete legally retained records or disable security logging to remain free.

Identity email is also required. Oracle’s October rate card includes the first **3,000 Email Delivery emails/month** free, but sender/domain approval and actual Free-tenancy send limits remain deployment checks. Queue Step36 notifications durably; delivery failure must not remove a security hold. [Oracle price list, p.69](https://www.oracle.com/il/a/ocom/docs/corporate/pricing/oracle-paas-and-iaas-global-price-list.pdf)

**3. Identity recommendation under ¥0**

**Choose self-hosted Keycloak as the preferred candidate. No candidate is currently proven to satisfy the complete Tojima contract without the outstanding acceptance work.**

Keycloak recommends a **2 GB container memory limit** for smaller production deployments; its default maximum heap is 70% of the container limit. Thus it fits beside PostgreSQL on 24 GB, and plausibly on the current 12 GB allowance. Proposed starting budget: Keycloak 2 GB, PostgreSQL approximately 3 GB, API/worker 1 GB, OS/proxy/monitoring 1 GB, leaving headroom. Actual concurrent-login and backup performance is **UNVERIFIED**. [Keycloak container guidance](https://www.keycloak.org/server/containers)

| Candidate | ¥0 capability and material gaps | Disposition |
|---|---|---|
| **Keycloak self-hosted** | Native credential, Passkey, MFA, recovery-code and hosted UI capabilities; best existing Tojima evidence. R01 and ADR acceptance gaps remain. | **Preferred.** No independent password/WebAuthn/MFA implementation in Go. |
| **ZITADEL Cloud Free** | **100 DAU**, unlimited stored users/organizations, three external IdPs; one instance/admin, **5,000 management API requests** in the published allowance. **One-day audit history**, 30-minute runtime logs. Regions: US/EU/Switzerland/Australia. | R11 fails native 90-day retention; no Japan region. Prior R02/R06/R07/R08/R10/R13 gaps remain unclosed by this research. [Pricing](https://zitadel.com/pricing), [detailed limits](https://zitadel.com/pricing/detail) |
| **ZITADEL self-hosted** | Avoids Cloud retention/region limits; AGPL deployment available, commercial licensing optional. Recovery-code API exists. | Re-test prior gaps; do not infer that API availability supplies the required recovery UI. [Licensing](https://help.zitadel.com/zitadel-licensing-faqs), [recovery API](https://zitadel.com/docs/reference/api/user/zitadel.user.v2.UserService.GenerateRecoveryCodes) |
| **Ory self-hosted** | Credential/Passkey/MFA engines available; more integration and UI ownership. Enterprise adds extended audit features. | Prior R01/R08/R11 gaps remain. Ordinary self-collected security logs may help R11, but completeness and usable UI are **UNVERIFIED**; paid Enterprise is not automatically necessary for every log requirement. [Official offering](https://www.ory.com/open-source) |
| **authentik OSS** | Free OIDC, Passkeys and native single-use backup codes. | Credible alternative; complete R01–R13 behaviour, especially immediate revocation, absolute lifetime, safe factor changes and non-resurrection, is **UNVERIFIED**. [Pricing](https://goauthentik.io/pricing/), [Passkeys](https://docs.goauthentik.io/add-secure-apps/flows-stages/stages/authenticator_webauthn/), [backup codes](https://docs.goauthentik.io/add-secure-apps/flows-stages/stages/authenticator_static/) |
| **Authelia** | Free IAM/OIDC; current versions support passwordless Passkeys. | Do not dismiss it as “second-factor WebAuthn only.” Complete invite/recovery/account-lifecycle requirements are **UNVERIFIED**. [Official overview](https://www.authelia.com/), [Passkey support](https://www.authelia.com/overview/authentication/security-key/) |
| **Logto** | Self-hosted OSS includes authentication/MFA capabilities. Cloud Free pricing excludes MFA; paid add-ons are outside scope. | OSS remains a contender, but complete R01–R13 acceptance is **UNVERIFIED**. Distinguish paid Console MFA enforcement from end-user MFA. [Cloud pricing](https://logto.io/pricing), [OSS terms](https://logto.io/self-hosted-plans), [MFA](https://logto.io/products/mfa) |
| **Supabase Auth Free** | 50,000 MAU; current Passkeys **and recovery-code APIs exist**, both experimental. Native time-boxed sessions require Pro or above. | R02/R03/R06 need current qualification; R08 needs Tojima enforcement on Free; JWT revocation still requires server-side checks. Complete ready-made ja/zh-TW security UI is **UNVERIFIED**. [Passkeys](https://supabase.com/docs/guides/auth/passkeys), [recovery codes](https://supabase.com/docs/reference/javascript/auth-mfa-recovery-codes-generate), [session limits](https://supabase.com/docs/guides/auth/sessions) |
| **Firebase Auth / Identity Platform** | Email/password supported. Identity Platform publishes 50,000 free Tier-1 MAU; Spark upgraded projects have a 3,000 DAU limit. | Native standalone WebAuthn Passkey and single-use MFA recovery-code support meeting R02/R03/R06 is **UNVERIFIED**. Google-account Passkeys do not establish Firebase-native Passkeys. [Capabilities](https://firebase.google.com/docs/auth/), [pricing](https://cloud.google.com/identity-platform/pricing) |
| **Cloudflare Access** | Useful edge access control; free offering covers small teams. OTP or integrated identity can protect applications. | Complete R01–R13 staff identity is **UNVERIFIED**. Use as an administrative perimeter if appropriate, not as tenant/store authorization. [Access offering](https://www.cloudflare.com/zero-trust/products/access/), [OTP behaviour](https://developers.cloudflare.com/cloudflare-one/integrations/identity-providers/one-time-pin/) |

For Keycloak, retain the PoC distinction:

| Requirement | Current evidence | Cheap closure / required acceptance |
|---|---|---|
| **R01 Email/password** | **PARTIAL**: lockout tested; ten static denied passwords do not establish breached-password screening. | Reviewed HIBP password-policy extension or sufficiently maintained offline denylist; test every password creation/change/reset path. |
| **R02 Web Passkey** | PoC PASS. | Re-run against selected version, hostname and actual browsers. |
| **R03 Flutter Passkey** | System-browser path passed in PoC; real iOS/Android devices unverified. | Authorization Code + PKCE through system browser; verify return links and enrollment/login on both platforms. |
| **R04 Invite-only** | PoC PASS. | Disable public registration; test invitation reuse, expiry and role assignment. |
| **R05 Manager MFA** | PoC PASS. | Enforce required assurance in Go; do not equate any Passkey login automatically with the required assurance. |
| **R06 Single-use recovery codes** | PoC PASS. | Test reuse rejection, replacement invalidation, UI and audit. |
| **R07 Immediate revocation** | PASS using per-request introspection. | Local-session design below requires new acceptance. |
| **R08 30-day absolute cap** | PoC PASS with configuration conditions. | Enforce an immutable original session deadline in Go; prohibit offline access. |
| **R09 300-second step-up** | PoC PASS. | Fixed expiry from verified fresh authentication; never slide on activity. |
| **R10 Email/factor changes** | PoC PASS. | Verify fresh authentication, notification and session invalidation across every route. |
| **R11 90-day logs** | PoC PASS by configuration. | Verify event coverage, search/export and restore; preserve Go authorization/payment events too. |
| **R12 Disable/restore** | PoC PASS. | Add Step36 user-only notifications and email-failure hold tests. |
| **R13 Old tokens never revive** | Conditional PASS. | Verify all-client consent/session revocation, offline-token prohibition, factor-reset logout and recovery after backup restore. |

The broader deployment remains **PARTIAL** until these outstanding checks pass. The source report’s ja/zh-TW support also needs screen-by-screen acceptance for login, recovery, factor changes and errors.

For **R01**, HIBP’s Pwned Passwords range API is free and needs no subscription key. It sends only the first five hexadecimal characters of a SHA-1 password hash; compare suffixes locally and request padding. Never send plaintext passwords or full hashes. [HIBP API](https://haveibeenpwned.com/API/v3#PwnedPasswords)

An existing [Keycloak password-policy extension](https://github.com/rishvic/keycloak-pwned-password-policy) is a candidate, **not a verified dependency**: compatibility, maintenance, failure behaviour and supply-chain review remain outstanding. Fail closed for password setting when screening cannot complete; existing authenticated sessions need not stop. Keycloak also supports a native password blacklist, but a tiny or stale list cannot close R01. [Keycloak password policies](https://www.keycloak.org/docs/latest/server_admin/#_password-policies)

**4. D5: behaviour when the IdP is unavailable**

| Design | IdP unavailable, Go/PostgreSQL healthy | Revocation consequence |
|---|---|---|
| **Every request introspected** | Staff API requests fail closed immediately. | Straightforward online validation; availability depends on the IdP for every operation. |
| **Locally validated JWT only** | Issued JWTs work until expiry. | Revoked credentials can remain usable until expiry. Does not satisfy immediate revocation alone. |
| **Tojima session + PostgreSQL authorization/revocation checks** | Established sessions continue within existing lifetime/assurance limits. New login, recovery and fresh step-up fail closed. | Immediate for revocations synchronously committed in Tojima; external-only changes need a reliable propagation contract. |

**Recommend the third design**, implemented within the shared MOD-08 adapter using established OIDC/session libraries:

1. Keycloak performs credential authentication, Passkeys, MFA and recovery. Go never receives or verifies those credentials.
2. On successful OIDC login, Go establishes a server-side session mapped to the Keycloak `sub` and `sid`. Store original authentication time, absolute expiry, assurance and step-up expiry.
3. Every request checks the PostgreSQL primary for session validity, global security hold, current membership, tenant/store scope and authorization version. Avoid positive authorization caches.
4. For suspension, offboarding or privilege removal, **commit the Go security change first**, alongside the audit record and durable outbox. Then disable/logout/revoke in Keycloak. IdP failure leaves the local hold active.
5. Restoring an account never restores old sessions or step-up grants. Require fresh complete authentication.
6. Consume signed Keycloak back-channel logout and administrative events as additional revocation inputs. Validate issuer, audience, signature and replay protection. Keycloak documents direct back-channel logout, but generic administrative event forwarding needs an explicit integration. [Keycloak logout and events](https://www.keycloak.org/docs/latest/server_admin/)

**Maximum staleness:**

- **Tojima-controlled revocation:** zero authorization-cache TTL; the next authorization check after commit rejects it. Already-authorized in-flight operations require transaction-level locking/version checks for sensitive mutations.
- **External Keycloak changes:** asynchronous delivery has **no finite guaranteed maximum** during a partition. A five-minute JWT gives up to five minutes of exposure; it is not immediate revocation.
- Therefore route all supported security-changing operations through the Go coordinator, including factor-reset/account-change workflows. Lock down direct administration and provide a break-glass revocation procedure.
- If direct Keycloak changes cannot be coordinated reliably, retain introspection for affected operations or fail closed when synchronization is uncertain. Continuous operation and immediate detection of unreachable external changes cannot both be guaranteed.

Use an initial ordinary-session horizon of one shift—approximately 8–12 hours—within the **30-day absolute maximum**. Existing step-up expires after **300 seconds** even during an outage. PostgreSQL or authorization-state uncertainty stops staff mutations.

This protects against an **IdP process outage**. A VM, disk or shared PostgreSQL outage still stops the whole stack.

Payment webhook authentication remains independent of staff identity. Verify signatures and durably commit the event before returning success; otherwise return failure. Process using account-scoped identifiers, uniqueness constraints and idempotent transitions. After recovery, reconcile Stripe state before retrying uncertain payment actions. [Stripe webhook guidance](https://docs.stripe.com/webhooks)

**5. Japanese retention policy**

Grades: **A = explicit statutory baseline verified; B = legal applicability depends on the record/entity; C = recommended product/security policy.**

| Record | Grade and legal basis | Recommended Tojima retention |
|---|---|---|
| **Orders, sales books, receipts and supporting transaction records** | **A:** Corporation-tax baseline is **seven years from the day after the return filing deadline**. Specified blue-return loss years/disaster-loss years require **ten years**; this is not every year in which a loss is merely carried forward. [NTA No.5930](https://www.nta.go.jp/taxes/shiraberu/taxanswer/hojin/5930.htm) | **Ten years**, calculated from the applicable legal starting point. Preserve corrected/cancelled versions and accounting links. |
| **Corporate account books and important business materials** | **B:** Companies Act Art.432(2) separately specifies ten years for stock companies, from closing the books. Applicability depends on entity/record classification. [Official government translation](https://www.cas.go.jp/jp/seisaku/hourei/data/Corp_2.pdf) | Apply the longer applicable period; ten-year financial policy avoids relying solely on the seven-year tax minimum. |
| **Electronic receipts/invoices and transaction data** | **A/B:** Electronic Bookkeeping Act controls preservation method; it does not independently make every record “seven years.” Electronic transaction records require appropriate electronic preservation, authenticity and accessibility. [NTA guidance](https://www.nta.go.jp/about/introduction/torikumi/week/dennsityoubo.htm) | Same **ten-year financial archive**. Preserve original electronic records, searchable metadata and correction history; screenshots alone are insufficient. |
| **Payment, refund, settlement and reconciliation records** | **B:** Accounting evidence follows applicable tax/company retention. A universal separate Stripe-specific statutory duration was **UNVERIFIED**. | **Ten years** for financial facts and necessary provider references. Keep secrets and unnecessary payload fields out of the archive. |
| **Attendance, actual work time, payroll-supporting shift records** | **A:** Labour Standards Act Art.109 says **five years**; Art.143 currently applies **three years “for the time being.”** [MHLW explanation](https://www.startup-roudou.mhlw.go.jp/qa/zigyonushi/syuugyoukisoku/q6.html) | **Five years** from the applicable record-specific starting point. Payroll/tax records may require longer; avoid retaining irrelevant draft schedules automatically. |
| **Authentication, security and authorization logs** | **C:** No universal fixed Japanese retention duration for this ordinary system was verified. APPI requires appropriate security measures. [PPC guidance](https://www.ppc.go.jp/personalinfo/legal/guidelines_tsusoku/) | **90 days searchable minimum**, as required by R11. Consider one year of minimized privileged-security events; financial audit events follow the financial archive. |
| **Guest personal data** | **A/C:** APPI has no general fixed retention period; make efforts to erase personal data without delay when no longer needed. Other lawful retention duties remain. [PPC FAQ](https://www.ppc.go.jp/all_faq_index/faq1-q5-2/) | Avoid collection where possible. Proposed default: remove unnecessary contact/session data **30 days after completed service**, subject to actual dispute/support needs. Retain required financial records separately with minimized identity. |
| **Departed staff IdP accounts** | **C:** No universal IdP-account retention period was verified. Credential access and employment-record retention serve different purposes. | Revoke immediately at the effective departure time. Remove credentials/profile after a documented **30–90-day** administrative window; preserve a minimal non-reusable subject reference in lawful audit/employment records. |

Backups need their own expiry and deletion policy. Restoring an old backup must reapply account holds, revocations and deletion decisions before reopening access. A 30-day recovery backup is not a ten-year legal archive.

The free storage allowance is a **capacity limit, not a retention exemption**. Measure archive growth before pilot admission; if compliant retention no longer fits free resources and existing owned storage, the deployment cannot remain an accepted ¥0 pilot.

**6. Upgrade path after the owner changes the ¥0 constraint**

First add **recoverability**, then remove reclaimable capacity, then add availability. Kubernetes is not the first production purchase.

The following are **planning estimates; exact Tokyo SKU totals are UNVERIFIED**, not vendor quotes. Assumptions: small workload, roughly 730 hours/month, no commitments, and **¥150/USD solely for budgeting**, not a verified exchange rate. Taxes, labour and Stripe fees are excluded.

| Step | Concrete addition | Rough monthly budget | Recovery implication |
|---|---|---:|---|
| **0 — Free pilot** | Single OCI VM, measured encrypted backups, second existing-storage copy, restoration drill. | **¥0 cloud subscription** | No guaranteed RTO. Nightly-only backups can lose approximately 24 hours; do not claim RPO five minutes. |
| **1 — Independent durable backup** | GCP Tokyo object storage, PostgreSQL base backups + continuous WAL, backup-age alerts, retained configs/keys and restoration scripts. | **+$5–20 / ¥750–3,000** | Set WAL archive timeout around 60 seconds and alert before five minutes of unprotected changes. RPO remains unproven until measured restores succeed. [Storage pricing](https://cloud.google.com/storage/pricing) |
| **2 — Paid, reproducible single host** | GCP Tokyo VM around 2 vCPU/8 GB, appropriate persistent disk, bounded logs and automated recreation. Keep Compose. | **$65–110 total / ¥9,750–16,500** | Removes Free-tier reclamation dependence; still one failure domain. RTO one hour requires a demonstrated timed rebuild/restore. [Compute pricing](https://cloud.google.com/products/compute/pricing), [network pricing](https://cloud.google.com/vpc/pricing) |
| **3 — Managed PostgreSQL** | Once authorized, move the shared cluster to Cloud SQL PostgreSQL with PITR, private access and deletion protection. | **$180–320 total / ¥27,000–48,000**, single-zone starting design | Reduces database operations burden. PITR creates a new instance; test routing, secrets and application recovery. [SQL pricing](https://cloud.google.com/sql/pricing), [PITR behaviour](https://docs.cloud.google.com/sql/docs/postgres/backup-recovery/pitr) |
| **4 — Zone-failure resilience** | Regional HA PostgreSQL, two application/IdP instances across zones, load balancing, worker fencing/leases and external monitoring. | **$400–700 total / ¥60,000–105,000** | Practical basis for the later RPO five-minute/RTO one-hour target. Targets still require failure and restore drills. [SQL pricing](https://cloud.google.com/sql/pricing), [compute pricing](https://cloud.google.com/products/compute/pricing) |
| **5 — Orchestration / second cloud** | Adopt k3s/GKE only when deployment/team needs justify it; later add independently recoverable cross-region/cloud backups. | **UNVERIFIED** until architecture is specified | Keep one transactional writer with explicit failover and fencing. Two clouds do not automatically improve payment correctness. |

For pilot admission, the remaining concrete gates are: available Free-only capacity, completed Keycloak acceptance gaps, a proven revocation contract, successful backup restoration, sufficient legal-retention capacity, and payment reconciliation after simulated outages. If these cannot be demonstrated within ¥0, keep the environment at dev/demo status.


231,952
1. **2026-10-26 05:25 JST — Recommend one OCI Always Free A1 VM, Docker Compose, PostgreSQL, Keycloak, Go API/worker, and Cloudflare Pages/Tunnel.**
2. Size the Free tenancy to **2 OCPUs / 12 GB**; Oracle’s current documentation no longer promises 4 / 24. [OCI limits](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm)
3. Oracle’s October PAYG price list retains a larger free A1 allowance, but PAYG permits charges and conflicts with the owner’s present rule. [Oracle price list, p.55](https://www.oracle.com/il/a/ocom/docs/corporate/pricing/oracle-paas-and-iaas-global-price-list.pdf)
4. Tokyo and Osaka exist; available free A1 capacity at signup or recovery is **UNVERIFIED**. [OCI regions](https://www.oracle.com/cloud/public-cloud-regions/)
5. Fixed hosting subscriptions can total **¥0**; electricity, domains, labour, and real Stripe transactions are separate costs.
6. Keep Keycloak as the preferred candidate, with breached-password screening and the ADR’s outstanding acceptance tests completed before staff use.
7. Validate established Tojima sessions against local PostgreSQL authorization state; fail closed for new login and expired step-up during an IdP outage.
8. Immediate revocation requires a synchronous Tojima database commit; asynchronous Keycloak events alone cannot guarantee it.
9. Adopt ten-year financial archives, five-year attendance records, and at least 90 days of searchable security logs, with legal categories kept distinct.
10. The dependable fallback is existing owned hardware; a second permanent, card-safe ¥0 cloud architecture meeting every requirement is **UNVERIFIED**.

**Scope and evidence**

This is read-only advice. I read all three requested files; no project files were modified and no accounts were created. Official public pages were retrieved live. **UNVERIFIED** means public evidence did not establish the fact or account-specific entitlement. Recommendations and sizing estimates are engineering judgments, not vendor guarantees.

ADR-0003 remains controlling project context: the PoC’s “12 PASS / 1 PARTIAL” is historical test evidence, not adoption approval. Mobile devices, offline-token non-resurrection, Step36 notifications, and the availability design still need acceptance.

**1. Hosting decision and free quotas**

| Option | Current free allowance / conditions | Decision for Tojima |
|---|---|---|
| **OCI A1 — Free tenancy** | **1,500 OCPU-hours + 9,000 GB-hours/month**, equivalent to **2 OCPUs / 12 GB** continuously. Home region only. | Best cloud candidate for the complete container stack. [Official limits](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm) |
| **OCI A1 — PAYG** | October 8, 2026 price list gives A1 **3,000 OCPU-hours + 18,000 GB-hours/month** free: approximately 4 OCPUs / 24 GB continuously. Account-specific eligibility is **UNVERIFIED**; excess usage is billable. | Do not upgrade under the current prohibition on billable resources. [Price list, p.55](https://www.oracle.com/il/a/ocom/docs/corporate/pricing/oracle-paas-and-iaas-global-price-list.pdf) |
| **OCI disks / backup / transfer** | **200 GB** boot+block storage combined; **five** volume backups; Free-only Object Storage **20 GB combined**, **50,000 requests/month**; outbound **10 TB/month**. | Enough to begin; account-wide quotas include every environment. [Official limits](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm) |
| **Google Compute Engine** | One e2-micro, **30 GB-month standard persistent disk**, **1 GB/month outbound**. Eligible regions: Oregon, Iowa, South Carolina. **Tokyo is excluded.** | Too small for this complete stack; also requires ongoing billing eligibility. [Free-tier terms](https://docs.cloud.google.com/free/docs/free-cloud-features) |
| **Cloud Run** | Request-based allowance: **180,000 vCPU-seconds, 360,000 GiB-seconds, 2 million requests/month**; applied using us-central1 prices. Instance-based has separate **240,000 / 450,000** compute allowances. | Useful later for Go HTTP workloads. Not a permanent PostgreSQL host; continuous workers/IdP consume compute beyond intermittent-request economics. [Pricing](https://cloud.google.com/run/pricing) |
| **Cloud SQL / AlloyDB** | No permanent free transactional PostgreSQL allowance established. Published free offers are trials. | Excluded now. [Google free offers](https://docs.cloud.google.com/free/docs/free-cloud-features) |
| **AWS new-account Free plan** | **$100 credits initially**, up to **$100 earned**; closes after **six months or credit exhaustion**, whichever comes first. No charges unless converted to Paid or paid-only services activated. | Trial/credit-funded hosting; excluded as the permanent architecture. [AWS Free Tier](https://aws.amazon.com/free/) |
| **Cloudflare Workers Free** | **100,000 requests/day**, **10 ms CPU/request**, **128 MB memory**. | Edge logic, not a replacement for the existing Go container, PostgreSQL, and Keycloak. [Limits](https://developers.cloudflare.com/workers/platform/limits/) |
| **Cloudflare Pages Free** | **500 builds/month**, one concurrent build, **20,000 files/site**, **25 MiB/file**. Functions consume Workers allowances. | Good for the Vite SPA. [Limits](https://developers.cloudflare.com/pages/platform/limits/), [billing](https://developers.cloudflare.com/workers/platform/pricing/) |
| **Cloudflare Tunnel** | Available on all plans; exposes an origin through outbound connections. | Suitable free ingress to the VM. Use a named stable tunnel and existing domain. [Official documentation](https://developers.cloudflare.com/tunnel/) |
| **Cloudflare D1 Free** | **5 million rows read/day**, **100,000 written/day**, **5 GB/account**; **500 MB/database**, ten databases; seven-day Time Travel. | SQLite semantics, so it does not satisfy the selected shared PostgreSQL design. [Pricing](https://developers.cloudflare.com/d1/platform/pricing/), [limits](https://developers.cloudflare.com/d1/platform/limits/) |
| **Hyperdrive Free** | **100,000 database queries/day**. Supplies connection pooling/caching, not the underlying database. | Unnecessary for Go connecting directly to its local PostgreSQL. [Pricing](https://developers.cloudflare.com/hyperdrive/platform/pricing/) |
| **Cloudflare WAF rate limiting** | **One free rule per zone**, IP counting, ten-second counting/mitigation periods. Enforcement can lag. | Put it on login-sensitive paths; retain Keycloak and application rate controls. [Rule availability and limitations](https://developers.cloudflare.com/waf/rate-limiting-rules/) |

**OCI capacity and account risks**

Tokyo (`ap-tokyo-1`) and Osaka (`ap-osaka-1`) are live regions, each with one availability domain. A live region does not establish free-instance capacity. “Out of host capacity” can prevent initial deployment and replacement. [Region identifiers](https://docs.oracle.com/en-us/iaas/Content/General/Concepts/regions.htm)

Oracle may reclaim idle Always Free instances when, over seven days, CPU p95, network utilization, and—on A1—memory utilization are all below 20%. Do not manufacture load to evade reclamation. [Reclamation policy](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm)

Free Tier has no SLA. Oracle also permits suspension/termination for abandoned accounts idle for at least 30 days and for terms violations. Staying Free avoids paid-resource access after the trial; upgrading retains applicable free allowances but enables overage billing. **PAYG exemption from idle reclamation is UNVERIFIED.** [Oracle FAQ](https://www.oracle.com/cloud/free/faq/)

**Free managed PostgreSQL**

| Provider | Free resources | Region near Japan | Recovery / inactivity | Fit |
|---|---|---|---|---|
| **Neon** | **1 GB/project**, **100 CU-hours/project/month**, up to 2 CU, **5 GB/project egress**; no card required. Current pricing lists 100 projects. | Singapore verified; Tokyo absent from the published region list. | Scales to zero after five idle minutes. Free instant restore: **up to six hours or 1 GB of changes**. | Strong dev/demo option. Constant Keycloak/worker activity can exhaust compute: even 0.25 CU continuously needs about 183 CU-hours/month. [Pricing](https://neon.com/pricing), [regions](https://neon.com/docs/introduction/regions) |
| **Supabase** | **500 MB DB**, shared CPU/**500 MB RAM**, two active projects, **5 GB egress**, separate cached-egress allowance. | Tokyo available. | Pauses after one inactive week. Free-tier users are instructed to export off-site backups; no free PITR entitlement established. | Good small test DB; limited headroom for application data plus IdP/audit growth. [Pricing](https://supabase.com/pricing), [regions](https://supabase.com/docs/guides/platform/regions), [backups](https://supabase.com/docs/guides/platform/backups) |
| **Aiven** | One node, **1 CPU / 1 GB RAM / 1 GB disk**, 20 connections, no pooling; no card or time limit. | Free-plan Tokyo/Singapore selection **UNVERIFIED**; Aiven reserves the right to change region. | Backups included; free PITR/retention **UNVERIFIED**. Inactive services may be powered off after notification. | Worth evaluating for dev; not assumed to satisfy recovery requirements. [Official free-plan terms](https://aiven.io/docs/products/postgresql/concepts/pg-free-tier) |
| **Render** | Free PostgreSQL expires after **30 days**. | Irrelevant to permanent selection. | Paid databases get ongoing recovery features. | Exclude: a time-limited offer. [Official service terms](https://render.com/docs/service-types) |

Keep Go as the sole business-write authority with any BaaS. Do not expose business tables through client-side database credentials or treat provider Auth as tenant/store authorization.

**Recommended ¥0 architecture**

```text
Vite SPA → Cloudflare Pages
Staff/browser → Cloudflare → named Tunnel → reverse proxy
                                                ├─ Go API
                                                ├─ Keycloak
Stripe webhooks ────────────────────────────────→ Go API
                                                └─ PostgreSQL
Go payment worker ─────────────────────────────→ PostgreSQL / Stripe

Encrypted backups → OCI Object Storage
                  → second copy on existing owner-controlled storage
```

Use **Compose first**, with ARM64 images:

- One A1 VM at **2 OCPUs / 12 GB**, with an explicitly limited Keycloak container.
- Separate PostgreSQL databases and roles for Keycloak and Tojima; separate credentials/databases/realms for staging. Do not let the IdP database role access business tables.
- Private container networking for PostgreSQL; only the reverse proxy accepts application ingress. Restrict administrative interfaces.
- Preserve tenant/store constraints, current-membership checks, durable payment inbox/outbox, and idempotency.
- Budget backup storage explicitly: daily encrypted backups plus WAL where capacity permits, monitored against the 20 GB object allowance. Keep encryption keys and a second recoverable copy outside the OCI account.
- Retain 90 days of security events, with bounded operational logs and disk alerts.

These are proposed controls, not validated deployment results.

**Compose versus k3s:** both can run on this machine. Compose leaves more memory and operational attention for PostgreSQL and identity. Learn single-node k3s locally using the same container images; it adds no host-failure availability. Migration to k3s does not itself supply database replication, fencing, backups, or recovery.

**Fallback:** restore the same Compose stack onto an existing owner-controlled Linux machine with approximately 8–12 GB available RAM, existing broadband, and Cloudflare Tunnel. Incremental cloud subscription cost can remain ¥0; hardware availability is **UNVERIFIED**, and electricity/ISP costs remain real. Maintain the public IdP hostname/RP ID to preserve Passkey usability.

Osaka is an alternative **initial home-region choice**, not an assumed second free deployment. No compliant, universally available second-cloud fallback was verified. If neither OCI capacity nor suitable existing hardware exists, retain a local demo until the constraint changes.

**2. Cost and protection against charges**

**Expected fixed infrastructure subscription: ¥0/month**, conditional on remaining within the selected free entitlements. This excludes domain renewal, labour, electricity, and payment processing. Stripe transactions incur commercial fees even when hosting is free. [Stripe Japan pricing](https://stripe.com/jp/pricing)

| Service/account | Card and charge boundary | Recommended control |
|---|---|---|
| **OCI Free-only** | Card required for identity verification; temporary authorization holds can occur. Oracle disallows prepaid/virtual/single-use cards. | Remain Free-only; never upgrade or activate paid services. MFA-protect tenancy ownership. Monitor allowance use and account notices. [FAQ](https://www.oracle.com/cloud/free/faq/) |
| **OCI PAYG** | Chargeable above free allowances. A budget is not a universal hard stop. | Excluded now. Later use compartment quotas, restrictive IAM, resource inventory and alerts; these cannot establish “card never charged.” [OCI allowance controls](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm) |
| **Google Cloud** | Continued Free Tier use requires billing eligibility; paid billing charges overages. Trial-only accounts stop when the trial expires. | Excluded from the strict non-billable architecture. [Free-program terms](https://docs.cloud.google.com/free/docs/free-cloud-features) |
| **Google budgets** | Alerts-only budgets do not cap spending. New spend-cap budgets cover selected services, including Cloud Run, but enforcement is delayed and overages remain billable; persistent costs continue. | Useful later, not a ¥0 guarantee. [Alerts](https://docs.cloud.google.com/billing/docs/how-to/budgets), [spend-cap limitations](https://docs.cloud.google.com/billing/docs/how-to/budgets-spend-caps) |
| **Cloudflare** | Use Free Pages, Tunnel and WAF. Exact current card requirement for every Zero Trust onboarding path is **UNVERIFIED**. | Do not activate Workers Paid, R2 billing or paid add-ons. Treat any required paid enrollment as outside the recommendation. [Plans](https://www.cloudflare.com/plans/) |
| **Neon / Aiven** | Officially offer no-card free PostgreSQL. | Keep Free; accept suspension at limits rather than upgrading. [Neon](https://neon.com/pricing), [Aiven](https://aiven.io/docs/products/postgresql/concepts/pg-free-tier) |
| **AWS Free plan** | Official terms provide a no-charge boundary until paid conversion/paid-only activation, but hosting expires. | Suitable only for temporary learning, excluded from the permanent plan. [AWS terms](https://aws.amazon.com/free/) |

Possible accidental costs include oversized disks, extra snapshots, object versions, external IPv4, NAT/load balancers, container registries, log ingestion, mail/SMS, and cross-region traffic. For example, Google’s standard VM external IPv4 price is **$0.005/hour**, independently of compute eligibility. [VPC pricing](https://cloud.google.com/vpc/pricing)

For free-resource monitoring, alert at approximately **50%, 75%, and 90%** of storage, backup-request and transfer allowances. At capacity, stop nonessential jobs and fail new writes safely; do not delete legally retained records or disable security logging to remain free.

Identity email is also required. Oracle’s October rate card includes the first **3,000 Email Delivery emails/month** free, but sender/domain approval and actual Free-tenancy send limits remain deployment checks. Queue Step36 notifications durably; delivery failure must not remove a security hold. [Oracle price list, p.69](https://www.oracle.com/il/a/ocom/docs/corporate/pricing/oracle-paas-and-iaas-global-price-list.pdf)

**3. Identity recommendation under ¥0**

**Choose self-hosted Keycloak as the preferred candidate. No candidate is currently proven to satisfy the complete Tojima contract without the outstanding acceptance work.**

Keycloak recommends a **2 GB container memory limit** for smaller production deployments; its default maximum heap is 70% of the container limit. Thus it fits beside PostgreSQL on 24 GB, and plausibly on the current 12 GB allowance. Proposed starting budget: Keycloak 2 GB, PostgreSQL approximately 3 GB, API/worker 1 GB, OS/proxy/monitoring 1 GB, leaving headroom. Actual concurrent-login and backup performance is **UNVERIFIED**. [Keycloak container guidance](https://www.keycloak.org/server/containers)

| Candidate | ¥0 capability and material gaps | Disposition |
|---|---|---|
| **Keycloak self-hosted** | Native credential, Passkey, MFA, recovery-code and hosted UI capabilities; best existing Tojima evidence. R01 and ADR acceptance gaps remain. | **Preferred.** No independent password/WebAuthn/MFA implementation in Go. |
| **ZITADEL Cloud Free** | **100 DAU**, unlimited stored users/organizations, three external IdPs; one instance/admin, **5,000 management API requests** in the published allowance. **One-day audit history**, 30-minute runtime logs. Regions: US/EU/Switzerland/Australia. | R11 fails native 90-day retention; no Japan region. Prior R02/R06/R07/R08/R10/R13 gaps remain unclosed by this research. [Pricing](https://zitadel.com/pricing), [detailed limits](https://zitadel.com/pricing/detail) |
| **ZITADEL self-hosted** | Avoids Cloud retention/region limits; AGPL deployment available, commercial licensing optional. Recovery-code API exists. | Re-test prior gaps; do not infer that API availability supplies the required recovery UI. [Licensing](https://help.zitadel.com/zitadel-licensing-faqs), [recovery API](https://zitadel.com/docs/reference/api/user/zitadel.user.v2.UserService.GenerateRecoveryCodes) |
| **Ory self-hosted** | Credential/Passkey/MFA engines available; more integration and UI ownership. Enterprise adds extended audit features. | Prior R01/R08/R11 gaps remain. Ordinary self-collected security logs may help R11, but completeness and usable UI are **UNVERIFIED**; paid Enterprise is not automatically necessary for every log requirement. [Official offering](https://www.ory.com/open-source) |
| **authentik OSS** | Free OIDC, Passkeys and native single-use backup codes. | Credible alternative; complete R01–R13 behaviour, especially immediate revocation, absolute lifetime, safe factor changes and non-resurrection, is **UNVERIFIED**. [Pricing](https://goauthentik.io/pricing/), [Passkeys](https://docs.goauthentik.io/add-secure-apps/flows-stages/stages/authenticator_webauthn/), [backup codes](https://docs.goauthentik.io/add-secure-apps/flows-stages/stages/authenticator_static/) |
| **Authelia** | Free IAM/OIDC; current versions support passwordless Passkeys. | Do not dismiss it as “second-factor WebAuthn only.” Complete invite/recovery/account-lifecycle requirements are **UNVERIFIED**. [Official overview](https://www.authelia.com/), [Passkey support](https://www.authelia.com/overview/authentication/security-key/) |
| **Logto** | Self-hosted OSS includes authentication/MFA capabilities. Cloud Free pricing excludes MFA; paid add-ons are outside scope. | OSS remains a contender, but complete R01–R13 acceptance is **UNVERIFIED**. Distinguish paid Console MFA enforcement from end-user MFA. [Cloud pricing](https://logto.io/pricing), [OSS terms](https://logto.io/self-hosted-plans), [MFA](https://logto.io/products/mfa) |
| **Supabase Auth Free** | 50,000 MAU; current Passkeys **and recovery-code APIs exist**, both experimental. Native time-boxed sessions require Pro or above. | R02/R03/R06 need current qualification; R08 needs Tojima enforcement on Free; JWT revocation still requires server-side checks. Complete ready-made ja/zh-TW security UI is **UNVERIFIED**. [Passkeys](https://supabase.com/docs/guides/auth/passkeys), [recovery codes](https://supabase.com/docs/reference/javascript/auth-mfa-recovery-codes-generate), [session limits](https://supabase.com/docs/guides/auth/sessions) |
| **Firebase Auth / Identity Platform** | Email/password supported. Identity Platform publishes 50,000 free Tier-1 MAU; Spark upgraded projects have a 3,000 DAU limit. | Native standalone WebAuthn Passkey and single-use MFA recovery-code support meeting R02/R03/R06 is **UNVERIFIED**. Google-account Passkeys do not establish Firebase-native Passkeys. [Capabilities](https://firebase.google.com/docs/auth/), [pricing](https://cloud.google.com/identity-platform/pricing) |
| **Cloudflare Access** | Useful edge access control; free offering covers small teams. OTP or integrated identity can protect applications. | Complete R01–R13 staff identity is **UNVERIFIED**. Use as an administrative perimeter if appropriate, not as tenant/store authorization. [Access offering](https://www.cloudflare.com/zero-trust/products/access/), [OTP behaviour](https://developers.cloudflare.com/cloudflare-one/integrations/identity-providers/one-time-pin/) |

For Keycloak, retain the PoC distinction:

| Requirement | Current evidence | Cheap closure / required acceptance |
|---|---|---|
| **R01 Email/password** | **PARTIAL**: lockout tested; ten static denied passwords do not establish breached-password screening. | Reviewed HIBP password-policy extension or sufficiently maintained offline denylist; test every password creation/change/reset path. |
| **R02 Web Passkey** | PoC PASS. | Re-run against selected version, hostname and actual browsers. |
| **R03 Flutter Passkey** | System-browser path passed in PoC; real iOS/Android devices unverified. | Authorization Code + PKCE through system browser; verify return links and enrollment/login on both platforms. |
| **R04 Invite-only** | PoC PASS. | Disable public registration; test invitation reuse, expiry and role assignment. |
| **R05 Manager MFA** | PoC PASS. | Enforce required assurance in Go; do not equate any Passkey login automatically with the required assurance. |
| **R06 Single-use recovery codes** | PoC PASS. | Test reuse rejection, replacement invalidation, UI and audit. |
| **R07 Immediate revocation** | PASS using per-request introspection. | Local-session design below requires new acceptance. |
| **R08 30-day absolute cap** | PoC PASS with configuration conditions. | Enforce an immutable original session deadline in Go; prohibit offline access. |
| **R09 300-second step-up** | PoC PASS. | Fixed expiry from verified fresh authentication; never slide on activity. |
| **R10 Email/factor changes** | PoC PASS. | Verify fresh authentication, notification and session invalidation across every route. |
| **R11 90-day logs** | PoC PASS by configuration. | Verify event coverage, search/export and restore; preserve Go authorization/payment events too. |
| **R12 Disable/restore** | PoC PASS. | Add Step36 user-only notifications and email-failure hold tests. |
| **R13 Old tokens never revive** | Conditional PASS. | Verify all-client consent/session revocation, offline-token prohibition, factor-reset logout and recovery after backup restore. |

The broader deployment remains **PARTIAL** until these outstanding checks pass. The source report’s ja/zh-TW support also needs screen-by-screen acceptance for login, recovery, factor changes and errors.

For **R01**, HIBP’s Pwned Passwords range API is free and needs no subscription key. It sends only the first five hexadecimal characters of a SHA-1 password hash; compare suffixes locally and request padding. Never send plaintext passwords or full hashes. [HIBP API](https://haveibeenpwned.com/API/v3#PwnedPasswords)

An existing [Keycloak password-policy extension](https://github.com/rishvic/keycloak-pwned-password-policy) is a candidate, **not a verified dependency**: compatibility, maintenance, failure behaviour and supply-chain review remain outstanding. Fail closed for password setting when screening cannot complete; existing authenticated sessions need not stop. Keycloak also supports a native password blacklist, but a tiny or stale list cannot close R01. [Keycloak password policies](https://www.keycloak.org/docs/latest/server_admin/#_password-policies)

**4. D5: behaviour when the IdP is unavailable**

| Design | IdP unavailable, Go/PostgreSQL healthy | Revocation consequence |
|---|---|---|
| **Every request introspected** | Staff API requests fail closed immediately. | Straightforward online validation; availability depends on the IdP for every operation. |
| **Locally validated JWT only** | Issued JWTs work until expiry. | Revoked credentials can remain usable until expiry. Does not satisfy immediate revocation alone. |
| **Tojima session + PostgreSQL authorization/revocation checks** | Established sessions continue within existing lifetime/assurance limits. New login, recovery and fresh step-up fail closed. | Immediate for revocations synchronously committed in Tojima; external-only changes need a reliable propagation contract. |

**Recommend the third design**, implemented within the shared MOD-08 adapter using established OIDC/session libraries:

1. Keycloak performs credential authentication, Passkeys, MFA and recovery. Go never receives or verifies those credentials.
2. On successful OIDC login, Go establishes a server-side session mapped to the Keycloak `sub` and `sid`. Store original authentication time, absolute expiry, assurance and step-up expiry.
3. Every request checks the PostgreSQL primary for session validity, global security hold, current membership, tenant/store scope and authorization version. Avoid positive authorization caches.
4. For suspension, offboarding or privilege removal, **commit the Go security change first**, alongside the audit record and durable outbox. Then disable/logout/revoke in Keycloak. IdP failure leaves the local hold active.
5. Restoring an account never restores old sessions or step-up grants. Require fresh complete authentication.
6. Consume signed Keycloak back-channel logout and administrative events as additional revocation inputs. Validate issuer, audience, signature and replay protection. Keycloak documents direct back-channel logout, but generic administrative event forwarding needs an explicit integration. [Keycloak logout and events](https://www.keycloak.org/docs/latest/server_admin/)

**Maximum staleness:**

- **Tojima-controlled revocation:** zero authorization-cache TTL; the next authorization check after commit rejects it. Already-authorized in-flight operations require transaction-level locking/version checks for sensitive mutations.
- **External Keycloak changes:** asynchronous delivery has **no finite guaranteed maximum** during a partition. A five-minute JWT gives up to five minutes of exposure; it is not immediate revocation.
- Therefore route all supported security-changing operations through the Go coordinator, including factor-reset/account-change workflows. Lock down direct administration and provide a break-glass revocation procedure.
- If direct Keycloak changes cannot be coordinated reliably, retain introspection for affected operations or fail closed when synchronization is uncertain. Continuous operation and immediate detection of unreachable external changes cannot both be guaranteed.

Use an initial ordinary-session horizon of one shift—approximately 8–12 hours—within the **30-day absolute maximum**. Existing step-up expires after **300 seconds** even during an outage. PostgreSQL or authorization-state uncertainty stops staff mutations.

This protects against an **IdP process outage**. A VM, disk or shared PostgreSQL outage still stops the whole stack.

Payment webhook authentication remains independent of staff identity. Verify signatures and durably commit the event before returning success; otherwise return failure. Process using account-scoped identifiers, uniqueness constraints and idempotent transitions. After recovery, reconcile Stripe state before retrying uncertain payment actions. [Stripe webhook guidance](https://docs.stripe.com/webhooks)

**5. Japanese retention policy**

Grades: **A = explicit statutory baseline verified; B = legal applicability depends on the record/entity; C = recommended product/security policy.**

| Record | Grade and legal basis | Recommended Tojima retention |
|---|---|---|
| **Orders, sales books, receipts and supporting transaction records** | **A:** Corporation-tax baseline is **seven years from the day after the return filing deadline**. Specified blue-return loss years/disaster-loss years require **ten years**; this is not every year in which a loss is merely carried forward. [NTA No.5930](https://www.nta.go.jp/taxes/shiraberu/taxanswer/hojin/5930.htm) | **Ten years**, calculated from the applicable legal starting point. Preserve corrected/cancelled versions and accounting links. |
| **Corporate account books and important business materials** | **B:** Companies Act Art.432(2) separately specifies ten years for stock companies, from closing the books. Applicability depends on entity/record classification. [Official government translation](https://www.cas.go.jp/jp/seisaku/hourei/data/Corp_2.pdf) | Apply the longer applicable period; ten-year financial policy avoids relying solely on the seven-year tax minimum. |
| **Electronic receipts/invoices and transaction data** | **A/B:** Electronic Bookkeeping Act controls preservation method; it does not independently make every record “seven years.” Electronic transaction records require appropriate electronic preservation, authenticity and accessibility. [NTA guidance](https://www.nta.go.jp/about/introduction/torikumi/week/dennsityoubo.htm) | Same **ten-year financial archive**. Preserve original electronic records, searchable metadata and correction history; screenshots alone are insufficient. |
| **Payment, refund, settlement and reconciliation records** | **B:** Accounting evidence follows applicable tax/company retention. A universal separate Stripe-specific statutory duration was **UNVERIFIED**. | **Ten years** for financial facts and necessary provider references. Keep secrets and unnecessary payload fields out of the archive. |
| **Attendance, actual work time, payroll-supporting shift records** | **A:** Labour Standards Act Art.109 says **five years**; Art.143 currently applies **three years “for the time being.”** [MHLW explanation](https://www.startup-roudou.mhlw.go.jp/qa/zigyonushi/syuugyoukisoku/q6.html) | **Five years** from the applicable record-specific starting point. Payroll/tax records may require longer; avoid retaining irrelevant draft schedules automatically. |
| **Authentication, security and authorization logs** | **C:** No universal fixed Japanese retention duration for this ordinary system was verified. APPI requires appropriate security measures. [PPC guidance](https://www.ppc.go.jp/personalinfo/legal/guidelines_tsusoku/) | **90 days searchable minimum**, as required by R11. Consider one year of minimized privileged-security events; financial audit events follow the financial archive. |
| **Guest personal data** | **A/C:** APPI has no general fixed retention period; make efforts to erase personal data without delay when no longer needed. Other lawful retention duties remain. [PPC FAQ](https://www.ppc.go.jp/all_faq_index/faq1-q5-2/) | Avoid collection where possible. Proposed default: remove unnecessary contact/session data **30 days after completed service**, subject to actual dispute/support needs. Retain required financial records separately with minimized identity. |
| **Departed staff IdP accounts** | **C:** No universal IdP-account retention period was verified. Credential access and employment-record retention serve different purposes. | Revoke immediately at the effective departure time. Remove credentials/profile after a documented **30–90-day** administrative window; preserve a minimal non-reusable subject reference in lawful audit/employment records. |

Backups need their own expiry and deletion policy. Restoring an old backup must reapply account holds, revocations and deletion decisions before reopening access. A 30-day recovery backup is not a ten-year legal archive.

The free storage allowance is a **capacity limit, not a retention exemption**. Measure archive growth before pilot admission; if compliant retention no longer fits free resources and existing owned storage, the deployment cannot remain an accepted ¥0 pilot.

**6. Upgrade path after the owner changes the ¥0 constraint**

First add **recoverability**, then remove reclaimable capacity, then add availability. Kubernetes is not the first production purchase.

The following are **planning estimates; exact Tokyo SKU totals are UNVERIFIED**, not vendor quotes. Assumptions: small workload, roughly 730 hours/month, no commitments, and **¥150/USD solely for budgeting**, not a verified exchange rate. Taxes, labour and Stripe fees are excluded.

| Step | Concrete addition | Rough monthly budget | Recovery implication |
|---|---|---:|---|
| **0 — Free pilot** | Single OCI VM, measured encrypted backups, second existing-storage copy, restoration drill. | **¥0 cloud subscription** | No guaranteed RTO. Nightly-only backups can lose approximately 24 hours; do not claim RPO five minutes. |
| **1 — Independent durable backup** | GCP Tokyo object storage, PostgreSQL base backups + continuous WAL, backup-age alerts, retained configs/keys and restoration scripts. | **+$5–20 / ¥750–3,000** | Set WAL archive timeout around 60 seconds and alert before five minutes of unprotected changes. RPO remains unproven until measured restores succeed. [Storage pricing](https://cloud.google.com/storage/pricing) |
| **2 — Paid, reproducible single host** | GCP Tokyo VM around 2 vCPU/8 GB, appropriate persistent disk, bounded logs and automated recreation. Keep Compose. | **$65–110 total / ¥9,750–16,500** | Removes Free-tier reclamation dependence; still one failure domain. RTO one hour requires a demonstrated timed rebuild/restore. [Compute pricing](https://cloud.google.com/products/compute/pricing), [network pricing](https://cloud.google.com/vpc/pricing) |
| **3 — Managed PostgreSQL** | Once authorized, move the shared cluster to Cloud SQL PostgreSQL with PITR, private access and deletion protection. | **$180–320 total / ¥27,000–48,000**, single-zone starting design | Reduces database operations burden. PITR creates a new instance; test routing, secrets and application recovery. [SQL pricing](https://cloud.google.com/sql/pricing), [PITR behaviour](https://docs.cloud.google.com/sql/docs/postgres/backup-recovery/pitr) |
| **4 — Zone-failure resilience** | Regional HA PostgreSQL, two application/IdP instances across zones, load balancing, worker fencing/leases and external monitoring. | **$400–700 total / ¥60,000–105,000** | Practical basis for the later RPO five-minute/RTO one-hour target. Targets still require failure and restore drills. [SQL pricing](https://cloud.google.com/sql/pricing), [compute pricing](https://cloud.google.com/products/compute/pricing) |
| **5 — Orchestration / second cloud** | Adopt k3s/GKE only when deployment/team needs justify it; later add independently recoverable cross-region/cloud backups. | **UNVERIFIED** until architecture is specified | Keep one transactional writer with explicit failover and fencing. Two clouds do not automatically improve payment correctness. |

For pilot admission, the remaining concrete gates are: available Free-only capacity, completed Keycloak acceptance gaps, a proven revocation contract, successful backup restoration, sufficient legal-retention capacity, and payment reconciliation after simulated outages. If these cannot be demonstrated within ¥0, keep the environment at dev/demo status.


