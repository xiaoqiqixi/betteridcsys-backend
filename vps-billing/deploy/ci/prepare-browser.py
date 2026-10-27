#!/usr/bin/env python3
"""Tighten the installed Chrome tree and its fixed /opt ancestors in hosted CI.

Protected dpkg metadata supplies a local installed-content consistency check,
not a fresh package signature or download. No browser is executed, installed or
reconfigured. Existing owner/execute/set-ID bits are preserved; only group and
other write permissions are removed. Any unexpected tree entry fails closed.
"""
import hashlib
import json
import os
from pathlib import PurePosixPath
import re
import stat
import subprocess
import sys
import time


ROOT_UID = 0
PACKAGE = "google-chrome-stable"
TREE = "/opt/google/chrome"
CORE = {TREE + "/chrome", TREE + "/chrome-sandbox", TREE + "/google-chrome"}
MAX_METADATA = 4 * 1024 * 1024
MAX_STATUS = 64 * 1024 * 1024
MAX_FILES = 2048
MAX_FILE = 512 * 1024 * 1024
MAX_TOTAL = 2 * 1024 * 1024 * 1024
MAX_SECONDS = 180
DIRECTORY_FLAGS = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
FILE_FLAGS = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC


class Failure(Exception):
    pass


def same_inode(first, second):
    return (first.st_dev, first.st_ino, first.st_uid, first.st_gid) == (second.st_dev, second.st_ino, second.st_uid, second.st_gid)


def path_state(name, info):
    return f"{name[:512]} (uid={info.st_uid}, mode={stat.S_IMODE(info.st_mode):04o})"


def protected(info, name, directory=False):
    expected = stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)
    if (not expected or info.st_uid != ROOT_UID or info.st_mode & 0o022
            or (not directory and info.st_nlink != 1)):
        raise Failure("Unprotected installed-package path: " + path_state(name, info))


def tighten(descriptor, name, directory=False):
    before = os.fstat(descriptor)
    expected = stat.S_ISDIR(before.st_mode) if directory else stat.S_ISREG(before.st_mode)
    if not expected or before.st_uid != ROOT_UID or (not directory and before.st_nlink != 1):
        raise Failure("Unsafe Chrome ownership, type or hardlink: " + path_state(name, before))
    old_mode = stat.S_IMODE(before.st_mode)
    new_mode = old_mode & ~0o022
    if new_mode != old_mode:
        try:
            os.fchmod(descriptor, new_mode)
        except OSError:
            raise Failure("Cannot tighten Chrome path: " + path_state(name, before)) from None
    after = os.fstat(descriptor)
    if not same_inode(before, after) or stat.S_IMODE(after.st_mode) != new_mode or after.st_nlink != before.st_nlink:
        raise Failure("Chrome identity or mode changed during tightening: " + path_state(name, after))
    return new_mode != old_mode


def open_directory(parent, component, name, repair=False):
    descriptor = os.open(component, DIRECTORY_FLAGS, dir_fd=parent)
    try:
        if repair:
            changed = tighten(descriptor, name, directory=True)
        else:
            protected(os.fstat(descriptor), name, directory=True)
            changed = False
        return descriptor, changed
    except BaseException:
        os.close(descriptor)
        raise


def open_path(root, components):
    current = os.dup(root)
    try:
        for index, component in enumerate(components):
            descriptor, _ = open_directory(current, component, "/" + "/".join(components[:index+1]))
            os.close(current)
            current = descriptor
        return current
    except BaseException:
        os.close(current)
        raise


def read_protected(parent, component, name, limit):
    descriptor = os.open(component, FILE_FLAGS, dir_fd=parent)
    try:
        before = os.fstat(descriptor)
        protected(before, name)
        if before.st_size < 1 or before.st_size > limit:
            raise Failure("Installed-package metadata size is invalid")
        with os.fdopen(os.dup(descriptor), "rb") as stream:
            raw = stream.read(limit + 1)
        after = os.fstat(descriptor)
        if (not same_inode(before, after) or len(raw) != before.st_size
                or before.st_mtime_ns != after.st_mtime_ns or before.st_ctime_ns != after.st_ctime_ns):
            raise Failure("Installed-package metadata changed during verification")
        return raw
    finally:
        os.close(descriptor)


