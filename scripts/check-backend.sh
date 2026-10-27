#!/usr/bin/env bash
# Actual isolated native MySQL Go regressions; not frontend/browser/install proof.
set -euo pipefail
umask 077
[[ -n ${ASTER_MYSQL_TEST_DSN:-} ]] || { echo 'Explicit disposable MySQL DSN required.' >&2; exit 1; }
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
scratch=$(mktemp -d "${RUNNER_TEMP:-/tmp}/aster-backend-check.XXXXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
cd "$root/vps-billing/backend"
go mod download
go mod verify
go test -mod=readonly -race -count=1 -v -timeout=20m ./... | tee "$scratch/tests.log"
if grep -Eq -- '^[[:space:]]*--- SKIP:' "$scratch/tests.log"; then
  echo 'Real MySQL regression must not skip tests.' >&2; exit 1
fi
go vet -mod=readonly ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -trimpath -buildvcs=false -o "$scratch/aster-api" ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -trimpath -buildvcs=false -o "$scratch/aster-plugin-host" ./cmd/plugin-host
printf '%s\n' 'PASS: real MySQL, Go race/vet and Linux builds; browser/install/upgrade are separate gates.'
