#!/bin/sh
# Container smoke test: build the compose stack, prove /healthz, the guest API and that the
# migrations ran, then remove every container, volume and image it created.
# Runs on the host (Docker engine) under `orb-test --heavy --host`.
set -eu

smoke_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
smoke_project="tojima-smoke-$$"
smoke_password="smoke-$$-local-only"
smoke_user="tojima"
smoke_db="tojima"
smoke_app_user="tojima_app"
smoke_app_password="smoke-app-$$-local-only"

# Compose interpolates these from the environment; --env-file /dev/null keeps a
# developer's real .env out of the smoke run. APP_BIND is forced to loopback so
# an exported APP_BIND=0.0.0.0 in the caller's shell cannot expose the server.
POSTGRES_PASSWORD="$smoke_password"
POSTGRES_USER="$smoke_user"
POSTGRES_DB="$smoke_db"
APP_DB_USER="$smoke_app_user"
APP_DB_PASSWORD="$smoke_app_password"
APP_BIND=127.0.0.1
APP_PORT=0
export POSTGRES_PASSWORD POSTGRES_USER POSTGRES_DB APP_DB_USER APP_DB_PASSWORD APP_BIND APP_PORT
# Synthetic local policy only, never read payment credentials from the caller.
SESSION_TTL=4h GRANT_TTL=1h GRANT_WINDOW=1m GRANT_WINDOW_MAX=100
PIN_TTL=15m PIN_LOCKOUT=1m PIN_MAX_ATTEMPTS=3 PIN_LENGTH=6
PIN_PEPPER=c21va2Utb25seS1zeW50aGV0aWMtcGVwcGVyLTMya2V5ISE=
PAYMENT_CREATE_RETRY_WINDOW=20h PAYMENT_CONTINUATION_TTL=30m PAYMENT_WORK_LEASE_TTL=30s
STAFF_AUTH_MODE=disabled
STRIPE_MODE=disabled
export SESSION_TTL GRANT_TTL GRANT_WINDOW GRANT_WINDOW_MAX PIN_TTL PIN_LOCKOUT
export PIN_MAX_ATTEMPTS PIN_LENGTH PIN_PEPPER PAYMENT_CREATE_RETRY_WINDOW
export PAYMENT_CONTINUATION_TTL PAYMENT_WORK_LEASE_TTL STRIPE_MODE STAFF_AUTH_MODE
unset STRIPE_SECRET_KEY STRIPE_WEBHOOK_SECRET STRIPE_ACCOUNT_ID STRIPE_CREDENTIAL_REF
unset STRIPE_API_VERSION STRIPE_SUCCESS_URL STRIPE_CANCEL_URL STRIPE_MAX_AMOUNT_JPY
unset APP_PUBLIC_ORIGIN CHECKOUT_PROOF_TTL CHECKOUT_SIGNING_KEYS
unset STAFF_OIDC_ISSUER STAFF_OIDC_CLIENT_ID STAFF_OIDC_CLIENT_SECRET STAFF_OIDC_DISCOVERY_URL
unset KEYCLOAK_DB_PASSWORD KEYCLOAK_PUBLIC_URL KEYCLOAK_PORT
unset KC_BOOTSTRAP_ADMIN_USERNAME KC_BOOTSTRAP_ADMIN_PASSWORD TOJIMA_STAFF_CLIENT_SECRET COMPOSE_PROFILES
smoke_docker_checked=false

compose() {
    docker compose -f "$smoke_root/compose.yaml" --env-file /dev/null -p "$smoke_project" "$@"
}

cleanup() {
    smoke_status=$?
    if ! compose down -v --rmi local --remove-orphans >/dev/null 2>&1; then
        echo "cleanup: docker compose down failed" >&2
        smoke_status=1
    fi
    smoke_left_containers=$(compose ps -aq 2>/dev/null) || smoke_left_containers=
    smoke_left_volumes=$(docker volume ls -q \
        --filter "label=com.docker.compose.project=$smoke_project" 2>/dev/null) || smoke_left_volumes=
    smoke_left_images=$(docker images -q --filter "reference=${smoke_project}-*" 2>/dev/null) || smoke_left_images=
    for smoke_left in "$smoke_left_containers" "$smoke_left_volumes" "$smoke_left_images"; do
        if [ -n "$smoke_left" ]; then
            echo "cleanup: resources left behind: $smoke_left" >&2
            smoke_status=1
        fi
    done
    if [ "$smoke_docker_checked" = true ]; then
        smoke_dangling_after=$(docker volume ls -qf dangling=true) || smoke_status=1
        echo "dangling volumes after: ${smoke_dangling_after:-none}"
    fi
    exit "$smoke_status"
}
trap cleanup EXIT HUP INT TERM

