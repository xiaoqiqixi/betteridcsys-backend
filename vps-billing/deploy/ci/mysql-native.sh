#!/usr/bin/env bash
# Disposable native MySQL 8.4 fixture; never install a service or alter host users.
# Usage: bash mysql-native.sh [--packages /path/to/pinned-debs] -- command [args...]
# RUNNER_TEMP must be an existing private CI scratch directory. Ubuntu 22/24
# native runtime libraries must be provisioned by the workflow, not this script.
# Offline packages still require every reviewed, committed manifest checksum.
set +x
set -euo pipefail
umask 077

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
command_stop_seconds=${ASTER_NATIVE_COMMAND_STOP_SECONDS:-90}
[[ $command_stop_seconds =~ ^([1-9]|[1-8][0-9]|90)$ ]] \
  || fail 'ASTER_NATIVE_COMMAND_STOP_SECONDS must be an integer from 1 through 90.'
package_source=''
if [[ ${1:-} == --packages ]]; then
  [[ $# -ge 3 ]] || fail 'Usage: mysql-native.sh [--packages DIRECTORY] -- command [args...]'
  package_source=$2
  shift 2
fi
[[ ${1:-} == -- && $# -ge 2 ]] || fail 'Usage: mysql-native.sh [--packages DIRECTORY] -- command [args...]'
shift
command -v python3 >/dev/null 2>&1 || fail 'python3 is required to validate the distribution manifest.'
controller_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)

# This gate precedes every download, archive extraction and database execution.
# Never accept a caller-supplied hash, URL or already-extracted executable tree.
if ! metadata=$(python3 -I "$controller_dir/mysql-distribution.py"); then
  exit 1
fi
mapfile -t distribution <<<"$metadata"
distribution_manifest=${distribution[0]}
mysql_version=${distribution[1]}

[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'Native MySQL CI requires Linux amd64.'
[[ $EUID != 0 ]] || fail 'Run this disposable fixture as an unprivileged CI user, never root.'
[[ -n ${RUNNER_TEMP:-} && -d $RUNNER_TEMP && -w $RUNNER_TEMP ]] || fail 'RUNNER_TEMP must identify an existing writable CI scratch directory.'
for tool in dpkg-deb sha256sum mktemp cp setsid timeout; do
  command -v "$tool" >/dev/null 2>&1 || fail "Required command unavailable: $tool"
done
if [[ -z $package_source ]]; then
  command -v curl >/dev/null 2>&1 || fail 'curl is required unless --packages is supplied.'
else
  [[ -d $package_source && -r $package_source ]] || fail 'The specified offline package directory is not readable.'
fi

# Ignore inherited MySQL passwords, login paths, server options and test DSNs.
for variable in ${!MYSQL_@} ${!MYSQLTEST_@} ${!ASTER_NATIVE_MYSQL_@} ASTER_MYSQL_TEST_DSN; do unset "$variable"; done
fixture_tmp=$(mktemp -d "$RUNNER_TEMP/aster-mysql.XXXXXXXX")
mysql_pid=''
command_pid=''
running_job() {
  local expected=$1 current
  [[ -n $expected ]] || return 1
  while IFS= read -r current; do
    [[ $current == "$expected" ]] && return 0
  done < <(jobs -pr)
  return 1
}
stop_job() {
  local owned=$1 group=${2:-false} attempts=100
  # A native acceptance child may need to stop its owned systemd/Nginx units.
  # Give it a bounded grace period before stopping this fixture's MySQL server.
  if [[ $group == true ]]; then attempts=$((command_stop_seconds * 10)); fi
  [[ -n $owned ]] || return 0
  if running_job "$owned"; then
    if [[ $group == true ]]; then
      kill -TERM -- "-$owned" 2>/dev/null || kill -TERM -- "$owned" 2>/dev/null || true
    else
      kill -TERM -- "$owned" 2>/dev/null || true
    fi
    for ((attempt = 0; attempt < attempts; attempt++)); do
      running_job "$owned" || break
      sleep 0.1
    done
    if running_job "$owned"; then
      if [[ $group == true ]]; then
        kill -KILL -- "-$owned" 2>/dev/null || kill -KILL -- "$owned" 2>/dev/null || true
      else
        kill -KILL -- "$owned" 2>/dev/null || true
      fi
    fi
  fi
  wait "$owned" 2>/dev/null || true
}
cleanup() {
  local result=$?
  trap - EXIT INT TERM
  stop_job "$command_pid" true
  stop_job "$mysql_pid"
  rm -rf -- "$fixture_tmp"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
diagnostic_codes() {
  # Never print SQL, connection strings or arbitrary server/client log messages.
  # Numeric Oracle error identifiers and SQLSTATE are enough to triage CI startup.
  python3 - "$fixture_tmp/initialize.log" "$fixture_tmp/mysql.log" "$fixture_tmp/server.log" "$fixture_tmp/readiness.log" <<'PY'
from pathlib import Path
import re
import sys
codes = set()
for filename in sys.argv[1:]:
    try:
        text = Path(filename).read_text(errors="replace")
    except OSError:
        continue
    codes.update(re.findall(r"\[ERROR\]\s+\[(MY-[0-9]{6})\]", text))
    codes.update(f"client-{number}/{state}" for number, state in re.findall(r"ERROR ([0-9]{4,5}) \(([A-Z0-9]{5})\)", text))
print("MySQL diagnostic codes: " + (", ".join(sorted(codes)[:20]) or "none available; private log text withheld"), file=sys.stderr)
PY
}

mkdir "$fixture_tmp/packages" "$fixture_tmp/distribution" "$fixture_tmp/data" "$fixture_tmp/tmp"
for ((index = 2; index < ${#distribution[@]}; index += 3)); do
  filename=${distribution[index]}
  digest=${distribution[index + 1]}
  expected_size=${distribution[index + 2]}
  package_path="$fixture_tmp/packages/$filename"
  if [[ -n $package_source ]]; then
    [[ -f $package_source/$filename && ! -L $package_source/$filename && -r $package_source/$filename ]] \
      || fail 'Every offline MySQL package must be a readable regular file, not a link or device.'
    cp -- "$package_source/$filename" "$package_path"
  else
    # No retry or mirror: a refusal or failed transfer stops this run.
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
      --connect-timeout 20 --max-time 300 --max-filesize "$expected_size" --output "$package_path" \
      "https://repo.mysql.com/apt/ubuntu/pool/mysql-8.4-lts/m/mysql-community/$filename"
  fi
  printf '%s  %s\n' "$digest" "$package_path" | sha256sum --check --status \
    || fail 'Native MySQL package SHA-256 mismatch; refusing extraction and execution.'
  [[ $(stat -c %s -- "$package_path") == "$expected_size" ]] || fail 'Native MySQL package size does not match the manifest.'
  printf 'Verified %s SHA256 %s\n' "$filename" "$digest"
done

# Only after ALL hashes match, validate Debian control metadata and extract data
# archives. dpkg-deb never installs packages or executes their maintainer scripts.
# Python's data filter rejects devices and escaping links; the extra path check
# rejects absolute/traversing members rather than silently normalizing them.
python3 - "$distribution_manifest" "$fixture_tmp" <<'PY'
import json
from pathlib import Path, PurePosixPath
import subprocess
import sys
import tarfile
manifest = json.loads(Path(sys.argv[1]).read_text())
root = Path(sys.argv[2])
destination = root / "distribution"
packages = []
for item in manifest["packages"]:
    path = root / "packages" / f"{item['name']}_{manifest['package_version']}_amd64.deb"
    info = subprocess.check_output(["dpkg-deb", "--field", str(path), "Package", "Version", "Architecture"], text=True, timeout=30)
    fields = dict(line.split(": ", 1) for line in info.splitlines())
    if fields != {"Package": item["name"], "Version": manifest["package_version"], "Architecture": "amd64"}:
        raise SystemExit("FAIL: Debian package identity/version/architecture does not match its pin")
    packages.append(path)
for number, package in enumerate(packages):
    archive_path = root / f"package-data-{number}.tar"
    with archive_path.open("wb") as output:
        subprocess.run(["dpkg-deb", "--fsys-tarfile", str(package)], stdout=output, check=True, timeout=60)
    with tarfile.open(archive_path, "r:") as archive:
        members = archive.getmembers()
        for member in members:
            if member.name.startswith("/") or ".." in PurePosixPath(member.name).parts or any(ord(c) < 32 for c in member.name + member.linkname):
                raise SystemExit("FAIL: unsafe path in Debian data archive")
        archive.extractall(destination, members=members, filter="data")
    archive_path.unlink()
PY
mysql_root="$fixture_tmp/distribution/usr"
mysqld="$mysql_root/sbin/mysqld"
for binary in sbin/mysqld bin/mysql bin/mysqldump; do
  [[ -f $mysql_root/$binary && ! -L $mysql_root/$binary && -x $mysql_root/$binary ]] \
    || fail "Verified packages lack the expected $binary executable."
done
messages_dir=''
for candidate in "$mysql_root/share/mysql-8.4" "$mysql_root/share/mysql"; do
  if [[ -f $candidate/english/errmsg.sys ]]; then messages_dir=$candidate; break; fi
done
[[ -n $messages_dir ]] || fail 'Verified packages lack the MySQL server message directory.'
plugin_dir="$mysql_root/lib/mysql/plugin"
[[ -d $plugin_dir ]] || fail 'Verified packages lack the MySQL plugin directory.'
if ! server_version=$(timeout --signal=TERM --kill-after=1s 10s \
  "$mysqld" --no-defaults --version 2>"$fixture_tmp/version.log"); then
  # This pre-initialization command sees no fixture credentials and helps identify
  # a missing native library without dumping database initialization/server logs.
  cat "$fixture_tmp/version.log" >&2
  fail 'Native MySQL executable cannot run; check the reviewed Ubuntu runtime library prerequisites.'
fi
[[ $server_version == *"Ver $mysql_version "* ]] || fail 'Native executable version does not match the reviewed manifest.'

# A public test fixture credential, never a production secret. The root account
# is locked by init-file before the server accepts connections. No inherited
# defaults/login files are read, and no existing host MySQL instance is touched.
fixture_password='AsterCiFixtureOnly2026'
cat >"$fixture_tmp/bootstrap.sql" <<'SQL'
CREATE DATABASE aster_ci_test CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE USER 'aster_ci'@'127.0.0.1' IDENTIFIED BY 'AsterCiFixtureOnly2026';
GRANT ALL PRIVILEGES ON *.* TO 'aster_ci'@'127.0.0.1';
CREATE USER 'aster_ci'@'localhost' IDENTIFIED BY 'AsterCiFixtureOnly2026';
GRANT ALL PRIVILEGES ON *.* TO 'aster_ci'@'localhost' WITH GRANT OPTION;
ALTER USER 'root'@'localhost' ACCOUNT LOCK;
SQL
socket_path="$fixture_tmp/mysql.sock"
python3 - "$socket_path" <<'PY'
import os
import sys
if len(os.fsencode(sys.argv[1])) > 107:
    raise SystemExit("FAIL: RUNNER_TEMP path is too long for the private MySQL socket")
PY
timeout --signal=TERM --kill-after=5s 120s "$mysqld" --no-defaults --initialize-insecure \
  --basedir="$mysql_root" --datadir="$fixture_tmp/data" --tmpdir="$fixture_tmp/tmp" \
  --lc-messages-dir="$messages_dir" --plugin-dir="$plugin_dir" \
  >"$fixture_tmp/initialize.log" 2>&1 &
mysql_pid=$!
initialization_result=0
wait "$mysql_pid" || initialization_result=$?
if [[ $initialization_result != 0 ]]; then
  diagnostic_codes
  fail "Native MySQL initialization failed (exit $initialization_result); no existing database was used."
fi
mysql_pid=''
mysql_port=$(python3 - <<'PY'
import socket
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
    listener.bind(("127.0.0.1", 0))
    print(listener.getsockname()[1])
PY
)
"$mysqld" --no-defaults --basedir="$mysql_root" --datadir="$fixture_tmp/data" \
  --lc-messages-dir="$messages_dir" --plugin-dir="$plugin_dir" \
  --tmpdir="$fixture_tmp/tmp" --socket="$socket_path" --pid-file="$fixture_tmp/mysql.pid" \
  --log-error="$fixture_tmp/mysql.log" --init-file="$fixture_tmp/bootstrap.sql" \
  --skip-networking --bind-address=127.0.0.1 --port="$mysql_port" --mysqlx=0 --skip-name-resolve \
  --skip-log-bin --local-infile=0 --secure-file-priv=NULL --default-time-zone=+00:00 \
  --innodb-buffer-pool-size=128M >"$fixture_tmp/server.log" 2>&1 &
mysql_pid=$!
ready=false
readiness_deadline=$((SECONDS + 45))
while ((SECONDS < readiness_deadline)); do
  if ! running_job "$mysql_pid"; then
    diagnostic_codes
    fail 'The owned native MySQL process exited before readiness.'
  fi
  if actual_version=$(MYSQL_PWD="$fixture_password" timeout --signal=TERM --kill-after=1s 2s \
    "$mysql_root/bin/mysql" --no-defaults --no-login-paths \
    --protocol=SOCKET --socket="$socket_path" --user=aster_ci \
    --connect-timeout=1 --batch --skip-column-names --execute='SELECT VERSION()' 2>"$fixture_tmp/readiness.log"); then
    [[ $actual_version == "$mysql_version" ]] || fail 'Running MySQL version does not match the reviewed manifest.'
    ready=true
    break
  fi
  sleep 1
done
if [[ $ready != true ]]; then
  diagnostic_codes
  fail 'Native MySQL did not become ready within the bounded startup wait.'
fi
running_job "$mysql_pid" || fail 'The owned native MySQL exited; refusing to run tests against another process.'
rm -- "$fixture_tmp/bootstrap.sql"
printf 'Native MySQL %s ready through its private Unix socket.\n' "$mysql_version"
# Unix sockets are secure local transport for caching_sha2_password. Use the
# existing localhost fixture account without changing authentication or TLS rules.
export ASTER_MYSQL_TEST_DSN="aster_ci:$fixture_password@unix($socket_path)/aster_ci_test?parseTime=true"
printf '[client]\nuser=aster_ci\npassword=%s\n' "$fixture_password" >"$fixture_tmp/admin.cnf"
export ASTER_NATIVE_MYSQL_ROOT="$mysql_root"
export ASTER_NATIVE_MYSQL_SOCKET="$socket_path"
export ASTER_NATIVE_MYSQL_ADMIN_CNF="$fixture_tmp/admin.cnf"
export ASTER_NATIVE_MYSQL_FIXTURE_ROOT="$fixture_tmp"
export ASTER_NATIVE_MYSQL_PORT="$mysql_port"
# Keep a session leader alive until cleanup even when the command exits before
# its descendants. It anchors the process-group ID, so cleanup cannot signal a
# reused PID/group. On TERM it waits until group members stop; the shell applies
# a bounded KILL to the still-owned leader/group if a descendant ignores TERM.
setsid --wait -- python3 - "$fixture_tmp/command-status" "$@" <<'PY' &
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

stopping = False

def stop(*_):
    global stopping
    stopping = True

signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
process = subprocess.Popen(sys.argv[2:])
while process.poll() is None and not stopping:
    time.sleep(0.05)
if process.returncode is not None:
    code = process.returncode if process.returncode >= 0 else 128 - process.returncode
    status = Path(sys.argv[1])
    pending = status.with_suffix(".pending")
    pending.write_text(str(code), encoding="ascii")
    pending.replace(status)
while not stopping:
    time.sleep(0.05)
while True:
    process.poll()
    live_members = False
    for entry in Path("/proc").iterdir():
        if not entry.name.isdecimal() or int(entry.name) == os.getpid():
            continue
        try:
            # comm may contain spaces/parentheses; fields after its final ')' are
            # state, ppid, process group. Zombies no longer execute or hold files.
            fields = (entry / "stat").read_text().rsplit(")", 1)[1].split()
            if int(fields[2]) == os.getpgrp() and fields[0] != "Z":
                live_members = True
                break
        except (OSError, ValueError, IndexError):
            continue
    if not live_members:
        break
    time.sleep(0.05)
PY
command_pid=$!
while [[ ! -f $fixture_tmp/command-status ]]; do
  running_job "$command_pid" || fail 'The owned command supervisor exited without a result.'
  sleep 0.1
done
result=$(cat "$fixture_tmp/command-status")
[[ $result =~ ^[0-9]+$ && $result -le 255 ]] || fail 'The owned command returned an invalid status.'
unset ASTER_MYSQL_TEST_DSN
[[ $result == 0 ]] || exit "$result"
running_job "$mysql_pid" || fail 'The owned native MySQL exited while the checks ran.'
printf 'Native MySQL fixture checks passed; stopping the owned server.\n'
