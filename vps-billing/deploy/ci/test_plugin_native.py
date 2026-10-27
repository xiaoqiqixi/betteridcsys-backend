#!/usr/bin/env python3
"""Offline guards and real local TLS fixture tests, not systemd acceptance."""
import concurrent.futures
import hashlib
import importlib.util
import os
from pathlib import Path
import smtplib
import ssl
import subprocess
import tempfile
import unittest
from unittest.mock import patch


HERE = Path(__file__).resolve().parent
SCRIPT = HERE / "plugin-native.sh"


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    # dataclass metadata needs its defining module in sys.modules.
    import sys
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


FIXTURE = load("plugin_smtp_loopback_test", HERE / "smtp-loopback-fixture.py")
NATIVE = load("plugin_native_test", HERE / "plugin-native.py")
INSTALLER = load("plugin_native_installer_test", HERE.parent / "scripts/plugin-manager.py")


class SafeDiagnosticTests(unittest.TestCase):
    def setUp(self):
        previous = NATIVE.DIAGNOSTICS
        NATIVE.DIAGNOSTICS = NATIVE.SafeDiagnostics()
        self.addCleanup(setattr, NATIVE, "DIAGNOSTICS", previous)

    def test_real_failed_subprocess_records_status_without_exposing_argv_or_stderr(self):
        marker = "synthetic-sensitive-diagnostic-marker"
        with self.assertRaises(NATIVE.AcceptanceFailure) as failure:
            NATIVE.command(["python3", "-c", "import sys;sys.stderr.write('" + marker + "');sys.exit(17)"])
        self.assertIn("expected exit 0, actual exit 17", str(failure.exception))
        self.assertNotIn(marker, str(failure.exception))
        self.assertEqual(NATIVE.DIAGNOSTICS.last_command,
                         {"command": "python3", "expected_exit_code": 0, "actual_exit_code": 17})

    def test_command_names_strip_sudo_identity_environment_code_and_unknown_basename(self):
        marker = "synthetic-sensitive-diagnostic-marker"
        self.assertEqual(NATIVE.command_name(["sudo", "-n", "-u", "fixture-user", "-g", "fixture-group",
            "env", "-i", "UNEXPECTED_SECRET=" + marker, "python3", "-c", marker]), "python3")
        self.assertEqual(NATIVE.command_name(["sudo", "-n", "systemctl", "restart", marker]),
                         "systemctl restart")
        self.assertEqual(NATIVE.command_name(["/tmp/" + marker]), "fixture-command")

    def test_api_status_failure_never_includes_payload_or_headers(self):
        import io
        import json
        marker = "synthetic-sensitive-provider-text"

        class Response(io.BytesIO):
            status = 503

        class Opener:
            def open(self, request, timeout):
                return Response(json.dumps({"error": {"message": marker}}).encode())

        client = NATIVE.API.__new__(NATIVE.API)
        client.origin, client.csrf, client.opener = "https://localhost:4443", "fixture-csrf", Opener()
        client.accounts = {key: "credential-" + key for key in ("smtp_username", "smtp_password", "session", "admin")}
        with self.assertRaises(NATIVE.AcceptanceFailure) as failure:
            client.request("/api/v1/admin/plugins/check", "POST", {})
        self.assertIn("expected 200, actual 503", str(failure.exception))
        self.assertNotIn(marker, str(failure.exception))
        self.assertEqual(NATIVE.DIAGNOSTICS.last_api, {"method": "POST", "path": "/api/v1/admin/plugins/check",
                         "expected_status": 200, "actual_status": 503})


