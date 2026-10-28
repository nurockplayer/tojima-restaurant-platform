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
them into chat or commits. Stop with `preview.sh stop`, resume with `preview.sh start`;
`preview.sh down -v` deletes all data.

## Frontend

`sh deploy/preview/deploy-pages.sh` builds the SPA in Docker and deploys it with
the Function to Pages project `tojima-preview` (custom domain `tojima.jabiko.app`).

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
