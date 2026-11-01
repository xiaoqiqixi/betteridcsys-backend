#!/usr/bin/env python3
"""Linux unprivileged host-preparation tests with a synthetic official API."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import sys
import unittest

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('paired_install_fixtures', ROOT / 'scripts/test_release_bundle.py')
fixtures = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixtures)
controls = fixtures.load('paired_install_controls', ROOT / 'vps-billing/deploy/scripts/core-operation.py')
manager, updater = controls.manager, controls.updater


class PairedHostPreparation(unittest.TestCase):
    def setUp(self):
        self.case = fixtures.ReleasePolicy()
        self.case.setUp()
        self.addCleanup(self.case.doCleanups)
        self.api = self.case.api
        self.original_json = self.api.json.side_effect
        self.assets = self.case.intent['assets']
        self.release = dict(id=9, tag_name='v0.5.10', draft=False, prerelease=False, target_commitish=self.case.source,
                            assets=[dict(name=a['name'], id=a['id'], size=a['size'], state='uploaded', digest='sha256:'+a['sha256']) for a in self.assets])
        def request(path):
            if path == '/releases/tags/v0.5.10': return self.release
            if path == '/git/ref/tags/v0.5.10': return dict(ref='refs/tags/v0.5.10', object=dict(type='commit',sha=self.case.source))
            if path == '/compare/'+self.case.source+'...main': return dict(status='identical', merge_base_commit=dict(sha=self.case.source))
            return self.original_json(path)
        self.api.json.side_effect = request
        self.api.repository.return_value = dict(id=1435344087, full_name=updater.REPOSITORY, default_branch='main')
        self.api.asset.side_effect = lambda identifier, limit: self.case.files[next(a['name'] for a in self.assets if a['id']==identifier)]
        self.trust = updater._Trust(os.getuid(), 0)
        self.target = str(self.case.fixture.base / 'prepared')

    def test_preparation_and_executor_recheck_the_pair_without_activation(self):
        result = manager.prepare('0.5.10', self.target, api=self.api, trust=self.trust)
        self.assertTrue(result['independent_publisher_signature'])
        self.assertFalse(result['active_release_changed'])
        self.assertFalse(result['program_executed'])
        actual = controls.prepared_release(self.target, self.trust)
        self.assertEqual(actual['source_sha'], self.case.source)
        self.assertIn('web/index.html', actual['files_sha256'])
        receipt = json.loads(Path(self.target, manager.RECEIPT).read_bytes())
        self.assertEqual(receipt['distribution_format'], 'paired-bundles-v1')
        Path(self.target, 'web/index.html').write_bytes(b'tampered')
        with self.assertRaises(updater.Rejected):
            controls.prepared_release(self.target, self.trust)

    def test_failed_signature_never_creates_a_prepared_directory(self):
        self.api.attestation.side_effect = ValueError('synthetic signature failure')
        with self.assertRaises(updater.Rejected):
            manager.prepare('0.5.10', self.target, api=self.api, trust=self.trust)
        self.assertFalse(Path(self.target).exists())

    def test_legacy_repository_handoff_is_refused_without_network(self):
        intent = copy.deepcopy(self.case.intent)
        # Full old-format rejection is also covered by test-update-manager.py.
        self.api.repository.return_value['id'] = 1418618420
        with self.assertRaises(updater.Rejected):
            manager.prepare('0.5.10', self.target, api=self.api, trust=self.trust)
        self.api.asset.assert_not_called()


if __name__ == '__main__':
    unittest.main()
