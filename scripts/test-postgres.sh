#!/bin/sh
# Disposable local PostgreSQL for P-76 tests; never uses a caller's database URL.
set -eu

pg_test_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
pg_test_container="tojima-p76-test-$$"
pg_test_image="postgres:17"

cleanup() {
    docker rm -f -v "$pg_test_container" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

docker run --detach --rm --name "$pg_test_container" \
    --env POSTGRES_HOST_AUTH_METHOD=trust --env POSTGRES_DB=tojima_test \
    --publish 127.0.0.1::5432 "$pg_test_image" >/dev/null

pg_test_ready=0
pg_test_attempt=0
while [ "$pg_test_attempt" -lt 30 ]; do
    if docker exec "$pg_test_container" pg_isready -h 127.0.0.1 -U postgres -d tojima_test >/dev/null 2>&1; then
        pg_test_ready=1
        break
    fi
    pg_test_attempt=$((pg_test_attempt + 1))
    sleep 1
done
if [ "$pg_test_ready" -ne 1 ]; then
    echo "Disposable PostgreSQL did not become ready" >&2
    exit 1
fi

pg_test_port=$(docker port "$pg_test_container" 5432/tcp | sed 's/^127\.0\.0\.1://')
case "$pg_test_port" in
    ''|*[!0-9]*) echo "Invalid disposable PostgreSQL port" >&2; exit 1 ;;
esac

cd "$pg_test_root"
TEST_DATABASE_URL="postgres://postgres@127.0.0.1:$pg_test_port/tojima_test?sslmode=disable" \
    make ci
