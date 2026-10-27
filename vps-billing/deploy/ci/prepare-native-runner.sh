#!/usr/bin/env bash
# Provision libraries in an explicitly disposable GitHub-hosted Ubuntu runner.
# Database/Nginx packages are never installed as host services by this script.
set +x
set -euo pipefail
umask 077

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
[[ $# == 0 ]] || fail 'This runner preparation takes no arguments.'
[[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted ]] \
  || fail 'Only an ephemeral GitHub-hosted runner is supported.'
[[ $EUID != 0 && $(uname -m) == x86_64 ]] || fail 'Use the unprivileged Linux amd64 runner account.'
[[ -f /etc/os-release ]] || fail 'Missing OS identity.'
. /etc/os-release
[[ $ID == ubuntu ]] || fail 'Only Ubuntu 22.04/24.04 is supported.'
case $VERSION_ID in
  22.04)
    platform_libraries=(libaio1 libssl3 libtirpc3)
    # Jammy's nginx-core 1.18 lacks ssl_reject_handshake. Use the reviewed
    # official stable Jammy package; no third-party APT source is installed.
    nginx_package=nginx
    nginx_package_version='1.30.6-1~jammy'
    nginx_package_size=1224970
    nginx_package_sha256='ddafa6d5decef787c7942e6c4a9c62cf881d7ea598ec42b9997ecbf298e1bde3'
    ;;
  24.04)
    platform_libraries=(libaio1t64 libssl3t64 libtirpc3t64)
    nginx_package=nginx
    ;;
  *) fail 'Only Ubuntu 22.04/24.04 is supported.' ;;
esac
[[ -n ${GITHUB_ENV:-} && -f $GITHUB_ENV && -w $GITHUB_ENV ]] || fail 'Missing job environment output.'
for command in sudo apt-get dpkg-deb sha256sum curl; do
  command -v "$command" >/dev/null || fail "Missing $command."
done

# The hosted image may make its installed Chrome tree shared-writable. Verify
# protected dpkg records and remove only those write bits before any browser use.
# This does not install Chrome, restore set-ID bits, or change sandbox policy.
controller_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
sudo env GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=github-hosted \
  /usr/bin/python3 -I "$controller_dir/prepare-browser.py"

# /run is visible to the hardened API; PrivateTmp and ProtectHome intentionally
# hide ordinary temporary/home directories. Never reuse an existing path.
fixture_root=$(sudo mktemp -d /run/aster-native.XXXXXXXX)
[[ $fixture_root =~ ^/run/aster-native\.[A-Za-z0-9]{8}$ ]] || fail 'Unexpected scratch path.'
sudo chown -- "$(id -u):$(id -g)" "$fixture_root"
printf 'ASTER_NATIVE_FIXTURE_ROOT=%s\n' "$fixture_root" >>"$GITHUB_ENV"
mkdir "$fixture_root/packages" "$fixture_root/nginx"

apt_security=(-o Acquire::Retries=0 -o APT::Get::AllowUnauthenticated=false
  -o Acquire::AllowInsecureRepositories=false -o Acquire::AllowDowngradeToInsecureRepositories=false)
sudo apt-get "${apt_security[@]}" update
# These are runtime libraries/tools and browser-test fonts only. Do not install or upgrade MySQL or
# Nginx services, change repositories, disable signature checks or run a daemon.
sudo env DEBIAN_FRONTEND=noninteractive apt-get "${apt_security[@]}" install --yes --no-install-recommends \
  "${platform_libraries[@]}" libmecab2 libnuma1 libtinfo6 \
  libgssapi-krb5-2 libkrb5-3 libsasl2-2 libudev1 libpcre2-8-0 openssl fonts-noto-cjk
