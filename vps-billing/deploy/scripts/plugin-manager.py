#!/usr/bin/env python3
"""Install and explicitly approve runtime slots; never execute package contents.

Production trust is fixed to root. The private _Trust object is only useful to
unit tests invoking Python functions; the CLI has no relaxed-trust option.
"""
import argparse
import contextlib
import ctypes
import dataclasses
import fcntl
import hashlib
import ipaddress
import json
import os
import re
import secrets
import stat
import struct
import sys
import zlib

CORE_VERSION = "0.5.10"
PAYMENT_MIN_CORE_VERSION = "0.5.3"
RESOURCE_MIN_CORE_VERSION = "0.5.10"
FILES = frozenset({"plugin", "manifest.json", "LICENSE", "README.md", "THIRD_PARTY_NOTICES.json", "SHA256SUMS"})
MAX_ARCHIVE = 80 << 20
MAX_BINARY = 64 << 20
MAX_POLICY = 16 << 10
VERSION = re.compile(r"(?:0|[1-9][0-9]{0,5})\.(?:0|[1-9][0-9]{0,5})\.(?:0|[1-9][0-9]{0,5})\Z")
DIGEST = re.compile(r"[a-f0-9]{64}\Z")
MANIFEST_KEYS = {"schema", "id", "version", "protocol", "core_min", "core_max_exclusive", "capabilities", "executable", "sha256"}
SELECTION_KEYS = {"enabled", "id", "version", "manifest_sha256", "worker_uid", "client_uid", "socket", "allowed_targets"}
PAYMENT_ID = re.compile(r"payment\.[a-z][a-z0-9-]{0,47}\Z")
RESOURCE_ID = re.compile(r"resource\.[a-z][a-z0-9-]{0,47}\Z")
CONTROLLER_ID = re.compile(r"[a-z0-9][a-z0-9_-]{0,63}\Z")
MERCHANT_ID = re.compile(r"[!-~]{1,128}\Z")


def plugin_kind(identifier):
    if identifier == "email.smtp":
        return "smtp"
    if isinstance(identifier, str) and identifier != "payment.disabled" and PAYMENT_ID.fullmatch(identifier):
        return "payment"
    if isinstance(identifier, str) and RESOURCE_ID.fullmatch(identifier):
        return "resource"
    raise Rejected("Unsupported runtime plugin identity.")


def package_prefix(identifier):
    kind = plugin_kind(identifier)
    return "aster-smtp" if kind == "smtp" else "aster-" + kind + "-" + identifier.removeprefix(kind + ".")


class Rejected(Exception):
    """Safe fixed errors, with no archive payload or secret in diagnostics."""


@dataclasses.dataclass(frozen=True)
class _Trust:
    owner: int = 0
    ancestor: int = 0


@dataclasses.dataclass(frozen=True)
class Package:
    manifest: dict
    files: dict
    archive_sha256: str


def version_tuple(value):
    if not isinstance(value, str) or not VERSION.fullmatch(value):
        raise Rejected("Invalid stable semantic version.")
    return tuple(map(int, value.split(".")))


def absolute(path):
    if not isinstance(path, str) or len(path) > 1024 or not path.startswith("/") or path == "/" or os.path.normpath(path) != path or any(ord(c) < 32 or ord(c) == 127 for c in path):
        raise Rejected("A canonical absolute path is required.")
    return path


def same_file(left, right):
    return all(getattr(left, field) == getattr(right, field) for field in ("st_dev", "st_ino", "st_mode", "st_nlink", "st_uid", "st_gid", "st_size", "st_mtime_ns", "st_ctime_ns"))


def check_metadata(info, directory, trust, ancestor=False):
    owner_ok = info.st_uid == trust.owner or ancestor and info.st_uid == trust.ancestor
    sticky_ancestor = ancestor and directory and info.st_uid == trust.ancestor and info.st_mode & stat.S_ISVTX
    if not owner_ok or info.st_mode & 0o022 and not sticky_ancestor or info.st_mode & (stat.S_ISUID | stat.S_ISGID):
        raise Rejected("Installation paths must be protected and root-owned.")
    if directory and not stat.S_ISDIR(info.st_mode) or not directory and (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1):
        raise Rejected("Links and non-regular installation inputs are forbidden.")