def canonical_path(value, absolute):
    if (not re.fullmatch(r"[A-Za-z0-9_+./-]{1,512}", value)
            or value.startswith("/") != absolute
            or any(component in ("", ".", "..") for component in value.split("/")[1 if absolute else 0:])):
        raise Failure("Noncanonical installed-package path")
    path = str(PurePosixPath(value))
    if path != value:
        raise Failure("Noncanonical installed-package path")
    return value if absolute else "/" + value


def parse_metadata(md5_raw, list_raw):
    try:
        md5_text, list_text = md5_raw.decode("ascii"), list_raw.decode("ascii")
    except UnicodeDecodeError:
        raise Failure("Installed-package metadata must be bounded ASCII") from None
    digests = {}
    for line in md5_text.splitlines():
        match = re.fullmatch(r"([a-f0-9]{32})  ([^ ]+)", line)
        if not match:
            raise Failure("Malformed installed-package checksum entry")
        path = canonical_path(match[2], absolute=False)
        if path in digests:
            raise Failure("Duplicate installed-package checksum path")
        digests[path] = match[1]
    registered = set()
    for line in list_text.splitlines():
        # dpkg's root directory entry is conventionally '/.'. It is never a
        # mutation target; every actual package pathname remains canonical.
        path = "/" if line == "/." else canonical_path(line, absolute=True)
        if path in registered:
            raise Failure("Duplicate installed-package file-list path")
        registered.add(path)
    if len(digests) > MAX_FILES * 4 or len(registered) > MAX_FILES * 4:
        raise Failure("Installed-package metadata has too many entries")
    if not digests or not registered or not set(digests).issubset(registered):
        raise Failure("Installed checksum paths are absent from package file list")
    if "/opt" not in registered:
        raise Failure("Chrome package file list does not register /opt")
    selected = {path: digest for path, digest in digests.items() if path.startswith(TREE + "/")}
    members = {path for path in registered if path == TREE or path.startswith(TREE + "/")}
    if not CORE.issubset(selected) or TREE not in members or "/opt/google" not in registered:
        raise Failure("Core Chrome package files are not covered by installed checksums")
    if not selected or len(members) > MAX_FILES:
        raise Failure("Chrome package tree exceeds its bounded file count")
    return selected, members


def package_identity(root, dpkg, query):
    # The executable and its ancestors have already been verified. Execute the
    # pinned open file and give dpkg-query only the pinned metadata directory;
    # inherited DPKG_ROOT/ADMINDIR and all app/credential variables are excluded.
    result = subprocess.run(
        ["dpkg-query", "--admindir=/proc/self/fd/" + str(dpkg), "--show",
         "--showformat=${Status}\t${Architecture}\t${binary:Package}\t${Version}\n", PACKAGE],
        executable="/proc/self/fd/" + str(query), pass_fds=(dpkg, query),
        env={"PATH": "/usr/bin:/bin", "LC_ALL": "C"}, cwd="/",
        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        timeout=15, check=False)
    if result.returncode != 0 or len(result.stdout) > 4096:
        raise Failure("Cannot verify installed Chrome package identity")
    try:
        status, architecture, package, version = result.stdout.decode("ascii").strip().split("\t")
    except (UnicodeDecodeError, ValueError):
        raise Failure("Malformed installed Chrome package identity") from None
    if (status != "install ok installed" or architecture != "amd64" or package != PACKAGE
            or not re.fullmatch(r"[0-9][A-Za-z0-9.+:~_-]{0,127}", version)):
        raise Failure("Expected installed amd64 google-chrome-stable package")
    return version


