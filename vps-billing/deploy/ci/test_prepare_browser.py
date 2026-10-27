#!/usr/bin/env python3
"""Offline installed-package fixtures only; no root, real Chrome or downloads."""
import hashlib
import importlib.util
import os
from pathlib import Path
import stat
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("prepare-browser.py").resolve()
SPEC = importlib.util.spec_from_file_location("prepare_browser", SCRIPT)
prepare_browser = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(prepare_browser)


class PrepareBrowserTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="aster-installed-chrome-fixture-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.tree = self.root / "opt/google/chrome"
        self.info = self.root / "var/lib/dpkg/info"
        for directory in (self.tree / "locales", self.info, self.root / "usr/bin"):
            directory.mkdir(parents=True)
        for directory in self.root.rglob("*"):
            if directory.is_dir():
                directory.chmod(0o755)
        (self.root / "var/lib/dpkg/status").write_text("Package: google-chrome-stable\nStatus: install ok installed\n")
        (self.root / "usr/bin/dpkg-query").write_text("synthetic protected executable; never executed\n")
        (self.root / "usr/bin/dpkg-query").chmod(0o755)
        self.files = {
            "/opt/google/chrome/chrome": b"synthetic chrome executable\n",
            "/opt/google/chrome/chrome-sandbox": b"synthetic sandbox executable\n",
            "/opt/google/chrome/google-chrome": b"synthetic wrapper executable\n",
            "/opt/google/chrome/libEGL.so": b"synthetic library\n",
            "/opt/google/chrome/locales/en-US.pak": b"synthetic locale\n",
        }
        for name, content in self.files.items():
            path = self.root / name.lstrip("/")
            path.write_bytes(content)
            path.chmod(0o777 if name in prepare_browser.CORE else 0o666)
        self.members = {"/.", "/opt", "/opt/google", "/opt/google/chrome", "/opt/google/chrome/locales"} | set(self.files)
        # The hosted image may remove this non-Chrome-tree package file. Its
        # absence must not cause broad dpkg --verify errors to be ignored.
        self.members.add("/etc/cron.daily/google-chrome")
        self.digests = {name: hashlib.md5(content, usedforsecurity=False).hexdigest() for name, content in self.files.items()}
        self.digests["/etc/cron.daily/google-chrome"] = hashlib.md5(b"removed runner cron", usedforsecurity=False).hexdigest()
        self.md5_path = self.info / "google-chrome-stable.md5sums"
        self.list_path = self.info / "google-chrome-stable.list"
        self.write_metadata()
        (self.root / "opt/google").chmod(0o777)
        self.tree.chmod(0o777)
        (self.tree / "locales").chmod(0o777)
        self.root_fd = os.open(self.root, prepare_browser.DIRECTORY_FLAGS)
        self.addCleanup(os.close, self.root_fd)
        # Model root ownership using this unprivileged fixture owner. The real
        # entry point has no UID/path override and still requires UID zero.
        patch = mock.patch.object(prepare_browser, "ROOT_UID", os.geteuid())
        patch.start()
        self.addCleanup(patch.stop)
        identity = mock.patch.object(prepare_browser, "package_identity", return_value="141.0.0.1-1")
        self.identity = identity.start()
        self.addCleanup(identity.stop)

    def write_metadata(self):
        self.md5_path.write_text("".join(digest + "  " + name.lstrip("/") + "\n" for name, digest in sorted(self.digests.items())))
        self.list_path.write_text("\n".join(sorted(self.members)) + "\n")
        self.md5_path.chmod(0o644)
        self.list_path.chmod(0o644)

    def prepare(self):
        return prepare_browser.prepare(self.root_fd)

    def test_modes_only_tighten_and_foreign_paths_are_untouched(self):
        opt = self.root / "opt"
        opt.chmod(0o777)
        cache = opt / "hostedtoolcache/tool/version"
        cache.mkdir(parents=True)
        cached_tool = cache / "tool"
        cached_tool.write_bytes(b"foreign cached tool")
        for path in (opt / "hostedtoolcache", cache.parent, cache, cached_tool):
            path.chmod(0o777)
        cache_before = {path: (path.stat().st_mode, path.stat().st_uid, path.stat().st_gid)
                        for path in (opt / "hostedtoolcache", cache.parent, cache, cached_tool)}
        foreign = self.root / "opt/google/another-product"
        foreign.write_bytes(b"foreign file")
        foreign.chmod(0o777)
        changes = []
        original_chmod = os.fchmod
        def tracked_chmod(descriptor, mode):
            path = Path(os.readlink(f"/proc/self/fd/{descriptor}"))
            before = os.fstat(descriptor)
            self.assertTrue(path in (opt, self.root / "opt/google") or path.is_relative_to(self.tree))
            self.assertEqual(mode, stat.S_IMODE(before.st_mode) & ~0o022)
            original_chmod(descriptor, mode)
            after = os.fstat(descriptor)
            self.assertEqual((before.st_uid, before.st_gid), (after.st_uid, after.st_gid))
            changes.append(path)
        with mock.patch.object(os, "fchmod", side_effect=tracked_chmod):
            result = self.prepare()
        self.assertTrue(result["passed"])
        self.assertEqual(result["files_verified"], len(self.files))
        self.assertEqual(result["tightened_entries"], len(changes))
        self.assertFalse(result["sandbox_changed"])
        self.assertFalse(result["browser_executed"])
        self.assertEqual(stat.S_IMODE((self.tree / "chrome").stat().st_mode), 0o755)
        self.assertEqual(stat.S_IMODE((self.tree / "libEGL.so").stat().st_mode), 0o644)
        self.assertEqual(stat.S_IMODE(foreign.stat().st_mode), 0o777)
        self.assertEqual(foreign.read_bytes(), b"foreign file")
        self.assertEqual(stat.S_IMODE(opt.stat().st_mode), 0o755)
        for path, before in cache_before.items():
            self.assertEqual((path.stat().st_mode, path.stat().st_uid, path.stat().st_gid), before)
        self.assertEqual(cached_tool.read_bytes(), b"foreign cached tool")
        with mock.patch.object(os, "fchmod") as chmod:
            self.assertEqual(self.prepare()["tightened_entries"], 0)
            chmod.assert_not_called()

    def test_existing_setid_bits_are_preserved_without_creating_a_setid_fixture(self):
        def info(mode):
            return SimpleNamespace(st_mode=stat.S_IFREG | mode, st_uid=os.geteuid(), st_gid=os.getegid(), st_nlink=1, st_dev=1, st_ino=2)
        with mock.patch.object(os, "fstat", side_effect=[info(0o4777), info(0o4755)]), mock.patch.object(os, "fchmod") as chmod:
            self.assertTrue(prepare_browser.tighten(99, "/opt/google/chrome/chrome-sandbox"))
            chmod.assert_called_once_with(99, 0o4755)
        with mock.patch.object(os, "fstat", side_effect=[info(0o755), info(0o755)]), mock.patch.object(os, "fchmod") as chmod:
            self.assertFalse(prepare_browser.tighten(99, "/opt/google/chrome/chrome-sandbox"))
            chmod.assert_not_called()

    def test_unprotected_metadata_and_ancestors_fail_before_any_mode_change(self):
        for path in (self.md5_path, self.list_path, self.root / "var/lib/dpkg/status", self.info):
            with self.subTest(path=path.name):
                original = stat.S_IMODE(path.stat().st_mode)
                path.chmod(original | 0o022)
                with mock.patch.object(os, "fchmod") as chmod:
                    with self.assertRaises(prepare_browser.Failure):
                        self.prepare()
                    chmod.assert_not_called()
                path.chmod(original)

    def test_unprotected_root_is_rejected_without_repair(self):
        self.root.chmod(0o777)
        with mock.patch.object(os, "fchmod") as chmod:
            with self.assertRaisesRegex(prepare_browser.Failure, r"path: / \(uid=\d+, mode=0777\)"):
                self.prepare()
            chmod.assert_not_called()
        self.assertEqual(stat.S_IMODE(self.root.stat().st_mode), 0o777)

    def test_opt_must_be_registered_before_any_mode_change(self):
        self.members.remove("/opt")
        self.write_metadata()
        opt = self.root / "opt"
        opt.chmod(0o777)
        with mock.patch.object(os, "fchmod") as chmod:
            with self.assertRaisesRegex(prepare_browser.Failure, "does not register /opt"):
                self.prepare()
            chmod.assert_not_called()
        self.assertEqual(stat.S_IMODE(opt.stat().st_mode), 0o777)

    def test_nonroot_opt_is_rejected_before_any_mode_change(self):
        opt = self.root / "opt"
        opt.chmod(0o777)
        original_stat = os.fstat
        def wrong_owner(descriptor):
            info = original_stat(descriptor)
            if Path(os.readlink(f"/proc/self/fd/{descriptor}")) == opt:
                fields = list(info)
                fields[4] = os.geteuid() + 1
                return os.stat_result(fields)
            return info
        with mock.patch.object(os, "fstat", side_effect=wrong_owner), mock.patch.object(os, "fchmod") as chmod:
            with self.assertRaisesRegex(prepare_browser.Failure, rf"/opt \(uid={os.geteuid() + 1}, mode=0777\)"):
                self.prepare()
            chmod.assert_not_called()
        self.assertEqual(stat.S_IMODE(opt.stat().st_mode), 0o777)

    def test_opt_symlink_is_rejected_without_touching_target(self):
        opt = self.root / "opt"
        target = self.root / "foreign-opt"
        opt.rename(target)
        target.chmod(0o777)
        opt.symlink_to(target, target_is_directory=True)
        with mock.patch.object(os, "fchmod") as chmod:
            with self.assertRaises(OSError):
                self.prepare()
            chmod.assert_not_called()
        self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o777)

    def test_opt_preserves_existing_other_mode_bits(self):
        opt = self.root / "opt"
        opt.chmod(0o1777)
        self.prepare()
        self.assertEqual(stat.S_IMODE(opt.stat().st_mode), 0o1755)
        self.assertFalse(opt.stat().st_mode & stat.S_ISUID)

    def test_opt_is_rechecked_after_content_verification(self):
        original = prepare_browser.verify_tree
        opt = self.root / "opt"
        def changed(*args, **kwargs):
            result = original(*args, **kwargs)
            if kwargs["repair"] is False:
                opt.chmod(0o777)
            return result
        with mock.patch.object(prepare_browser, "verify_tree", side_effect=changed):
            with self.assertRaisesRegex(prepare_browser.Failure, r"path: /opt \(uid=\d+, mode=0777\)"):
                self.prepare()

    def test_metadata_symlinks_and_hardlinks_are_rejected(self):
        original = self.md5_path.read_bytes()
        self.md5_path.unlink()
        target = self.root / "foreign-checksums"
        target.write_bytes(original)
        target.chmod(0o644)
        self.md5_path.symlink_to(target)
        with mock.patch.object(os, "fchmod") as chmod:
            with self.assertRaises((OSError, prepare_browser.Failure)):
                self.prepare()
            chmod.assert_not_called()
        self.md5_path.unlink()
        os.link(target, self.md5_path)
        with self.assertRaises(prepare_browser.Failure):
            self.prepare()

    def test_bad_hash_fails_after_tightening_without_executing_content(self):
        (self.tree / "chrome").write_bytes(b"changed contents must never execute")
        with self.assertRaisesRegex(prepare_browser.Failure, "checksum differs"):
            self.prepare()
        self.assertEqual(stat.S_IMODE((self.tree / "chrome").stat().st_mode), 0o755)

    def test_extra_missing_and_uncovered_files_fail_closed(self):
        extra = self.tree / "unregistered.so"
        extra.write_bytes(b"unregistered")
        with self.assertRaisesRegex(prepare_browser.Failure, "Unregistered"):
            self.prepare()
        extra.unlink()
        missing = self.tree / "libEGL.so"
        missing.unlink()
        with self.assertRaisesRegex(prepare_browser.Failure, "missing or unexpected"):
            self.prepare()
        missing.write_bytes(self.files["/opt/google/chrome/libEGL.so"])
        self.digests.pop("/opt/google/chrome/libEGL.so")
        self.write_metadata()
        with self.assertRaisesRegex(prepare_browser.Failure, "lacks checksum"):
            self.prepare()

    def test_escape_and_contained_symlinks_are_both_rejected(self):
        link = self.tree / "libEGL.so"
        link.unlink()
        foreign = self.root / "outside-library"
        foreign.write_bytes(b"outside")
        foreign.chmod(0o666)
        for target in (foreign, self.tree / "chrome"):
            with self.subTest(target=target.name):
                link.symlink_to(target)
                with self.assertRaisesRegex(prepare_browser.Failure, "symlink"):
                    self.prepare()
                link.unlink()
        self.assertEqual(stat.S_IMODE(foreign.stat().st_mode), 0o666)

    def test_hardlinks_cannot_change_foreign_inode_permissions(self):
        foreign = self.root / "outside-hardlink"
        os.link(self.tree / "chrome", foreign)
        before = stat.S_IMODE(foreign.stat().st_mode)
        with self.assertRaisesRegex(prepare_browser.Failure, "hardlinks"):
            self.prepare()
        self.assertEqual(stat.S_IMODE(foreign.stat().st_mode), before)

    def test_special_files_and_nonexecutables_are_not_repaired_into_executables(self):
        library = self.tree / "libEGL.so"
        library.unlink()
        os.mkfifo(library)
        with self.assertRaisesRegex(prepare_browser.Failure, "special file"):
            self.prepare()
        library.unlink()
        library.write_bytes(self.files["/opt/google/chrome/libEGL.so"])
        (self.tree / "chrome").chmod(0o644)
        with self.assertRaisesRegex(prepare_browser.Failure, "not executable"):
            self.prepare()
        self.assertEqual(stat.S_IMODE((self.tree / "chrome").stat().st_mode), 0o644)

    def test_parser_rejects_duplicates_traversal_and_missing_core_coverage(self):
        good_md5, good_list = self.md5_path.read_bytes(), self.list_path.read_bytes()
        for bad in (good_md5 + good_md5.splitlines()[0] + b"\n", b"0" * 32 + b"  ../../etc/passwd\n", b"0" * 32 + b"  /opt/google/chrome/chrome\n", b"not-checksums\n"):
            with self.subTest(bad=bad[:40]), self.assertRaises(prepare_browser.Failure):
                prepare_browser.parse_metadata(bad, good_list)
        self.digests.pop("/opt/google/chrome/chrome-sandbox")
        self.write_metadata()
        with self.assertRaisesRegex(prepare_browser.Failure, "Core Chrome"):
            self.prepare()

    def test_metadata_size_file_size_and_total_bounds(self):
        with mock.patch.object(prepare_browser, "MAX_METADATA", 8):
            with self.assertRaisesRegex(prepare_browser.Failure, "metadata size"):
                self.prepare()
        with mock.patch.object(prepare_browser, "MAX_FILE", 8):
            with self.assertRaisesRegex(prepare_browser.Failure, "bounded size"):
                self.prepare()
        with mock.patch.object(prepare_browser, "MAX_TOTAL", 8):
            with self.assertRaisesRegex(prepare_browser.Failure, "bounded total"):
                self.prepare()
        with mock.patch.object(prepare_browser, "MAX_FILES", 4):
            with self.assertRaisesRegex(prepare_browser.Failure, "bounded file count"):
                self.prepare()

    def test_protected_metadata_cannot_change_during_repair(self):
        original = prepare_browser.verify_tree
        def changed(*args, **kwargs):
            result = original(*args, **kwargs)
            if kwargs["repair"] is False:
                self.md5_path.write_bytes(self.md5_path.read_bytes() + b"\n")
            return result
        with mock.patch.object(prepare_browser, "verify_tree", side_effect=changed):
            with self.assertRaisesRegex(prepare_browser.Failure, "metadata changed"):
                self.prepare()

    def test_wrong_owner_is_rejected_without_chown(self):
        with mock.patch.object(prepare_browser, "ROOT_UID", os.geteuid() + 1), mock.patch.object(os, "fchmod") as chmod:
            with self.assertRaises(prepare_browser.Failure):
                self.prepare()
            chmod.assert_not_called()

    def test_main_rejects_arguments_and_non_hosted_context_before_mutation(self):
        with mock.patch.object(prepare_browser.sys, "argv", ["prepare-browser.py", "--path", "/tmp"]):
            with self.assertRaisesRegex(prepare_browser.Failure, "no arguments"):
                prepare_browser.main()
        with mock.patch.object(prepare_browser.sys, "argv", ["prepare-browser.py"]), mock.patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(prepare_browser.Failure, "GitHub-hosted"):
                prepare_browser.main()


