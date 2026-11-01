#!/usr/bin/env python3
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('native_fixture_report.py')
SPEC = importlib.util.spec_from_file_location('native_fixture_report', SCRIPT)
REPORT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(REPORT)


class FixtureReports(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.output = self.root / 'reports/fixture-cleanup.json'

    def invoke(self, exit_code=0):
        return subprocess.run(['python3', '-I', str(SCRIPT), '--root', str(self.root), '--output', str(self.output),
                               '--source', 'a' * 40, '--exit-code', str(exit_code)], capture_output=True, text=True)

    def test_clean_success_records_only_public_facts(self):
        (self.root / 'private-dsn').write_text('secret-canary')
        self.assertEqual(self.invoke().returncode, 0)
        proof = json.loads(self.output.read_text())
        self.assertTrue(proof['passed'])
        self.assertTrue(proof['owned_mysql_fixture_removed'])
        self.assertTrue(proof['owned_benchmark_scratch_removed'])
        self.assertNotIn('secret-canary', self.output.read_text())
        self.assertNotIn(str(self.root), self.output.read_text())
        self.assertEqual(self.output.stat().st_mode & 0o777, 0o600)

    def test_command_failure_remains_failure_after_successful_cleanup(self):
        self.assertNotEqual(self.invoke(7).returncode, 0)
        proof = json.loads(self.output.read_text())
        self.assertEqual(proof['command_exit_code'], 7)
        self.assertFalse(proof['passed'])

    def test_leftover_fixture_or_link_is_failure_and_is_never_deleted(self):
        for prefix in ('aster-mysql.', 'aster-performance.'):
            with self.subTest(prefix=prefix):
                leftover = self.root / (prefix + 'secret-canary')
                leftover.symlink_to(self.root / 'missing')
                self.assertNotEqual(self.invoke().returncode, 0)
                self.assertTrue(leftover.is_symlink())
                self.assertNotIn('secret-canary', self.output.read_text())
                self.assertFalse(json.loads(self.output.read_text())['passed'])
                self.output.unlink()
                leftover.unlink()

    def test_evidence_is_exclusive_and_rejects_shared_or_linked_directories(self):
        self.assertEqual(self.invoke().returncode, 0)
        original = self.output.read_bytes()
        self.assertNotEqual(self.invoke().returncode, 0)
        self.assertEqual(self.output.read_bytes(), original)
        link = self.root / 'linked'
        link.symlink_to(self.root)
        with self.assertRaises(ValueError):
            REPORT.record(link, self.root / 'other/proof.json', 'a' * 40, 0)
        self.root.chmod(0o755)
        with self.assertRaises(ValueError):
            REPORT.record(self.root, self.root / 'other/proof.json', 'a' * 40, 0)

    def test_malformed_source_and_exit_status_cannot_claim_success(self):
        for source, code in [('main', 0), ('a' * 40, -1), ('a' * 40, 256)]:
            with self.subTest(source=source, code=code), self.assertRaises(ValueError):
                REPORT.record(self.root, self.output, source, code)
        self.assertFalse(self.output.exists())


if __name__ == '__main__':
    unittest.main()
