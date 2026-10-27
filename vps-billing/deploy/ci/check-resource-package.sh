#!/usr/bin/env bash
# Native Linux acceptance: build a resource package, then verify (never install).
# The compiler runs as the caller; only the bounded /run verification uses root.
set +x
set -euo pipefail
umask 077
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
[[ $# == 1 ]] || fail 'Usage: check-resource-package.sh source-repository-root'
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'Resource package acceptance requires native Linux amd64; not executed on this platform.'
[[ -d $1 && ! -L $1 ]] || fail 'Source root must be an existing directory, not a symlink.'
source_root=$(cd -- "$1" && pwd -P)
project_root="$source_root/vps-billing"
for file in LICENSE vps-billing/backend/go.mod vps-billing/docs/RESOURCE_PLUGINS.md vps-billing/deploy/scripts/build-resource-plugin.py vps-billing/deploy/scripts/plugin-manager.py vps-billing/deploy/ci/resource_package_gate.py; do
  [[ -f "$source_root/$file" && ! -L "$source_root/$file" ]] || fail "Missing regular source prerequisite: $file"
done
[[ -x /usr/bin/python3 && -x /usr/bin/mktemp && -x /usr/bin/rm ]] || fail 'System Python3, mktemp and rm are required.'
go_tool=$(command -v go) || fail 'Go is required for the native static resource build.'
[[ -f $go_tool && -x $go_tool ]] || fail 'Go must resolve to an executable file.'
[[ $("$go_tool" env GOHOSTOS GOHOSTARCH) == $'linux\namd64' ]] || fail 'Go must be a native Linux amd64 toolchain.'
privilege=()
if [[ $(id -u) != 0 ]]; then
  [[ -x /usr/bin/sudo ]] || fail 'Non-root CI requires system sudo for protected package verification.'
  /usr/bin/sudo -n -- /usr/bin/true || fail 'Noninteractive sudo is required; package verification was not executed.'
  privilege=(/usr/bin/sudo -n --)
fi
# Fixed temporary parent avoids inherited TMPDIR redirection and unsafe cleanup.
build_dir=$(/usr/bin/mktemp -d /tmp/aster-resource-package-build.XXXXXXXX)
[[ $build_dir =~ ^/tmp/aster-resource-package-build\.[[:alnum:]]{8}$ && -d $build_dir && ! -L $build_dir ]] || fail 'Unexpected build directory.'
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [[ $build_dir =~ ^/tmp/aster-resource-package-build\.[[:alnum:]]{8}$ && -d $build_dir && ! -L $build_dir && -O $build_dir ]]; then
    /usr/bin/rm -rf -- "$build_dir" || result=1
  else
    printf 'FAIL: refusing cleanup of replaced build directory.\n' >&2
    result=1
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
package="$build_dir/resource.tar.gz"
# No inherited database secret is needed by either build or privileged verify.
unset ASTER_MYSQL_TEST_DSN
/usr/bin/python3 -I "$project_root/deploy/scripts/build-resource-plugin.py" --go "$go_tool" --output "$package"
manager="$project_root/deploy/scripts/plugin-manager.py"
package_sha=$(/usr/bin/python3 -I -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$package")
manager_sha=$(/usr/bin/python3 -I -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$manager")
"${privilege[@]}" /usr/bin/python3 -I "$project_root/deploy/ci/resource_package_gate.py" "$package" "$package_sha" "$manager" "$manager_sha"
printf 'PASS: native static resource package built and production-verified; no installation or service action.\n'
