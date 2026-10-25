#!/bin/sh
# Container smoke test: build the compose stack, prove /healthz and that the
# migrations ran, then remove every container, volume and image it created.
# Runs on the host (Docker engine) under `orb-test --heavy --host`.
set -eu

smoke_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
smoke_project="tojima-smoke-$$"
smoke_password="smoke-$$-local-only"
smoke_user="tojima"
smoke_db="tojima"

# Compose interpolates these from the environment; --env-file /dev/null keeps a
# developer's real .env out of the smoke run. APP_BIND is forced to loopback so
# an exported APP_BIND=0.0.0.0 in the caller's shell cannot expose the server.
POSTGRES_PASSWORD="$smoke_password"
POSTGRES_USER="$smoke_user"
POSTGRES_DB="$smoke_db"
APP_BIND=127.0.0.1
APP_PORT=0
export POSTGRES_PASSWORD POSTGRES_USER POSTGRES_DB APP_BIND APP_PORT

compose() {
    docker compose --env-file /dev/null -p "$smoke_project" "$@"
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
echo "RESULT: PASS"