class SupportedSystemctlTests(unittest.TestCase):
    def test_only_an_advertised_exact_kill_selector_is_used(self):
        for output, expected in (
                (b"     --kill-who=WHO      Whom to send signal to\n", "--kill-who"),
                (b"     --kill-whom=WHOM    Whom to send signal to\n", "--kill-whom"),
                (b" --kill-who=WHO\n --kill-whom=WHOM\n", "--kill-whom")):
            with self.subTest(expected=expected):
                self.assertEqual(NATIVE.systemctl_kill_scope(output), expected)
        for output in (b"", b"systemctl 249", b"--kill-whatever=all", b"--kill-who no parameter", b"text--kill-who=WHO", None, "--kill-who=WHO", b" " * 65537):
            with self.subTest(kind=type(output).__name__, size=len(output) if output is not None else None), self.assertRaises(NATIVE.AcceptanceFailure):
                NATIVE.systemctl_kill_scope(output)

    def test_both_selectors_kill_only_the_owned_main_with_the_same_signal_once(self):
        for selector in ("--kill-who", "--kill-whom"):
            fixture = NATIVE.NativePluginAcceptance.__new__(NATIVE.NativePluginAcceptance)
            fixture.worker_unit = "aster-ci-smtp-123-1-456.service"
            fixture.kill_scope = selector
            with self.subTest(selector=selector), patch.object(NATIVE, "privileged") as command:
                fixture.kill_worker_main()
                command.assert_called_once_with("systemctl", "kill", selector + "=main", "--signal=KILL", fixture.worker_unit)
        for unit, selector in (("sshd.service", "--kill-who"), ("aster-ci-smtp-123-1-456.service", "--kill-all")):
            fixture.worker_unit, fixture.kill_scope = unit, selector
            with self.subTest(unit=unit, selector=selector), patch.object(NATIVE, "privileged") as command, self.assertRaises(NATIVE.AcceptanceFailure):
                fixture.kill_worker_main()
            command.assert_not_called()

    def test_plugin_and_payment_evidence_use_the_actual_supported_os(self):
        for version in ("22.04", "24.04"):
            with self.subTest(version=version), patch.object(Path, "read_text", return_value='ID=ubuntu\nVERSION_ID="' + version + '"\n'):
                self.assertEqual(NATIVE.runner_environment(), "isolated Ubuntu " + version + " GitHub-hosted runner")
        for text in ('ID=ubuntu\nVERSION_ID="26.04"\n', 'ID=debian\nVERSION_ID="12"\n', ''):
            with self.subTest(text=text), patch.object(Path, "read_text", return_value=text), self.assertRaises(NATIVE.AcceptanceFailure):
                NATIVE.runner_environment()
        self.assertIn('"environment": runner_environment()', (HERE / "plugin-native.py").read_text())
        self.assertIn('"environment": shared.runner_environment()', (HERE / "payment-native.py").read_text())
        for filename in ("plugin-native.py", "payment-native.py"):
            self.assertIn('"systemctl_kill_selector": self.kill_scope', (HERE / filename).read_text())


