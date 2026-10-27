#!/usr/bin/env python3
"""Portable boundary tests only. These never claim actual browser/MySQL proof."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("browser-mysql.py")
SPEC = importlib.util.spec_from_file_location("browser_mysql", SCRIPT)
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class BrowserMySQLGuards(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="billing-gate-", dir="/tmp")
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name).resolve()
        self.root, self.temp = self.base / "repo", self.base / "runner"
        self.root.mkdir(mode=0o700)
        self.temp.mkdir(mode=0o700)
        self.fixture = self.temp / "aster-mysql.12345678"
        self.fixture.mkdir(mode=0o700)
        # Portable unit fixture: mock only the socket stat classification.
        # Actual Unix-socket connectivity and @@datadir are checked by Linux Go.
        (self.fixture / "mysql.sock").touch()
        self.billing = self.root / "vps-billing"
        for relative in ("frontend/dist/index.html", "frontend/tests/browser-mysql.mjs", "frontend/tests/ui-copy-boundaries.mjs", "deploy/ci/browser-mysql-fixture.gotxt"):
            file = self.billing / relative
            file.parent.mkdir(parents=True, exist_ok=True)
            file.write_text("source")
        self.modules = self.base / "tools/node_modules"
        for package in (self.modules / "playwright/package.json", self.billing / "frontend/node_modules/@playwright/test/package.json"):
            package.parent.mkdir(parents=True, exist_ok=True)
            package.write_text('{"version":"1.64.0"}')
        self.browsers = self.base / "browsers"
        self.browsers.mkdir()
        self.output = self.temp / "public-evidence"
        self.args = argparse.Namespace(repo_root=str(self.root), output=str(self.output))
        self.env = {"RUNNER_TEMP": str(self.temp), "ASTER_NATIVE_MYSQL_FIXTURE_ROOT": str(self.fixture),
                    "ASTER_NATIVE_MYSQL_SOCKET": str(self.fixture / "mysql.sock"), "ASTER_MYSQL_TEST_DSN": "private-fixture-only",
                    "PLAYWRIGHT_MODULE_DIR": str(self.modules), "PLAYWRIGHT_BROWSERS_PATH": str(self.browsers)}
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(runner.stat, "S_ISSOCK", return_value=True).start()
        mock.patch.dict(os.environ, self.env, clear=True).start()
        mock.patch.object(runner.platform, "system", return_value="Linux").start()
        mock.patch.object(runner.platform, "machine", return_value="x86_64").start()
        mock.patch.object(runner.shutil, "which", return_value="/usr/bin/owned-tool").start()

    def rejected(self):
        with self.assertRaises((runner.Failure, OSError)):
            runner.validate(self.args)
        self.assertFalse(self.output.exists())

    def test_owned_native_input_accepted_without_creating_output(self):
        self.assertEqual(runner.validate(self.args), (self.root, self.billing, self.temp, self.output))
        self.assertFalse(self.output.exists())

    def test_non_linux_rejected(self):
        with mock.patch.object(runner.platform, "system", return_value="Darwin"):
            self.rejected()

    def test_non_amd64_rejected(self):
        with mock.patch.object(runner.platform, "machine", return_value="aarch64"):
            self.rejected()

    def test_root_rejected(self):
        with mock.patch.object(runner.os, "geteuid", return_value=0):
            self.rejected()

    def test_public_scratch_rejected(self):
        self.temp.chmod(0o755)
        self.rejected()

    def test_foreign_socket_rejected(self):
        os.environ["ASTER_NATIVE_MYSQL_SOCKET"] = str(self.base / "mysql.sock")
        self.rejected()

    def test_symlink_fixture_rejected(self):
        alias = self.temp / "aster-mysql.alias"
        alias.symlink_to(self.fixture, target_is_directory=True)
        os.environ["ASTER_NATIVE_MYSQL_FIXTURE_ROOT"] = str(alias)
        self.rejected()

    def test_regular_file_instead_of_socket_rejected(self):
        with mock.patch.object(runner.stat, "S_ISSOCK", return_value=False):
            self.rejected()

    def test_missing_private_dsn_rejected(self):
        del os.environ["ASTER_MYSQL_TEST_DSN"]
        self.rejected()

    def test_existing_output_rejected(self):
        self.output.mkdir()
        with self.assertRaises(runner.Failure): runner.validate(self.args)
        self.assertEqual(list(self.output.iterdir()), [])

    def test_external_output_rejected(self):
        self.args.output = str(self.base / "external")
        self.rejected()

    def test_output_inside_database_rejected(self):
        self.args.output = str(self.fixture / "public")
        self.rejected()

    def test_missing_built_frontend_rejected(self):
        (self.billing / "frontend/dist/index.html").unlink()
        self.rejected()

    def test_symlink_source_rejected(self):
        source = self.billing / "frontend/tests/browser-mysql.mjs"
        source.unlink()
        source.symlink_to(self.billing / "frontend/tests/ui-copy-boundaries.mjs")
        self.rejected()

    def test_unpinned_browser_rejected(self):
        (self.modules / "playwright/package.json").write_text('{"version":"1.63.0"}')
        self.rejected()

    def test_missing_tool_rejected(self):
        with mock.patch.object(runner.shutil, "which", return_value=None): self.rejected()

    def test_child_environment_allowlists_no_credentials_or_security_overrides(self):
        scratch = self.temp / "case"
        scratch.mkdir(mode=0o700)
        source = {**self.env, "PATH": "/usr/bin", "HOME": str(self.base),
                  "GH_TOKEN": "secret", "GITHUB_TOKEN": "secret", "APP_SECRET": "secret",
                  "HTTP_PROXY": "http://proxy.invalid", "GOFLAGS": "injected", "GOENV": "injected",
                  "NODE_OPTIONS": "injected", "MOZ_DISABLE_CONTENT_SANDBOX": "1",
                  "PLAYWRIGHT_FIREFOX_POLICIES_JSON": "injected", "NODE_EXTRA_CA_CERTS": "injected"}
        env = runner.clean_environment(source, scratch, self.billing)
        for key in ("GH_TOKEN", "GITHUB_TOKEN", "APP_SECRET", "HTTP_PROXY", "GOFLAGS", "NODE_OPTIONS", "MOZ_DISABLE_CONTENT_SANDBOX", "PLAYWRIGHT_FIREFOX_POLICIES_JSON", "NODE_EXTRA_CA_CERTS"):
            self.assertNotIn(key, env)
        self.assertEqual(env["GOENV"], "off")
        self.assertEqual(env["CGO_ENABLED"], "1")
        self.assertEqual(env["HOME"], str(scratch / "home"))
        self.assertEqual(env["ASTER_MYSQL_TEST_DSN"], self.env["ASTER_MYSQL_TEST_DSN"])

    def test_failed_exclusive_output_creation_never_writes_existing_directory(self):
        self.output.mkdir()
        sentinel = self.output / "unrelated"
        sentinel.write_text("preserve")
        with mock.patch.object(runner, "validate", return_value=(self.root, self.billing, self.temp, self.output)), \
             mock.patch("sys.argv", [str(SCRIPT), "--repo-root", str(self.root), "--output", str(self.output)]), \
             mock.patch.object(runner.signal, "signal"), mock.patch("builtins.print"):
            self.assertEqual(runner.main(), 1)
        self.assertEqual([p.name for p in self.output.iterdir()], ["unrelated"])
        self.assertEqual(sentinel.read_text(), "preserve")

    def test_cleanup_exception_does_not_skip_private_scratch_removal(self):
        process_owner = mock.Mock()
        process_owner.start.side_effect = runner.Failure("bounded stage failure")
        process_owner.cleanup.side_effect = RuntimeError("private diagnostic must not escape")
        with mock.patch.object(runner, "validate", return_value=(self.root, self.billing, self.temp, self.output)), \
             mock.patch("sys.argv", [str(SCRIPT), "--repo-root", str(self.root), "--output", str(self.output)]), \
             mock.patch.object(runner.signal, "signal"), mock.patch("builtins.print"), \
             mock.patch.object(runner.OWNER, "Processes", return_value=process_owner):
            self.assertEqual(runner.main(), 1)
        self.assertEqual(list(self.temp.glob("aster-billing-browser.*")), [])
        result = json.loads((self.output / "summary.json").read_text())
        self.assertFalse(result["passed"])
        self.assertFalse(result["owned_process_cleanup"])
        self.assertNotIn("private diagnostic", json.dumps(result))


    def test_generated_ca_is_added_only_to_acceptance_child(self):
        scratch = self.temp / "case"
        scratch.mkdir(mode=0o700)
        ca = scratch / "ca.pem"
        ca.write_text("owned certificate")
        ca.chmod(0o600)
        base = {"PATH": "/usr/bin"}
        child = runner.acceptance_environment(base, scratch)
        self.assertEqual(child["NODE_EXTRA_CA_CERTS"], str(ca))
        self.assertNotIn("NODE_EXTRA_CA_CERTS", base)
        self.assertNotIn("NODE_TLS_REJECT_UNAUTHORIZED", child)

    def test_external_node_trust_override_is_rejected(self):
        for key in ("NODE_EXTRA_CA_CERTS", "NODE_TLS_REJECT_UNAUTHORIZED"):
            with self.assertRaises(runner.Failure):
                runner.acceptance_environment({key: "inherited"}, self.temp)

    def test_ca_symlink_or_nonprivate_file_is_rejected(self):
        scratch = self.temp / "case"
        scratch.mkdir(mode=0o700)
        ca = scratch / "ca.pem"
        target = scratch / "other.pem"
        target.write_text("owned")
        target.chmod(0o600)
        ca.symlink_to(target)
        with self.assertRaises(runner.Failure): runner.acceptance_environment({}, scratch)
        ca.unlink(); ca.write_text("owned"); ca.chmod(0o644)
        with self.assertRaises(runner.Failure): runner.acceptance_environment({}, scratch)



class BrowserMySQLEvidence(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="billing-evidence-")
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name).resolve()
        self.scratch, self.output = self.base / "case", self.base / "output"
        self.public = self.scratch / "public"
        self.public.mkdir(parents=True)
        self.output.mkdir()
        self.report = {"schema": "aster-billing-browser-mysql-v1", "passed": True, "api_mode": "production", "mysql_fixture": True,
                       "synthetic_get_faults": 4, "business_writes": 0, "authenticated_logins": 4, "authenticated_logouts": 4,
                       "cases": [{"id": f"{kind}-{role}-{size}-{locale}", "passed": True} for kind in ("currency", "audit") for role in ("customer", "admin") for size in ("desktop", "mobile") for locale in ("zh-CN", "en")]
                       + [{"id": f"synthetic-get-fault-{size}-{locale}", "passed": True} for size in ("desktop", "mobile") for locale in ("zh-CN", "en")], "screenshots": runner.SHOTS}
        self.tls = {"untrusted_ca_rejected": True, "private_profile_ca_trusted": True, "ignore_https_errors": False, "host_trust_modified": False}
        self.write()
        for name in runner.SHOTS:
            # Synthetic header tests only, deliberately NOT a rendered screenshot.
            (self.public / name).write_bytes(b"\x89PNG\r\n\x1a\n" + struct.pack(">I", 13) + b"IHDR" + struct.pack(">II", 375 if "mobile" in name else 1440, 900))

    def write(self):
        (self.public / "report.json").write_text(json.dumps(self.report))
        (self.public / "tls.json").write_text(json.dumps(self.tls))

    def rejected(self):
        with self.assertRaises((runner.Failure, OSError, ValueError, TypeError)):
            runner.export_public(self.scratch, self.output)
        self.assertEqual(list(self.output.iterdir()), [])

    def test_complete_fixed_evidence_accepted(self):
        runner.export_public(self.scratch, self.output)
        self.assertEqual(sorted(p.name for p in (self.output / "screenshots").iterdir()), runner.SHOTS)
        self.assertEqual(sorted(p.name for p in self.output.iterdir()), ["report.json", "screenshots", "tls.json"])

    def test_non_object_report_rejected(self):
        for value in (None, [], "private"):
            (self.public / "report.json").write_text(json.dumps(value))
            self.rejected()

    def test_non_object_tls_rejected(self):
        for value in (None, [], "private"):
            (self.public / "tls.json").write_text(json.dumps(value))
            self.rejected()

    def test_private_credentials_never_export(self):
        (self.public / "credentials.json").write_text("private")
        self.rejected()

    def test_unknown_report_field_never_export(self):
        self.report["raw_error"] = "private"
        self.write(); self.rejected()

    def test_demo_mode_rejected(self):
        self.report["api_mode"] = "demo"
        self.write(); self.rejected()

    def test_missing_or_duplicated_case_rejected(self):
        self.report["cases"][0] = self.report["cases"][1]
        self.write(); self.rejected()

    def test_failed_case_rejected(self):
        self.report["cases"][0]["passed"] = False
        self.write(); self.rejected()

    def test_business_write_rejected(self):
        self.report["business_writes"] = 1
        self.write(); self.rejected()

    def test_boolean_integer_confusion_rejected(self):
        self.report["passed"] = 1
        self.write(); self.rejected()

    def test_tls_bypass_rejected(self):
        self.tls["ignore_https_errors"] = True
        self.write(); self.rejected()

    def test_tls_boolean_integer_confusion_rejected(self):
        self.tls["untrusted_ca_rejected"] = 1
        self.write(); self.rejected()

    def test_missing_tls_negative_probe_rejected(self):
        self.tls["untrusted_ca_rejected"] = False
        self.write(); self.rejected()

    def test_missing_screenshot_rejected(self):
        (self.public / runner.SHOTS[0]).unlink()
        self.rejected()

    def test_symlink_screenshot_rejected(self):
        target = self.public / runner.SHOTS[0]
        target.unlink(); target.symlink_to(self.public / runner.SHOTS[1])
        self.rejected()

    def test_non_png_rejected(self):
        (self.public / runner.SHOTS[0]).write_text("not PNG")
        self.rejected()

    def test_unbounded_png_rejected(self):
        (self.public / runner.SHOTS[0]).write_bytes(b"\x89PNG\r\n\x1a\n" + struct.pack(">I", 13) + b"IHDR" + struct.pack(">II", 375, 1000000))
        self.rejected()


class BrowserMySQLProgress(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="billing-progress-")
        self.addCleanup(self.temporary.cleanup)
        self.scratch = Path(self.temporary.name).resolve()
        self.path = self.scratch / "progress.json"
        self.value = {"schema": "aster-billing-browser-progress-v1", "phase": "currency-assert",
                      "role": "admin", "size": "mobile", "locale": "en",
                      "fault_hits": 1, "fault_http_200": 1, "fault_fulfilled": 1, "fault_failed": 0}
        self.write()

    def write(self):
        self.path.write_text(json.dumps(self.value))

    def rejected(self):
        with self.assertRaises((runner.Failure, OSError, ValueError, TypeError)):
            runner.read_browser_progress(self.scratch)

    def test_exact_progress_returned(self):
        self.assertEqual(runner.read_browser_progress(self.scratch), self.value)

    def test_missing_progress_is_unknown_not_success(self):
        self.path.unlink()
        self.assertIsNone(runner.read_browser_progress(self.scratch))

    def test_every_source_phase_has_an_allowlisted_diagnostic(self):
        import re
        script = SCRIPT.parents[2] / "frontend/tests/browser-mysql.mjs"
        shared = script.with_name("ui-copy-boundaries.mjs")
        phases = set(re.findall(r'(?:progress|checkpoint)\("([a-z-]+)"', script.read_text() + shared.read_text()))
        self.assertEqual(phases, runner.BROWSER_PHASES)

    def test_unrecognized_field_rejected(self):
        self.value["error"] = "credential-bearing message"
        self.write(); self.rejected()

    def test_unrecognized_labels_rejected(self):
        for key in ("schema", "phase", "role", "size", "locale"):
            previous = self.value[key]
            for value in ("private", None, [], 1, True):
                self.value[key] = value
                self.write(); self.rejected()
            self.value[key] = previous

    def test_unbounded_or_non_integer_fault_counts_rejected(self):
        for key in ("fault_hits", "fault_http_200", "fault_fulfilled", "fault_failed"):
            previous = self.value[key]
            for value in (-1, 33, True, 1.5, "private", None):
                self.value[key] = value
                self.write(); self.rejected()
            self.value[key] = previous

    def test_non_object_rejected(self):
        for value in (None, [], "private"):
            self.path.write_text(json.dumps(value)); self.rejected()

    def test_duplicate_key_rejected(self):
        text = json.dumps(self.value)
        self.path.write_text(text[:-1] + ',"role":"customer"}')
        self.rejected()

    def test_oversized_diagnostic_rejected(self):
        self.path.write_text(" " * 2048 + json.dumps(self.value))
        self.rejected()

    def test_symlink_diagnostic_rejected(self):
        self.path.rename(self.scratch / "private")
        self.path.symlink_to(self.scratch / "private")
        self.rejected()



if __name__ == "__main__":
    unittest.main()