def protected_open(path, directory, trust=_Trust()):
    absolute(path)
    descriptor = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    try:
        parts = path[1:].split("/")
        for index, part in enumerate(parts):
            is_directory = directory or index < len(parts) - 1
            flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK
            if is_directory:
                flags |= os.O_DIRECTORY
            child = os.open(part, flags, dir_fd=descriptor)
            os.close(descriptor)
            descriptor = child
            check_metadata(os.fstat(descriptor), is_directory, trust, index < len(parts) - 1)
        result, descriptor = descriptor, -1
        return result
    except OSError as exc:
        raise Rejected("Protected installation input could not be opened.") from exc
    finally:
        if descriptor >= 0:
            os.close(descriptor)


def read_protected(path, maximum, trust=_Trust()):
    descriptor = protected_open(path, False, trust)
    try:
        before = os.fstat(descriptor)
        if not 0 < before.st_size <= maximum:
            raise Rejected("Installation input size is invalid.")
        with os.fdopen(descriptor, "rb", closefd=False) as stream:
            raw = stream.read(maximum + 1)
        if len(raw) != before.st_size or not same_file(before, os.fstat(descriptor)):
            raise Rejected("Installation input changed during verification.")
        return raw
    finally:
        os.close(descriptor)


def strict_json(raw, maximum):
    if not 0 < len(raw) <= maximum:
        raise Rejected("JSON input size is invalid.")

    def pairs(items):
        result = {}
        for key, value in items:
            if not re.fullmatch(r"[a-z0-9_]{1,64}", key) or key in result or len(result) >= 64:
                raise Rejected("JSON contains unsupported keys.")
            result[key] = value
        return result

    def invalid_constant(_):
        raise Rejected("JSON contains an invalid number.")

    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=pairs, parse_constant=invalid_constant)
    except (UnicodeError, ValueError, RecursionError) as exc:
        raise Rejected("Invalid JSON input.") from exc

    def inspect(item, depth=0):
        if item is None or depth > 8 or isinstance(item, list) and len(item) > 64:
            raise Rejected("JSON exceeds supported bounds.")
        if isinstance(item, (list, dict)):
            for child in item.values() if isinstance(item, dict) else item:
                inspect(child, depth + 1)

    inspect(value)
    return value


def validate_manifest(value, core):
    if not isinstance(value, dict) or not MANIFEST_KEYS <= set(value):
        raise Rejected("Unsupported runtime manifest.")
    kind = plugin_kind(value["id"])
    expected_keys = MANIFEST_KEYS | ({"payment"} if kind == "payment" else set())
    capabilities = {"smtp": ["smtp.send"], "payment": ["payment.checkout", "payment.verify_callback"], "resource": ["resource.send", "resource.lookup"]}[kind]
    if set(value) != expected_keys or type(value["schema"]) is not int or value["schema"] != 1 or value["protocol"] != "aster." + kind + "/1" or value["capabilities"] != capabilities or value["executable"] != "plugin" or not isinstance(value["sha256"], str) or not DIGEST.fullmatch(value["sha256"]):
        raise Rejected("Unsupported runtime manifest.")
    if kind == "payment":
        validate_payment(value["payment"])
        if version_tuple(core) < version_tuple(PAYMENT_MIN_CORE_VERSION) or version_tuple(value["core_min"]) < version_tuple(PAYMENT_MIN_CORE_VERSION):
            raise Rejected("Payment runtime protocol requires Core 0.5.3 or newer.")
    if kind == "resource" and (version_tuple(core) < version_tuple(RESOURCE_MIN_CORE_VERSION) or version_tuple(value["core_min"]) < version_tuple(RESOURCE_MIN_CORE_VERSION)):
        raise Rejected("Resource runtime protocol requires Core 0.5.10 or newer.")
    version_tuple(value["version"])
    if not version_tuple(value["core_min"]) <= version_tuple(core) < version_tuple(value["core_max_exclusive"]):
        raise Rejected("Plugin is incompatible with the selected core version.")
    return value


