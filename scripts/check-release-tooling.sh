#!/usr/bin/env bash
# Unprivileged tests; no database, host changes or publication.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
export PYTHONDONTWRITEBYTECODE=1
python3 -B "$root/.github/tests/test_native_workflow.py"
python3 -B "$root/scripts/test_native_fixture_report.py"
python3 -B "$root/vps-billing/deploy/ci/test_performance_runner.py"
python3 -B "$root/vps-billing/deploy/ci/test_performance_report.py"
python3 -B "$root/scripts/test_frontend_pair.py"
python3 -B "$root/scripts/test_backend_bundle.py"
python3 -B "$root/scripts/test_pair_runtime.py"
python3 -B "$root/scripts/test_release_bundle.py"
python3 -B "$root/scripts/test_paired_install.py"
python3 -B "$root/scripts/test_native_migration.py"
# Historical cross-component docs/browser suites remain in check.sh; these are
# the independent release and host-tool regression suites.
python3 -B -m unittest discover -s "$root/vps-billing/deploy/ci" -p 'test_runtime_release.py'
python3 -B -m unittest discover -s "$root/vps-billing/deploy/ci" -p 'test_check.py'
for name in accept_native plugin_native payment_native; do
  python3 -B -m unittest discover -s "$root/vps-billing/deploy/ci" -p "test_$name.py"
done
for name in release-manifest plugin-manager update-manager core-manager core-operation; do
  python3 -B "$root/vps-billing/deploy/scripts/test-$name.py"
done
(cd "$root/vps-billing/backend" && go test -race -count=1 ./internal/updates/...)
