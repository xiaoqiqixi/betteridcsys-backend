#!/usr/bin/env python3
"""No-privilege unit tests for the resource CI gate's bounded root operations."""
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

DIRECTORY = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("resource_package_gate", DIRECTORY / "resource_package_gate.py")
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class ResourcePackageGateTest(unittest.TestCase):
    def test_resource_package_starts_at_first_protocol_core_release(self):
        specification = importlib.util.spec_from_file_location(
            "resource_package_builder", DIRECTORY.parent / "scripts/build-resource-plugin.py")
        builder = importlib.util.module_from_spec(specification)
        specification.loader.exec_module(builder)
        raw = builder.package(b"synthetic binary, never executed", b"project license", b"readme",
                              "go license", "go patents", "fixture Go version")
        with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
            member = "aster-resource-aster-cloud-0.1.1-linux-amd64/manifest.json"
            manifest = json.load(archive.extractfile(member))
        self.assertEqual(manifest["core_min"], "0.5.10")
        self.assertEqual(manifest["core_max_exclusive"], "0.6.0")
        self.assertEqual(manifest["version"], "0.1.1")
        self.assertEqual(manifest["protocol"], "aster.resource/1")
        worker_source = (DIRECTORY.parents[1] / "backend/plugins/resourcebridge/config.go").read_text()
        self.assertEqual(re.search(r'const Version = "([^"]+)"', worker_source)[1], manifest["version"])
        core_source = (DIRECTORY.parents[1] / "backend/pkg/pluginapi/protocol.go").read_text()
        self.assertEqual(re.search(r'ResourceMinCoreVersion\s*=\s*"([^"]+)"', core_source)[1], manifest["core_min"])

    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory(prefix="aster-resource-gate-unit-")
        self.addCleanup(self.scratch.cleanup)
        self.root = Path(self.scratch.name).resolve()
        self.target = self.root / "target"
        self.target.mkdir(mode=0o700)
        self.descriptor = os.open(self.target, os.O_RDONLY)
        self.addCleanup(os.close, self.descriptor)
        self.source = self.root / "source"
        self.source.write_bytes(b"package fixture")
        self.digest = hashlib.sha256(self.source.read_bytes()).hexdigest()

    def test_copy_is_hash_pinned_and_restrictive(self):
        gate.copy_verified(str(self.source), self.digest, "resource.tar.gz", self.descriptor)
        output = self.target / "resource.tar.gz"
        self.assertEqual(output.read_bytes(), self.source.read_bytes())
        self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o600)
        with self.assertRaises(FileExistsError):
            gate.copy_verified(str(self.source), self.digest, "resource.tar.gz", self.descriptor)

    def test_hash_mismatch_leaves_no_output(self):
        with self.assertRaisesRegex(RuntimeError, "SHA256"):
            gate.copy_verified(str(self.source), "f" * 64, "resource.tar.gz", self.descriptor)
        self.assertEqual(list(self.target.iterdir()), [])

    def test_copy_rejects_arbitrary_destination_and_invalid_hash(self):
        for target, digest in (("../outside", self.digest), ("payload", self.digest), ("resource.tar.gz", "0")):
            with self.subTest(target=target, digest=digest):
                with self.assertRaisesRegex(RuntimeError, "Invalid verification input"):
                    gate.copy_verified(str(self.source), digest, target, self.descriptor)
        self.assertEqual(list(self.target.iterdir()), [])

    def test_copy_rejects_relative_dot_and_symlink_paths(self):
        (self.root / "link").symlink_to(self.source)
        (self.root / "alias").symlink_to(self.root, target_is_directory=True)
        for source in ("source", str(self.root) + "/./source", str(self.root / "link"), str(self.root / "alias/source")):
            with self.subTest(source=source):
                with self.assertRaises((OSError, RuntimeError)):
                    gate.copy_verified(source, self.digest, "resource.tar.gz", self.descriptor)
        self.assertEqual(list(self.target.iterdir()), [])

    def test_copy_rejects_nonregular_and_oversized_input_without_reading(self):
        with self.assertRaisesRegex(RuntimeError, "bounded regular"):
            gate.copy_verified(str(self.target), self.digest, "resource.tar.gz", self.descriptor)
        os.mkfifo(self.root / "fifo", 0o600)
        with self.assertRaisesRegex(RuntimeError, "bounded regular"):
            gate.copy_verified(str(self.root / "fifo"), self.digest, "resource.tar.gz", self.descriptor)
        with mock.patch.object(gate, "MAX_BYTES", 2):
            with self.assertRaisesRegex(RuntimeError, "bounded regular"):
                gate.copy_verified(str(self.source), self.digest, "resource.tar.gz", self.descriptor)

    def test_cleanup_paths_have_a_nonconfigurable_fixed_prefix(self):
        with mock.patch.object(gate.os, "lstat") as lookup:
            for path in ("/", "/run", "/tmp/aster-resource-package-gate.abcdefgh", "/run/aster-resource-package-gate.",
                         "/run/aster-resource-package-gate.abcdefgh/..", "/run/aster-resource-package-gate.abcdefgh/extra"):
                with self.subTest(path=path):
                    with self.assertRaisesRegex(RuntimeError, "Unexpected protected"):
                        gate.protected_directory(path)
            lookup.assert_not_called()

    def test_protected_ancestors_owner_mode_symlink_and_identity(self):
        root = SimpleNamespace(st_mode=stat.S_IFDIR | 0o755, st_uid=0, st_dev=1, st_ino=2)
        protected = SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=0, st_dev=1, st_ino=3)
        path = gate.PREFIX + "abcdefgh"
        with mock.patch.object(gate.os, "lstat", side_effect=[root, root, protected]):
            self.assertEqual(gate.protected_directory(path), (1, 3))
        for unsafe in (SimpleNamespace(st_mode=stat.S_IFLNK | 0o777, st_uid=0),
                       SimpleNamespace(st_mode=stat.S_IFDIR | 0o777, st_uid=0),
                       SimpleNamespace(st_mode=stat.S_IFDIR | 0o755, st_uid=1000)):
            with mock.patch.object(gate.os, "lstat", side_effect=[root, unsafe]):
                with self.assertRaisesRegex(RuntimeError, "ancestor"):
                    gate.protected_directory(path)
        for changed in (SimpleNamespace(st_mode=stat.S_IFDIR | 0o750, st_uid=0, st_dev=1, st_ino=3),
                        SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=1000, st_dev=1, st_ino=3),
                        SimpleNamespace(st_mode=stat.S_IFLNK | 0o700, st_uid=0, st_dev=1, st_ino=3),
                        SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=0, st_dev=1, st_ino=4)):
            with mock.patch.object(gate.os, "lstat", side_effect=[root, root, changed]):
                with self.assertRaisesRegex(RuntimeError, "replaced"):
                    gate.protected_directory(path, (1, 3))

    def test_cleanup_refuses_changed_inode_unexpected_entries_and_symlinks(self):
        info = self.target.stat()
        identity = (info.st_dev, info.st_ino)
        with mock.patch.object(gate, "protected_directory"):
            with self.assertRaisesRegex(RuntimeError, "replaced"):
                gate.cleanup(str(self.target), (info.st_dev, info.st_ino + 1))
            (self.target / "unrelated").write_bytes(b"preserve")
            with self.assertRaisesRegex(RuntimeError, "unexpected"):
                gate.cleanup(str(self.target), identity)
            self.assertTrue((self.target / "unrelated").exists())
            (self.target / "unrelated").unlink()
            (self.target / "resource.tar.gz").symlink_to(self.source)
            with self.assertRaisesRegex(RuntimeError, "unsafe"):
                gate.cleanup(str(self.target), identity)
            self.assertTrue(self.source.exists())

    def test_root_entry_requires_exact_arguments_linux_and_root(self):
        with mock.patch.object(gate.tempfile, "mkdtemp") as create:
            for arguments in ([], ["a"] * 3, ["a"] * 5):
                with self.assertRaisesRegex(RuntimeError, "Expected"):
                    gate.main(arguments)
            with mock.patch.object(gate.sys, "platform", "darwin"):
                with self.assertRaisesRegex(RuntimeError, "requires Linux root"):
                    gate.main(["a"] * 4)
            with mock.patch.object(gate.sys, "platform", "linux"), mock.patch.object(gate.os, "geteuid", return_value=1000):
                with self.assertRaisesRegex(RuntimeError, "requires Linux root"):
                    gate.main(["a"] * 4)
            create.assert_not_called()

    def test_verify_only_uses_isolated_python_bounded_env_and_always_cleans(self):
        info = self.target.stat()
        root_info = SimpleNamespace(st_mode=stat.S_IFDIR | 0o755, st_uid=0)
        for invalid in (None, "service", "version"):
            output = {"verified": True, "id": "resource.aster-cloud", "version": "0.1.1",
                      "archive_sha256": self.digest, "service_action": "install" if invalid == "service" else "none"}
            if invalid == "version":
                output["version"] = "0.1.0"
            with self.subTest(invalid=invalid), mock.patch.object(gate.sys, "platform", "linux"), \
                    mock.patch.object(gate.os, "geteuid", return_value=0), \
                    mock.patch.object(gate.os, "lstat", return_value=root_info), \
                    mock.patch.object(gate.os, "umask"), mock.patch.object(gate.signal, "signal"), \
                    mock.patch.object(gate.tempfile, "mkdtemp", return_value=str(self.target)), \
                    mock.patch.object(gate, "protected_directory", return_value=(info.st_dev, info.st_ino)), \
                    mock.patch.object(gate, "copy_verified") as copy, \
                    mock.patch.object(gate, "cleanup") as clean, \
                    mock.patch.object(gate.subprocess, "run", return_value=SimpleNamespace(stdout=json.dumps(output))) as run:
                if invalid:
                    with self.assertRaisesRegex(RuntimeError, "unexpected verification result"):
                        gate.main([str(self.source), self.digest, str(self.source), self.digest])
                else:
                    gate.main([str(self.source), self.digest, str(self.source), self.digest])
                self.assertEqual(copy.call_count, 2)
                run.assert_called_once_with(
                    ["/usr/bin/python3", "-I", str(self.target / "plugin-manager.py"), "verify",
                     str(self.target / "resource.tar.gz"), "--sha256", self.digest],
                    cwd=str(self.target), env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8"},
                    capture_output=True, text=True, timeout=60, check=True)
                clean.assert_called_once_with(str(self.target), (info.st_dev, info.st_ino))

    def test_shell_missing_arguments_and_unsupported_platform_fail_explicitly(self):
        result = subprocess.run(["bash", str(DIRECTORY / "check-resource-package.sh")], text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Usage:", result.stderr)
        fakebin = self.root / "bin"
        fakebin.mkdir()
        (fakebin / "uname").write_text("#!/bin/sh\nprintf 'Darwin\\n'\n")
        (fakebin / "uname").chmod(0o700)
        result = subprocess.run(["bash", str(DIRECTORY / "check-resource-package.sh"), str(self.root)],
                                env=dict(os.environ, PATH=str(fakebin) + os.pathsep + os.environ["PATH"]),
                                text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not executed on this platform", result.stderr)


if __name__ == "__main__":
    unittest.main()
