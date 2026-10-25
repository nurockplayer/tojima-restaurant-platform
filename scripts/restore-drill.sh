#!/bin/sh
# Scripted, timed local restore drill for P-84 8n (local part only).
# Uses two disposable postgres:17 containers; never touches business data,
# hosting backups or production configuration.
set -eu

drill_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
drill_prefix="tojima-restore-drill-$$"
drill_source="$drill_prefix-source"
drill_target="$drill_prefix-target"
drill_image="postgres:17"
drill_dir=$(mktemp -d "${TMPDIR:-/tmp}/tojima-restore-drill.XXXXXX")
drill_dump="$drill_dir/tojima.dump"
drill_helper="$drill_dir/migrate.go"

now_ms() { python3 -c 'import time; print(int(time.time() * 1000))'; }
fail() { echo "RESULT: FAIL $*" >&2; exit 1; }

cleanup() {
    docker rm -f -v "$drill_source" "$drill_target" >/dev/null 2>&1 || true
    rm -rf "$drill_dir"
}
trap cleanup EXIT HUP INT TERM

start_container() {
    drill_name=$1
    shift
    docker run --detach --rm --name "$drill_name" \
        --memory 1g --cpus 1 \
        --env POSTGRES_HOST_AUTH_METHOD=trust --env POSTGRES_DB=tojima_test \
        "$@" "$drill_image" >/dev/null
}

wait_ready() {
    drill_attempt=0
    while [ "$drill_attempt" -lt 60 ]; do
        if docker exec "$1" pg_isready -h 127.0.0.1 -U postgres -d tojima_test >/dev/null 2>&1; then
            return 0
        fi
        drill_attempt=$((drill_attempt + 1))
        sleep 1
    done
    fail "disposable PostgreSQL $1 did not become ready"
}

drill_start=$(now_ms)
start_container "$drill_source" --publish 127.0.0.1::5432
wait_ready "$drill_source"

drill_port=$(docker port "$drill_source" 5432/tcp | sed 's/^127\.0\.0\.1://')
case "$drill_port" in
    ''|*[!0-9]*) fail "invalid disposable PostgreSQL port" ;;
esac
drill_url="postgres://postgres@127.0.0.1:$drill_port/tojima_test?sslmode=disable"

# The repository has no migration binary; reuse the real exported runner the
# same way docs/deploy-runbook.md does, from a temporary helper outside the repo.
cat > "$drill_helper" <<'GO'
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func main() {
	dsn := flag.String("dsn", "", "disposable loopback database URL")
	schema := flag.String("schema", "", "existing disposable schema")
	flag.Parse()
	cfg, err := pgx.ParseConfig(*dsn)
	if err != nil || *dsn == "" || *schema == "" {
		panic("explicit test DSN/schema required")
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		panic("loopback only")
	}
	cfg.RuntimeParams["search_path"] = pgx.Identifier{*schema}.Sanitize()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err == nil {
		defer conn.Close(context.Background())
		err = postgres.Up(ctx, conn)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "migration drill failed; inspect locally")
		os.Exit(1)
	}
}
GO

cd "$drill_root"
go run "$drill_helper" -dsn "$drill_url" -schema public || fail "migrations did not apply"

# Synthetic rows only, marked as such; no business data.
docker exec -i "$drill_source" psql -X -U postgres -d tojima_test -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
INSERT INTO tenants(tenant_id, name)
VALUES ('drill-tenant', 'Restore drill synthetic tenant');
INSERT INTO stores(tenant_id, store_id, name, qr_entry_mode, guest_display_name)
VALUES ('drill-tenant', 'drill-store', 'Restore drill synthetic store', 'STAFF_OPEN', 'Drill store');
INSERT INTO dining_tables(tenant_id, store_id, table_id, name, public_locator)
VALUES ('drill-tenant', 'drill-store', 'drill-table', 'Drill table', 'drill-locator');
INSERT INTO table_sessions(session_id, tenant_id, store_id, table_id, status, qr_mode, expires_at)
VALUES ('drill-session', 'drill-tenant', 'drill-store', 'drill-table', 'OPEN', 'STAFF_OPEN', now() + interval '1 hour');
INSERT INTO table_carts(session_id) VALUES ('drill-session');
INSERT INTO table_cart_lines(session_id, generation, line_id, product_key, product_name, quantity)
VALUES ('drill-session', 1, '00000000000000000000000000000001', 'drill-product', 'Synthetic product', 2);

INSERT INTO menu_catalogs(tenant_id, owner_kind) VALUES ('drill-tenant', 'COMPANY') RETURNING catalog_id AS cid \gset
INSERT INTO menu_catalog_revisions(catalog_id, revision, draft_version, published_by)
VALUES (:'cid', 1, 1, 'drill-synthetic');
INSERT INTO menu_revision_categories(catalog_id, revision, category_key, name, position)
VALUES (:'cid', 1, 'drill-category', 'Synthetic category', 0);
INSERT INTO menu_revision_products(catalog_id, revision, product_key, category_key, name, base_price_minor, position, active)
VALUES (:'cid', 1, 'drill-product', 'drill-category', 'Synthetic product', 500, 0, true);
UPDATE menu_catalog_revisions SET sealed = true WHERE catalog_id = :'cid' AND revision = 1;
UPDATE menu_catalogs SET current_revision = 1 WHERE catalog_id = :'cid';

