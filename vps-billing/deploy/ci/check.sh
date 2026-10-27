#!/usr/bin/env bash
# Run local/CI regressions against one source checkout. No GitHub writes.
# Usage: bash /trusted/controller/vps-billing/deploy/ci/check.sh /path/to/source
# The disposable MySQL credential needs CREATE/DROP DATABASE for migration tests;
# the selected database name must contain _test. Never use a production database.
set +x
set -euo pipefail
umask 077

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}
step() { printf '\n== %s ==\n' "$*"; }

[[ $# -le 1 ]] || fail 'Usage: check.sh [source-repository-root]'
[[ -n ${ASTER_MYSQL_TEST_DSN:-} ]] || fail 'ASTER_MYSQL_TEST_DSN is required; real MySQL tests must not silently skip.'
for tool in git go node npm python3 bash sh mktemp tee grep; do
  command -v "$tool" >/dev/null 2>&1 || fail "Required command unavailable: $tool"
done
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'This check requires a Linux amd64 runner to execute the built API.'

if [[ $# == 1 ]]; then
  source_path=$1
else
  controller_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
  source_path=$(git -C "$controller_dir" rev-parse --show-toplevel)
fi
source_root=$(cd -- "$source_path" && pwd -P)
project_root="$source_root/vps-billing"
for required in backend/go.mod frontend/package-lock.json tests/config_smoke.py tests/security_smoke.py deploy/scripts/test-release-manifest.py deploy/scripts/test-plugin-manager.py deploy/scripts/test-update-manager.py deploy/scripts/test-core-manager.py deploy/scripts/test-core-operation.py deploy/scripts/test-backup-native.sh deploy/ci/test_release_pipeline.py deploy/ci/check-resource-package.sh deploy/ci/resource_package_gate.py; do
  [[ -f "$project_root/$required" ]] || fail "Source checkout is missing $required"
done

# Ignore any inherited production, bootstrap, provider or smoke-test settings.
# Preserve the explicit disposable test DSN only until the Go test suite finishes.
for variable in ${!APP_@} ${!SESSION_SECRET@} ${!MYSQL_@} ${!BOOTSTRAP_@} ${!TRUSTED_PROXY@} ${!PAYMENT_PROVIDER@} ${!EMAIL_PROVIDER@} ${!SMTP_@} ${!PLUGIN_@} ${!UPDATE_@} ${!HOST_OPERATION_@} ${!INTEGRATIONS_CONFIG@} ${!LISTEN_ADDR@} ${!TEST_@}; do
  unset "$variable"
done
export NO_PROXY=127.0.0.1,localhost,::1
export no_proxy=$NO_PROXY
export PYTHONDONTWRITEBYTECODE=1

check_tmp=$(mktemp -d "${TMPDIR:-/tmp}/aster-ci-check.XXXXXXXX")
api_pid=''
api_running() {
  local running
  [[ -n "$api_pid" ]] || return 1
  while IFS= read -r running; do
    [[ "$running" == "$api_pid" ]] && return 0
  done < <(jobs -pr)
  return 1
}
cleanup() {
  local result=$?
  trap - EXIT INT TERM
  if api_running; then
    kill -TERM -- "$api_pid" 2>/dev/null || true
    for ((attempt = 0; attempt < 120; attempt++)); do
      api_running || break
      sleep 0.1
    done
    if api_running; then kill -KILL -- "$api_pid" 2>/dev/null || true; fi
  fi
  if [[ -n "$api_pid" ]]; then wait "$api_pid" 2>/dev/null || true; fi
  rm -rf -- "$check_tmp"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

step 'Go race tests with required disposable MySQL'
(
  cd -- "$project_root/backend"
  go test -race -count=1 -timeout=20m -v ./...
) | tee "$check_tmp/go-tests.log"
skip_scan_status=0
grep -E '^[[:space:]]*--- SKIP:' "$check_tmp/go-tests.log" >/dev/null 2>&1 || skip_scan_status=$?
case "$skip_scan_status" in
  0) fail 'A Go test was skipped; this validation requires all database tests to run.' ;;
  1) ;; # Complete scan with no skipped tests.
  *) fail "Go skip scanner failed (exit $skip_scan_status); test completeness is unknown." ;;
esac
unset ASTER_MYSQL_TEST_DSN

step 'Go vet and static Linux amd64 build'
(
  cd -- "$project_root/backend"
  go vet ./...
  go list -buildvcs=false -deps ./cmd/server > "$check_tmp/core-dependencies.txt"
  python3 - "$check_tmp/core-dependencies.txt" <<'PYDEPS'
from pathlib import Path
import sys
deps = set(Path(sys.argv[1]).read_text().splitlines())
if not deps or "aster/billing/cmd/server" not in deps:
    raise SystemExit("FAIL: core dependency inventory is incomplete")
if deps.intersection({"net/smtp", "os/exec"}) or any(name.startswith("aster/billing/plugins/") for name in deps):
    raise SystemExit("FAIL: core imported concrete plugin transport or process execution")
print("PASS: core excludes concrete plugin transport and process execution")
PYDEPS
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$check_tmp/aster-api" ./cmd/server
)

step 'Resource static package and protected production manager verification'
bash "$project_root/deploy/ci/check-resource-package.sh" "$source_root" | tee "$check_tmp/resource-package-verification.log"

step 'Frontend locked dependencies, types, tests, production build and formatting'
(
  cd -- "$project_root/frontend"
  npm ci --include=dev --no-audit --no-fund
  npm run typecheck
  npm test
  npm run build
  npm run format:check
)

step 'Release pipeline and dependency notice generator tests'
(
  cd -- "$project_root"
  python3 -m unittest discover -s deploy/ci -p 'test_*.py' -v
  python3 deploy/scripts/test-release-manifest.py -v
  python3 deploy/scripts/test-plugin-manager.py -v
  python3 deploy/scripts/test-update-manager.py -v
  python3 deploy/scripts/test-core-manager.py -v
  python3 deploy/scripts/test-core-operation.py -v
)

step 'Shell/Python syntax and mock backup regression'
while IFS= read -r -d '' script; do
  case $(head -n 1 -- "$script") in
    '#!/bin/sh'|'#!/usr/bin/env sh') sh -n "$script" ;;
    *) bash -n "$script" ;;
  esac