def validate_payment(value):
    if not isinstance(value, dict) or set(value) != {"currencies", "checkout_hosts"} or not isinstance(value["currencies"], list) or not 1 <= len(value["currencies"]) <= 32 or not isinstance(value["checkout_hosts"], list) or not 1 <= len(value["checkout_hosts"]) <= 8:
        raise Rejected("Unsupported payment declarations.")
    codes, hosts = set(), set()
    for currency in value["currencies"]:
        if not isinstance(currency, dict) or set(currency) != {"code", "precision"} or not isinstance(currency["code"], str) or not re.fullmatch(r"[A-Z]{3}", currency["code"]) or currency["code"] in codes or type(currency["precision"]) is not int or not 0 <= currency["precision"] <= 6:
            raise Rejected("Unsupported payment currency declarations.")
        codes.add(currency["code"])
    for host in value["checkout_hosts"]:
        if not isinstance(host, str) or not 1 <= len(host) <= 253 or host in hosts or not all(re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", label) for label in host.split(".")):
            raise Rejected("Unsupported payment checkout host.")
        try:
            ipaddress.ip_address(host)
        except ValueError:
            pass
        else:
            raise Rejected("Payment checkout hosts must be DNS names.")
        hosts.add(host)
    return value


def validate_elf(raw):
    if not 64 <= len(raw) <= MAX_BINARY or raw[:7] != b"\x7fELF\x02\x01\x01" or raw[7] not in (0, 3):
        raise Rejected("A Linux amd64 static ELF executable is required.")
    kind, machine, revision, entry, programs, _, _, header_size, program_size, program_count = struct.unpack_from("<HHIQQQIHHH", raw, 16)
    if kind != 2 or machine != 62 or revision != 1 or header_size != 64 or program_size != 56 or not 1 <= program_count <= 128 or programs < 64 or programs + program_size * program_count > len(raw):
        raise Rejected("Invalid Linux amd64 ELF program headers.")
    executable_entry = False
    for index in range(program_count):
        segment, flags, offset, address, _, size, memory, alignment = struct.unpack_from("<IIQQQQQQ", raw, programs + index * program_size)
        if segment in (2, 3):
            raise Rejected("Dynamic executable loaders and dependencies are forbidden.")
        if offset + size > len(raw) or size > memory or alignment not in (0, 1) and alignment & (alignment - 1):
            raise Rejected("Invalid ELF segment bounds.")
        if segment == 1 and flags & 1 and address <= entry < address + memory:
            executable_entry = True
    if not executable_entry:
        raise Rejected("ELF entry point is not in an executable load segment.")


def tar_number(raw):
    value = raw.strip(b"\x00 ")
    if not value or not re.fullmatch(b"[0-7]+", value):
        raise Rejected("Unsupported archive numeric field.")
    return int(value, 8)


def unpack_archive(compressed, raw_resources=False):
    """Parse only bounded plain USTAR/GNU regular records; no extension records."""
    try:
        decoder = zlib.decompressobj(16 + zlib.MAX_WBITS)
        raw = decoder.decompress(compressed, MAX_ARCHIVE + 1)
        if len(raw) > MAX_ARCHIVE or not decoder.eof or decoder.unconsumed_tail or decoder.unused_data:
            raise Rejected("Archive expansion or gzip framing is invalid.")
    except zlib.error as exc:
        raise Rejected("Invalid gzip archive.") from exc
    expected_files = FILES - {"plugin"} if raw_resources else FILES
    suffix = "-resources" if raw_resources else ""
    files, root, offset, directory_seen = {}, None, 0, False
    while offset + 512 <= len(raw):
        block = raw[offset:offset + 512]
        if not any(block):
            if len(raw) - offset < 1024 or any(raw[offset:]):
                raise Rejected("Archive has invalid trailing records.")
            break
        if len(files) >= len(FILES) + 1 or block[257:263] not in (b"ustar\x00", b"ustar "):
            raise Rejected("Unsupported archive format.")
        expected = tar_number(block[148:156])
        if sum(block[:148]) + 8 * 32 + sum(block[156:]) != expected:
            raise Rejected("Archive header checksum is invalid.")
        name_bytes = block[:100].split(b"\x00", 1)[0]
        prefix = block[345:500].split(b"\x00", 1)[0] if block[257:263] == b"ustar\x00" else b""
        if prefix:
            name_bytes = prefix + b"/" + name_bytes
        try:
            name = name_bytes.decode("ascii")
        except UnicodeError as exc:
            raise Rejected("Archive paths must be ASCII.") from exc
        kind, size, mode = block[156:157], tar_number(block[124:136]), tar_number(block[100:108])
        if tar_number(block[108:116]) != 0 or tar_number(block[116:124]) != 0 or any(block[157:257]) or kind not in (b"0", b"\x00", b"5"):
            raise Rejected("Archive links, extensions and special files are forbidden.")
        parts = name.rstrip("/").split("/")
        if not 1 <= len(parts) <= 2 or any(part in ("", ".", "..") for part in parts) or "\\" in name:
            raise Rejected("Archive contains an unsafe path.")
        if root is None:
            root = parts[0]
        if parts[0] != root or not re.fullmatch(r"aster-(?:smtp|(?:payment|resource)-[a-z][a-z0-9-]{0,47})-(?:0|[1-9][0-9]{0,5})\.(?:0|[1-9][0-9]{0,5})\.(?:0|[1-9][0-9]{0,5})-linux-amd64" + suffix, root):
            raise Rejected("Archive root does not identify a runtime package.")
        data_start = offset + 512
        offset = data_start + (size + 511) // 512 * 512
        if offset > len(raw):
            raise Rejected("Archive member extends past its bounds.")
        if kind == b"5":
            if len(parts) != 1 or name not in (root, root + "/") or directory_seen or size != 0 or mode != 0o755:
                raise Rejected("Unsupported archive directory.")
            directory_seen = True
            continue
        if len(parts) != 2 or name.endswith("/") or parts[1] not in expected_files or parts[1] in files or mode != (0o755 if parts[1] == "plugin" else 0o644):
            raise Rejected("Archive inventory or file mode is invalid.")
        maximum = {"plugin": MAX_BINARY, "manifest.json": 8192, "THIRD_PARTY_NOTICES.json": 4 << 20}.get(parts[1], 256 << 10)
        if not 0 < size <= maximum:
            raise Rejected("Archive member exceeds its size limit.")
        files[parts[1]] = raw[data_start:data_start + size]
    else:
        raise Rejected("Archive is missing terminal records.")
    if not directory_seen or set(files) != expected_files:
        raise Rejected("Archive must contain the exact declared runtime inventory.")
    return root, files


def verify_package(path, expected, core=CORE_VERSION, trust=_Trust(), *, binary=None, binary_sha256=None):
    if not isinstance(expected, str) or not DIGEST.fullmatch(expected):
        raise Rejected("An explicit lowercase SHA-256 is required.")
    raw_resources = binary is not None or binary_sha256 is not None
    if raw_resources and (not isinstance(binary, str) or not binary or not isinstance(binary_sha256, str) or not DIGEST.fullmatch(binary_sha256)):
        raise Rejected("Both a protected raw binary path and its explicit SHA-256 are required.")
    compressed = read_protected(path, MAX_ARCHIVE, trust)
    if hashlib.sha256(compressed).hexdigest() != expected:
        raise Rejected("Archive SHA-256 does not match the approved value.")
    root, files = unpack_archive(compressed, raw_resources)
    if raw_resources:
        if any(value.startswith(b"\x7fELF") for value in files.values()):
            raise Rejected("Resource archives cannot contain ELF executables.")
        raw_binary = read_protected(binary, MAX_BINARY, trust)
        if hashlib.sha256(raw_binary).hexdigest() != binary_sha256:
            raise Rejected("Raw binary SHA-256 does not match the approved value.")
        validate_elf(raw_binary)
        files["plugin"] = raw_binary
    manifest = validate_manifest(strict_json(files["manifest.json"], 8192), core)
    if root != package_prefix(manifest["id"]) + "-" + manifest["version"] + "-linux-amd64" + ("-resources" if raw_resources else ""):
        raise Rejected("Manifest version does not match archive root.")
    expected_lines = {}
    try:
        for line in files["SHA256SUMS"].decode("ascii").splitlines():
            match = re.fullmatch(r"([a-f0-9]{64})  ([A-Za-z0-9_.]+)", line)
            if not match or match[2] in expected_lines:
                raise Rejected("Internal checksum inventory is invalid.")
            expected_lines[match[2]] = match[1]
    except UnicodeError as exc:
        raise Rejected("Internal checksums must be ASCII.") from exc
    if set(expected_lines) != FILES - {"SHA256SUMS"} or any(hashlib.sha256(files[name]).hexdigest() != digest for name, digest in expected_lines.items()) or hashlib.sha256(files["plugin"]).hexdigest() != manifest["sha256"]:
        raise Rejected("Package member hashes do not match.")
    validate_elf(files["plugin"])
    try:
        for name in ("LICENSE", "README.md"):
            if not files[name].decode("utf-8").strip():
                raise ValueError()
        notices = json.loads(files["THIRD_PARTY_NOTICES.json"].decode("utf-8"))
        if not isinstance(notices, (dict, list)) or not notices:
            raise ValueError()
    except (UnicodeError, ValueError, RecursionError) as exc:
        raise Rejected("Package documentation and dependency notices are invalid.") from exc
    return Package(manifest, files, expected)


def open_directory(path, trust=_Trust(), create=False):
    try:
        return protected_open(path, True, trust)
    except Rejected:
        if not create:
            raise
    parent, name = os.path.split(absolute(path))
    descriptor = protected_open(parent, True, trust)
    try:
        try:
            os.mkdir(name, 0o755, dir_fd=descriptor)
            created = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=descriptor)
            try:
                os.fchmod(created, 0o755)
                os.fsync(descriptor)
            finally:
                os.close(created)
        except FileExistsError:
            pass
    finally:
        os.close(descriptor)
    return protected_open(path, True, trust)


