#!/usr/bin/env python3
"""Offline executor tests: protected real files and fake host/database effects.

No subprocess, system identity, systemd unit, database or network is changed.
Official release verification uses the immutable in-memory updater fixtures.
The Python-only trust seam permits tests under an ordinary Unix account.
ACL syscall outcomes are mocked; these tests do not establish host ACL support.
"""
import contextlib
import copy
import errno
import gzip
import importlib.util
import io
import json
import os
import re
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch


SCRIPTS = Path(__file__).resolve().parent


def load(name, path):
    specification = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(specification)
    sys.modules[name] = module
    specification.loader.exec_module(module)
    return module


operation = load("aster_core_operation_under_test", SCRIPTS / "core-operation.py")
fixtures = load("aster_operation_release_fixtures", SCRIPTS / "test-update-manager.py")
updater = operation.updater
CANARY = "SecretCanary_NeverDisplay_7ca4829d"


def snapshot(root):
    result = {}
    for path in sorted(root.rglob("*")):
        info = path.lstat()
        result[str(path.relative_to(root))] = (
            stat.S_IMODE(info.st_mode),
            os.readlink(path) if path.is_symlink() else
            path.read_bytes() if path.is_file() else None,
        )
    return result


class FakeNative:
    """Model fixed effects while the executor owns real journal/link changes."""
    def __init__(self, case):
        self.case, self.events = case, []
        self.active = "active"
        self.migrated = False
        self.fail_migration = False
        self.partial_migration = False
        self.fail_ready = False
        self.fail_start = False
        self.preflight_mutation = None
        self.admin_count = 1
        self.created_identity = False
        self.enabled_units = set()
        self.partial_revokes = False
        self.partial_revokes_after_preflight = None
        self.provision_queries = []

    def run(self, argv, **kwargs):
        self.events.append(("run", tuple(argv)))
        if argv[0] == "/usr/sbin/useradd":
            self.created_identity = True
        return subprocess.CompletedProcess(argv, 0, stdout=b"")

    def sql(self, config, query):
        self.events.append(("sql", "inspect" if query.startswith("SELECT") else "provision"))
        if query.strip() == "SELECT @@partial_revokes;":
            selected = self.partial_revokes if self.partial_revokes_after_preflight is None else self.partial_revokes_after_preflight
            return str(int(selected))
        if query.startswith("SELECT"):
            return "8.4.0\tMySQL Community Server - GPL\t1\t" + str(int(self.partial_revokes)) + "\n0\n0"
        self.provision_queries.append(query)
        return ""

    def service(self, verb, unit=None):
        self.events.append(("service", verb, unit))
        if verb == "stop":
            self.active = "inactive"
        elif verb in {"start", "restart"}:
            if self.fail_start:
                raise updater.Rejected("Fixed start failure")
            self.active = "active"
        elif verb == "enable":
            self.enabled_units.add(unit)
        elif verb == "is-enabled":
            return "enabled" if unit in self.enabled_units else "disabled"
        return self.active if verb == "show" else ""

    def preflight(self, release, environment):
        self.events.append(("preflight", release, copy.deepcopy(environment)))
        facts = operation.prepared_release(release, self.case.trust)
        checksums = dict(facts["migration_checksums"])
        if self.partial_migration:
            checksums.pop(str(len(checksums)))
        empty = self.case.spec["action"] == "install" and not self.migrated
        if empty:
            checksums = {}
        report = {
            "schema": 1, "read_only": True, "configuration_checked": True,
            "core_version": facts["version"], "exit_code": 0,
            "database": {
                "read_only": True, "vendor": "MySQL", "transport": "local-unix-socket",
                "database": self.case.spec["database"], "status": "empty" if empty else "current",
                "table_visibility_verified": True,
                "schema_version": len(checksums),
                "required_schema_version": len(facts["migration_checksums"]),
                "migration_checksums": checksums,
                "pending_migrations": [], "admin_count": self.admin_count,
            },
        }
        if self.partial_migration:
            report["database"]["status"] = "pending_migrations"
        if self.preflight_mutation:
            self.preflight_mutation(report)
        return report

    def api(self, release, environment, command, allow_failure=False):
        self.events.append(("api", command, release, copy.deepcopy(environment)))
        if command == "-migrate":
            self.migrated = True
            if self.fail_migration:
                raise updater.Rejected("Fixed migration failure")
        elif command == "-create-user":
            self.admin_count += 1
        return subprocess.CompletedProcess([command], 0, stdout=b"", stderr=b"")

    def backup(self, old_release, output):
        self.events.append(("backup", old_release, output))
        self.case.backup_files(Path(output), old_release)
        return operation.Native(self.case.spec, self.case.trust).completed_backup(old_release, output)

    def verify_backup(self, saved_evidence, old_release):
        return operation.Native(self.case.spec, self.case.trust).verify_backup(saved_evidence, old_release)

    def edge(self, action):
        self.events.append(("edge", action))

    def process_identity(self, release):
        self.events.append(("process_identity", release))
        return {"verified": True}

    def validate_service(self, release):
        self.events.append(("validate_service", release))
        return {"verified": True}

    def unit_absent(self, unit):
        self.events.append(("unit_absent", unit))
        return True

    def https(self, path):
        self.events.append(("https", path))
        if path.startswith("/api/"):
            return 503, b"maintenance"
        if self.fail_ready:
            raise updater.Rejected("Fixed HTTPS failure")
        if path == "/index.html":
            return 200, (self.case.target / "web/index.html").read_bytes()
        return 200, b"{\"status\":\"ready\"}"