done < <(find "$project_root/deploy" -type f -name '*.sh' -print0)
python3 - "$project_root" <<'PY'
from pathlib import Path
import sys
root = Path(sys.argv[1])
for directory in (root / "deploy", root / "tests"):
    for path in sorted(directory.rglob("*.py")):
        compile(path.read_bytes(), str(path), "exec")
print("PASS: Python syntax")
PY
bash "$project_root/deploy/scripts/test-backup-native.sh"

step 'Production startup rejection regressions'
python3 "$project_root/tests/config_smoke.py" --binary "$check_tmp/aster-api"

step 'Loopback demo API and HTTP security regressions (public demo fixtures only)'
api_port=$(python3 - <<'PY'
import socket
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
    listener.bind(("127.0.0.1", 0))
    print(listener.getsockname()[1])
PY
)
api_base="http://127.0.0.1:$api_port"
APP_MODE=demo APP_ORIGIN="$api_base" LISTEN_ADDR="127.0.0.1:$api_port" \
  PAYMENT_PROVIDER=payment.disabled EMAIL_PROVIDER=email.disabled \
  "$check_tmp/aster-api" >"$check_tmp/demo-api.log" 2>&1 &
api_pid=$!
# The port is selected locally immediately before starting our own process. If a
# concurrent process wins that bind, fail; never stop it or test against it.
if ! python3 - "$api_base" "$api_pid" <<'PY'
import json
import os
import sys
import time
import urllib.request
base, process = sys.argv[1], int(sys.argv[2])
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
deadline = time.monotonic() + 30
while time.monotonic() < deadline:
    try:
        os.kill(process, 0)
    except ProcessLookupError:
        raise SystemExit("FAIL: the CI demo API exited before readiness")
    try:
        with opener.open(base + "/readyz", timeout=1) as response:
            ready = response.status == 200
        with opener.open(base + "/api/v1/session", timeout=1) as response:
            session = json.load(response)
        if ready and session.get("data", {}).get("demo_mode") is True:
            raise SystemExit(0)
    except (OSError, ValueError):
        pass
    time.sleep(0.1)
raise SystemExit("FAIL: the CI demo API did not become ready within 30 seconds")
PY
then
  fail 'Demo API startup failed. No requests that mutate state were sent.'
fi
api_running || fail 'The owned demo API exited; refusing to send smoke-test writes.'
TEST_CUSTOMER_EMAIL=demo@aster.local TEST_CUSTOMER_PASSWORD='DemoCustomer!2026' \
  TEST_ADMIN_EMAIL=admin@aster.local TEST_ADMIN_PASSWORD='DemoAdmin!2026' \
  TEST_SECOND_EMAIL=other@aster.local TEST_SECOND_PASSWORD='DemoOther!2026' \
  python3 "$project_root/tests/security_smoke.py" --base "$api_base" --origin "$api_base" --expect-fresh-cny
api_running || fail 'The owned demo API exited during HTTP regression tests.'

step 'All required checks passed (browser, live providers and deployment not exercised)'
