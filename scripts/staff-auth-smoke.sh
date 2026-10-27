#!/bin/sh
# Disposable local Keycloak + production BFF wiring smoke. No browser acceptance.
# Run only under orb-test --heavy --host; never use real .env or credentials.
set -eu
smoke_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
smoke_project="tojima-staff-smoke-$$"
smoke_tmp=$(mktemp -d)
smoke_docker_checked=false
compose() {
    docker compose -f "$smoke_root/compose.yaml" --env-file "$smoke_tmp/.env" -p "$smoke_project" --profile staff "$@"
}
cleanup() {
    smoke_status=$?
    trap - EXIT HUP INT TERM
    if ! compose down -v --rmi local --remove-orphans >/dev/null 2>&1; then
        echo 'cleanup: docker compose down failed' >&2
        smoke_status=1
    fi
    smoke_left_containers=$(compose ps -aq 2>/dev/null) || smoke_left_containers=
    smoke_left_volumes=$(docker volume ls -q --filter "label=com.docker.compose.project=$smoke_project" 2>/dev/null) || smoke_left_volumes=
    smoke_left_images=$(docker images -q --filter "reference=${smoke_project}-*" 2>/dev/null) || smoke_left_images=
    for smoke_left in "$smoke_left_containers" "$smoke_left_volumes" "$smoke_left_images"; do
        if [ -n "$smoke_left" ]; then echo "cleanup: resources left behind: $smoke_left" >&2; smoke_status=1; fi
    done
    if [ "$smoke_docker_checked" = true ]; then
        smoke_dangling_after=$(docker volume ls -qf dangling=true) || smoke_status=1
        echo "dangling volumes after: ${smoke_dangling_after:-none}"
    fi
    rm -rf "$smoke_tmp"
    exit "$smoke_status"
}
trap cleanup EXIT HUP INT TERM
fail() { echo "RESULT: FAIL $*" >&2; exit 1; }
for smoke_tool in docker curl python3 openssl; do command -v "$smoke_tool" >/dev/null 2>&1 || fail "$smoke_tool is required"; done
# Environment overrides --env-file. Clear every Compose interpolation input,
# retaining Docker's own connection/context environment.
for smoke_key in POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD KEYCLOAK_DB_PASSWORD APP_BIND APP_PORT \
    SESSION_TTL GRANT_TTL GRANT_WINDOW GRANT_WINDOW_MAX PIN_TTL PIN_LOCKOUT PIN_MAX_ATTEMPTS PIN_LENGTH PIN_PEPPER \
    PAYMENT_CREATE_RETRY_WINDOW PAYMENT_CONTINUATION_TTL PAYMENT_WORK_LEASE_TTL \
    STRIPE_MODE STRIPE_SECRET_KEY STRIPE_WEBHOOK_SECRET STRIPE_ACCOUNT_ID STRIPE_CREDENTIAL_REF STRIPE_API_VERSION \
    STRIPE_SUCCESS_URL STRIPE_CANCEL_URL STRIPE_MAX_AMOUNT_JPY APP_PUBLIC_ORIGIN CHECKOUT_PROOF_TTL CHECKOUT_SIGNING_KEYS \
    STAFF_AUTH_MODE STAFF_OIDC_ISSUER STAFF_OIDC_CLIENT_ID STAFF_OIDC_CLIENT_SECRET STAFF_OIDC_DISCOVERY_URL \
    KEYCLOAK_PORT KEYCLOAK_PUBLIC_URL KC_BOOTSTRAP_ADMIN_USERNAME KC_BOOTSTRAP_ADMIN_PASSWORD TOJIMA_STAFF_CLIENT_SECRET \
    COMPOSE_PROFILES; do unset "$smoke_key"; done
# Reserve two distinct loopback ports while selecting them; Compose binds them
# immediately below (a competing host process can still cause a clean failure).
python3 - <<'PY' >"$smoke_tmp/ports"
import socket
sockets = [socket.socket(), socket.socket()]
for s in sockets:
    s.bind(('127.0.0.1', 0))
    print(s.getsockname()[1])