class ExecutorTests(unittest.TestCase):
    def setUp(self):
        previous_umask = os.umask(0o077)
        self.addCleanup(os.umask, previous_umask)
        self.temporary = tempfile.TemporaryDirectory(prefix="aster-core-operation-test-")
        self.base = Path(self.temporary.name)
        self.trust = updater._Trust(os.geteuid(), os.stat("/").st_uid)
        original_binary = operation.binary
        mapped_root = os.stat("/").st_uid
        def mapped_system_binary(path, expected_name, trust=updater._Trust(), expected_sha256=None):
            # Some offline containers map the whole root filesystem to nobody.
            # Preserve the real fixed-binary checks while modeling that mapping.
            if path == operation.SYSTEM_BUSCTL and expected_name == "busctl" and trust == updater._Trust():
                trust = updater._Trust(mapped_root, mapped_root)
            return original_binary(path, expected_name, trust, expected_sha256)
        self.binary_patch = patch.object(operation, "binary", side_effect=mapped_system_binary)
        self.binary_patch.start()
        self.addCleanup(self.binary_patch.stop)
        self.acl_remove_patch = patch.object(operation.os, "removexattr", side_effect=OSError(errno.ENODATA, "Mocked absent ACL"))
        self.acl_get_patch = patch.object(operation.os, "getxattr", side_effect=OSError(errno.ENODATA, "Mocked absent ACL"))
        self.acl_remove = self.acl_remove_patch.start()
        self.acl_get = self.acl_get_patch.start()
        self.addCleanup(self.acl_remove_patch.stop)
        self.addCleanup(self.acl_get_patch.stop)
        self.instance = self.directory("instance", 0o755)
        self.directory("instance/releases", 0o755)
        self.config = self.directory("config", 0o755)
        self.directory("config/secrets", 0o750)
        self.private = self.directory("private", 0o700)
        self.units = self.directory("units", 0o755)
        self.unit_patch = patch.object(operation, "UNIT_DIRECTORY", str(self.units), create=True)
        self.unit_patch.start()
        self.addCleanup(self.unit_patch.stop)
        self.state = self.base / "state"
        self.old = self.release("0.5.1")
        self.target = self.release("0.5.2")
        self.current = self.instance / "current"
        self.current.symlink_to(self.old, target_is_directory=True)
        self.directory("tools", 0o755)
        for name in ("mysql", "mysqldump", "nginx"):
            self.write("tools/" + name, fixtures.elf(), 0o755)
        self.write("private/certificate", b"Offline certificate fixture\n")
        self.write("private/certificate-key", (CANARY + "\n").encode())
        self.write("private/migrator-dsn", ("aster_migrator:" + CANARY + "@unix(/run/mysqld/mysqld.sock)/aster_test?parseTime=true\n").encode())
        self.write("private/backup.cnf", ("[client]\nuser=aster_backup\npassword=" + CANARY + "\n").encode())
        self.write("config/secrets/mysql_runtime_dsn", ("aster_runtime:" + CANARY + "@unix(/run/mysqld/mysqld.sock)/aster_test?parseTime=true\n").encode(), 0o640)
        self.write("config/secrets/session_secret", (CANARY * 2 + "\n").encode(), 0o640)
        self.spec = {
            "schema": 1, "action": "upgrade", "operation_id": "a" * 32,
            "release_directory": str(self.target), "instance_root": str(self.instance),
            "config_root": str(self.config), "state_directory": str(self.state),
            "origin": "https://billing.example.test", "listen_address": "127.0.0.1:18080",
            "runtime_user": "asttest", "api_unit": "aster-test.service",
            "socket": "/run/mysqld/mysqld.sock", "database": "aster_test",
            "mysql_binary": str(self.base / "tools/mysql"),
            "mysqldump_binary": str(self.base / "tools/mysqldump"),
            "nginx_binary": str(self.base / "tools/nginx"),
            "nginx_sha256": updater.digest(fixtures.elf()), "edge_mode": "managed",
            "edge_listen_address": "127.0.0.1",
            "certificate": str(self.base / "private/certificate"),
            "certificate_key": str(self.base / "private/certificate-key"),
            "migrator_dsn_file": str(self.base / "private/migrator-dsn"),
            "backup_client_config": str(self.base / "private/backup.cnf"),
            "acknowledge_backup_restore_limits": True,
        }
        environment = operation.runtime_environment(self.spec)
        environment.pop("PLUGIN_DIRECTORY")
        environment.pop("PLUGIN_POLICY_FILE")
        self.write("config/api.env", "".join(k + "=" + v + "\n" for k, v in sorted(environment.items())).encode(), 0o640)
        unit = (self.target / "deploy/systemd/aster-api.service").read_text()
        unit = unit.replace("/opt/aster", str(self.instance)).replace("/etc/aster", str(self.config))
        unit = unit.replace("User=aster\n", "User=asttest\n").replace("Group=aster\n", "Group=asttest\n")
        unit = unit.replace("SupplementaryGroups=aster-plugins\n", "")
        self.write("units/" + self.spec["api_unit"], unit.encode(), 0o644)
        self.write("units/" + operation.edge_unit(self.spec), operation.edge_unit_text(self.spec), 0o644)
        self.write("config/nginx.conf", operation.nginx_text(self.spec), 0o644)
        self.spec_path = self.base / "private/spec.json"
        self.save_spec()
        self.native = FakeNative(self)
        self.ssl_patch = patch.object(operation.ssl, "SSLContext")
        self.ssl_patch.start()
        self.addCleanup(self.ssl_patch.stop)
        self.addCleanup(self.temporary.cleanup)

    def directory(self, name, mode=0o700):
        path = self.base / name
        path.mkdir(mode=mode)
        path.chmod(mode)
        return path

    def write(self, name, raw, mode=0o600):
        path = self.base / name
        path.write_bytes(raw)
        path.chmod(mode)
        return path

    def release(self, version, migration_count=2):
        fixture = fixtures.Fixture("core", raw=True, core_version=version)
        fixture.version, fixture.tag = version, "v" + version
        protocol = "vps-billing/backend/pkg/pluginapi/protocol.go"
        fixture.source[protocol] = ('package pluginapi\nconst CoreVersion = "' + version + '"\n').encode()
        fixture.build.update(release_label=version,
                             binary_assets=updater.binary_assets("core", version),
                             resources_asset=updater.archive_name("core", version, "raw-binaries-v1"))
        fixture.build["component_versions"]["core"] = version
        fixture.build["input_sha256"][protocol] = updater.digest(fixture.source[protocol])
        for path in list(fixture.build["source_files"]):
            if path.startswith("migrations/"):
                source = fixture.build["source_files"].pop(path)
                fixture.source.pop(source, None)
                fixture.bundle.pop(path)
        for number in range(1, migration_count + 1):
            path = "migrations/" + str(number).zfill(3) + "_fixture.sql"
            source = "vps-billing/backend/" + path
            fixture.source[source] = ("-- Immutable offline migration " + str(number) + "\n").encode()
            fixture.build["source_files"][path] = source
            fixture.bundle[path] = (fixture.source[source], 0o644)
        for name in ("core-manager.py", "core-operation.py"):
            if name == "core-operation.py" and updater.stable(version) < (0, 5, 2):
                continue
            path = "deploy/scripts/" + name
            source = "vps-billing/" + path
            fixture.source.setdefault(source, ("Reviewed offline fixture " + source + "\n").encode())
            fixture.build["source_files"][path] = source
            fixture.bundle[path] = (fixture.source[source], 0o755)
        for name in ("aster-api.service", "aster-smtp.service"):
            path = "deploy/systemd/" + name
            source = "vps-billing/" + path
            fixture.source[source] = (SCRIPTS.parent / "systemd" / name).read_bytes()
            fixture.bundle[path] = (fixture.source[source], 0o644)
        fixture.bundle["licenses/go/filippo.io__edwards25519@v1.1.1/LICENSE"] = (
            b"Explicit offline Go dependency license fixture; not an official artifact.\n", 0o644)
        fixture.bundle["licenses/npm/@babel_types@7.29.8/LICENSE"] = (
            b"Explicit offline scoped npm license fixture; not an official artifact.\n", 0o644)
        fixture.refresh()
        target = self.instance / "releases" / version
        operation.manager.prepare(version, str(target), api=fixture, trust=self.trust)
        return target

    def save_spec(self):
        self.spec_path.write_bytes(updater.json_bytes(self.spec))
        self.spec_path.chmod(0o600)

    def plan(self):
        return operation.plan(str(self.spec_path), self.trust)

    def execute(self, digest=None, resume=False):
        if digest is None:
            digest = self.plan()["plan_sha256"]
        with patch.object(operation.time, "sleep", return_value=None):
            return operation.execute(str(self.spec_path), digest, resume, self.trust, self.native)

    def journal(self):
        return self.state / self.spec["operation_id"] / "JOURNAL.json"

    def read_journal(self):
        return updater.strict_json(self.journal().read_bytes())

    def reseal(self, root):
        receipt_path = root / operation.manager.RECEIPT
        receipt = updater.strict_json(receipt_path.read_bytes())
        receipt["files_sha256"] = {
            str(path.relative_to(root)): updater.digest(path.read_bytes())
            for path in root.rglob("*") if path.is_file() and path != receipt_path
        }
        receipt_path.write_bytes(updater.json_bytes(receipt))

    def backup_files(self, output, old_release=None):
        if not output.exists():
            output.mkdir(mode=0o700)
        folder = output / "aster-test-verified-backup"
        folder.mkdir(mode=0o700)
        facts = operation.prepared_release(str(self.old) if old_release is None else old_release, self.trust)
        metadata = {
            "schema": 1, "source_commit": facts["source_sha"], "core_version": facts["version"],
            "database": self.spec["database"], "schema_version": len(facts["migration_checksums"]),
            "migration_checksums": facts["migration_checksums"],
        }
        for name, raw in {
                "database.sql.gz": gzip.compress(("-- Synthetic offline SQL\n-- " + CANARY + "\nSELECT 1;\n").encode(), mtime=0),
                "BACKUP.json": updater.json_bytes(metadata),
                "BACKUP.txt": b"Synthetic offline backup. SQL contents and isolated recovery are not verified.\n"}.items():
            path = folder / name
            path.write_bytes(raw)
            path.chmod(0o600)
        self.backup_checksums(folder)
        return folder

    def backup_checksums(self, folder):
        checksums = "".join(updater.digest((folder / name).read_bytes()) + "  " + name + "\n"
                            for name in ("database.sql.gz", "BACKUP.txt", "BACKUP.json"))
        path = folder / "SHA256SUMS"
        path.write_text(checksums)
        path.chmod(0o600)

    def backup_output(self):
        for path in (self.state, self.state / self.spec["operation_id"],
                     self.state / self.spec["operation_id"] / "backups"):
            if not path.exists():
                path.mkdir(mode=0o700)
        return self.state / self.spec["operation_id"] / "backups"

    def assert_descriptor_closed(self, descriptor):
        with self.assertRaises(OSError) as failure:
            os.fstat(descriptor)
        self.assertEqual(failure.exception.errno, errno.EBADF)

    def plugin_fixture(self, selected="1.1.0", enabled=False):
        root = self.directory("instance/plugins", 0o755)
        self.directory("instance/plugins/email.smtp", 0o755)
        manifests = {}
        for version, maximum in (("1.0.0", "0.5.0"), ("1.1.0", "0.6.0")):
            prefix = "instance/plugins/email.smtp/" + version
            self.directory(prefix, 0o755)
            raw = updater.json_bytes({
                "schema": 1, "id": "email.smtp", "version": version,
                "protocol": "aster.smtp/1", "core_min": "0.4.0",
                "core_max_exclusive": maximum, "capabilities": ["smtp.send"],
                "executable": "plugin", "sha256": updater.digest(fixtures.elf()),
            })
            self.write(prefix + "/manifest.json", raw, 0o644)
            self.write(prefix + "/plugin", fixtures.elf(), 0o755)
            manifests[version] = updater.digest(raw)
        policy = {
            "schema": 1, "revision": 1,
            "smtp": {"enabled": enabled, "id": "email.smtp", "version": selected,
                     "manifest_sha256": manifests[selected], "worker_uid": 11002,
                     "client_uid": 11001, "socket": "/run/aster-plugin/email.smtp.sock",
                     "allowed_targets": [{"host": "mail.example.test", "port": 587}]},
        }
        policy_path = self.write("config/plugins.json", updater.json_bytes(policy), 0o644)
        environment = operation.runtime_environment(self.spec)
        self.write("config/api.env", "".join(key + "=" + value + "\n"
                                            for key, value in sorted(environment.items())).encode(), 0o640)
        return environment, root, policy_path, manifests

    def typed_bus_response(self, argv):
        unit = self.spec["api_unit"]
        self.assertIn("get-property", argv)
        self.assertNotIn("call", argv)
        if argv[-1] == "Id":
            value = {"type": "s", "data": unit}
        else:
            value = {"type": "a(sasbttttuii)", "data": []}
        return subprocess.CompletedProcess(argv, 0, stdout=updater.json_bytes(value))

    def test_plan_is_read_only_stable_and_canonical(self):
        before = snapshot(self.base)
        with patch("subprocess.run", side_effect=AssertionError("Plan executed a program")), \
                patch("subprocess.Popen", side_effect=AssertionError("Plan executed a program")):
            first, second = self.plan(), self.plan()
        self.assertEqual(first, second)
        canonical = {key: first[key] for key in ("schema", "spec", "inputs")}
        self.assertEqual(first["plan_sha256"], updater.digest(updater.json_bytes(canonical)))
        self.assertEqual(snapshot(self.base), before)
        self.assertNotIn(CANARY, json.dumps(first))

    def test_spec_requires_private_single_link_regular_file(self):
        raw = self.spec_path.read_bytes()
        self.spec_path.chmod(0o644)
        with self.assertRaises(updater.Rejected):
            self.plan()
        self.spec_path.chmod(0o600)
        original = self.private / "original.json"
        self.spec_path.rename(original)
        for link in (lambda: self.spec_path.symlink_to(original), lambda: os.link(original, self.spec_path)):
            link()
            with self.assertRaises(updater.Rejected):
                self.plan()
            self.spec_path.unlink()
        self.spec_path.write_bytes(raw)
        self.spec_path.chmod(0o600)

    def test_spec_duplicate_unknown_and_command_keys_rejected(self):
        original = copy.deepcopy(self.spec)
        for key in ("shell", "url", "service", "command", "migration_sql"):
            self.spec = dict(original, **{key: CANARY})
            self.save_spec()
            with self.subTest(key=key), self.assertRaises(updater.Rejected) as failure:
                self.plan()
            self.assertNotIn(CANARY, str(failure.exception))
        self.spec = original
        self.spec_path.write_bytes(updater.json_bytes(original).replace(b'"schema": 1,', b'"schema": 1, "schema": 1,'))
        with self.assertRaises(updater.Rejected):
            self.plan()

    def test_schema_number_cannot_be_replaced_with_json_boolean(self):
        self.spec["schema"] = True
        self.save_spec()
        with self.assertRaises(updater.Rejected):
            self.plan()

    def test_confirmed_digest_pins_credentials_and_config(self):
        digest = self.plan()["plan_sha256"]
        secret = self.base / "config/secrets/session_secret"
        secret.write_bytes((CANARY * 3 + "\n").encode())
        self.assertNotEqual(self.plan()["plan_sha256"], digest)
        with self.assertRaises(updater.Rejected):
            self.execute(digest)
        self.assertEqual(self.native.events, [])
        self.assertEqual(os.readlink(self.current), str(self.old))
        self.assertFalse((self.config / "maintenance").exists())

    def test_wrong_digest_causes_no_host_actions(self):
        with self.assertRaises(updater.Rejected):
            self.execute("0" * 64)
        self.assertEqual(self.native.events, [])
        self.assertEqual(os.readlink(self.current), str(self.old))

    def test_exact_prepared_inventory_modes_links_and_tamper(self):
        path = self.target / "bin/aster-api"
        raw = path.read_bytes()
        path.write_bytes(CANARY.encode())
        with self.assertRaises(updater.Rejected):
            operation.prepared_release(str(self.target), self.trust)
        path.write_bytes(raw)
        path.chmod(0o644)
        with self.assertRaises(updater.Rejected):
            operation.prepared_release(str(self.target), self.trust)
        path.chmod(0o755)
        original = self.private / "original-api"
        path.rename(original)
        for link in (lambda: path.symlink_to(original), lambda: os.link(original, path)):
            link()
            with self.assertRaises(updater.Rejected):
                operation.prepared_release(str(self.target), self.trust)
            path.unlink()
        path.write_bytes(raw)
        path.chmod(0o755)
        extra = self.target / "extra-secret"
        extra.write_bytes(CANARY.encode())
        extra.chmod(0o644)
        with self.assertRaises(updater.Rejected):
            operation.prepared_release(str(self.target), self.trust)

    def test_scoped_license_members_hash_without_relaxing_operator_paths(self):
        names = ("licenses/go/filippo.io__edwards25519@v1.1.1/LICENSE",
                 "licenses/npm/@babel_types@7.29.8/LICENSE")
        for root in (self.old, self.target):
            facts = operation.prepared_release(str(root), self.trust)
            for relative in names:
                path = root / relative
                with self.subTest(root=root.name, relative=relative):
                    # This is the old hashing call's rejection, retained for
                    # operator paths while prepared-member hashing is scoped.
                    with self.assertRaises(updater.Rejected):
                        operation.sha_file(str(path), self.trust)
                    self.assertEqual(operation.sha_prepared_member(str(root), relative, self.trust),
                                     updater.digest(path.read_bytes()))
                    self.assertEqual(facts["files_sha256"][relative], updater.digest(path.read_bytes()))
            for relative in ("LICENSE", "PROJECT-README.md", "deploy/scripts/update-manager.py"):
                self.assertEqual(operation.sha_prepared_member(str(root), relative, self.trust),
                                 facts["files_sha256"][relative])

    def test_prepared_member_hash_rejects_noncanonical_and_escaping_relatives(self):
        invalid = ("", ".", "..", "../LICENSE", "../0.5.1/LICENSE", "licenses/../LICENSE",
                   "licenses//LICENSE", "licenses/./LICENSE", "/etc/passwd", str(self.private / "spec.json"),
                   "licenses/bad file/LICENSE", "licenses/bad\nfile/LICENSE", "licenses/bad\tfile/LICENSE",
                   "licenses/back\\slash/LICENSE", "licenses/bad\0file/LICENSE")
        for relative in invalid:
            with self.subTest(relative=relative), self.assertRaises(updater.Rejected):
                operation.sha_prepared_member(str(self.target), relative, self.trust)
        for root in (str(self.target) + "/../0.5.2", str(self.target) + "@unapproved"):
            with self.subTest(root=root), self.assertRaises(updater.Rejected):
                operation.sha_prepared_member(root, "LICENSE", self.trust)

    def test_prepared_scoped_license_links_empty_files_and_tamper_are_rejected(self):
        relative = "licenses/npm/@babel_types@7.29.8/LICENSE"
        path = self.target / relative
        original_raw = path.read_bytes()
        original = self.private / "original-scoped-license"
        path.rename(original)
        for link in (lambda: path.symlink_to(original), lambda: os.link(original, path)):
            link()
            with self.assertRaises(updater.Rejected):
                operation.sha_prepared_member(str(self.target), relative, self.trust)
            with self.assertRaises(updater.Rejected):
                operation.prepared_release(str(self.target), self.trust)
            path.unlink()
        original.rename(path)
        path.write_bytes(b"")
        with self.assertRaises(updater.Rejected):
            operation.sha_prepared_member(str(self.target), relative, self.trust)
        with self.assertRaises(updater.Rejected):
            operation.prepared_release(str(self.target), self.trust)
        path.write_bytes(CANARY.encode())
        self.assertNotEqual(operation.sha_prepared_member(str(self.target), relative, self.trust),
                            updater.digest(original_raw))
        with self.assertRaises(updater.Rejected) as failure:
            operation.prepared_release(str(self.target), self.trust)
        self.assertNotIn(CANARY, str(failure.exception))

    def test_prepared_tree_rejects_resealed_bad_member_names_and_symlink_ancestors(self):
        for name in ("bad license", "bad\nlicense"):
            path = self.target / "licenses" / name
            path.write_bytes(b"Invalid-name fixture\n")
            path.chmod(0o644)
            self.reseal(self.target)
            with self.subTest(name=name), self.assertRaises(updater.Rejected):
                operation.prepared_release(str(self.target), self.trust)
            path.unlink()
        self.reseal(self.target)
        alias = self.base / "prepared-alias"
        alias.symlink_to(self.target, target_is_directory=True)
        with self.assertRaises(updater.Rejected):
            operation.sha_prepared_member(str(alias), "LICENSE", self.trust)
        directory = self.target / "licenses/npm"
        original = self.private / "original-npm-licenses"
        directory.rename(original)
        directory.symlink_to(original, target_is_directory=True)
        with self.assertRaises(updater.Rejected):
            operation.sha_prepared_member(str(self.target), "licenses/npm/@babel_types@7.29.8/LICENSE", self.trust)
        with self.assertRaises(updater.Rejected):
            operation.prepared_release(str(self.target), self.trust)

    def test_source_bound_migration_gap_rejected_even_with_resealed_receipt(self):
        (self.target / "migrations/001_fixture.sql").unlink()
        self.reseal(self.target)
        with self.assertRaises(updater.Rejected):
            operation.prepared_release(str(self.target), self.trust)

    def test_resealed_receipt_cannot_omit_required_source_or_forge_binary_metadata(self):
        build_path = self.target / "manifests/BUILD.json"
        original_build = build_path.read_bytes()
        build = updater.strict_json(original_build)
        path = "deploy/scripts/backup-native.sh"
        original_script = (self.target / path).read_bytes()
        (self.target / path).unlink()
        build["source_files"].pop(path)
        build_path.write_bytes(updater.json_bytes(build))
        self.reseal(self.target)
        with self.assertRaises(updater.Rejected):
            operation.prepared_release(str(self.target), self.trust)
        (self.target / path).write_bytes(original_script)
        (self.target / path).chmod(0o755)
        build = updater.strict_json(original_build)
        build["binary_sha256"]["bin/aster-api"] = "0" * 64
        build_path.write_bytes(updater.json_bytes(build))
        self.reseal(self.target)
        with self.assertRaises(updater.Rejected):
            operation.prepared_release(str(self.target), self.trust)

    def test_unrelated_api_unit_is_rejected_before_any_host_action(self):
        path = self.units / self.spec["api_unit"]
        path.write_bytes(path.read_bytes().replace(b"User=asttest\n", b"User=root\n"))
        with self.assertRaises(updater.Rejected):
            self.plan()
        self.assertEqual(self.native.events, [])

    def test_retained_old_plugin_allows_selected_compatible_version_for_target_core(self):
        environment, _, _, manifests = self.plugin_fixture(selected="1.1.0", enabled=False)
        with patch("subprocess.run", side_effect=AssertionError("Plugin verification executed a program")), \
                patch("subprocess.Popen", side_effect=AssertionError("Plugin verification executed a program")):
            facts = operation.plugin_facts(environment, "0.5.2", self.trust)
            self.assertEqual(self.plan()["inputs"]["plugins"], facts)
        self.assertEqual(facts["manifests"], {"email.smtp/" + version: value
                                              for version, value in manifests.items()})

    def test_selected_incompatible_old_plugin_fails_even_when_disabled(self):
        environment, _, policy_path, _ = self.plugin_fixture(selected="1.0.0", enabled=False)
        policy = updater.strict_json(policy_path.read_bytes())
        for enabled in (False, True):
            policy["smtp"]["enabled"] = enabled
            policy_path.write_bytes(updater.json_bytes(policy))
            with self.subTest(enabled=enabled), self.assertRaises(updater.Rejected) as failure:
                operation.plugin_facts(environment, "0.5.2", self.trust)
            self.assertIs(type(failure.exception), updater.Rejected)
            self.assertNotIn(CANARY, str(failure.exception))

    def payment_fixture(self, selected="1.1.0", enabled=False):
        environment, root, policy_path, smtp = self.plugin_fixture()
        self.directory("instance/plugins/payment.contract-example", 0o755)
        manifests = {}
        for version, maximum in (("1.0.0", "0.5.4"), ("1.1.0", "0.6.0")):
            prefix = "instance/plugins/payment.contract-example/" + version
            self.directory(prefix, 0o755)
            raw = updater.json_bytes({
                "schema": 1, "id": "payment.contract-example", "version": version,
                "protocol": "aster.payment/1", "core_min": "0.5.3",
                "core_max_exclusive": maximum, "capabilities": ["payment.checkout", "payment.verify_callback"],
                "executable": "plugin", "sha256": updater.digest(fixtures.elf()),
                "payment": {"currencies": [{"code": "CNY", "precision": 2}],
                            "checkout_hosts": ["checkout.example.test"]},
            })
            self.write(prefix + "/manifest.json", raw, 0o644)
            self.write(prefix + "/plugin", fixtures.elf(), 0o755)
            manifests[version] = updater.digest(raw)
        policy = updater.strict_json(policy_path.read_bytes())
        policy["payment"] = {"enabled": enabled, "id": "payment.contract-example", "version": selected,
                             "manifest_sha256": manifests[selected], "worker_uid": 11003, "client_uid": 11001,
                             "socket": "/run/aster-payment/plugin.sock", "merchant_id": "synthetic-merchant",
                             "allowed_targets": [{"host": "api.example.test", "port": 443}]}
        policy_path.write_bytes(updater.json_bytes(policy))
        return environment, root, policy_path, smtp, manifests

    def test_payment_upgrade_verification_pins_selected_and_retained_packages_without_execution(self):
        environment, _, _, smtp, payment = self.payment_fixture()
        with patch("subprocess.run", side_effect=AssertionError("Plugin verification executed a program")), \
                patch("subprocess.Popen", side_effect=AssertionError("Plugin verification executed a program")):
            facts = operation.plugin_facts(environment, "0.5.4", self.trust)
        expected = {"email.smtp/" + version: value for version, value in smtp.items()}
        expected.update({"payment.contract-example/" + version: value for version, value in payment.items()})
        self.assertEqual(facts["manifests"], expected)

    def test_incompatible_payment_selection_blocks_upgrade_even_when_disabled(self):
        environment, _, policy_path, _, _ = self.payment_fixture(selected="1.0.0")
        policy = updater.strict_json(policy_path.read_bytes())
        for enabled in (False, True):
            policy["payment"]["enabled"] = enabled
            policy_path.write_bytes(updater.json_bytes(policy))
            with self.subTest(enabled=enabled), self.assertRaises(updater.Rejected):
                operation.plugin_facts(environment, "0.5.4", self.trust)

    def test_changed_payment_manifest_and_cross_slot_uid_block_upgrade(self):
        environment, _, policy_path, _, _ = self.payment_fixture()
        policy = updater.strict_json(policy_path.read_bytes())
        original = policy["payment"]["manifest_sha256"]
        policy["payment"]["manifest_sha256"] = "0" * 64
        policy_path.write_bytes(updater.json_bytes(policy))
        with self.assertRaises(updater.Rejected):
            operation.plugin_facts(environment, "0.5.4", self.trust)
        policy["payment"]["manifest_sha256"] = original
        policy["payment"]["worker_uid"] = policy["smtp"]["worker_uid"]
        policy_path.write_bytes(updater.json_bytes(policy))
        with self.assertRaises(updater.Rejected):
            operation.plugin_facts(environment, "0.5.4", self.trust)

    def test_corrupt_retained_plugin_and_policy_raise_fixed_normalized_errors(self):
        environment, root, policy_path, _ = self.plugin_fixture(selected="1.1.0", enabled=False)
        executable = root / "email.smtp/1.0.0/plugin"
        original = executable.read_bytes()
        executable.write_bytes(CANARY.encode())
        with self.assertRaises(updater.Rejected) as failure:
            operation.plugin_facts(environment, "0.5.2", self.trust)
        self.assertIs(type(failure.exception), updater.Rejected)
        self.assertEqual(str(failure.exception), "Installed plugin policy, integrity or selected-version compatibility verification failed.")
        executable.write_bytes(original)
        policy = updater.strict_json(policy_path.read_bytes())
        policy["smtp"]["worker_uid"] = policy["smtp"]["client_uid"]
        policy["smtp"]["allowed_targets"][0]["host"] = CANARY
        policy_path.write_bytes(updater.json_bytes(policy))
        with self.assertRaises(updater.Rejected) as failure:
            operation.plugin_facts(environment, "0.5.2", self.trust)
        self.assertIs(type(failure.exception), updater.Rejected)
        self.assertEqual(str(failure.exception), "Installed plugin policy, integrity or selected-version compatibility verification failed.")
        self.assertNotIn(CANARY, str(failure.exception))

    def test_effective_service_drops_override_and_root_identity_before_execution(self):
        native = operation.Native(self.spec, self.trust)
        current = str(self.instance / "current")
        executable = current + "/bin/aster-api"
        valid = {
            "LoadState": "loaded", "FragmentPath": str(self.units / self.spec["api_unit"]),
            "DropInPaths": "", "User": "asttest", "Group": "asttest",
            "WorkingDirectory": current,
            "ExecStart": "{ path=" + executable + " ; argv[]=" + executable + " ; ignore_errors=no ; }",
            "EnvironmentFiles": str(self.config / "api.env") + " (ignore_errors=no)",
        }
        account = types.SimpleNamespace(pw_uid=11001, pw_gid=11001)
        def service_account(name):
            return types.SimpleNamespace(pw_uid=33, pw_gid=33) if name == "www-data" else account
        def completed(argv, values):
            if argv[0] == operation.SYSTEM_BUSCTL:
                return self.typed_bus_response(argv)
            selected = dict(values) if self.spec["api_unit"] in argv else {
                "FragmentPath": str(self.units / operation.edge_unit(self.spec)), "DropInPaths": ""}
            # Real Ubuntu 255 omits the empty hook arrays even with --all;
            # their typed D-Bus values provide the independent proof below.
            return subprocess.CompletedProcess(argv, 0, stdout="".join(k + "=" + v + "\n" for k, v in selected.items()).encode())
        with patch.object(operation.pwd, "getpwnam", side_effect=service_account), \
                patch.object(native, "run", side_effect=lambda argv, **kwargs: completed(argv, valid)) as run:
            self.assertTrue(native.validate_service(str(self.old))["service_configuration_verified"])
        systemctl_calls = [call for call in run.call_args_list if call.args[0][0] == "/usr/bin/systemctl"]
        self.assertEqual(len(systemctl_calls), 2)
        self.assertTrue(all("--all" in call.args[0] for call in systemctl_calls))
        self.assertEqual(len([call for call in run.call_args_list if call.args[0][0] == operation.SYSTEM_BUSCTL]), 7)
        invalid = (("DropInPaths", "/etc/override.conf"), ("User", "root"),
                   ("EnvironmentFiles", "/unrelated/api.env"))
        for key, value in invalid:
            changed = dict(valid, **{key: value})
            with self.subTest(key=key), patch.object(operation.pwd, "getpwnam", side_effect=service_account), \
                    patch.object(native, "run", side_effect=lambda argv, **kwargs: completed(argv, changed)), \
                    self.assertRaises(updater.Rejected) as failure:
                native.validate_service(str(self.old))
            self.assertNotIn(CANARY, str(failure.exception))
        with patch.object(operation.pwd, "getpwnam", return_value=types.SimpleNamespace(pw_uid=0, pw_gid=0)), \
                patch.object(native, "run") as run, self.assertRaises(updater.Rejected):
            native.validate_service(str(self.old))
        run.assert_not_called()

    def test_typed_service_hooks_follow_direct_unit_id_and_property_json_contract(self):
        native = operation.Native(self.spec, self.trust)
        hooks = ("ExecCondition", "ExecStartPre", "ExecStartPost", "ExecStop", "ExecStopPost", "ExecReload")
        self.assertEqual(operation.SERVICE_HOOKS, hooks)
        with patch.object(native, "run", side_effect=lambda argv, **kwargs: self.typed_bus_response(argv)) as run:
            self.assertEqual(native.verify_empty_service_hooks(), {hook: True for hook in hooks})
        base = ["/usr/bin/busctl", "--system", "--json=short", "--no-pager", "--auto-start=no",
                "--allow-interactive-authorization=no", "--timeout=10"]
        self.assertEqual(run.call_args_list[0].args[0], base + [
            "get-property", "org.freedesktop.systemd1",
            "/org/freedesktop/systemd1/unit/aster_2dtest_2eservice",
            "org.freedesktop.systemd1.Unit", "Id"])
        self.assertEqual(len(run.call_args_list), 7)
        for call, hook in zip(run.call_args_list[1:], hooks):
            self.assertEqual(call.args[0], base + [
                "get-property", "org.freedesktop.systemd1",
                "/org/freedesktop/systemd1/unit/aster_2dtest_2eservice",
                "org.freedesktop.systemd1.Service", hook])
        self.assertTrue(all(call.kwargs == {"timeout": 15} for call in run.call_args_list))
        self.assertTrue(all("get-property" in call.args[0] and "call" not in call.args[0]
                            for call in run.call_args_list))

    def test_typed_service_unit_id_rejects_wrong_identity_signature_and_malformed_json(self):
        native = operation.Native(self.spec, self.trust)
        expected = self.spec["api_unit"]
        invalid = [
            {"type": "s", "data": [expected]}, {"type": "o", "data": expected},
            {"type": "s", "data": []}, {"type": "s", "data": {}},
            {"type": "s", "data": "aster-other.service"}, {"type": "s", "data": ""},
            {"type": "s", "data": False}, {"type": "s", "data": None}, {"type": "s"},
            {"type": "s", "data": expected, "unrecognized": CANARY},
            b'{"type":"s","type":"s","data":"aster-test.service"}', b'{"type":',
        ]
        for index, value in enumerate(invalid):
            raw = value if isinstance(value, bytes) else updater.json_bytes(value)
            with self.subTest(index=index), \
                    patch.object(native, "run", return_value=subprocess.CompletedProcess([], 0, stdout=raw)) as run, \
                    self.assertRaises(updater.Rejected) as failure:
                native.verify_empty_service_hooks()
            self.assertEqual(run.call_count, 1)
            self.assertNotIn(CANARY, str(failure.exception))

    def test_typed_service_derives_exact_object_path_for_unit_with_underscore(self):
        self.spec["api_unit"] = "aster_ci-test.service"
        native = operation.Native(self.spec, self.trust)
        with patch.object(native, "run", side_effect=lambda argv, **kwargs: self.typed_bus_response(argv)) as run:
            native.verify_empty_service_hooks()
        self.assertEqual(len(run.call_args_list), 7)
        for call in run.call_args_list:
            argv = call.args[0]
            self.assertIn("get-property", argv)
            self.assertNotIn("call", argv)
            self.assertEqual(argv[argv.index("get-property") + 2],
                             "/org/freedesktop/systemd1/unit/aster_5fci_2dtest_2eservice")

    def test_typed_service_hooks_reject_each_nonempty_missing_mixed_or_malformed_property(self):
        native = operation.Native(self.spec, self.trust)
        hooks = ("ExecCondition", "ExecStartPre", "ExecStartPost", "ExecStop", "ExecStopPost", "ExecReload")
        command = ["/usr/bin/false", ["/usr/bin/false"], False, 0, 0, 0, 0, 0, 0, 0]
        invalid = [
            {"type": "a(sasbttttuii)", "data": [command]},
            {"type": "a(sasbttttuii)", "data": [[], False, 0, CANARY]},
            {"type": "a(sasbttttuii)", "data": [[]]},
            {"type": "a(sasbttttuii)", "data": False},
            {"type": "a(sasbttttuii)", "data": {}},
            {"type": "as", "data": []}, {"type": "a(sasbttttuii)"},
            {"data": []}, {"type": "a(sasbttttuii)", "data": [], "extra": CANARY},
            b'{"type":"a(sasbttttuii)","data":[],"data":[]}', b'not-json',
        ]
        for hook in hooks:
            for index, value in enumerate(invalid):
                raw = value if isinstance(value, bytes) else updater.json_bytes(value)
                def response(argv, **kwargs):
                    if "get-property" in argv and argv[-1] == hook:
                        return subprocess.CompletedProcess(argv, 0, stdout=raw)
                    return self.typed_bus_response(argv)
                with self.subTest(hook=hook, index=index), patch.object(native, "run", side_effect=response), \
                        self.assertRaises(updater.Rejected) as failure:
                    native.verify_empty_service_hooks()
                self.assertNotIn(CANARY, str(failure.exception))

    def test_typed_service_hooks_require_successful_unit_id_and_every_property_command(self):
        native = operation.Native(self.spec, self.trust)
        for missing in ("Id", "ExecCondition", "ExecStartPre", "ExecStartPost", "ExecStop", "ExecStopPost", "ExecReload"):
            def response(argv, **kwargs):
                if argv[-1] == missing:
                    raise updater.Rejected("A fixed native operation failed; raw diagnostics were suppressed.")
                return self.typed_bus_response(argv)
            with self.subTest(missing=missing), patch.object(native, "run", side_effect=response), \
                    self.assertRaises(updater.Rejected):
                native.verify_empty_service_hooks()

    def test_healthy_old_listener_cannot_satisfy_candidate_process_identity(self):
        native = operation.Native(self.spec, self.trust)
        with patch.object(native, "run", return_value=subprocess.CompletedProcess([], 0, stdout=b"12345\n")), \
                patch.object(operation.os, "readlink", return_value=str(self.old / "bin/aster-api")), \
                self.assertRaises(updater.Rejected):
            native.process_identity(str(self.target))

    def test_upgrade_success_retains_backup_and_never_bootstraps(self):
        digest = self.plan()["plan_sha256"]
        result = self.execute(digest)
        self.assertEqual(result["status"], "succeeded")
        self.assertEqual(os.readlink(self.current), str(self.target))
        self.assertFalse((self.config / "maintenance").exists())
        self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate"])
        self.assertEqual(len([event for event in self.native.events if event[0] == "backup"]), 1)
        self.assertEqual(self.read_journal()["database_migration"], {"started": True, "completed": True})
        self.assertNotIn(CANARY, self.journal().read_text())
        before = copy.deepcopy(self.native.events)
        self.assertEqual(self.execute(digest, resume=True)["status"], "succeeded")
        self.assertEqual(self.native.events, before)
        self.assertEqual(stat.S_IMODE(self.journal().stat().st_mode), 0o600)

    def history_upgrade_case(self):
        # Both receipts are produced by the real prepare path, not fabricated.
        # The synthetic old source was valid before the modeled main rewrite.
        with patch.object(fixtures, "SOURCE", "9" * 40):
            self.old = self.release("0.5.9", migration_count=13)
        self.target = self.release("0.5.10", migration_count=13)
        self.current.unlink()
        self.current.symlink_to(self.old, target_is_directory=True)
        self.spec["release_directory"] = str(self.target)
        self.save_spec()
        self.native = FakeNative(self)

    def test_real_prepared_059_upgrades_to_0510_without_refetching_old_history(self):
        self.history_upgrade_case()
        old_before = snapshot(self.old)
        # This checks only local prepared-directory continuity. New-target
        # ancestry was verified in prepare; no GitHub access is allowed here.
        with patch.object(updater, "Official", side_effect=AssertionError("No old-source bypass or refetch")):
            plan = self.plan()
            self.assertEqual(plan["inputs"]["previous"]["version"], "0.5.9")
            self.assertEqual(plan["inputs"]["target"]["version"], "0.5.10")
            self.assertEqual(plan["inputs"]["previous"]["source_sha"], "9" * 40)
            self.assertEqual(plan["inputs"]["target"]["source_sha"], fixtures.SOURCE)
            self.assertEqual(plan["inputs"]["previous"]["migration_checksums"],
                             plan["inputs"]["target"]["migration_checksums"])
            self.assertEqual(len(plan["inputs"]["target"]["migration_checksums"]), 13)
            result = self.execute(plan["plan_sha256"])
        self.assertEqual(result["status"], "succeeded")
        self.assertEqual(os.readlink(self.current), str(self.target))
        self.assertEqual(snapshot(self.old), old_before)
        self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate"])
        self.assertEqual(len([event for event in self.native.events if event[0] == "backup"]), 1)

    def test_059_without_prepared_receipt_cannot_upgrade_to_0510(self):
        self.history_upgrade_case()
        self.assertEqual(self.plan()["inputs"]["previous"]["version"], "0.5.9")
        (self.old / "manifests/PREPARED.json").unlink()
        before = snapshot(self.base)
        with patch.object(updater, "Official", side_effect=AssertionError("No receipt reconstruction")), \
                self.assertRaises(updater.Rejected):
            self.plan()
        self.assertEqual(snapshot(self.base), before)
        self.assertEqual(os.readlink(self.current), str(self.old))
        self.assertFalse(self.native.events)

    def install_case(self, enable_at_boot, interrupt_configuration=False, conflicting_configuration=False,
                     partial_revokes=False, change_partial_revokes=False):
        self.current.unlink()
        shutil.rmtree(self.config)
        for path in self.units.iterdir():
            path.unlink()
        for key in tuple(operation.UPGRADE_KEYS):
            self.spec.pop(key, None)
        password = self.write("private/bootstrap-password", (CANARY + "_fresh\n").encode())
        dba = self.write("private/dba.cnf", ("[client]\nuser=aster_dba\npassword=" + CANARY + "\n").encode())
        self.spec.update(action="install", dba_client_config=str(dba),
                         bootstrap_email="operator@example.test", bootstrap_name="Offline Operator",
                         bootstrap_password_file=str(password), enable_at_boot=enable_at_boot)
        self.save_spec()
        self.native.active = "inactive"
        self.native.admin_count = 0
        self.native.partial_revokes = partial_revokes
        self.native.partial_revokes_after_preflight = not partial_revokes if change_partial_revokes else None
        account = types.SimpleNamespace(pw_uid=11001, pw_gid=11001)
        def find_account(name):
            if name == "www-data":
                return types.SimpleNamespace(pw_uid=33, pw_gid=33)
            if self.native.created_identity:
                return account
            raise KeyError(name)
        with patch.object(operation.pwd, "getpwnam", side_effect=find_account), \
                patch.object(operation.grp, "getgrnam", side_effect=KeyError("absent")), \
                patch.object(operation.os, "chown"):
            digest = self.plan()["plan_sha256"]
            if change_partial_revokes:
                with self.assertRaises(updater.Rejected):
                    self.execute(digest)
                self.assertEqual(self.native.provision_queries, [])
                self.assertEqual([event[1] for event in self.native.events if event[0] == "sql"], ["inspect", "inspect"])
                self.assertEqual([event for event in self.native.events if event[0] == "api"], [])
                self.assertFalse(self.current.exists())
                return
            if interrupt_configuration:
                original_new_file = operation.new_file
                unit_path = self.units / self.spec["api_unit"]
                interrupted = False
                def interrupted_write(path, *args, **kwargs):
                    nonlocal interrupted
                    original_new_file(path, *args, **kwargs)
                    if path == str(unit_path) and not interrupted:
                        interrupted = True
                        raise OSError("Offline interruption after durable generated-file publication")
                with patch.object(operation, "new_file", side_effect=interrupted_write), \
                        self.assertRaises(updater.Rejected):
                    self.execute(digest)
                self.assertTrue(interrupted)
                journal = self.read_journal()
                self.assertEqual(journal["steps"]["configure"]["status"], "started")
                self.assertTrue(journal["database_migration"]["completed"])
                self.assertEqual(self.native.admin_count, 1)
                self.assertEqual(os.readlink(self.current), str(self.target))
                if conflicting_configuration:
                    unit_path.write_bytes(b"Unexpected generated unit; preserve for operator review.\n")
                    before_file = unit_path.read_bytes()
                    with self.assertRaises(updater.Rejected):
                        self.execute(digest, resume=True)
                    self.assertEqual(unit_path.read_bytes(), before_file)
                    self.assertTrue((self.config / "maintenance").exists())
                    self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate", "-create-user"])
                    self.assertEqual(self.native.admin_count, 1)
                    return
                before_file = unit_path.stat()
                with patch.object(operation, "new_file", wraps=original_new_file) as new_file:
                    result = self.execute(digest, resume=True)
                self.assertNotIn(str(unit_path), [call.args[0] for call in new_file.call_args_list])
                after_file = unit_path.stat()
                self.assertEqual((before_file.st_ino, before_file.st_mtime_ns),
                                 (after_file.st_ino, after_file.st_mtime_ns))
                self.assertEqual(stat.S_IMODE((self.config / "nginx-temp").stat().st_mode), 0o755)
            else:
                result = self.execute(digest)
            before = list(self.native.events)
            self.assertEqual(self.execute(digest, resume=True)["status"], "succeeded")
        self.assertEqual(result["status"], "succeeded")
        self.assertEqual(self.native.events, before)
        self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate", "-create-user"])
        self.assertEqual([event[1] for event in self.native.events if event[0] == "sql"], ["inspect", "inspect", "provision"])
        self.assertEqual(len(self.native.provision_queries), 1)
        query = self.native.provision_queries[0]
        database_targets = [line.split(" ON ", 1)[1].split(" TO ", 1)[0]
                            for line in query.splitlines()
                            if line.startswith("GRANT ") and " ON *.* TO " not in line]
        target = "`aster_test`.*" if partial_revokes else r"`aster\_test`.*"
        self.assertEqual(database_targets, [target, target, target])
        self.assertFalse(any("SET GLOBAL" in line.upper() for line in query.splitlines()))
        self.assertIs(self.read_journal()["steps"]["install_preflight"]["result"]["partial_revokes"], partial_revokes)
        self.assertEqual(self.native.admin_count, 1)
        self.assertEqual(os.readlink(self.current), str(self.target))
        self.assertEqual(stat.S_IMODE((self.config / "api.env").stat().st_mode), 0o640)
        self.assertEqual(stat.S_IMODE(self.config.stat().st_mode), 0o755)
        self.assertEqual(stat.S_IMODE((self.config / "secrets").stat().st_mode), 0o750)
        self.assertEqual(stat.S_IMODE((self.config / "nginx-temp").stat().st_mode), 0o755)
        self.assertEqual(stat.S_IMODE((self.instance / "plugins").stat().st_mode), 0o755)
        self.assertNotIn(CANARY, self.journal().read_text())
        units = {self.spec["api_unit"], operation.edge_unit(self.spec)}
        enabled = [event[2] for event in self.native.events if event[:2] == ("service", "enable")]
        checked = [event[2] for event in self.native.events if event[:2] == ("service", "is-enabled")]
        if enable_at_boot:
            self.assertEqual(set(enabled), units)
            self.assertEqual(set(checked), units)
            self.assertEqual(len(enabled), 2)
            self.assertEqual(len(checked), 2)
            for unit in units:
                self.assertTrue((self.units / unit).is_file())
        else:
            self.assertEqual(enabled, [])
            self.assertEqual(checked, [])

    def test_fresh_install_provisions_then_bootstraps_exactly_once(self):
        self.install_case(enable_at_boot=False)

    def test_fresh_install_enables_only_both_generated_units_when_explicit(self):
        self.install_case(enable_at_boot=True)

    def test_interrupted_configuration_resumes_without_overwrite_or_repeating_database_and_admin(self):
        self.install_case(enable_at_boot=False, interrupt_configuration=True)

    def test_interrupted_configuration_refuses_a_changed_generated_file(self):
        self.install_case(enable_at_boot=False, interrupt_configuration=True,
                          conflicting_configuration=True)

    def test_fresh_install_escapes_database_underscore_when_partial_revocations_disabled(self):
        self.install_case(enable_at_boot=False, partial_revokes=False)

    def test_fresh_install_grants_literal_database_when_partial_revocations_enabled(self):
        self.install_case(enable_at_boot=False, partial_revokes=True)

    def test_partial_revocation_mode_change_blocks_database_creation_sql(self):
        self.install_case(enable_at_boot=False, partial_revokes=False, change_partial_revokes=True)

    def test_uncertain_migration_never_reexecutes_or_mutates_schema(self):
        digest = self.plan()["plan_sha256"]
        self.native.fail_migration = True
        self.native.partial_migration = False
        original_api = self.native.api
        def partial(release, environment, command, allow_failure=False):
            if command == "-migrate":
                self.native.partial_migration = True
            return original_api(release, environment, command, allow_failure)
        self.native.api = partial
        with self.assertRaises(updater.Rejected):
            self.execute(digest)
        self.assertEqual(self.read_journal()["status"], "recovery_required")
        self.assertTrue(self.read_journal()["database_migration"]["started"])
        self.assertFalse(self.read_journal()["database_migration"]["completed"])
        self.native.fail_migration = False
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)
        self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate"])
        self.assertTrue((self.config / "maintenance").exists())

    def test_uncertain_migration_forbids_automatic_rollback_even_when_old_schema_reports_current(self):
        self.native.fail_migration = True
        self.native.partial_migration = False
        with self.assertRaises(updater.Rejected):
            self.execute()
        journal = self.read_journal()
        self.assertEqual(journal["status"], "recovery_required")
        self.assertEqual(journal["database_migration"], {"started": True, "completed": False})
        self.assertNotEqual(journal["program_rollback"], "verified_old_schema")
        self.assertEqual(os.readlink(self.current), str(self.old))
        self.assertTrue((self.config / "maintenance").exists())
        self.assertEqual([event for event in self.native.events if event[:2] == ("service", "start")], [])
        self.assertEqual(self.native.active, "inactive")

    def hold_activated_target(self, failure_point="readiness"):
        """Offline physical-schema fault after the activation receipt is done."""
        digest = self.plan()["plan_sha256"]
        fault = {"armed": True, "schema_unknown": False}
        original_preflight = self.native.preflight
        original_https = self.native.https
        original_identity = self.native.process_identity

        def preflight(release, environment):
            report = original_preflight(release, environment)
            journal = self.read_journal()
            activated = journal["steps"].get("activate", {}).get("status") == "done"
            ready = journal["steps"].get("readiness", {}).get("status")
            if fault["armed"] and activated and (
                    failure_point == "readiness" and ready == "started" or
                    failure_point == "finish" and ready == "done"):
                fault.update(armed=False, schema_unknown=True)
            if fault["schema_unknown"]:
                # Keep the complete migration ledger: it cannot prove that
                # physical tables still match either verified program.
                report["exit_code"] = 1
                report["database"]["status"] = "incompatible"
            return report

        def https(path):
            if failure_point == "static" and fault["armed"] and path == "/index.html":
                self.assertEqual(self.read_journal()["steps"]["readiness"]["status"], "done")
                self.assertFalse((self.config / "maintenance").exists())
                fault.update(armed=False, schema_unknown=True)
                return 200, b"Unexpected offline static entry\n"
            return original_https(path)

        def process_identity(release):
            # Unlike a permissive host double, a stopped API cannot supply a
            # verifiable PID. This is the original late-resume failure trigger.
            updater.require(self.native.active == "active", "Offline API is stopped.")
            return original_identity(release)

        self.native.preflight = preflight
        self.native.https = https
        self.native.process_identity = process_identity
        with self.assertRaises(updater.Rejected):
            self.execute(digest)
        self.assertFalse(fault["armed"])
        self.assert_activated_target_held()
        ready = self.read_journal()["steps"]["readiness"]["status"]
        self.assertEqual(ready, "started" if failure_point == "readiness" else "done")
        return digest, fault

    def assert_activated_target_held(self):
        journal = self.read_journal()
        operation.validate_journal(journal)
        self.assertEqual(journal["status"], "recovery_required")
        self.assertEqual(journal["program_rollback"], "blocked_schema_or_readiness")
        self.assertEqual(journal["database_migration"], {"started": True, "completed": True})
        self.assertEqual(journal["steps"]["migrate"]["status"], "done")
        self.assertEqual(journal["steps"]["activate"], {"status": "started", "result": {"recovery_stopped": True}})
        self.assertTrue(journal["maintenance"])
        self.assertFalse(journal["https_ready"])
        self.assertEqual((self.config / "maintenance").read_bytes(), b"Aster maintenance\n")
        self.assertEqual(os.readlink(self.current), str(self.target))
        self.assertEqual(self.native.active, "inactive")
        self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate"])
        self.assertEqual(len([event for event in self.native.events if event[0] == "backup"]), 1)
        self.assertEqual(self.native.admin_count, 1)

    def repair_activated_target(self, failure_point):
        digest, fault = self.hold_activated_target(failure_point)
        starts = [event for event in self.native.events if event[:2] == ("service", "start")]
        self.assertEqual(len(starts), 1)
        # A completed migration receipt never authorizes restarting an unknown
        # physical schema, even on another exact-plan resume.
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)
        self.assert_activated_target_held()
        self.assertEqual([event for event in self.native.events if event[:2] == ("service", "start")], starts)
        fault["schema_unknown"] = False  # Independent operator repair only.
        before_resume = len(self.native.events)
        result = self.execute(digest, resume=True)
        resumed = self.native.events[before_resume:]
        start = next(i for i, event in enumerate(resumed) if event[:2] == ("service", "start"))
        schema_checks = [event for event in resumed[:start] if event[0] == "preflight"]
        self.assertTrue(schema_checks)
        self.assertEqual(schema_checks[-1][1], str(self.target))
        self.assertEqual(schema_checks[-1][2]["MYSQL_DSN_FILE"], self.spec["migrator_dsn_file"])
        self.assertEqual(result["status"], "succeeded")
        self.assertEqual(result["program_rollback"], "none")
        self.assertTrue(result["https_ready"])
        self.assertFalse(result["maintenance"])
        self.assertEqual(os.readlink(self.current), str(self.target))
        self.assertEqual(self.native.active, "active")
        self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate"])
        self.assertEqual(len([event for event in self.native.events if event[:2] == ("service", "start")]), 2)
        operation.validate_journal(self.read_journal())
        self.assertEqual(operation.status(str(self.state), self.spec["operation_id"], self.trust), result)
        self.assertNotIn("recovery_stopped", json.dumps(result))
        self.assertNotIn(CANARY, self.journal().read_text())
        before_terminal = list(self.native.events)
        self.assertEqual(self.execute(digest, resume=True), result)
        self.assertEqual(self.native.events, before_terminal)

    def test_completed_activation_unknown_readiness_stays_stopped_until_schema_repair(self):
        self.repair_activated_target("readiness")

    def test_completed_readiness_finish_failure_can_resume_target_after_schema_repair(self):
        self.repair_activated_target("finish")

    def test_completed_readiness_static_failure_restores_maintenance_and_can_resume_target(self):
        self.repair_activated_target("static")

    def test_stopped_target_recovery_requires_exact_marker_and_actual_https_maintenance(self):
        digest, _ = self.hold_activated_target()
        starts = [event for event in self.native.events if event[:2] == ("service", "start")]
        (self.config / "maintenance").write_bytes(b"Unknown maintenance marker\n")
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)
        self.assertEqual((self.config / "maintenance").read_bytes(), b"Unknown maintenance marker\n")
        self.assertEqual([event for event in self.native.events if event[:2] == ("service", "start")], starts)
        (self.config / "maintenance").write_bytes(b"Aster maintenance\n")
        original_https = self.native.https
        def bypassed_maintenance(path):
            return (200, b"Unexpected bypass") if path.startswith("/api/") else original_https(path)
        self.native.https = bypassed_maintenance
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)
        self.assert_activated_target_held()
        self.assertEqual([event for event in self.native.events if event[:2] == ("service", "start")], starts)

    def test_stopped_target_recovery_refuses_unexpected_current_or_running_service(self):
        digest, _ = self.hold_activated_target()
        starts = [event for event in self.native.events if event[:2] == ("service", "start")]
        self.current.unlink()
        self.current.symlink_to(self.old)
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)
        self.assertEqual(os.readlink(self.current), str(self.old))
        self.assertEqual([event for event in self.native.events if event[:2] == ("service", "start")], starts)
        self.current.unlink()
        self.current.symlink_to(self.target)
        self.native.active = "active"  # Model an unapproved external restart.
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)
        self.assert_activated_target_held()
        self.assertEqual([event for event in self.native.events if event[:2] == ("service", "start")], starts)

    def test_stopped_target_private_marker_validation_precedes_all_host_effects(self):
        digest, _ = self.hold_activated_target()
        journal = self.read_journal()
        malformed = [dict(status="started", result=value) for value in (None, [], True)]
        malformed += [dict(status="started", result={"recovery_stopped": value})
                      for value in (False, 1, "true", None)]
        malformed += [{"status": "done", "result": {"recovery_stopped": True}},
                      {"status": "started", "result": {"recovery_stopped": True, "extra": True}}]
        before = list(self.native.events)
        for record in malformed:
            changed = copy.deepcopy(journal)
            changed["steps"]["activate"] = record
            self.journal().write_bytes(updater.json_bytes(changed))
            with self.subTest(record=record), self.assertRaises(updater.Rejected):
                self.execute(digest, resume=True)
            self.assertEqual(self.native.events, before)
            self.assertEqual(self.native.active, "inactive")
            self.assertEqual(os.readlink(self.current), str(self.target))

    def test_recovery_stop_checkpoint_is_saved_before_old_schema_probe(self):
        original_preflight = self.native.preflight
        observed = []
        def preflight(release, environment):
            report = original_preflight(release, environment)
            journal = self.read_journal()
            if journal["steps"].get("activate", {}).get("status") == "done":
                report["exit_code"] = 1
                report["database"]["status"] = "incompatible"
            if release == str(self.old) and journal["database_migration"]["completed"]:
                self.assertEqual(journal["steps"]["activate"], {"status": "started", "result": {"recovery_stopped": True}})
                self.assertEqual(journal["service_action"], "stopped")
                self.assertEqual(self.native.active, "inactive")
                observed.append(True)
                report["exit_code"] = 1
                report["database"]["status"] = "incompatible"
            return report
        self.native.preflight = preflight
        with self.assertRaises(updater.Rejected):
            self.execute()
        self.assertEqual(observed, [True])
        self.assert_activated_target_held()

    def test_late_failure_still_rolls_back_when_old_schema_and_readiness_are_verified(self):
        digest = self.plan()["plan_sha256"]
        original_https = self.native.https
        def https(path):
            if path == "/readyz" and os.readlink(self.current) == str(self.target):
                raise updater.Rejected("Offline target readiness failure")
            return original_https(path)
        self.native.https = https
        with self.assertRaises(updater.Rejected):
            self.execute(digest)
        journal = self.read_journal()
        operation.validate_journal(journal)
        self.assertEqual(journal["status"], "rolled_back")
        self.assertEqual(journal["program_rollback"], "verified_old_schema")
        self.assertEqual(journal["service_action"], "previous_started")
        self.assertEqual(journal["steps"]["activate"], {"status": "started", "result": {"recovery_stopped": True}})
        self.assertEqual(os.readlink(self.current), str(self.old))
        self.assertEqual(self.native.active, "active")
        self.assertTrue((self.config / "maintenance").exists())
        self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate"])
        before = list(self.native.events)
        self.assertEqual(self.execute(digest, resume=True)["status"], "rolled_back")
        self.assertEqual(self.native.events, before)

    def test_ordinary_started_activation_reconciles_without_stopped_target_marker(self):
        digest = self.plan()["plan_sha256"]
        original_edge = self.native.edge
        original_preflight = self.native.preflight
        interrupted = []
        def edge(action):
            if action == "reload" and not interrupted:
                interrupted.append(True)
                raise updater.Rejected("Offline activation interruption")
            return original_edge(action)
        def preflight(release, environment):
            report = original_preflight(release, environment)
            if release == str(self.old) and self.native.migrated:
                report["exit_code"] = 1
                report["database"]["status"] = "incompatible"
            return report
        self.native.edge = edge
        self.native.preflight = preflight
        with self.assertRaises(updater.Rejected):
            self.execute(digest)
        journal = self.read_journal()
        self.assertEqual(journal["status"], "recovery_required")
        self.assertEqual(journal["steps"]["activate"], {"status": "started"})
        self.assertEqual(self.native.active, "inactive")
        result = self.execute(digest, resume=True)
        self.assertEqual(result["status"], "succeeded")
        self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate"])
        self.assertEqual(os.readlink(self.current), str(self.target))
        operation.validate_journal(self.read_journal())

    def test_manual_schema_repair_then_verified_resume_clears_stale_rollback_hold(self):
        digest = self.plan()["plan_sha256"]
        original_api = self.native.api
        def interrupted_migration(release, environment, command, allow_failure=False):
            if command == "-migrate":
                self.native.partial_migration = True
                self.native.fail_migration = True
            return original_api(release, environment, command, allow_failure)
        self.native.api = interrupted_migration
        with self.assertRaises(updater.Rejected):
            self.execute(digest)
        held = self.read_journal()
        self.assertEqual(held["status"], "recovery_required")
        self.assertEqual(held["program_rollback"], "blocked_schema_or_readiness")
        self.assertEqual(held["database_migration"], {"started": True, "completed": False})
        self.assertTrue((self.config / "maintenance").exists())
        # Model an independent operator repair; resume only observes this state.
        self.native.partial_migration = False
        self.native.fail_migration = False
        result = self.execute(digest, resume=True)
        self.assertEqual(result["status"], "succeeded")
        self.assertEqual(result["program_rollback"], "none")
        self.assertTrue(result["https_ready"])
        self.assertFalse(result["maintenance"])
        self.assertEqual(os.readlink(self.current), str(self.target))
        self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate"])
        journal = self.read_journal()
        operation.validate_journal(journal)
        self.assertEqual(operation.status(str(self.state), self.spec["operation_id"], self.trust), result)
        before = list(self.native.events)
        self.assertEqual(self.execute(digest, resume=True), result)
        self.assertEqual(self.native.events, before)

    def test_interrupted_migration_can_reconcile_completed_schema_without_repeating(self):
        digest = self.plan()["plan_sha256"]
        self.native.fail_migration = True
        with self.assertRaises(updater.Rejected):
            self.execute(digest)
        journal = self.read_journal()
        # Model a verified current target after interruption, not a new migration.
        journal["status"] = "recovery_required"
        self.journal().write_bytes(updater.json_bytes(journal))
        self.native.fail_migration = False
        result = self.execute(digest, resume=True)
        self.assertEqual(result["status"], "succeeded")
        self.assertEqual([event[1] for event in self.native.events if event[0] == "api"], ["-migrate"])

    def test_migration_intent_is_durable_before_database_effect(self):
        original_api = self.native.api
        observed = []
        def verify_checkpoint(release, environment, command, allow_failure=False):
            journal = self.read_journal()
            observed.append(journal["phase"])
            self.assertEqual(journal["steps"]["migrate"]["status"], "started")
            self.assertTrue(journal["database_migration"]["started"])
            self.assertFalse(journal["database_migration"]["completed"])
            return original_api(release, environment, command, allow_failure)
        self.native.api = verify_checkpoint
        self.execute()
        self.assertEqual(observed, ["migrate"])

    def test_backup_failure_can_only_restore_verified_old_program_and_keeps_maintenance(self):
        digest = self.plan()["plan_sha256"]
        with patch.object(self.native, "backup", side_effect=updater.Rejected("Fixed backup failure")), \
                self.assertRaises(updater.Rejected):
            self.execute(digest)
        journal = self.read_journal()
        self.assertEqual(journal["status"], "rolled_back")
        self.assertEqual(journal["program_rollback"], "verified_old_schema")
        self.assertEqual(os.readlink(self.current), str(self.old))
        self.assertTrue((self.config / "maintenance").exists())
        self.assertFalse(journal["database_migration"]["started"])
        self.assertEqual([event for event in self.native.events if event[0] == "api"], [])
        before = list(self.native.events)
        self.assertEqual(self.execute(digest, resume=True)["status"], "rolled_back")
        self.assertEqual(self.native.events, before)

    def test_real_gzip_backup_is_verified_without_claiming_sql_or_restore_validation(self):
        output = self.backup_output()
        folder = self.backup_files(output)
        native = operation.Native(self.spec, self.trust)
        evidence = native.completed_backup(str(self.old), str(output))
        self.assertEqual(evidence["directory"], str(folder))
        self.assertTrue(evidence["gzip_integrity_verified"])
        self.assertFalse(evidence["database_contents_verified"])
        self.assertFalse(evidence["isolated_restore_verified"])
        self.assertEqual(native.verify_backup(evidence, str(self.old)), evidence)

    def test_completed_backup_requires_exact_private_mode_on_every_member(self):
        output = self.backup_output()
        folder = self.backup_files(output)
        native = operation.Native(self.spec, self.trust)
        members = ("database.sql.gz", "BACKUP.json", "BACKUP.txt", "SHA256SUMS")
        self.assertTrue(all(stat.S_IMODE((folder / name).stat().st_mode) == 0o600 for name in members))
        original = native.completed_backup(str(self.old), str(output))
        for name in members:
            path = folder / name
            path.chmod(0o644)
            try:
                with self.subTest(member=name, mode="0644"), self.assertRaises(updater.Rejected) as failure:
                    native.completed_backup(str(self.old), str(output))
                self.assertEqual(str(failure.exception), "Private paths must use mode 0700 or 0600.")
            finally:
                path.chmod(0o600)
        text = folder / "BACKUP.txt"
        for mode in (0o666, 0o640, 0o755):
            text.chmod(mode)
            try:
                with self.subTest(member="BACKUP.txt", mode=oct(mode)), self.assertRaises(updater.Rejected) as failure:
                    native.completed_backup(str(self.old), str(output))
                self.assertNotIn(CANARY, str(failure.exception))
            finally:
                text.chmod(0o600)
        self.assertEqual(native.completed_backup(str(self.old), str(output)), original)

    def test_real_backup_corruption_missing_members_and_checksum_mutations_are_rejected(self):
        output = self.backup_output()
        native = operation.Native(self.spec, self.trust)
        for mutation in ("corrupt_sql", "truncated_gzip_resealed", "concatenated_gzip_resealed",
                         "missing_metadata", "extra_member", "missing_checksum", "duplicate_checksum"):
            folder = self.backup_files(output)
            sql = folder / "database.sql.gz"
            sums = folder / "SHA256SUMS"
            if mutation == "corrupt_sql":
                sql.write_bytes(sql.read_bytes() + CANARY.encode())
            elif mutation == "truncated_gzip_resealed":
                sql.write_bytes(sql.read_bytes()[:-5])
                self.backup_checksums(folder)
            elif mutation == "concatenated_gzip_resealed":
                sql.write_bytes(sql.read_bytes() + gzip.compress(b"SELECT 2;\n", mtime=0))
                self.backup_checksums(folder)
            elif mutation == "missing_metadata":
                (folder / "BACKUP.json").unlink()
            elif mutation == "extra_member":
                (folder / "extra-secret").write_bytes(CANARY.encode())
            elif mutation == "missing_checksum":
                sums.write_text("\n".join(sums.read_text().splitlines()[:-1]) + "\n")
            else:
                sums.write_text(sums.read_text() + sums.read_text().splitlines()[0] + "\n")
            with self.subTest(mutation=mutation), self.assertRaises(updater.Rejected) as failure:
                native.completed_backup(str(self.old), str(output))
            self.assertNotIn(CANARY, str(failure.exception))
            shutil.rmtree(folder)

    def test_resealed_backup_metadata_must_match_old_database_source_and_complete_schema(self):
        output = self.backup_output()
        native = operation.Native(self.spec, self.trust)
        for mutation in ("database", "source_commit", "core_version", "migration_checksums", "schema_prefix"):
            folder = self.backup_files(output)
            path = folder / "BACKUP.json"
            metadata = updater.strict_json(path.read_bytes())
            if mutation == "database":
                metadata["database"] = "another_database"
            elif mutation == "source_commit":
                metadata["source_commit"] = "f" * 40
            elif mutation == "core_version":
                metadata["core_version"] = "0.0.1"
            elif mutation == "migration_checksums":
                metadata["migration_checksums"]["1"] = "f" * 64
            else:
                metadata["schema_version"] = 1
                metadata["migration_checksums"].pop("2")
            path.write_bytes(updater.json_bytes(metadata))
            self.backup_checksums(folder)
            with self.subTest(mutation=mutation), self.assertRaises(updater.Rejected):
                native.completed_backup(str(self.old), str(output))
            shutil.rmtree(folder)

    def test_saved_backup_evidence_rejects_replacement_even_when_new_gzip_and_checksums_are_valid(self):
        output = self.backup_output()
        folder = self.backup_files(output)
        native = operation.Native(self.spec, self.trust)
        saved = native.completed_backup(str(self.old), str(output))
        (folder / "database.sql.gz").write_bytes(gzip.compress(b"-- Different valid backup\nSELECT 2;\n", mtime=0))
        self.backup_checksums(folder)
        native.completed_backup(str(self.old), str(output))
        with self.assertRaises(updater.Rejected):
            native.verify_backup(saved, str(self.old))

    def test_tamper_between_fresh_backup_and_migration_blocks_database_effects(self):
        original = self.native.backup
        def changed_backup(old_release, output):
            evidence = original(old_release, output)
            (Path(evidence["directory"]) / "database.sql.gz").write_bytes(b"Corrupted after checkpoint")
            return evidence
        self.native.backup = changed_backup
        digest = self.plan()["plan_sha256"]
        with self.assertRaises(updater.Rejected):
            self.execute(digest)
        self.assertFalse(self.read_journal()["database_migration"]["started"])
        self.assertEqual([event for event in self.native.events if event[0] == "api"], [])
        self.assertEqual(os.readlink(self.current), str(self.old))
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)

    def test_tampered_backup_blocks_terminal_resume_before_success_claim(self):
        digest = self.plan()["plan_sha256"]
        self.execute(digest)
        saved = self.read_journal()["steps"]["backup"]["result"]
        (Path(saved["directory"]) / "BACKUP.json").unlink()
        before = list(self.native.events)
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)
        self.assertEqual(self.native.events, before)

    def test_schema_does_not_treat_exit_zero_as_current(self):
        facts = operation.prepared_release(str(self.target), self.trust)
        report = self.native.preflight(str(self.target), {})
        for mutation in (
                lambda value: value["database"].update(status="pending_migrations"),
                lambda value: value["database"].update(migration_checksums={}),
                lambda value: value["database"].update(database="other_database"),
                lambda value: value["database"].update(table_visibility_verified=False),
                lambda value: value.update(core_version="0.0.0")):
            changed = copy.deepcopy(report)
            mutation(changed)
            with self.assertRaises(updater.Rejected):
                operation.schema(changed, facts, self.spec["database"], {"current"})

    def test_current_link_refuses_unexpected_target_and_existing_directory(self):
        self.current.unlink()
        self.current.mkdir(mode=0o755)
        with self.assertRaises(updater.Rejected):
            operation.set_current(self.spec, str(self.target), str(self.old), self.trust)
        self.current.rmdir()
        self.current.symlink_to(self.target)
        with self.assertRaises(updater.Rejected):
            operation.set_current(self.spec, str(self.old), str(self.old), self.trust)
        self.assertEqual(os.readlink(self.current), str(self.target))

    def test_operation_lock_rejects_concurrency_and_unsafe_files(self):
        self.state.mkdir(mode=0o700)
        fd = updater.protected_open(str(self.state), True, self.trust, True)
        try:
            with operation.operation_lock(fd, self.trust):
                with self.assertRaises(updater.Rejected):
                    self.execute()
            lock = self.state / ".operation.lock"
            lock.chmod(0o666)
            with self.assertRaises(updater.Rejected):
                self.execute()
            self.assertEqual(self.native.events, [])
        finally:
            os.close(fd)

    def test_instance_lock_covers_different_private_state_directories(self):
        fd = updater.protected_open(str(self.instance), True, self.trust)
        try:
            with operation.operation_lock(fd, self.trust):
                self.spec["state_directory"] = str(self.base / "different-state")
                self.save_spec()
                with self.assertRaises(updater.Rejected):
                    self.execute()
            self.assertEqual(self.native.events, [])
            self.assertEqual(os.readlink(self.current), str(self.old))
        finally:
            os.close(fd)

    def test_terminal_resume_rechecks_current_and_confirmed_secret(self):
        digest = self.plan()["plan_sha256"]
        self.execute(digest)
        before = list(self.native.events)
        self.current.unlink()
        self.current.symlink_to(self.old)
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)
        self.current.unlink()
        self.current.symlink_to(self.target)
        (self.config / "secrets/session_secret").write_bytes(b"ChangedAfterConfirmation_abcdef0123456789\n")
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)
        self.assertEqual(self.native.events, before)

    def test_unknown_or_mismatched_journal_cannot_resume(self):
        digest = self.plan()["plan_sha256"]
        self.execute(digest)
        journal = self.read_journal()
        for change in ({"status": "invented"}, {"operation_id": "b" * 32}, {"action": "install"}):
            self.journal().write_bytes(updater.json_bytes(dict(journal, **change)))
            with self.subTest(change=change), self.assertRaises(updater.Rejected):
                self.execute(digest, resume=True)

    def test_success_receipt_requires_consistent_journal_state(self):
        digest = self.plan()["plan_sha256"]
        self.execute(digest)
        journal = self.read_journal()
        journal["database_migration"] = {"started": False, "completed": False}
        journal["steps"] = {}
        self.journal().write_bytes(updater.json_bytes(journal))
        with self.assertRaises(updater.Rejected):
            self.execute(digest, resume=True)

    def test_status_rejects_forged_or_unknown_journal(self):
        self.state.mkdir(mode=0o700)
        directory = self.state / self.spec["operation_id"]
        directory.mkdir(mode=0o700)
        self.journal().write_bytes(updater.json_bytes({"schema": 1, "operation_id": self.spec["operation_id"], "status": "succeeded"}))
        self.journal().chmod(0o600)
        with self.assertRaises(updater.Rejected):
            operation.status(str(self.state), self.spec["operation_id"], self.trust)

    def test_digest_mismatch_does_not_create_operation_state(self):
        before = snapshot(self.base)
        with self.assertRaises(updater.Rejected):
            self.execute("0" * 64)
        self.assertEqual(snapshot(self.base), before)

    def test_native_runner_clears_ambient_env_and_suppresses_secret_diagnostics(self):
        native = operation.Native(self.spec, self.trust)
        observed = {}
        def child(argv, **kwargs):
            observed.update(argv=argv, **kwargs)
            return types.SimpleNamespace(poll=lambda: 1, returncode=1, pid=999999)
        with patch.dict(os.environ, {"MYSQL_DSN": CANARY, "BOOTSTRAP_PASSWORD": CANARY,
                                    "HTTP_PROXY": CANARY, "LD_PRELOAD": CANARY}), \
                patch.object(operation.subprocess, "Popen", side_effect=child), \
                self.assertRaises(updater.Rejected) as failure:
            native.run(["/usr/bin/systemctl", "show", "aster-test.service"])
        self.assertNotIn(CANARY, str(failure.exception))
        self.assertNotIn(CANARY, json.dumps(observed["env"]))
        self.assertIs(observed["stderr"], subprocess.DEVNULL)
        self.assertTrue(observed["close_fds"])

    def test_native_timeout_diagnostic_does_not_echo_argv_or_output(self):
        native = operation.Native(self.spec, self.trust)
        process = types.SimpleNamespace(poll=lambda: None, returncode=None, pid=999999, wait=lambda: None)
        with patch.object(operation.subprocess, "Popen", return_value=process), \
                patch.object(operation.time, "monotonic", side_effect=(0, 2)), \
                patch.object(operation.os, "killpg") as terminate, \
                self.assertRaises(updater.Rejected) as failure:
            native.run(["/usr/bin/systemctl", "show", "aster-test.service"], timeout=1)
        self.assertNotIn(CANARY, str(failure.exception))
        terminate.assert_called_once()

    def test_native_output_is_bounded_before_reading_into_memory(self):
        native = operation.Native(self.spec, self.trust)
        def child(argv, **kwargs):
            kwargs["stdout"].write(b"X" * ((4 << 20) + 1))
            kwargs["stdout"].flush()
            return types.SimpleNamespace(poll=lambda: 0, returncode=0, pid=999999)
        with patch.object(operation.subprocess, "Popen", side_effect=child), \
                self.assertRaises(updater.Rejected):
            native.run(["/usr/bin/systemctl", "show", "aster-test.service"])

    def test_runtime_preflight_uses_runtime_identity_and_migrator_stays_separate(self):
        native = operation.Native(self.spec, self.trust)
        environment = operation.read_environment(str(self.config / "api.env"), self.trust)[0]
        with patch.object(native, "run", return_value=subprocess.CompletedProcess([], 0, stdout=b"")) as run:
            native.api(str(self.target), environment, "-preflight")
            self.assertEqual(run.call_args.kwargs["identity"], self.spec["runtime_user"])
            migrator = dict(environment, MYSQL_DSN_FILE=self.spec["migrator_dsn_file"])
            native.api(str(self.target), migrator, "-preflight")
            self.assertIsNone(run.call_args.kwargs["identity"])
        with patch.object(native, "run", side_effect=AssertionError("Unknown command executed")), \
                self.assertRaises(updater.Rejected):
            native.api(str(self.target), environment, "-reset-admin")

    def test_environment_file_never_evaluates_shell_or_inline_secrets(self):
        path = self.config / "api.env"
        original = path.read_bytes()
        for line in ("MYSQL_DSN=" + CANARY, "APP_ORIGIN=$(touch /tmp/aster-must-never-exist)",
                     "APP_ORIGIN=`id`", "export APP_MODE=production", "APP_ORIGIN='https://example.test'"):
            path.write_bytes(original + (line + "\n").encode())
            with self.subTest(line=line), self.assertRaises(updater.Rejected) as failure:
                operation.read_environment(str(path), self.trust)
            self.assertNotIn(CANARY, str(failure.exception))

    def test_dsn_refuses_tcp_other_socket_and_other_database(self):
        path = Path(self.spec["migrator_dsn_file"])
        for value in ("aster:x@tcp(127.0.0.1:3306)/aster_test?parseTime=true",
                      "aster:x@unix(/run/other.sock)/aster_test?parseTime=true",
                      "aster:x@unix(/run/mysqld/mysqld.sock)/other?parseTime=true"):
            path.write_text(value)
            with self.assertRaises(updater.Rejected):
                operation.dsn_target(str(path), self.spec, self.trust, True)

    def test_status_is_read_only_and_omits_private_paths(self):
        self.execute()
        before = snapshot(self.base)
        value = operation.status(str(self.state), self.spec["operation_id"], self.trust)
        self.assertEqual(value["status"], "succeeded")
        self.assertEqual(snapshot(self.base), before)
        self.assertNotIn(str(self.base), json.dumps(value))
        self.assertNotIn(CANARY, json.dumps(value))

    def test_new_backup_directory_uses_private_descriptor_and_ordered_acl_checks(self):
        path = self.private / "new-backup-output"
        events, captured = [], {}
        actual_chmod, actual_sync = operation.os.fchmod, operation.os.fsync
        def remove(descriptor, name):
            self.assertIs(type(descriptor), int)
            self.assertEqual(name, "system.posix_acl_default")
            self.assertEqual(stat.S_IMODE(os.fstat(descriptor).st_mode), 0o700)
            self.assertEqual(os.fstat(descriptor).st_ino, path.stat().st_ino)
            captured["descriptor"] = descriptor
            events.append("remove")
        def read(descriptor, name):
            self.assertEqual((descriptor, name), (captured["descriptor"], "system.posix_acl_default"))
            events.append("read")
            raise OSError(errno.ENODATA, "Mocked absent ACL")
        def chmod(descriptor, mode):
            if descriptor == captured.get("descriptor"):
                self.assertEqual(mode, 0o700)
                events.append("chmod")
            return actual_chmod(descriptor, mode)
        def sync(descriptor):
            if descriptor == captured.get("descriptor"):
                events.append("fsync")
            return actual_sync(descriptor)
        with patch.object(operation, "create_directory", wraps=operation.create_directory) as create, \
                patch.object(operation.os, "removexattr", side_effect=remove), \
                patch.object(operation.os, "getxattr", side_effect=read), \
                patch.object(operation.os, "fchmod", side_effect=chmod), \
                patch.object(operation.os, "fsync", side_effect=sync):
            operation.create_backup_directory(str(path), self.trust)
        create.assert_called_once_with(str(path), 0o700, self.trust)
        self.assertEqual(events, ["remove", "read", "chmod", "fsync"])
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o700)
        self.assertEqual(path.stat().st_uid, self.trust.owner)
        self.assert_descriptor_closed(captured["descriptor"])

    def test_backup_directory_accepts_only_mocked_absent_or_unsupported_acl_results(self):
        for index, (remove_errno, read_errno) in enumerate((
                (None, errno.ENODATA), (errno.ENODATA, errno.ENODATA),
                (errno.EOPNOTSUPP, errno.EOPNOTSUPP), (errno.ENODATA, errno.EOPNOTSUPP))):
            remove_failure = None if remove_errno is None else OSError(remove_errno, "Mocked ACL removal result")
            path = self.private / ("acl-case-" + str(index))
            with self.subTest(index=index), \
                    patch.object(operation.os, "removexattr", side_effect=remove_failure, return_value=None) as remove, \
                    patch.object(operation.os, "getxattr", side_effect=OSError(read_errno, "Mocked ACL read result")) as read:
                operation.create_backup_directory(str(path), self.trust)
            self.assertEqual(remove.call_args.args[0], read.call_args.args[0])
            self.assertEqual(remove.call_args.args[1], "system.posix_acl_default")
            self.assertEqual(read.call_args.args[1], "system.posix_acl_default")
            self.assert_descriptor_closed(remove.call_args.args[0])

    def test_backup_directory_never_changes_existing_output_or_calls_acl_syscalls(self):
        path = self.directory("private/existing-backup", 0o750)
        self.write("private/existing-backup/canary", CANARY.encode())
        before = snapshot(self.base)
        with patch.object(operation.os, "fchmod", wraps=operation.os.fchmod) as chmod, \
                self.assertRaises(updater.Rejected):
            operation.create_backup_directory(str(path), self.trust)
        self.assertEqual(snapshot(self.base), before)
        chmod.assert_not_called()
        self.acl_remove.assert_not_called()
        self.acl_get.assert_not_called()

    def test_backup_directory_rejects_unexpected_io_errors_and_closes_descriptor(self):
        actual_chmod, actual_sync = operation.os.fchmod, operation.os.fsync
        for stage in ("remove", "read", "chmod", "fsync"):
            path = self.private / ("failed-backup-" + stage)
            captured = {}
            def remove(descriptor, name):
                captured["descriptor"] = descriptor
                if stage == "remove":
                    raise OSError(errno.EACCES, CANARY)
                raise OSError(errno.ENODATA, "Mocked absent ACL")
            def read(descriptor, name):
                raise OSError(errno.EIO if stage == "read" else errno.ENODATA, CANARY)
            def chmod(descriptor, mode):
                if stage == "chmod" and descriptor == captured.get("descriptor"):
                    raise OSError(errno.EPERM, CANARY)
                return actual_chmod(descriptor, mode)
            def sync(descriptor):
                if stage == "fsync" and descriptor == captured.get("descriptor"):
                    raise OSError(errno.EIO, CANARY)
                return actual_sync(descriptor)
            with self.subTest(stage=stage), \
                    patch.object(operation.os, "removexattr", side_effect=remove), \
                    patch.object(operation.os, "getxattr", side_effect=read), \
                    patch.object(operation.os, "fchmod", side_effect=chmod), \
                    patch.object(operation.os, "fsync", side_effect=sync), \
                    self.assertRaises(updater.Rejected) as failure:
                operation.create_backup_directory(str(path), self.trust)
            self.assertEqual(str(failure.exception), "New private backup directory ACL could not be normalized.")
            self.assertNotIn(CANARY, str(failure.exception))
            self.assert_descriptor_closed(captured["descriptor"])
        with patch.object(operation, "create_directory", side_effect=OSError(errno.EIO, CANARY)), \
                self.assertRaises(updater.Rejected) as failure:
            operation.create_backup_directory(str(self.private / "failed-creation"), self.trust)
        self.assertNotIn(CANARY, str(failure.exception))

    def test_backup_directory_refuses_any_persistent_default_acl(self):
        for index, value in enumerate((b"", b"Mocked persistent default ACL")):
            path = self.private / ("persistent-acl-" + str(index))
            with patch.object(operation.os, "getxattr", return_value=value, side_effect=None) as read, \
                    self.assertRaises(updater.Rejected):
                operation.create_backup_directory(str(path), self.trust)
            self.assert_descriptor_closed(read.call_args.args[0])

    def test_backup_directory_refuses_unsafe_paths_before_acl_syscalls(self):
        alias = self.private / "backup-link"
        alias.symlink_to(self.private, target_is_directory=True)
        ancestor = self.base / "backup-parent-link"
        ancestor.symlink_to(self.private, target_is_directory=True)
        for path in ("/", "relative-backup", str(alias), str(ancestor / "new-backup")):
            with self.subTest(path=path), self.assertRaises(updater.Rejected):
                operation.create_backup_directory(path, self.trust)
        self.private.chmod(0o777)
        try:
            with self.assertRaises(updater.Rejected):
                operation.create_backup_directory(str(self.private / "unsafe-parent-output"), self.trust)
        finally:
            self.private.chmod(0o700)
        self.acl_remove.assert_not_called()
        self.acl_get.assert_not_called()
        self.assertTrue(alias.is_symlink())

    def test_backup_directory_requires_private_mode_and_expected_owner_before_acl_access(self):
        path = self.private / "wrong-mode-output"
        def wrong_mode(output, mode, trust):
            Path(output).mkdir(mode=0o700)
            Path(output).chmod(0o755)
        with patch.object(operation, "create_directory", side_effect=wrong_mode), \
                self.assertRaises(updater.Rejected):
            operation.create_backup_directory(str(path), self.trust)
        actual_stat = operation.os.fstat
        other = self.private / "wrong-owner-output"
        def wrong_owner(descriptor):
            info = actual_stat(descriptor)
            if os.readlink("/proc/self/fd/" + str(descriptor)) == str(other):
                values = {name: getattr(info, name) for name in dir(info) if name.startswith("st_")}
                values["st_uid"] = self.trust.owner + 1
                return types.SimpleNamespace(**values)
            return info
        with patch.object(operation.os, "fstat", side_effect=wrong_owner), self.assertRaises(updater.Rejected):
            operation.create_backup_directory(str(other), self.trust)
        self.acl_remove.assert_not_called()
        self.acl_get.assert_not_called()

    def test_backup_directory_detects_changed_metadata_and_closes_descriptor(self):
        actual_stat = operation.os.fstat
        for index, changed in enumerate(("st_dev", "st_ino", "st_uid", "st_gid", "st_nlink", "st_mode")):
            captured = {}
            def remove(descriptor, name):
                captured["descriptor"] = descriptor
                raise OSError(errno.ENODATA, "Mocked absent ACL")
            def changed_metadata(descriptor):
                info = actual_stat(descriptor)
                if descriptor == captured.get("descriptor"):
                    values = {name: getattr(info, name) for name in dir(info) if name.startswith("st_")}
                    values[changed] = (info.st_mode & ~0o777) | 0o755 if changed == "st_mode" else values[changed] + 1
                    return types.SimpleNamespace(**values)
                return info
            path = self.private / ("changed-metadata-" + str(index))
            with self.subTest(changed=changed), patch.object(operation.os, "removexattr", side_effect=remove), \
                    patch.object(operation.os, "fstat", side_effect=changed_metadata), \
                    self.assertRaises(updater.Rejected):
                operation.create_backup_directory(str(path), self.trust)
            self.assert_descriptor_closed(captured["descriptor"])

    def test_upgrade_backup_uses_acl_checked_private_output_helper(self):
        with patch.object(operation, "create_backup_directory", wraps=operation.create_backup_directory) as create:
            self.execute()
        create.assert_called_once_with(str(self.state / self.spec["operation_id"] / "backups"), self.trust)
        self.assertEqual(self.acl_remove.call_count, 1)
        self.assertEqual(self.acl_get.call_count, 1)

    def test_cli_keeps_root_requirement_and_no_relaxed_trust_options(self):
        output = io.StringIO()
        with patch.object(operation.os, "geteuid", return_value=12345), contextlib.redirect_stderr(output):
            self.assertEqual(operation.main(["plan", "--spec", str(self.spec_path)]), 1)
        self.assertIn("root", output.getvalue())
        with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            operation.main(["plan", "--spec", str(self.spec_path), "--trust-owner", "12345"])


