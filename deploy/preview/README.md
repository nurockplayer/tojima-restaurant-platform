# Internal preview (Pages + TachiNAS)

Internal test environment only: synthetic data, cash only (Stripe disabled),
staff sign-in through the real Keycloak realm. Not production hosting.

```text
phone -> https://tojima.jabiko.app       Cloudflare Pages: SPA + Function proxy
           /v1/*, /staff/auth/*, /healthz -> https://tojima-api.jabiko.app
phone -> https://tojima-id.jabiko.app    Keycloak
tojima-api / tojima-id -> named tunnel "tojima-preview" -> cloudflared (compose service on the NAS)
```

The tunnel ingress (managed in Cloudflare) sends `tojima-api` to `http://server:8080`
with `httpHostHeader: tojima.jabiko.app`, so the server's Host/Origin checks and
`__Host-` cookies see a single first-party origin, and `tojima-id` to
`http://keycloak:8080`. `_routes.json` invokes the Function only for proxied
paths; static assets do not use Workers requests. Every proxied request counts
against the Workers Free quota (100,000 requests/day), which the 2.5 s guest
cart polling can exhaust in a busy store; SSE is planned before the pilot.

## Backend host

Copy the repository with `COPYFILE_DISABLE=1 tar` (macOS AppleDouble `._*`
files break the Keycloak realm import), then on the host:

```sh
sh deploy/preview/env.sh                  # once: random secrets in deploy/preview/.env (0600)
sh deploy/preview/set-token.sh < token    # tunnel token on stdin
sh deploy/preview/preview.sh up -d --build --wait
python3 deploy/preview/seed.py "$PWD" "$PWD/deploy/preview/.env" tojima-preview   # once per fresh volume
python3 deploy/preview/qr.py deploy/preview/tables.csv deploy/preview/tables.html
```

`seed.py` provisions the store with `/storeadmin`, publishes a five-item menu with
a temporary MFA seed user (held and deleted afterwards), and creates Keycloak user
`owner` with a temporary password and `UPDATE_PASSWORD` + `CONFIGURE_TOTP`. It
writes `tables.csv` and `owner-credentials.txt` (0600) next to `.env`; never paste
them into chat or commits. Seeding is one-shot and refuses to run twice.

**Re-seed** (also after a failed seed, which can leave Keycloak users behind):
`preview.sh down -v`, delete `tables.*` and `owner-credentials.txt`, then
`preview.sh up -d --build --wait` and run `seed.py` again. All preview data is lost. Stop with `preview.sh stop`, resume with `preview.sh start`;
`preview.sh down -v` deletes all data.

## Frontend

`sh deploy/preview/deploy-pages.sh` builds the SPA in Docker and deploys it with
the Function to Pages project `tojima-preview` (custom domain `tojima.jabiko.app`).

## Phone-journey E2E

`sh deploy/preview/e2e.sh` runs the Owner's 12-step phone checklist against
`https://tojima.jabiko.app` with no human input, once per engine and
sequentially: Playwright Chromium with the `Pixel 7` descriptor, then Playwright
WebKit with `iPhone 15`. Staff and the two guests are separate browser contexts.

The staff account is `e2e-bot` signing in through the real Keycloak realm
(password, then OTP; the secret is enrolled on the first run and written back to
`~/.tojima-preview-nas/e2e-credentials.json`, 0600), followed by a step-up so
manager actions are allowed. The journey uses `4番卓` only, and leaves it closed
and カレーライス on sale afterwards, even after a failure.

Covered: staff sign-in and step-up; open and close a table; guest menu; cart
sync between two guests; cash order submit showing 店員の受付待ち · 未払い or
受付済み · 未払い; accept on `/staff/` when required; start and finish cooking on `/kitchen/`; served, and the
guest status updating without a reload; cash settle and close; a sold-out
カレーライス that cannot be ordered; Traditional Chinese text with the 1,100
price; add-control accessible names and where focus lands after adding;
landscape rotation without horizontal overflow; a refetch after returning from
the background.

Results: `deploy/preview/e2e/out/report.json` plus a screenshot per failed step,
a table on stdout and a last line of `RESULT: PASS` or `RESULT: FAIL <n> steps`
(exit 0/1). `E2E_ONLY=webkit sh deploy/preview/e2e.sh` runs a single engine.

Two gaps are reported, not hidden. The store catalog console (`/catalog/`)
connects only with a staff access token, never with the Keycloak session, so the
sold-out step drives the same admin endpoint the console uses and the report says
so. The kitchen board starts and finishes cooking but has no "served" action, so
the order is served from `/staff/` after the board.

Not covered: real iOS Safari and Android Chrome on physical devices,
VoiceOver/TalkBack speech output, the Safari dynamic toolbar, and table entry
modes other than the preview's `STAFF_OPEN`.

## Current deployment (2026-10-29 JST)

| Item | Value |
|---|---|
| Cloudflare account | `6ad170220aed1f2e19a535b093750331`, zone `jabiko.app` (Free) |
| Pages project | `tojima-preview` → custom domain `tojima.jabiko.app` (DNS CNAME `tojima` → `tojima-preview.pages.dev`), var `API_ORIGIN=https://tojima-api.jabiko.app` |
| Named tunnel | `tojima-preview` (`84535fc0-6bd1-42b4-8d55-b990480051e7`), remotely managed; CNAMEs `tojima-api`, `tojima-id` → `<id>.cfargotunnel.com` |
| Backend host | Owner's Synology NAS (`ssh TachiNAS`, DSM 7.x x86_64), checkout `~/tojima-preview`, compose project `tojima-preview`; user in the `docker` group, socket group restored at boot by `/usr/local/etc/rc.d/docker-sock-group.sh` |
| Secrets | NAS `~/tojima-preview/deploy/preview/.env` (0600; DB, Keycloak, PIN pepper, tunnel token) |
| Seed outputs | NAS `deploy/preview/{tables.csv,owner-credentials.txt}`; Owner's Mac copy `~/.tojima-preview-nas/` (0600) |
| Staff account | Keycloak user `owner` (TENANT_ADMIN, STORE_MANAGER, KITCHEN, STAFF_CASHIER on `preview-co/main`) |

Update: copy the new checkout to the NAS (keep `deploy/preview/.env`), run
`preview.sh up -d --build --wait`, and `deploy-pages.sh` for frontend changes.
Retire: `preview.sh down -v --rmi local` on the NAS, then delete the Pages
project, the tunnel and the three DNS records in Cloudflare.
