#!/usr/bin/env bash
# Verify the ci.yml checkout and the same immutable, bounded CI bundle locally.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/aster-paired-check.XXXXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
source=$(git -C "$root" rev-parse HEAD)
gh api repos/xiaoqiqixi/betteridcsys-frontend > "$scratch/repository.json"
python3 -I "$root/scripts/frontend_pair.py" verify --backend-source-sha "$source" --repository-metadata "$scratch/repository.json"
python3 -I "$root/scripts/release_bundle.py" fetch-frontend --output "$scratch/frontend"
