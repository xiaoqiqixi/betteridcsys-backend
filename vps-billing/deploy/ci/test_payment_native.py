#!/usr/bin/env python3
"""Offline payment acceptance guards and real synthetic package contract tests.

These tests compile/run only the fixture's --version command. They never start a
payment service, change OS identities, run privileged actions, or move money.
"""
import hashlib
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


HERE = Path(__file__).resolve().parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


NATIVE = load("payment_native_offline", HERE / "payment-native.py")
INSTALLER = load("payment_native_manager", HERE.parent / "scripts/plugin-manager.py")
UPDATER = load("payment_native_updater", HERE.parent / "scripts/update-manager.py")


class PaymentNativeGuards(unittest.TestCase):
    def test_payment_crash_uses_the_same_owned_main_scope_for_both_systemd_capabilities(self):
        self.assertIs(NATIVE.NativePaymentAcceptance.kill_worker_main,
                      NATIVE.shared.NativePluginAcceptance.kill_worker_main)
        for selector in ("--kill-who", "--kill-whom"):
            fixture = NATIVE.NativePaymentAcceptance.__new__(NATIVE.NativePaymentAcceptance)
            fixture.worker_unit = "aster-ci-payment-123-1-456.service"
            fixture.kill_scope = selector
            with self.subTest(selector=selector), patch.object(NATIVE.shared, "privileged") as command:
                fixture.kill_worker_main()
                command.assert_called_once_with("systemctl", "kill", selector + "=main", "--signal=KILL", fixture.worker_unit)
        source = (HERE / "payment-native.py").read_text()
        self.assertIn("self.kill_worker_main()", source)
        self.assertNotIn("--kill-whom=main", source)

    def test_failure_metadata_captures_live_worker_and_credential_modes_without_payloads(self):
        fixture = NATIVE.NativePaymentAcceptance.__new__(NATIVE.NativePaymentAcceptance)
        fixture.worker_unit = "aster-ci-payment-1-1-1.service"
        fixture.socket = Path("/run/aster-ci-1-1-1/payment/plugin.sock")
        fixture.config = Path("/etc/aster-ci-1-1-1")
        marker = b"synthetic-sensitive-output-that-must-not-be-published"
        digest = b"a" * 64
        def response(arguments, **options):
            self.assertEqual(arguments[:2], ["sudo", "-n"])
            self.assertEqual(options["cwd"], "/")
            self.assertLessEqual(options["timeout"], 5)
            if arguments[2:4] == ["systemctl", "show"]:
                raw = (b"LoadState=loaded\nActiveState=active\nSubState=running\nResult=success\nMainPID=1234\nNRestarts=0\n"
                       b"ExecMainCode=0\nExecMainStatus=0\nStatusErrno=0\nUnknown=" + marker + b"\n")
            elif arguments[2] == "stat":
                raw = b"c1b0 2003 2004 660 1\n" if arguments[-1] == str(fixture.socket) else b"8120 0 0 440 1\n"
            elif arguments[2] == "cat":
                raw = b"Name:\t" + marker + b"\nUid:\t2003\t2003\t2003\t2003\n"
            elif arguments[2] == "sha256sum":
                raw = digest + b"  " + marker + b"\n"
            else:
                self.fail("Unexpected diagnostic command")
            return subprocess.CompletedProcess(arguments, 0, raw, marker)
        with patch.object(NATIVE.subprocess, "run", side_effect=response):
            value = fixture.failure_diagnostics()
        self.assertEqual(value["service"]["MainPID"], 1234)
        self.assertEqual(value["process_uids"], [2003] * 4)
        self.assertEqual(value["process_executable_sha256"], digest.decode())
        self.assertEqual(value["socket"]["kind"], "socket")
        self.assertEqual(value["credential_file"]["mode"], "440")
        self.assertEqual(value["credential_file"]["links"], 1)
        self.assertNotIn(marker.decode(), str(value))

    def test_failed_metadata_probe_stays_bounded_and_does_not_require_created_credential(self):
        fixture = NATIVE.NativePaymentAcceptance.__new__(NATIVE.NativePaymentAcceptance)
        fixture.worker_unit = "aster-ci-payment-1-1-1.service"
        fixture.socket = Path("/run/aster-ci-1-1-1/payment/plugin.sock")
        fixture.config = Path("/etc/aster-ci-1-1-1")
        with patch.object(NATIVE.subprocess, "run", side_effect=subprocess.TimeoutExpired("fixed-probe", 5)):
            value = fixture.failure_diagnostics()
        self.assertEqual(value, {"socket": {"status": "unavailable"}, "credential_source": {"status": "unavailable"}, "service": {}})

    def test_missing_opt_in_or_non_hosted_environment_reject_before_output_creation(self):
        with tempfile.TemporaryDirectory(prefix="aster-payment-guard-") as directory:
            output = Path(directory) / "evidence"
            for arguments in ([], ["--allow-isolated-runner", str(HERE.parents[2]), str(output)]):
                result = subprocess.run(["bash", str(HERE / "payment-native.sh"), *arguments],
                    env={"PATH": os.environ["PATH"]}, capture_output=True, timeout=5)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(output.exists())

    def test_new_payment_policy_preserves_exact_published_executor_inventory(self):
        self.assertEqual(UPDATER.trusted_paths_for_core("0.5.2"), UPDATER.EXECUTOR_TRUSTED_PATHS)
        self.assertEqual(UPDATER.trusted_paths_for_core("0.5.3"), UPDATER.PAYMENT_TRUSTED_PATHS)
        self.assertEqual(UPDATER.trusted_paths_for_core("0.5.9"), UPDATER.PAYMENT_TRUSTED_PATHS)
        self.assertEqual(UPDATER.PAYMENT_TRUSTED_PATHS - UPDATER.EXECUTOR_TRUSTED_PATHS, {
            "vps-billing/deploy/ci/payment-native.sh", "vps-billing/deploy/ci/payment-native.py",
            "vps-billing/deploy/ci/test_payment_native.py", "vps-billing/deploy/systemd/aster-payment.service"})
        self.assertEqual(UPDATER.trusted_paths_for_core("0.5.10"), UPDATER.TRUSTED_PATHS)
        self.assertEqual(UPDATER.TRUSTED_PATHS - UPDATER.PAYMENT_TRUSTED_PATHS, {
            ".github/workflows/ci.yml", "vps-billing/deploy/ci/mysql-distribution.py",
            "vps-billing/deploy/ci/mysql-distribution-ubuntu22.json",
            "vps-billing/deploy/ci/browser-mysql.sh", "vps-billing/deploy/ci/browser-mysql.py",
            "vps-billing/deploy/ci/browser-mysql-fixture.gotxt",
            "vps-billing/frontend/tests/browser-mysql.mjs",
            "vps-billing/frontend/tests/ui-copy-boundaries.mjs"})

    def test_source_built_fixture_raw_binary_and_resources_match_installer_contract(self):
        source = HERE.parents[2]
        trust = INSTALLER._Trust(os.geteuid(), os.stat("/").st_uid)
        with tempfile.TemporaryDirectory(prefix="aster-payment-package-") as directory:
            scratch = Path(directory)
            hashes = []
            for version in ("0.1.0", "0.1.1"):
                with self.subTest(version=version):
                    resource, binary, manifest = NATIVE.fixture_package(source, scratch, version)
                    resource.chmod(0o644)
                    binary.chmod(0o755)
                    pin = hashlib.sha256(resource.read_bytes()).hexdigest()
                    package = INSTALLER.verify_package(str(resource), pin, trust=trust, binary=str(binary),
                        binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest())
                    self.assertEqual(package.manifest, manifest)
                    self.assertEqual(package.manifest["payment"]["checkout_hosts"], ["checkout.example.test"])
                    hashes.append(manifest["sha256"])
                    with self.assertRaises(INSTALLER.Rejected):
                        INSTALLER.verify_package(str(resource), pin, trust=trust, binary=str(binary), binary_sha256="0" * 64)
            self.assertEqual(len(hashes), 2)
            self.assertNotEqual(hashes[0], hashes[1])


if __name__ == "__main__":
    unittest.main()