def file_digest(descriptor, name, deadline):
    before = os.fstat(descriptor)
    protected(before, name)
    if before.st_size < 0 or before.st_size > MAX_FILE:
        raise Failure("Chrome file exceeds its bounded size")
    digest = hashlib.md5(usedforsecurity=False)
    size = 0
    while True:
        if time.monotonic() > deadline:
            raise Failure("Chrome consistency check exceeded its deadline")
        chunk = os.read(descriptor, 1024 * 1024)
        if not chunk:
            break
        size += len(chunk)
        if size > before.st_size or size > MAX_FILE:
            raise Failure("Chrome file grew during verification")
        digest.update(chunk)
    after = os.fstat(descriptor)
    if (not same_inode(before, after) or size != before.st_size
            or before.st_mtime_ns != after.st_mtime_ns or before.st_ctime_ns != after.st_ctime_ns
            or before.st_mode != after.st_mode or before.st_nlink != after.st_nlink):
        raise Failure("Chrome file changed during verification")
    return digest.hexdigest(), size


def verify_tree(chrome, expected, members, deadline, repair):
    seen = {TREE}
    files = set()
    total = 0
    tightened = 0

    def walk(directory, prefix, depth):
        nonlocal total, tightened
        if depth > 16 or time.monotonic() > deadline:
            raise Failure("Chrome tree exceeds its bounded depth or deadline")
        names = []
        with os.scandir(directory) as entries:
            for entry in entries:
                if len(names) + len(seen) >= MAX_FILES:
                    raise Failure("Chrome tree exceeds its bounded file count")
                names.append(entry.name)
        for name in sorted(names):
            path = canonical_path(prefix + "/" + name, absolute=True)
            if path not in members:
                raise Failure("Unregistered Chrome tree entry: " + path)
            info = os.stat(name, dir_fd=directory, follow_symlinks=False)
            if info.st_uid != ROOT_UID:
                raise Failure("Chrome tree entry is not root-owned: " + path)
            seen.add(path)
            if stat.S_ISDIR(info.st_mode):
                child, changed = open_directory(directory, name, path, repair)
                try:
                    if not same_inode(info, os.fstat(child)):
                        raise Failure("Chrome directory changed while opening: " + path)
                    tightened += changed
                    walk(child, path, depth + 1)
                finally:
                    os.close(child)
            elif stat.S_ISREG(info.st_mode):
                if path not in expected or info.st_nlink != 1:
                    raise Failure("Chrome file lacks checksum coverage or has hardlinks: " + path)
                descriptor = os.open(name, FILE_FLAGS, dir_fd=directory)
                try:
                    if not same_inode(info, os.fstat(descriptor)):
                        raise Failure("Chrome file changed while opening: " + path)
                    if total + os.fstat(descriptor).st_size > MAX_TOTAL:
                        raise Failure("Chrome tree exceeds its bounded total size")
                    if repair:
                        tightened += tighten(descriptor, path)
                    if path in CORE and not os.fstat(descriptor).st_mode & stat.S_IXUSR:
                        raise Failure("Core Chrome file is not executable; permissions will not be added")
                    digest, size = file_digest(descriptor, path, deadline)
                    if digest != expected[path]:
                        raise Failure("Installed Chrome content checksum differs: " + path)
                    total += size
                    if total > MAX_TOTAL:
                        raise Failure("Chrome tree exceeds its bounded total size")
                    files.add(path)
                finally:
                    os.close(descriptor)
            else:
                raise Failure("Unexpected Chrome symlink or special file: " + path)

    walk(chrome, TREE, 0)
    if files != set(expected) or seen != members:
        raise Failure("Installed Chrome tree has missing or unexpected package entries")
    return {"files": len(files), "bytes": total, "tightened_entries": tightened}


