"""Native main CI authorization, source pairing and bounded evidence contracts."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / '.github/workflows/ci.yml'


def job(name):
    return re.search(r'^  ' + re.escape(name) + r':\n(.*?)(?=^  [a-z][a-z-]*:|\Z)',
                     WORKFLOW.read_text(), re.MULTILINE | re.DOTALL).group(1)


class NativeWorkflow(unittest.TestCase):
    def test_main_evidence_completes_while_new_pushes_wait(self):
        for name in ('ci.yml', 'release-tooling.yml'):
            with self.subTest(workflow=name):
                workflow = (WORKFLOW.parent / name).read_text()
                concurrency = workflow.split('concurrency:\n', 1)[1].split('\njobs:', 1)[0]
                self.assertIn('group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}', concurrency)
                condition = re.search(r'^  cancel-in-progress: (.+)$', concurrency, re.MULTILINE).group(1)
                self.assertEqual(condition, "${{ github.event_name == 'pull_request' }}")

    def test_both_jobs_require_the_official_main_push_and_read_only_token(self):
        for name in ('native-financial', 'financial-performance'):
            source = job(name)
            guard = next(line.strip() for line in source.splitlines() if line.strip().startswith('if:'))
            self.assertEqual(guard, "if: github.repository == 'xiaoqiqixi/betteridcsys-backend' && github.repository_id == '1435344087' && github.event_name == 'push' && github.ref == 'refs/heads/main'")
            self.assertNotIn(': write', source)
            self.assertNotIn('continue-on-error:', source)
            self.assertIn('persist-credentials: false', source)
            self.assertIn('if: always()', source)
            self.assertIn('sudo rm -rf --one-file-system -- "$ASTER_NATIVE_FIXTURE_ROOT"', source)
        self.assertIn('  push:\n    branches: [main]', WORKFLOW.read_text())

    def test_acceptance_runs_both_platforms_and_preserves_the_full_legacy_gate(self):
        source = job('native-financial')
        self.assertIn('name: Native financial acceptance (${{ matrix.os }})', source)
        self.assertIn('os: [ubuntu-22.04, ubuntu-24.04]', source)
        self.assertIn('fail-fast: false', source)
        self.assertIn('scripts/frontend_pair.py pin', source)
        self.assertIn('ref: ${{ steps.pair.outputs.source_sha }}', source)
        self.assertIn('repository: xiaoqiqixi/betteridcsys-frontend', source)
        self.assertIn('bash scripts/check-paired-frontend.sh', source)
        self.assertIn('mysql-native.sh -- bash vps-billing/deploy/ci/accept-native.sh --allow-isolated-runner', source)
        self.assertIn('vars.ASTER_INSTALLED_LEGACY_BASELINE', source)
        self.assertIn('exit 1', source.split('      - name: Resolve', 1)[0])
        self.assertNotIn('releases/download', source)
        self.assertNotIn('if: env.ASTER_INSTALLED_LEGACY_BASELINE', source)
        artifact = source.split('      - name: Retain only bounded native evidence', 1)[1].split('      - name: Remove', 1)[0]
        self.assertNotIn('.log', artifact)
        self.assertNotIn('**', artifact)
        self.assertIn('native-acceptance/core-native/core-native.json', artifact)
        self.assertIn('native-acceptance/cleanup.json', artifact)

    def test_performance_has_a_real_fixture_and_preserves_failure_and_cleanup_status(self):
        source = job('financial-performance')
        self.assertIn('name: Synthetic financial performance (ubuntu-24.04)', source)
        self.assertIn('mysql-native.sh -- bash vps-billing/deploy/ci/performance-native.sh --allow-isolated-runner', source)
        self.assertIn('timeout-minutes: 25', source)
        self.assertIn('result=$?', source)
        self.assertIn('scripts/native_fixture_report.py', source)
        self.assertIn('--exit-code "$result"', source)
        self.assertNotIn('|| true', source)
        self.assertIn('go test -race -count=1 -timeout=3m', source)
        self.assertIn('TestMySQLHTTPPerformanceSeedUsesLiveStaffMembershipAndCurrentSchema', source)
        self.assertIn('TestMigrationDispatchKeepsInternalZeroVersionOutOfSourceLookup', source)


if __name__ == '__main__':
    unittest.main()
