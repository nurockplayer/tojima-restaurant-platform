#!/bin/sh
# docker compose for the internal preview: sh deploy/preview/preview.sh up -d --build --wait
set -eu
ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
exec docker compose -f "$ROOT/compose.yaml" -f "$ROOT/deploy/preview/compose.preview.yaml" \
    --env-file "$ROOT/deploy/preview/.env" -p tojima-preview --profile staff "$@"
