#!/usr/bin/env python3
"""Offline hostile-input and policy-transition tests; no plugin is executed."""
from concurrent.futures import ThreadPoolExecutor
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import stat
import struct
import subprocess
import sys
import tarfile
import tempfile
import threading
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("plugin-manager.py")
SPEC = importlib.util.spec_from_file_location("aster_plugin_manager", SCRIPT)
manager = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = manager
SPEC.loader.exec_module(manager)


def elf():
    raw = bytearray(256)
    raw[:7] = b"\x7fELF\x02\x01\x01"
    struct.pack_into("<HHIQQQIHHHHHH", raw, 16, 2, 62, 1, 0x400080, 64, 0, 0, 64, 56, 1, 0, 0, 0)
    struct.pack_into("<IIQQQQQQ", raw, 64, 1, 5, 0, 0x400000, 0x400000, 256, 256, 4096)
    return bytes(raw)


def contents(version="1.0.0"):
    binary = elf()
    manifest = {"schema": 1, "id": "email.smtp", "version": version, "protocol": "aster.smtp/1", "core_min": "0.4.0", "core_max_exclusive": "0.6.0", "capabilities": ["smtp.send"], "executable": "plugin", "sha256": hashlib.sha256(binary).hexdigest()}
    return {"plugin": binary, "manifest.json": json.dumps(manifest).encode(), "LICENSE": b"Preserved project license\n", "README.md": b"Independent SMTP plugin\n", "THIRD_PARTY_NOTICES.json": b'{"go_runtime":{"license":"Preserved runtime license"}}'}


def payment_contents(version="1.0.0", identifier="payment.contract-example"):
    files = contents(version)
    manifest = json.loads(files["manifest.json"])
    manifest.update(id=identifier, protocol="aster.payment/1", core_min="0.5.3", capabilities=["payment.checkout", "payment.verify_callback"],
                    payment={"currencies": [{"code": "CNY", "precision": 2}, {"code": "JPY", "precision": 0}],
                             "checkout_hosts": ["checkout.example.test"]})
    files["manifest.json"] = json.dumps(manifest).encode()
    files["README.md"] = b"Synthetic payment contract; no real payment service.\n"
    return files


def archive(files=None, version="1.0.0", edits=None, extras=(), format=tarfile.GNU_FORMAT, root=None):
    files = contents(version) if files is None else dict(files)
    if "SHA256SUMS" not in files:
        files["SHA256SUMS"] = "".join(hashlib.sha256(value).hexdigest() + "  " + name + "\n" for name, value in sorted(files.items())).encode()
    root = root or "aster-smtp-" + version + "-linux-amd64"
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w:gz", format=format) as stream:
        directory = tarfile.TarInfo(root)
        directory.type = tarfile.DIRTYPE
        directory.mode = 0o755
        if edits:
            edits(directory)
        stream.addfile(directory)
        for name, value in files.items():
            member = tarfile.TarInfo(root + "/" + name)
            member.mode = 0o755 if name == "plugin" else 0o644
            member.size = len(value)
            if edits:
                edits(member)
            stream.addfile(member, io.BytesIO(value) if member.isreg() else None)
        for member, value in extras:
            stream.addfile(member, io.BytesIO(value) if member.isreg() else None)
    return raw.getvalue()


class ManagerTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="aster-manager-test-")
        self.base = Path(self.temporary.name)
        self.trust = manager._Trust(os.geteuid(), os.stat("/").st_uid)
        self.root = self.base / "plugins"
        self.config = self.base / "config"
        self.config.mkdir(mode=0o755)
        self.policy = self.config / "plugins.json"

    def tearDown(self):
        for base, directories, _ in os.walk(self.base):
            os.chmod(base, 0o700)
            for directory in directories:
                target = Path(base) / directory
                if not target.is_symlink():
                    os.chmod(target, 0o700)
        self.temporary.cleanup()

    def package(self, raw=None, **kwargs):
        raw = archive(**kwargs) if raw is None else raw
        path = self.base / ("package-" + str(len(list(self.base.glob("package-*")))) + ".tar.gz")
        path.write_bytes(raw)
        os.chmod(path, 0o644)
        return path, hashlib.sha256(raw).hexdigest()

    def verify(self, raw=None, **kwargs):
        path, digest = self.package(raw, **kwargs)
        return manager.verify_package(str(path), digest, trust=self.trust)

    def install(self, version="1.0.0"):
        package = self.verify(version=version)
        manager.install_package(str(self.root), package, self.trust)
        return package

    def selection(self, version="1.0.0", targets=None):
        return {"id": "email.smtp", "version": version, "worker_uid": 2001, "client_uid": 2002, "socket": "/run/aster-smtp/plugin.sock", "allowed_targets": [{"host": "localhost", "port": 2525}] if targets is None else targets}

    def mutate(self, action, revision, **kwargs):
        return manager.mutate_policy(str(self.root), str(self.policy), action, revision, trust=self.trust, **kwargs)

    def install_payment(self, version="1.0.0"):
        package = self.verify(files=payment_contents(version), version=version,
                              root="aster-payment-contract-example-" + version + "-linux-amd64")
        manager.install_package(str(self.root), package, self.trust)
        return package

    def payment_selection(self, version="1.0.0"):
        result = self.selection(version)
        result.update(id="payment.contract-example", worker_uid=2003, socket="/run/aster-payment/plugin.sock",
                      merchant_id="synthetic-merchant", allowed_targets=[{"host": "api.example.test", "port": 443}])
        return result

    def test_payment_install_approval_enable_disable_and_rollback_preserve_smtp(self):
        self.install()
        self.mutate("select", 0, selection=self.selection())
        smtp = self.mutate("enable", 1)["smtp"]
        self.install_payment()
        self.install_payment("1.0.1")
        first = self.mutate("select", 2, kind="payment", selection=self.payment_selection("1.0.1"))
        self.assertFalse(first["payment"]["enabled"])
        self.assertEqual(first["payment"]["merchant_id"], "synthetic-merchant")
        self.assertEqual(first["smtp"], smtp)
        second = self.mutate("enable", 3, kind="payment")
        self.assertTrue(second["payment"]["enabled"])
        rollback = self.mutate("rollback", 4, kind="payment", version="1.0.0")
        self.assertEqual(rollback["payment"]["version"], "1.0.0")
        self.assertTrue(rollback["payment"]["enabled"])
        self.assertEqual(rollback["smtp"], smtp)
        final = self.mutate("disable", 5, kind="payment")
        self.assertFalse(final["payment"]["enabled"])
        self.assertTrue(final["smtp"]["enabled"])

    def test_payment_package_identity_and_declared_capabilities_are_bound(self):
        for field, value in (("id", "payment.disabled"), ("id", "payment.Upper"),
                             ("protocol", "aster.payment/2"), ("capabilities", ["payment.checkout"]),
                             ("payment", {"currencies": [], "checkout_hosts": ["checkout.example.test"]})):
            files = payment_contents()
            manifest = json.loads(files["manifest.json"])
            manifest[field] = value
            files["manifest.json"] = json.dumps(manifest).encode()
            with self.subTest(field=field, value=value), self.assertRaises(manager.Rejected):
                self.verify(files=files, root="aster-payment-contract-example-1.0.0-linux-amd64")
        with self.assertRaises(manager.Rejected):
            self.verify(files=payment_contents())
        with self.assertRaises(manager.Rejected):
            manager.validate_manifest(json.loads(payment_contents(identifier="payment.disabled")["manifest.json"]), manager.CORE_VERSION)

    def test_default_core_compatibility_matches_compiled_runtime_protocol(self):
        protocol = SCRIPT.parents[2] / "backend/pkg/pluginapi/protocol.go"
        declared = re.search(r'\bCoreVersion\s*=\s*"([^"]+)"', protocol.read_text())
        self.assertIsNotNone(declared)
        self.assertEqual(declared[1], manager.CORE_VERSION)

    def test_current_release_version_and_existing_smtp_compatibility_remain_independent(self):
        self.assertEqual(manager.CORE_VERSION, "0.5.10")
        self.assertEqual(manager.RESOURCE_MIN_CORE_VERSION, "0.5.10")
        self.assertEqual(manager.PAYMENT_MIN_CORE_VERSION, "0.5.3")
        version = SCRIPT.parents[2] / "backend/plugins/smtp/VERSION"
        self.assertEqual(version.read_text().strip(), "1.1.0")
        manifest = json.loads(contents(version="1.1.0")["manifest.json"])
        manager.validate_manifest(manifest, manager.CORE_VERSION)
        self.assertEqual((manifest["core_min"], manifest["core_max_exclusive"]), ("0.4.0", "0.6.0"))
        with self.assertRaises(manager.Rejected):
            manager.validate_manifest(manifest, "0.6.0")
        manifest["core_max_exclusive"] = "0.5.7"
        with self.assertRaises(manager.Rejected):
            manager.validate_manifest(manifest, manager.CORE_VERSION)

    def test_resource_first_release_rejects_old_core_and_old_manifest_minimum(self):
        manifest = json.loads(contents(version="0.1.1")["manifest.json"])
        manifest.update(id="resource.aster-cloud", protocol="aster.resource/1", core_min="0.5.10",
                        capabilities=["resource.send", "resource.lookup"])
        manager.validate_manifest(manifest, "0.5.10")
        for core in ("0.5.8", "0.5.9", "0.6.0"):
            with self.subTest(core=core), self.assertRaises(manager.Rejected):
                manager.validate_manifest(manifest, core)
        for minimum in ("0.4.0", "0.5.9"):
            with self.subTest(minimum=minimum), self.assertRaises(manager.Rejected):
                manager.validate_manifest(manifest | {"core_min": minimum}, "0.5.10")

    def test_payment_protocol_rejects_old_core_override_and_inaccurate_minimum(self):
        manifest = json.loads(payment_contents()["manifest.json"])
        with self.assertRaises(manager.Rejected):
            manager.validate_manifest(manifest, "0.5.2")
        manifest["core_min"] = "0.4.0"
        with self.assertRaises(manager.Rejected):
            manager.validate_manifest(manifest, "0.5.3")

    def test_payment_raw_resources_keep_binary_and_resource_hash_approvals_separate(self):
        files = payment_contents()
        files["SHA256SUMS"] = "".join(hashlib.sha256(data).hexdigest() + "  " + name + "\n"
                                       for name, data in sorted(files.items())).encode()
        raw_binary = files.pop("plugin")
        raw = archive(files=files, root="aster-payment-contract-example-1.0.0-linux-amd64-resources")
        path, digest = self.package(raw)
        binary = self.base / "raw-payment"
        binary.write_bytes(raw_binary)
        binary.chmod(0o755)
        package = manager.verify_package(str(path), digest, trust=self.trust, binary=str(binary),
                                         binary_sha256=hashlib.sha256(raw_binary).hexdigest())
        manager.install_package(str(self.root), package, self.trust)
        self.assertEqual((self.root / "payment.contract-example/1.0.0/plugin").read_bytes(), raw_binary)
        self.assertFalse(self.policy.exists())
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(path), digest, trust=self.trust, binary=str(binary), binary_sha256="0" * 64)

    def test_payment_declarations_reject_ambiguous_currency_hosts_and_extra_fields(self):
        baseline = json.loads(payment_contents()["manifest.json"])["payment"]
        for field, value in (("currencies", [{"code": "CNY", "precision": 2}] * 2),
                             ("currencies", [{"code": "cny", "precision": 2}]),
                             ("currencies", [{"code": "CNY", "precision": True}]),
                             ("currencies", [{"code": "CNY", "precision": 7}]),
                             ("checkout_hosts", ["127.0.0.1"]), ("checkout_hosts", ["*.example.test"]),
                             ("checkout_hosts", ["Checkout.example.test"]), ("checkout_hosts", ["checkout.example.test:443"]),
                             ("checkout_hosts", ["checkout.example.test"] * 2), ("secret", "not-permitted")):
            value = dict(baseline, **{field: value})
            with self.subTest(declarations=value), self.assertRaises(manager.Rejected):
                manager.validate_payment(value)

    def test_payment_selection_requires_merchant_and_separate_workers_and_socket(self):
        self.install()
        self.mutate("select", 0, selection=self.selection())
        self.install_payment()
        for field, value in (("merchant_id", ""), ("merchant_id", "merchant with space"), ("merchant_id", "x" * 129),
                             ("worker_uid", 2001), ("worker_uid", 2002), ("client_uid", 2001),
                             ("socket", "/run/aster-smtp/plugin.sock")):
            selection = self.payment_selection()
            selection[field] = value
            with self.subTest(field=field, value=value), self.assertRaises(manager.Rejected):
                self.mutate("select", 1, kind="payment", selection=selection)
            self.assertEqual(manager.read_policy(str(self.policy), self.trust)["revision"], 1)
        missing = self.payment_selection()
        del missing["merchant_id"]
        with self.assertRaises(manager.Rejected):
            self.mutate("select", 1, kind="payment", selection=missing)
        with self.assertRaises(manager.Rejected):
            self.mutate("select", 1, selection=self.payment_selection())

    def test_payment_manifest_corruption_blocks_enable_but_allows_disable(self):
        self.install_payment()
        first = self.mutate("select", 0, kind="payment", selection=self.payment_selection())
        self.assertEqual(first["payment"]["manifest_sha256"], hashlib.sha256(
            (self.root / "payment.contract-example/1.0.0/manifest.json").read_bytes()).hexdigest())
        self.mutate("enable", 1, kind="payment")
        executable = self.root / "payment.contract-example/1.0.0/plugin"
        executable.chmod(0o755)
        executable.write_bytes(b"corrupted")
        with self.assertRaises(manager.Rejected):
            self.mutate("enable", 2, kind="payment")
        self.assertFalse(self.mutate("disable", 2, kind="payment")["payment"]["enabled"])

    def test_verify_gnu_and_ustar_without_execution(self):
        for format in (tarfile.GNU_FORMAT, tarfile.USTAR_FORMAT):
            with self.subTest(format=format):
                package = self.verify(format=format)
                self.assertEqual(package.manifest["version"], "1.0.0")
                self.assertFalse(self.root.exists())
                self.assertFalse(self.policy.exists())

    def raw_inputs(self, version="1.1.0", extra=None):
        installed = contents(version)
        installed["SHA256SUMS"] = "".join(hashlib.sha256(data).hexdigest() + "  " + name + "\n" for name, data in sorted(installed.items())).encode()
        binary = installed.pop("plugin")
        if extra:
            installed.update(extra)
        resource = archive(files=installed, version=version, root="aster-smtp-" + version + "-linux-amd64-resources")
        path, pin = self.package(resource)
        binary_path = self.base / ("raw-" + version)
        binary_path.write_bytes(binary)
        binary_path.chmod(0o755)
        return path, pin, binary_path, hashlib.sha256(binary).hexdigest()

    def test_raw_resources_and_protected_binary_assemble_without_core_or_policy_change(self):
        path, pin, binary, binary_pin = self.raw_inputs()
        core = self.base / "aster-api"
        core.write_bytes(b"existing core remains unchanged")
        before = hashlib.sha256(core.read_bytes()).hexdigest()
        package = manager.verify_package(str(path), pin, trust=self.trust, binary=str(binary), binary_sha256=binary_pin)
        self.assertEqual(set(package.files), manager.FILES)
        manager.install_package(str(self.root), package, self.trust)
        self.assertEqual((self.root / "email.smtp/1.1.0/plugin").read_bytes(), elf())
        self.assertEqual(hashlib.sha256(core.read_bytes()).hexdigest(), before)
        self.assertFalse(self.policy.exists())
        with self.assertRaises(manager.Rejected):
            manager.install_package(str(self.root), package, self.trust)

    def test_raw_binary_flag_pairs_hash_and_resource_binary_mix_rejected(self):
        path, pin, binary, binary_pin = self.raw_inputs()
        for options in ({"binary": str(binary)}, {"binary_sha256": binary_pin}, {"binary": str(binary), "binary_sha256": "0" * 64}, {}):
            with self.subTest(options=options), self.assertRaises(manager.Rejected):
                manager.verify_package(str(path), pin, trust=self.trust, **options)
        legacy, legacy_pin = self.package()
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(legacy), legacy_pin, trust=self.trust, binary=str(binary), binary_sha256=binary_pin)
        mixed, mixed_pin, binary, binary_pin = self.raw_inputs(extra={"plugin": elf()})
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(mixed), mixed_pin, trust=self.trust, binary=str(binary), binary_sha256=binary_pin)

    def test_raw_binary_symlink_hardlink_write_and_tampering_rejected(self):
        path, pin, binary, binary_pin = self.raw_inputs()
        link = self.base / "binary-link"
        link.symlink_to(binary)
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(path), pin, trust=self.trust, binary=str(link), binary_sha256=binary_pin)
        link.unlink(); os.link(binary, link)
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(path), pin, trust=self.trust, binary=str(binary), binary_sha256=binary_pin)
        link.unlink(); binary.chmod(0o777)
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(path), pin, trust=self.trust, binary=str(binary), binary_sha256=binary_pin)
        binary.chmod(0o755); binary.write_bytes(b"changed executable")
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(path), pin, trust=self.trust, binary=str(binary), binary_sha256=binary_pin)

    def test_raw_resource_elf_disguised_as_utf8_readme_with_correct_dual_pins_rejected(self):
        files = contents("1.1.0")
        files["README.md"] = b"\x7fELF" + b"\x00" * 64
        files["README.md"].decode("utf-8")
        files["SHA256SUMS"] = "".join(hashlib.sha256(data).hexdigest() + "  " + name + "\n" for name, data in sorted(files.items())).encode()
        binary_bytes = files.pop("plugin")
        raw = archive(files=files, version="1.1.0", root="aster-smtp-1.1.0-linux-amd64-resources")
        path, pin = self.package(raw)
        binary = self.base / "approved-raw-binary"
        binary.write_bytes(binary_bytes); binary.chmod(0o755)
        with self.assertRaisesRegex(manager.Rejected, "Resource archives cannot contain ELF"):
            manager.verify_package(str(path), pin, trust=self.trust, binary=str(binary), binary_sha256=hashlib.sha256(binary_bytes).hexdigest())

    def test_legacy_original_compatibility_and_old_archive_bytes_still_supported(self):
        files = contents()
        manifest = json.loads(files["manifest.json"]); manifest["core_max_exclusive"] = "0.5.0"
        files["manifest.json"] = json.dumps(manifest).encode()
        path, pin = self.package(files=files)
        package = manager.verify_package(str(path), pin, core="0.4.0", trust=self.trust)
        self.assertEqual(package.manifest["version"], "1.0.0")
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(path), pin, core="0.5.0", trust=self.trust)

    def test_outer_hash_required_and_bound(self):
        path, digest = self.package()
        for wrong in ("", "A" * 64, "0" * 64, digest[:-1]):
            with self.subTest(wrong=wrong), self.assertRaises(manager.Rejected):
                manager.verify_package(str(path), wrong, trust=self.trust)

    def test_exact_inventory_duplicates_and_unknown_file(self):
        root = "aster-smtp-1.0.0-linux-amd64"
        for name in ("plugin", "unexpected", "../outside", "subdir/plugin"):
            member = tarfile.TarInfo(root + "/" + name)
            member.mode, member.size = 0o644, 1
            with self.subTest(name=name), self.assertRaises(manager.Rejected):
                self.verify(extras=[(member, b"x")])
        files = contents()
        del files["README.md"]
        with self.assertRaises(manager.Rejected):
            self.verify(files=files)

    def test_links_devices_fifo_extensions_and_modes_rejected(self):
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.CHRTYPE, tarfile.BLKTYPE, tarfile.FIFOTYPE, tarfile.GNUTYPE_LONGNAME, tarfile.XHDTYPE):
            member = tarfile.TarInfo("aster-smtp-1.0.0-linux-amd64/evil")
            member.type, member.mode, member.linkname = kind, 0o644, "/tmp/evil"
            with self.subTest(kind=kind), self.assertRaises(manager.Rejected):
                self.verify(extras=[(member, b"")])
        for field, value in (("mode", 0o777), ("mode", 0o4755), ("uid", 1000), ("gid", 1000)):
            def edit(member):
                if member.name.endswith("/plugin"):
                    setattr(member, field, value)
            with self.subTest(field=field, value=value), self.assertRaises(manager.Rejected):
                self.verify(edits=edit)

    def test_pax_long_names_and_duplicate_root_forbidden(self):
        def pax(member):
            member.pax_headers = {"comment": "unsupported"}
        with self.assertRaises(manager.Rejected):
            self.verify(format=tarfile.PAX_FORMAT, edits=pax)
        member = tarfile.TarInfo("aster-smtp-1.0.0-linux-amd64")
        member.type, member.mode = tarfile.DIRTYPE, 0o755
        with self.assertRaises(manager.Rejected):
            self.verify(extras=[(member, b"")])
        member = tarfile.TarInfo("aster-smtp-1.0.0-linux-amd64/" + "x" * 150)
        member.mode, member.size = 0o644, 1
        with self.assertRaises(manager.Rejected):
            self.verify(extras=[(member, b"x")])

    def test_path_traversal_wrong_root_and_version(self):
        for root in ("../outside", "/absolute", "aster-smtp-1.0.1-linux-amd64", "aster-smtp-01.0.0-linux-amd64"):
            with self.subTest(root=root), self.assertRaises(manager.Rejected):
                self.verify(root=root)

    def test_gzip_bombs_concatenation_truncation_and_trailing_data(self):
        raw = archive()
        for malformed in (raw + raw, raw + b"garbage", raw[:-10]):
            with self.subTest(size=len(malformed)), self.assertRaises(manager.Rejected):
                self.verify(raw=malformed)
        with patch.object(manager, "MAX_ARCHIVE", 1024), self.assertRaises(manager.Rejected):
            manager.unpack_archive(raw)

    def test_internal_hash_duplicate_mismatch_and_missing_rejected(self):
        for checksums in (b"0" * 64 + b"  plugin\n", b"not-a-checksum\n", b"0" * 64 + b"  plugin\n" + b"0" * 64 + b"  plugin\n"):
            files = contents()
            files["SHA256SUMS"] = checksums
            with self.subTest(checksums=checksums[:12]), self.assertRaises(manager.Rejected):
                self.verify(files=files)
        files = contents()
        manifest = json.loads(files["manifest.json"])
        manifest["sha256"] = "0" * 64
        files["manifest.json"] = json.dumps(manifest).encode()
        with self.assertRaises(manager.Rejected):
            self.verify(files=files)

    def test_manifest_strict_duplicate_alias_unknown_null_and_types(self):
        baseline = contents()
        original = baseline["manifest.json"]
        malformed = [original[:-1] + b',"schema":1}', original.replace(b'"schema"', b'"Schema"'), original[:-1] + b',"extra":0}', original.replace(b'"schema": 1', b'"schema": null'), original.replace(b'"schema": 1', b'"schema": true'), original + b"{}"]
        for value in malformed:
            files = dict(baseline)
            files["manifest.json"] = value
            with self.subTest(value=value[:30]), self.assertRaises(manager.Rejected):
                self.verify(files=files)

    def test_manifest_capability_protocol_and_compatibility(self):
        for key, value in (("id", "email.other"), ("protocol", "aster.smtp/2"), ("capabilities", ["smtp.send", "filesystem"]), ("executable", "../plugin"), ("core_min", "0.6.0"), ("core_max_exclusive", "0.4.0"), ("version", "1.0.0-rc.1")):
            files = contents()
            manifest = json.loads(files["manifest.json"])
            manifest[key] = value
            files["manifest.json"] = json.dumps(manifest).encode()
            with self.subTest(key=key), self.assertRaises(manager.Rejected):
                self.verify(files=files)

    def test_static_linux_amd64_required(self):
        mutations = [(0, b"#!sh"), (4, b"\x01"), (5, b"\x02"), (18, b"\xb7\x00"), (16, b"\x03\x00"), (64, struct.pack("<I", 2)), (64, struct.pack("<I", 3)), (68, struct.pack("<I", 4)), (96, struct.pack("<Q", 4096)), (32, struct.pack("<Q", 4096))]
        for offset, replacement in mutations:
            binary = bytearray(elf())
            binary[offset:offset + len(replacement)] = replacement
            with self.subTest(offset=offset, replacement=replacement), self.assertRaises(manager.Rejected):
                manager.validate_elf(bytes(binary))

    def test_invalid_license_and_notices_rejected(self):
        for name, value in (("LICENSE", b"\xff"), ("README.md", b"\n"), ("THIRD_PARTY_NOTICES.json", b"{}"), ("THIRD_PARTY_NOTICES.json", b"null")):
            files = contents()
            files[name] = value
            with self.subTest(name=name, value=value), self.assertRaises(manager.Rejected):
                self.verify(files=files)

    def test_archive_symlink_hardlink_and_writable_parents_rejected(self):
        path, digest = self.package()
        for name, hard in (("symlink", False), ("hardlink", True)):
            linked = self.base / name
            os.link(path, linked) if hard else linked.symlink_to(path)
            with self.subTest(name=name), self.assertRaises(manager.Rejected):
                manager.verify_package(str(linked), digest, trust=self.trust)
            linked.unlink()
        os.chmod(path, 0o666)
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(path), digest, trust=self.trust)
        os.chmod(path, 0o644)
        os.chmod(self.base, 0o777)
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(path), digest, trust=self.trust)
        os.chmod(self.base, 0o700)

    def test_production_trust_rejects_nonroot_fixture(self):
        if os.geteuid() == 0:
            self.skipTest("nonroot ownership fixture requires a nonroot test user")
        path, digest = self.package()
        with self.assertRaises(manager.Rejected):
            manager.verify_package(str(path), digest)

    def test_install_preserves_core_policy_and_immutable_version(self):
        core = self.base / "aster-api"
        core.write_bytes(b"synthetic unchanged core")
        before = core.read_bytes()
        package = self.install()
        installed = self.root / "email.smtp" / "1.0.0"
        self.assertEqual(set(path.name for path in installed.iterdir()), manager.FILES)
        self.assertEqual((installed / "plugin").read_bytes(), package.files["plugin"])
        self.assertEqual(stat.S_IMODE(installed.stat().st_mode), 0o555)
        self.assertEqual(stat.S_IMODE((installed / "plugin").stat().st_mode), 0o555)
        self.assertFalse(self.policy.exists())
        self.assertEqual(core.read_bytes(), before)
        with self.assertRaises(manager.Rejected):
            manager.install_package(str(self.root), package, self.trust)
        self.assertEqual((installed / "plugin").read_bytes(), package.files["plugin"])
        self.assertFalse(list(self.root.glob(".install-*")))

    def test_atomic_install_refuses_existing_empty_version(self):
        package = self.verify()
        (self.root / "email.smtp" / "1.0.0").mkdir(parents=True)
        with self.assertRaises(manager.Rejected):
            manager.install_package(str(self.root), package, self.trust)
        self.assertEqual(list((self.root / "email.smtp" / "1.0.0").iterdir()), [])

    def test_install_symlink_parent_and_identifier_rejected(self):
        package = self.verify()
        outside = self.base / "outside"
        outside.mkdir()
        self.root.symlink_to(outside, target_is_directory=True)
        with self.assertRaises(manager.Rejected):
            manager.install_package(str(self.root), package, self.trust)
        self.assertEqual(list(outside.iterdir()), [])
        self.root.unlink()
        self.root.mkdir()
        (self.root / "email.smtp").symlink_to(outside, target_is_directory=True)
        with self.assertRaises(manager.Rejected):
            manager.install_package(str(self.root), package, self.trust)
        self.assertEqual(list(outside.iterdir()), [])

    def test_lock_file_link_rejected(self):
        package = self.verify()
        self.root.mkdir()
        elsewhere = self.base / "lock"
        elsewhere.touch(mode=0o600)
        os.link(elsewhere, self.root / ".manager.lock")
        with self.assertRaises(manager.Rejected):
            manager.install_package(str(self.root), package, self.trust)
        self.assertEqual(elsewhere.read_bytes(), b"")

    def test_restrictive_umask_does_not_hide_installed_files(self):
        previous = os.umask(0o077)
        try:
            self.install()
        finally:
            os.umask(previous)
        self.assertEqual(stat.S_IMODE(self.root.stat().st_mode), 0o755)
        self.assertEqual(stat.S_IMODE((self.root / "email.smtp").stat().st_mode), 0o755)

    def test_select_enable_upgrade_and_rollback_revisions(self):
        self.install()
        self.install("1.0.1")
        first = self.mutate("select", 0, selection=self.selection())
        self.assertFalse(first["smtp"]["enabled"])
        self.assertEqual(first["revision"], 1)
        second = self.mutate("enable", 1)
        self.assertTrue(second["smtp"]["enabled"])
        with self.assertRaises(manager.Rejected):
            self.mutate("disable", 1)
        self.assertEqual(manager.read_policy(str(self.policy), self.trust), second)
        third = self.mutate("select", 2, selection=self.selection("1.0.1"))
        self.assertFalse(third["smtp"]["enabled"])
        self.mutate("enable", 3)
        fifth = self.mutate("rollback", 4, version="1.0.0")
        self.assertTrue(fifth["smtp"]["enabled"])
        self.assertEqual(fifth["smtp"]["version"], "1.0.0")
        self.assertEqual(fifth["smtp"]["allowed_targets"], first["smtp"]["allowed_targets"])
        self.assertEqual(stat.S_IMODE(self.policy.stat().st_mode), 0o644)
        self.assertFalse(list(self.config.glob(".plugin-policy-*")))

    def test_rollback_disabled_selection_and_wrong_direction(self):
        self.install()
        self.install("1.0.1")
        self.mutate("select", 0, selection=self.selection("1.0.1"))
        with self.assertRaises(manager.Rejected):
            self.mutate("rollback", 1, version="1.0.2")
        policy = self.mutate("rollback", 1, version="1.0.0")
        self.assertFalse(policy["smtp"]["enabled"])

    def test_enable_requires_explicit_destination(self):
        self.install()
        first = self.mutate("select", 0, selection=self.selection(targets=[]))
        with self.assertRaises(manager.Rejected):
            self.mutate("enable", 1)
        self.assertEqual(manager.read_policy(str(self.policy), self.trust), first)

    def test_wrong_identity_and_invalid_targets_do_not_write_policy(self):
        self.install()
        for field, value in (("worker_uid", 0), ("client_uid", 2001), ("worker_uid", True), ("socket", "/run/../socket"), ("allowed_targets", [{"host": "evil\nsecret", "port": 2525}]), ("allowed_targets", [{"host": "localhost", "port": True}]), ("allowed_targets", [{"host": "localhost", "port": 2525}] * 2)):
            selection = self.selection()
            selection[field] = value
            with self.subTest(field=field, value=value), self.assertRaises(manager.Rejected):
                self.mutate("select", 0, selection=selection)
            self.assertFalse(self.policy.exists())

    def test_installed_corruption_blocks_enable_but_not_emergency_disable(self):
        self.install()
        self.mutate("select", 0, selection=self.selection())
        self.mutate("enable", 1)
        executable = self.root / "email.smtp" / "1.0.0" / "plugin"
        os.chmod(executable, 0o755)
        executable.write_bytes(b"corrupt")
        with self.assertRaises(manager.Rejected):
            self.mutate("enable", 2)
        policy = self.mutate("disable", 2)
        self.assertFalse(policy["smtp"]["enabled"])

    def test_installed_manifest_symlink_blocks_selection(self):
        self.install()
        directory = self.root / "email.smtp" / "1.0.0"
        os.chmod(directory, 0o755)
        source = directory / "manifest.json"
        elsewhere = self.base / "manifest.json"
        elsewhere.write_bytes(source.read_bytes())
        source.unlink()
        source.symlink_to(elsewhere)
        with self.assertRaises(manager.Rejected):
            self.mutate("select", 0, selection=self.selection())

    def test_policy_symlink_hardlink_and_unknown_fields_rejected(self):
        self.install()
        elsewhere = self.base / "policy"
        elsewhere.write_text('{"schema":1,"revision":1}')
        for hard in (False, True):
            os.link(elsewhere, self.policy) if hard else self.policy.symlink_to(elsewhere)
            with self.subTest(hard=hard), self.assertRaises(manager.Rejected):
                self.mutate("select", 1, selection=self.selection())
            self.policy.unlink()
        self.policy.write_text('{"schema":1,"revision":1,"secret":"never print this"}')
        with self.assertRaises(manager.Rejected) as caught:
            self.mutate("select", 1, selection=self.selection())
        self.assertNotIn("never print this", str(caught.exception))

    def test_missing_policy_status_and_state_changes(self):
        self.install()
        self.assertEqual(manager.read_policy(str(self.policy), self.trust), {"schema": 1, "revision": 0})
        for action in ("enable", "disable", "rollback"):
            with self.subTest(action=action), self.assertRaises(manager.Rejected):
                self.mutate(action, 0, version="1.0.0")
        self.assertFalse(self.policy.exists())

    def test_atomic_policy_readers_never_observe_partial_json(self):
        self.install()
        self.mutate("select", 0, selection=self.selection())
        stop, failures = threading.Event(), []
        def reader():
            while not stop.is_set():
                try:
                    value = json.loads(self.policy.read_bytes())
                    self.assertEqual(value["schema"], 1)
                except Exception as exc:
                    failures.append(exc)
                    return
        thread = threading.Thread(target=reader)
        thread.start()
        try:
            for revision in range(1, 15):
                self.mutate("enable" if revision % 2 else "disable", revision)
        finally:
            stop.set()
            thread.join()
        self.assertEqual(failures, [])

    def test_uint64_revision_overflow_rejected(self):
        self.install()
        self.mutate("select", 0, selection=self.selection())
        original = self.policy.read_bytes()
        for revision in (-1, True, (1 << 64) - 1, 1 << 64):
            with self.subTest(revision=revision), self.assertRaises(manager.Rejected):
                self.mutate("disable", revision)
        self.assertEqual(self.policy.read_bytes(), original)

    def test_concurrent_policy_writers_require_a_fresh_revision(self):
        self.install()
        self.mutate("select", 0, selection=self.selection())
        barrier = threading.Barrier(2)
        def writer():
            barrier.wait()
            try:
                return self.mutate("enable", 1)
            except manager.Rejected:
                return None
        with ThreadPoolExecutor(max_workers=2) as executor:
            results = list(executor.map(lambda _: writer(), range(2)))
        self.assertEqual(sum(result is not None for result in results), 1)
        self.assertEqual(manager.read_policy(str(self.policy), self.trust)["revision"], 2)

    def test_symlink_policy_parent_cannot_write_an_outside_policy(self):
        self.install()
        outside = self.base / "outside-config"
        outside.mkdir()
        self.config.rmdir()
        self.config.symlink_to(outside, target_is_directory=True)
        with self.assertRaises(manager.Rejected):
            self.mutate("select", 0, selection=self.selection())
        self.assertEqual(list(outside.iterdir()), [])

    def test_strict_json_bounds(self):
        invalid = [b'{"valid":null}', b'{"valid":NaN}', b'{"Valid":0}', b'"\xff"', b"[" * 10 + b"0" + b"]" * 10, json.dumps(list(range(65))).encode(), json.dumps({"k" + str(i): i for i in range(65)}).encode()]
        for raw in invalid:
            with self.subTest(raw=raw[:20]), self.assertRaises(manager.Rejected):
                manager.strict_json(raw, 8192)

    def test_cli_nonroot_has_no_relaxed_trust_option(self):
        if os.geteuid() == 0:
            self.skipTest("CLI nonroot rejection requires nonroot test user")
        path, digest = self.package()
        process = subprocess.run([sys.executable, str(SCRIPT), "--root", str(self.root), "--policy", str(self.policy), "install", str(path), "--sha256", digest], capture_output=True, text=True, check=False)
        self.assertEqual(process.returncode, 1)
        self.assertIn("as root", process.stderr)
        self.assertFalse(self.root.exists())
        self.assertNotIn("--trust", subprocess.run([sys.executable, str(SCRIPT), "--help"], capture_output=True, text=True, check=False).stdout)

    def test_target_parser_including_ipv6(self):
        self.assertEqual(manager.parse_target("localhost:2525"), {"host": "localhost", "port": 2525})
        self.assertEqual(manager.parse_target("[::1]:2525"), {"host": "::1", "port": 2525})
        for target in ("localhost", "localhost:0", "evil/host:443", "localhost:not-a-port"):
            with self.subTest(target=target), self.assertRaises(Exception):
                manager.parse_target(target)


