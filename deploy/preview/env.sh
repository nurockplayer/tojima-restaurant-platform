#!/bin/sh
# Write deploy/preview/.env once with fresh random secrets. Add the tunnel
# token afterwards with set-token.sh.
set -eu
HERE=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
[ -e "$HERE/.env" ] && { echo "$HERE/.env exists; refusing to overwrite secrets" >&2; exit 1; }
APP=https://tojima.jabiko.app
KC=https://tojima-id.jabiko.app
secret=$(openssl rand -hex 48)
umask 077
cat >"$HERE/.env" <<ENV
POSTGRES_USER=tojima
POSTGRES_DB=tojima
POSTGRES_PASSWORD=$(openssl rand -hex 32)
APP_DB_USER=tojima_app
APP_DB_PASSWORD=$(openssl rand -hex 32)
KEYCLOAK_DB_PASSWORD=$(openssl rand -hex 32)
APP_BIND=127.0.0.1
APP_PORT=18080
KEYCLOAK_PORT=18081
KEYCLOAK_PUBLIC_URL=$KC
KEYCLOAK_BACKCHANNEL_LOGOUT_URL=http://server:8080/staff/auth/backchannel-logout
KC_BOOTSTRAP_ADMIN_USERNAME=preview-admin
KC_BOOTSTRAP_ADMIN_PASSWORD=$(openssl rand -hex 32)
TOJIMA_STAFF_CLIENT_SECRET=$secret
STAFF_AUTH_MODE=oidc
STAFF_OIDC_ISSUER=$KC/realms/tojima
STAFF_OIDC_DISCOVERY_URL=http://keycloak:8080/realms/tojima
STAFF_OIDC_CLIENT_ID=tojima-staff
STAFF_OIDC_CLIENT_SECRET=$secret
APP_PUBLIC_ORIGIN=$APP
SESSION_TTL=4h
GRANT_TTL=1h
GRANT_WINDOW=1m
GRANT_WINDOW_MAX=100
PIN_TTL=15m
PIN_LOCKOUT=1m
PIN_MAX_ATTEMPTS=3
PIN_LENGTH=6
PIN_PEPPER=$(openssl rand -base64 32)
PAYMENT_CREATE_RETRY_WINDOW=20h
PAYMENT_CONTINUATION_TTL=30m
PAYMENT_WORK_LEASE_TTL=30s
STRIPE_MODE=disabled
ENV
echo "wrote $HERE/.env"
