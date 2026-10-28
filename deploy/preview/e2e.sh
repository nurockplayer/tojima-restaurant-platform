#!/bin/sh
# P-94 phone-journey E2E against the internal preview.
#
#   sh deploy/preview/e2e.sh            # both engines, sequential
#   E2E_ONLY=webkit sh deploy/preview/e2e.sh
#
# Needs Docker and ~/.tojima-preview-nas/e2e-credentials.json (0600); the first
# run enrolls the OTP secret and writes it back there. The image is the pinned
# playwright release, so the browser builds match the pinned playwright-core.
set -eu

repo=$(cd "$(dirname "$0")/../.." && pwd)
out=${E2E_OUT:-"$repo/deploy/preview/e2e/out"}
creds=${E2E_CREDS:-"$HOME/.tojima-preview-nas"}

[ -f "$creds/e2e-credentials.json" ] || { echo "missing $creds/e2e-credentials.json" >&2; exit 2; }
[ -f "$creds/tables.csv" ] || { echo "missing $creds/tables.csv" >&2; exit 2; }
mkdir -p "$out"

set +e
docker run --rm --init --ipc=host \
  -e E2E_ONLY="${E2E_ONLY:-}" \
  -v "$repo/deploy/preview/e2e:/src:ro" \
  -v "$creds:/creds" \
  -v "$out:/out" \
  mcr.microsoft.com/playwright:v1.56.1-noble \
  sh -c 'cp -r /src /work && cd /work && corepack enable &&
         COREPACK_ENABLE_DOWNLOAD_PROMPT=0 pnpm install --frozen-lockfile &&
         node journey.mjs /creds /out'
status=$?
set -e

echo "report: $out/report.json"
exit "$status"
