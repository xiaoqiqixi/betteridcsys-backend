#!/usr/bin/env python3
"""Offline adversarial verification plus isolated HTTP handoff E2E; no application.

Private filesystem trust and injected test transport are Python-only fixtures;
the production CLI exposes neither. All sockets in this test are loopback.
"""
import base64
import copy
import gzip
import hashlib
import http.server
from email.message import Message
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import shutil
import struct
import subprocess
import sys
import tarfile
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("update-manager.py")
SPEC = importlib.util.spec_from_file_location("aster_update_manager", SCRIPT)
manager = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = manager
SPEC.loader.exec_module(manager)
REPO = SCRIPT.parents[3]
NOW = manager.timestamp("2026-10-14T17:00:00Z")
SOURCE = "1" * 40


def encoded(value):
    return manager.json_bytes(value)


def elf():
    raw = bytearray(256)
    raw[:7] = b"\x7fELF\x02\x01\x01"
    struct.pack_into("<HHIQQQIHHHHHH", raw, 16, 2, 62, 1, 0x400080, 64, 0, 0, 64, 56, 1, 0, 0, 0)
    struct.pack_into("<IIQQQQQQ", raw, 64, 1, 5, 0, 0x400000, 0x400000, 256, 256, 4096)
    return bytes(raw)


def archive(files, component, version, edit=None, extras=(), distribution_format="archive-v1"):
    root = manager.archive_name(component, version, distribution_format)[:-7]
    directories = {root}
    for name in files:
        parts = name.split("/")
        directories.update(root + "/" + "/".join(parts[:index]) for index in range(1, len(parts)))
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w:gz", format=tarfile.GNU_FORMAT) as stream:
        for name in sorted(directories):
            item = tarfile.TarInfo(name)
            item.type, item.mode = tarfile.DIRTYPE, 0o755
            stream.addfile(item)
        for name, (raw, mode) in sorted(files.items()):
            item = tarfile.TarInfo(root + "/" + name)
            item.mode, item.size = mode, len(raw)
            if edit:
                edit(item)
            stream.addfile(item, io.BytesIO(raw) if item.isreg() else None)
        for item, raw in extras:
            stream.addfile(item, io.BytesIO(raw) if item.isreg() else None)
    return output.getvalue()