class IsolatedCanaryBootstrapTests(unittest.TestCase):
    def test_no_identity_cleanup_stops_the_real_owned_child(self):
        process = NATIVE.start_canary_process(["/usr/bin/python3", "-I", "-c", "import time;time.sleep(30)"])
        NATIVE.stop_canary_process(process, None)
        self.assertIsNotNone(process.poll())
        self.assertNotEqual(process.returncode, 0)

    def test_diagnostic_pipe_read_does_not_wait_for_a_live_writer(self):
        reader, writer = os.pipe()
        with os.fdopen(reader, "rb") as stream:
            try:
                self.assertEqual(NATIVE.bounded_pipe_diagnostic(stream), b"")
                os.write(writer, b"CANARY_BOOTSTRAP_ERROR=PermissionError\n")
                self.assertEqual(NATIVE.bounded_pipe_diagnostic(stream), b"CANARY_BOOTSTRAP_ERROR=PermissionError\n")
            finally:
                os.close(writer)

    def test_peer_probe_accepts_real_connection_closure_and_rejects_any_response(self):
        import socket
        import threading
        import time
        expected_headers = (b"POST /handshake HTTP/1.1\r\nHost: plugin.local\r\n"
                            b"Content-Type: application/json\r\nContent-Length: 0\r\n\r\n")
        for payload in (b"", b"HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\n\r\n"):
            with self.subTest(payload=bool(payload)), tempfile.TemporaryDirectory(prefix="aster-peer-probe-") as directory:
                endpoint = str(Path(directory) / "probe.sock")
                received_headers, fixture_errors = [], []
                with socket.socket(socket.AF_UNIX) as listener:
                    listener.bind(endpoint)
                    listener.listen(1)
                    listener.settimeout(3)

                    def reject():
                        try:
                            connection, _ = listener.accept()
                            with connection:
                                headers = bytearray()
                                deadline = time.monotonic() + 2
                                while b"\r\n\r\n" not in headers:
                                    remaining = deadline - time.monotonic()
                                    if remaining <= 0 or len(headers) >= 4096:
                                        raise TimeoutError("Peer-probe fixture headers exceeded bounds")
                                    connection.settimeout(remaining)
                                    chunk = connection.recv(4096 - len(headers))
                                    if not chunk:
                                        raise EOFError("Peer-probe fixture request ended before headers")
                                    headers.extend(chunk)
                                received_headers.append(bytes(headers))
                                if bytes(headers) != expected_headers:
                                    raise ValueError("Peer-probe fixture request headers differ")
                                if payload:
                                    connection.sendall(payload)
                                    connection.shutdown(socket.SHUT_WR)
                                else:
                                    connection.shutdown(socket.SHUT_RDWR)
                        except Exception as error:
                            fixture_errors.append(type(error).__name__)

                    thread = threading.Thread(target=reject)
                    thread.start()
                    result = subprocess.run(["/usr/bin/python3", "-I", "-c", NATIVE.WRONG_UID_PROBE, endpoint],
                                            cwd="/", capture_output=True, timeout=5)
                    thread.join(3)
                    self.assertFalse(thread.is_alive())
                    self.assertEqual(fixture_errors, [])
                    self.assertEqual(received_headers, [expected_headers])
                    self.assertEqual(result.returncode, 3 if payload else 0, result.stderr.decode())

    def test_real_nonroot_isolated_exec_preserves_canary_descriptor_before_host_cleanup(self):
        import json
        self.assertNotEqual(os.geteuid(), 0, "This regression must run as an ordinary account")
        with tempfile.TemporaryDirectory(prefix="aster-canary-bootstrap-") as directory:
            root = Path(directory)
            canary = root / "nonsecret-canary"
            canary.write_text("synthetic-descriptor-canary")
            target = root / "inspector"
            target.write_text("#!/usr/bin/python3\nimport json,os\n"
                "values=[]\nfor name in os.listdir('/proc/self/fd'):\n"
                " try:\n  fd=int(name)\n  if fd>2 and os.readlink('/proc/self/fd/'+name)=="
                + repr(str(canary)) + ": values.append([fd,os.get_inheritable(fd),os.read(fd,64).decode()])\n"
                " except FileNotFoundError: pass\n"
                "print(json.dumps({'cwd':os.getcwd(),'uid':os.geteuid(),'descriptors':values}))\n")
            target.chmod(0o755)
            process = NATIVE.start_canary_process(["/usr/bin/python3", "-I", "-c", NATIVE.CANARY_WRAPPER,
                str(target), str(root), str(root / "policy"), str(canary)])
            output, errors = process.communicate(timeout=5)
            self.assertEqual(process.returncode, 0, errors.decode())
            identity, raw = output.splitlines()
            values = identity.split()
            self.assertEqual(len(values), 2)
            self.assertTrue(all(value.isdigit() for value in values))
            result = json.loads(raw)
            self.assertEqual(result["cwd"], "/")
            self.assertEqual(result["uid"], os.geteuid())
            self.assertEqual(result["descriptors"], [[int(values[1]), True, "synthetic-descriptor-canary"]])

    def test_preidentity_open_failure_exposes_only_fixed_class_and_numeric_exit(self):
        with tempfile.TemporaryDirectory(prefix="aster-canary-missing-") as directory:
            missing = str(Path(directory) / "synthetic-sensitive-path")
            process = NATIVE.start_canary_process(["/usr/bin/python3", "-I", "-c", NATIVE.CANARY_WRAPPER,
                "/nonexistent", directory, directory, missing])
            output, errors = process.communicate(timeout=5)
            self.assertEqual(process.returncode, 70)
            self.assertEqual(output, b"")
            self.assertEqual(errors, b"CANARY_BOOTSTRAP_ERROR=FileNotFoundError\n")
            self.assertNotIn(missing.encode(), errors)
            self.assertEqual(NATIVE.canary_failure_class(errors), "FileNotFoundError")


