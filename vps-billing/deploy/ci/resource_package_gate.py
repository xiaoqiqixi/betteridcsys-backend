#!/usr/bin/env python3
"""Bounded root half of check-resource-package.sh; verify only, never install."""
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import tempfile

PREFIX = "/run/aster-resource-package-gate."
FILES = frozenset({"resource.tar.gz", "plugin-manager.py"})
MAX_BYTES = 128 * 1024 * 1024


def reject(message):
    raise RuntimeError(message)


def protected_directory(path, identity=None):
    if not re.fullmatch(re.escape(PREFIX) + r"[a-z0-9_]{8}", path):
        reject("Unexpected protected verification directory.")
    for parent in ("/", "/run"):
        info = os.lstat(parent)
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            reject("Unprotected verification ancestor.")
    info = os.lstat(path)
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or
            stat.S_IMODE(info.st_mode) != 0o700 or
            identity is not None and (info.st_dev, info.st_ino) != identity):
        reject("Protected verification directory was replaced or has unsafe permissions.")
    return info.st_dev, info.st_ino


def copy_verified(source, digest, target, descriptor):
    if target not in FILES or not re.fullmatch(r"[0-9a-f]{64}", digest):
        reject("Invalid verification input.")
    if not os.path.isabs(source) or any(part in (".", "..") for part in source.split("/")):
        reject("Input must be an absolute canonical path.")
    # Refuse symlinks in every component, not merely the final file.
    path = Path(source)
    for parent in reversed(path.parents):
        info = os.lstat(parent)
        if not stat.S_ISDIR(info.st_mode):
            reject("Input ancestor is not a regular directory.")
    source_fd = os.open(source, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        before = os.fstat(source_fd)
        if not stat.S_ISREG(before.st_mode) or not 0 < before.st_size <= MAX_BYTES:
            reject("Input is not a bounded regular file.")
        chunks, total = [], 0
        while True:
            chunk = os.read(source_fd, min(1024 * 1024, MAX_BYTES + 1 - total))
            if not chunk:
                break
            chunks.append(chunk)
            total += len(chunk)
            if total > MAX_BYTES:
                reject("Input size changed beyond the verification limit.")
        raw = b"".join(chunks)
        after = os.fstat(source_fd)
        if ((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns) !=
                (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns) or
                hashlib.sha256(raw).hexdigest() != digest):
            reject("Input changed or SHA256 verification failed.")
    finally:
        os.close(source_fd)
    destination = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=descriptor)
    with os.fdopen(destination, "wb") as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())


def cleanup(path, identity):
    protected_directory(path, identity)
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        info = os.fstat(descriptor)
        if (info.st_dev, info.st_ino) != identity:
            reject("Refusing cleanup of replaced verification directory.")
        names = os.listdir(descriptor)
        if not set(names).issubset(FILES):
            reject("Refusing cleanup of unexpected verification contents.")
        for name in names:
            info = os.stat(name, dir_fd=descriptor, follow_symlinks=False)
            if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1:
                reject("Refusing cleanup of unsafe verification content.")
        for name in names:
            os.unlink(name, dir_fd=descriptor)
        protected_directory(path, identity)
        os.rmdir(path)
    finally:
        os.close(descriptor)


def interrupted(signum, _frame):
    raise RuntimeError("Protected verification interrupted by signal " + str(signum))


def main(arguments):
    if len(arguments) != 4:
        reject("Expected package path/hash and manager path/hash.")
    if sys.platform != "linux" or os.geteuid() != 0:
        reject("Protected resource package verification requires Linux root; not executed.")
    # Validate /run BEFORE creating anything. Do not follow a replaced /run.
    for parent in ("/", "/run"):
        info = os.lstat(parent)
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            reject("Unprotected verification ancestor.")
    for number in (signal.SIGTERM, signal.SIGINT):
        signal.signal(number, interrupted)
    os.umask(0o077)
    path = tempfile.mkdtemp(prefix="aster-resource-package-gate.", dir="/run")
    identity = protected_directory(path)
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        try:
            copy_verified(arguments[0], arguments[1], "resource.tar.gz", descriptor)
            copy_verified(arguments[2], arguments[3], "plugin-manager.py", descriptor)
        finally:
            os.close(descriptor)
        protected_directory(path, identity)
        result = subprocess.run(
            ["/usr/bin/python3", "-I", path + "/plugin-manager.py", "verify",
             path + "/resource.tar.gz", "--sha256", arguments[1]],
            cwd=path, env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8"},
            capture_output=True, text=True, timeout=60, check=True,
        )
        data = json.loads(result.stdout)
        if (data.get("verified") is not True or data.get("id") != "resource.aster-cloud" or
                data.get("version") != "0.1.1" or data.get("archive_sha256") != arguments[1] or
                data.get("service_action") != "none"):
            reject("Production manager returned an unexpected verification result.")
        print(json.dumps(data, sort_keys=True), flush=True)
    finally:
        # No rm -rf and no caller-selected destination. A changed inode, symlink,
        # unexpected file or permission causes failure instead of broad deletion.
        cleanup(path, identity)


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        # Do not forward manager stderr or dump environment/credentials.
        detail = str(error) if isinstance(error, RuntimeError) else type(error).__name__
        print("FAIL: protected resource package verification: " + detail, file=sys.stderr)
        raise SystemExit(1)
