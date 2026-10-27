#!/usr/bin/env python3
"""Unprivileged guard/loopback transport checks, not native deployment evidence."""
from http.server import BaseHTTPRequestHandler, HTTPServer
import ast
import importlib.util
import json
import os
from pathlib import Path
import re
import ssl
import struct
import subprocess
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("accept-native.sh")
HTTP_SCRIPT = Path(__file__).with_name("accept-native-http.py")
CORE_SCRIPT = Path(__file__).with_name("core-native.sh")
PREPARE_SCRIPT = Path(__file__).with_name("prepare-native-runner.sh")
CORE_SPEC = importlib.util.spec_from_file_location("core_native_acceptance", CORE_SCRIPT.with_suffix(".py"))
CORE = importlib.util.module_from_spec(CORE_SPEC)
CORE_SPEC.loader.exec_module(CORE)
SPEC = importlib.util.spec_from_file_location("native_acceptance_http", HTTP_SCRIPT)
HTTP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HTTP)


class NativeReleaseModeTests(unittest.TestCase):
    def test_cleanup_kill_selector_uses_the_hosts_advertised_option(self):
        source = SCRIPT.read_text()
        start = source.index('systemctl_help=$(')
        end = source.index('\nreadonly systemctl_kill_scope', start) + len('\nreadonly systemctl_kill_scope')
        block = source[start:end]
        command = '''set -euo pipefail
fail() { exit 9; }
systemctl() { [[ $# == 1 && $1 == --help ]] || exit 80; printf '%s' "$HELP"; }
''' + block + '\nprintf "%s\\n" "$systemctl_kill_scope"\n'
        for text, expected in ((' --kill-who=WHO\n', '--kill-who'), (' --kill-whom=WHOM\n', '--kill-whom'),
                               (' --kill-who=WHO\n --kill-whom=WHOM\n', '--kill-whom'), ('', None)):
            with self.subTest(expected=expected):
                result = subprocess.run(['bash', '-c', command], env={**os.environ, 'HELP': text}, capture_output=True, text=True, timeout=5)
                self.assertEqual(result.returncode, 0 if expected else 9)
                if expected: self.assertEqual(result.stdout.strip(), expected)
        self.assertIn('systemctl kill "$systemctl_kill_scope=all" --signal=KILL "$unit"', source)

    def test_source_version_parser_uses_only_the_actual_core_declaration(self):
        parser = SCRIPT.read_text().split("<<'PYVERSION'\n", 1)[1].split("\nPYVERSION", 1)[0]
        valid = 'package pluginapi\nconst (\n CoreVersion = "0.5.3"\n PaymentMinCoreVersion = "0.5.3"\n)\n'
        cases = [(valid, True),
                 ('package pluginapi\nconst CoreVersion = "0.5.3"\n', True),
                 (valid.replace(' CoreVersion = "0.5.3"\n', ''), False),
                 (valid + '\nconst CoreVersion = "0.5.4"\n', False),
                 (valid.replace(' CoreVersion = "0.5.3"', ' CoreVersion = "0.5.3-rc1"'), False)]
        with tempfile.TemporaryDirectory(prefix='aster-source-version-') as directory:
            source = Path(directory) / 'protocol.go'
            for document, accepted in cases:
                with self.subTest(document=document):
                    source.write_text(document)
                    result = subprocess.run([sys.executable, '-', str(source)], input=parser,
                                            capture_output=True, text=True, timeout=5)
                    self.assertEqual(result.returncode == 0, accepted, result.stderr)
                    if accepted:
                        self.assertEqual(result.stdout.strip(), '0.5.3')
                    else:
                        self.assertIn('one stable Core version', result.stderr)

    def test_surviving_public_preparation_is_reused_as_the_pinned_upgrade_baseline(self):
        source = SCRIPT.read_text()
        assignments = re.findall(r'^(?:public|baseline)_(?:version|target)=.*$', source, re.M)
        self.assertEqual(len(assignments), 4)
        # Evaluate only the actual version/path assignments, with no host action.
        command = 'set -euo pipefail\nopt=/opt/aster-ci-123-1-456\n' + '\n'.join(assignments)
        command += '\nprintf "%s\\n" "$public_version" "$public_target" "$baseline_version" "$baseline_target"\n'
        result = subprocess.run(['bash', '-c', command], check=True, capture_output=True, text=True, timeout=5)
        public_version, public_target, baseline_version, baseline_target = result.stdout.splitlines()
        self.assertEqual(public_version, CORE.PUBLISHED_VERSION)
        self.assertEqual(baseline_version, CORE.PUBLISHED_VERSION)
        self.assertEqual(public_target, baseline_target)
        self.assertEqual(baseline_target, '/opt/aster-ci-123-1-456/core-upgrade/releases/0.5.2')
        self.assertIn('ASTER_CORE_BASELINE_RELEASE="$baseline_target"', source)
        self.assertNotIn('ASTER_CORE_BASELINE_RELEASE="$public_target"', source)
        self.assertIn('$ASTER_CORE_FIXTURE_OPT/core-upgrade/releases/0.5.2', CORE_SCRIPT.read_text())
        self.assertEqual(source.count('core-manager.py" prepare --version'), 1)
        self.assertIn('emit_json_fact PUBLISHED_CORE_BASELINE_PREPARE_JSON public-core-prepare.json', source)

    def test_baseline_requires_the_surviving_published_release_and_exact_six_asset_names(self):
        # Identity guard fixtures only: these are never official metadata or
        # native acceptance evidence, and no program is executed here.
        with tempfile.TemporaryDirectory(prefix='aster-baseline-identity-') as directory:
            root = Path(directory)
            (root / 'manifests').mkdir()
            (root / 'bin').mkdir()
            binaries = {'bin/aster-api': b'guard-only-api', 'bin/aster-plugin-host': b'guard-only-host'}
            for name, raw in binaries.items():
                (root / name).write_bytes(raw)
            build = {'component_versions': {'core': '0.5.2'}, 'source_commit': CORE.PUBLISHED_SOURCE,
                'binary_sha256': {name: CORE.sha256(raw) for name, raw in binaries.items()}}
            receipt = {'version': '0.5.2', 'source_sha': CORE.PUBLISHED_SOURCE, 'release_id': 413559379,
                'run_id': 38702170922, 'run_attempt': 1,
                'assets': [{'name': name} for name in sorted(CORE.PUBLISHED_ASSETS)]}

            def verify(selected_build=build, selected_receipt=receipt):
                (root / 'manifests/BUILD.json').write_text(json.dumps(selected_build))
                (root / 'manifests/PREPARED.json').write_text(json.dumps(selected_receipt))
                return CORE.verify_published_baseline(root)

            proof = verify()
            self.assertEqual(proof['version'], '0.5.2')
            self.assertEqual(proof['tag'], 'v0.5.2')
            self.assertEqual(proof['source_commit'], '9ed610a283020f50e2cfc529f8b5145b4a9c8fc3')
            self.assertEqual(proof['release_id'], 413559379)
            self.assertEqual(proof['build_run_id'], 38702170922)
            for changed in ({'version': '0.5.1'}, {'source_sha': 'a' * 40},
                    {'release_id': 413373046}, {'run_id': 38672929446}, {'run_attempt': 2},
                    {'assets': receipt['assets'][:-1]},
                    {'assets': receipt['assets'][:-1] + [{'name': 'unreviewed-asset'}]},
                    {'assets': receipt['assets'][:-1] + [receipt['assets'][0]]}):
                with self.subTest(changed=changed):
                    with self.assertRaises(CORE.AcceptanceFailure):
                        verify(selected_receipt={**receipt, **changed})
            for changed in ({'component_versions': {'core': '0.5.1'}}, {'source_commit': 'a' * 40}):
                with self.subTest(changed=changed):
                    with self.assertRaises(CORE.AcceptanceFailure):
                        verify(selected_build={**build, **changed})
            (root / 'bin/aster-api').write_bytes(b'changed')
            with self.assertRaisesRegex(CORE.AcceptanceFailure, 'executable differs'):
                verify()

    def test_source_candidate_rejects_a_version_that_is_not_newer_than_the_surviving_baseline(self):
        with tempfile.TemporaryDirectory(prefix='aster-candidate-version-') as directory:
            root = Path(directory)
            pipeline = SimpleNamespace(strict_json=json.loads)
            for version in ('0.5.1', '0.5.2'):
                (root / 'BUILD.json').write_text(json.dumps({'component_versions': {'core': version}}))
                with self.subTest(version=version), patch.object(CORE, 'module', return_value=pipeline), \
                        patch.object(CORE, 'command', return_value=('a' * 40 + '\n').encode()):
                    with self.assertRaisesRegex(CORE.AcceptanceFailure, 'newer than the published baseline'):
                        CORE.verify_source_candidate(root, root, root)

    def test_private_extract_modes_are_readable_with_only_approved_executables(self):
        executable = {
            'bin/aster-api', 'bin/aster-plugin-host', 'deploy/scripts/backup-native.sh',
            'deploy/scripts/plugin-manager.py', 'deploy/scripts/update-manager.py',
            'deploy/scripts/core-manager.py',
            'deploy/scripts/core-operation.py',
        }
        files = executable | {'manifests/BUILD.json', 'migrations/001_schema.sql', 'web/index.html', 'licenses/NOTICE'}
        with tempfile.TemporaryDirectory(prefix='aster public release ') as temp:
            root = Path(temp)
            for name in files:
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                target.write_text('Public fixture; no credentials.\n')
                target.chmod(0o600)
            for directory in [root, *(p for p in root.rglob('*') if p.is_dir())]:
                directory.chmod(0o700)
            function = re.search(r'^normalize_fixture_release\(\) \{\n.*?^\}', SCRIPT.read_text(), re.M | re.S)
            self.assertIsNotNone(function)
            # Execute the actual normalization using real find/chmod on owned
            # files. The test's sudo function removes privilege only here.
            command = 'set -euo pipefail\nsudo() { [[ $1 == -n ]]; shift; "$@"; }\n' + function.group() + '\nnormalize_fixture_release "$1"\n'
            subprocess.run(['bash', '-c', command, 'mode-test', str(root)], check=True, capture_output=True, text=True, timeout=10)
            for directory in [root, *(p for p in root.rglob('*') if p.is_dir())]:
                self.assertEqual(directory.stat().st_mode & 0o7777, 0o755)
            for name in files:
                self.assertEqual((root / name).stat().st_mode & 0o7777, 0o755 if name in executable else 0o644)


class NativeHTTPSTransportTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix="aster-loopback-tls-")
        cls.addClassCleanup(cls.directory.cleanup)
        root = Path(cls.directory.name)
        cert, key = root / "localhost.pem", root / "localhost.key"
        subprocess.run([
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
            "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost",
            "-keyout", str(key), "-out", str(cert),
        ], check=True, capture_output=True, timeout=15)

        class EchoPeer(BaseHTTPRequestHandler):
            def do_POST(self):
                # A local TLS transport fixture, never an imitation API result.
                payload = json.dumps({
                    "peer": self.client_address[0], "host": self.headers["Host"],
                    "cookie": self.headers.get("Cookie"), "csrf": self.headers.get("X-CSRF-Token"),
                    "body": self.rfile.read(int(self.headers.get("Content-Length", "0"))).decode(),
                }).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, *args):
                pass

        cls.server = HTTPServer(("127.0.0.1", 0), EchoPeer)
        cls.addClassCleanup(cls.server.server_close)
        server_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        server_context.load_cert_chain(cert, key)
        cls.server.socket = server_context.wrap_socket(cls.server.socket, server_side=True)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.addClassCleanup(cls.thread.join, 5)
        cls.addClassCleanup(cls.server.shutdown)
        cls.port = cls.server.server_port
        cls.context = ssl.create_default_context(cafile=str(cert))

    def test_real_source_peers_and_session_headers_reach_verified_tls_server(self):
        for peer in ("127.0.0.2", "127.0.0.3"):
            with self.subTest(peer=peer):
                status, raw, _ = HTTP.loopback_https_request(
                    self.port, self.context, "/fixture", source=peer, method="POST",
                    headers={"Cookie": "fixture=test-only", "X-CSRF-Token": "test-only"}, body=b"fixture-body",
                )
                self.assertEqual(status, 200)
                self.assertEqual(json.loads(raw), {
                    "peer": peer, "host": f"localhost:{self.port}", "cookie": "fixture=test-only",
                    "csrf": "test-only", "body": "fixture-body",
                })

    def test_untrusted_certificate_is_rejected(self):
        with self.assertRaises(ssl.SSLCertVerificationError):
            HTTP.loopback_https_request(self.port, ssl.create_default_context(), "/fixture")

    def test_disabled_hostname_check_and_non_fixture_source_are_rejected(self):
        context = ssl.create_default_context()
        context.check_hostname = False
        with self.assertRaisesRegex(ValueError, "Verified TLS"):
            HTTP.loopback_https_request(self.port, context, "/fixture")
        with self.assertRaisesRegex(ValueError, "loopback sources"):
            HTTP.loopback_https_request(self.port, self.context, "/fixture", source="192.0.2.1")

    def test_http_port_cli_rejects_privileged_or_duplicate_ports_before_reading_files(self):
        for port in ("80", "443", "4443", "65536"):
            with self.subTest(port=port):
                result = subprocess.run([
                    "python3", str(HTTP_SCRIPT), "--project", "/nonexistent", "--port", "4443",
                    "--http-port", port, "--ca", "/nonexistent", "--accounts", "/nonexistent",
                    "--report", "/nonexistent",
                ], text=True, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 2)
                self.assertNotIn("Traceback", result.stderr)