@contextlib.contextmanager
def locked(directory, name, trust):
    descriptor = os.open(name, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, 0o600, dir_fd=directory)
    try:
        check_metadata(os.fstat(descriptor), False, trust)
        fcntl.flock(descriptor, fcntl.LOCK_EX)
        yield
    finally:
        os.close(descriptor)


def rename_new(source_dir, source, destination_dir, destination):
    """Linux renameat2 prevents replacing even an empty existing version."""
    library = ctypes.CDLL(None, use_errno=True)
    function = getattr(library, "renameat2", None)
    if function is None:
        raise Rejected("This Linux host lacks atomic no-overwrite installation.")
    function.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
    function.restype = ctypes.c_int
    if function(source_dir, source.encode("ascii"), destination_dir, destination.encode("ascii"), 1) != 0:
        raise Rejected("An installed version cannot be overwritten.")


def install_package(root, package, trust=_Trust()):
    descriptor = open_directory(root, trust, create=True)
    stage = ".install-" + secrets.token_hex(16)
    stage_fd, id_fd, installed = -1, -1, False
    try:
        with locked(descriptor, ".manager.lock", trust):
            identifier = package.manifest["id"]
            created_id = False
            try:
                os.mkdir(identifier, 0o755, dir_fd=descriptor)
                created_id = True
            except FileExistsError:
                pass
            id_fd = os.open(identifier, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=descriptor)
            check_metadata(os.fstat(id_fd), True, trust)
            if created_id:
                os.fchmod(id_fd, 0o755)
            os.mkdir(stage, 0o700, dir_fd=descriptor)
            stage_fd = os.open(stage, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=descriptor)
            for name, raw in sorted(package.files.items()):
                file_fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=stage_fd)
                with os.fdopen(file_fd, "wb") as stream:
                    stream.write(raw)
                    stream.flush()
                    os.fchmod(stream.fileno(), 0o555 if name == "plugin" else 0o444)
                    os.fsync(stream.fileno())
            os.fsync(stage_fd)
            rename_new(descriptor, stage, id_fd, package.manifest["version"])
            installed = True
            # Keep the directory private until publication; moving it between
            # parents requires directory write permission for nonroot fixtures.
            # The manager lock prevents selection before final protection.
            os.fchmod(stage_fd, 0o555)
            os.fsync(stage_fd)
            os.fsync(id_fd)
            os.fsync(descriptor)
    except OSError as exc:
        raise Rejected("Protected plugin installation failed.") from exc
    finally:
        if stage_fd >= 0:
            if not installed:
                os.fchmod(stage_fd, 0o700)
                for name in FILES:
                    try:
                        os.unlink(name, dir_fd=stage_fd)
                    except FileNotFoundError:
                        pass
            os.close(stage_fd)
        if not installed:
            try:
                os.rmdir(stage, dir_fd=descriptor)
            except FileNotFoundError:
                pass
        if id_fd >= 0:
            os.close(id_fd)
        os.close(descriptor)
    return {"id": package.manifest["id"], "version": package.manifest["version"], "installed": True, "enabled": False, "service_action": "none"}


