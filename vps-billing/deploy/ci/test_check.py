#!/usr/bin/env python3
"""Exercise CI orchestration with local fake tools, not a database acceptance run."""
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import textwrap
import unittest

CHECK = Path(__file__).with_name("check.sh").resolve()


def executable(path, source):
    path.write_text(textwrap.dedent(source), encoding="utf-8")
    path.chmod(0o700)


class CheckScriptTest(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory(prefix="aster-check-script-test-")
        self.addCleanup(self.scratch.cleanup)
        self.root = Path(self.scratch.name)
        self.source = self.root / "source checkout with spaces"
        project = self.source / "vps-billing"
        for directory in ("backend", "frontend", "tests", "deploy/ci", "deploy/scripts"):
            (project / directory).mkdir(parents=True, exist_ok=True)
        (project / "deploy/ci/resource_package_gate.py").write_text("# checked separately\n")
        (project / "deploy/ci/check-resource-package.sh").write_text(textwrap.dedent('''\
            #!/usr/bin/env bash
            set -eu
            [[ -z ${ASTER_MYSQL_TEST_DSN+x} ]]
            printf 'resource package\\n' >> "$CI_TEST_EVENTS"
            exit "${CI_TEST_RESOURCE_PACKAGE_FAIL:-0}"
        '''))
        (project / "backend/go.mod").write_text("module test.invalid/fixture\n")
        (project / "frontend/package-lock.json").write_text("{}\n")
        (project / "deploy/ci/test_release_pipeline.py").write_text(
            "import unittest\nclass Fixture(unittest.TestCase):\n    def test_fixture(self): self.assertTrue(True)\n"
        )
        (project / "deploy/scripts/test-release-manifest.py").write_text("pass\n")
        (project / "deploy/scripts/test-plugin-manager.py").write_text("pass\n")
        (project / "deploy/scripts/test-update-manager.py").write_text("pass\n")
        (project / "deploy/scripts/test-core-manager.py").write_text("pass\n")
        (project / "deploy/scripts/test-core-operation.py").write_text("pass\n")
        (project / "deploy/scripts/test-backup-native.sh").write_text("#!/bin/sh\nexit 0\n")
        (project / "tests/config_smoke.py").write_text(
            "import os\nassert 'ASTER_MYSQL_TEST_DSN' not in os.environ\n"
        )
        (project / "tests/security_smoke.py").write_text(textwrap.dedent('''\
            import os
            import sys
            assert os.environ['TEST_CUSTOMER_EMAIL'] == 'demo@aster.local'
            assert os.environ['TEST_CUSTOMER_PASSWORD'] == 'DemoCustomer!2026'
            assert os.environ['TEST_ADMIN_EMAIL'] == 'admin@aster.local'
            assert os.environ['TEST_ADMIN_PASSWORD'] == 'DemoAdmin!2026'
            assert os.environ['TEST_SECOND_EMAIL'] == 'other@aster.local'
            assert os.environ['TEST_SECOND_PASSWORD'] == 'DemoOther!2026'
            assert 'ASTER_MYSQL_TEST_DSN' not in os.environ
            sys.exit(7 if os.environ.get('CI_TEST_HTTP_FAIL') == '1' else 0)
        '''))
        self.api_source = self.root / "fake_api.py"
        executable(self.api_source, '''\
            #!/usr/bin/env python3
            from http.server import BaseHTTPRequestHandler, HTTPServer
            import json
            import os
            from pathlib import Path
            import signal
            assert os.environ['APP_MODE'] == 'demo'
            assert os.environ['PAYMENT_PROVIDER'] == 'payment.disabled'
            assert os.environ['EMAIL_PROVIDER'] == 'email.disabled'
            assert 'SESSION_SECRET' not in os.environ
            assert 'MYSQL_DSN' not in os.environ
            assert 'ASTER_MYSQL_TEST_DSN' not in os.environ
            Path(os.environ['CI_TEST_PID_FILE']).write_text(str(os.getpid()))
            class Handler(BaseHTTPRequestHandler):
                def do_GET(self):
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(json.dumps({'data': {'demo_mode': True}}).encode())
                def log_message(self, *_):
                    pass
            def stop(*_):
                raise SystemExit(0)
            signal.signal(signal.SIGTERM, stop)
            host, port = os.environ['LISTEN_ADDR'].rsplit(':', 1)
            assert host == '127.0.0.1'
            assert os.environ['APP_ORIGIN'] == 'http://' + host + ':' + port
            HTTPServer((host, int(port)), Handler).serve_forever()
        ''')
        self.bin = self.root / "bin"
        self.bin.mkdir()
        executable(self.bin / "go", '''\
            #!/usr/bin/env python3
            import os
            from pathlib import Path
            import shutil
            import sys
            for name in ('APP_MODE', 'MYSQL_DSN', 'SESSION_SECRET', 'BOOTSTRAP_PASSWORD',
                         'PAYMENT_PROVIDER', 'EMAIL_PROVIDER', 'INTEGRATIONS_CONFIG_FILE', 'LISTEN_ADDR',
                         'TRUSTED_PROXY_CIDRS', 'SMTP_ENCRYPTION_KEY_FILE', 'TEST_CUSTOMER_PASSWORD'):
                assert name not in os.environ, name
            with open(os.environ['CI_TEST_EVENTS'], 'a') as output:
                output.write('go ' + sys.argv[1] + '\\n')
            if sys.argv[1] == 'test':
                assert os.environ['ASTER_MYSQL_TEST_DSN'] == 'test-only-dsn-marker'
                assert sys.argv[1:] == ['test', '-race', '-count=1', '-timeout=20m', '-v', './...']
                print('--- SKIP: fixture (0.00s)' if os.environ.get('CI_TEST_GO_SKIP') == '1' else '--- PASS: fixture (0.00s)')
                if os.environ.get('CI_TEST_GO_EXIT'):
                    sys.exit(int(os.environ['CI_TEST_GO_EXIT']))
            else:
                assert 'ASTER_MYSQL_TEST_DSN' not in os.environ
            if sys.argv[1] == 'list':
                print('aster/billing/cmd/server')
                if os.environ.get('CI_TEST_GO_FORBIDDEN_DEP'):
                    print(os.environ['CI_TEST_GO_FORBIDDEN_DEP'])
                sys.exit(8 if os.environ.get('CI_TEST_GO_LIST_FAIL') else 0)
            if sys.argv[1] == 'build':
                assert os.environ['CGO_ENABLED'] == '0'
                assert os.environ['GOOS'] == 'linux' and os.environ['GOARCH'] == 'amd64'
                target = Path(sys.argv[sys.argv.index('-o') + 1])
                shutil.copyfile(os.environ['CI_TEST_API_SOURCE'], target)
                target.chmod(0o700)
        ''')
        executable(self.bin / "npm", '''\
            #!/usr/bin/env python3
            import os
            import sys
            assert 'ASTER_MYSQL_TEST_DSN' not in os.environ
            with open(os.environ['CI_TEST_EVENTS'], 'a') as output:
                output.write('npm ' + ' '.join(sys.argv[1:]) + '\\n')
        ''')
        executable(self.bin / "node", "#!/bin/sh\nexit 0\n")
        self.pid_file = self.root / "api.pid"
        self.events = self.root / "events.log"
        self.runner_tmp = self.root / "runner-temp"
        self.runner_tmp.mkdir()
        self.env = dict(os.environ)
        self.env.update({
            "PATH": str(self.bin) + os.pathsep + os.environ["PATH"],
            "TMPDIR": str(self.runner_tmp),
            "ASTER_MYSQL_TEST_DSN": "test-only-dsn-marker",
            "CI_TEST_EVENTS": str(self.events),
            "CI_TEST_API_SOURCE": str(self.api_source),
            "CI_TEST_PID_FILE": str(self.pid_file),
        })
        for name in ("APP_MODE", "MYSQL_DSN", "SESSION_SECRET", "BOOTSTRAP_PASSWORD",
                     "PAYMENT_PROVIDER", "EMAIL_PROVIDER", "INTEGRATIONS_CONFIG_FILE", "LISTEN_ADDR",
                     "TRUSTED_PROXY_CIDRS", "SMTP_ENCRYPTION_KEY_FILE", "TEST_CUSTOMER_PASSWORD"):
            self.env[name] = "must-be-scrubbed-fixture"
        self.addCleanup(self.stop_leftover_fixture)

    def stop_leftover_fixture(self):
        # Only the PID written by our own fake API, never an arbitrary process.
        if self.pid_file.exists():
            try:
                os.kill(int(self.pid_file.read_text()), signal.SIGTERM)
            except ProcessLookupError:
                pass

    def run_check(self, **overrides):
        env = dict(self.env, **overrides)
        result = subprocess.run(["bash", str(CHECK), str(self.source)], cwd=self.root,
                                env=env, text=True, capture_output=True, timeout=45)
        self.assertNotIn("test-only-dsn-marker", result.stdout + result.stderr)
        self.assertNotIn("must-be-scrubbed-fixture", result.stdout + result.stderr)
        self.assertEqual(list(self.runner_tmp.iterdir()), [], "CI temporary files were not cleaned")
        return result

    def assert_api_stopped(self):
        self.assertTrue(self.pid_file.exists(), "fixture API was not started")
        with self.assertRaises(ProcessLookupError):
            os.kill(int(self.pid_file.read_text()), 0)
        self.pid_file.unlink()

    def test_missing_mysql_dsn_fails_before_any_tool(self):
        result = self.run_check(ASTER_MYSQL_TEST_DSN="")
        self.assertEqual(result.returncode, 1)
        self.assertIn("ASTER_MYSQL_TEST_DSN is required", result.stderr)
        self.assertFalse(self.events.exists())

    def test_missing_update_regressions_stop_before_any_build_or_go_test(self):
        (self.source / "vps-billing/deploy/scripts/test-update-manager.py").unlink()
        result = self.run_check()
        self.assertEqual(result.returncode, 1)
        self.assertIn("Source checkout is missing deploy/scripts/test-update-manager.py", result.stderr)
        self.assertFalse(self.events.exists())
        self.assertFalse(self.pid_file.exists())

    def test_missing_resource_gate_stops_before_any_build(self):
        (self.source / "vps-billing/deploy/ci/check-resource-package.sh").unlink()
        result = self.run_check()
        self.assertEqual(result.returncode, 1)
        self.assertIn("Source checkout is missing deploy/ci/check-resource-package.sh", result.stderr)
        self.assertFalse(self.events.exists())

    def test_resource_package_failure_survives_tee_and_stops_frontend(self):
        result = self.run_check(CI_TEST_RESOURCE_PACKAGE_FAIL="9")
        self.assertEqual(result.returncode, 9, result.stdout + result.stderr)
        self.assertEqual(self.events.read_text().splitlines(), ["go test", "go vet", "go list", "go build", "resource package"])
        self.assertFalse(self.pid_file.exists())
        self.assertNotIn("All required checks passed", result.stdout)

    def test_skipped_go_test_fails_closed(self):
        result = self.run_check(CI_TEST_GO_SKIP="1")
        self.assertEqual(result.returncode, 1)
        self.assertIn("A Go test was skipped", result.stderr)
        self.assertEqual(self.events.read_text(), "go test\n")
        self.assertFalse(self.pid_file.exists())

    def test_go_budget_is_fixed_bounded_and_does_not_follow_inherited_timeout(self):
        # The fake Go tool requires the exact literal command, including all
        # packages and no -run/-skip filter. Explicit CLI flags win over GOFLAGS.
        result = self.run_check(GOFLAGS='-timeout=0', GO_TEST_TIMEOUT='1ns')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.events.read_text().splitlines().count('go test'), 1)
        self.assert_api_stopped()

    def test_go_failure_survives_tee_and_stops_all_later_checks(self):
        result = self.run_check(CI_TEST_GO_EXIT='7')
        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
        self.assertEqual(self.events.read_text(), 'go test\n')
        self.assertNotIn('All required checks passed', result.stdout)
        self.assertFalse(self.pid_file.exists())

    def test_go_skip_scanner_errors_stop_before_later_checks(self):
        for status in (2, 127):
            with self.subTest(status=status):
                if self.events.exists():
                    self.events.unlink()
                executable(self.bin / "grep", f"#!/bin/sh\nexit {status}\n")
                result = self.run_check()
                self.assertEqual(result.returncode, 1)
                self.assertIn(f"Go skip scanner failed (exit {status})", result.stderr)
                self.assertEqual(self.events.read_text(), "go test\n")
                self.assertFalse(self.pid_file.exists())
                self.assertNotIn("All required checks passed", result.stdout)

    def test_success_scrubs_environment_runs_all_checks_and_stops_api(self):
        result = self.run_check()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("All required checks passed", result.stdout)
        self.assertEqual(self.events.read_text().splitlines(), [
            "go test", "go vet", "go list", "go build", "resource package", "npm ci --include=dev --no-audit --no-fund",
            "npm run typecheck", "npm test", "npm run build", "npm run format:check",
        ])
        self.assert_api_stopped()

    def test_builtin_smtp_or_process_execution_dependencies_fail_before_build(self):
        for dependency in ("aster/billing/plugins/smtp", "aster/billing/plugins/paymentfixture", "net/smtp", "os/exec"):
            with self.subTest(dependency=dependency):
                if self.events.exists(): self.events.unlink()
                result = self.run_check(CI_TEST_GO_FORBIDDEN_DEP=dependency)
                self.assertEqual(result.returncode, 1)
                self.assertIn("core imported concrete plugin transport or process execution", result.stderr)
                self.assertEqual(self.events.read_text().splitlines(), ["go test", "go vet", "go list"])
                self.assertFalse(self.pid_file.exists())

    def test_failed_dependency_inventory_cannot_be_treated_as_empty_clean_result(self):
        result = self.run_check(CI_TEST_GO_LIST_FAIL="1")
        self.assertEqual(result.returncode, 8)
        self.assertEqual(self.events.read_text().splitlines(), ["go test", "go vet", "go list"])
        self.assertFalse(self.pid_file.exists())

    def test_http_failure_preserves_exit_code_and_stops_api(self):
        result = self.run_check(CI_TEST_HTTP_FAIL="1")
        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
        self.assertNotIn("All required checks passed", result.stdout)
        self.assert_api_stopped()


if __name__ == "__main__":
    unittest.main()