class NativeAcceptanceGuardTests(unittest.TestCase):
    script = SCRIPT

    def run_guard(self, supplied, arguments=None):
        with tempfile.TemporaryDirectory(prefix="aster-native-guard-") as directory:
            root = Path(directory)
            marker = root / "privileged-command-ran"
            # A guard regression must fail before any privileged command. Never
            # use a fake successful systemd/MySQL result as acceptance evidence.
            sudo = root / "sudo"
            sudo.write_text(f"#!/bin/sh\n: > '{marker}'\nexit 99\n")
            sudo.chmod(0o755)
            env = {"PATH": str(root) + ":" + os.environ["PATH"]}
            env.update(supplied)
            result = subprocess.run(
                ["bash", str(self.script), *(arguments if arguments is not None else [
                    "--allow-isolated-runner", str(root), str(root / "evidence")
                ])],
                env=env, text=True, capture_output=True, timeout=10,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker.exists(), result.stdout + result.stderr)
            self.assertFalse((root / "evidence").exists())
            return result.stderr

    def test_no_implicit_host_mutation(self):
        self.assertIn("Explicit isolated-runner", self.run_guard({}, []))

    def test_local_or_self_hosted_is_rejected_before_sudo(self):
        for settings in ({}, {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "self-hosted"}):
            with self.subTest(settings=settings):
                self.assertIn("GitHub-hosted", self.run_guard(settings))

    def test_wrong_repository_is_rejected(self):
        self.assertIn("Unexpected repository", self.run_guard({
            "GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted",
            "GITHUB_REPOSITORY": "another/project",
        }))

    def test_missing_or_malformed_run_identity_is_rejected(self):
        for identity in ("", "../other", "123;false", "not-a-number"):
            with self.subTest(identity=identity):
                self.assertIn("Missing run identity", self.run_guard({
                    "GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted",
                    "GITHUB_REPOSITORY": "xiaoqiqixi/betteridcsys",
                    "GITHUB_RUN_ID": identity, "GITHUB_RUN_ATTEMPT": "1",
                }))


class CoreExecutorAcceptanceGuardTests(NativeAcceptanceGuardTests):
    script = CORE_SCRIPT