def validate_targets(targets):
    if not isinstance(targets, list) or len(targets) > 8:
        raise Rejected("At most eight explicit plugin destinations are allowed.")
    seen = set()
    for item in targets:
        if not isinstance(item, dict) or set(item) != {"host", "port"} or not isinstance(item["host"], str) or not 0 < len(item["host"]) <= 253 or any(ord(c) < 32 or ord(c) == 127 or c in "/\\ " for c in item["host"]) or type(item["port"]) is not int or not 1 <= item["port"] <= 65535:
            raise Rejected("Plugin destination is invalid.")
        pair = (item["host"], item["port"])
        if pair in seen:
            raise Rejected("Duplicate plugin destinations are forbidden.")
        seen.add(pair)
    return targets


def validate_policy(value):
    if not isinstance(value, dict) or not {"schema", "revision"} <= set(value) <= {"schema", "revision", "smtp", "payment", "resource"} or type(value["schema"]) is not int or value["schema"] != 1 or type(value["revision"]) is not int or not 1 <= value["revision"] <= (1 << 64) - 1:
        raise Rejected("Unsupported plugin policy.")
    for kind in ("smtp", "payment", "resource"):
        if kind not in value:
            continue
        selected = value[kind]
        expected_keys = SELECTION_KEYS | ({"merchant_id"} if kind == "payment" else {"controller_id"} if kind == "resource" else set())
        if not isinstance(selected, dict) or set(selected) != expected_keys or type(selected["enabled"]) is not bool or plugin_kind(selected["id"]) != kind or not isinstance(selected["manifest_sha256"], str) or not DIGEST.fullmatch(selected["manifest_sha256"]):
            raise Rejected("Unsupported runtime selection.")
        if kind == "payment" and (not isinstance(selected["merchant_id"], str) or not MERCHANT_ID.fullmatch(selected["merchant_id"])):
            raise Rejected("Payment requires an explicit non-secret merchant identity.")
        if kind == "resource" and (not isinstance(selected["controller_id"], str) or not CONTROLLER_ID.fullmatch(selected["controller_id"])):
            raise Rejected("Resource requires an explicit controller identity.")
        version_tuple(selected["version"])
        if any(type(selected[key]) is not int or not (0 if kind == "resource" and key == "client_uid" else 1) <= selected[key] <= (1 << 32) - 1 for key in ("worker_uid", "client_uid")) or selected["worker_uid"] == selected["client_uid"] or kind == "resource" and selected["client_uid"] != 0:
            raise Rejected("Separate non-root worker and API identities are required.")
        absolute(selected["socket"])
        if len(selected["socket"]) > 100:
            raise Rejected("Plugin socket path is too long.")
        validate_targets(selected["allowed_targets"])
        if selected["enabled"] and not selected["allowed_targets"]:
            raise Rejected("Enabling a plugin requires an explicit destination.")
    if "smtp" in value and "payment" in value:
        smtp, payment = value["smtp"], value["payment"]
        if smtp["worker_uid"] == payment["worker_uid"] or smtp["worker_uid"] == payment["client_uid"] or payment["worker_uid"] == smtp["client_uid"] or smtp["socket"] == payment["socket"]:
            raise Rejected("Runtime slots require separate workers and sockets.")
    if "resource" in value:
        resource = value["resource"]
        for kind in ("smtp", "payment"):
            other = value.get(kind)
            if other and (resource["worker_uid"] in (other["worker_uid"], other["client_uid"]) or resource["socket"] == other["socket"]):
                raise Rejected("Resource requires a separate worker and socket.")
    return value


