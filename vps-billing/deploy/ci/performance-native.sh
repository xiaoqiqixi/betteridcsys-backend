#!/usr/bin/env bash
# Explicit synthetic HTTP benchmark inside mysql-native.sh's owned fixture.
# This never starts a production service or sends payment/email traffic.
set +x
set -euo pipefail
umask 077

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
report_failure_phase() {
  # A private process log is never forwarded. Only exact fixed stage markers
  # survive this bounded projection, including when an error contains secrets.
  python3 -I - "$1" <<'PYPERFPHASE'
import os
import re
import stat
import sys

phase = 'unknown'
try:
    fd = os.open(sys.argv[1], os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) & 0o077 or info.st_size > 1024 * 1024:
            raise ValueError()
        for line in stream.read(1024 * 1024 + 1).splitlines():
            match = re.fullmatch(rb'ASTER_HTTP_PERF_STAGE=(configuration|database|seed|hash|worker|preflight|warmup|measurement|cleanup|report)', line)
            if match:
                phase = match.group(1).decode('ascii')
except (OSError, ValueError, IndexError):
    pass
print('PERFORMANCE_FAILURE_STAGE=' + phase)
PYPERFPHASE
}
perf_repo=''
perf_output=''
perf_baseline=''
perf_allowed=false
while (($#)); do
  case "$1" in
    --allow-isolated-runner) perf_allowed=true; shift ;;
    --repo-root|--output|--baseline-sha)
      [[ $# -ge 2 ]] || fail 'Missing performance argument.'
      case "$1" in
        --repo-root) perf_repo=$2 ;;
        --output) perf_output=$2 ;;
        --baseline-sha) perf_baseline=$2 ;;
      esac
      shift 2 ;;
    *) fail 'Unsupported performance argument.' ;;
  esac
done

# Refuse local/self-hosted execution before reading any inherited test DSN or
# creating output. The ordinary Go test suite never enables this benchmark.
[[ $perf_allowed == true && ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]] \
  || fail 'Performance measurement requires an explicitly opted-in disposable GitHub-hosted runner.'
[[ ${GITHUB_REPOSITORY:-} == xiaoqiqixi/betteridcsys && ${GITHUB_REF:-} == refs/heads/main ]] \
  || fail 'Performance measurement is restricted to the public main checkout.'
[[ ${GITHUB_EVENT_NAME:-} == push || ${GITHUB_EVENT_NAME:-} == workflow_dispatch ]] \
  || fail 'Unsupported performance event.'
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ && ${GITHUB_SHA:-} =~ ^[0-9a-f]{40}$ ]] \
  || fail 'Missing fixed run/source identity.'
[[ $EUID != 0 && $(uname -s) == Linux && $(uname -m) == x86_64 ]] \
  || fail 'Use the unprivileged Linux amd64 runner account.'
. /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 24.04 ]] || fail 'Only Ubuntu 24.04 is supported.'
for perf_tool in git go python3 tar sha256sum mktemp timeout; do
  command -v "$perf_tool" >/dev/null || fail 'A required performance tool is unavailable.'