class NativePreparationSmokeGuardTests(unittest.TestCase):
    def test_actual_smoke_response_projection_is_bounded_and_never_echoes_payload(self):
        source = PREPARE_SCRIPT.read_text().split("<<'PYHOOKSMOKE'\n", 1)[1].split("\nPYHOOKSMOKE", 1)[0]
        function = next(node for node in ast.parse(source).body if isinstance(node, ast.FunctionDef) and node.name == "safe_reply_shape")
        namespace = {"json": json}
        exec(compile(ast.Module(body=[function], type_ignores=[]), "actual_smoke_projection", "exec"), namespace)
        project = namespace["safe_reply_shape"]
        hook = project(b'{"type":"a(sasbttttuii)","data":[]}', "ExecStartPre", "aster-test.service")
        self.assertTrue(hook["keys_exact"])
        self.assertEqual(hook["list_length"], 0)
        nested = project(b'{"type":"a(sasbttttuii)","data":[[]]}', "ExecStartPre", "aster-test.service")
        self.assertEqual(nested["nested_single_list_length"], 0)
        identity = project(b'{"type":"s","data":"aster-test.service"}', "unit-id", "aster-test.service")
        self.assertTrue(identity["identity_matches"])
        self.assertEqual(identity["type"], "s")
        self.assertEqual(identity["data_kind"], "string")
        self.assertNotIn("aster-test.service", json.dumps(identity))
        wrapped = project(b'{"type":"s","data":["aster-test.service"]}', "unit-id", "aster-test.service")
        self.assertFalse(wrapped["identity_matches"])
        private = json.dumps({"type": "private-provider-value", "data": ["/private/object/path"], "private-key": "private-password"}).encode()
        result = project(private, "unit-id", "aster-test.service")
        self.assertFalse(result["identity_matches"])
        self.assertFalse(result["keys_exact"])
        self.assertEqual(result["type"], "other_or_absent")
        self.assertNotIn("private", json.dumps(result))
        self.assertEqual(project(b"private-stderr", "unit-id", "aster-test.service"), {"json_valid": False})
        self.assertTrue(project(b"x" * 8193, "unit-id", "aster-test.service")["exceeded_json_bound"])

    def test_actual_inline_smoke_rejects_unowned_context_before_import_or_privilege(self):
        source = PREPARE_SCRIPT.read_text().split("<<'PYHOOKSMOKE'\n", 1)[1].split("\nPYHOOKSMOKE", 1)[0]
        with tempfile.TemporaryDirectory(prefix="aster-hook-smoke-guard-") as directory:
            root = Path(directory)
            marker = root / "privileged-command-ran"
            sudo = root / "sudo"
            sudo.write_text("#!/bin/sh\n: > '" + str(marker) + "'\nexit 99\n")
            sudo.chmod(0o755)
            for settings, reason in (({}, "isolated_runner_required"),
                ({"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "self-hosted", "GITHUB_REPOSITORY": "xiaoqiqixi/betteridcsys"}, "isolated_runner_required"),
                ({"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_REPOSITORY": "another/project"}, "isolated_runner_required"),
                ({"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_REPOSITORY": "xiaoqiqixi/betteridcsys", "GITHUB_RUN_ID": "../other", "GITHUB_RUN_ATTEMPT": "1"}, "run_identity_required")):
                with self.subTest(settings=settings):
                    result = subprocess.run(["python3", "-I", "-", "/nonexistent/source.py", str(root)], input=source,
                        env={"PATH": str(root) + ":" + os.environ["PATH"], **settings}, capture_output=True, text=True, timeout=5)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(reason, result.stderr)
                    self.assertFalse(marker.exists())
                    self.assertEqual(set(path.name for path in root.iterdir()), {"sudo"})


class LateSchemaFaultGuardTests(unittest.TestCase):
    """Exercise the fault adapter only; these doubles are not native evidence."""
    def adapter(self, journal=None, active="active"):
        calls = []
        result = object()

        class Native:
            def __init__(self, spec):
                self.spec = spec

            def api(self, release, environment, command_name, allow_failure=False):
                calls.append(("api", release, environment, command_name, allow_failure))
                if getattr(self, "api_failure", None) is not None:
                    raise self.api_failure
                return result

            def service(self, verb, unit=None):
                calls.append(("service", verb, unit))
                if getattr(self, "service_failure", None) is not None:
                    raise self.service_failure
                return active

            def preflight(self, release, environment):
                calls.append(("preflight", release, environment))
                return result

        def checked_journal():
            calls.append(("checked_journal",))
            return journal if journal is not None else {
                "steps": {"activate": {"status": "done"}},
                "database_migration": {"started": True, "completed": True},
            }

        fixture = SimpleNamespace(root=Path("/owned-fixture"), candidate="/owned-fixture/releases/0.5.2",
            config=Path("/owned-config"), spec={"api_unit": "owned-fixture.service", "database": "owned_database"},
            checked_journal=checked_journal,
            confirm_current=lambda release: calls.append(("confirm_current", release)),
            full_data_hash=lambda: calls.append(("data_hash",)) or "synthetic-data-digest",
            resources=SimpleNamespace(sql=lambda query, *, database: calls.append(("sql", query, database))))
        return CORE.late_schema_native(SimpleNamespace(Native=Native), fixture), fixture, calls, result

    def test_non_target_and_non_runtime_preflights_are_forwarded_without_fault(self):
        native, fixture, calls, result = self.adapter()
        runtime = {"MYSQL_DSN_FILE": str(fixture.config / "secrets/mysql_runtime_dsn")}
        for release, environment in (("/owned-fixture/releases/0.5.1", runtime),
                (fixture.candidate, {"MYSQL_DSN_FILE": "/owned-private/migrator_dsn"}),
                (fixture.candidate, {})):
            with self.subTest(release=release, environment=environment):
                self.assertIs(native.preflight(release, environment), result)
                self.assertEqual(calls[-1], ("preflight", release, environment))
        self.assertEqual(len(calls), 3)
        self.assertFalse(native.injected)

    def test_fault_refuses_earlier_or_uncertain_activation_before_schema_mutation(self):
        for activation, migration, starts in (("started", {"started": True, "completed": True}, 1),
                ("done", {"started": True, "completed": False}, 1),
                ("done", {"started": False, "completed": False}, 1),
                ("done", {"started": True, "completed": True}, 0),
                ("done", {"started": True, "completed": True}, 2)):
            with self.subTest(activation=activation, migration=migration, starts=starts):
                native, fixture, calls, _ = self.adapter({"steps": {"activate": {"status": activation}},
                    "database_migration": migration})
                native.target_starts = starts
                with self.assertRaises(CORE.AcceptanceFailure):
                    native.preflight(fixture.candidate, {"MYSQL_DSN_FILE": str(fixture.config / "secrets/mysql_runtime_dsn")})
                self.assertEqual(calls, [("checked_journal",)])
                self.assertFalse(native.injected)

    def test_fault_requires_live_target_then_mutates_only_owned_database_once(self):
        native, fixture, calls, result = self.adapter()
        environment = {"MYSQL_DSN_FILE": str(fixture.config / "secrets/mysql_runtime_dsn")}
        with patch.object(CORE.os, "readlink", return_value=fixture.candidate):
            native.service("start", fixture.spec["api_unit"])
        self.assertIs(native.preflight(fixture.candidate, environment), result)
        self.assertIs(native.preflight(fixture.candidate, environment), result)
        self.assertEqual(calls, [
            ("service", "start", fixture.spec["api_unit"]), ("checked_journal",),
            ("confirm_current", fixture.candidate), ("service", "show", fixture.spec["api_unit"]),
            ("data_hash",), ("sql", "ALTER TABLE plans MODIFY COLUMN cpu SMALLINT NOT NULL;", "owned_database"),
            ("preflight", fixture.candidate, environment), ("preflight", fixture.candidate, environment),
        ])
        self.assertTrue(native.injected)
        self.assertTrue(native.activation_was_done)
        self.assertEqual(native.data_before_fault, "synthetic-data-digest")

    def test_inactive_target_refuses_fault_before_hash_or_sql(self):
        native, fixture, calls, _ = self.adapter(active="inactive")
        native.target_starts = 1
        with self.assertRaises(CORE.AcceptanceFailure):
            native.preflight(fixture.candidate, {"MYSQL_DSN_FILE": str(fixture.config / "secrets/mysql_runtime_dsn")})
        self.assertEqual([call[0] for call in calls], ["checked_journal", "confirm_current", "service"])
        self.assertFalse(native.injected)

    def test_attempt_counters_include_failed_forwarded_actions(self):
        native, fixture, calls, result = self.adapter()
        environment = {"MYSQL_DSN_FILE": "/owned-private/migrator_dsn"}
        self.assertIs(native.api(fixture.candidate, environment, "-preflight", True), result)
        self.assertIs(native.api("/owned-fixture/releases/0.5.1", environment, "-migrate"), result)
        self.assertEqual(native.migration_calls, 0)
        failure = RuntimeError("private-native-diagnostic")
        native.api_failure = failure
        for expected in (1, 2):
            with self.assertRaises(RuntimeError) as caught:
                native.api(fixture.candidate, environment, "-migrate", True)
            self.assertIs(caught.exception, failure)
            self.assertEqual(native.migration_calls, expected)
        native.service_failure = failure
        with patch.object(CORE.os, "readlink", return_value=fixture.candidate):
            with self.assertRaises(RuntimeError) as caught:
                native.service("start", fixture.spec["api_unit"])
        self.assertIs(caught.exception, failure)
        self.assertEqual(native.target_starts, 1)
        self.assertEqual(calls[-1], ("service", "start", fixture.spec["api_unit"]))


class CorePaymentSchemaMigrationTests(unittest.TestCase):
    """Scenario guards with fake native/SQL calls, never deployment evidence."""

    def scenario(self, *, old_accepts_new=False, changed_business=False, invented_payment=False, invented_product_configuration=False):
        state = {"schema": 6}
        calls = []
        candidate = str(SCRIPT.parents[2] / "backend")
        published = "/published-baseline"

        class Native:
            def __init__(self, spec):
                self.spec = spec

            def api(self, release, environment, command_name, allow_failure=False):
                calls.append((release, command_name))
                if command_name == "-migrate":
                    if release == published:
                        if state["schema"] != 6:
                            raise AssertionError("published migration did not start from schema 6")
                        state["schema"] = 7
                    else:
                        if state["schema"] not in (7, CORE.CANDIDATE_SCHEMA):
                            raise AssertionError("candidate did not preserve the published prefix")
                        state["schema"] = CORE.CANDIDATE_SCHEMA
                incompatible = release == published and state["schema"] == CORE.CANDIDATE_SCHEMA and not old_accepts_new
                status = "incompatible" if incompatible else ("pending_migrations" if release == candidate and state["schema"] < CORE.CANDIDATE_SCHEMA else "current")
                raw = json.dumps({"schema": 1, "read_only": True, "configuration_checked": True,
                    "database": {"schema_version": state["schema"], "status": status}}).encode()
                return subprocess.CompletedProcess(["synthetic-native"], int(incompatible), raw)

            def preflight(self, release, environment):
                result = self.api(release, environment, "-preflight", allow_failure=True)
                return dict(json.loads(result.stdout), exit_code=result.returncode)

        def sql(query, *, database=None):
            if query.startswith("SELECT COUNT(*) FROM information_schema.TABLES"):
                return str({6: 13, 7: 14, CORE.CANDIDATE_SCHEMA: 23}[state["schema"]])
            if query.startswith("SELECT SUM(amount_minor)"):
                return "1500"
            if query.startswith("SELECT TABLE_NAME"):
                return "\n".join("table_" + letter for letter in "abcdefghijklm")
            if query.startswith("SELECT COUNT(*) FROM users WHERE disabled"):
                return "0"
            if query.startswith("SELECT COUNT(*) FROM plans p LEFT JOIN catalog_regions"):
                return "0"
            if query.startswith("SELECT (SELECT COUNT(*) FROM payment_attempts)"):
                return str(int(invented_payment))
            if query.startswith("SELECT (SELECT COUNT(*) FROM product_configurations)"):
                return str(int(invented_product_configuration))
            return ""

        def data_hash(database=None, tables=()):
            if tables:
                return "changed" if changed_business and state["schema"] == CORE.CANDIDATE_SCHEMA else "existing-business-data-hash"
            return "complete-current-schema-data-hash"

        with tempfile.TemporaryDirectory(prefix="aster-legacy-schema-guard-") as directory:
            resources = SimpleNamespace(restores=[], control=Path(directory), socket=Path(directory) / "unused.sock", sql=sql)
            fresh = SimpleNamespace(candidate=candidate, spec={"database": "fresh_fixture"}, full_data_hash=data_hash, legacy_business_hash=data_hash)
            executor = SimpleNamespace(Native=Native, runtime_environment=lambda spec: {}, require=CORE.require)
            with patch.object(CORE, "stage"):
                report = CORE.legacy_schema_migration(resources, executor, fresh, {"directory": published})
        return report, calls, candidate, published

    def test_legacy_hash_projects_every_pre_taxonomy_plan_field_without_new_columns(self):
        queries, dumps = [], []
        def sql(query, *, database=None):
            queries.append(query)
            return "original-plan-bytes" if "FROM plans" in query else "original-user-bytes"
        fixture = SimpleNamespace(resources=SimpleNamespace(sql=sql), full_data_hash=lambda database, tables: dumps.append(list(tables)) or "original-other-bytes")
        result = CORE.Fixture.legacy_business_hash(fixture, "owned", ["users", "plans", "orders", "plan_prices"])
        self.assertEqual(dumps, [["orders", "plan_prices"]])
        self.assertEqual(result, CORE.sha256(b"original-other-bytes\noriginal-user-bytes\noriginal-plan-bytes"))
        self.assertIn("SELECT HEX(id),HEX(name),HEX(description),cpu,memory_gb,disk_gb,transfer_tb,HEX(region),price_minor,HEX(currency),available FROM plans ORDER BY BINARY id;", queries)
        self.assertFalse(any("type_id" in query or "region_id" in query or "SELECT *" in query for query in queries))
        self.assertTrue(any("HEX(password_hash)" in query and "created_at" in query for query in queries))

    def test_historical_prefix_uses_actual_published_then_candidate_migration_order(self):
        report, calls, candidate, published = self.scenario()
        self.assertEqual([call for call in calls if call[1] == "-migrate"],
            [(published, "-migrate"), (candidate, "-migrate"), (candidate, "-migrate")])
        self.assertEqual((report["schema_before"], report["published_schema"], report["schema_after"]), (6, 7, CORE.CANDIDATE_SCHEMA))
        self.assertEqual((report["tables_before"], report["tables_published"], report["tables_after"]), (13, 14, 23))
        self.assertEqual(report["published_baseline_preflight_after_payment_migration"], "incompatible")
        self.assertTrue(report["additive_payment_migration_preserved_existing_data"])
        self.assertTrue(report["new_payment_tables_empty"])
        self.assertTrue(report["new_product_configuration_tables_empty"])
        self.assertTrue(report["taxonomy_tables_and_exact_legacy_relationships_added"])
        self.assertTrue(report["repeat_migration_data_unchanged"])

    def test_old_program_acceptance_after_current_schema_cannot_claim_success(self):
        with self.assertRaisesRegex(CORE.AcceptanceFailure, "rejected by the published old program"):
            self.scenario(old_accepts_new=True)

    def test_additive_migration_cannot_change_business_rows_or_invent_payment(self):
        for changed, invented in ((True, False), (False, True)):
            with self.subTest(changed=changed, invented=invented), self.assertRaises(CORE.AcceptanceFailure):
                self.scenario(changed_business=changed, invented_payment=invented)

    def test_additive_migration_cannot_backfill_product_configuration_or_provisioning_jobs(self):
        with self.assertRaisesRegex(CORE.AcceptanceFailure, "Migration invented product configuration"):
            self.scenario(invented_product_configuration=True)


class CoreExecutorEvidenceSafetyTests(unittest.TestCase):
    def test_acl_wire_projection_excludes_ids_and_bounds_entries(self):
        raw = struct.pack("<I", 2) + b"".join(struct.pack("<HHI", tag, permissions, identity)
            for tag, permissions, identity in ((1, 7, 0xffffffff), (2, 6, 123456789), (16, 7, 0xffffffff), (32, 7, 0xffffffff)))
        observed = CORE.acl_wire_summary(raw)
        self.assertTrue(observed["valid_wire_shape"])
        self.assertEqual(observed["version"], 2)
        self.assertEqual(observed["entry_count"], 4)
        self.assertEqual(observed["entries"], [{"tag": 1, "permissions": 7}, {"tag": 2, "permissions": 6},
            {"tag": 16, "permissions": 7}, {"tag": 32, "permissions": 7}])
        self.assertNotIn("123456789", json.dumps(observed))
        self.assertNotIn("4294967295", json.dumps(observed))
        large = CORE.acl_wire_summary(struct.pack("<I", 2) + struct.pack("<HHI", 2, 6, 123456789) * 129)
        self.assertEqual(len(large["entries"]), 128)
        self.assertTrue(large["entries_truncated"])
        self.assertFalse(CORE.acl_wire_summary(b"private-malformed-ACL")["valid_wire_shape"])

    def test_owned_acl_diagnostics_uses_actual_nofollow_xattr_reads_without_mutation(self):
        with tempfile.TemporaryDirectory(prefix="aster-acl-observation-") as directory:
            root = Path(directory)
            target = root / "private-file"
            target.write_bytes(b"private-data")
            link = root / "private-link"
            link.symlink_to(target)
            original = os.getxattr
            before = target.stat()
            with patch.object(CORE.os, "getxattr", wraps=original) as observed:
                report = CORE.owned_acl_diagnostics(link)
            self.assertEqual(observed.call_count, 2)
            self.assertEqual([call.args[1] for call in observed.call_args_list], ["system.posix_acl_default", "system.posix_acl_access"])
            self.assertTrue(all(call.kwargs == {"follow_symlinks": False} for call in observed.call_args_list))
            self.assertEqual(target.read_bytes(), b"private-data")
            self.assertEqual(target.stat().st_mode, before.st_mode)
            self.assertEqual(set(report), {"default", "access"})
            self.assertNotIn("private", json.dumps(report))

    def test_restore_component_projection_stops_at_links_and_exports_only_metadata(self):
        with tempfile.TemporaryDirectory(prefix="aster-restore-components-") as directory:
            root = Path(directory)
            target = root / "private-credential"
            target.write_text("private-password")
            target.chmod(0o622)
            observed = CORE.protected_component_diagnostics(str(target))
            final = observed["components"][-1]
            self.assertTrue(final["endpoint"])
            self.assertTrue(final["group_or_world_writable"])
            self.assertFalse(final["privileged_bits"])
            self.assertEqual(final["mode"], "0o622")
            link = root / "private-link"
            link.symlink_to(root)
            linked = CORE.protected_component_diagnostics(str(link / target.name))
            self.assertEqual(linked["components"][-1]["type"], "symlink")
            self.assertFalse(linked["components"][-1]["endpoint"])
            self.assertNotIn("private", json.dumps(observed) + json.dumps(linked))
            self.assertLessEqual(len(observed["components"]), 12)

    def test_restore_open_observer_preserves_actual_descriptor_error_and_restores_function(self):
        class Rejected(RuntimeError):
            pass
        failure = Rejected("private-filesystem-path-and-password")
        with tempfile.TemporaryDirectory(prefix="aster-restore-observer-") as directory:
            root = Path(directory)
            target = root / "private-file"
            target.write_bytes(b"private-data")
            calls = []
            def original(path, *args, **kwargs):
                calls.append((path, args, kwargs))
                if kwargs.get("private"):
                    raise failure
                return os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
            updater = SimpleNamespace(protected_open=original, Rejected=Rejected)
            resources = SimpleNamespace(restore_paths={"restore_credentials": target})
            fixture = SimpleNamespace(resources=resources, baseline=str(root / "private-baseline"))
            with CORE.observe_restore_protected_open(SimpleNamespace(updater=updater), fixture):
                descriptor = updater.protected_open(str(target), False)
                try:
                    self.assertEqual(os.read(descriptor, 12), b"private-data")
                finally:
                    os.close(descriptor)
                with self.assertRaises(Rejected) as caught:
                    updater.protected_open(str(target), private=True)
                self.assertIs(caught.exception, failure)
            self.assertIs(updater.protected_open, original)
            self.assertEqual(calls, [(str(target), (False,), {}), (str(target), (), {"private": True})])
            self.assertEqual(resources.restore_filesystem_trace["failed_opens"][0]["role"], "restore_credentials")
            self.assertNotIn("private", json.dumps(resources.restore_filesystem_trace))

    def test_restore_path_projection_reports_metadata_without_following_links_or_reading_contents(self):
        with tempfile.TemporaryDirectory(prefix="aster-restore-metadata-") as directory:
            root = Path(directory)
            output = root / "private-backup-output"
            output.mkdir(mode=0o700)
            completed = output / "private-database-and-timestamp"
            completed.mkdir(mode=0o700)
            (completed / "BACKUP.json").write_text("private-password-and-DSN")
            (completed / "database.sql.gz").symlink_to("/nonexistent-private-target")
            observed = CORE.restore_path_diagnostics({"backup_output": output})
            by_label = {entry["label"]: entry for entry in observed}
            self.assertEqual(by_label["backup_output"]["mode"], "0o700")
            self.assertTrue(by_label["backup_child_0_metadata"]["regular_file"])
            self.assertFalse(by_label["backup_child_0_archive"]["regular_file"])
            self.assertFalse(by_label["backup_child_0_archive"]["directory"])
            self.assertNotIn("private", json.dumps(observed))
            self.assertNotIn(str(root), json.dumps(observed))

    def test_checked_native_input_fixed_rejections_have_complete_safe_categories(self):
        scripts = SCRIPT.parent.parent / "scripts"
        targets = {
            "core-operation.py": {"protected_path", "read_file", "sha_file", "sha_prepared_member", "_sha_protected_file", "prepared_release", "read_environment", "create_backup_directory", "validate_journal", "reconcile_activation"},
            "update-manager.py": {"stable", "exact_path", "protected_open", "read_protected", "strict_json", "static_elf"},
        }
        fixed = set()
        for filename, names in targets.items():
            parsed = ast.parse((scripts / filename).read_text())
            found = {node.name: node for node in ast.walk(parsed) if isinstance(node, ast.FunctionDef) and node.name in names}
            self.assertEqual(set(found), names)
            for function in found.values():
                for node in ast.walk(function):
                    if isinstance(node, ast.Call):
                        name = node.func.id if isinstance(node.func, ast.Name) else node.func.attr if isinstance(node.func, ast.Attribute) else ""
                        argument = node.args[1] if name == "require" and len(node.args) >= 2 else node.args[0] if name == "Rejected" and node.args else None
                        if isinstance(argument, ast.Constant) and isinstance(argument.value, str):
                            fixed.add(argument.value)
        self.assertGreaterEqual(len(fixed), 40)
        self.assertEqual(fixed - CORE.SAFE_REJECTIONS.keys(), set())
        for message in fixed:
            self.assertRegex(CORE.SAFE_REJECTIONS[message], r"^[a-z][a-z0-9_]{1,63}$")
            self.assertNotEqual(CORE.SAFE_REJECTIONS[message], message)
            observed = CORE.failure_diagnostics(SimpleNamespace(fixtures=[]), RuntimeError(message))
            self.assertEqual(observed["reason"], CORE.SAFE_REJECTIONS[message])
            self.assertNotIn(message, json.dumps(observed))

    def test_legacy_api_report_projection_never_echoes_arbitrary_values(self):
        raw = json.dumps({"schema": 1, "read_only": True, "configuration_checked": True,
            "error": "private-provider-password", "database": {"status": "pending_migrations", "schema_version": 6,
            "required_schema_version": 7, "error": "private-database-DSN", "database": "private-name"}}).encode()
        observed = CORE.redacted_preflight_report(raw)
        self.assertTrue(observed["schema_is_one"])
        self.assertTrue(observed["configuration_checked"])
        self.assertTrue(observed["error_present"])
        self.assertEqual(observed["database_status"], "pending_migrations")
        self.assertEqual(observed["schema_version"], 6)
        self.assertNotIn("private", json.dumps(observed))
        wrong = CORE.redacted_preflight_report(b'{"schema":true,"read_only":"private","database":{"status":[],"schema_version":true,"required_schema_version":999}}')
        self.assertFalse(wrong["schema_is_one"])
        self.assertIsNone(wrong["read_only"])
        self.assertIsNone(wrong["schema_version"])
        self.assertIsNone(wrong["required_schema_version"])
        self.assertEqual(wrong["database_status"], "other_or_absent")
        self.assertEqual(CORE.redacted_preflight_report(b"private-native-stderr"), {"json_valid": False})
        self.assertTrue(CORE.redacted_preflight_report(b"x" * ((128 << 10) + 1))["exceeded_json_bound"])

    def test_legacy_api_observer_forwards_actual_result_and_preserves_strict_failure(self):
        class Rejected(RuntimeError):
            pass
        calls = []
        result = subprocess.CompletedProcess(["private-command"], 1, b'{"schema":1,"read_only":true,"configuration_checked":true,"database":{"status":"incompatible","schema_version":6}}')
        class Native:
            def __init__(self, spec):
                self.spec = spec
            def api(self, release, environment, command_name, allow_failure=False):
                calls.append((release, environment, command_name, allow_failure))
                return result
        def require(ok, message):
            if not ok:
                raise Rejected(message)
        executor = SimpleNamespace(Native=Native, require=require)
        trace = {"calls": []}
        native = CORE.legacy_native(executor, {}, "/private-candidate", trace)
        environment = {"MYSQL_DSN_FILE": "/private-credential"}
        self.assertIs(native.api("/private-candidate", environment, "-preflight", allow_failure=True), result)
        self.assertEqual(calls, [("/private-candidate", environment, "-preflight", True)])
        with self.assertRaisesRegex(Rejected, "raw diagnostics were suppressed"):
            native.api("/private-baseline", environment, "-migrate")
        self.assertTrue(trace["calls"][-1]["completed"])
        self.assertEqual(trace["calls"][-1]["exit_code"], 1)
        self.assertEqual(trace["calls"][-1]["rejection"], "fixed_native_command_failed")
        self.assertNotIn("private", json.dumps(trace))

    def test_selected_unit_diagnostics_only_emit_expected_checks_and_bounded_categories(self):
        spec = {"api_unit": "aster-fixture.service", "instance_root": "/opt/aster-fixture",
                "config_root": "/etc/aster-fixture", "runtime_user": "astfixture"}
        executable = spec["instance_root"] + "/current/bin/aster-api"
        values = {"LoadState": "loaded", "ActiveState": "failed", "SubState": "failed", "Result": "exit-code",
            "FragmentPath": "/etc/systemd/system/" + spec["api_unit"], "DropInPaths": "",
            "User": "astfixture", "Group": "astfixture", "WorkingDirectory": spec["instance_root"] + "/current",
            "ExecStart": "{ path=" + executable + " ; argv[]=" + executable + " ; ignore_errors=no ; }",
            "EnvironmentFiles": spec["config_root"] + "/api.env (ignore_errors=no)",
            "ExecStartPre": "", "ExecStartPost": "", "ExecStop": "", "ExecReload": "",
            "ExecMainCode": "1", "ExecMainStatus": "203", "StatusErrno": "0"}
        report = CORE.selected_unit_diagnostics(spec, "api", values)
        self.assertTrue(all(report["checks"].values()))
        self.assertEqual(report["ExecMainStatus"], 203)
        for key in ("ExecStartPre", "ExecStartPost", "ExecStop", "ExecReload"):
            values.pop(key)
        missing = CORE.selected_unit_diagnostics(spec, "api", values)
        for key in ("ExecStartPre", "ExecStartPost", "ExecStop", "ExecReload"):
            self.assertNotIn(key, missing["properties_present"])
            self.assertNotIn(key + "_empty", missing["checks"])
        self.assertEqual(missing["checks"], report["checks"])
        self.assertTrue(missing["cli_hook_serialization_ignored"])
        self.assertEqual(missing["hook_verification_source"], "systemd-dbus-json")
        values.update(ExecStart="private-command-with-secret", EnvironmentFiles="private-credentials",
                      ActiveState="private-provider-reply", ExecMainStatus="private-password")
        report = CORE.selected_unit_diagnostics(spec, "api", values)
        self.assertFalse(report["checks"]["exec_start_parsed"])
        self.assertFalse(report["checks"]["environment_files_expected"])
        self.assertEqual(report["ActiveState"], "other_or_absent")
        self.assertIsNone(report["ExecMainStatus"])
        self.assertNotIn("private-", json.dumps(report))
        self.assertNotIn(spec["config_root"], json.dumps(report))

    def test_native_diagnostic_wrapper_forwards_results_and_suppresses_exception_bodies(self):
        class Native:
            def __init__(self, spec):
                self.spec = spec
            def service(self, verb, unit):
                return "private-native-result"
            def validate_service(self, release):
                raise RuntimeError("private-command-DSN-password")
            def verify_empty_service_hooks(self):
                return {name: True for name in ("ExecCondition", "ExecStartPre", "ExecStartPost", "ExecStop", "ExecStopPost", "ExecReload")}
        executor = SimpleNamespace(Native=Native, edge_unit=lambda spec: "aster-fixture-edge.service")
        spec = {"operation_id": "d" * 32, "api_unit": "aster-fixture.service"}
        CORE.NATIVE_TRACES.pop(spec["operation_id"], None)
        native = CORE.diagnostic_native(executor)(spec)
        self.assertEqual(native.service("start", spec["api_unit"]), "private-native-result")
        self.assertTrue(all(native.verify_empty_service_hooks().values()))
        with self.assertRaisesRegex(RuntimeError, "private-command"):
            native.validate_service("/private-release")
        trace = CORE.NATIVE_TRACES.pop(spec["operation_id"])
        self.assertEqual(trace["first_failure"], {"substep": "validate-service", "event": "failed", "reason": "failure_details_suppressed"})
        self.assertEqual(trace["typed_service_hooks"], {"source": "systemd-dbus-json", "verified": True})
        self.assertNotIn("private-", json.dumps(trace))

    def test_real_umask_077_keeps_new_public_ancestors_traversable_and_secrets_private(self):
        with tempfile.TemporaryDirectory(prefix="aster-core-umask-") as directory:
            root = Path(directory)
            control, mount, instance = root / "control", root / "socket", root / "instance"
            previous = os.umask(0o077)
            try:
                CORE.create_public_controls(control, mount)
                CORE.create_release_parent(instance)
                private = control / "private"
                private.mkdir(mode=0o700)
                CORE.write(private / "secret", "synthetic-fixture-secret")
            finally:
                os.umask(previous)
            for path in (control, control / "public", mount, instance, instance / "releases"):
                self.assertEqual(path.stat().st_mode & 0o7777, 0o755, str(path))
            self.assertEqual(private.stat().st_mode & 0o7777, 0o700)
            self.assertEqual((private / "secret").stat().st_mode & 0o7777, 0o600)
            # An already prepared public baseline is accepted without mutation.
            CORE.create_release_parent(instance)
            instance.chmod(0o700)
            with self.assertRaises(CORE.AcceptanceFailure):
                CORE.create_release_parent(instance)
            self.assertEqual(instance.stat().st_mode & 0o7777, 0o700)

    def test_failure_diagnostics_emit_fixed_reason_and_metadata_without_exception_body(self):
        with tempfile.TemporaryDirectory(prefix="aster-core-diagnostics-") as directory:
            root = Path(directory)
            control, mount, instance = root / "control", root / "socket", root / "instance"
            CORE.create_public_controls(control, mount)
            CORE.create_release_parent(instance)
            def no_checkpoint():
                raise RuntimeError("private SQL/password/DSN must not escape")
            fixture = SimpleNamespace(root=instance, config=root / "config", state=root / "state", status=no_checkpoint,
                spec={"action": "install", "operation_id": "a" * 32, "status_file": str(control / "public/status.json")})
            resources = SimpleNamespace(control=control, mount=mount, fixtures=[fixture])
            output = CORE.failure_diagnostics(resources, RuntimeError("private SQL/password/DSN must not escape"))
            self.assertEqual(output["reason"], "unclassified_failure_details_suppressed")
            self.assertFalse(output["fixtures"][0]["checkpoint_available"])
            self.assertNotIn("private SQL/password/DSN", json.dumps(output))
            metadata = {row["label"]: row for row in output["fixtures"][0]["path_metadata"]}
            self.assertEqual(metadata["status_parent"]["mode"], "0o755")
            fixed = CORE.failure_diagnostics(resources, RuntimeError("Public status ancestors must be traversable by the non-root API."))
            self.assertEqual(fixed["reason"], "public_status_parent_not_traversable")

    def test_failed_native_command_never_exposes_raw_diagnostics(self):
        with self.assertRaises(CORE.AcceptanceFailure) as failure:
            CORE.command(["python3", "-c", "import sys; print('private-dsn'); print('private-password',file=sys.stderr); sys.exit(17)"])
        self.assertIn("exit 17", str(failure.exception))
        self.assertNotIn("private-dsn", str(failure.exception))
        self.assertNotIn("private-password", str(failure.exception))

    def test_evidence_is_created_without_overwriting_existing_facts(self):
        with tempfile.TemporaryDirectory(prefix="aster-core-facts-") as directory:
            root = Path(directory)
            CORE.fact(root, "fixture", {"checked": True})
            self.assertEqual(json.loads((root / "fixture.json").read_text()), {"checked": True})
            with self.assertRaises(FileExistsError):
                CORE.fact(root, "fixture", {"checked": False})
            self.assertEqual(json.loads((root / "fixture.json").read_text()), {"checked": True})

    def test_noncanonical_fact_name_cannot_escape_evidence_directory(self):
        with tempfile.TemporaryDirectory(prefix="aster-core-facts-") as directory:
            for name in ("../outside", "/absolute", "contains space", "", "secret\nvalue"):
                with self.subTest(name=name), self.assertRaises(CORE.AcceptanceFailure):
                    CORE.fact(directory, name, {})
            self.assertEqual(list(Path(directory).iterdir()), [])


if __name__ == "__main__":
    unittest.main()
