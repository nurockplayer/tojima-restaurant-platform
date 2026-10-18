#!/bin/sh

# Owner-operated temporary public walkthrough. This script never provisions tools.
set -eu
set +x
set +a
unset gate_secret secret_value secret_name fifo saved_home home_was_set cloudflared_path \
  saved_database_url saved_gate_secret saved_public_origin env_names env_name

repo_dir=$(CDPATH='' cd "$(dirname "$0")/.." && pwd -P)
cd "$repo_dir"

die() {
  printf '%s\n' "remote demo: $1" >&2
  exit 1
}

[ -n "${CLOUDFLARE_ACCOUNT_ID:-}" ] || die "CLOUDFLARE_ACCOUNT_ID must be set to the target Cloudflare account ID (non-secret) to avoid interactive account selection"

command -v python3 >/dev/null 2>&1 || die "Python 3 is required"
command -v go >/dev/null 2>&1 || die "Go is required"
command -v pnpm >/dev/null 2>&1 || die "pnpm is required"
command -v openssl >/dev/null 2>&1 || die "OpenSSL is required"
command -v cloudflared >/dev/null 2>&1 || die "cloudflared is required"
command -v wrangler >/dev/null 2>&1 || die "Wrangler is required"
cloudflared_path=$(command -v cloudflared)
saved_home=${HOME-}
home_was_set=${HOME+x}

if [ "${TOJIMA_DEMO_GATE_SECRET+x}" = x ]; then
  unset TOJIMA_DEMO_GATE_SECRET
fi

if ! python3 - <<'PY'
import ipaddress
import os
import sys
from urllib.parse import parse_qsl, urlsplit

raw = os.environ.get("TEST_DATABASE_URL", "")
try:
    parsed = urlsplit(raw)
    host = parsed.hostname
    if parsed.scheme not in ("postgres", "postgresql") or not host:
        raise ValueError
    parsed.port  # Reject malformed or out-of-range ports.
    if not ipaddress.ip_address(host).is_loopback:
        raise ValueError
    if parsed.port == 0:
        raise ValueError
    if parsed.fragment or parsed.username is None or parsed.path in ("", "/"):
        raise ValueError
    if "," in host:
        raise ValueError
    query = parse_qsl(parsed.query, keep_blank_values=True)
    allowed_query = {"sslmode"}
    if any(key.lower() not in allowed_query for key, _ in query):
        raise ValueError
    if len(query) != len({key.lower() for key, _ in query}):
        raise ValueError
except (ValueError, TypeError):
    print("remote demo: TEST_DATABASE_URL must name one literal loopback PostgreSQL host and use only supported query parameters", file=sys.stderr)
    sys.exit(1)

origin = os.environ.get("TOJIMA_DEMO_PUBLIC_ORIGIN", "https://tojima-mobile-order-demo.pages.dev")
try:
    public = urlsplit(origin)
    if (public.scheme != "https" or not public.hostname or public.username is not None or
        public.password is not None or public.path or public.query or public.fragment or
        public.geturl() != origin or any(c.isspace() for c in origin)):
        raise ValueError
except ValueError:
    print("remote demo: TOJIMA_DEMO_PUBLIC_ORIGIN must be an exact https origin", file=sys.stderr)
    sys.exit(1)
PY
then
  exit 1
fi

# libpq/pgx recognize these as connection-string defaults; none may redirect
# the explicitly supplied TEST_DATABASE_URL or silently supply credentials.
for pg_name in $(env | sed -n 's/^\(PG[A-Z0-9_]*\)=.*/\1/p'); do
  unset "$pg_name"
done

public_origin=${TOJIMA_DEMO_PUBLIC_ORIGIN:-https://tojima-mobile-order-demo.pages.dev}
tmpdir=$(mktemp -d /tmp/tojima-remote-demo.XXXXXX) || die "cannot create a private temporary directory"
chmod 700 "$tmpdir" || die "cannot protect the temporary directory"

operationstest_pid=
gateway_pid=
cloudflared_pid=
caffeinate_pid=
transient_pid=
cleanup() {
  trap - 0 INT TERM
  unset gate_secret secret_value
  for child_pid in "$transient_pid" "$cloudflared_pid" "$gateway_pid" "$operationstest_pid" "$caffeinate_pid"; do
    if [ -n "$child_pid" ]; then
      kill -TERM "$child_pid" 2>/dev/null || :
    fi
  done
  for child_pid in "$transient_pid" "$cloudflared_pid" "$gateway_pid" "$operationstest_pid" "$caffeinate_pid"; do
    if [ -n "$child_pid" ]; then
      wait "$child_pid" 2>/dev/null || :
    fi
  done
  rm -rf "$tmpdir"
}
trap cleanup 0
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf '%s\n' "remote demo: $1" >&2
  exit 1
}