done
[[ $(GOENV=off GOTOOLCHAIN=local GOFLAGS='' go env GOVERSION) == go1.27.3 ]] || fail 'The reviewed Go 1.27.3 toolchain is required.'
[[ -n $perf_repo && -n $perf_output && $perf_output == /* ]] || fail 'Explicit source and absolute output paths are required.'
perf_repo=$(cd -- "$perf_repo" && pwd -P)
[[ $(git -C "$perf_repo" rev-parse --show-toplevel) == "$perf_repo" && $(git -C "$perf_repo" rev-parse HEAD) == "$GITHUB_SHA" ]] \
  || fail 'Performance source must be the exact selected checkout.'
git -C "$perf_repo" diff --quiet HEAD -- || fail 'Performance source has tracked modifications.'
[[ -z $(git -C "$perf_repo" ls-files --others --exclude-standard) ]] || fail 'Performance source has untracked files.'
if [[ -n $perf_baseline ]]; then
  [[ $perf_baseline =~ ^[0-9a-f]{40}$ ]] || fail 'Baseline must be a full fixed commit SHA.'
  [[ $(git -C "$perf_repo" cat-file -t "$perf_baseline" 2>/dev/null) == commit ]] \
    || fail 'Baseline source is not available in the reviewed repository history.'
  git -C "$perf_repo" merge-base --is-ancestor "$perf_baseline" "$GITHUB_SHA" \
    || fail 'Baseline must be an ancestor of the selected main source.'
fi

# The inherited connection must identify mysql-native.sh's private Unix socket,
# not merely a database with a test-like name. Never print its value.
python3 -I - <<'PY'
import os
from pathlib import Path
import re
import stat

def reject():
    raise SystemExit('FAIL: An owned native MySQL fixture is required.')

root = Path(os.environ.get('ASTER_NATIVE_MYSQL_FIXTURE_ROOT', ''))
parent = os.environ.get('ASTER_NATIVE_FIXTURE_ROOT', '')
if not re.fullmatch(r'/run/aster-native\.[A-Za-z0-9]{8}', parent):
    reject()
if root.parent != Path(parent) or not re.fullmatch(r'aster-mysql\.[A-Za-z0-9]{8}', root.name):
    reject()
for directory in (Path(parent), root):
    info = directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) & 0o077:
        reject()
socket = root / 'mysql.sock'
info = socket.lstat()
if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.geteuid():
    reject()
if os.environ.get('ASTER_NATIVE_MYSQL_SOCKET') != str(socket):
    reject()
expected = 'aster_ci:AsterCiFixtureOnly2026@unix(' + str(socket) + ')/aster_ci_test?parseTime=true'
if os.environ.get('ASTER_MYSQL_TEST_DSN') != expected:
    reject()
PY

perf_project="$perf_repo/vps-billing"
perf_harness="$perf_project/backend/internal/billing/http_performance_test.go"
[[ -f $perf_harness && ! -L $perf_harness && -f $perf_project/deploy/ci/performance-report.py ]] \
  || fail 'The source-bound performance harness and validator are required.'
[[ ! -e $perf_output && ! -L $perf_output && -d $(dirname -- "$perf_output") ]] \
  || fail 'Performance output must be a new directory under an existing parent.'
mkdir -- "$perf_output"
perf_scratch=$(mktemp -d "$ASTER_NATIVE_FIXTURE_ROOT/aster-performance.XXXXXXXX")
perf_harness_sha=$(sha256sum -- "$perf_harness")
perf_harness_sha=${perf_harness_sha%% *}
cleanup() {
  local perf_result=$? perf_removed=false
  trap - EXIT INT TERM
  if [[ $perf_scratch =~ ^/run/aster-native\.[A-Za-z0-9]{8}/aster-performance\.[A-Za-z0-9]{8}$ && -d $perf_scratch && ! -L $perf_scratch && $(stat -c %u -- "$perf_scratch") == "$EUID" ]]; then
    rm -rf --one-file-system -- "$perf_scratch" || true
  fi
  [[ ! -e $perf_scratch && ! -L $perf_scratch ]] && perf_removed=true
  python3 -I - "$perf_output/runner-cleanup.json" "$perf_result" "$perf_removed" "$GITHUB_SHA" <<'PY'
import json
import os
import sys
value = {'schema': 'aster.http-performance-runner-cleanup/1', 'source_sha': sys.argv[4],
         'benchmark_exit_code': int(sys.argv[2]), 'owned_scratch_removed': sys.argv[3] == 'true'}
fd = os.open(sys.argv[1], os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
with os.fdopen(fd, 'w') as output:
    json.dump(value, output, sort_keys=True)
    output.write('\n')
print('PERFORMANCE_RUNNER_CLEANUP_JSON ' + json.dumps(value, sort_keys=True, separators=(',', ':')))
PY
  [[ $perf_result == 0 && $perf_removed == true ]] || exit 1
  exit 0
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Clear inherited app/plugin/provider settings and runtime/build overrides. Only
# the validated disposable DSN is retained; child configuration travels through
# the harness's private control channel, never a debug HTTP API.
for perf_variable in ${!APP_@} ${!SESSION_SECRET@} ${!MYSQL_@} ${!BOOTSTRAP_@} ${!SMTP_@} ${!PLUGIN_@} ${!PAYMENT_PROVIDER@} ${!EMAIL_PROVIDER@} ${!INTEGRATIONS_CONFIG@} ${!TRUSTED_PROXY@} ${!LISTEN_ADDR@} ${!UPDATE_@} ${!HOST_OPERATION_@} ${!TEST_@} ${!ASTER_HTTP_PERF_@}; do
  unset "$perf_variable"
done
unset GH_TOKEN GITHUB_TOKEN ASTER_ACCEPT_GITHUB_TOKEN GOFLAGS GODEBUG GOMAXPROCS GOGC GOMEMLIMIT
export GOENV=off GOTOOLCHAIN=local PYTHONDONTWRITEBYTECODE=1
export ASTER_HTTP_PERF_SOURCE_SHA="$GITHUB_SHA"
export ASTER_HTTP_PERF_RESULT_PATH="$perf_scratch/raw-result.json"
export ASTER_HTTP_PERF_USERS=100 ASTER_HTTP_PERF_ORDERS=10000
export ASTER_HTTP_PERF_WARMUP=1s ASTER_HTTP_PERF_DURATION=3s ASTER_HTTP_PERF_REPEATS=2
export ASTER_HTTP_PERF_SEED=aster-http-performance-v1
printf 'Building the source-bound synthetic HTTP harness.\n'
if ! (cd -- "$perf_project/backend" && go test -mod=readonly -buildvcs=false -c -o "$perf_scratch/candidate.test" ./internal/billing) >"$perf_scratch/private-build.log" 2>&1; then
  printf 'PERFORMANCE_FAILURE_STAGE=compile\n'
  fail 'Performance harness compilation failed; private compiler output was withheld.'
fi
if [[ -n $perf_baseline ]]; then
  mkdir -- "$perf_scratch/baseline"
  git -C "$perf_repo" archive --format=tar "$perf_baseline" vps-billing/backend \
    | tar --extract --directory="$perf_scratch/baseline" --no-same-owner
  perf_baseline_backend="$perf_scratch/baseline/vps-billing/backend"
  perf_baseline_harness="$perf_baseline_backend/internal/billing/http_performance_test.go"
  # runtime.Caller must find the same harness bytes at the overlay target too.
  # Only this test file is supplied by the current source; production code is
  # from the independently recorded fixed baseline commit.
  [[ ! -L $perf_baseline_harness && ( ! -e $perf_baseline_harness || -f $perf_baseline_harness ) ]] \
    || fail 'Baseline harness destination is unsafe.'
  cp -- "$perf_harness" "$perf_baseline_harness"
  python3 -I - "$perf_baseline_harness" "$perf_harness" "$perf_scratch/overlay.json" <<'PY'
import json
import sys
with open(sys.argv[3], 'x') as output:
    json.dump({'Replace': {sys.argv[1]: sys.argv[2]}}, output)
PY
  if ! (cd -- "$perf_baseline_backend" && go test -mod=readonly -buildvcs=false -overlay "$perf_scratch/overlay.json" -c -o "$perf_scratch/baseline.test" ./internal/billing) >"$perf_scratch/private-baseline-build.log" 2>&1; then
    printf 'PERFORMANCE_FAILURE_STAGE=compile\n'
    fail 'Baseline is incompatible with the selected harness; private compiler output was withheld.'
  fi
  export ASTER_HTTP_PERF_BASELINE_BINARY="$perf_scratch/baseline.test"
  perf_binary_sha=$(sha256sum -- "$ASTER_HTTP_PERF_BASELINE_BINARY")
  export ASTER_HTTP_PERF_BASELINE_BINARY_SHA256=${perf_binary_sha%% *}
  export ASTER_HTTP_PERF_BASELINE_SOURCE_SHA="$perf_baseline"
fi
printf 'Measuring synthetic loopback HTTP only; raw process output stays private.\n'
if ! timeout --signal=TERM --kill-after=30s 960s "$perf_scratch/candidate.test" \
  -test.run '^$' -test.bench '^BenchmarkHTTPPerformance$' -test.benchtime=1x -test.count=1 -test.timeout=15m \
  >"$perf_scratch/private-benchmark.log" 2>&1; then
  report_failure_phase "$perf_scratch/private-benchmark.log"
  fail 'The isolated performance benchmark failed; private process output was withheld.'
fi
perf_report_args=(--input "$ASTER_HTTP_PERF_RESULT_PATH" --output "$perf_output/performance.json"
  --source-sha "$GITHUB_SHA" --harness-sha256 "$perf_harness_sha")
if [[ -n $perf_baseline ]]; then perf_report_args+=(--baseline-sha "$perf_baseline"); fi
if ! python3 -I "$perf_project/deploy/ci/performance-report.py" "${perf_report_args[@]}"; then
  printf 'PERFORMANCE_FAILURE_STAGE=report\n'
  fail 'Performance report validation failed.'
fi