INSERT INTO table_orders(order_id, session_id, tenant_id, store_id, table_id, generation,
    payment_method, requires_staff_acceptance, catalog_id, catalog_mode, catalog_revision,
    quote_snapshot, tax_snapshot, total_gross_jpy)
VALUES ('00000000000000000000000000000002', 'drill-session', 'drill-tenant', 'drill-store',
    'drill-table', 1, 'CASH', false, :'cid', 'LINKED', 1,
    '{"source":"drill-synthetic"}', '{"source":"drill-synthetic"}', 1000);
SQL

dump_start=$(now_ms)
docker exec "$drill_source" pg_dump -U postgres -d tojima_test -Fc > "$drill_dump" || fail "pg_dump failed"
dump_ms=$(( $(now_ms) - dump_start ))

start_container "$drill_target"
wait_ready "$drill_target"

restore_start=$(now_ms)
docker exec -i "$drill_target" pg_restore -U postgres -d tojima_test \
    --exit-on-error --no-owner --no-privileges < "$drill_dump" || fail "pg_restore failed"
restore_ms=$(( $(now_ms) - restore_start ))

verify_start=$(now_ms)

# (a) Schema-only dumps must match. PG 17 plain dumps carry random
# \restrict/\unrestrict tokens, which are not schema.
docker exec "$drill_source" pg_dump -U postgres -d tojima_test --schema-only > "$drill_dir/source-schema.sql" \
    || fail "source schema-only dump failed"
docker exec "$drill_target" pg_dump -U postgres -d tojima_test --schema-only > "$drill_dir/target-schema.sql" \
    || fail "restored schema-only dump failed"
for drill_side in source target; do
    grep -v -e '^\\restrict ' -e '^\\unrestrict ' "$drill_dir/$drill_side-schema.sql" \
        > "$drill_dir/$drill_side-schema.filtered.sql"
done
cmp -s "$drill_dir/source-schema.filtered.sql" "$drill_dir/target-schema.filtered.sql" \
    || fail "schema-only dumps differ"

# (b) Migration ledger rows must match, including applied_at.
drill_ledger_sql="SELECT version || '|' || extract(epoch FROM applied_at)::text FROM schema_migrations ORDER BY version"
docker exec "$drill_source" psql -At -U postgres -d tojima_test -c "$drill_ledger_sql" > "$drill_dir/source-ledger.txt" \
    || fail "source ledger query failed"
docker exec "$drill_target" psql -At -U postgres -d tojima_test -c "$drill_ledger_sql" > "$drill_dir/target-ledger.txt" \
    || fail "restored ledger query failed"
cmp -s "$drill_dir/source-ledger.txt" "$drill_dir/target-ledger.txt" || fail "migration ledger differs"
drill_ledger_rows=$(wc -l < "$drill_dir/source-ledger.txt" | tr -d ' ')

# (c) Every table in the app schema: row count and order-independent checksum.
drill_tables=$(docker exec "$drill_source" psql -At -U postgres -d tojima_test \
    -c "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename") \
    || fail "could not list tables"
[ -n "$drill_tables" ] || fail "no tables in the public schema"
drill_table_count=0
for drill_table in $drill_tables; do
    drill_sum_sql="SELECT count(*) || '|' || coalesce(md5(string_agg(t::text, '' ORDER BY t::text)), 'empty') FROM public.\"$drill_table\" t"
    drill_source_sum=$(docker exec "$drill_source" psql -At -U postgres -d tojima_test -c "$drill_sum_sql") \
        || fail "checksum query failed for $drill_table on source"
    drill_target_sum=$(docker exec "$drill_target" psql -At -U postgres -d tojima_test -c "$drill_sum_sql") \
        || fail "checksum query failed for $drill_table on restored copy"
    [ "$drill_source_sum" = "$drill_target_sum" ] \
        || fail "table $drill_table differs (source $drill_source_sum, restored $drill_target_sum)"
    drill_table_count=$((drill_table_count + 1))
done

# (d) Sequence state must match; a restored sequence that lost its progress
# would still pass the table checksums and only fail on the next insert.
drill_seq_sql="SELECT schemaname || '.' || sequencename || '|' || coalesce(last_value::text, 'null') FROM pg_sequences ORDER BY 1"
docker exec "$drill_source" psql -At -U postgres -d tojima_test -c "$drill_seq_sql" > "$drill_dir/source-sequences.txt" \
    || fail "source sequence query failed"
docker exec "$drill_target" psql -At -U postgres -d tojima_test -c "$drill_seq_sql" > "$drill_dir/target-sequences.txt" \
    || fail "restored sequence query failed"
cmp -s "$drill_dir/source-sequences.txt" "$drill_dir/target-sequences.txt" || fail "sequences differ"
drill_seq_count=$(wc -l < "$drill_dir/source-sequences.txt" | tr -d ' ')

verify_ms=$(( $(now_ms) - verify_start ))
total_ms=$(( $(now_ms) - drill_start ))
dump_bytes=$(wc -c < "$drill_dump" | tr -d ' ')

echo "ledger rows: $drill_ledger_rows"
echo "tables verified: $drill_table_count"
echo "sequences verified: $drill_seq_count"
echo "dump size: $dump_bytes bytes"
echo "dump: ${dump_ms} ms"
echo "restore: ${restore_ms} ms"
echo "verify: ${verify_ms} ms"
echo "total: ${total_ms} ms"
echo "RESULT: PASS"
