#!/usr/bin/env bash
# Invoked by accept-native.sh only on the disposable hosted Ubuntu runner.
set +x
set -euo pipefail
umask 077
fail() { printf 'FAIL runtime plugin acceptance: %s\n' "$*" >&2; exit 1; }
[[ $# == 3 && $1 == --allow-isolated-runner ]] || fail 'Explicit isolated-runner opt-in, source root and new evidence directory required.'
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]] || fail 'Only disposable GitHub-hosted runners are accepted.'
[[ ${GITHUB_REPOSITORY:-} == xiaoqiqixi/betteridcsys ]] || fail 'Unexpected repository; no privileged actions permitted.'
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]] || fail 'Missing run identity.'
[[ $EUID != 0 && $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'Start as the ordinary Linux amd64 runner account.'
[[ $(cat /proc/1/comm) == systemd ]] || fail 'PID 1 must be systemd; no simulated service acceptance.'
python3 - <<'PY'
from pathlib import Path
values = dict(line.split('=', 1) for line in Path('/etc/os-release').read_text().splitlines() if '=' in line)
if values.get('ID', '').strip('"') != 'ubuntu' or values.get('VERSION_ID', '').strip('"') not in {'22.04', '24.04'}:
    raise SystemExit('FAIL: plugin acceptance requires Ubuntu 22.04/24.04')
PY
source_root=$(cd -- "$2" && pwd -P)
[[ $3 == /* && ! -e $3 && -d $(dirname -- "$3") ]] || fail 'Evidence directory must be new with an existing parent.'
for name in ASTER_PLUGIN_OPT ASTER_PLUGIN_CONFIG ASTER_PLUGIN_RUN ASTER_PLUGIN_SCRATCH ASTER_PLUGIN_API_UNIT ASTER_PLUGIN_WORKER_UNIT ASTER_PLUGIN_API_USER ASTER_PLUGIN_WORKER_USER ASTER_PLUGIN_WEB_USER ASTER_PLUGIN_SOCKET_GROUP ASTER_PLUGIN_HTTPS_PORT; do
  [[ -n ${!name:-} ]] || fail "Missing owned fixture contract: $name"
done
for tool in go python3 sudo systemctl systemd-analyze sha256sum timeout; do command -v "$tool" >/dev/null || fail "Required tool unavailable: $tool"; done
sudo -n true || fail 'Passwordless sudo is required on the disposable runner.'
mkdir -m 700 -- "$3"
export PYTHONDONTWRITEBYTECODE=1
python3 "$source_root/vps-billing/deploy/ci/plugin-native.py" --source "$source_root" --evidence "$3"