class PackageIdentityTest(unittest.TestCase):
    def test_fixed_executable_metadata_fd_and_clean_environment(self):
        response = SimpleNamespace(returncode=0, stdout=b"install ok installed\tamd64\tgoogle-chrome-stable\t141.0.0.1-1\n")
        with mock.patch.object(subprocess, "run", return_value=response) as run:
            self.assertEqual(prepare_browser.package_identity(1, 11, 12), "141.0.0.1-1")
        args, options = run.call_args
        self.assertEqual(args[0][1], "--admindir=/proc/self/fd/11")
        self.assertEqual(args[0][-1], "google-chrome-stable")
        self.assertEqual(options["executable"], "/proc/self/fd/12")
        self.assertEqual(options["pass_fds"], (11, 12))
        self.assertEqual(options["env"], {"PATH": "/usr/bin:/bin", "LC_ALL": "C"})
        self.assertEqual(options["timeout"], 15)

    def test_query_failure_wrong_architecture_package_and_status(self):
        for output in (b"install ok installed\tarm64\tgoogle-chrome-stable\t1.2-1\n",
                       b"install ok installed\tamd64\tother-chrome\t1.2-1\n",
                       b"deinstall ok config-files\tamd64\tgoogle-chrome-stable\t1.2-1\n",
                       b"malformed\n"):
            with self.subTest(output=output), mock.patch.object(subprocess, "run", return_value=SimpleNamespace(returncode=0, stdout=output)):
                with self.assertRaises(prepare_browser.Failure):
                    prepare_browser.package_identity(1, 11, 12)
        with mock.patch.object(subprocess, "run", return_value=SimpleNamespace(returncode=1, stdout=b"")):
            with self.assertRaises(prepare_browser.Failure):
                prepare_browser.package_identity(1, 11, 12)


if __name__ == "__main__":
    unittest.main()