class ResourceManagerTest(unittest.TestCase):
    # Reuse fixtures; the complete existing hostile package suite runs above.
    setUp = ManagerTest.setUp
    tearDown = ManagerTest.tearDown
    package = ManagerTest.package
    verify = ManagerTest.verify
    install = ManagerTest.install
    selection = ManagerTest.selection
    mutate = ManagerTest.mutate
    def install_resource(self, version="0.1.0"):
        files = contents(version)
        manifest = json.loads(files["manifest.json"])
        manifest.update(id="resource.aster-cloud", protocol="aster.resource/1", core_min="0.5.10", capabilities=["resource.send", "resource.lookup"])
        files["manifest.json"] = json.dumps(manifest).encode()
        package = self.verify(files=files, version=version, root="aster-resource-aster-cloud-" + version + "-linux-amd64")
        manager.install_package(str(self.root), package, self.trust)
        return package

    def resource_selection(self, version="0.1.0"):
        return {"id": "resource.aster-cloud", "version": version, "worker_uid": 2300, "client_uid": 0,
                "socket": "/run/aster-resource/plugin.sock", "allowed_targets": [{"host": "cloud.example.test", "port": 443}], "controller_id": "primary"}

    def test_resource_install_approve_rollback_preserves_other_slots(self):
        self.install()
        self.mutate("select", 0, selection=self.selection())
        self.mutate("enable", 1)
        original = manager.read_policy(str(self.policy), self.trust)["smtp"]
        self.install_resource()
        with patch.object(subprocess, "run", side_effect=AssertionError("package execution forbidden")):
            selected = self.mutate("select", 2, selection=self.resource_selection(), kind="resource")
            self.assertFalse(selected["resource"]["enabled"])
            enabled = self.mutate("enable", 3, kind="resource")
        self.assertEqual(original, enabled["smtp"])
        self.assertEqual(enabled["resource"]["client_uid"], 0)
        with self.assertRaises(manager.Rejected):
            self.mutate("remove", 4, kind="resource")
        self.install_resource("0.2.0")
        self.mutate("select", 4, selection=self.resource_selection("0.2.0"), kind="resource")
        rolled = self.mutate("rollback", 5, version="0.1.0", kind="resource")
        self.assertEqual(rolled["resource"]["version"], "0.1.0")
        self.assertFalse(rolled["resource"]["enabled"])
        self.assertEqual(rolled["smtp"], original)
        self.mutate("enable", 6, kind="resource")
        disabled = self.mutate("disable", 7, kind="resource")
        self.assertFalse(disabled["resource"]["enabled"])
        removed = self.mutate("remove", 8, kind="resource")
        self.assertNotIn("resource", removed)
        self.assertEqual(original, removed["smtp"])
        self.assertTrue((self.root / "resource.aster-cloud/0.1.0/plugin").is_file())
        with self.assertRaises(manager.Rejected):
            self.mutate("remove", 9, kind="smtp")

    def test_resource_root_client_controller_and_isolation_required(self):
        self.install_resource()
        for change in ({"client_uid": 2301}, {"worker_uid": 0}, {"controller_id": "../bad"}, {"merchant_id": "not-a-payment"}):
            with self.subTest(change=change), self.assertRaises(manager.Rejected):
                self.mutate("select", 0, selection=self.resource_selection() | change, kind="resource")
        self.assertFalse(self.policy.exists())
        self.install()
        self.mutate("select", 0, selection=self.selection())
        for change in ({"worker_uid": 2001}, {"worker_uid": 2002}, {"socket": "/run/aster-smtp/plugin.sock"}):
            with self.subTest(change=change), self.assertRaises(manager.Rejected):
                self.mutate("select", 1, selection=self.resource_selection() | change, kind="resource")
        self.assertNotIn("resource", manager.read_policy(str(self.policy), self.trust))

    def test_resource_capability_and_legacy_core_rejected(self):
        package = self.install_resource()
        for change in ({"capabilities": ["resource.send", "resource.lookup", "resource.exec"]}, {"protocol": "aster.payment/1"}, {"core_min": "0.5.8"}, {"core_min": "0.5.9"}):
            with self.subTest(change=change), self.assertRaises(manager.Rejected):
                manager.validate_manifest(package.manifest | change, manager.CORE_VERSION)
        for core in ("0.5.8", "0.5.9"):
            with self.subTest(core=core), self.assertRaises(manager.Rejected):
                manager.validate_manifest(package.manifest, core)


if __name__ == "__main__":
    unittest.main(verbosity=2)
