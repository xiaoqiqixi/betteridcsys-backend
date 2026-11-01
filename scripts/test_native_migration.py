#!/usr/bin/env python3
"""Migration guards, local CLI shape, and absence of legacy network fallbacks."""
from pathlib import Path
import os
import subprocess
import unittest

ROOT = Path(__file__).resolve().parent.parent
CI = ROOT / 'vps-billing/deploy/ci'


class NativeMigration(unittest.TestCase):
    def test_all_privileged_entrypoints_pin_both_repository_name_and_id(self):
        for name in ('core-native.sh', 'accept-native.sh', 'payment-native.sh', 'plugin-native.sh', 'performance-native.sh'):
            text = (CI / name).read_text()
            start = text.index('[[ ${GITHUB_REPOSITORY:-}')
            end = text.index('\n', start)
            if text[end-1] == '\\':
                end = text.index('\n', end+1)
            guard = text[start:end]
            for repo, identifier, accepted in [('xiaoqiqixi/betteridcsys-backend', '1435344087', True),
                    ('xiaoqiqixi/betteridcsys', '1418618420', False), ('xiaoqiqixi/betteridcsys-backend', '1418618420', False),
                    ('attacker/betteridcsys-backend', '1435344087', False)]:
                with self.subTest(name=name, repo=repo, id=identifier):
                    result = subprocess.run(['bash', '-c', 'fail() { exit 9; };\n' + guard],
                        env={**os.environ, 'GITHUB_REPOSITORY': repo, 'GITHUB_REPOSITORY_ID': identifier, 'GITHUB_REF': 'refs/heads/main'},
                        capture_output=True)
                    self.assertEqual(result.returncode, 0 if accepted else 9)
            self.assertIn('$1 == --check', text)
            self.assertEqual(subprocess.run(['bash','-n',str(CI/name)],capture_output=True).returncode,0)

    def test_legacy_baselines_are_local_only_and_candidate_uses_bundle_contract(self):
        acceptance = (CI / 'accept-native.sh').read_text()
        controller = (CI / 'core-native.py').read_text()
        self.assertIn('ASTER_INSTALLED_LEGACY_BASELINE', acceptance)
        self.assertIn('--clone-installed-baseline', acceptance)
        self.assertNotIn('core-manager.py" prepare --version', acceptance)
        self.assertNotIn('executor.manager.prepare(PUBLISHED_VERSION', controller)
        self.assertIn('executor.prepared_release(str(source))', controller)
        self.assertIn('verify_published_baseline(source)', controller)
        self.assertIn('assemble-candidate', acceptance)
        packaging = (ROOT / 'vps-billing/deploy/scripts/package-release.sh').read_text()
        core = packaging.split('if [[ $kind == core ]]; then',1)[1].split('exit 0',1)[0]
        self.assertIn('backend_bundle.py', core)
        self.assertIn('fetch-frontend', core)
        self.assertNotIn('npm', core)
        self.assertNotIn('/frontend/', core.replace('$output/frontend/', ''))

    def test_check_uses_paired_checkout_policy_without_frontend_source_layout(self):
        check = (CI / 'check.sh').read_text()
        helper = (ROOT / 'scripts/check-paired-frontend.sh').read_text()
        self.assertIn('scripts/check-paired-frontend.sh',check)
        self.assertNotIn('frontend/package-lock.json',check)
        self.assertNotIn('npm run',check)
        self.assertIn('frontend_pair.py" verify',helper)
        self.assertIn('release_bundle.py" fetch-frontend',helper)


if __name__ == '__main__':
    unittest.main()