class PluginNativeGuardTests(unittest.TestCase):
    def guard(self, settings, arguments=None):
        with tempfile.TemporaryDirectory(prefix="aster-plugin-guard-") as directory:
            root = Path(directory)
            marker = root / "sudo-ran"
            sudo = root / "sudo"
            sudo.write_text("#!/bin/sh\n: > '" + str(marker) + "'\nexit 99\n")
            sudo.chmod(0o755)
            environment = {"PATH": str(root) + ":" + os.environ["PATH"], **settings}
            result = subprocess.run(["bash", str(SCRIPT), *(arguments if arguments is not None else
                ["--allow-isolated-runner", str(root), str(root / "evidence")])],
                env=environment, capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(marker.exists(), result.stdout + result.stderr)
            self.assertFalse((root / "evidence").exists())
            return result.stderr

    def test_explicit_opt_in_required_before_any_mutation(self):
        self.assertIn("Explicit isolated-runner", self.guard({}, []))

    def test_no_local_or_self_hosted_privileged_execution(self):
        for values in ({}, {"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "self-hosted"}):
            with self.subTest(values=values):
                self.assertIn("GitHub-hosted", self.guard(values))

    def test_other_repository_rejected_before_sudo(self):
        self.assertIn("Unexpected repository", self.guard({"GITHUB_ACTIONS": "true",
            "RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_REPOSITORY": "another/project"}))

    def test_malformed_run_identity_rejected_before_sudo(self):
        for value in ("", "../other", "1;false", "arbitrary"):
            with self.subTest(value=value):
                self.assertIn("Missing run identity", self.guard({"GITHUB_ACTIONS": "true",
                    "RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_REPOSITORY": "xiaoqiqixi/betteridcsys",
                    "GITHUB_RUN_ID": value, "GITHUB_RUN_ATTEMPT": "1"}))


class LoopbackSMTPFixtureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix="aster-plugin-tls-")
        cls.addClassCleanup(cls.directory.cleanup)
        cls.root = Path(cls.directory.name)
        cls.certificate, cls.key = cls.root / "localhost.pem", cls.root / "localhost.key"
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
            "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost", "-keyout", str(cls.key),
            "-out", str(cls.certificate)], check=True, capture_output=True, timeout=15)

    def setUp(self):
        self.fixture = FIXTURE.LoopbackSMTP(str(self.certificate), str(self.key), "fixture-user", "fixture-password").start()
        self.addCleanup(self.fixture.close)
        self.context = ssl.create_default_context(cafile=str(self.certificate))

    def client(self, context=None):
        return smtplib.SMTP_SSL("localhost", self.fixture.port, timeout=5, context=context or self.context)

    def send(self):
        with self.client() as smtp:
            smtp.login("fixture-user", "fixture-password")
            smtp.sendmail("smtp@native-ci.invalid", "operator@native-ci.invalid",
                          "Subject: Aster SMTP configuration test\r\n\r\nSynthetic fixture only\r\n")

    def test_real_verified_tls_auth_and_fixed_message_counters_without_relay(self):
        self.send()
        report = self.fixture.report()
        self.assertEqual(report["accepted"], 1)
        self.assertEqual(report["authenticated"], 1)
        self.assertEqual(report["relayed_messages"], 0)
        self.assertTrue(report["loopback_only"])
        self.assertFalse(report["message_content_recorded"])
        self.assertEqual(self.fixture.server.server_address[0], "127.0.0.1")

    def test_external_recipient_and_wrong_password_cannot_be_accepted(self):
        with self.client() as smtp:
            smtp.login("fixture-user", "fixture-password")
            with self.assertRaises(smtplib.SMTPRecipientsRefused):
                smtp.sendmail("smtp@native-ci.invalid", "someone@example.com", "Subject: synthetic\r\n\r\nbody")
        with self.client() as smtp:
            with self.assertRaises(smtplib.SMTPAuthenticationError):
                smtp.login("fixture-user", "wrong-password")
        self.assertEqual(self.fixture.report()["accepted"], 0)

    def test_unknown_certificate_is_rejected_by_real_client(self):
        with self.assertRaises(ssl.SSLCertVerificationError):
            self.client(ssl.create_default_context())
        self.assertEqual(self.fixture.report()["authenticated"], 0)

    def test_data_boundary_observable_before_final_reply_for_crash_test(self):
        self.fixture.hold_reply.set()
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            sent = pool.submit(self.send)
            self.assertTrue(self.fixture.data_received.wait(3))
            self.assertEqual(self.fixture.report()["accepted"], 1)
            self.assertFalse(sent.done())
            self.fixture.release_reply.set()
            sent.result(timeout=3)


class NativeIndependentPackageTests(unittest.TestCase):
    def test_real_independent_smtp_build_fixture_matches_production_installer_contract(self):
        source = HERE.parents[2]
        with tempfile.TemporaryDirectory(prefix="aster-plugin-package-") as directory:
            package, binary, manifest = NATIVE.fixture_package(source, Path(directory), "1.1.1")
            trust = INSTALLER._Trust(owner=os.geteuid(), ancestor=os.stat("/").st_uid)
            resources_pin = hashlib.sha256(package.read_bytes()).hexdigest()
            binary_pin = hashlib.sha256(binary.read_bytes()).hexdigest()
            verified = INSTALLER.verify_package(str(package), resources_pin, "0.5.0", trust=trust,
                binary=str(binary), binary_sha256=binary_pin)
            self.assertEqual(verified.manifest, manifest)
            self.assertEqual(set(verified.files), INSTALLER.FILES)
            self.assertEqual(manifest["version"], "1.1.1")
            self.assertIn(b"never publish", verified.files["README.md"])
            self.assertEqual((manifest["core_min"], manifest["core_max_exclusive"]), ("0.4.0", "0.6.0"))
            for raw_pin, metadata_pin in (("0" * 64, resources_pin), (binary_pin, "0" * 64)):
                with self.subTest(raw_pin_matches=raw_pin == binary_pin), self.assertRaises(INSTALLER.Rejected):
                    INSTALLER.verify_package(str(package), metadata_pin, "0.5.0", trust=trust,
                        binary=str(binary), binary_sha256=raw_pin)
            import tarfile
            with tarfile.open(package, "r:gz") as archive:
                self.assertNotIn("plugin", [Path(name).name for name in archive.getnames()])


if __name__ == "__main__":
    unittest.main()
