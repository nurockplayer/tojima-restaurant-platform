#!/bin/sh
# Store the Cloudflare tunnel token (read from stdin, never from arguments).
set -eu
HERE=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
# read returns 1 at EOF without a newline; keep the token anyway.
IFS= read -r token || [ -n "$token" ]
[ -n "$token" ] || { echo 'tunnel token missing on stdin' >&2; exit 1; }
umask 077
grep -v '^TUNNEL_TOKEN=' "$HERE/.env" >"$HERE/.env.new" || true
printf 'TUNNEL_TOKEN=%s\n' "$token" >>"$HERE/.env.new"
mv "$HERE/.env.new" "$HERE/.env"
echo 'tunnel token stored'