class EdgeLoggingTests(unittest.TestCase):
    # Pure rendering/contract checks: no Linux ACL or host/service fixture.
    def setUp(self):
        self.spec = {"release_directory": "/unused", "instance_root": "/opt/aster-test", "config_root": "/etc/aster-test",
                     "runtime_user": "asttest", "api_unit": "aster-test.service", "nginx_binary": "/usr/sbin/nginx",
                     "origin": "https://billing.example.test", "edge_listen_address": "127.0.0.1",
                     "listen_address": "127.0.0.1:18080", "certificate": "/private/certificate", "certificate_key": "/private/key"}

    def test_generated_and_manual_nginx_share_only_safe_json_log_fields(self):
        block = operation.nginx_logging_text()
        template = (SCRIPTS.parent / "nginx.native.conf").read_text()
        manual = template.split("# BEGIN ASTER PRIVACY LOGGING", 1)[1].split("# END ASTER PRIVACY LOGGING", 1)[0]
        self.assertEqual(block, "# BEGIN ASTER PRIVACY LOGGING" + manual + "# END ASTER PRIVACY LOGGING")
        self.assertIn("escape=json", block)
        self.assertIn('default OTHER;', block)
        self.assertIn('"~^[0-9a-f]{32}$" $upstream_http_x_request_id;', block)
        # The maps may inspect URI/method to classify them, but log_format must
        # never interpolate the original URI, method or any client header.
        format_text = block.split("log_format aster_access", 1)[1]
        variables = set(re.findall(r"\$[a-z_0-9]+", format_text))
        self.assertEqual(variables, {"$time_iso8601", "$request_id", "$aster_api_request_id", "$aster_log_method",
                                     "$aster_log_route", "$status", "$body_bytes_sent", "$request_time",
                                     "$upstream_status", "$upstream_response_time"})
        destination = "access_log syslog:server=unix:/dev/log,tag=aster_edge,severity=info,nohostname aster_access;"
        self.assertEqual(template.count(destination), 4)
        self.assertEqual(template.count("error_log /dev/null warn;"), 4)
        # Nothing in the shared http include sets another site's logging policy.
        before_servers = template.split("\nserver {", 1)[0]
        self.assertNotIn("access_log ", before_servers)
        self.assertNotIn("error_log ", before_servers)

    def test_generated_nginx_preserves_header_inheritance_and_safe_log_sink(self):
        text = operation.nginx_text(self.spec).decode()
        self.assertIn(operation.nginx_logging_text(), text)
        self.assertIn("expires -1;", text)
        location = text.split("    location / {", 1)[1]
        self.assertNotRegex(location, r"(?m)^\s*add_header\b")
        self.assertNotIn("access_log off", text)
        self.assertIn("access_log syslog:server=unix:/dev/log,tag=aster_edge,severity=info,nohostname aster_access;", text)
        self.assertNotIn("error_log stderr", text)

    def test_product_configuration_and_provisioning_routes_use_fixed_private_safe_labels(self):
        # Pure contract test of the actual allowlist, not nginx execution.
        block = operation.nginx_logging_text()
        routes = block.split("map $uri $aster_log_route {", 1)[1].split("\n}", 1)[0]
        regular = []
        exact = {}
        for line in routes.splitlines():
            match = re.fullmatch(r'\s*"~([^\"]+)"\s+([^;]+);', line)
            if match:
                regular.append((re.compile(match[1]), match[2]))
            else:
                fields = line.strip().removesuffix(";").split()
                if len(fields) == 2:
                    exact[fields[0]] = fields[1]
        def label(uri):
            return exact.get(uri) or next((value for pattern, value in regular if pattern.search(uri)), exact["default"])
        identifier = "a1" * 16
        cases = {
            "/api/v1/admin/catalog/starter/product-configuration": "/api/v1/admin/catalog/:id/product-configuration",
            "/api/v1/admin/catalog/" + "p" * 64 + "/product-configuration": "/api/v1/admin/catalog/:id/product-configuration",
            "/api/v1/admin/orders/" + identifier + "/provisioning-jobs": "/api/v1/admin/orders/:id/provisioning-jobs",
            "/api/v1/admin/provisioning-jobs/" + identifier: "/api/v1/admin/provisioning-jobs/:id",
        }
        for uri, expected in cases.items():
            with self.subTest(uri=uri):
                self.assertEqual(label(uri), expected)
                self.assertNotIn(identifier, label(uri))
                self.assertNotIn("starter", label(uri))
        for uri in (
            "/api/v1/admin/catalog/" + "p" * 65 + "/product-configuration",
            "/api/v1/admin/catalog/" + CANARY + "/nested/product-configuration",
            "/api/v1/admin/orders/" + identifier[:-1] + "/provisioning-jobs",
            "/api/v1/admin/orders/" + identifier.upper() + "/provisioning-jobs",
            "/api/v1/admin/provisioning-jobs/" + identifier + "/retry",
            "/api/v1/admin/provisioning-jobs/" + CANARY,
        ):
            with self.subTest(invalid_uri=uri):
                self.assertEqual(label(uri), "OTHER")
        format_text = block.split("log_format aster_access", 1)[1]
        for raw in ("$uri", "$request_uri", "$args", "$query_string", "$http_authorization", "$request_body"):
            self.assertNotIn(raw, format_text)

    def test_syslog_tags_use_nginx_supported_grammar(self):
        template = (SCRIPTS.parent / "nginx.native.conf").read_text()
        for text, expected_count in ((template, 4), (operation.nginx_text(self.spec).decode(), 1)):
            tags = re.findall(r"\btag=([^,;\s]+)", text)
            self.assertEqual(len(tags), expected_count)
            for tag in tags:
                self.assertRegex(tag, r"^[A-Za-z0-9_]+$")
                self.assertEqual(tag, "aster_edge")

    def test_fixture_initial_error_log_option_preserves_supported_version_floor(self):
        edge = load("edge_initial_log_options_under_test", SCRIPTS.parent / "ci/edge-observability.py")
        self.assertEqual(edge.initial_error_log_options("nginx version: nginx/1.19.4"), [])
        for version in ("1.19.5", "1.24.0 (Ubuntu)", "1.30.6"):
            self.assertEqual(edge.initial_error_log_options("nginx version: nginx/" + version), ["-e", "/dev/null"])
        for version in ("nginx/1.18.0", "nginx/1.19.3", "unknown version"):
            with self.subTest(version=version), self.assertRaises(RuntimeError):
                edge.initial_error_log_options(version)

    def test_generated_units_disable_core_dumps_and_bound_journal_rate(self):
        with patch.object(operation, "read_file", return_value=(SCRIPTS.parent / "systemd/aster-api.service").read_bytes()):
            api = operation.api_unit_text(self.spec).decode()
        for unit in (api, operation.edge_unit_text(self.spec).decode()):
            self.assertIn("LimitCORE=0\n", unit)
            self.assertIn("LogRateLimitIntervalSec=30s\n", unit)
            self.assertIn("LogRateLimitBurst=10000\n", unit)
        for name in ("aster-smtp.service", "aster-payment.service"):
            unit = (SCRIPTS.parent / "systemd" / name).read_text()
            self.assertIn("StandardOutput=null\n", unit)
            self.assertIn("StandardError=null\n", unit)

    def test_safe_edge_log_parser_rejects_private_fields_and_forged_ids(self):
        edge = load("edge_observability_fixture_under_test", SCRIPTS.parent / "ci/edge-observability.py")
        record = {"event": "edge_request", "time": "2026-10-20T00:00:00+00:00", "edge_id": "c" * 32,
                  "api_request_id": edge.API_ID, "method": "GET", "route": "/api/v1/invoices/:id",
                  "status": 200, "body_bytes": 16, "request_time": "0.001",
                  "upstream_status": "200", "upstream_response_time": "0.001"}
        self.assertEqual(edge.log_record(b"<190>fixture aster_edge: " + json.dumps(record).encode()), record)
        for change in ({"route": edge.CANARY}, {"route": edge.RESOURCE_ID}, {"edge_id": edge.CLIENT_ID},
                       {"api_request_id": edge.CANARY}, {"method": "PRIVATE_METHOD"}, {"status": "200"},
                       {"body_bytes": "16"}, {"request_time": "-1"}, {"raw_path": "/secret"}):
            with self.subTest(fields=sorted(change)), self.assertRaises((RuntimeError, ValueError)):
                edge.log_record(json.dumps(dict(record, **change)).encode())

    def test_native_command_diagnostic_contains_no_argv_output_or_environment(self):
        native = operation.Native(dict(self.spec, operation_id="a" * 32))
        output = io.StringIO()
        process = types.SimpleNamespace(poll=lambda: 42, returncode=42, pid=999999)
        with contextlib.redirect_stderr(output), patch.object(operation.subprocess, "Popen", return_value=process), \
                self.assertRaises(updater.Rejected):
            native.run(["/private/" + CANARY, CANARY], env={"MYSQL_DSN": CANARY}, data=CANARY.encode())
        value = json.loads(output.getvalue())
        self.assertEqual(value, {"event": "native_command_failed", "operation_id": "a" * 32,
                                 "phase": "native_command", "tool_role": "OTHER", "exit_code": 42, "reason": "command_failed"})
        self.assertNotIn(CANARY, output.getvalue())


if __name__ == "__main__":
    unittest.main()
