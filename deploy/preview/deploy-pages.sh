#!/bin/sh
# Build the SPA in Docker and deploy it with the API proxy to Pages project
# tojima-preview. Run on a machine with Docker and an authenticated Wrangler.
set -eu
ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
P=$ROOT/deploy/preview/pages
docker build --target web -t tojima-preview-web "$ROOT"
cid=$(docker create tojima-preview-web)
trap 'docker rm -f "$cid" >/dev/null 2>&1 || true; rm -rf "$P/dist"' EXIT
rm -rf "$P/dist"
docker cp "$cid:/src/web/dist" "$P/dist"
cp "$P/_routes.json" "$P/_headers" "$P/dist/"
cd "$P"
wrangler pages deploy dist --project-name tojima-preview --branch main --commit-dirty=true
