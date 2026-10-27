#!/usr/bin/env python3
"""Exercise the public CLI using real Git, Go and archive bytes, not a host install."""
import hashlib
import gzip
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parent


class BackendBundleCLI(unittest.TestCase):
    def setUp(self):
        self.case = tempfile.TemporaryDirectory(prefix='finance-backend-bundle-')
        self.addCleanup(self.case.cleanup)
        self.root = Path(self.case.name).resolve() / 'source'
        self.root.mkdir()
        self.output = self.root.parent / 'output'
        self.env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
        self.env.update(GIT_CONFIG_GLOBAL='/dev/null', GIT_CONFIG_NOSYSTEM='1',
                        GIT_AUTHOR_NAME='Fixture', GIT_COMMITTER_NAME='Fixture',
                        GIT_AUTHOR_EMAIL='fixture@example.invalid', GIT_COMMITTER_EMAIL='fixture@example.invalid',
                        GIT_AUTHOR_DATE='2026-10-27T00:00:00Z', GIT_COMMITTER_DATE='2026-10-27T00:00:00Z')
        for name in ('backend_bundle.py', 'frontend_pair.py'):
            source = SCRIPTS / name
            if source.exists():
                self.write('scripts/' + name, source.read_bytes())
        self.write('LICENSE', b'Fixture license\n')
        self.write('README.md', b'Fixture backend\n')
        self.write('vps-billing/backend/go.mod', b'module fixture/backend\n\ngo 1.22\n')
        self.write('vps-billing/backend/go.sum', b'')
        self.write('vps-billing/backend/pkg/pluginapi/protocol.go', b'package pluginapi\nconst (\nCoreVersion = "0.5.10"\n)\n')
        for name in ('server', 'plugin-host'):
            self.write('vps-billing/backend/cmd/' + name + '/main.go',
                       b'package main\nimport "fmt"\nfunc main() { fmt.Println("real compiled fixture") }\n')
        self.write('vps-billing/backend/migrations/001_initial.sql', b'CREATE TABLE fixture (id INT);\n')
        self.write('vps-billing/docs/OPERATIONS.md', b'Fixture operations\n')
        self.write('vps-billing/deploy/nginx.native.conf', b'# fixture nginx\n')
        self.write('vps-billing/deploy/systemd/aster-api.service', b'[Unit]\nDescription=Fixture\n')
        for name in ('backup-native.sh', 'core-manager.py', 'core-operation.py', 'plugin-manager.py', 'update-manager.py'):
            self.write('vps-billing/deploy/scripts/' + name, b'# fixture installer resource; never executed\n')
        self.pin = dict(schema=1, repository='xiaoqiqixi/betteridcsys-frontend', repository_id=1435344023,
                        source_sha='a' * 40, frontend_version='0.5.10', core_version='0.5.10', compatibility_sha256='b' * 64)
        self.write('FRONTEND_PAIR.json', json.dumps(self.pin).encode())
        self.git('init', '-q', '-b', 'main')
        self.commit()

    def write(self, name, raw):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)

    def git(self, *args):
        return subprocess.check_output(['git', '-C', str(self.root), *args], env=self.env, stderr=subprocess.DEVNULL).decode().strip()

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-qm', 'Reviewed source fixture')
        self.sha = self.git('rev-parse', 'HEAD')

    def call(self, *args):
        return subprocess.run([sys.executable, '-I', str(self.root / 'scripts/backend_bundle.py'), *args],
                              env=self.env, capture_output=True, text=True, timeout=180)

    def build(self, output=None):
        return self.call('build', '--source-sha', self.sha, '--output', str(output or self.output))

    def inspect(self):
        return self.call('inspect', '--manifest', str(self.output / 'BACKEND.json'),
                         '--archive', str(self.output / 'aster-finance-backend-0.5.10-linux-amd64.tar.gz'),
                         '--expected-source-sha', self.sha, '--expected-frontend-sha', self.pin['source_sha'])

    def test_real_go_build_preserves_pair_and_is_not_authenticated_or_installable(self):
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        built = json.loads(result.stdout)
        self.assertIs(built['authenticated_source'], False)
        self.assertIs(built['installable_release'], False)
        manifest = json.loads((self.output / 'BACKEND.json').read_bytes())
        self.assertEqual(manifest['repository_id'], 1435344087)
        self.assertEqual(manifest['source_commit'], self.sha)
        self.assertEqual(manifest['frontend_pair'], self.pin)
        self.assertNotEqual(manifest['source_commit'], manifest['frontend_pair']['source_sha'])
        archive = self.output / manifest['asset']['name']
        self.assertEqual(hashlib.sha256(archive.read_bytes()).hexdigest(), manifest['asset']['sha256'])
        with tarfile.open(archive, 'r:gz') as source:
            names = source.getnames()
            self.assertIn('migrations/001_initial.sql', names)
            self.assertIn('licenses/go-runtime/LICENSE', names)
            self.assertFalse(any(name.startswith(('web/', 'dist/')) for name in names))
            self.assertTrue(source.extractfile('bin/aster-api').read().startswith(b'\x7fELF\x02\x01'))
        result = self.inspect()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIs(json.loads(result.stdout)['authenticated_source'], False)

    def test_bool_frontend_schema_cannot_substitute_for_integer_identity(self):
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        path = self.output / 'BACKEND.json'
        value = json.loads(path.read_bytes())
        value['frontend_pair']['schema'] = True
        path.write_text(json.dumps(value))
        self.assertNotEqual(self.inspect().returncode, 0)

    def rewrite_archive(self, change):
        path = self.output / 'BACKEND.json'
        value = json.loads(path.read_bytes())
        asset = self.output / value['asset']['name']
        with tarfile.open(asset, 'r:gz') as source:
            files = {entry.name: source.extractfile(entry).read() for entry in source}
        change(files)
        stream = io.BytesIO()
        with tarfile.open(fileobj=stream, mode='w', format=tarfile.USTAR_FORMAT) as archive:
            for name, raw in sorted(files.items()):
                entry = tarfile.TarInfo(name)
                entry.size = len(raw)
                entry.mode = 0o555 if name.startswith('bin/') else 0o444
                entry.mtime = value['source_date_epoch']
                archive.addfile(entry, io.BytesIO(raw))
        payload = gzip.compress(stream.getvalue(), mtime=value['source_date_epoch'])
        asset.write_bytes(payload)
        value['asset']['size'] = len(payload)
        value['asset']['sha256'] = hashlib.sha256(payload).hexdigest()
        value['files'] = [{'path': name, 'size': len(raw), 'sha256': hashlib.sha256(raw).hexdigest()}
                          for name, raw in sorted(files.items())]
        path.write_text(json.dumps(value))

    def test_inspection_refuses_bundled_frontend_even_with_matching_digests(self):
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.rewrite_archive(lambda files: files.update({'web/index.html': b'not the approved frontend'}))
        self.assertNotEqual(self.inspect().returncode, 0)

    def test_missing_migrations_is_not_a_complete_backend(self):
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.rewrite_archive(lambda files: files.pop('migrations/001_initial.sql'))
        self.assertNotEqual(self.inspect().returncode, 0)

    def test_two_real_builds_are_byte_identical_and_existing_output_is_never_reused(self):
        first = self.build()
        self.assertEqual(first.returncode, 0, first.stderr)
        second = self.root.parent / 'second'
        result = self.build(second)
        self.assertEqual(result.returncode, 0, result.stderr)
        original = {path.name: path.read_bytes() for path in self.output.iterdir()}
        self.assertEqual(original, {path.name: path.read_bytes() for path in second.iterdir()})
        self.assertNotEqual(self.build().returncode, 0)
        self.assertEqual(original, {path.name: path.read_bytes() for path in self.output.iterdir()})

    def test_wrong_source_dirty_tree_and_untracked_source_are_refused_before_output(self):
        real_sha = self.sha
        self.sha = 'c' * 40
        self.assertNotEqual(self.build().returncode, 0)
        self.sha = real_sha
        license_file = self.root / 'LICENSE'
        raw = license_file.read_bytes()
        license_file.write_bytes(b'not reviewed')
        self.assertNotEqual(self.build().returncode, 0)
        license_file.write_bytes(raw)
        self.write('not-reviewed.py', b'not source')
        self.assertNotEqual(self.build().returncode, 0)
        self.assertFalse(self.output.exists())

    def test_tracked_symlink_and_symlink_output_parent_are_refused(self):
        (self.root / 'LICENSE').unlink()
        (self.root / 'LICENSE').symlink_to('README.md')
        self.commit()
        self.assertNotEqual(self.build().returncode, 0)
        self.assertFalse(self.output.exists())
        (self.root / 'LICENSE').unlink()
        self.write('LICENSE', b'Fixture license\n')
        self.commit()
        alias = self.root.parent / 'alias'
        alias.symlink_to(self.root.parent, target_is_directory=True)
        self.assertNotEqual(self.build(alias / 'output').returncode, 0)
        self.assertFalse(self.output.exists())

    def test_corrupt_asset_wrong_source_and_duplicate_manifest_fields_are_refused(self):
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        manifest = self.output / 'BACKEND.json'
        original = manifest.read_bytes()
        value = json.loads(original)
        asset = self.output / value['asset']['name']
        original_asset = asset.read_bytes()
        asset.write_bytes(original_asset[:-1] + bytes([original_asset[-1] ^ 1]))
        self.assertNotEqual(self.inspect().returncode, 0)
        asset.write_bytes(original_asset)
        value['source_commit'] = 'd' * 40
        manifest.write_text(json.dumps(value))
        self.assertNotEqual(self.inspect().returncode, 0)
        manifest.write_bytes(original.rstrip()[:-1] + b',"schema":1}')
        self.assertNotEqual(self.inspect().returncode, 0)

    def test_extra_gzip_member_and_traversal_are_refused_after_recomputing_hashes(self):
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.rewrite_archive(lambda files: files.update({'../escape': b'no escape'}))
        self.assertNotEqual(self.inspect().returncode, 0)
        self.rewrite_archive(lambda files: files.pop('../escape'))
        manifest = self.output / 'BACKEND.json'
        value = json.loads(manifest.read_bytes())
        asset = self.output / value['asset']['name']
        raw = asset.read_bytes() + gzip.compress(b'hidden second stream')
        asset.write_bytes(raw)
        value['asset'].update(size=len(raw), sha256=hashlib.sha256(raw).hexdigest())
        manifest.write_text(json.dumps(value))
        self.assertNotEqual(self.inspect().returncode, 0)

    def test_ignored_untracked_build_recipe_cannot_claim_a_reviewed_commit(self):
        self.write('.gitignore', b'/scripts/backend_bundle.py\n')
        self.git('rm', '--cached', 'scripts/backend_bundle.py')
        self.commit()
        result = self.build()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.output.exists())


if __name__ == '__main__':
    unittest.main()