fail() {
    echo "RESULT: FAIL $*" >&2
    exit 1
}

cd "$smoke_root"
command -v docker >/dev/null 2>&1 || fail "docker is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"

smoke_dangling_before=$(docker volume ls -qf dangling=true) || fail "cannot check dangling volumes"
smoke_docker_checked=true
echo "dangling volumes before: ${smoke_dangling_before:-none}"
echo "project: $smoke_project"

compose up -d --build --wait || fail "docker compose up --wait"

smoke_addr=$(compose port server 8080) || fail "could not resolve the published server port"
case "$smoke_addr" in
    ''|*[!0-9.:\[\]]*) fail "invalid published address: $smoke_addr" ;;
esac

smoke_health=$(curl --fail --silent --show-error --max-time 5 "http://$smoke_addr/healthz") \
    || fail "GET http://$smoke_addr/healthz"
[ "$smoke_health" = '{"status":"ok"}' ] || fail "unexpected health body: $smoke_health"
echo "healthz: $smoke_addr -> $smoke_health"

smoke_migrate=$(compose ps -aq migrate) || fail "migrate container not found"
[ -n "$smoke_migrate" ] || fail "migrate container not found"
smoke_migrate_state=$(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$smoke_migrate")
[ "$smoke_migrate_state" = "exited 0" ] || fail "migrate container state: $smoke_migrate_state"

smoke_expected=$(ls migrations/postgres/*.up.sql | wc -l | tr -d ' ')
smoke_applied=$(compose exec -T postgres psql -X -U "$smoke_user" -d "$smoke_db" -At \
    -c "SELECT count(*) FROM schema_migrations") || fail "schema_migrations query"
[ "$smoke_applied" = "$smoke_expected" ] \
    || fail "schema_migrations rows: $smoke_applied, expected $smoke_expected"

smoke_table=$(compose exec -T postgres psql -X -U "$smoke_user" -d "$smoke_db" -At \
    -c "SELECT to_regclass('public.table_sessions') IS NOT NULL") || fail "table_sessions query"
[ "$smoke_table" = "t" ] || fail "table_sessions was not created"

echo "migrations: $smoke_applied rows, table_sessions present"

# Least privilege: the server role must hold no elevated attribute and must not
# be able to run DDL. Migrations stay on the owner role.
smoke_app_elevated=$(compose exec -T postgres psql -X -U "$smoke_user" -d "$smoke_db" -At \
    -c "SELECT rolsuper OR rolcreaterole OR rolcreatedb FROM pg_roles WHERE rolname='$smoke_app_user'") \
    || fail "runtime role attributes query"
[ "$smoke_app_elevated" = "f" ] \
    || fail "runtime role $smoke_app_user is superuser/createrole/createdb: $smoke_app_elevated"
# The probe must fail for lack of privilege, not because the role cannot connect.
if smoke_ddl=$(compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U "$smoke_app_user" -d "$smoke_db" \
    -c 'CREATE TABLE smoke_ddl_probe(i int)' 2>&1); then
    fail "runtime role $smoke_app_user created a table (DDL allowed)"
fi
printf '%s\n' "$smoke_ddl" | grep -q 'permission denied for schema' \
    || fail "runtime role DDL probe failed for another reason"
echo "runtime role: $smoke_app_user is not elevated and cannot create tables"

smoke_staffadmin=$(compose run --rm --no-deps --entrypoint /staffadmin server status --subject nobody) \
    || fail "staffadmin status"
printf '%s\n' "$smoke_staffadmin" | grep -qx 'Exists: false' || fail "staffadmin missing-subject status"
echo 'staffadmin: missing subject reports Exists: false'
# Seed only the disposable compose DB, through SQL rather than staff fixtures.
# Reading this known table proves the API used the migrated DB, not a 404 stub.
compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U "$smoke_user" -d "$smoke_db" >/dev/null <<'SQL' || fail "seed guest table"
INSERT INTO tenants(tenant_id,name) VALUES ('compose-smoke-tenant','Compose smoke');
INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode,guest_display_name)
VALUES ('compose-smoke-tenant','compose-smoke-store','Compose smoke','DIRECT_SCAN','Compose smoke store');
INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator)
VALUES ('compose-smoke-tenant','compose-smoke-store','compose-smoke-table','Smoke table','compose-smoke-locator');
SQL
smoke_guest=$(curl --fail --silent --show-error --max-time 5 \
    "http://$smoke_addr/v1/guest/tables/compose-smoke-locator") || fail "guest table API"
[ "$smoke_guest" = '{"store_display_name":"Compose smoke store","mode":"DIRECT_SCAN","open":false}' ] \
    || fail "unexpected guest table response: $smoke_guest"
echo "guest API: migrated table read succeeded"
echo "RESULT: PASS"