def installed_manifest(root, identifier, version, core, trust):
    plugin_kind(identifier)
    version_tuple(version)
    base = absolute(root) + "/" + identifier + "/" + version
    raw = read_protected(base + "/manifest.json", 8192, trust)
    manifest = validate_manifest(strict_json(raw, 8192), core)
    if manifest["id"] != identifier or manifest["version"] != version:
        raise Rejected("Installed manifest identity does not match its directory.")
    executable = read_protected(base + "/plugin", MAX_BINARY, trust)
    if hashlib.sha256(executable).hexdigest() != manifest["sha256"]:
        raise Rejected("Installed executable hash does not match.")
    descriptor = protected_open(base + "/plugin", False, trust)
    try:
        if not os.fstat(descriptor).st_mode & 0o111:
            raise Rejected("Installed plugin must be executable.")
    finally:
        os.close(descriptor)
    validate_elf(executable)
    return manifest, hashlib.sha256(raw).hexdigest()


def read_policy(path, trust):
    parent, name = os.path.split(absolute(path))
    descriptor = protected_open(parent, True, trust)
    try:
        try:
            os.stat(name, dir_fd=descriptor, follow_symlinks=False)
        except FileNotFoundError:
            return {"schema": 1, "revision": 0}
    finally:
        os.close(descriptor)
    return validate_policy(strict_json(read_protected(path, MAX_POLICY, trust), MAX_POLICY))