for s in sockets: s.close()
PY
smoke_app_port=$(sed -n '1p' "$smoke_tmp/ports")
smoke_kc_port=$(sed -n '2p' "$smoke_tmp/ports")
smoke_client_secret=$(openssl rand -hex 48)
umask 077
cat >"$smoke_tmp/.env" <<ENV
POSTGRES_USER=tojima
POSTGRES_DB=tojima
POSTGRES_PASSWORD=$(openssl rand -hex 32)
KEYCLOAK_DB_PASSWORD=$(openssl rand -hex 32)
APP_BIND=127.0.0.1
APP_PORT=$smoke_app_port
KEYCLOAK_PORT=$smoke_kc_port
KEYCLOAK_PUBLIC_URL=http://127.0.0.1:$smoke_kc_port
KC_BOOTSTRAP_ADMIN_USERNAME=smoke-admin
KC_BOOTSTRAP_ADMIN_PASSWORD=$(openssl rand -hex 32)
TOJIMA_STAFF_CLIENT_SECRET=$smoke_client_secret
STAFF_AUTH_MODE=oidc
STAFF_OIDC_ISSUER=http://127.0.0.1:$smoke_kc_port/realms/tojima
STAFF_OIDC_DISCOVERY_URL=http://keycloak:8080/realms/tojima
STAFF_OIDC_CLIENT_ID=tojima-staff
STAFF_OIDC_CLIENT_SECRET=$smoke_client_secret
APP_PUBLIC_ORIGIN=http://127.0.0.1:$smoke_app_port
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
smoke_dangling_before=$(docker volume ls -qf dangling=true) || fail 'cannot check dangling volumes'
smoke_docker_checked=true
echo "dangling volumes before: ${smoke_dangling_before:-none}"
echo "project: $smoke_project"
compose up -d --build --wait --wait-timeout 360 || fail 'docker compose up --wait'
smoke_kc_url="http://127.0.0.1:$smoke_kc_port"
smoke_app_url="http://127.0.0.1:$smoke_app_port"
curl --fail --silent --show-error --max-time 10 "$smoke_kc_url/realms/tojima/.well-known/openid-configuration" >"$smoke_tmp/discovery.json" || fail discovery
python3 - "$smoke_tmp/discovery.json" <<'PY' || fail 'strong ACR discovery'
import json, sys
with open(sys.argv[1]) as f: d = json.load(f)
assert 'strong' in d.get('acr_values_supported', []), 'strong ACR missing'
PY
# Keycloak can advertise offline_access globally. Verify effective assigned
# default AND optional scopes on this client using the bootstrap admin instead.
# Keep the password/token in private files/stdin, never argv or stdout.
python3 - "$smoke_tmp/.env" <<'PY' >"$smoke_tmp/admin-form"
import sys, urllib.parse
with open(sys.argv[1]) as f: env = dict(line.rstrip('\n').split('=',1) for line in f if '=' in line)
print(urllib.parse.urlencode({'client_id':'admin-cli','grant_type':'password','username':env['KC_BOOTSTRAP_ADMIN_USERNAME'],'password':env['KC_BOOTSTRAP_ADMIN_PASSWORD']}),end='')
PY
curl --fail --silent --show-error --max-time 10 --data-binary @"$smoke_tmp/admin-form" "$smoke_kc_url/realms/master/protocol/openid-connect/token" >"$smoke_tmp/admin-token.json" || fail 'bootstrap token'
python3 - "$smoke_tmp/admin-token.json" <<'PY' >"$smoke_tmp/admin-header"
import json, sys
with open(sys.argv[1]) as f: print('Authorization: Bearer '+json.load(f)['access_token'])
PY
curl --fail --silent --show-error --max-time 10 -H @"$smoke_tmp/admin-header" "$smoke_kc_url/admin/realms/tojima/clients?clientId=tojima-staff" >"$smoke_tmp/clients.json" || fail 'client lookup'
smoke_client_id=$(python3 - "$smoke_tmp/clients.json" <<'PY'
import json, sys, uuid
with open(sys.argv[1]) as f: clients = json.load(f)
assert len(clients) == 1 and clients[0]['clientId'] == 'tojima-staff'
print(uuid.UUID(clients[0]['id']))
PY
) || fail 'client identity'
for smoke_scope_kind in default-client-scopes optional-client-scopes; do
    curl --fail --silent --show-error --max-time 10 -H @"$smoke_tmp/admin-header" \
        "$smoke_kc_url/admin/realms/tojima/clients/$smoke_client_id/$smoke_scope_kind" >"$smoke_tmp/scopes.json" || fail 'client scope lookup'
    python3 - "$smoke_tmp/scopes.json" <<'PY' || fail 'offline client scope present'