class Fixture:
    def __init__(self, component="smtp", raw=False, core_version=None):
        self.format = "raw-binaries-v1" if raw else "archive-v1"
        self.component = component
        self.version = "1.0.1" if component == "smtp" else "0.4.1"
        if raw: self.version = "1.1.0" if component == "smtp" else "0.5.0"
        selected_core = core_version or ("0.5.0" if raw else "0.4.1")
        if component == "core" and core_version is not None:
            self.version = core_version
        self.tag = ("smtp-v" if component == "smtp" else "v") + self.version
        self.source = {path: (REPO / path).read_bytes() for path in manager.INPUTS}
        self.source["vps-billing/backend/pkg/pluginapi/protocol.go"] = b'package pluginapi\nconst CoreVersion = "0.4.1"\n'
        self.source["vps-billing/backend/plugins/smtp/VERSION"] = b"1.0.1\n"
        if raw:
            self.source["vps-billing/backend/pkg/pluginapi/protocol.go"] = ('package pluginapi\nconst CoreVersion = "' + selected_core + '"\n').encode()
            self.source["vps-billing/backend/plugins/smtp/VERSION"] = b"1.1.0\n"
        for path in manager.TRUSTED_PATHS:
            self.source[path] = b"Reviewed policy fixture " + path.encode() + b"\n"
        binaries = {"plugin": elf()} if component == "smtp" else {"bin/aster-api": elf(), "bin/aster-plugin-host": elf()}
        copies = {"LICENSE": "LICENSE"}
        self.bundle = {path: (raw, 0o755) for path, raw in binaries.items()}
        if component == "smtp":
            copies["README.md"] = "vps-billing/backend/plugins/smtp/README.md"
            self.source[copies["README.md"]] = b"Independent fixture SMTP; no message is sent.\n"
            self.bundle["manifest.json"] = (encoded({"schema": 1, "id": "email.smtp", "version": self.version, "protocol": "aster.smtp/1", "core_min": "0.4.0", "core_max_exclusive": "0.6.0" if raw else "0.5.0", "capabilities": ["smtp.send"], "executable": "plugin", "sha256": manager.digest(elf())}), 0o644)
            self.bundle["THIRD_PARTY_NOTICES.json"] = (encoded({"schema": 1, "notices": [{"path": "go-runtime/LICENSE", "sha256": manager.digest(b"Fixture original runtime notice\n"), "text": "Fixture original runtime notice\n"}]}), 0o644)
        else:
            copies.update({"REPOSITORY-README.md": "README.md", "PROJECT-README.md": "vps-billing/README.md"})
            copies.update({"manifests/go.mod": "vps-billing/backend/go.mod", "manifests/go.sum": "vps-billing/backend/go.sum", "manifests/package.json": "vps-billing/frontend/package.json", "manifests/package-lock.json": "vps-billing/frontend/package-lock.json"})
            self.source["vps-billing/README.md"] = b"Fixture project guide\n"
            for path in ("deploy/scripts/backup-native.sh", "deploy/scripts/plugin-manager.py", "deploy/scripts/update-manager.py", "deploy/systemd/aster-api.service", "deploy/systemd/aster-smtp.service", "deploy/systemd/api.env.example", "deploy/nginx.native.conf"):
                copies[path] = "vps-billing/" + path
                self.source[copies[path]] = b"Reviewed fixture deployment file\n"
            for minimum, path in (((0, 5, 1), "deploy/scripts/core-manager.py"), ((0, 5, 2), "deploy/scripts/core-operation.py"),
                                  ((0, 5, 3), "deploy/systemd/aster-payment.service")):
                if manager.stable(selected_core) >= minimum:
                    copies[path] = "vps-billing/" + path
            for path in ("manifests/go-dependencies.json", "manifests/npm-installed-dependencies.json", "manifests/aster-api-build.txt", "manifests/aster-plugin-host-build.txt", "web/index.html"):
                self.bundle[path] = (b"Fixture runtime metadata\n", 0o644)
        if raw and component == "core":
            for name in ("001_core.sql", "007_currency_registry.sql"):
                copies["migrations/" + name] = "vps-billing/backend/migrations/" + name
                self.source[copies["migrations/" + name]] = ("-- Reviewed fixture SQL migration " + name + "\n").encode()
        for destination, source in copies.items():
            self.bundle[destination] = (self.source[source], 0o755 if destination.startswith("deploy/scripts/") else 0o644)
        self.build = {"schema": 2, "release_kind": component, "release_label": self.version, "source_commit": SOURCE, "target": "linux/amd64", "cgo_enabled": False, "component_versions": {"core": "0.4.1", "smtp": "1.0.1"}, "plugin_protocol": "aster.smtp/1", "smtp_core_compatibility": {"min": "0.4.0", "max_exclusive": "0.5.0"}, "input_sha256": {path: manager.digest(raw) for path, raw in self.source.items() if path in manager.INPUTS}, "source_files": copies, "binary_sha256": {path: manager.digest(raw) for path, raw in binaries.items()}}
        if raw:
            self.build.update(schema=3, distribution_format=self.format, component_versions={"core": selected_core, "smtp": "1.1.0"}, smtp_core_compatibility={"min": "0.4.0", "max_exclusive": "0.6.0"}, binary_assets=manager.binary_assets(component, self.version), resources_asset=manager.archive_name(component, self.version, self.format))
        self.files = {}
        self.refresh()
        self.calls = []

    def refresh(self):
        if self.component == "smtp":
            self.bundle["SHA256SUMS"] = ("".join(manager.digest(raw) + "  " + path + "\n" for path, (raw, _) in sorted(self.bundle.items()) if path != "SHA256SUMS").encode(), 0o644)
        else:
            self.bundle["manifests/BUILD.json"] = (encoded(self.build), 0o644)
        resources = dict(self.bundle)
        raw_assets = {}
        if self.format == "raw-binaries-v1":
            for path, name in manager.binary_assets(self.component, self.version).items():
                raw_assets[name] = resources.pop(path)[0]
        self.files = {"BUILD.json": encoded(self.build), manager.archive_name(self.component, self.version, self.format): archive(resources, self.component, self.version, distribution_format=self.format), **raw_assets}
        self.provenance = {"schema": 1, "repository": manager.REPOSITORY, "repository_id": manager.REPOSITORY_ID, "run_id": 1234, "run_attempt": 1, "event": "push", "tag": self.tag, "version": self.version, "source_sha": SOURCE, "run_head_sha": SOURCE, "workflow_sha": SOURCE, "workflow_path": manager.WORKFLOW, "default_branch": "main", "default_sha": SOURCE, "trusted_blobs": {path: manager.blob_sha(self.source[path]) for path in manager.trusted_paths_for_core(self.build["component_versions"]["core"])}, "artifact_name": "candidate-1234-1", "assets": {path: {"size": len(raw), "sha256": manager.digest(raw)} for path, raw in self.files.items()}}
        self.files["PROVENANCE.json"] = encoded(self.provenance)
        self.files["SHA256SUMS"] = "".join(manager.digest(raw) + "  " + path + "\n" for path, raw in sorted(self.files.items())).encode()
        self.assets = [{"name": path, "id": index + 100, "size": len(raw), "sha256": manager.digest(raw)} for index, (path, raw) in enumerate(sorted(self.files.items()))]
        self.intent = {"schema": 1, "request_id": "a" * 32, "actor_id": "b" * 32, "created_at": "2026-10-14T17:00:00Z", "expires_at": "2026-10-14T17:30:00Z", "repository": manager.REPOSITORY, "repository_id": manager.REPOSITORY_ID, "component": self.component, "tag": self.tag, "version": self.version, "source_sha": SOURCE, "release_id": 5678, "run_id": 1234, "run_attempt": 1, "current_core_version": "0.4.0", "current_plugin_version": "1.0.0", "plugin_policy_revision": 7, "assets": copy.deepcopy(self.assets)}
        if self.format == "raw-binaries-v1": self.intent.update(schema=2, distribution_format=self.format)
        self.envelope = {"schema": self.intent["schema"], "status": "awaiting_host_operator", "intent": self.intent}
        identity = {"id": manager.REPOSITORY_ID, "full_name": manager.REPOSITORY}
        self.responses = {"/git/ref/tags/" + self.tag: {"ref": "refs/tags/" + self.tag, "object": {"type": "commit", "sha": SOURCE}}, "/compare/" + SOURCE + "...main": {"status": "identical", "merge_base_commit": {"sha": SOURCE}}, "/releases/tags/" + self.tag: {"id": 5678, "tag_name": self.tag, "draft": False, "prerelease": False, "target_commitish": SOURCE, "assets": [dict(name=a["name"], id=a["id"], size=a["size"], digest="sha256:" + a["sha256"], state="uploaded") for a in self.assets]}, "/actions/workflows/release-build.yml": {"id": 33, "path": manager.WORKFLOW, "state": "active"}, "/actions/runs/1234": {"id": 1234, "run_attempt": 1, "workflow_id": 33, "path": manager.WORKFLOW, "status": "completed", "conclusion": "success", "event": "push", "head_sha": SOURCE, "head_branch": self.tag, "repository": identity, "head_repository": identity}, "/git/trees/" + SOURCE + "?recursive=1": {"truncated": False, "tree": [{"path": path, "type": "blob", "mode": "100644", "sha": manager.blob_sha(raw)} for path, raw in self.source.items()]}}
        for raw in self.source.values():
            blob = manager.blob_sha(raw)
            self.responses["/git/blobs/" + blob] = {"sha": blob, "size": len(raw), "encoding": "base64", "content": base64.b64encode(raw).decode()}

    def repository(self):
        self.calls.append("repository")
        return {"id": manager.REPOSITORY_ID, "full_name": manager.REPOSITORY, "default_branch": "main"}

    def reseal(self):
        """An attacker replacing all release asset bytes still faces source checks."""
        self.files["PROVENANCE.json"] = encoded(self.provenance)
        self.files["SHA256SUMS"] = "".join(manager.digest(raw) + "  " + path + "\n" for path, raw in sorted(self.files.items()) if path != "SHA256SUMS").encode()
        for asset in self.assets:
            asset.update(size=len(self.files[asset["name"]]), sha256=manager.digest(self.files[asset["name"]]))
        self.intent["assets"] = copy.deepcopy(self.assets)
        self.responses["/releases/tags/" + self.tag]["assets"] = [dict(name=a["name"], id=a["id"], size=a["size"], digest="sha256:" + a["sha256"], state="uploaded") for a in self.assets]

    def json(self, path):
        self.calls.append(path)
        return copy.deepcopy(self.responses[path])

    def asset(self, asset_id, limit):
        self.calls.append(asset_id)
        asset = next(item for item in self.assets if item["id"] == asset_id)
        return self.files[asset["name"]]


class CredentialTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="aster-opaque-credential-")
        self.base = Path(self.temp.name)
        self.base.chmod(0o700)
        self.path = self.base / "read-token"
        self.trust = manager._Trust(owner=os.getuid(), ancestor=os.stat("/").st_uid)

    def tearDown(self):
        self.temp.cleanup()

    def save(self, raw):
        self.path.write_bytes(raw)
        self.path.chmod(0o600)

    def test_opaque_legacy_new_and_bounded_transport_syntax(self):
        # Deliberately synthetic strings, never credentials or JWT assertions.
        for value in ("a", "github_pat_fixture", "ghs_42_" + "a-_." * 130,
                      "AZaz09-._~+/==", "x" * manager.MAX_GITHUB_TOKEN):
            with self.subTest(length=len(value)):
                self.assertTrue(manager.valid_github_token(value))
                self.assertEqual(manager.Official(value).token, value)
        self.assertEqual(manager.Official().token, "")

    def test_header_injection_non_ascii_malformed_and_oversized_rejected_redacted(self):
        for value in (None, 7, b"bytes", "", "x" * (manager.MAX_GITHUB_TOKEN + 1),
                      " leading", "trailing ", "a b", "a\tb", "a\rb", "a\nb",
                      "a\r\nInjected: value", "a\x00b", "a\x7fb", "a\u00e9b", "a=b", "===", "a:b"):
            with self.subTest(kind=type(value).__name__):
                self.assertFalse(manager.valid_github_token(value))
                if value != "":
                    with self.assertRaises(manager.Rejected) as caught:
                        manager.Official(value)
                    self.assertEqual(str(caught.exception), "Invalid protected GitHub credential format.")

    def test_protected_file_exact_bound_and_one_optional_line_ending(self):
        for token in (b"ghs_42_" + b"a-_." * 130, b"x" * manager.MAX_GITHUB_TOKEN):
            for ending in (b"", b"\n", b"\r\n"):
                with self.subTest(length=len(token), ending=ending):
                    self.save(token + ending)
                    self.assertEqual(manager.load_protected_token(str(self.path), self.trust), token.decode("ascii"))
        for raw in (b"", b"\n", b"\r\n", b"a\r", b"a\n\n", b"a\r\n\r\n", b" a ", b"a\nInjected: value",
                    b"a\x00b", b"a\xffb", b"a=b", b"=" * 20, b"x" * (manager.MAX_GITHUB_TOKEN + 1),
                    b"x" * (manager.MAX_GITHUB_TOKEN + 3)):
            with self.subTest(length=len(raw)):
                self.save(raw)
                with self.assertRaises(manager.Rejected):
                    manager.load_protected_token(str(self.path), self.trust)

    def test_private_protection_links_and_modes_still_required(self):
        self.save(b"ghs_fixture")
        self.path.chmod(0o640)
        with self.assertRaises(manager.Rejected):
            manager.load_protected_token(str(self.path), self.trust)
        self.path.chmod(0o600)
        link = self.base / "link"
        link.symlink_to(self.path)
        with self.assertRaises(manager.Rejected):
            manager.load_protected_token(str(link), self.trust)
        link.unlink()
        os.link(self.path, link)
        with self.assertRaises(manager.Rejected):
            manager.load_protected_token(str(self.path), self.trust)