def write_policy(parent_fd, name, policy):
    raw = (json.dumps(policy, sort_keys=True, indent=2, ensure_ascii=False) + "\n").encode("utf-8")
    if len(raw) > MAX_POLICY:
        raise Rejected("Policy exceeds its size limit.")
    temporary = ".plugin-policy-" + secrets.token_hex(16)
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=parent_fd)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(raw)
            stream.flush()
            os.fchmod(stream.fileno(), 0o644)
            os.fsync(stream.fileno())
        os.replace(temporary, name, src_dir_fd=parent_fd, dst_dir_fd=parent_fd)
        os.fsync(parent_fd)
    finally:
        try:
            os.unlink(temporary, dir_fd=parent_fd)
        except FileNotFoundError:
            pass


def mutate_policy(root, path, action, revision, core=CORE_VERSION, selection=None, version=None, trust=_Trust(), *, kind="smtp"):
    if type(revision) is not int or revision < 0 or revision >= (1 << 64) - 1:
        raise Rejected("An explicit valid current revision is required.")
    if kind not in ("smtp", "payment", "resource"):
        raise Rejected("Unsupported runtime slot.")
    root_fd = protected_open(root, True, trust)
    parent, name = os.path.split(absolute(path))
    parent_fd = protected_open(parent, True, trust)
    try:
        with locked(root_fd, ".manager.lock", trust), locked(parent_fd, "." + name + ".lock", trust):
            policy = read_policy(path, trust)
            if policy["revision"] != revision:
                raise Rejected("Policy revision changed; inspect status before retrying.")
            if action == "select":
                selected = dict(selection or {})
                if plugin_kind(selected.get("id")) != kind:
                    raise Rejected("Selection identity must match the runtime slot.")
                selected["enabled"] = False
                _, manifest_hash = installed_manifest(root, selected.get("id"), selected.get("version"), core, trust)
                selected["manifest_sha256"] = manifest_hash
                policy[kind] = selected
            elif action == "remove":
                if kind != "resource" or kind not in policy or policy[kind]["enabled"]:
                    raise Rejected("Only a disabled resource selection can be removed before a legacy core rollback.")
                del policy[kind]
            elif action in ("enable", "disable", "rollback"):
                if kind not in policy:
                    raise Rejected("Select an installed plugin before changing its state.")
                selected = policy[kind]
                if action == "rollback":
                    if version_tuple(version) >= version_tuple(selected["version"]):
                        raise Rejected("Rollback requires an earlier installed version.")
                    selected["version"] = version
                    _, selected["manifest_sha256"] = installed_manifest(root, selected["id"], version, core, trust)
                elif action == "enable":
                    _, manifest_hash = installed_manifest(root, selected["id"], selected["version"], core, trust)
                    if manifest_hash != selected["manifest_sha256"]:
                        raise Rejected("The pinned installed manifest changed.")
                    selected["enabled"] = True
                else:
                    selected["enabled"] = False
            else:
                raise Rejected("Unsupported policy action.")
            policy["revision"] = revision + 1
            validate_policy(policy)
            write_policy(parent_fd, name, policy)
            return policy
    finally:
        os.close(parent_fd)
        os.close(root_fd)


