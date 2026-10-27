#!/usr/bin/env python3
"""Real Git/Go/static-archive CLI integration; not native installation proof."""
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import sys
import unittest

SCRIPTS = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('backend_fixture', SCRIPTS / 'test_backend_bundle.py')
fixtures = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixtures)


class PairedRuntimeCLI(unittest.TestCase):
    def setUp(self):
        self.backend = fixtures.BackendBundleCLI()
        self.backend.setUp()
        self.addCleanup(self.backend.doCleanups)
        self.root = self.backend.root
        self.base = self.root.parent
        if (SCRIPTS / 'pair_runtime.py').exists():
            self.backend.write('scripts/pair_runtime.py', (SCRIPTS / 'pair_runtime.py').read_bytes())
        shutil.copytree(SCRIPTS.parent / 'contracts/frontend-bundle', self.root / 'contracts/frontend-bundle')
        frontend = self.base / 'frontend-source'
        frontend.mkdir()
        package = dict(name='fixture-frontend', version='0.5.10')
        values = {'package.json': package, 'package-lock.json': dict(package, lockfileVersion=3, packages={'': package}),
                  'compatibility.json': dict(schema=1, core_version='0.5.10', api_contract='/api/v1',
                                             backend_repository='xiaoqiqixi/betteridcsys-backend', source_commit='f' * 40,
                                             defaults=dict(locale='zh-CN', currency='CNY'))}
        for name, value in values.items():
            (frontend / name).write_text(json.dumps(value))
        (frontend / 'LICENSE').write_text('Original frontend fixture license\n')
        (frontend / 'dist').mkdir()
        (frontend / 'dist/index.html').write_text('<!doctype html><title>Paired static fixture</title>')
        (frontend / 'dist/app.js').write_text('console.log("static fixture");')
        self.backend.pin['compatibility_sha256'] = hashlib.sha256((frontend / 'compatibility.json').read_bytes()).hexdigest()
        self.backend.write('FRONTEND_PAIR.json', json.dumps(self.backend.pin).encode())
        self.backend.commit()
        result = self.backend.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.frontend = self.base / 'frontend-output'
        result = subprocess.run([sys.executable, '-I', str(self.root / 'contracts/frontend-bundle/frontend_bundle.py'),
                                 'build', '--source-root', str(frontend), '--output', str(self.frontend),
                                 '--source-sha', 'a' * 40, '--source-date-epoch', '1793059200'], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.output = self.base / 'runtime'

    def call(self, *args):
        return subprocess.run([sys.executable, '-I', str(self.root / 'scripts/pair_runtime.py'), *args],
                              capture_output=True, text=True, timeout=30)

    def stage(self):
        return self.call('stage', '--backend-manifest', str(self.backend.output / 'BACKEND.json'),
                         '--backend-archive', str(self.backend.output / 'aster-finance-backend-0.5.10-linux-amd64.tar.gz'),
                         '--frontend-manifest', str(self.frontend / 'FRONTEND.json'),
                         '--frontend-archive', str(self.frontend / 'aster-finance-frontend-0.5.10.tar.gz'),
                         '--backend-source', self.backend.sha, '--output', str(self.output))

    def verify(self):
        return self.call('verify', '--directory', str(self.output), '--backend-source', self.backend.sha)

    def test_stage_actual_compiled_backend_and_static_frontend_with_separate_sources(self):
        result = self.stage()
        self.assertEqual(result.returncode, 0, result.stderr)
        record = json.loads(result.stdout)
        self.assertIs(record['authenticated_release'], False)
        self.assertIs(record['installed'], False)
        self.assertEqual(record['backend_source'], self.backend.sha)
        self.assertEqual(record['frontend_source'], 'a' * 40)
        self.assertTrue((self.output / 'bin/aster-api').read_bytes().startswith(b'\x7fELF'))
        self.assertEqual((self.output / 'web/index.html').read_text(), '<!doctype html><title>Paired static fixture</title>')
        self.assertEqual((self.output / 'licenses/frontend/LICENSE').read_text(), 'Original frontend fixture license\n')
        self.assertTrue((self.output / 'migrations/001_initial.sql').is_file())
        self.assertFalse((self.output / 'manifests/BUILD.json').exists())
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIs(json.loads(result.stdout)['installed'], False)

    def test_wrong_frontend_version_or_source_does_not_create_runtime(self):
        path = self.frontend / 'FRONTEND.json'
        original = json.loads(path.read_text())
        for key, wrong in (('source_commit', 'd' * 40), ('version', '0.5.11'), ('repository_id', 1435344087)):
            with self.subTest(key=key):
                value = dict(original, **{key: wrong})
                path.write_text(json.dumps(value))
                self.assertNotEqual(self.stage().returncode, 0)
                self.assertFalse(self.output.exists())

    def test_corrupt_archive_or_unreviewed_parser_is_refused_before_staging(self):
        archive = self.frontend / 'aster-finance-frontend-0.5.10.tar.gz'
        raw = archive.read_bytes()
        archive.write_bytes(raw[:-1])
        self.assertNotEqual(self.stage().returncode, 0)
        self.assertFalse(self.output.exists())
        archive.write_bytes(raw)
        parser = self.root / 'contracts/frontend-bundle/frontend_bundle.py'
        parser.write_bytes(parser.read_bytes() + b'\n# altered source\n')
        self.assertNotEqual(self.stage().returncode, 0)
        self.assertFalse(self.output.exists())

    def test_existing_output_is_preserved_and_source_mismatch_is_refused(self):
        result = self.stage()
        self.assertEqual(result.returncode, 0, result.stderr)
        receipt = (self.output / 'PAIR.json').read_bytes()
        self.assertNotEqual(self.stage().returncode, 0)
        self.assertEqual((self.output / 'PAIR.json').read_bytes(), receipt)
        self.backend.sha = 'c' * 40
        self.assertNotEqual(self.verify().returncode, 0)

    def test_changed_bytes_permissions_and_extra_directory_do_not_verify(self):
        result = self.stage()
        self.assertEqual(result.returncode, 0, result.stderr)
        path = self.output / 'web/index.html'
        original = path.read_bytes()
        path.chmod(0o600)
        path.write_bytes(b'replaced frontend')
        path.chmod(0o444)
        self.assertNotEqual(self.verify().returncode, 0)
        path.chmod(0o600)
        path.write_bytes(original)
        self.assertNotEqual(self.verify().returncode, 0)
        path.chmod(0o444)
        (self.output / 'unexpected').mkdir(mode=0o700)
        self.assertNotEqual(self.verify().returncode, 0)
        (self.output / 'unexpected').rmdir()
        self.assertEqual(self.verify().returncode, 0)

    def test_false_authentication_receipt_cannot_be_changed_to_true(self):
        result = self.stage()
        self.assertEqual(result.returncode, 0, result.stderr)
        path = self.output / 'PAIR.json'
        value = json.loads(path.read_text())
        value['authenticated_release'] = True
        path.chmod(0o600)
        path.write_text(json.dumps(value))
        path.chmod(0o444)
        self.assertNotEqual(self.verify().returncode, 0)


if __name__ == '__main__':
    unittest.main()
