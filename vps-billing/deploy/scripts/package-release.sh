#!/usr/bin/env bash
# Reproducible binary-only kit from a reviewed Git commit. No publication/deployment.
set -euo pipefail
umask 022
export LC_ALL=C TZ=UTC GOENV=off GOFLAGS= GOWORK=off GOTOOLCHAIN=local
usage() {
  echo 'Usage: package-release.sh --commit COMMIT --kind core|smtp --version MAJOR.MINOR.PATCH --output NEW_DIRECTORY' >&2
}
commit_ref= version= output= kind=core
while (($#)); do
  case "$1" in
    --commit|--kind|--version|--output)
      (($# >= 2)) || { usage; exit 2; }
      case "$1" in --commit) commit_ref=$2;; --kind) kind=$2;; --version) version=$2;; --output) output=$2;; esac
      shift 2 ;;
    *) usage; exit 2 ;;
  esac
done
[[ -n "$commit_ref" && -n "$version" && -n "$output" && $kind =~ ^(core|smtp)$ ]] || { usage; exit 2; }
[[ "$version" =~ ^(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})$ ]] || { echo 'Invalid stable semantic version' >&2; exit 2; }
for tool in git tar gzip sha256sum python3 go; do
  command -v "$tool" >/dev/null || { echo "Required tool missing: $tool" >&2; exit 1; }
done
if [[ $kind == core ]]; then
  for tool in node npm; do command -v "$tool" >/dev/null || { echo "Required tool missing: $tool" >&2; exit 1; }; done
fi
tar --version | head -1 | grep -q 'GNU tar' || { echo 'GNU tar is required' >&2; exit 1; }
script_dir=$(cd "$(dirname "$0")" && pwd -P)
repo=$(git -C "$script_dir" rev-parse --show-toplevel)
commit=$(git -C "$repo" rev-parse --verify "${commit_ref}^{commit}")
epoch=$(git -C "$repo" show -s --format=%ct "$commit")
export SOURCE_DATE_EPOCH="$epoch"
output=$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$output")
[[ "$output" != "$repo" && "$output" != "$repo/"* ]] || { echo 'Output must be outside the repository' >&2; exit 1; }
[[ ! -e "$output" ]] || { echo 'Refusing to overwrite output directory' >&2; exit 1; }
for recipe in package-release.sh release-manifest.py; do
  git -C "$repo" show "$commit:vps-billing/deploy/scripts/$recipe" | cmp -s - "$script_dir/$recipe" || {
    echo "Packaging recipe differs from selected commit: $recipe" >&2; exit 1;
  }