check_long_lived_children() {
  for child_pid in "$operationstest_pid" "$gateway_pid" "$cloudflared_pid"; do
    if [ -n "$child_pid" ] && ! kill -0 "$child_pid" 2>/dev/null; then
      fail "a demo child process exited; stopping the full demo"
    fi
  done
}

finish_transient() {
  if wait "$transient_pid"; then
    status=0
  else
    status=$?
  fi
  transient_pid=
  return "$status"
}

run_transient() {
  env -u TEST_DATABASE_URL "$@" >/dev/null 2>&1 &
  transient_pid=$!
  finish_transient
}

printf '%s\n' "Building the demo web app and isolated Go runtimes..."
if ! run_transient pnpm --dir web build; then fail "web build failed"; fi
if ! run_transient go build -o "$tmpdir/operationstest" ./cmd/operationstest; then fail "operationstest build failed"; fi
if ! run_transient go build -o "$tmpdir/demogateway" ./cmd/demogateway; then fail "demo gateway build failed"; fi

credentials_file=$tmpdir/credentials.json
(
  saved_database_url=$TEST_DATABASE_URL
  env_names=$(/usr/bin/env | /usr/bin/sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p')
  for env_name in $env_names; do
    unset "$env_name"
  done
  TEST_DATABASE_URL=$saved_database_url
  export TEST_DATABASE_URL
  exec "$tmpdir/operationstest" -demo-shironagasukujira \
    -credentials "$credentials_file" -web-dist "$repo_dir/web/dist"
) >"$tmpdir/operationstest.log" 2>/dev/null &
operationstest_pid=$!

ready=0
attempt=0
while [ "$attempt" -lt 60 ]; do
  check_long_lived_children
  if ! kill -0 "$operationstest_pid" 2>/dev/null; then
    fail "isolated runtime exited before becoming ready; private logs were discarded"
  fi
  if grep -Fq 'isolated operations test runtime listening on http://127.0.0.1:8082' "$tmpdir/operationstest.log"; then
    ready=1
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
[ "$ready" -eq 1 ] || fail "isolated runtime did not become ready within 60 seconds"
if ! env -u TEST_DATABASE_URL python3 - "$credentials_file" <<'PY'
import os
import stat
import sys

try:
    info = os.stat(sys.argv[1], follow_symlinks=False)
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600:
        raise RuntimeError
except Exception:
    print("remote demo: synthetic credentials file is missing or not mode 0600", file=sys.stderr)
    sys.exit(1)
PY
then
  fail "synthetic credentials file is missing or not mode 0600"
fi
if ! env -u TEST_DATABASE_URL python3 - <<'PY'
import http.client
import sys

try:
    connection = http.client.HTTPConnection("127.0.0.1", 8082, timeout=2)
    connection.request("GET", "/healthz")
    response = connection.getresponse()
    response.read()
    if response.status != 200:
        raise RuntimeError
except Exception:
    print("remote demo: isolated runtime readiness check failed", file=sys.stderr)
    sys.exit(1)
PY
then
  fail "isolated runtime readiness check failed"
fi

unset gate_secret secret_value secret_name fifo
gate_secret=$(env -u TEST_DATABASE_URL openssl rand -hex 32) || fail "could not create the per-run gateway secret"
if [ "${#gate_secret}" -ne 64 ]; then
  fail "OpenSSL returned an unexpected secret length"
fi
(
  saved_gate_secret=$gate_secret
  saved_public_origin=$public_origin
  env_names=$(/usr/bin/env | /usr/bin/sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p')
  for env_name in $env_names; do
    unset "$env_name"
  done
  TOJIMA_DEMO_GATE_SECRET=$saved_gate_secret
  TOJIMA_DEMO_PUBLIC_ORIGIN=$saved_public_origin
  export TOJIMA_DEMO_GATE_SECRET TOJIMA_DEMO_PUBLIC_ORIGIN
  exec "$tmpdir/demogateway" -listen 127.0.0.1:8090 \
    -upstream http://127.0.0.1:8082 -public-origin "$saved_public_origin"
) >"$tmpdir/gateway.log" 2>&1 &
gateway_pid=$!

ready=0
attempt=0
while [ "$attempt" -lt 30 ]; do
  check_long_lived_children
  if ! kill -0 "$gateway_pid" 2>/dev/null; then
    fail "demo gateway exited before becoming ready"
  fi
  if grep -Fq 'demo gateway listening on loopback' "$tmpdir/gateway.log"; then
    ready=1
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
[ "$ready" -eq 1 ] || fail "demo gateway did not become ready within 30 seconds"

if command -v caffeinate >/dev/null 2>&1; then
  caffeinate_path=$(command -v caffeinate)
  (exec env -u TEST_DATABASE_URL "$caffeinate_path" -i) >/dev/null 2>&1 &
  caffeinate_pid=$!
fi

# Explicitly bypass user/system tunnel config, which can disable Quick Tunnels.
: >"$tmpdir/cloudflared-empty.yaml" || fail "cannot create an empty tunnel config"
(
  env_names=$(/usr/bin/env | /usr/bin/sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p')
  for env_name in $env_names; do
    unset "$env_name"
  done
  if [ "$home_was_set" = x ]; then
    HOME=$saved_home
    export HOME
  fi
  exec "$cloudflared_path" tunnel --config "$tmpdir/cloudflared-empty.yaml" --no-autoupdate --url http://127.0.0.1:8090
) >"$tmpdir/cloudflared.log" 2>&1 &
cloudflared_pid=$!
tunnel_origin=
attempt=0
while [ "$attempt" -lt 90 ]; do
  check_long_lived_children
  if ! kill -0 "$cloudflared_pid" 2>/dev/null; then
    fail "cloudflared exited before publishing a temporary URL"
  fi
  tunnel_origin=$(env -u TEST_DATABASE_URL python3 - "$tmpdir/cloudflared.log" <<'PY'
import re
import sys
from pathlib import Path

log = Path(sys.argv[1]).read_text(errors="replace")
matches = set(re.findall(r"https://[a-z0-9-]+\.trycloudflare\.com", log))
if len(matches) == 1:
    print(matches.pop())
PY
)
  if [ -n "$tunnel_origin" ]; then
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done
[ -n "$tunnel_origin" ] || fail "no strict trycloudflare.com origin appeared within 90 seconds"

printf '%s\n' "Temporary demo URL: $tunnel_origin"
printf '%s\n' "Updating the two per-run Pages secrets and deploying the gated demo..."
put_pages_secret() {
  secret_name=$1
  secret_value=$2
  printf '%s\n' "$secret_value" | (
    cd "$repo_dir/deploy/pages-demo" || exit 1
    exec env -u TEST_DATABASE_URL -u WRANGLER_LOG -u WRANGLER_LOG_PATH \
      WRANGLER_LOG_SANITIZE=true WRANGLER_WRITE_LOGS=false \
      wrangler pages secret put "$secret_name" \
      --project-name tojima-mobile-order-demo
  ) >/dev/null 2>&1 &
  transient_pid=$!
  unset secret_value
  finish_transient
}
if ! put_pages_secret DEMO_ORIGIN "$tunnel_origin"; then
  fail "could not update the Pages tunnel origin secret"
fi
check_long_lived_children
if ! put_pages_secret DEMO_GATE_SECRET "$gate_secret"; then
  fail "could not update the Pages gateway secret"
fi
unset gate_secret
check_long_lived_children
(
  cd "$repo_dir/deploy/pages-demo" || exit 1
  exec env -u TEST_DATABASE_URL -u WRANGLER_LOG -u WRANGLER_LOG_PATH \
    WRANGLER_LOG_SANITIZE=true WRANGLER_WRITE_LOGS=false \
    wrangler pages deploy "$repo_dir/deploy/pages-demo" \
    --project-name tojima-mobile-order-demo --branch main
) >/dev/null 2>&1 &
transient_pid=$!
if ! finish_transient; then
  fail "Pages deployment failed; the previous deployment may still be active"
fi

printf '%s\n' "Demo guide: $public_origin/__demo/guide"
printf '%s\n' "Private synthetic credentials file: $credentials_file"
printf '%s\n' "Demo is running. Keep this terminal and this Mac awake; press Ctrl-C to stop the local runtime and tunnel."
while :; do
  check_long_lived_children
  sleep 2
done