def parse_target(raw):
    host, delimiter, port = raw.rpartition(":")
    if host.startswith("[") and host.endswith("]"):
        host = host[1:-1]
    if not delimiter or not re.fullmatch(r"[0-9]{1,5}", port):
        raise argparse.ArgumentTypeError("Use HOST:PORT or [IPv6]:PORT.")
    result = {"host": host, "port": int(port)}
    try:
        validate_targets([result])
    except Rejected as exc:
        raise argparse.ArgumentTypeError(str(exc)) from exc
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default="/opt/aster/plugins")
    parser.add_argument("--policy", default="/etc/aster/plugins.json")
    parser.add_argument("--core-version", default=CORE_VERSION)
    commands = parser.add_subparsers(dest="action", required=True)
    for name in ("verify", "install"):
        command = commands.add_parser(name)
        command.add_argument("package")
        command.add_argument("--sha256", required=True)
        command.add_argument("--binary")
        command.add_argument("--binary-sha256")
    commands.add_parser("status")
    for name in ("select", "enable", "disable", "rollback", "remove"):
        command = commands.add_parser(name)
        command.add_argument("--kind", choices=["smtp", "payment", "resource"])
        command.add_argument("--revision", required=True, type=int)
        if name == "select":
            command.add_argument("--id", required=True)
            command.add_argument("--version", required=True)
            command.add_argument("--worker-uid", type=int, required=True)
            command.add_argument("--client-uid", type=int, required=True)
            command.add_argument("--socket", required=True)
            command.add_argument("--target", action="append", type=parse_target, default=[])
            command.add_argument("--merchant-id")
            command.add_argument("--controller-id")
        if name == "rollback":
            command.add_argument("--version", required=True)
    options = parser.parse_args(argv)
    try:
        if sys.platform != "linux" or os.geteuid() != 0:
            raise Rejected("Run this Linux installation tool as root.")
        version_tuple(options.core_version)
        absolute(options.root)
        absolute(options.policy)
        if options.action in ("install", "verify"):
            package = verify_package(absolute(options.package), options.sha256, options.core_version, binary=options.binary, binary_sha256=options.binary_sha256)
            result = install_package(options.root, package) if options.action == "install" else {"verified": True, "id": package.manifest["id"], "version": package.manifest["version"], "archive_sha256": package.archive_sha256, "service_action": "none"}
            result.update(distribution_format="raw-binaries-v1" if options.binary is not None else "archive-v1", binary_sha256=package.manifest["sha256"])
        elif options.action == "status":
            result = read_policy(options.policy, _Trust())
        else:
            selection = None
            if options.action == "select":
                selection = {"id": options.id, "version": options.version, "worker_uid": options.worker_uid, "client_uid": options.client_uid, "socket": options.socket, "allowed_targets": options.target}
                if options.merchant_id is not None:
                    selection["merchant_id"] = options.merchant_id
                if options.controller_id is not None:
                    selection["controller_id"] = options.controller_id
            kind = options.kind or (plugin_kind(options.id) if options.action == "select" else "smtp")
            result = mutate_policy(options.root, options.policy, options.action, options.revision, options.core_version, selection, getattr(options, "version", None), kind=kind)
        print(json.dumps(result, sort_keys=True, ensure_ascii=False))
        return 0
    except (Rejected, OSError) as exc:
        print("Plugin manager rejected the action: " + (str(exc) if isinstance(exc, Rejected) else "Protected filesystem operation failed."), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
