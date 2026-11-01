#!/usr/bin/env bash
# Run the gateway boundary before the complete native backend regression suite.
set -euo pipefail
umask 077
[[ -n ${ASTER_MYSQL_TEST_DSN:-} ]] || { echo 'Explicit disposable MySQL DSN required.' >&2; exit 1; }
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
scratch=$(mktemp -d "${RUNNER_TEMP:-/tmp}/aster-gateway-check.XXXXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
cd "$root/vps-billing/backend"
go test -mod=readonly -race -count=1 -v -timeout=10m ./internal/billing -run 'Gateway|Payment' | tee "$scratch/tests.log"
if grep -Eq -- '^[[:space:]]*--- SKIP:' "$scratch/tests.log"; then
  echo 'Real gateway and payment regressions must not skip tests.' >&2; exit 1
fi
