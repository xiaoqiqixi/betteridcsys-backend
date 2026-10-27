#!/usr/bin/env python3
"""Real CLI and disposable Git checkouts; not browser/release acceptance."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

TOOL = Path(__file__).with_name('frontend_pair.py')


class FrontendPairCLI(unittest.TestCase):
    def setUp(self):
        self.case = tempfile.TemporaryDirectory(prefix='finance-pair-test-')
        self.addCleanup(self.case.cleanup)
        self.root = Path(self.case.name).resolve()
        (self.root / 'scripts').mkdir()
        shutil.copyfile(TOOL, self.root / 'scripts/frontend_pair.py')
        self.frontend = self.root / 'vps-billing/frontend'
        self.frontend.mkdir(parents=True)
        protocol = self.root / 'vps-billing/backend/pkg/pluginapi/protocol.go'
        protocol.parent.mkdir(parents=True)
        protocol.write_text('package pluginapi\nconst (\n CoreVersion = "0.5.10"\n)\n')
        self.compatibility = dict(schema=1, core_version='0.5.10', api_contract='/api/v1',
                                  backend_repository='xiaoqiqixi/betteridcsys-backend',
                                  source_commit='b' * 40, defaults=dict(locale='zh-CN', currency='CNY'))
        (self.frontend / 'compatibility.json').write_text(json.dumps(self.compatibility))
        (self.frontend / 'package.json').write_text(json.dumps(dict(name='finance', version='0.5.10')))
        self.init_repo(self.frontend)
        frontend_sha = self.git(self.frontend, 'rev-parse', 'HEAD').strip()
        self.pin = dict(schema=1, repository='xiaoqiqixi/betteridcsys-frontend', repository_id=1435344023,
                        source_sha=frontend_sha, frontend_version='0.5.10', core_version='0.5.10',
                        compatibility_sha256=hashlib.sha256((self.frontend / 'compatibility.json').read_bytes()).hexdigest())
        (self.root / 'FRONTEND_PAIR.json').write_text(json.dumps(self.pin))
        (self.root / '.gitignore').write_text('/vps-billing/frontend/\n')
        self.init_repo(self.root)
        self.backend_sha = self.git(self.root, 'rev-parse', 'HEAD').strip()
        self.metadata = dict(id=1435344023, full_name='xiaoqiqixi/betteridcsys-frontend', private=False,
                             default_branch='main', owner=dict(id=123864154))
        self.metadata_file = self.root / 'repository.json'
        self.metadata_file.write_text(json.dumps(self.metadata))

    def git(self, root, *args):
        result = subprocess.run(['git', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                                 '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=/dev/null', '-C', str(root), *args],
                                text=True, capture_output=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def init_repo(self, root):
        self.git(root, 'init', '-q')
        self.git(root, 'add', '.')
        self.git(root, 'commit', '-qm', 'Disposable source fixture')

    def call(self, action='verify'):
        argv = [sys.executable, '-I', str(self.root / 'scripts/frontend_pair.py'), action]
        if action == 'verify':
            argv += ['--repository-metadata', str(self.metadata_file), '--backend-source-sha', self.backend_sha]
        return subprocess.run(argv, capture_output=True, text=True, timeout=15)

    def test_exact_separate_sources_are_reported_without_claiming_a_release(self):
        result = self.call()
        self.assertEqual(result.returncode, 0, result.stderr)
        value = json.loads(result.stdout)
        self.assertEqual(value['backend_source_sha'], self.backend_sha)
        self.assertEqual(value['frontend_source_sha'], self.pin['source_sha'])
        self.assertIs(value['verified_checkout'], True)
        self.assertIs(value['authenticated_release'], False)
        self.assertEqual(value['defaults'], dict(locale='zh-CN', currency='CNY'))

    def test_pin_is_an_immutable_commit_not_a_branch_url_or_other_repository(self):
        good = self.call('pin')
        self.assertEqual(good.returncode, 0, good.stderr)
        self.assertEqual(good.stdout, 'source_sha=' + self.pin['source_sha'] + '\n')
        for key, value in (('source_sha', 'main'), ('source_sha', 'https://example.invalid/code'),
                           ('repository', 'xiaoqiqixi/betteridcsys'), ('repository_id', True),
                           ('schema', True), ('extra', 'ignored'), ('frontend_version', '0.5.10\nX=1')):
            with self.subTest(key=key, value=value):
                (self.root / 'FRONTEND_PAIR.json').write_text(json.dumps(dict(self.pin, **{key: value})))
                result = self.call('pin')
                self.assertEqual(result.returncode, 1)
                self.assertEqual(result.stdout, '')

    def test_repository_recreation_private_fork_and_wrong_owner_are_rejected(self):
        for key, value in (('id', 1418618420), ('id', True), ('full_name', 'other/frontend'),
                           ('owner', dict(id=1)), ('owner', dict(id=True)), ('private', True),
                           ('private', 0), ('default_branch', 'develop')):
            with self.subTest(key=key, value=value):
                self.metadata_file.write_text(json.dumps(dict(self.metadata, **{key: value})))
                result = self.call()
                self.assertEqual(result.returncode, 1)
                self.assertNotIn('other/frontend', result.stderr)
                self.assertIn('official_repository_identity_mismatch', result.stderr)
                self.assertEqual(result.stdout, '')

    def test_uncommitted_or_unreviewed_frontend_code_is_rejected(self):
        package = self.frontend / 'package.json'
        original = package.read_bytes()
        package.write_text('{"version":"0.5.11"}')
        self.assertEqual(self.call().returncode, 1)
        package.write_bytes(original)
        untracked = self.frontend / 'unreviewed.js'
        untracked.write_text('throw new Error("unreviewed")')
        self.assertEqual(self.call().returncode, 1)
        self.git(self.frontend, 'add', '.')
        self.git(self.frontend, 'commit', '-qm', 'Unpinned source')
        self.assertEqual(self.call().returncode, 1)

    def test_wrong_backend_revision_or_changed_core_version_cannot_pass(self):
        self.backend_sha = 'c' * 40
        self.assertEqual(self.call().returncode, 1)
        self.backend_sha = self.git(self.root, 'rev-parse', 'HEAD').strip()
        protocol = self.root / 'vps-billing/backend/pkg/pluginapi/protocol.go'
        protocol.write_text('package pluginapi\nconst CoreVersion = "0.5.11"\n')
        self.assertEqual(self.call('pin').returncode, 1)
        self.assertEqual(self.call().returncode, 1)

    def test_symlinked_metadata_and_frontend_checkout_cannot_redirect_the_source(self):
        original = self.metadata_file
        self.metadata_file = self.root / 'metadata-link.json'
        self.metadata_file.symlink_to(original)
        self.assertEqual(self.call().returncode, 1)
        self.metadata_file = original
        moved = self.root / 'outside-frontend'
        self.frontend.rename(moved)
        self.frontend.symlink_to(moved, target_is_directory=True)
        self.assertEqual(self.call().returncode, 1)

    def test_duplicate_pin_fields_are_refused_without_github_outputs(self):
        pin = self.root / 'FRONTEND_PAIR.json'
        pin.write_text(json.dumps(self.pin)[:-1] + ', "schema": 1}')
        result = self.call('pin')
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, '')

    def test_readme_origin_commit_is_not_misreported_as_the_backend_commit(self):
        result = self.call()
        self.assertEqual(result.returncode, 0, result.stderr)
        value = json.loads(result.stdout)
        self.assertNotEqual(value['backend_source_sha'], self.compatibility['source_commit'])
        self.assertNotEqual(value['frontend_source_sha'], self.compatibility['source_commit'])

    def commit_new_pair(self):
        self.git(self.frontend, 'add', '.')
        self.git(self.frontend, 'commit', '-qm', 'New immutable frontend fixture')
        self.pin['source_sha'] = self.git(self.frontend, 'rev-parse', 'HEAD').strip()
        self.pin['compatibility_sha256'] = hashlib.sha256((self.frontend / 'compatibility.json').read_bytes()).hexdigest()
        (self.root / 'FRONTEND_PAIR.json').write_text(json.dumps(self.pin))
        self.git(self.root, 'add', 'FRONTEND_PAIR.json')
        self.git(self.root, 'commit', '-qm', 'Review new source pairing')
        self.backend_sha = self.git(self.root, 'rev-parse', 'HEAD').strip()

    def test_even_a_new_exact_git_pair_cannot_change_defaults_or_api_contract(self):
        for key, value in (('defaults', dict(locale='en', currency='USD')), ('api_contract', '/api/v2'),
                           ('backend_repository', 'other/backend'), ('schema', True), ('core_version', '0.5.9')):
            with self.subTest(key=key):
                (self.frontend / 'compatibility.json').write_text(json.dumps(dict(self.compatibility, **{key: value})))
                self.commit_new_pair()
                self.assertEqual(self.call().returncode, 1)

    def test_ignored_build_outputs_do_not_turn_into_source_or_release_authentication(self):
        (self.frontend / '.gitignore').write_text('/dist/\n/node_modules/\n*.tsbuildinfo\n')
        self.commit_new_pair()
        (self.frontend / 'dist').mkdir()
        (self.frontend / 'dist/index.html').write_text('<!doctype html><title>Built fixture</title>')
        (self.frontend / 'tsconfig.tsbuildinfo').write_text('{"generated":true}')
        result = self.call()
        self.assertEqual(result.returncode, 0, result.stderr)
        value = json.loads(result.stdout)
        self.assertIs(value['authenticated_release'], False)
        self.assertEqual(value['frontend_source_sha'], self.pin['source_sha'])

    def test_tracked_compiler_cache_is_rejected_before_using_the_pair(self):
        (self.frontend / 'tsconfig.tsbuildinfo').write_text('{"generated":true}')
        self.commit_new_pair()
        result = self.call()
        self.assertEqual(result.returncode, 1)
        self.assertIn('generated_cache_tracked', result.stderr)


if __name__ == '__main__':
    unittest.main()
