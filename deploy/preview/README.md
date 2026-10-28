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
