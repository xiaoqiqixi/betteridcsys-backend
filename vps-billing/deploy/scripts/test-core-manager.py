#!/usr/bin/env python3
"""Offline host preparation tests; no credentials, services or database changes.

Official release responses and ELF bytes are immutable local fixtures. The
Python-only trust override permits an ordinary test user to own temporary files;
the operator CLI must retain its root requirement.
"""
import contextlib
import copy
import fcntl
import importlib.util
import io
import os
from pathlib import Path
import socket
import stat
import struct
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


SCRIPTS = Path(__file__).parent
fixtures = load("aster_core_release_test_fixtures", SCRIPTS / "test-update-manager.py")
manager = load("aster_core_manager_under_test", SCRIPTS / "core-manager.py")
updater = manager.updater


def snapshot(root):
    result = {}
    for path in sorted(root.rglob("*")):
        info = path.lstat()
        result[str(path.relative_to(root))] = (
            stat.S_IMODE(info.st_mode), info.st_mtime_ns,
            os.readlink(path) if path.is_symlink() else
            path.read_bytes() if path.is_file() else None,
        )
    return result


class CorePreparationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="aster-core-manager-test-")
        self.base = Path(self.temporary.name)
        self.trust = updater._Trust(os.geteuid(), os.stat("/").st_uid)
        self.fixture = fixtures.Fixture("core", raw=True)
        self.releases = self.base / "releases"
        self.releases.mkdir(mode=0o755)
        self.target = self.releases / self.fixture.version
        self.running = self.base / "running-release"
        self.running.mkdir(mode=0o755)
        (self.running / "canary").write_bytes(b"Existing active program; never executed.\n")
        self.current = self.base / "current"
        self.current.symlink_to(self.running, target_is_directory=True)
        self.active_before = snapshot(self.running)

    def tearDown(self):
        self.temporary.cleanup()

    def prepare(self, **kwargs):
        return manager.prepare(self.fixture.version, str(self.target),
                               api=self.fixture, trust=self.trust, **kwargs)

    def local_assets(self):
        directory = self.base / "assets"
        directory.mkdir(mode=0o700)
        for name, raw in self.fixture.files.items():
            path = directory / name
            path.write_bytes(raw)
            path.chmod(0o644)
        return directory

    def assert_inactive(self):
        self.assertEqual(os.readlink(self.current), str(self.running))
        self.assertEqual(snapshot(self.running), self.active_before)

    def assert_no_publication(self):
        self.assertFalse(self.target.exists())
        self.assertFalse(self.target.is_symlink())
        self.assertEqual(list(self.releases.glob(".prepare-*")), [])
        self.assert_inactive()

    def assert_prepared(self, result):
        expected = {
            "schema": 1, "status": "prepared", "version": self.fixture.version,
            "source_sha": fixtures.SOURCE, "release_directory": str(self.target),
            "release_directory_created": True, "active_release_changed": False,
            "service_action": "none", "database_action": "none",
            "program_executed": False, "source_and_integrity_verified": True,
            "independent_publisher_signature": False,
        }
        for key, value in expected.items():
            self.assertEqual(result[key], value, key)
        self.assertTrue(result["required_operator_checks"])
        record_path = self.target / "manifests/PREPARED.json"
        record = updater.strict_json(record_path.read_bytes())
        self.assertEqual(record["receipt_kind"], "local_operator_preparation_receipt")
        self.assertFalse(record["official_release_inventory"])
        for key, value in expected.items():
            self.assertEqual(record[key], value, key)
        self.assertEqual(record["assets"], self.fixture.assets)
        self.assertEqual(record["files_sha256"], {
            name: updater.digest(raw) for name, (raw, _) in self.fixture.bundle.items()
        })
        actual_files = {str(path.relative_to(self.target))
                        for path in self.target.rglob("*") if path.is_file()}
        self.assertEqual(actual_files, set(self.fixture.bundle) | {"manifests/PREPARED.json"})
        for name, (raw, mode) in self.fixture.bundle.items():
            path = self.target / name
            self.assertEqual(path.read_bytes(), raw, name)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), mode, name)
        for path in self.target.rglob("*"):
            self.assertFalse(path.is_symlink(), path)
            self.assertEqual(path.stat().st_uid, os.geteuid(), path)
            if path.is_dir():
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o755, path)
        self.assertEqual(stat.S_IMODE(self.target.stat().st_mode), 0o755)
        self.assertEqual(stat.S_IMODE(record_path.stat().st_mode), 0o644)
        for name in (".core-manager.lock", "CORE_PREPARE.jsonl"):
            self.assertEqual(stat.S_IMODE((self.releases / name).stat().st_mode), 0o600)
        self.assert_inactive()

    def test_downloaded_core_is_verified_and_published_without_activation(self):
        with patch("subprocess.run", side_effect=AssertionError("A program must not run")), \
                patch("subprocess.Popen", side_effect=AssertionError("A program must not run")):
            self.assert_prepared(self.prepare())
        downloaded = [item for item in self.fixture.calls if type(item) is int]
        self.assertEqual(set(downloaded), {item["id"] for item in self.fixture.assets})

    def test_0510_new_main_source_prepares_without_history_exception(self):
        # A successful fixture is not a claim that v0.5.10 public assets exist.
        self.fixture = fixtures.Fixture("core", raw=True, core_version="0.5.10")
        self.target = self.releases / self.fixture.version
        self.fixture.responses["/compare/" + fixtures.SOURCE + "...main"] = {
            "status": "ahead", "merge_base_commit": {"sha": fixtures.SOURCE}}
        self.assert_prepared(self.prepare())
        self.assertIn("/compare/" + fixtures.SOURCE + "...main", self.fixture.calls)

    def test_0510_prepare_rejects_diverged_source_or_policy_before_publication(self):
        for case in ("diverged_source", "wrong_merge_base", "diverged_policy"):
            with self.subTest(case=case):
                self.fixture = fixtures.Fixture("core", raw=True, core_version="0.5.10")
                self.target = self.releases / self.fixture.version
                source_key = "/compare/" + fixtures.SOURCE + "...main"
                if case == "diverged_policy":
                    policy = "4" * 40
                    self.fixture.provenance["default_sha"] = policy
                    self.fixture.responses["/git/trees/" + policy + "?recursive=1"] = copy.deepcopy(
                        self.fixture.responses["/git/trees/" + fixtures.SOURCE + "?recursive=1"])
                    self.fixture.responses["/compare/" + policy + "...main"] = {
                        "status": "diverged", "merge_base_commit": {"sha": "5" * 40}}
                    self.fixture.reseal()
                else:
                    self.fixture.responses[source_key] = {
                        "status": "diverged" if case == "diverged_source" else "ahead",
                        "merge_base_commit": {"sha": "5" * 40}}
                with self.assertRaisesRegex(updater.Rejected, "retained on main"):
                    self.prepare()
                self.assert_no_publication()

    def test_operating_system_support_is_not_a_full_host_acceptance_claim(self):
        for distro, version, state, support in (
                ("ubuntu", "24.04", "ok", "native_ci_baseline"),
                ("ubuntu", "22.04", "ok", "supported_target"),
                ("ubuntu", "26.04", "warning", "unsupported"),
                ("debian", "12", "warning", "unsupported")):
            with self.subTest(distro=distro, version=version):
                facts = manager.operating_system_support(distro, version)
                self.assertEqual(facts["status"], state)
                self.assertEqual(facts["support_status"], support)
                self.assertEqual(facts["deployment_targets"], ["ubuntu 22.04", "ubuntu 24.04"])
                self.assertEqual(facts["native_ci_baseline"], "ubuntu 24.04")
                self.assertFalse(facts["this_host_validated"])
                self.assertIn("requires successful exact-source native CI", facts["notice"])
                self.assertNotIn("validation is pending", facts["notice"])

    def test_protected_local_assets_avoid_asset_download_but_verify_official_source(self):
        assets = self.local_assets()
        with patch.object(self.fixture, "asset", side_effect=AssertionError("No asset download")):
            self.assert_prepared(self.prepare(assets_directory=str(assets)))
        self.assertIn("repository", self.fixture.calls)
        self.assertIn("/actions/runs/1234", self.fixture.calls)
        self.assertIn("/git/trees/" + fixtures.SOURCE + "?recursive=1", self.fixture.calls)

    def test_existing_release_is_never_overwritten(self):
        self.target.mkdir(mode=0o755)
        (self.target / "canary").write_bytes(b"Existing immutable version\n")
        before = snapshot(self.target)
        with self.assertRaises(updater.Rejected):
            self.prepare()
        self.assertEqual(snapshot(self.target), before)
        self.assert_inactive()

    def test_target_symlink_and_unprotected_ancestor_are_rejected(self):
        self.target.symlink_to(self.running, target_is_directory=True)
        with self.assertRaises(updater.Rejected):
            self.prepare()
        self.target.unlink()
        self.releases.chmod(0o777)
        with self.assertRaises(updater.Rejected):
            self.prepare()
        self.assert_no_publication()

    def test_lost_parent_creation_race_never_chmods_existing_directory(self):
        raced = self.base / "raced-parent"
        mkdir = manager.os.mkdir
        def lost_creation(name, mode=0o777, **kwargs):
            mkdir(name, 0o700, **kwargs)
            raise FileExistsError("An existing directory won the creation race")
        with patch.object(manager.os, "mkdir", side_effect=lost_creation), \
                patch.object(manager.os, "fchmod", side_effect=AssertionError("Cannot chmod an existing directory")):
            descriptor = manager.parent_directory(str(raced), self.trust, create=True)
            os.close(descriptor)
        self.assertEqual(stat.S_IMODE(raced.stat().st_mode), 0o700)
        self.assert_inactive()

    def test_noncanonical_target_and_unstable_version_are_rejected(self):
        for version, target in (("0.5.0-rc.1", str(self.target)),
                                (self.fixture.version, str(self.releases / ".." / "escape")),
                                (self.fixture.version, "relative-release")):
            with self.subTest(version=version, target=target), self.assertRaises(updater.Rejected):
                manager.prepare(version, target, api=self.fixture, trust=self.trust)
        self.assert_no_publication()

    def test_release_repository_tag_run_and_asset_mutations_are_rejected(self):
        mutations = (
            ("/releases/tags/v0.5.0", "draft", True),
            ("/releases/tags/v0.5.0", "prerelease", True),
            ("/releases/tags/v0.5.0", "target_commitish", "f" * 40),
            ("/actions/runs/1234", "conclusion", "failure"),
            ("/actions/runs/1234", "head_repository", {"id": 1, "full_name": updater.REPOSITORY}),
        )
        for path, key, value in mutations:
            self.fixture = fixtures.Fixture("core", raw=True)
            self.fixture.responses[path][key] = value
            with self.subTest(key=key), self.assertRaises(updater.Rejected):
                self.prepare()
            self.assert_no_publication()
        self.fixture = fixtures.Fixture("core", raw=True)
        with patch.object(self.fixture, "repository", return_value={"id": 1, "full_name": updater.REPOSITORY, "default_branch": "main"}), \
                self.assertRaises(updater.Rejected):
            self.prepare()
        self.assert_no_publication()
        self.fixture = fixtures.Fixture("core", raw=True)
        self.fixture.responses["/releases/tags/v0.5.0"]["assets"].append({
            "name": "unexpected.sh", "id": 999, "size": 1,
            "digest": "sha256:" + "a" * 64, "state": "uploaded",
        })
        with self.assertRaises(updater.Rejected):
            self.prepare()
        self.assert_no_publication()

    def test_legacy_archive_and_smtp_are_not_core_preparation_inputs(self):
        for component, raw in (("core", False), ("smtp", True)):
            self.fixture = fixtures.Fixture(component, raw=raw)
            if component == "smtp":
                # Advertise the SMTP inventory at the requested Core tag so the
                # test reaches component validation rather than a missing fake URL.
                release = dict(self.fixture.responses["/releases/tags/" + self.fixture.tag])
                release["tag_name"] = "v" + self.fixture.version
                self.fixture.responses["/releases/tags/v" + self.fixture.version] = release
            with self.subTest(component=component, raw=raw), self.assertRaises(updater.Rejected):
                self.prepare()
            self.assert_no_publication()

    def test_exact_historical_policy_inventory_is_accepted_for_existing_release(self):
        self.fixture.provenance["trusted_blobs"] = {
            path: value for path, value in self.fixture.provenance["trusted_blobs"].items()
            if path in updater.LEGACY_TRUSTED_PATHS
        }
        self.fixture.reseal()
        self.assert_prepared(self.prepare())

    def test_published_prepare_and_current_executor_inventory_are_version_bound(self):
        for version, expected in (("0.5.1", updater.PREPARE_TRUSTED_PATHS), ("0.5.2", updater.EXECUTOR_TRUSTED_PATHS),
                                  ("0.5.3", updater.PAYMENT_TRUSTED_PATHS), ("0.5.9", updater.PAYMENT_TRUSTED_PATHS),
                                  ("0.5.10", updater.TRUSTED_PATHS)):
            with self.subTest(version=version):
                self.fixture = fixtures.Fixture("core", raw=True, core_version=version)
                self.target = self.releases / version
                self.assertEqual(set(self.fixture.provenance["trusted_blobs"]), expected)
                self.assert_prepared(self.prepare())

    def test_payment_core_cannot_omit_payment_native_or_service_policy(self):
        self.fixture = fixtures.Fixture("core", raw=True, core_version="0.5.3")
        self.target = self.releases / self.fixture.version
        self.fixture.provenance["trusted_blobs"] = {
            path: value for path, value in self.fixture.provenance["trusted_blobs"].items()
            if path in updater.EXECUTOR_TRUSTED_PATHS
        }
        self.fixture.reseal()
        with self.assertRaisesRegex(updater.Rejected, "exact reviewed trust inventory"):
            self.prepare()
        self.assert_no_publication()

    def test_current_executor_policy_cannot_be_downgraded_to_prepare_inventory(self):
        self.fixture = fixtures.Fixture("core", raw=True, core_version="0.5.2")
        self.target = self.releases / self.fixture.version
        self.fixture.provenance["trusted_blobs"] = {
            path: value for path, value in self.fixture.provenance["trusted_blobs"].items()
            if path in updater.PREPARE_TRUSTED_PATHS
        }
        self.fixture.reseal()
        with self.assertRaisesRegex(updater.Rejected, "exact reviewed trust inventory"):
            self.prepare()
        self.assert_no_publication()

    def test_new_release_cannot_omit_new_host_tool_policy_inventory(self):
        self.fixture.version, self.fixture.tag = "0.5.1", "v0.5.1"
        protocol = "vps-billing/backend/pkg/pluginapi/protocol.go"
        self.fixture.source[protocol] = b'package pluginapi\nconst CoreVersion = "0.5.1"\n'
        self.fixture.build.update(
            release_label=self.fixture.version,
            binary_assets=updater.binary_assets("core", self.fixture.version),
            resources_asset=updater.archive_name("core", self.fixture.version, "raw-binaries-v1"),
        )
        self.fixture.build["component_versions"]["core"] = self.fixture.version
        self.fixture.build["input_sha256"][protocol] = updater.digest(self.fixture.source[protocol])
        path = "deploy/scripts/core-manager.py"
        self.fixture.build["source_files"][path] = "vps-billing/" + path
        self.fixture.bundle[path] = (self.fixture.source["vps-billing/" + path], 0o755)
        self.fixture.refresh()
        self.fixture.provenance["trusted_blobs"] = {
            path: value for path, value in self.fixture.provenance["trusted_blobs"].items()
            if path in updater.LEGACY_TRUSTED_PATHS
        }
        self.fixture.reseal()
        with self.assertRaisesRegex(updater.Rejected, "exact reviewed trust inventory"):
            self.prepare()
        self.assert_no_publication()

    def test_local_asset_tamper_link_and_world_writable_file_are_rejected(self):
        assets = self.local_assets()
        name = next(iter(updater.binary_assets("core", self.fixture.version).values()))
        binary = assets / name
        raw = binary.read_bytes()
        binary.write_bytes(b"secret-canary-modified-executable")
        with self.assertRaises(updater.Rejected) as failure:
            self.prepare(assets_directory=str(assets))
        self.assertNotIn("secret-canary", str(failure.exception))
        self.assert_no_publication()
        binary.write_bytes(raw)
        original = self.base / "original-asset"
        binary.rename(original)
        for link in (lambda: binary.symlink_to(original), lambda: os.link(original, binary)):
            link()
            with self.assertRaises(updater.Rejected):
                self.prepare(assets_directory=str(assets))
            binary.unlink()
            self.assert_no_publication()
        binary.write_bytes(raw)
        binary.chmod(0o666)
        with self.assertRaises(updater.Rejected):
            self.prepare(assets_directory=str(assets))
        self.assert_no_publication()

    def test_resealed_source_copy_and_wrong_elf_are_rejected(self):
        self.fixture.bundle["deploy/systemd/aster-api.service"] = (b"Unreviewed service bytes\n", 0o644)
        self.fixture.refresh()
        with self.assertRaises(updater.Rejected):
            self.prepare()
        self.assert_no_publication()
        self.fixture = fixtures.Fixture("core", raw=True)
        wrong = bytearray(fixtures.elf())
        struct.pack_into("<H", wrong, 18, 183)
        self.fixture.bundle["bin/aster-api"] = (bytes(wrong), 0o755)
        self.fixture.build["binary_sha256"]["bin/aster-api"] = updater.digest(wrong)
        self.fixture.refresh()
        with self.assertRaises(updater.Rejected):
            self.prepare()
        self.assert_no_publication()

    def test_resource_symlink_is_rejected_even_with_resealed_asset_checksums(self):
        name = updater.archive_name("core", self.fixture.version, "raw-binaries-v1")
        resources = {path: item for path, item in self.fixture.bundle.items()
                     if path not in updater.binary_assets("core", self.fixture.version)}
        def unsafe(member):
            if member.name.endswith("/web/index.html"):
                member.type, member.linkname = tarfile.SYMTYPE, "/outside-secret-canary"
        self.fixture.files[name] = fixtures.archive(resources, "core", self.fixture.version,
                                                    edit=unsafe, distribution_format="raw-binaries-v1")
        self.fixture.provenance["assets"][name] = {
            "size": len(self.fixture.files[name]), "sha256": updater.digest(self.fixture.files[name]),
        }
        self.fixture.reseal()
        with self.assertRaises(updater.Rejected) as failure:
            self.prepare()
        self.assertNotIn("outside-secret-canary", str(failure.exception))
        self.assert_no_publication()

    def test_lock_conflict_prevents_publication(self):
        lock = self.releases / ".core-manager.lock"
        descriptor = os.open(lock, os.O_CREAT | os.O_RDWR, 0o600)
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(updater.Rejected):
                self.prepare()
        finally:
            os.close(descriptor)
        self.assert_no_publication()

    def test_prepublication_write_failure_cleans_only_own_temporary_directory(self):
        unrelated = self.releases / ".prepare-unrelated"
        unrelated.mkdir(mode=0o700)
        (unrelated / "canary").write_bytes(b"An unrelated interrupted operation\n")
        before = snapshot(unrelated)
        with patch.object(manager, "write_file", side_effect=OSError("secret-canary-I/O-error")), \
                self.assertRaises(updater.Rejected) as failure:
            self.prepare()
        self.assertNotIn("secret-canary", str(failure.exception))
        self.assertFalse(self.target.exists())
        self.assertEqual(snapshot(unrelated), before)
        self.assertEqual(list(self.releases.glob(".prepare-*")), [unrelated])
        self.assert_inactive()

    def test_files_and_directories_are_synced_before_atomic_publication(self):
        events = []
        real_fsync, real_rename = manager.os.fsync, manager.rename_new
        def fsync(descriptor):
            events.append("directory" if stat.S_ISDIR(os.fstat(descriptor).st_mode) else "file")
            return real_fsync(descriptor)
        def rename(*args, **kwargs):
            events.append("publish")
            return real_rename(*args, **kwargs)
        with patch.object(manager.os, "fsync", side_effect=fsync), \
                patch.object(manager, "rename_new", side_effect=rename):
            self.prepare()
        published = events.index("publish")
        self.assertIn("file", events[:published])
        self.assertIn("directory", events[:published])
        self.assertIn("directory", events[published + 1:])
        self.assert_inactive()

    def test_postpublication_sync_failure_preserves_target_and_reports_uncertainty(self):
        published = False
        real_fsync, real_rename = manager.os.fsync, manager.rename_new
        def rename(*args, **kwargs):
            nonlocal published
            result = real_rename(*args, **kwargs)
            published = True
            return result
        def fsync(descriptor):
            if published:
                raise OSError("secret-canary-durability-error")
            return real_fsync(descriptor)
        with patch.object(manager, "rename_new", side_effect=rename), \
                patch.object(manager.os, "fsync", side_effect=fsync), \
                self.assertRaises(updater.Rejected) as failure:
            self.prepare()
        self.assertTrue(published)
        self.assertTrue(self.target.is_dir())
        self.assertNotIn("secret-canary", str(failure.exception))
        self.assertRegex(str(failure.exception).lower(), "uncertain|may have|inspect")
        self.assert_inactive()

    def test_readonly_doctor_never_executes_or_connects_and_keeps_prepared_files(self):
        self.prepare()
        before = snapshot(self.base)
        with patch.object(socket, "socket", side_effect=AssertionError("Doctor cannot open sockets")), \
                patch("subprocess.run", side_effect=AssertionError("Doctor cannot execute programs")), \
                patch("subprocess.Popen", side_effect=AssertionError("Doctor cannot execute programs")):
            report = manager.doctor(str(self.target), socket_path="/tmp/aster-fixture-mysql.sock",
                                    trust=self.trust)
        self.assertIsInstance(report, dict)
        self.assertIn("checks", report)
        checks = report["checks"]
        for check in checks.values() if isinstance(checks, dict) else checks:
            self.assertIn(check["status"], {"ok", "warning", "blocked"})
        self.assertEqual(snapshot(self.base), before)

    def test_doctor_missing_release_never_creates_it(self):
        before = snapshot(self.base)
        report = manager.doctor(str(self.target), trust=self.trust)
        self.assertIn("checks", report)
        checks = report["checks"]
        values = list(checks.values()) if isinstance(checks, dict) else checks
        release_check = next(item for item in values if item["check"] == "release_path")
        self.assertEqual(release_check["status"], "ok")
        self.assertFalse(release_check["exists"])
        self.assertFalse(report["database_readiness_verified"])
        self.assertFalse(report["application_readiness_verified"])
        self.assertEqual(snapshot(self.base), before)

    def test_cli_nonroot_is_rejected_before_doctor_or_release_access(self):
        output = io.StringIO()
        with patch.object(manager.os, "geteuid", return_value=2000), \
                patch.object(manager, "doctor", side_effect=AssertionError("Root guard must run first")), \
                patch.object(manager.updater, "Official", side_effect=AssertionError("No release access")), \
                contextlib.redirect_stderr(output):
            result = manager.main(["doctor", "--release-directory", str(self.target)])
        self.assertEqual(result, 1)
        self.assertIn("explicitly as root", output.getvalue())
        self.assert_no_publication()


if __name__ == "__main__":
    unittest.main(verbosity=2)
