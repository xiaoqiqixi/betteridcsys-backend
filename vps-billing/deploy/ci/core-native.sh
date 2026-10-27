#!/usr/bin/env bash
# Actual Core executor acceptance. Only the disposable hosted native fixture.
# This wrapper is not an installation or artifact-trust bypass for operators.
set +x
set -euo pipefail
umask 077
fail() { printf 'FAIL Core executor acceptance: %s\n' "$*" >&2; exit 1; }
[[ $# == 3 && $1 == --allow-isolated-runner ]] || fail 'Explicit isolated-runner opt-in, source root and new evidence directory required.'
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]] || fail 'Only disposable GitHub-hosted Actions runners are accepted.'
[[ ${GITHUB_REPOSITORY:-} == xiaoqiqixi/betteridcsys ]] || fail 'Unexpected repository; no privileged actions permitted.'
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]] || fail 'Missing run identity.'
[[ $EUID != 0 && $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'Start as the ordinary Linux amd64 runner account.'
[[ $(cat /proc/1/comm) == systemd ]] || fail 'PID 1 must be systemd; no simulated service acceptance.'
python3 - <<'PY'
from pathlib import Path
values = dict(line.split('=', 1) for line in Path('/etc/os-release').read_text().splitlines() if '=' in line)
if values.get('ID', '').strip('"') != 'ubuntu' or values.get('VERSION_ID', '').strip('"') not in {'22.04', '24.04'}:
    raise SystemExit('FAIL: Core executor acceptance requires Ubuntu 22.04/24.04')
PY
source_root=$(cd -- "$2" && pwd -P)
[[ $3 == /* && ! -e $3 && -d $(dirname -- "$3") ]] || fail 'Evidence directory must be new with an existing parent.'
for name in ASTER_CORE_FIXTURE_OPT ASTER_CORE_CANDIDATE_ASSETS ASTER_CORE_CANDIDATE_RELEASE ASTER_CORE_BASELINE_RELEASE ASTER_CORE_GITHUB_TOKEN_FILE ASTER_ACCEPT_NGINX_BIN ASTER_NATIVE_MYSQL_ROOT ASTER_NATIVE_MYSQL_SOCKET ASTER_NATIVE_MYSQL_ADMIN_CNF ASTER_NATIVE_MYSQL_FIXTURE_ROOT; do
  [[ -n ${!name:-} && ${!name} == /* ]] || fail "Missing owned native fixture contract: $name"
done
[[ $ASTER_CORE_FIXTURE_OPT =~ ^/opt/aster-ci-[0-9]+-[0-9]+-[0-9]+$ ]] || fail 'Unexpected owned native fixture root.'
[[ $ASTER_CORE_CANDIDATE_RELEASE == "$ASTER_CORE_FIXTURE_OPT/releases/a" && $ASTER_CORE_BASELINE_RELEASE == "$ASTER_CORE_FIXTURE_OPT/core-upgrade/releases/0.5.2" ]] || fail 'Release paths must belong to the parent native fixture.'
[[ -S $ASTER_NATIVE_MYSQL_SOCKET && -f $ASTER_NATIVE_MYSQL_ADMIN_CNF && ! -L $ASTER_NATIVE_MYSQL_ADMIN_CNF ]] || fail 'Native MySQL fixture unavailable.'
[[ $(realpath -e "$ASTER_NATIVE_MYSQL_SOCKET") == "$ASTER_NATIVE_MYSQL_FIXTURE_ROOT/"* ]] || fail 'MySQL socket is outside the owned fixture.'
[[ $(stat -c %a "$ASTER_NATIVE_MYSQL_ADMIN_CNF") == 600 && $(stat -c %u "$ASTER_NATIVE_MYSQL_ADMIN_CNF") == "$EUID" ]] || fail 'MySQL fixture client file must be private and runner-owned.'
for tool in python3 sudo systemctl openssl git; do command -v "$tool" >/dev/null || fail "Required tool unavailable: $tool"; done
sudo -n true || fail 'Passwordless sudo is required on the disposable runner.'
mkdir -m 700 -- "$3"
controller="$ASTER_CORE_FIXTURE_OPT/core-acceptance-controller"
[[ ! -e $controller && ! -L $controller ]] || fail 'Refusing to reuse a controller directory.'
sudo -n install -d -o root -g root -m 755 "$controller"
for file in core-native.py release_pipeline.py; do
  sudo -n install -o root -g root -m 644 "$source_root/vps-billing/deploy/ci/$file" "$controller/$file"
done
for file in core-operation.py core-manager.py update-manager.py plugin-manager.py; do
  sudo -n install -o root -g root -m 644 "$source_root/vps-billing/deploy/scripts/$file" "$controller/$file"
done
sudo -n env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin PYTHONDONTWRITEBYTECODE=1 \
  GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=github-hosted GITHUB_REPOSITORY=xiaoqiqixi/betteridcsys \
  "GITHUB_RUN_ID=$GITHUB_RUN_ID" "GITHUB_RUN_ATTEMPT=$GITHUB_RUN_ATTEMPT" \
  python3 -I "$controller/core-native.py" --source "$source_root" --evidence "$3" \
  --fixture-root "$ASTER_CORE_FIXTURE_OPT" --candidate-assets "$ASTER_CORE_CANDIDATE_ASSETS" \
  --candidate-release "$ASTER_CORE_CANDIDATE_RELEASE" --baseline-release "$ASTER_CORE_BASELINE_RELEASE" \
  --github-token-file "$ASTER_CORE_GITHUB_TOKEN_FILE" \
  --nginx "$ASTER_ACCEPT_NGINX_BIN" --mysql-root "$ASTER_NATIVE_MYSQL_ROOT" \
  --mysql-socket "$ASTER_NATIVE_MYSQL_SOCKET" --mysql-admin "$ASTER_NATIVE_MYSQL_ADMIN_CNF" \
  --mysql-fixture "$ASTER_NATIVE_MYSQL_FIXTURE_ROOT"