done
tmp=$(mktemp -d "${TMPDIR:-/tmp}/aster-release.XXXXXXXX")
trap 'rm -rf "$tmp"' EXIT
snapshot="$tmp/source"
mkdir -p "$snapshot"
git -C "$repo" archive "$commit" -- LICENSE README.md vps-billing | tar -xf - -C "$snapshot"
python3 "$script_dir/release-manifest.py" prune "$snapshot"
build="$snapshot/vps-billing"
# Versions originate in selected source, never an arbitrary release label.
python3 "$script_dir/release-manifest.py" version "$snapshot" "$kind" "$version"
if [[ $kind == core ]]; then bundle_name="aster-core-${version}-linux-amd64-resources"; else bundle_name="aster-smtp-${version}-linux-amd64-resources"; fi
bundle="$tmp/$bundle_name"
binary_root="$tmp/binaries"
mkdir -p "$bundle" "$binary_root"
(
  cd "$build/backend"
  go mod download
  go mod verify
  go list -mod=readonly -m -json all > "$tmp/go-modules.json"
  export CGO_ENABLED=0 GOOS=linux GOARCH=amd64
  if [[ $kind == core ]]; then
    mkdir -p "$binary_root/bin"
    go build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w' -o "$binary_root/bin/aster-api" ./cmd/server
    go build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w' -o "$binary_root/bin/aster-plugin-host" ./cmd/plugin-host
    go version -m "$binary_root/bin/aster-api" > "$tmp/go-build.txt"
    go version -m "$binary_root/bin/aster-plugin-host" > "$tmp/go-host-build.txt"
    # The shipped core cannot import the independent SMTP transport.
    go list -mod=readonly -buildvcs=false -deps ./cmd/server | python3 -c 'import sys; deps=set(sys.stdin.read().splitlines()); sys.exit(1 if "aster/billing/plugins/smtp" in deps or "net/smtp" in deps or "os/exec" in deps else 0)'
  else
    go build -mod=readonly -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$version" -o "$binary_root/plugin" ./plugins/smtp/cmd/plugin
    go version -m "$binary_root/plugin" > "$tmp/go-build.txt"
  fi
)
cp "$snapshot/LICENSE" "$bundle/LICENSE"
if [[ $kind == core ]]; then
  (
    cd "$build/frontend"
    npm ci --no-audit --no-fund
    npm run build
  )
  mkdir -p "$bundle/web" "$bundle/manifests" "$bundle/licenses" "$bundle/deploy/scripts"
  cp -R "$build/frontend/dist/." "$bundle/web/"
  cp -R "$build/docs" "$bundle/docs"
  mkdir -p "$bundle/migrations"
  cp "$build/backend/migrations/"*.sql "$bundle/migrations/"
  cp -R "$build/deploy/systemd" "$bundle/deploy/systemd"
  cp "$build/deploy/nginx.native.conf" "$bundle/deploy/"
  cp "$build/deploy/scripts/backup-native.sh" "$build/deploy/scripts/plugin-manager.py" "$build/deploy/scripts/update-manager.py" "$build/deploy/scripts/core-manager.py" "$build/deploy/scripts/core-operation.py" "$bundle/deploy/scripts/"
  cp "$snapshot/README.md" "$bundle/REPOSITORY-README.md"
  cp "$build/README.md" "$bundle/PROJECT-README.md"
  cp "$build/backend/go.mod" "$build/backend/go.sum" "$bundle/manifests/"
  cp "$build/frontend/package.json" "$build/frontend/package-lock.json" "$bundle/manifests/"
fi
python3 "$script_dir/release-manifest.py" manifest "$snapshot" "$bundle" "$tmp/go-modules.json" "$tmp/go-build.txt" "$commit" "$version" "$epoch" "$kind" "$tmp/BUILD.json" "$binary_root"
find "$binary_root" -type f -exec chmod 755 {} +
find "$bundle" -type d -exec chmod 755 {} +
find "$bundle" -type f -exec chmod 644 {} +
if [[ $kind == core ]]; then
  chmod 755 "$bundle/deploy/scripts/backup-native.sh" "$bundle/deploy/scripts/plugin-manager.py" "$bundle/deploy/scripts/update-manager.py" "$bundle/deploy/scripts/core-manager.py" "$bundle/deploy/scripts/core-operation.py"
fi
mkdir -p "$(dirname "$output")"
publish=$(mktemp -d "$(dirname "$output")/.aster-package.XXXXXXXX")
trap 'rm -rf "$tmp" "$publish"' EXIT
# Resources contain no ELF executables. Raw binary assets remain independently verifiable.
if [[ $kind == core ]]; then
  cp "$binary_root/bin/aster-api" "$publish/aster-api-${version}-linux-amd64"
  cp "$binary_root/bin/aster-plugin-host" "$publish/aster-plugin-host-${version}-linux-amd64"
else
  cp "$binary_root/plugin" "$publish/aster-smtp-${version}-linux-amd64"
fi
tar --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner --format=ustar -C "$tmp" -cf - "$bundle_name" | gzip -n > "$publish/$bundle_name.tar.gz"
cp "$tmp/BUILD.json" "$publish/BUILD.json"
(
  cd "$publish"
  sha256sum BUILD.json ./aster-* > SHA256SUMS
  sha256sum -c SHA256SUMS
)
mv -T "$publish" "$output"
echo "Release files: $output"
echo "Source commit: $commit; kind: $kind (publication and deployment were not performed)"