class LongNameArchiveTest(unittest.TestCase):
    """The historical producer uses exactly GNU's bounded LongLink marker."""
    root = "aster-core-0.5.0-linux-amd64-resources"
    relative = "licenses/npm/@babel_helper-validator-identifier@7.29.7/LICENSE"

    @staticmethod
    def checksum(raw, offset):
        raw[offset + 148:offset + 156] = b" " * 8
        value = sum(raw[offset:offset + 512])
        raw[offset + 148:offset + 156] = ("%06o\0 " % value).encode()

    def legal_archive(self, relative=None, extras=()):
        # Python's fixture writer uses mode 0000 for generated extension records;
        # the actual GNU tar producer uses 0644. Model its exact historical form.
        compressed = archive({relative or self.relative: (b"Original license text\n", 0o644)}, "core", "0.5.0", extras=extras, distribution_format="raw-binaries-v1")
        raw, offset = bytearray(gzip.decompress(compressed)), 0
        while offset + 512 <= len(raw) and any(raw[offset:offset + 512]):
            size = manager.octal(raw[offset + 124:offset + 136])
            if raw[offset + 156:offset + 157] == b"L":
                raw[offset + 100:offset + 108] = b"0000644\0"
                self.checksum(raw, offset)
            offset += 512 + (size + 511) // 512 * 512
        return raw

    def setUp(self):
        self.raw = self.legal_archive()
        offset = 0
        while self.raw[offset + 156:offset + 157] != b"L":
            offset += 512 + (manager.octal(self.raw[offset + 124:offset + 136]) + 511) // 512 * 512
        self.extension = offset
        self.size = manager.octal(self.raw[offset + 124:offset + 136])
        self.payload = offset + 512
        self.follower = self.payload + (self.size + 511) // 512 * 512

    def parsed(self, raw):
        return manager.unpack_archive(gzip.compress(raw, mtime=0), self.root)

    def test_actual_gnu_long_name_shape_and_ustar_prefix_are_accepted(self):
        self.assertEqual(self.size, 102)
        self.assertEqual(self.raw[self.payload:self.payload + self.size], (self.root + "/" + self.relative).encode() + b"\0")
        self.assertEqual(self.parsed(self.raw), {self.relative: (b"Original license text\n", 0o644)})
        output = io.BytesIO()
        with tarfile.open(fileobj=output, mode="w:gz", format=tarfile.USTAR_FORMAT) as stream:
            directories = [self.root, self.root + "/licenses", self.root + "/licenses/npm", self.root + "/licenses/npm/" + self.relative.split("/")[2]]
            for name in directories:
                item = tarfile.TarInfo(name); item.type, item.mode = tarfile.DIRTYPE, 0o755
                stream.addfile(item)
            item = tarfile.TarInfo(self.root + "/" + self.relative)
            item.mode, item.size = 0o644, 1
            stream.addfile(item, io.BytesIO(b"x"))
        self.assertEqual(manager.unpack_archive(output.getvalue(), self.root)[self.relative], (b"x", 0o644))

    def test_long_name_bound_including_long_directories(self):
        prefix = self.root + "/licenses/" + "/".join(["a" * 90] * 9) + "/"
        full = prefix + "z" * (1024 - len(prefix))
        self.assertEqual(len(full), 1024)
        relative = full[len(self.root) + 1:]
        self.assertIn(relative, self.parsed(self.legal_archive(relative)))
        with self.assertRaises(manager.Rejected):
            self.parsed(self.legal_archive(relative + "z"))

    def test_marker_header_metadata_size_and_checksum_mutations_rejected(self):
        for position, replacement in ((0, b"wrong-marker"), (20, b"x"), (100, b"0000755\0"), (108, b"0000001\0"), (116, b"0000001\0"), (124, b"00000000145\0"), (124, b"00000002002\0"), (124, b"\x80"), (136, b"\x80"), (157, b"x"), (257, b"ustar\0"), (263, b"00"), (265, b"x"), (297, b"x"), (329, b"x"), (345, b"x")):
            raw = bytearray(self.raw)
            raw[self.extension + position:self.extension + position + len(replacement)] = replacement
            self.checksum(raw, self.extension)
            with self.subTest(position=position, replacement=replacement), self.assertRaises(manager.Rejected):
                self.parsed(raw)
        raw = bytearray(self.raw); raw[self.extension + 148] ^= 1
        with self.assertRaises(manager.Rejected):
            self.parsed(raw)

    def test_long_name_nul_ascii_and_block_padding_mutations_rejected(self):
        for position, replacement in ((self.size - 1, b"x"), (10, b"\0"), (self.size - 2, b"\0"), (10, b"\xff"), (10, b"\x1f"), (self.size, b"x")):
            raw = bytearray(self.raw)
            raw[self.payload + position:self.payload + position + len(replacement)] = replacement
            with self.subTest(position=position, replacement=replacement), self.assertRaises(manager.Rejected):
                self.parsed(raw)

    def test_pending_name_requires_one_matching_regular_follower(self):
        for kind in (b"L", b"K", b"x", b"g", b"S", b"1", b"2", b"3", b"6"):
            raw = bytearray(self.raw); raw[self.follower + 156:self.follower + 157] = kind
            self.checksum(raw, self.follower)
            with self.subTest(kind=kind), self.assertRaises(manager.Rejected):
                self.parsed(raw)
        for position, replacement in ((0, b"mismatch"), (257, b"ustar\0"), (263, b"00"), (345, b"prefix"), (157, b"link-target")):
            raw = bytearray(self.raw)
            raw[self.follower + position:self.follower + position + len(replacement)] = replacement
            self.checksum(raw, self.follower)
            with self.subTest(position=position), self.assertRaises(manager.Rejected):
                self.parsed(raw)
        for raw in (self.raw[:self.follower], self.raw[:self.follower] + b"\0" * 1024, self.raw[:self.follower] + self.raw[self.extension:self.follower] + self.raw[self.follower:]):
            with self.assertRaises(manager.Rejected):
                self.parsed(raw)

    def test_effective_long_name_paths_and_duplicates_are_checked(self):
        for prefix in (b"/", (self.root + "/../").encode(), b"wrong-root/"):
            replacement = prefix + b"a" * (self.size - 1 - len(prefix))
            self.assertEqual(len(replacement), self.size - 1)
            raw = bytearray(self.raw)
            raw[self.payload:self.payload + self.size] = replacement + b"\0"
            raw[self.follower:self.follower + 100] = replacement[:100]
            self.checksum(raw, self.follower)
            with self.subTest(path=replacement), self.assertRaises(manager.Rejected):
                self.parsed(raw)
        duplicate = tarfile.TarInfo(self.root + "/" + self.relative)
        duplicate.mode, duplicate.size = 0o644, 1
        with self.assertRaises(manager.Rejected):
            self.parsed(self.legal_archive(extras=[(duplicate, b"x")]))


class HandoffTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="aster-update-test-")
        self.base = Path(self.temporary.name)
        self.trust = manager._Trust(os.geteuid(), os.stat("/").st_uid)
        self.state = self.base / "state"
        self.state.mkdir(mode=0o700)
        self.handoff = self.base / "handoff.json"
        self.fixture = Fixture()
        self.save_handoff()

    def tearDown(self):
        self.temporary.cleanup()

    def save_handoff(self):
        self.handoff.write_bytes(encoded(self.fixture.envelope))
        self.handoff.chmod(0o600)

    def run_stage(self, **kwargs):
        return manager.stage(str(self.handoff), str(self.state), "0.4.0", "1.0.0", api=self.fixture, trust=self.trust, now=NOW, **kwargs)

    def backup(self):
        backup = self.base / "database.sql.gz"
        backup.write_bytes(gzip.compress(b"-- synthetic SQL; never restored or executed\n"))
        backup.chmod(0o600)
        metadata = self.base / "schema.json"
        metadata.write_bytes(encoded({"schema": 1, "source_commit": "2" * 40, "core_version": "0.4.0", "database": "aster_fixture", "schema_version": 6, "migration_checksums": {str(x): "3" * 64 for x in range(1, 7)}}))
        metadata.chmod(0o600)
        return {"backup": str(backup), "backup_sha256": manager.digest(backup.read_bytes()), "backup_metadata": str(metadata)}

    def test_independent_smtp_verified_stage_never_changes_core(self):
        core = self.base / "running-core"
        core.write_bytes(b"Existing core byte sequence\n")
        before = manager.digest(core.read_bytes())
        result = self.run_stage()
        self.assertEqual(result["status"], "awaiting_host_operator")
        self.assertEqual(result["history"], ["staged", "verified", "awaiting_host_operator"])
        self.assertFalse(result["installation_changed"])
        self.assertFalse(result["verification"]["independent_publisher_signature"])
        self.assertEqual(before, manager.digest(core.read_bytes()))
        self.assertEqual(result["service_action"], "none")
        saved = manager.status(str(self.state), "a" * 32, self.trust)
        self.assertEqual(saved["status"], "awaiting_host_operator")
        audit = (self.state / "audit.jsonl").read_text().splitlines()
        self.assertEqual([json.loads(x)["status"] for x in audit], result["history"])
        for path in (self.state / ("a" * 32)).iterdir():
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_raw_core_and_smtp_stage_exact_assets_without_execution(self):
        for component in ("core", "smtp"):
            self.fixture = Fixture(component, raw=True)
            self.save_handoff()
            journal = self.run_stage()
            self.assertEqual(journal["distribution_format"], "raw-binaries-v1")
            self.assertFalse(journal["installation_changed"])
            self.assertEqual(journal["status"], "backup_required" if component == "core" else "awaiting_host_operator")
            self.assertEqual(len(journal["assets"]), 6 if component == "core" else 5)
            manager.status(str(self.state), "a" * 32, self.trust)
            shutil.rmtree(self.state / ("a" * 32))

    def test_0510_stage_still_requires_current_main_ancestry(self):
        self.fixture = Fixture("core", raw=True, core_version="0.5.10")
        self.save_handoff()
        journal = self.run_stage()
        self.assertEqual(journal["status"], "backup_required")
        self.assertTrue(journal["verification"]["source_and_integrity_verified"])
        self.assertFalse(journal["installation_changed"])
        shutil.rmtree(self.state / ("a" * 32))
        self.fixture.responses["/compare/" + SOURCE + "...main"] = {
            "status": "diverged", "merge_base_commit": {"sha": "4" * 40}}
        with self.assertRaisesRegex(manager.Rejected, "retained on main"):
            self.run_stage()
        rejected = manager.status(str(self.state), "a" * 32, self.trust)
        self.assertEqual(rejected["status"], "rejected")
        self.assertFalse(rejected["installation_changed"])

    def test_schema2_format_asset_mixing_and_raw_tampering_rejected(self):
        for component in ("core", "smtp"):
            fixture = Fixture(component, raw=True)
            valid = encoded(fixture.envelope)
            manager.validate_handoff(valid, "0.4.0", "1.0.0", NOW)
            for field, bad in (("distribution_format", "unknown"), ("distribution_format", "archive-v1"), ("distribution_format", []), ("distribution_format", {}), ("distribution_format", True), ("schema", 1)):
                altered = copy.deepcopy(fixture.envelope); altered["intent"][field] = bad
                with self.subTest(component=component, field=field), self.assertRaises(manager.Rejected):
                    manager.validate_handoff(encoded(altered), "0.4.0", "1.0.0", NOW)
            name = next(iter(manager.binary_assets(component, fixture.version).values()))
            fixture.files[name] = b"modified executable"
            with self.assertRaises(manager.Rejected):
                manager.verify_downloads(fixture, fixture.intent, fixture.files, "0.4.0")

    def test_raw_core_omitted_tagged_migration_rejected_without_staging_assets(self):
        self.fixture = Fixture("core", raw=True)
        # The official source tree still contains 001 and 007; refreshed asset
        # hashes and BUILD copies deliberately omit only the initial migration.
        self.fixture.bundle.pop("migrations/001_core.sql")
        self.fixture.build["source_files"].pop("migrations/001_core.sql")
        self.fixture.refresh()
        self.save_handoff()
        with self.assertRaisesRegex(manager.Rejected, "complete tagged migration inventory"):
            self.run_stage()
        request = self.state / ("a" * 32)
        self.assertEqual({path.name for path in request.iterdir()}, {"JOURNAL.json"})
        journal = manager.status(str(self.state), "a" * 32, self.trust)
        self.assertEqual(journal["status"], "rejected")
        self.assertEqual(journal["history"], ["staged", "rejected"])
        self.assertFalse(journal["installation_changed"])

    def test_gnu_long_name_does_not_hide_resource_elf(self):
        self.fixture = Fixture("core", raw=True)
        self.fixture.bundle[LongNameArchiveTest.relative] = (elf(), 0o644)
        self.fixture.refresh()
        name = manager.archive_name("core", self.fixture.version, self.fixture.format)
        raw, offset = bytearray(gzip.decompress(self.fixture.files[name])), 0
        while offset + 512 <= len(raw) and any(raw[offset:offset + 512]):
            size = manager.octal(raw[offset + 124:offset + 136])
            if raw[offset + 156:offset + 157] == b"L":
                raw[offset + 100:offset + 108] = b"0000644\0"
                LongNameArchiveTest.checksum(raw, offset)
            offset += 512 + (size + 511) // 512 * 512
        self.fixture.files[name] = gzip.compress(raw, mtime=0)
        self.fixture.provenance["assets"][name] = {"size": len(self.fixture.files[name]), "sha256": manager.digest(self.fixture.files[name])}
        self.fixture.reseal()
        self.save_handoff()
        with self.assertRaisesRegex(manager.Rejected, "Resource archives cannot contain ELF"):
            self.run_stage()
        self.assertEqual(manager.status(str(self.state), "a" * 32, self.trust)["status"], "rejected")

    def test_core_without_backup_does_not_claim_ready_or_apply(self):
        self.fixture = Fixture("core")
        self.save_handoff()
        result = self.run_stage()
        self.assertEqual(result["status"], "backup_required")
        self.assertIn("schema_and_migration_checksums", result["required_operator_checks"])
        self.assertIn("isolated_restore_and_business_verification", result["required_operator_checks"])

    def test_core_backup_integrity_is_not_restore_or_schema_proof(self):
        self.fixture = Fixture("core")
        self.save_handoff()
        result = self.run_stage(**self.backup())
        self.assertEqual(result["status"], "compatibility_review_required")
        self.assertFalse(result["installed_plugin_compatibility_verified"])
        self.assertTrue(result["backup"]["gzip_integrity_verified"])
        self.assertFalse(result["backup"]["isolated_restore_verified"])
        self.assertFalse(result["backup"]["database_contents_verified"])

    def test_backup_missing_args_wrong_hash_corrupt_or_concatenated_gzip(self):
        self.fixture = Fixture("core")
        self.save_handoff()
        arguments = self.backup()
        for changed in ({"backup": arguments["backup"]}, dict(arguments, backup_sha256="0" * 64)):
            with self.subTest(changed=sorted(changed)), self.assertRaises(manager.Rejected):
                manager.backup_evidence(changed.get("backup"), changed.get("backup_sha256"), changed.get("backup_metadata"), "0.4.0", self.trust)
        original = Path(arguments["backup"]).read_bytes()
        for malformed in (b"not gzip", original[:-8], original + original):
            Path(arguments["backup"]).write_bytes(malformed)
            with self.subTest(size=len(malformed)), self.assertRaises(manager.Rejected):
                manager.backup_evidence(arguments["backup"], manager.digest(malformed), arguments["backup_metadata"], "0.4.0", self.trust)

    def test_backup_schema_incomplete_and_current_version_mismatch(self):
        args = self.backup()
        original = json.loads(Path(args["backup_metadata"]).read_bytes())
        for changes in ({"core_version": "0.4.1"}, {"schema_version": 7}, {"database": "mysql"}, {"migration_checksums": {"1": "g" * 64}}):
            Path(args["backup_metadata"]).write_bytes(encoded(dict(original, **changes)))
            with self.subTest(changes=changes), self.assertRaises(manager.Rejected):
                manager.backup_evidence(args["backup"], args["backup_sha256"], args["backup_metadata"], "0.4.0", self.trust)

    def test_backup_expansion_bound(self):
        args = self.backup()
        with patch.object(manager, "MAX_BACKUP_EXPANDED", 10), self.assertRaises(manager.Rejected):
            manager.backup_evidence(args["backup"], args["backup_sha256"], args["backup_metadata"], "0.4.0", self.trust)

    def test_handoff_expiry_duplicates_signature_alias_unknown_and_identity(self):
        baseline = encoded(self.fixture.envelope)
        malformed = [baseline.replace(b'"schema": 1', b'"schema": true', 1), baseline[:-2] + b',"schema":1}\n', baseline.replace(b'"intent"', b'"Intent"'), encoded(dict(self.fixture.envelope, signature="0" * 64))]
        for raw in malformed:
            with self.subTest(raw=raw[:30]), self.assertRaises(manager.Rejected):
                manager.validate_handoff(raw, "0.4.0", "1.0.0", NOW)
        for key, value in (("repository", "evil/project"), ("repository_id", True), ("actor_id", 123), ("release_id", 0), ("tag", "v1.0.1"), ("version", "1.0.1-rc.1"), ("current_core_version", "0.4.1"), ("created_at", "2026-10-14T17:01:00Z"), ("expires_at", "2026-10-14T18:00:00Z")):
            envelope = copy.deepcopy(self.fixture.envelope)
            envelope["intent"][key] = value
            with self.subTest(key=key), self.assertRaises(manager.Rejected):
                manager.validate_handoff(encoded(envelope), "0.4.0", "1.0.0", NOW)
        with self.assertRaises(manager.Rejected):
            manager.validate_handoff(baseline, "0.4.0", "1.0.0", NOW + 1800)

    def test_empty_plugin_version_and_policy_revision_zero_supported(self):
        value = copy.deepcopy(self.fixture.envelope)
        value["intent"]["current_plugin_version"] = ""
        value["intent"]["plugin_policy_revision"] = 0
        result = manager.validate_handoff(encoded(value), "0.4.0", "", NOW)
        self.assertEqual(result["current_plugin_version"], "")

    def test_no_downgrade_or_same_version_update(self):
        for target in ("1.0.0", "0.9.9"):
            value = copy.deepcopy(self.fixture.envelope)
            value["intent"]["version"] = target
            value["intent"]["tag"] = "smtp-v" + target
            with self.subTest(target=target), self.assertRaises(manager.Rejected):
                manager.validate_handoff(encoded(value), "0.4.0", "1.0.0", NOW)

    def test_every_handoff_field_rejects_unexpected_container_types(self):
        for field in sorted(manager.INTENT_KEYS - {"assets"}):
            for value in ({}, []):
                envelope = copy.deepcopy(self.fixture.envelope)
                envelope["intent"][field] = value
                with self.subTest(field=field, value=value), self.assertRaises(manager.Rejected):
                    manager.validate_handoff(encoded(envelope), "0.4.0", "1.0.0", NOW)

    def test_exact_asset_inventory_ids_and_hashes(self):
        for changes in (lambda v: v.append(dict(v[0])), lambda v: v[0].update(id=v[1]["id"]), lambda v: v[0].update(sha256="A" * 64), lambda v: v[0].update(name="source.tar.gz"), lambda v: v[0].update(size=manager.MAX_ARCHIVE + 1)):
            assets = copy.deepcopy(self.fixture.assets)
            changes(assets)
            with self.assertRaises(manager.Rejected):
                manager.asset_inventory(assets, "smtp", "1.0.1")

    def test_official_release_tag_digest_prerelease_fork_and_failed_run(self):
        for path, key, value in (("/releases/tags/" + self.fixture.tag, "draft", True), ("/releases/tags/" + self.fixture.tag, "prerelease", True), ("/releases/tags/" + self.fixture.tag, "id", 1), ("/actions/runs/1234", "conclusion", "failure"), ("/actions/runs/1234", "run_attempt", 2), ("/actions/runs/1234", "head_repository", {"id": 1, "full_name": manager.REPOSITORY})):
            original = self.fixture.responses[path][key]
            self.fixture.responses[path][key] = value
            with self.subTest(key=key), self.assertRaises(manager.Rejected):
                manager.release_assets(self.fixture, self.fixture.intent)
                manager.verify_downloads(self.fixture, self.fixture.intent, self.fixture.files, "0.4.0")
            self.fixture.responses[path][key] = original
        self.fixture.responses["/git/ref/tags/" + self.fixture.tag]["object"]["sha"] = "f" * 40
        with self.assertRaises(manager.Rejected):
            manager.release_assets(self.fixture, self.fixture.intent)

    def test_download_hash_mismatch_leaves_rejected_journal_and_no_program(self):
        self.fixture.files[manager.archive_name("smtp", "1.0.1")] += b"tampered"
        with self.assertRaises(manager.Rejected):
            self.run_stage()
        result = manager.status(str(self.state), "a" * 32, self.trust)
        self.assertEqual(result["status"], "rejected")
        self.assertFalse(result["installation_changed"])
        self.assertEqual([p.name for p in (self.state / ("a" * 32)).iterdir()], ["JOURNAL.json"])

    def test_source_copy_and_source_version_mismatches_rejected(self):
        self.fixture.bundle["README.md"] = (b"Not from tagged source\n", 0o644)
        self.fixture.refresh()
        self.save_handoff()
        with self.assertRaisesRegex(manager.Rejected, "tagged source"):
            self.run_stage()

    def test_source_version_uses_one_core_declaration_and_ignores_payment_minimum_comments(self):
        protocol = "vps-billing/backend/pkg/pluginapi/protocol.go"
        accepted = ('package pluginapi\nconst (\n PaymentMinCoreVersion = "9.9.9"\n CoreVersion = "0.4.1"\n)\n',
                    'package pluginapi\n// CoreVersion = "9.9.9"\nconst CoreVersion = "0.4.1"\n')
        rejected = ('package pluginapi\nconst PaymentMinCoreVersion = "0.4.1"\n',
                    'package pluginapi\n// CoreVersion = "0.4.1"\n',
                    'package pluginapi\nconst CoreVersion = "0.4.1"\nconst CoreVersion = "0.4.1"\n')
        for source in accepted + rejected:
            with self.subTest(source=source):
                fixture = Fixture()
                fixture.source[protocol] = source.encode()
                fixture.build["input_sha256"][protocol] = manager.digest(source.encode())
                fixture.refresh()
                if source in accepted:
                    manager.verify_source(fixture, fixture.build, fixture.bundle, SOURCE, fixture.provenance)
                else:
                    with self.assertRaisesRegex(manager.Rejected, "component version"):
                        manager.verify_source(fixture, fixture.build, fixture.bundle, SOURCE, fixture.provenance)

    def test_claimed_policy_blobs_bind_exact_tag_default_and_workflow_trees(self):
        self.fixture.provenance["trusted_blobs"][manager.WORKFLOW] = "f" * 40
        self.fixture.reseal()
        with self.assertRaisesRegex(manager.Rejected, "tagged source"):
            manager.verify_downloads(self.fixture, self.fixture.intent, self.fixture.files, "0.4.0")
        self.fixture = Fixture()
        default = "4" * 40
        self.fixture.provenance["default_sha"] = default
        self.fixture.responses["/git/trees/" + default + "?recursive=1"] = copy.deepcopy(self.fixture.responses["/git/trees/" + SOURCE + "?recursive=1"])
        self.fixture.responses["/compare/" + default + "...main"] = {"status": "ahead", "merge_base_commit": {"sha": default}}
        self.fixture.reseal()
        self.assertTrue(manager.verify_downloads(self.fixture, self.fixture.intent, self.fixture.files, "0.4.0")["source_and_integrity_verified"])
        tree = self.fixture.responses["/git/trees/" + default + "?recursive=1"]["tree"]
        next(item for item in tree if item["path"] == manager.WORKFLOW)["sha"] = "f" * 40
        with self.assertRaisesRegex(manager.Rejected, "policy differ"):
            manager.verify_downloads(self.fixture, self.fixture.intent, self.fixture.files, "0.4.0")

    def test_core_critical_source_mappings_cannot_be_redirected(self):
        self.fixture = Fixture("core")
        destination = "deploy/scripts/update-manager.py"
        source = "vps-billing/backend/untrusted.py"
        self.fixture.build["source_files"][destination] = source
        self.fixture.source[source] = self.fixture.bundle[destination][0]
        self.fixture.refresh()
        self.save_handoff()
        with self.assertRaisesRegex(manager.Rejected, "source copy paths"):
            self.run_stage()

    def test_unknown_official_metadata_types_are_rejected_and_journaled(self):
        for path in ("/git/ref/tags/" + self.fixture.tag, "/releases/tags/" + self.fixture.tag, "/actions/runs/1234"):
            self.fixture = Fixture()
            self.fixture.intent["request_id"] = manager.secrets.token_hex(16)
            self.fixture.responses[path] = []
            self.save_handoff()
            with self.subTest(path=path), self.assertRaises(manager.Rejected):
                self.run_stage()
            checkpoint = manager.status(str(self.state), self.fixture.intent["request_id"], self.trust)
            self.assertEqual(checkpoint["status"], "rejected")

    def test_elf_dynamic_loader_and_wrong_architecture_rejected(self):
        for offset, format, value in ((18, "<H", 183), (64, "<I", 3), (64, "<I", 2), (24, "<Q", 0)):
            binary = bytearray(elf())
            struct.pack_into(format, binary, offset, value)
            with self.subTest(offset=offset, value=value), self.assertRaises(manager.Rejected):
                manager.static_elf(binary)

    def test_tar_links_extensions_traversal_modes_duplicates_and_bombs(self):
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.CHRTYPE, tarfile.FIFOTYPE, tarfile.XHDTYPE):
            def edit(member):
                if member.name.endswith("/plugin"):
                    member.type, member.linkname = kind, "/tmp/unknown"
            raw = archive(self.fixture.bundle, "smtp", "1.0.1", edit)
            with self.subTest(kind=kind), self.assertRaises(manager.Rejected):
                manager.unpack_archive(raw, "aster-smtp-1.0.1-linux-amd64")
        for path in ("../escape", "plugin", "unknown"):
            item = tarfile.TarInfo("aster-smtp-1.0.1-linux-amd64/" + path)
            item.mode, item.size = 0o644, 1
            raw = archive(self.fixture.bundle, "smtp", "1.0.1", extras=[(item, b"x")])
            if path == "unknown":
                parsed = manager.unpack_archive(raw, "aster-smtp-1.0.1-linux-amd64")
                self.assertIn("unknown", parsed)
            else:
                with self.subTest(path=path), self.assertRaises(manager.Rejected):
                    manager.unpack_archive(raw, "aster-smtp-1.0.1-linux-amd64")
        raw = archive(self.fixture.bundle, "smtp", "1.0.1")
        for bad in (raw + raw, raw[:-8], raw + b"suffix"):
            with self.assertRaises(manager.Rejected):
                manager.unpack_archive(bad, "aster-smtp-1.0.1-linux-amd64")
        with patch.object(manager, "MAX_EXPANDED", 100), self.assertRaises(manager.Rejected):
            manager.unpack_archive(raw, "aster-smtp-1.0.1-linux-amd64")

    def test_unknown_smtp_archive_member_rejected_even_with_valid_outer_hash(self):
        self.fixture.bundle["unknown"] = (b"Never execute this\n", 0o644)
        self.fixture.refresh()
        self.save_handoff()
        with self.assertRaises(manager.Rejected):
            self.run_stage()

    def test_plugin_current_core_compatibility_rejected(self):
        self.fixture.intent["current_core_version"] = "0.5.0"
        self.save_handoff()
        with self.assertRaises(manager.Rejected):
            manager.stage(str(self.handoff), str(self.state), "0.5.0", "1.0.0", api=self.fixture, trust=self.trust, now=NOW)

    def test_symlink_hardlink_world_write_private_modes_and_fifo_rejected(self):
        linked = self.base / "linked.json"
        linked.symlink_to(self.handoff)
        with self.assertRaises(manager.Rejected):
            manager.read_protected(str(linked), 32000, self.trust, True)
        linked.unlink()
        os.link(self.handoff, linked)
        with self.assertRaises(manager.Rejected):
            manager.read_protected(str(self.handoff), 32000, self.trust, True)
        linked.unlink()
        for mode in (0o644, 0o666):
            self.handoff.chmod(mode)
            with self.subTest(mode=mode), self.assertRaises(manager.Rejected):
                self.run_stage()
        self.handoff.chmod(0o600)
        self.state.chmod(0o755)
        with self.assertRaises(manager.Rejected):
            self.run_stage()
        fifo = self.base / "fifo"
        os.mkfifo(fifo, 0o600)
        with self.assertRaises(manager.Rejected):
            manager.read_protected(str(fifo), 32000, self.trust, True)

    def test_parent_symlink_and_group_writable_ancestors_rejected(self):
        link = self.base / "alias"
        link.symlink_to(self.state, target_is_directory=True)
        with self.assertRaises(manager.Rejected):
            manager.protected_open(str(link), True, self.trust, True)
        self.base.chmod(0o770)
        with self.assertRaises(manager.Rejected):
            self.run_stage()

    def test_lock_conflict_and_hostile_lock_file(self):
        root = manager.protected_open(str(self.state), True, self.trust, True)
        try:
            with manager.locked(root, self.trust):
                with self.assertRaisesRegex(manager.Rejected, "exclusive lock"):
                    self.run_stage()
        finally:
            os.close(root)
        lock = self.state / ".update.lock"
        lock.unlink()
        lock.symlink_to(self.handoff)
        with self.assertRaises((manager.Rejected, OSError)):
            self.run_stage()

    def test_replay_refused_and_tampered_staged_assets_detected(self):
        self.run_stage()
        with self.assertRaisesRegex(manager.Rejected, "already has"):
            self.run_stage()
        asset = self.state / ("a" * 32) / "BUILD.json"
        asset.write_bytes(b"{}")
        with self.assertRaisesRegex(manager.Rejected, "changed after"):
            manager.status(str(self.state), "a" * 32, self.trust)

    def test_interrupted_verification_has_no_success_claim_and_no_retry(self):
        with patch.object(self.fixture, "asset", side_effect=KeyboardInterrupt), self.assertRaises(KeyboardInterrupt):
            self.run_stage()
        result = manager.status(str(self.state), "a" * 32, self.trust)
        self.assertEqual(result["status"], "staged")
        self.assertFalse(result["installation_changed"])
        with self.assertRaises(manager.Rejected):
            self.run_stage()

    def test_unsupported_cli_apply_url_shell_and_nonroot_rejected(self):
        for arguments in (["apply"], ["stage", "--url", "https://evil.test"], ["stage", "--backup-command", "touch /tmp/unknown"]):
            result = subprocess.run([sys.executable, str(SCRIPT), *arguments], capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
        with patch.object(manager.os, "geteuid", return_value=2000):
            self.assertEqual(manager.main(["status", "--request-id", "a" * 32]), 1)

    def test_actual_loopback_http_stage_and_credential_stripped_cdn_redirect(self):
        fixture, observed = self.fixture, []
        prefix = "/repos/" + manager.REPOSITORY
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                observed.append((self.path, self.headers.get("Authorization", "")))
                if self.path.startswith(prefix + "/releases/assets/"):
                    asset_id = int(self.path.rsplit("/", 1)[1])
                    self.send_response(302)
                    self.send_header("Location", "https://release-assets.githubusercontent.com/fixture/" + str(asset_id) + "?sig=never-logged")
                    self.end_headers()
                    return
                if self.path.startswith("/fixture/"):
                    asset_id = int(self.path.split("?")[0].rsplit("/", 1)[1])
                    raw = fixture.asset(asset_id, manager.MAX_ARCHIVE)
                elif self.path == prefix:
                    raw = encoded(fixture.repository())
                else:
                    raw = encoded(fixture.json(self.path[len(prefix):]))
                self.send_response(200)
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)
            def log_message(self, *_):
                pass
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        class FixtureOpener:
            def __init__(self):
                self.inner = urllib.request.build_opener(urllib.request.ProxyHandler({}), manager.NoRedirect())
            def open(self, request, timeout):
                parsed = urllib.parse.urlsplit(request.full_url)
                local = "http://127.0.0.1:" + str(server.server_port) + parsed.path + ("?" + parsed.query if parsed.query else "")
                return self.inner.open(urllib.request.Request(local, headers=dict(request.header_items())), timeout=timeout)
        synthetic_token = "ghs_42_" + "a-_." * 130
        official = manager.Official(synthetic_token)
        official.opener = FixtureOpener()
        try:
            result = manager.stage(str(self.handoff), str(self.state), "0.4.0", "1.0.0", api=official, trust=self.trust, now=NOW)
            self.assertEqual(result["status"], "awaiting_host_operator")
            self.assertEqual(len([x for x in observed if x[0].startswith("/fixture/")]), 4)
            self.assertTrue(all(not authorization for path, authorization in observed if path.startswith("/fixture/")))
            self.assertTrue(all(authorization == "Bearer " + synthetic_token for path, authorization in observed if path.startswith(prefix)))
        finally:
            server.shutdown()
            thread.join()
            server.server_close()

    def test_fixed_https_hosts_actions_and_no_environment_proxies(self):
        official = manager.Official("fixture_token")
        with patch.object(official, "_get", return_value=b"{}") as fetch:
            self.assertEqual(official.json("/releases/latest"), {})
            fetch.assert_called_once_with("https://api.github.com/repos/" + manager.REPOSITORY + "/releases/latest", manager.MAX_JSON)
        for url, authenticated in (("http://api.github.com/repos/test", True), ("https://evil.test/asset", False), ("https://release-assets.githubusercontent.com:444/asset", False), ("https://release-assets.githubusercontent.com:invalid/asset", False), ("https://user@release-assets.githubusercontent.com/asset", False), ("https://release-assets.githubusercontent.com/asset#fragment", False), ("https://objects.githubusercontent.com/asset", False)):
            with self.subTest(url=url), self.assertRaises(manager.Rejected):
                official._get(url, 100, asset=True, authenticated=authenticated)
        for action in ("/contributors", "/releases", "/releases/latest?url=https://evil.test", "/releases/latest/extra", "/git/ref/tags/smtp-v1.0.1?url=https://evil.test", "/../../other"):
            with self.assertRaises(manager.Rejected):
                official.json(action)
        self.assertFalse(any(isinstance(handler, urllib.request.ProxyHandler) and handler.proxies for handler in official.opener.handlers))

    def test_only_one_asset_redirect_and_provider_errors_are_redacted(self):
        official = manager.Official("private_read_token")
        calls = []
        class RepeatedRedirect:
            def open(self, request, timeout):
                calls.append(request)
                headers = Message()
                headers["Location"] = "https://release-assets.githubusercontent.com/again?credential=secret"
                raise urllib.error.HTTPError(request.full_url, 302, "secret provider text", headers, io.BytesIO(b"private provider response"))
        official.opener = RepeatedRedirect()
        with self.assertRaises(manager.Rejected) as caught:
            official.asset(100, 1000)
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[0].get_header("Authorization"), "Bearer private_read_token")
        self.assertIsNone(calls[1].get_header("Authorization"))
        self.assertNotIn("secret", str(caught.exception))
        self.assertNotIn("private_read_token", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