import json, sys
with open(sys.argv[1]) as f: assert all(s['name'] != 'offline_access' for s in json.load(f))
PY
done
curl --fail --silent --show-error --max-time 10 -H @"$smoke_tmp/admin-header" \
    "$smoke_kc_url/admin/realms/tojima/roles/default-roles-tojima/composites" >"$smoke_tmp/default-roles.json" || fail 'default role lookup'
python3 - "$smoke_tmp/default-roles.json" <<'PYROLES' || fail 'unsafe default role composite'
import json, sys
with open(sys.argv[1]) as f: roles = json.load(f)
assert roles == [], 'default role must have no composites: ' + ', '.join(sorted(r.get('name', '?') for r in roles))
PYROLES
echo 'Keycloak: strong ACR advertised; no offline_access client scope'
# BFF liveness is independent of discovery; allow its bounded retry to finish.
smoke_attempt=0
while :; do
    smoke_status=$(curl --silent --show-error --max-time 10 -o /dev/null -D "$smoke_tmp/login-headers" -w '%{http_code}' "$smoke_app_url/staff/auth/login?return=/staff/") || fail 'login request'
    [ "$smoke_status" = 302 ] && break
    [ "$smoke_status" = 503 ] || fail "login status $smoke_status"
    smoke_attempt=$((smoke_attempt + 1))
    [ "$smoke_attempt" -lt 40 ] || fail 'discovery retry deadline'
    sleep 1
done
python3 - "$smoke_tmp/login-headers" "$smoke_kc_url" <<'PY' || fail 'PKCE/login redirect'
import sys, urllib.parse
with open(sys.argv[1]) as f: headers = dict(line.strip().split(':',1) for line in f if ':' in line)
location = next(v.strip() for k,v in headers.items() if k.lower() == 'location')
u = urllib.parse.urlsplit(location)
assert urllib.parse.urlunsplit((u.scheme,u.netloc,u.path,'','')) == sys.argv[2]+'/realms/tojima/protocol/openid-connect/auth'
q = urllib.parse.parse_qs(u.query)
for k,v in {'code_challenge_method':'S256','prompt':'login','acr_values':'basic'}.items(): assert q.get(k) == [v], k
assert q.get('code_challenge'), 'PKCE challenge missing'
PY
smoke_staff_path=/v1/staff/tenants/x/stores/y/operations/access
smoke_status=$(curl --silent --show-error --max-time 10 -o /dev/null -w '%{http_code}' "$smoke_app_url$smoke_staff_path") || fail 'anonymous staff request'
[ "$smoke_status" = 403 ] || fail "anonymous staff status $smoke_status"
smoke_status=$(curl --silent --show-error --max-time 10 -o /dev/null -w '%{http_code}' -X POST -H 'Origin: https://wrong.example.invalid' "$smoke_app_url$smoke_staff_path") || fail 'Origin request'
[ "$smoke_status" = 403 ] || fail "wrong Origin status $smoke_status"
echo 'BFF: PKCE redirect, anonymous staff denial and Origin denial verified'
echo 'RESULT: PASS'