if [[ $VERSION_ID == 22.04 ]]; then
  nginx_filename="nginx_${nginx_package_version}_amd64.deb"
  nginx_path="$fixture_root/packages/$nginx_filename"
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 20 --max-time 180 --max-filesize "$nginx_package_size" \
    "https://nginx.org/packages/ubuntu/pool/nginx/n/nginx/$nginx_filename" --output "$nginx_path"
  [[ $(stat -c %s -- "$nginx_path") == "$nginx_package_size" ]] || fail 'Pinned Jammy Nginx size mismatch.'
  printf '%s  %s\n' "$nginx_package_sha256" "$nginx_path" | sha256sum --check --status \
    || fail 'Pinned Jammy Nginx SHA256 mismatch; refusing metadata/extraction/execution.'
else
  (
    cd "$fixture_root/packages"
    apt-get "${apt_security[@]}" download "$nginx_package"
  )
fi
shopt -s nullglob
packages=("$fixture_root/packages/"*.deb)
[[ ${#packages[@]} == 1 ]] || fail 'Expected one reviewed Nginx package.'
package=${packages[0]}
[[ $(dpkg-deb --field "$package" Package) == "$nginx_package" ]] || fail 'Unexpected Nginx package identity.'
[[ $(dpkg-deb --field "$package" Architecture) == amd64 ]] || fail 'Unexpected Nginx package architecture.'
if [[ $VERSION_ID == 22.04 ]]; then
  [[ $(dpkg-deb --field "$package" Version) == "$nginx_package_version" ]] || fail 'Pinned Jammy Nginx version mismatch.'
fi
dpkg-deb --extract "$package" "$fixture_root/nginx"
nginx_binary="$fixture_root/nginx/usr/sbin/nginx"
[[ -f $nginx_binary && ! -L $nginx_binary && -x $nginx_binary ]] || fail 'Nginx executable missing.'
"$nginx_binary" -v
sha256sum "$package"
printf 'ASTER_ACCEPT_NGINX_BIN=%s\n' "$nginx_binary" >>"$GITHUB_ENV"
printf 'PASS: authenticated Ubuntu libraries and reviewed isolated Nginx executable prepared.\n'

# Exercise the actual host's typed systemd properties before the long database
# suites. These two root-owned fixtures are loaded for inspection, never started.
# The source module is imported only by the ordinary runner Python interpreter.
python3 -I - "$controller_dir/../scripts/core-operation.py" "$fixture_root" <<'PYHOOKSMOKE'
import errno
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import stat
import struct
import subprocess
import sys
import tempfile

def require(ok, reason):
    if not ok:
        raise RuntimeError(reason)

def run(arguments, allowed=(0,)):
    result = subprocess.run(arguments, cwd='/', stdin=subprocess.DEVNULL,
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=20)
    require(result.returncode in allowed and len(result.stdout) <= 16384, 'fixed_smoke_command_failed')
    return result.stdout

require(os.geteuid() != 0 and os.environ.get('GITHUB_ACTIONS') == 'true' and
        os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted' and
        os.environ.get('GITHUB_REPOSITORY') == 'xiaoqiqixi/betteridcsys-backend', 'isolated_runner_required')
identity = os.environ.get('GITHUB_RUN_ID', '') + '-' + os.environ.get('GITHUB_RUN_ATTEMPT', '')
require(re.fullmatch(r'[0-9]+-[0-9]+', identity) is not None, 'run_identity_required')
source = Path(sys.argv[1]).resolve()
spec = importlib.util.spec_from_file_location('native_prepare_hook_smoke', source)
module = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = module
spec.loader.exec_module(module)

def private_backup_acl_smoke(module, fixture_root):
    report = {'passed': False, 'phase': 'create-owned-private-root', 'acl_supported': False,
              'legacy_file_mode': None, 'private_file_mode': None,
              'new_directory_mode': None, 'new_default_acl_absent': None,
              'plain_private_creation_passed': False, 'owned_paths_removed': False,
              'global_acl_mutations': False, 'umask_restored': False}
    root = None
    root_identity = None
    previous_umask = os.umask(0o077)
    try:
        root = Path(tempfile.mkdtemp(prefix='aster-backup-acl-', dir=fixture_root))
        root_identity = root.lstat()
        require(root_identity.st_uid == os.geteuid() and stat.S_ISDIR(root_identity.st_mode) and
                stat.S_IMODE(root_identity.st_mode) == 0o700, 'private_acl_root_metadata_mismatch')
        parent = root / 'parent'
        parent.mkdir(mode=0o700)
        parent.chmod(0o700)
        default_acl = struct.pack('<I', 2) + b''.join(struct.pack('<HHI', tag, 7, 0xffffffff) for tag in (1, 4, 32))
        report['phase'] = 'seed-owned-parent-default-acl'
        try:
            os.setxattr(parent, 'system.posix_acl_default', default_acl, follow_symlinks=False)
            require(os.getxattr(parent, 'system.posix_acl_default', follow_symlinks=False) == default_acl,
                    'private_acl_seed_not_observed')
            report['acl_supported'] = True
            report['acl_seed_byte_count'] = len(default_acl)
            report['acl_seed_version'] = 2
        except OSError as failure:
            require(failure.errno in {errno.EOPNOTSUPP, errno.ENOTSUP}, 'private_acl_seed_failed')
        parent.chmod(0o700)
        def create_mode(path):
            descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o666)
            try:
                return stat.S_IMODE(os.fstat(descriptor).st_mode)
            finally:
                os.close(descriptor)
        if report['acl_supported']:
            report['phase'] = 'observe-real-legacy-inheritance'
            legacy = parent / 'legacy'
            legacy.mkdir(mode=0o700)
            legacy.chmod(0o700)
            report['legacy_file_mode'] = oct(create_mode(legacy / 'probe'))
            require(report['legacy_file_mode'] == '0o666', 'private_acl_legacy_inheritance_not_reproduced')
        report['phase'] = 'verify-actual-private-backup-helper'
        target = parent / 'private'
        module.create_backup_directory(str(target), module.updater._Trust(owner=os.geteuid(), ancestor=0))
        report['new_directory_mode'] = oct(stat.S_IMODE(target.lstat().st_mode))
        try:
            os.getxattr(target, 'system.posix_acl_default', follow_symlinks=False)
            report['new_default_acl_absent'] = False
        except OSError as failure:
            if failure.errno == errno.ENODATA:
                report['new_default_acl_absent'] = True
            else:
                require(not report['acl_supported'] and failure.errno in {errno.EOPNOTSUPP, errno.ENOTSUP},
                        'private_acl_absence_not_verified')
        report['private_file_mode'] = oct(create_mode(target / 'probe'))
        report['plain_private_creation_passed'] = report['new_directory_mode'] == '0o700' and report['private_file_mode'] == '0o600'
        require(report['plain_private_creation_passed'] and
                (not report['acl_supported'] or report['new_default_acl_absent'] is True), 'private_acl_helper_not_private')
        report['passed'] = True
        report['phase'] = 'complete'
    except Exception:
        # The fixed phase and observed modes are public; exceptions stay private.
        pass
    finally:
        current_umask = os.umask(previous_umask)
        report['umask_restored'] = True
        report['umask_unchanged_during_smoke'] = current_umask == 0o077
        try:
            if root is not None and root_identity is not None:
                now = root.lstat()
                require((now.st_dev, now.st_ino, now.st_uid) == (root_identity.st_dev, root_identity.st_ino, os.geteuid()) and
                        stat.S_ISDIR(now.st_mode) and stat.S_IMODE(now.st_mode) == 0o700,
                        'private_acl_cleanup_refuses_changed_root')
                shutil.rmtree(root)
                report['owned_paths_removed'] = not os.path.lexists(root)
        except Exception:
            report['owned_paths_removed'] = False
        report['passed'] = report['passed'] and report['owned_paths_removed'] and report['umask_unchanged_during_smoke']
        print('NATIVE_PRIVATE_BACKUP_ACL_SMOKE_JSON=' + json.dumps(report, sort_keys=True), flush=True)
    require(report['passed'], 'actual_private_backup_acl_smoke_failed')

private_backup_acl_smoke(module, sys.argv[2])
diagnostics = {'calls': [], 'last_call': 'not-started', 'rejection': 'none'}
def safe_reply_shape(raw, call, unit):
    report = {'json_valid': False}
    if len(raw) > 8192:
        report['exceeded_json_bound'] = True
        return report
    try:
        value = json.loads(raw)
    except (ValueError, UnicodeError):
        return report
    report['json_valid'] = True
    report['object'] = isinstance(value, dict)
    if not isinstance(value, dict):
        return report
    report['type_key_present'] = 'type' in value
    report['data_key_present'] = 'data' in value
    report['keys_exact'] = set(value) == {'type', 'data'}
    kind = value.get('type')
    report['type_is_string'] = isinstance(kind, str)
    report['type'] = kind if kind in ('s', 'a(sasbttttuii)', 'v') else 'other_or_absent'
    data = value.get('data')
    report['data_kind'] = 'list' if isinstance(data, list) else 'string' if isinstance(data, str) else 'object' if isinstance(data, dict) else 'other_or_absent'
    if isinstance(data, list):
        report['list_length'] = min(len(data), 10000)
        if len(data) == 1 and isinstance(data[0], list):
            report['nested_single_list_length'] = min(len(data[0]), 10000)
    if call == 'unit-id':
        report['identity_matches'] = data == unit
    return report

class ObservedNative(module.Native):
    def run(self, argv, env=None, data=None, timeout=90, allow_failure=False, identity=None):
        call = 'unit-id' if argv[-1] == 'Id' else argv[-1] if argv[-1] in module.SERVICE_HOOKS else 'other-fixed-native-call'
        diagnostics['last_call'] = call
        result = super().run(argv, env=env, data=data, timeout=timeout, allow_failure=True, identity=identity)
        diagnostics['calls'] = (diagnostics['calls'] + [{
            'call': call, 'exit_code': result.returncode if -255 <= result.returncode <= 255 else None,
            'shape': safe_reply_shape(result.stdout, call, self.spec['api_unit'])}])[-16:]
        module.require(allow_failure or result.returncode == 0, 'A fixed native operation failed; raw diagnostics were suppressed.')
        return result

try:
    info = Path('/usr/bin/busctl').lstat()
    diagnostics['busctl_metadata'] = {'uid': info.st_uid, 'gid': info.st_gid,
        'mode': oct(stat.S_IMODE(info.st_mode)), 'nlink': info.st_nlink,
        'regular_file': stat.S_ISREG(info.st_mode)}
except OSError:
    diagnostics['busctl_metadata_unavailable'] = True
units = [Path('/run/systemd/system') / ('aster-property-ci-' + identity + suffix + '.service')
         for suffix in ('-empty', '-hook')]
require(all(not os.path.lexists(path) for path in units), 'refuse_existing_smoke_units')
created = []
passed = False
empty_verified = 0
rejected = False
cleanup_ok = False
phase = 'install-owned-units'
try:
    with tempfile.TemporaryDirectory(prefix='aster-property-', dir=sys.argv[2]) as scratch:
        for number, target in enumerate(units):
            text = '[Unit]\nDescription=Aster isolated property inspection\n[Service]\nType=oneshot\n'
            if number:
                text += 'ExecStartPre=/usr/bin/true\n'
            text += 'ExecStart=/usr/bin/true\n'
            local = Path(scratch) / target.name
            local.write_text(text)
            created.append((target, text.encode()))
            run(['sudo', '-n', '/usr/bin/install', '-o', 'root', '-g', 'root', '-m', '644', str(local), str(target)])
            info = target.lstat()
            require(stat.S_ISREG(info.st_mode) and info.st_uid == info.st_gid == 0 and
                    stat.S_IMODE(info.st_mode) == 0o644 and info.st_nlink == 1 and target.read_bytes() == text.encode(),
                    'smoke_unit_metadata_mismatch')
        run(['sudo', '-n', '/usr/bin/systemctl', 'daemon-reload'])
        def inactive(target):
            # Read fixture state without starting it; typed properties load metadata independently.
            raw = run(['/usr/bin/systemctl', 'show', '--all', target.name,
                       '--property=LoadState', '--property=ActiveState', '--property=MainPID'])
            values = dict(line.split('=', 1) for line in raw.decode('ascii').splitlines() if '=' in line)
            require(values == {'LoadState': 'loaded', 'ActiveState': 'inactive', 'MainPID': '0'},
                    'smoke_fixture_not_inactive')
        for target in units:
            inactive(target)
        expected = set(module.SERVICE_HOOKS)
        phase = 'verify-empty-typed-hooks'
        observed = ObservedNative({'api_unit': units[0].name}).verify_empty_service_hooks()
        require(isinstance(observed, dict) and set(observed) == expected and all(value is True for value in observed.values()),
                'actual_empty_typed_hook_properties_not_verified')
        empty_verified = len(expected)
        phase = 'reject-nonempty-typed-hook'
        try:
            ObservedNative({'api_unit': units[1].name}).verify_empty_service_hooks()
        except module.Rejected as failure:
            last = diagnostics['calls'][-1] if diagnostics['calls'] else {}
            shape = last.get('shape', {})
            rejected = (str(failure) == 'Typed systemd service hooks are missing, nonempty or malformed.' and
                        last.get('call') == 'ExecStartPre' and last.get('exit_code') == 0 and
                        shape.get('type') == 'a(sasbttttuii)' and shape.get('data_kind') == 'list' and
                        shape.get('list_length', 0) > 0)
        require(rejected, 'actual_nonempty_typed_hook_was_accepted')
        for target in units:
            inactive(target)
        passed = True
        phase = 'complete'
except Exception as failure:
    # Only the bounded phase/result below is public, never exception bodies.
    diagnostics['rejection'] = {
        'A fixed native operation failed; raw diagnostics were suppressed.': 'fixed_native_command_failed',
        'Typed systemd unit identity does not match the reviewed service.': 'unit_identity_shape_or_value_mismatch',
        'Typed systemd service hooks are missing, nonempty or malformed.': 'hook_array_missing_nonempty_or_malformed',
    }.get(str(failure), 'other_fixed_or_suppressed_failure')
finally:
    # Only the two newly installed fixed-text paths are eligible for removal.
    cleanup_failures = 0
    for target, expected in created:
        try:
            if os.path.lexists(target):
                info = target.lstat()
                require(stat.S_ISREG(info.st_mode) and info.st_uid == info.st_gid == 0 and
                        stat.S_IMODE(info.st_mode) == 0o644 and info.st_nlink == 1 and
                        info.st_size == len(expected) and target.read_bytes() == expected,
                        'smoke_cleanup_refuses_changed_unit')
                run(['sudo', '-n', '/usr/bin/rm', '--', str(target)])
        except Exception:
            cleanup_failures += 1
    if created:
        try:
            run(['sudo', '-n', '/usr/bin/systemctl', 'daemon-reload'])
        except Exception:
            cleanup_failures += 1
    cleanup_ok = cleanup_failures == 0 and all(not os.path.lexists(target) for target, _ in created)
    print('NATIVE_SERVICE_HOOK_SMOKE_JSON=' + json.dumps({
        'passed': passed and cleanup_ok, 'phase': phase,
        'typed_empty_hook_properties_verified': empty_verified,
        'nonempty_hook_rejected': rejected, 'fixture_services_started': False,
        'owned_unit_files_removed': cleanup_ok, 'diagnostics': diagnostics}, sort_keys=True), flush=True)
    require(cleanup_ok, 'owned_smoke_fixture_cleanup_failed')
require(passed, 'actual_service_hook_smoke_failed')
PYHOOKSMOKE