def prepare(root):
    protected(os.fstat(root), "/", directory=True)
    dpkg = open_path(root, ["var", "lib", "dpkg"])
    info = query = opt = google = chrome = None
    try:
        # Fail before changing any Chrome mode when the metadata is unprotected.
        status_before = read_protected(dpkg, "status", "/var/lib/dpkg/status", MAX_STATUS)
        info, _ = open_directory(dpkg, "info", "/var/lib/dpkg/info")
        md5_raw = read_protected(info, PACKAGE + ".md5sums", "/var/lib/dpkg/info/" + PACKAGE + ".md5sums", MAX_METADATA)
        list_raw = read_protected(info, PACKAGE + ".list", "/var/lib/dpkg/info/" + PACKAGE + ".list", MAX_METADATA)
        expected, members = parse_metadata(md5_raw, list_raw)
        binary_directory = open_path(root, ["usr", "bin"])
        try:
            query = os.open("dpkg-query", FILE_FLAGS, dir_fd=binary_directory)
            protected(os.fstat(query), "/usr/bin/dpkg-query")
            if not os.fstat(query).st_mode & stat.S_IXUSR:
                raise Failure("Protected dpkg-query executable is unavailable")
        finally:
            os.close(binary_directory)
        version = package_identity(root, dpkg, query)
        opt, opt_changed = open_directory(root, "opt", "/opt", repair=True)
        google, google_changed = open_directory(opt, "google", "/opt/google", repair=True)
        chrome, chrome_changed = open_directory(google, "chrome", TREE, repair=True)
        deadline = time.monotonic() + MAX_SECONDS
        first = verify_tree(chrome, expected, members, deadline, repair=True)
        final = verify_tree(chrome, expected, members, deadline, repair=False)
        protected(os.fstat(opt), "/opt", directory=True)
        protected(os.fstat(google), "/opt/google", directory=True)
        protected(os.fstat(chrome), TREE, directory=True)
        if (read_protected(dpkg, "status", "/var/lib/dpkg/status", MAX_STATUS) != status_before
                or read_protected(info, PACKAGE + ".md5sums", "installed Chrome checksums", MAX_METADATA) != md5_raw
                or read_protected(info, PACKAGE + ".list", "installed Chrome file list", MAX_METADATA) != list_raw):
            raise Failure("Installed-package metadata changed during Chrome preparation")
        return {"passed": True, "package": PACKAGE, "version": version, "architecture": "amd64",
                "files_verified": final["files"], "bytes_verified": final["bytes"],
                "tightened_entries": first["tightened_entries"] + opt_changed + google_changed + chrome_changed,
                "verification": "protected installed-package MD5 consistency; no new package authentication",
                "sandbox_changed": False, "browser_executed": False}
    finally:
        for descriptor in (chrome, google, opt, query, info, dpkg):
            if descriptor is not None:
                os.close(descriptor)


def main():
    if len(sys.argv) != 1:
        raise Failure("This fixed-scope runner preparation takes no arguments")
    if (os.geteuid() != 0 or sys.platform != "linux" or os.uname().machine != "x86_64"
            or os.environ.get("GITHUB_ACTIONS") != "true"
            or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted"):
        raise Failure("Only root preparation of a disposable GitHub-hosted Linux amd64 runner is supported")
    root = os.open("/", DIRECTORY_FLAGS)
    try:
        protected(os.fstat(root), "/", directory=True)
        release_directory = open_path(root, ["usr", "lib"])
        try:
            release = read_protected(release_directory, "os-release", "/usr/lib/os-release", 65536).decode("ascii")
        finally:
            os.close(release_directory)
        fields = dict(re.findall(r'^([A-Z_]+)="?([^"\n]*)"?$', release, re.MULTILINE))
        if fields.get("ID") != "ubuntu" or fields.get("VERSION_ID") not in {"22.04", "24.04"}:
            raise Failure("Only disposable Ubuntu 22.04/24.04 runner images are supported")
        print(json.dumps(prepare(root), sort_keys=True))
    finally:
        os.close(root)


if __name__ == "__main__":
    try:
        main()
    except Failure as error:
        print("FAIL: " + str(error), file=sys.stderr)
        raise SystemExit(1)
    except (OSError, ValueError, UnicodeError, subprocess.SubprocessError):
        print("FAIL: Chrome preparation encountered an unsafe or unavailable installed-package path", file=sys.stderr)
        raise SystemExit(1)
