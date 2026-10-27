#!/usr/bin/env python3
"""Root operator handoff: verify official updates and stage; never install or execute.

There is deliberately no apply, shell, URL, service or migration option. A web
request is an audit hint, not authority to mutate a running installation. The
operator authorizes this read/download/stage action by explicitly invoking it.
"""
import argparse
import base64
import binascii
import contextlib
import dataclasses
import datetime
import fcntl
import hashlib
import json
import os
import re
import secrets
import stat
import struct
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import zlib

REPOSITORY = "xiaoqiqixi/betteridcsys"
REPOSITORY_ID = 1418618420
WORKFLOW = ".github/workflows/release-build.yml"
LEGACY_TRUSTED_PATHS = {WORKFLOW, "vps-billing/deploy/ci/release_pipeline.py", "vps-billing/deploy/ci/historical-artifacts.json", "vps-billing/deploy/ci/check.sh", "vps-billing/deploy/ci/prepare-native-runner.sh", "vps-billing/deploy/ci/prepare-browser.py", "vps-billing/deploy/ci/accept-native.sh", "vps-billing/deploy/ci/accept-native-http.py", "vps-billing/deploy/ci/plugin-native.sh", "vps-billing/deploy/ci/plugin-native.py", "vps-billing/deploy/ci/smtp-loopback-fixture.py", "vps-billing/deploy/ci/test_plugin_native.py", "vps-billing/deploy/ci/browser-demo.py", "vps-billing/deploy/ci/mysql-native.sh", "vps-billing/deploy/ci/mysql-distribution.json", "vps-billing/deploy/scripts/package-release.sh", "vps-billing/deploy/scripts/release-manifest.py", "vps-billing/deploy/scripts/plugin-manager.py", "vps-billing/deploy/scripts/update-manager.py", "vps-billing/deploy/scripts/test-update-manager.py", "vps-billing/deploy/systemd/aster-api.service", "vps-billing/deploy/systemd/aster-smtp.service"}
PREPARE_TRUSTED_PATHS = LEGACY_TRUSTED_PATHS | {"vps-billing/deploy/scripts/core-manager.py", "vps-billing/deploy/scripts/test-core-manager.py"}
EXECUTOR_TRUSTED_PATHS = PREPARE_TRUSTED_PATHS | {"vps-billing/deploy/scripts/core-operation.py", "vps-billing/deploy/scripts/test-core-operation.py", "vps-billing/deploy/ci/core-native.sh", "vps-billing/deploy/ci/core-native.py"}
PAYMENT_TRUSTED_PATHS = EXECUTOR_TRUSTED_PATHS | {"vps-billing/deploy/ci/payment-native.sh", "vps-billing/deploy/ci/payment-native.py", "vps-billing/deploy/ci/test_payment_native.py", "vps-billing/deploy/systemd/aster-payment.service"}
# Core 0.5.10 adds real-MySQL browser and Ubuntu22 native gates. Preserve older proof inventories;
# requiring these new files for historical artifacts would rewrite their contract.
TRUSTED_PATHS = PAYMENT_TRUSTED_PATHS | {
    ".github/workflows/ci.yml",
    "vps-billing/deploy/ci/mysql-distribution.py",
    "vps-billing/deploy/ci/mysql-distribution-ubuntu22.json",
    "vps-billing/deploy/ci/browser-mysql.sh",
    "vps-billing/deploy/ci/browser-mysql.py",
    "vps-billing/deploy/ci/browser-mysql-fixture.gotxt",
    "vps-billing/frontend/tests/browser-mysql.mjs",
    "vps-billing/frontend/tests/ui-copy-boundaries.mjs",
}
LICENSE_SHA256 = "c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4"
MAX_JSON = 2 << 20
MAX_ARCHIVE = 80 << 20
MAX_EXPANDED = 160 << 20
MAX_FILE = 64 << 20
MAX_GITHUB_TOKEN = 4096
MAX_BACKUP = 256 << 20
MAX_BACKUP_EXPANDED = 2 << 30
VERSION = re.compile(r"(?:0|[1-9][0-9]{0,5})\.(?:0|[1-9][0-9]{0,5})\.(?:0|[1-9][0-9]{0,5})\Z")
HASH = re.compile(r"[a-f0-9]{64}\Z")
SHA = re.compile(r"[a-f0-9]{40}\Z")
REQUEST = re.compile(r"[a-f0-9]{32}\Z")
INPUTS = {"LICENSE", "README.md", "vps-billing/backend/go.mod", "vps-billing/backend/go.sum", "vps-billing/frontend/package.json", "vps-billing/frontend/package-lock.json", "vps-billing/backend/pkg/pluginapi/protocol.go", "vps-billing/backend/plugins/smtp/VERSION"}
INTENT_KEYS = {"schema", "request_id", "actor_id", "created_at", "expires_at", "repository", "repository_id", "component", "tag", "version", "source_sha", "release_id", "run_id", "run_attempt", "current_core_version", "current_plugin_version", "plugin_policy_revision", "assets"}
PROVENANCE_KEYS = {"schema", "repository", "repository_id", "run_id", "run_attempt", "event", "tag", "version", "source_sha", "run_head_sha", "workflow_sha", "workflow_path", "default_branch", "default_sha", "trusted_blobs", "artifact_name", "assets"}


class Rejected(Exception):
    """Fixed safe diagnostic, never a provider body, signed URL or secret."""


@dataclasses.dataclass(frozen=True)
class _Trust:
    owner: int = 0
    ancestor: int = 0


def require(condition, message):
    if not condition:
        raise Rejected(message)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def stable(value):
    require(isinstance(value, str) and VERSION.fullmatch(value), "A stable semantic version is required.")
    return tuple(map(int, value.split(".")))


def trusted_paths_for_core(version):
    parsed = stable(version)
    return LEGACY_TRUSTED_PATHS if parsed <= (0, 5, 0) else PREPARE_TRUSTED_PATHS if parsed == (0, 5, 1) else EXECUTOR_TRUSTED_PATHS if parsed == (0, 5, 2) else PAYMENT_TRUSTED_PATHS if parsed < (0, 5, 10) else TRUSTED_PATHS


def positive(value):
    return type(value) is int and 0 < value < 1 << 63


def exact_path(value):
    require(isinstance(value, str) and 1 < len(value) <= 1024 and value.startswith("/") and os.path.normpath(value) == value and not any(ord(c) < 32 or ord(c) == 127 for c in value), "A canonical absolute protected path is required.")
    return value


def protected_open(path, directory=False, trust=_Trust(), private=False):
    exact_path(path)
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
            info = os.fstat(descriptor)
            ancestor = index < len(parts) - 1
            sticky = ancestor and is_directory and info.st_uid == trust.ancestor and info.st_mode & stat.S_ISVTX
            require(info.st_uid == trust.owner or ancestor and info.st_uid == trust.ancestor, "Paths must be root-owned and protected.")
            require(not info.st_mode & (stat.S_ISUID | stat.S_ISGID) and (not info.st_mode & 0o022 or sticky), "Writable or privileged paths are forbidden.")
            require(stat.S_ISDIR(info.st_mode) if is_directory else stat.S_ISREG(info.st_mode) and info.st_nlink == 1, "Links and special files are forbidden.")
            if not ancestor and private:
                require(stat.S_IMODE(info.st_mode) == (0o700 if directory else 0o600), "Private paths must use mode 0700 or 0600.")
        result, descriptor = descriptor, -1
        return result
    except OSError as exc:
        raise Rejected("Protected filesystem input could not be opened.") from exc
    finally:
        if descriptor >= 0:
            os.close(descriptor)


def unchanged(before, after):
    return all(getattr(before, key) == getattr(after, key) for key in ("st_dev", "st_ino", "st_mode", "st_nlink", "st_uid", "st_gid", "st_size", "st_mtime_ns", "st_ctime_ns"))


def read_protected(path, limit, trust=_Trust(), private=False):
    descriptor = protected_open(path, trust=trust, private=private)
    try:
        before = os.fstat(descriptor)
        require(0 < before.st_size <= limit, "Protected input exceeds its size limit.")
        with os.fdopen(descriptor, "rb", closefd=False) as stream:
            raw = stream.read(limit + 1)
        require(len(raw) == before.st_size and unchanged(before, os.fstat(descriptor)), "Protected input changed during verification.")
        return raw
    finally:
        os.close(descriptor)


def valid_github_token(token):
    # RFC 6750 transport syntax only. GitHub credentials are opaque, including
    # the variable-length installation tokens issued to Actions since 2026.
    return isinstance(token, str) and 0 < len(token) <= MAX_GITHUB_TOKEN and re.fullmatch(r"[A-Za-z0-9._~+/-]+=*", token) is not None


def load_protected_token(path, trust=_Trust()):
    raw = read_protected(path, MAX_GITHUB_TOKEN + 2, trust=trust, private=True)
    if raw.endswith(b"\r\n"):
        raw = raw[:-2]
    elif raw.endswith(b"\n"):
        raw = raw[:-1]
    try:
        token = raw.decode("ascii")
    except UnicodeError:
        raise Rejected("Invalid protected GitHub credential format.") from None
    require(valid_github_token(token), "Invalid protected GitHub credential format.")
    return token


def strict_json(raw, limit=MAX_JSON):
    require(isinstance(raw, bytes) and 0 < len(raw) <= limit, "JSON exceeds its size limit.")
    def pairs(items):
        result = {}
        for key, value in items:
            require(isinstance(key, str) and len(key) <= 512 and key not in result and len(result) < 20000, "JSON contains duplicate or oversized keys.")
            result[key] = value
        return result
    def invalid(_):
        raise Rejected("Non-finite JSON values are forbidden.")
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=pairs, parse_constant=invalid)
    except (ValueError, UnicodeError, RecursionError) as exc:
        raise Rejected("Invalid JSON input.") from exc
    def inspect(item, depth=0):
        require(depth <= 16 and (not isinstance(item, list) or len(item) <= 20000), "JSON nesting or array bounds exceeded.")
        if isinstance(item, (dict, list)):
            for child in item.values() if isinstance(item, dict) else item:
                inspect(child, depth + 1)
    inspect(value)
    return value


def timestamp(value):
    require(isinstance(value, str) and re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z", value), "UTC request timestamps are required.")
    try:
        return datetime.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=datetime.timezone.utc).timestamp()
    except ValueError as exc:
        raise Rejected("Invalid request timestamp.") from exc


def binary_assets(component, version):
    require(component in {"core", "smtp"}, "Unsupported release component.")
    stable(version)
    return ({"bin/aster-api": "aster-api-" + version + "-linux-amd64", "bin/aster-plugin-host": "aster-plugin-host-" + version + "-linux-amd64"} if component == "core" else {"plugin": "aster-smtp-" + version + "-linux-amd64"})


def archive_name(component, version, distribution_format="archive-v1"):
    require(isinstance(component, str) and component in {"core", "smtp"}, "Unsupported release component.")
    stable(version)
    require(isinstance(distribution_format, str) and distribution_format in {"archive-v1", "raw-binaries-v1"}, "Unsupported distribution format.")
    if distribution_format == "raw-binaries-v1":
        return ("aster-smtp-" if component == "smtp" else "aster-core-") + version + "-linux-amd64-resources.tar.gz"
    return ("aster-smtp-" if component == "smtp" else "aster-") + version + "-linux-amd64.tar.gz"


def asset_limit(name):
    return MAX_ARCHIVE if name.endswith(".tar.gz") else MAX_FILE if name.endswith("-linux-amd64") else MAX_JSON


def asset_inventory(value, component, version, distribution_format="archive-v1"):
    expected = {archive_name(component, version, distribution_format), "BUILD.json", "PROVENANCE.json", "SHA256SUMS"}
    if distribution_format == "raw-binaries-v1":
        expected.update(binary_assets(component, version).values())
    require(isinstance(value, list) and len(value) == len(expected), "The exact declared official release assets are required.")
    result, ids = {}, set()
    for item in value:
        require(isinstance(item, dict) and set(item) == {"name", "id", "size", "sha256"}, "Invalid handoff asset fields.")
        name = item["name"]
        require(isinstance(name, str) and name in expected and name not in result and positive(item["id"]) and item["id"] not in ids and positive(item["size"]) and item["size"] <= asset_limit(name) and isinstance(item["sha256"], str) and HASH.fullmatch(item["sha256"]), "Invalid or duplicate asset metadata.")
        result[name] = item
        ids.add(item["id"])
    require(set(result) == expected, "Unsupported release asset inventory.")
    return result


def validate_handoff(raw, current_core, current_plugin, now=None):
    envelope = strict_json(raw, 32 << 10)
    require(isinstance(envelope, dict) and set(envelope) == {"schema", "status", "intent"} and type(envelope["schema"]) is int and envelope["schema"] in {1, 2} and envelope["status"] == "awaiting_host_operator", "Unsupported update handoff envelope.")
    value = envelope["intent"]
    schema = envelope["schema"]
    keys = INTENT_KEYS | {"distribution_format"} if schema == 2 else INTENT_KEYS
    require(isinstance(value, dict) and set(value) == keys and type(value["schema"]) is int and value["schema"] == schema and isinstance(value["request_id"], str) and REQUEST.fullmatch(value["request_id"]) and isinstance(value["actor_id"], str) and REQUEST.fullmatch(value["actor_id"]), "Invalid update request identity.")
    require(value["repository"] == REPOSITORY and type(value["repository_id"]) is int and value["repository_id"] == REPOSITORY_ID and isinstance(value["component"], str) and value["component"] in {"core", "smtp"}, "Only the fixed official repository is supported.")
    stable(value["version"])
    require(value["tag"] == ("smtp-v" if value["component"] == "smtp" else "v") + value["version"] and isinstance(value["source_sha"], str) and SHA.fullmatch(value["source_sha"]) and positive(value["release_id"]) and positive(value["run_id"]) and positive(value["run_attempt"]), "Invalid release tag or build identity.")
    stable(current_core)
    if current_plugin:
        stable(current_plugin)
    require(value["current_core_version"] == current_core and value["current_plugin_version"] == current_plugin and type(value["plugin_policy_revision"]) is int and 0 <= value["plugin_policy_revision"] < 1 << 64, "Installed version changed; obtain a fresh handoff.")
    require(stable(value["version"]) > stable(current_core if value["component"] == "core" else current_plugin or "0.0.0"), "Updates require a newer stable component version.")
    created, expires = timestamp(value["created_at"]), timestamp(value["expires_at"])
    now = time.time() if now is None else now
    require(created <= now + 30 and 0 < expires - created <= 1800 and created >= now - 1800 and expires > now, "The update handoff has expired or has an invalid lifetime.")
    asset_inventory(value["assets"], value["component"], value["version"], value.get("distribution_format", "archive-v1"))
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Official:
    """Read-only fixed-repository HTTPS. No environment proxy or credential redirect."""
    def __init__(self, token=""):
        require(isinstance(token, str) and (not token or valid_github_token(token)), "Invalid protected GitHub credential format.")
        self.token = token
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def _get(self, url, limit, asset=False, authenticated=True):
        require(isinstance(url, str) and len(url) <= 8192, "Unsafe official download URL.")
        try:
            parsed = urllib.parse.urlsplit(url)
            port = parsed.port
        except ValueError as exc:
            raise Rejected("Unsafe official download URL.") from exc
        require(parsed.scheme == "https" and port in (None, 443) and not parsed.username and not parsed.password and not parsed.fragment and all(32 < ord(c) < 127 for c in url), "Unsafe official download URL.")
        allowed = {"api.github.com"} if authenticated else {"release-assets.githubusercontent.com"}
        require(parsed.hostname in allowed, "Download host is outside the official allowlist.")
        headers = {"User-Agent": "aster-update-handoff/1", "Accept": "application/octet-stream" if asset else "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
        if authenticated and self.token:
            headers["Authorization"] = "Bearer " + self.token
        request = urllib.request.Request(url, headers=headers, method="GET")
        started = time.monotonic()
        try:
            with self.opener.open(request, timeout=15) as response:
                require(response.status == 200 and response.headers.get("Content-Encoding", "identity") == "identity", "Unexpected official HTTP response.")
                length = response.headers.get("Content-Length")
                require(length is None or length.isascii() and length.isdigit() and 0 < int(length) <= limit, "Official response length exceeds its bound.")
                chunks, total = [], 0
                while True:
                    require(time.monotonic() - started < 90, "Official download exceeded its time limit.")
                    chunk = response.read(min(65536, limit + 1 - total))
                    if not chunk:
                        break
                    chunks.append(chunk)
                    total += len(chunk)
                    require(total <= limit, "Official download exceeds its size limit.")
                require(total > 0 and (length is None or total == int(length)), "Official response is truncated.")
                return b"".join(chunks)
        except urllib.error.HTTPError as exc:
            if authenticated and asset and exc.code in (301, 302, 303, 307, 308):
                location = exc.headers.get("Location", "")
                return self._get(location, limit, asset=True, authenticated=False)
            raise Rejected("Official repository request failed; check read access and release availability.") from None
        except (urllib.error.URLError, TimeoutError, OSError, ValueError) as exc:
            raise Rejected("Official repository download failed.") from None

    def json(self, suffix):
        require(isinstance(suffix, str) and re.fullmatch(r"/(?:git/ref/(?:tags/[A-Za-z0-9.-]+|heads/main)|git/tags/[a-f0-9]{40}|git/trees/[a-f0-9]{40}\?recursive=1|git/blobs/[a-f0-9]{40}|releases/(?:tags/[A-Za-z0-9.-]+|latest)|actions/runs/[1-9][0-9]*|actions/workflows/release-build.yml|compare/[a-f0-9]{40}\.\.\.main)", suffix), "Unsupported official API action.")
        return strict_json(self._get("https://api.github.com/repos/" + REPOSITORY + suffix, MAX_JSON))

    def repository(self):
        return strict_json(self._get("https://api.github.com/repos/" + REPOSITORY, MAX_JSON))

    def asset(self, asset_id, limit):
        require(positive(asset_id), "Invalid official asset ID.")
        return self._get("https://api.github.com/repos/" + REPOSITORY + "/releases/assets/" + str(asset_id), limit, asset=True)


def resolve_tag(api, tag):
    value = api.json("/git/ref/tags/" + tag)
    require(isinstance(value, dict) and value.get("ref") == "refs/tags/" + tag and isinstance(value.get("object"), dict), "Release tag identity mismatch.")
    item, seen = value["object"], set()
    for _ in range(4):
        require(isinstance(item.get("type"), str) and item["type"] in {"tag", "commit"} and isinstance(item.get("sha"), str) and SHA.fullmatch(item["sha"]) and item["sha"] not in seen, "Invalid or cyclic release tag.")
        seen.add(item["sha"])
        if item["type"] == "commit":
            return item["sha"]
        annotated = api.json("/git/tags/" + item["sha"])
        require(isinstance(annotated, dict) and annotated.get("sha") == item["sha"] and isinstance(annotated.get("object"), dict), "Annotated tag identity mismatch.")
        item = annotated["object"]
    raise Rejected("Release tag nesting exceeds its limit.")


def release_assets(api, intent):
    repository = api.repository()
    require(isinstance(repository, dict) and type(repository.get("id")) is int and repository["id"] == REPOSITORY_ID and repository.get("full_name") == REPOSITORY and repository.get("default_branch") == "main", "Official repository identity changed.")
    require(resolve_tag(api, intent["tag"]) == intent["source_sha"], "Release tag changed; request a new update check.")
    ancestry = api.json("/compare/" + intent["source_sha"] + "...main")
    require(isinstance(ancestry, dict) and isinstance(ancestry.get("status"), str) and ancestry["status"] in {"ahead", "identical"} and isinstance(ancestry.get("merge_base_commit"), dict) and ancestry["merge_base_commit"].get("sha") == intent["source_sha"], "Release source is not retained on main.")
    release = api.json("/releases/tags/" + intent["tag"])
    require(isinstance(release, dict) and type(release.get("id")) is int and release["id"] == intent["release_id"] and release.get("tag_name") == intent["tag"] and release.get("draft") is False and release.get("prerelease") is False and release.get("target_commitish") == intent["source_sha"] and isinstance(release.get("assets"), list), "A normal public release bound to this source is required.")
    assets = []
    for item in release["assets"]:
        require(isinstance(item, dict) and item.get("state") == "uploaded" and isinstance(item.get("digest"), str) and re.fullmatch(r"sha256:[a-f0-9]{64}", item["digest"]), "Official asset lacks a complete GitHub SHA-256 digest.")
        assets.append({"name": item.get("name"), "id": item.get("id"), "size": item.get("size"), "sha256": item["digest"][7:]})
    actual = asset_inventory(assets, intent["component"], intent["version"], intent.get("distribution_format", "archive-v1"))
    require(actual == asset_inventory(intent["assets"], intent["component"], intent["version"], intent.get("distribution_format", "archive-v1")), "Release assets changed; request a new update check.")
    return actual


def checksums(raw, names):
    require(len(raw) <= 4096, "Checksum inventory exceeds its bound.")
    try:
        lines = raw.decode("ascii").splitlines()
    except UnicodeError as exc:
        raise Rejected("Invalid checksum encoding.") from exc
    result = {}
    for line in lines:
        match = re.fullmatch(r"([a-f0-9]{64})  ([A-Za-z0-9_.-]+)", line)
        require(match is not None and match[2] not in result, "Invalid or duplicate checksum entry.")
        result[match[2]] = match[1]
    require(set(result) == names, "Checksum inventory differs from the fixed release assets.")
    return result


def octal(raw):
    value = raw.strip(b"\0 ")
    require(bool(value) and re.fullmatch(b"[0-7]+", value), "Unsupported tar numeric field.")
    return int(value, 8)


def unpack_archive(compressed, root):
    try:
        decoder = zlib.decompressobj(16 + zlib.MAX_WBITS)
        raw = decoder.decompress(compressed, MAX_EXPANDED + 1)
        require(len(raw) <= MAX_EXPANDED and decoder.eof and not decoder.unused_data and not decoder.unconsumed_tail, "Invalid gzip framing or archive expansion.")
    except zlib.error as exc:
        raise Rejected("Invalid release gzip archive.") from exc
    files, directories, offset, long_name = {}, set(), 0, None
    while offset + 512 <= len(raw):
        block = raw[offset:offset + 512]
        if not any(block):
            require(long_name is None and len(raw) - offset >= 1024 and not any(raw[offset:]), "Invalid tar terminal records.")
            break
        require(len(files) + len(directories) < 20000 and block[257:263] in (b"ustar\0", b"ustar "), "Unsupported or oversized tar archive.")
        require(octal(block[148:156]) == sum(block[:148]) + 8 * 32 + sum(block[156:]), "Tar header checksum mismatch.")
        kind, size, mode = block[156:157], octal(block[124:136]), octal(block[100:108])
        start = offset + 512
        offset = start + (size + 511) // 512 * 512
        require(offset <= len(raw), "Archive member exceeds its bounds.")
        if kind == b"L":
            # Historical GNU-produced Core resources contain long license paths.
            # Accept only this one bounded name record, never generic extensions,
            # chained overrides, long links, PAX, sparse files or extraction.
            require(long_name is None and block[:100] == b"././@LongLink".ljust(100, b"\0") and block[257:265] == b"ustar  \0" and mode == 0o644 and octal(block[108:116]) == 0 and octal(block[116:124]) == 0 and not any(block[157:257]) and not any(block[265:]) and 102 <= size <= 1025 and not any(raw[start + size:offset]), "Unsupported GNU long-name record.")
            octal(block[136:148])
            payload = raw[start:start + size]
            require(payload.endswith(b"\0") and b"\0" not in payload[:-1], "GNU long-name payload requires one terminal NUL.")
            long_name = payload[:-1]
            require(all(32 < value < 127 for value in long_name), "GNU long-name paths must be bounded ASCII.")
            continue
        name_raw = block[:100].split(b"\0", 1)[0]
        prefix = block[345:500].split(b"\0", 1)[0] if block[257:263] == b"ustar\0" else b""
        if long_name is not None:
            require(kind in (b"0", b"\0", b"5") and block[257:265] == b"ustar  \0" and block[:100] == long_name[:100] and not any(block[345:]), "GNU long-name must immediately name its matching regular member.")
            name_raw, long_name = long_name, None
        elif prefix:
            name_raw = prefix + b"/" + name_raw
        try:
            name = name_raw.decode("ascii")
        except UnicodeError as exc:
            raise Rejected("Archive paths must be ASCII.") from exc
        parts = name.rstrip("/").split("/")
        require(parts[0] == root and all(re.fullmatch(r"[A-Za-z0-9@_.+-]+", part) and part not in {".", ".."} for part in parts) and kind in (b"0", b"\0", b"5") and not any(block[157:257]) and octal(block[108:116]) == 0 and octal(block[116:124]) == 0, "Archive contains unsafe paths, links or extension records.")
        relative = "/".join(parts[1:])
        if kind == b"5":
            require(size == 0 and mode == 0o755 and relative not in directories and relative not in files, "Invalid archive directory.")
            directories.add(relative)
        else:
            require(relative and not name.endswith("/") and relative not in files and relative not in directories and 0 < size <= MAX_FILE and mode in (0o644, 0o755), "Invalid archive file or permissions.")
            files[relative] = (raw[start:start + size], mode)
    else:
        raise Rejected("Archive terminal records are missing.")
    require("" in directories and all("/".join(path.split("/")[:i]) in directories for path in files for i in range(1, len(path.split("/")))), "Archive directories are incomplete.")
    return files


def static_elf(raw):
    require(64 <= len(raw) <= MAX_FILE and raw[:7] == b"\x7fELF\x02\x01\x01" and raw[7] in (0, 3), "A static Linux amd64 ELF binary is required.")
    kind, machine, revision, entry, programs, _, _, header, width, count = struct.unpack_from("<HHIQQQIHHH", raw, 16)
    require(kind == 2 and machine == 62 and revision == 1 and header == 64 and width == 56 and 0 < count <= 128 and programs >= 64 and programs + count * width <= len(raw), "Invalid ELF program headers.")
    executable = False
    for index in range(count):
        segment, flags, offset, address, _, size, memory, alignment = struct.unpack_from("<IIQQQQQQ", raw, programs + index * width)
        require(segment not in (2, 3) and offset + size <= len(raw) and size <= memory and (alignment in (0, 1) or alignment & (alignment - 1) == 0), "Dynamic loaders or invalid ELF segments are forbidden.")
        executable |= segment == 1 and flags & 1 and address <= entry < address + memory
    require(executable, "ELF entry point is outside executable segments.")


def blob_sha(raw):
    return hashlib.sha1(b"blob " + str(len(raw)).encode() + b"\0" + raw).hexdigest()


def verify_source(api, build, bundle, source, provenance):
    trusted_blobs = provenance["trusted_blobs"]
    trusted_paths = set(trusted_blobs)
    # Historical inventories are exact, version-bound policies. In particular,
    # installing published 0.5.1 before upgrading must not silently accept a
    # subset of the executor policy introduced in 0.5.2.
    expected_trust = trusted_paths_for_core(build["component_versions"]["core"])
    require(trusted_paths == expected_trust, "The build policy must contain the exact reviewed trust inventory.")
    tree = api.json("/git/trees/" + source + "?recursive=1")
    require(isinstance(tree, dict) and tree.get("truncated") is False and isinstance(tree.get("tree"), list) and len(tree["tree"]) <= 20000, "Complete tagged source tree is required.")
    require(all(isinstance(path, str) and path.startswith((".github/", "vps-billing/")) and not any(part in {"", ".", ".."} for part in path.split("/")) for path in trusted_blobs), "Invalid trusted build policy paths.")
    needed = set(build["source_files"].values()) | INPUTS | set(trusted_blobs)
    selected, migrations = {}, {}
    raw_core = build.get("distribution_format") == "raw-binaries-v1" and build["release_kind"] == "core"
    migration_prefix = "vps-billing/backend/migrations/"
    for item in tree["tree"]:
        require(isinstance(item, dict), "Invalid Git tree member.")
        path = item.get("path")
        require(isinstance(path, str), "Invalid Git tree path.")
        if raw_core and path.startswith(migration_prefix) and path.endswith(".sql") and "/" not in path[len(migration_prefix):]:
            require(path not in migrations and item.get("type") == "blob" and item.get("mode") in {"100644", "100755"} and isinstance(item.get("sha"), str) and SHA.fullmatch(item["sha"]), "Invalid tagged SQL migration inventory.")
            migrations[path] = item["sha"]
        if item.get("path") in needed:
            require(item["path"] not in selected and item.get("type") == "blob" and item.get("mode") in {"100644", "100755"} and isinstance(item.get("sha"), str) and SHA.fullmatch(item["sha"]), "Selected source contains links or duplicate members.")
            selected[item["path"]] = item["sha"]
    if raw_core:
        expected_migrations = {"migrations/" + path[len(migration_prefix):]: path for path in migrations}
        copied_migrations = {path: source_path for path, source_path in build["source_files"].items() if path.startswith("migrations/")}
        require(expected_migrations and copied_migrations == expected_migrations and {path for path in bundle if path.startswith("migrations/")} == set(expected_migrations), "Core SQL resources must contain the complete tagged migration inventory.")
    require(set(selected) == needed, "Selected source inventory is incomplete.")
    require(all(selected[path] == value for path, value in trusted_blobs.items()), "Build policy differs from tagged source.")
    for policy_commit in {provenance["default_sha"], provenance["run_head_sha"]} - {source}:
        policy_tree = api.json("/git/trees/" + policy_commit + "?recursive=1")
        require(isinstance(policy_tree, dict) and policy_tree.get("truncated") is False and isinstance(policy_tree.get("tree"), list) and len(policy_tree["tree"]) <= 20000, "Complete build policy source tree is required.")
        policy = {}
        for item in policy_tree["tree"]:
            require(isinstance(item, dict), "Invalid policy Git tree member.")
            if item.get("path") in trusted_paths:
                require(item["path"] not in policy and item.get("type") == "blob" and item.get("mode") in {"100644", "100755"}, "Invalid policy Git source file.")
                policy[item["path"]] = item.get("sha")
        require(policy == trusted_blobs, "Tagged, workflow and default build policy differ.")
        ancestry = api.json("/compare/" + policy_commit + "...main")
        require(isinstance(ancestry, dict) and isinstance(ancestry.get("status"), str) and ancestry["status"] in {"ahead", "identical"} and isinstance(ancestry.get("merge_base_commit"), dict) and ancestry["merge_base_commit"].get("sha") == policy_commit, "Build policy source is no longer retained on main.")
    for destination, source_path in build["source_files"].items():
        require(blob_sha(bundle[destination][0]) == selected[source_path], "Release text differs from tagged source.")
    inputs = {}
    for path in sorted(INPUTS):
        blob = api.json("/git/blobs/" + selected[path])
        require(isinstance(blob, dict) and blob.get("sha") == selected[path] and type(blob.get("size")) is int and 0 < blob["size"] <= MAX_JSON // 2 and blob.get("encoding") == "base64" and isinstance(blob.get("content"), str), "Invalid tagged source input.")
        try:
            raw = base64.b64decode(blob["content"].replace("\n", "").replace("\r", ""), validate=True)
        except (ValueError, binascii.Error) as exc:
            raise Rejected("Invalid tagged input encoding.") from exc
        require(len(raw) == blob["size"] and blob_sha(raw) == selected[path] and digest(raw) == build["input_sha256"][path], "Release input does not match tagged source.")
        inputs[path] = raw
    core = re.findall(rb'(?m)^\s*(?:const\s+)?CoreVersion\s*=\s*"((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))"', inputs["vps-billing/backend/pkg/pluginapi/protocol.go"])
    require(len(core) == 1 and core[0].decode() == build["component_versions"]["core"] and inputs["vps-billing/backend/plugins/smtp/VERSION"].decode().strip() == build["component_versions"]["smtp"], "Release component version differs from source.")


def verify_downloads(api, intent, files, current_core, *, with_bundle=False):
    """Verify all inputs before optionally returning the assembled file bytes.

    The default metadata result remains the staging API. Only an explicit host
    caller requests the verified in-memory bundle; this function writes nothing
    and never executes any member.
    """
    assets = asset_inventory(intent["assets"], intent["component"], intent["version"], intent.get("distribution_format", "archive-v1"))
    require(set(files) == set(assets) and all(len(files[name]) == item["size"] and digest(files[name]) == item["sha256"] for name, item in assets.items()), "Downloaded assets differ from GitHub SHA-256 metadata.")
    sums = checksums(files["SHA256SUMS"], set(files) - {"SHA256SUMS"})
    require(all(digest(files[name]) == value for name, value in sums.items()), "Release checksum mismatch.")
    provenance = strict_json(files["PROVENANCE.json"])
    require(isinstance(provenance, dict) and set(provenance) == PROVENANCE_KEYS, "Unsupported build provenance fields.")
    require(type(provenance["schema"]) is int and provenance["schema"] == 1 and type(provenance["repository_id"]) is int and provenance["repository_id"] == REPOSITORY_ID and provenance["repository"] == REPOSITORY, "Build provenance repository mismatch.")
    require(positive(provenance["run_id"]) and positive(provenance["run_attempt"]) and all(provenance[field] == intent[field] for field in ("tag", "version", "source_sha", "run_id", "run_attempt")) and provenance["workflow_path"] == WORKFLOW and provenance["default_branch"] == "main" and isinstance(provenance["event"], str) and provenance["event"] in {"push", "workflow_dispatch"} and provenance["artifact_name"] == "candidate-" + str(intent["run_id"]) + "-" + str(intent["run_attempt"]), "Build provenance identity mismatch.")
    require(all(isinstance(provenance[field], str) and SHA.fullmatch(provenance[field]) for field in ("run_head_sha", "workflow_sha", "default_sha")) and provenance["workflow_sha"] == provenance["run_head_sha"] and isinstance(provenance["trusted_blobs"], dict) and 1 <= len(provenance["trusted_blobs"]) <= 64 and all(isinstance(v, str) and SHA.fullmatch(v) for v in provenance["trusted_blobs"].values()), "Invalid build policy provenance.")
    expected_provenance_assets = {name: {"size": len(raw), "sha256": digest(raw)} for name, raw in files.items() if name not in {"SHA256SUMS", "PROVENANCE.json"}}
    require(provenance["assets"] == expected_provenance_assets, "Build provenance asset inventory mismatch.")
    workflow = api.json("/actions/workflows/release-build.yml")
    run = api.json("/actions/runs/" + str(intent["run_id"]))
    require(isinstance(workflow, dict) and isinstance(run, dict), "Invalid official workflow or run metadata.")
    require(workflow.get("path") == WORKFLOW and workflow.get("state") == "active" and positive(workflow.get("id")) and positive(run.get("workflow_id")) and positive(run.get("id")) and positive(run.get("run_attempt")) and run["workflow_id"] == workflow["id"] and run.get("path") == WORKFLOW and run["id"] == intent["run_id"] and run["run_attempt"] == intent["run_attempt"] and run.get("status") == "completed" and run.get("conclusion") == "success" and run.get("event") == provenance["event"] and run.get("head_sha") == provenance["run_head_sha"], "The official build has not completed successfully.")
    require(all(isinstance(run.get(field), dict) and type(run[field].get("id")) is int and run[field]["id"] == REPOSITORY_ID and run[field].get("full_name") == REPOSITORY for field in ("repository", "head_repository")), "Build originated outside the official repository.")
    require(provenance["event"] != "push" or run.get("head_sha") == intent["source_sha"] and run.get("head_branch") == intent["tag"], "Tag build source mismatch.")
    require(provenance["event"] != "workflow_dispatch" or run.get("head_branch") == "main", "Manual build was not from main.")
    build = strict_json(files["BUILD.json"])
    component, version = intent["component"], intent["version"]
    format = intent.get("distribution_format", "archive-v1")
    raw_distribution = format == "raw-binaries-v1"
    require(isinstance(build, dict) and type(build.get("schema")) is int and build["schema"] == (3 if raw_distribution else 2) and build.get("release_kind") == component and build.get("release_label") == version and build.get("source_commit") == intent["source_sha"] and build.get("target") == "linux/amd64" and build.get("cgo_enabled") is False, "Release build component or platform mismatch.")
    versions = build.get("component_versions")
    require(isinstance(versions, dict) and set(versions) == {"core", "smtp"} and versions[component] == version, "Release component inventory mismatch.")
    stable(versions["core"]); stable(versions["smtp"])
    maximum = "0.6.0" if raw_distribution else "0.5.0"
    require(build.get("plugin_protocol") == "aster.smtp/1" and build.get("smtp_core_compatibility") == {"min": "0.4.0", "max_exclusive": maximum} and (0, 4, 0) <= stable(versions["core"]) < stable(maximum) and (0, 4, 0) <= stable(current_core if component == "smtp" else version) < stable(maximum), "Official plugin/core compatibility requires manual review.")
    if raw_distribution:
        require(build.get("distribution_format") == format and build.get("binary_assets") == binary_assets(component, version) and build.get("resources_asset") == archive_name(component, version, format) and not set(build).intersection({"resources_sha256", "resources_size", "resource_sha256", "resource_size"}), "Raw distribution layout or self-hash metadata is invalid.")
    else:
        require("distribution_format" not in build and "binary_assets" not in build and "resources_asset" not in build, "Legacy/raw build metadata cannot be mixed.")
    resource_name = archive_name(component, version, format)
    bundle = unpack_archive(files[resource_name], resource_name[:-7])
    if raw_distribution:
        require(not any(value[0].startswith(b"\x7fELF") for value in bundle.values()), "Resource archives cannot contain ELF executables.")
        for path, name in binary_assets(component, version).items():
            require(path not in bundle, "Resource/raw executable collision.")
            bundle[path] = (files[name], 0o755)
    require("LICENSE" in bundle and digest(bundle["LICENSE"][0]) == LICENSE_SHA256, "Preserved root LICENSE mismatch.")
    inputs, copies, binaries = build.get("input_sha256"), build.get("source_files"), build.get("binary_sha256")
    require(isinstance(inputs, dict) and set(inputs) == INPUTS and all(isinstance(v, str) and HASH.fullmatch(v) for v in inputs.values()) and inputs["LICENSE"] == LICENSE_SHA256, "Build input inventory mismatch.")
    require(isinstance(copies, dict) and copies.get("LICENSE") == "LICENSE" and 1 <= len(copies) <= 512 and all(isinstance(k, str) and k in bundle and isinstance(v, str) and (v in {"LICENSE", "README.md"} or v.startswith("vps-billing/")) for k, v in copies.items()), "Build source copy inventory mismatch.")
    required = {"bin/aster-api", "bin/aster-plugin-host"} if component == "core" else {"plugin"}
    require(isinstance(binaries, dict) and set(binaries) == required and all(isinstance(v, str) and HASH.fullmatch(v) for v in binaries.values()), "Build binary inventory mismatch.")
    for path, value in binaries.items():
        require(path in bundle and digest(bundle[path][0]) == value and bundle[path][1] == 0o755, "Binary hash or permissions mismatch.")
        static_elf(bundle[path][0])
    if component == "core":
        required_deploy = {"deploy/scripts/backup-native.sh", "deploy/scripts/plugin-manager.py", "deploy/scripts/update-manager.py", "deploy/systemd/aster-api.service", "deploy/systemd/aster-smtp.service", "deploy/systemd/api.env.example", "deploy/nginx.native.conf"}
        if stable(version) >= (0, 5, 1):
            required_deploy.add("deploy/scripts/core-manager.py")
        if stable(version) >= (0, 5, 2):
            required_deploy.add("deploy/scripts/core-operation.py")
        if stable(version) >= (0, 5, 3):
            required_deploy.add("deploy/systemd/aster-payment.service")
        allowed = required | set(copies) | {"manifests/BUILD.json", "manifests/go-dependencies.json", "manifests/npm-installed-dependencies.json", "manifests/aster-api-build.txt", "manifests/aster-plugin-host-build.txt"}
        executable = required | {path for path in required_deploy if path.startswith("deploy/scripts/")}
        exact_copies = {"LICENSE": "LICENSE", "REPOSITORY-README.md": "README.md", "PROJECT-README.md": "vps-billing/README.md", "manifests/go.mod": "vps-billing/backend/go.mod", "manifests/go.sum": "vps-billing/backend/go.sum", "manifests/package.json": "vps-billing/frontend/package.json", "manifests/package-lock.json": "vps-billing/frontend/package-lock.json"}
        exact_copies.update({path: "vps-billing/" + path for path in required_deploy})
        if raw_distribution:
            migrations = {path for path in copies if path.startswith("migrations/")}
            require(migrations and all(re.fullmatch(r"migrations/[0-9]{3}_[a-z0-9_]+\.sql", path) and copies[path] == "vps-billing/backend/" + path for path in migrations), "Core migration resources must bind to tagged SQL.")
            exact_copies.update({path: copies[path] for path in migrations})
        require(all(copies.get(path) == source for path, source in exact_copies.items()) and all(path in exact_copies or path.startswith(("docs/", "deploy/systemd/")) and source == "vps-billing/" + path for path, source in copies.items()), "Core source copy paths differ from the reviewed runtime layout.")
        require(required_deploy <= set(copies) and allowed <= set(bundle) and bundle["manifests/BUILD.json"][0] == files["BUILD.json"] and "web/index.html" in bundle, "Core archive is missing required runtime files.")
        require(all(path in allowed or path.startswith(("web/", "licenses/")) for path in bundle), "Core archive contains unexpected project source.")
        require(all(mode == (0o755 if path in executable else 0o644) for path, (_, mode) in bundle.items()), "Core archive executable permissions mismatch.")
    else:
        inventory = {"plugin", "manifest.json", "LICENSE", "README.md", "THIRD_PARTY_NOTICES.json", "SHA256SUMS"}
        require(set(bundle) == inventory and copies == {"LICENSE": "LICENSE", "README.md": "vps-billing/backend/plugins/smtp/README.md"} and all(mode == (0o755 if path == "plugin" else 0o644) for path, (_, mode) in bundle.items()), "SMTP archive must contain its exact independent inventory.")
        manifest = strict_json(bundle["manifest.json"][0], 8192)
        require(manifest == {"schema": 1, "id": "email.smtp", "version": version, "protocol": "aster.smtp/1", "core_min": "0.4.0", "core_max_exclusive": maximum, "capabilities": ["smtp.send"], "executable": "plugin", "sha256": binaries["plugin"]} and type(manifest.get("schema")) is int, "SMTP manifest identity mismatch.")
        inner = checksums(bundle["SHA256SUMS"][0], inventory - {"SHA256SUMS"})
        require(all(digest(bundle[path][0]) == value for path, value in inner.items()), "SMTP member checksum mismatch.")
        notices = strict_json(bundle["THIRD_PARTY_NOTICES.json"][0])
        require(isinstance(notices, dict) and type(notices.get("schema")) is int and notices["schema"] == 1 and isinstance(notices.get("notices"), list) and 0 < len(notices["notices"]) <= 256, "Original SMTP third-party notices are required.")
        for notice in notices["notices"]:
            require(isinstance(notice, dict) and set(notice) == {"path", "sha256", "text"} and isinstance(notice["path"], str) and len(notice["path"]) <= 512 and all(re.fullmatch(r"[A-Za-z0-9@_.+-]+", part) and part not in {".", ".."} for part in notice["path"].split("/")) and isinstance(notice["text"], str) and 0 < len(notice["text"].encode()) <= MAX_JSON and isinstance(notice["sha256"], str) and HASH.fullmatch(notice["sha256"]) and digest(notice["text"].encode()) == notice["sha256"], "SMTP third-party notice integrity mismatch.")
    verify_source(api, build, bundle, intent["source_sha"], provenance)
    require(resolve_tag(api, intent["tag"]) == intent["source_sha"], "Release tag changed during verification.")
    verified = {"source_sha": intent["source_sha"], "component": component, "version": version, "binary_sha256": binaries, "source_and_integrity_verified": True, "independent_publisher_signature": False}
    return (verified, bundle) if with_bundle else verified


def backup_evidence(path, expected, metadata_path, current_core, trust=_Trust()):
    require(isinstance(expected, str) and HASH.fullmatch(expected), "An explicit backup SHA-256 is required.")
    metadata_raw = read_protected(metadata_path, 16 << 10, trust, True)
    metadata = strict_json(metadata_raw, 16 << 10)
    require(isinstance(metadata, dict) and set(metadata) == {"schema", "source_commit", "core_version", "database", "schema_version", "migration_checksums"} and type(metadata["schema"]) is int and metadata["schema"] == 1 and isinstance(metadata["source_commit"], str) and SHA.fullmatch(metadata["source_commit"]) and metadata["core_version"] == current_core and isinstance(metadata["database"], str) and re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,63}", metadata["database"]) and metadata["database"].lower() not in {"mysql", "sys", "information_schema", "performance_schema"} and type(metadata["schema_version"]) is int and 0 < metadata["schema_version"] <= 64, "Backup schema metadata is incomplete or invalid.")
    migrations = metadata["migration_checksums"]
    require(isinstance(migrations, dict) and set(migrations) == {str(x) for x in range(1, metadata["schema_version"] + 1)} and all(isinstance(v, str) and HASH.fullmatch(v) for v in migrations.values()), "Backup migration history must be a complete checksum prefix.")
    descriptor = protected_open(path, trust=trust, private=True)
    try:
        before = os.fstat(descriptor)
        require(0 < before.st_size <= MAX_BACKUP, "Backup exceeds its size bound.")
        checksum, decoder, expanded, compressed = hashlib.sha256(), zlib.decompressobj(16 + zlib.MAX_WBITS), 0, 0
        with os.fdopen(descriptor, "rb", closefd=False) as stream:
            for chunk in iter(lambda: stream.read(65536), b""):
                checksum.update(chunk)
                compressed += len(chunk)
                require(compressed <= MAX_BACKUP, "Backup exceeds its compressed size bound.")
                pending = chunk
                while pending:
                    output = decoder.decompress(pending, min(1 << 20, MAX_BACKUP_EXPANDED + 1 - expanded))
                    expanded += len(output)
                    require(expanded <= MAX_BACKUP_EXPANDED and not decoder.unused_data, "Backup gzip expansion or framing exceeds its bound.")
                    pending = decoder.unconsumed_tail
        require(decoder.eof and expanded > 0 and checksum.hexdigest() == expected and unchanged(before, os.fstat(descriptor)), "Backup gzip, checksum or stable input verification failed.")
    except zlib.error as exc:
        raise Rejected("Backup gzip integrity verification failed.") from exc
    finally:
        os.close(descriptor)
    return {"sha256": expected, "compressed_size": before.st_size, "gzip_integrity_verified": True, "metadata_sha256": digest(metadata_raw), "schema_version": metadata["schema_version"], "source_commit": metadata["source_commit"], "database_contents_verified": False, "isolated_restore_verified": False}


def json_bytes(value):
    return (json.dumps(value, sort_keys=True, ensure_ascii=False, indent=2) + "\n").encode()


def new_file(parent, name, raw):
    descriptor = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=parent)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(raw)
        stream.flush()
        os.fchmod(stream.fileno(), 0o600)
        os.fsync(stream.fileno())
    os.fsync(parent)


def atomic_json(parent, name, value):
    temporary = ".journal-" + secrets.token_hex(16)
    new_file(parent, temporary, json_bytes(value))
    try:
        os.replace(temporary, name, src_dir_fd=parent, dst_dir_fd=parent)
        os.fsync(parent)
    finally:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(temporary, dir_fd=parent)


@contextlib.contextmanager
def locked(parent, trust):
    descriptor = os.open(".update.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, 0o600, dir_fd=parent)
    try:
        info = os.fstat(descriptor)
        require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_uid == trust.owner and stat.S_IMODE(info.st_mode) == 0o600, "Unsafe update lock file.")
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise Rejected("Another update staging operation holds the exclusive lock.") from exc
        yield
    finally:
        os.close(descriptor)


def audit(parent, request_id, status_value, trust):
    descriptor = os.open("audit.jsonl", os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, 0o600, dir_fd=parent)
    try:
        info = os.fstat(descriptor)
        require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_uid == trust.owner and stat.S_IMODE(info.st_mode) == 0o600 and info.st_size <= 16 << 20, "Unsafe or full update audit file.")
        raw = (json.dumps({"schema": 1, "request_id": request_id, "status": status_value, "at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")}, sort_keys=True) + "\n").encode()
        require(os.write(descriptor, raw) == len(raw), "Update audit append was incomplete.")
        os.fsync(descriptor)
        os.fsync(parent)
    finally:
        os.close(descriptor)


def stage(handoff, state_directory, current_core, current_plugin="", api=None, backup=None, backup_sha256=None, backup_metadata=None, trust=_Trust(), now=None):
    raw = read_protected(handoff, 32 << 10, trust, True)
    intent = validate_handoff(raw, current_core, current_plugin, now)
    require(bool(backup) == bool(backup_sha256) == bool(backup_metadata), "Backup path, checksum and schema metadata must be supplied together.")
    require(intent["component"] == "core" or not backup, "Database backup evidence belongs to a core update handoff.")
    root = protected_open(state_directory, True, trust, True)
    request_fd = -1
    try:
        with locked(root, trust):
            request_id = intent["request_id"]
            try:
                os.mkdir(request_id, 0o700, dir_fd=root)
            except FileExistsError as exc:
                raise Rejected("This request already has a checkpoint; inspect status instead of overwriting it.") from exc
            os.fsync(root)
            request_fd = os.open(request_id, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=root)
            journal = {"schema": 1, "request_id": request_id, "actor_id": intent["actor_id"], "component": intent["component"], "tag": intent["tag"], "source_sha": intent["source_sha"], "handoff_sha256": digest(raw), "distribution_format": intent.get("distribution_format", "archive-v1"), "status": "staged", "history": ["staged"], "installation_changed": False, "service_action": "none", "database_action": "none", "authorization": "explicit_root_operator_invocation", "web_origin_signature_verified": False}
            atomic_json(request_fd, "JOURNAL.json", journal)
            try:
                audit(root, request_id, "staged", trust)
                api = Official() if api is None else api
                official_assets = release_assets(api, intent)
                files = {}
                for name, item in sorted(official_assets.items()):
                    limit = asset_limit(name)
                    files[name] = api.asset(item["id"], min(limit, item["size"]))
                verified = verify_downloads(api, intent, files, current_core)
                require(release_assets(api, intent) == official_assets, "Official release changed during staging.")
                validate_handoff(raw, current_core, current_plugin, now)
                for name, data in sorted(files.items()):
                    new_file(request_fd, name, data)
                journal.update(verification=verified, assets=intent["assets"], status="verified")
                journal["history"].append("verified")
                atomic_json(request_fd, "JOURNAL.json", journal)
                audit(root, request_id, "verified", trust)
                evidence = backup_evidence(backup, backup_sha256, backup_metadata, current_core, trust) if backup else None
                journal["backup"] = evidence if evidence is not None else {"supplied": False}
                journal["required_operator_checks"] = (["maintenance_window_and_stop_writes", "fresh_database_backup", "isolated_restore_and_business_verification", "schema_and_migration_checksums", "configuration_and_secret_compatibility", "installed_plugin_manifest_compatibility", "retain_old_release_and_approved_recovery_plan", "manual_install_migrate_switch_and_readiness"] if intent["component"] == "core" else ["retain_current_root_plugin_policy_and_revision", "review_approved_uid_socket_and_targets", "explicit_plugin_install_select_enable_and_worker_restart", "verify_handshake_and_core_health", "retain_previous_compatible_plugin_for_rollback"])
                journal["installed_plugin_compatibility_verified"] = not (intent["component"] == "core" and current_plugin)
                if intent["component"] == "core" and current_plugin:
                    journal["required_operator_checks"].append("independently_verify_root_approved_plugin_policy_manifest_hash_uid_revision_and_core_bounds")
                journal["status"] = "backup_required" if intent["component"] == "core" and evidence is None else "compatibility_review_required" if intent["component"] == "core" and current_plugin else "awaiting_host_operator"
                journal["history"].append(journal["status"])
                atomic_json(request_fd, "JOURNAL.json", journal)
                audit(root, request_id, journal["status"], trust)
                return journal
            except (Rejected, OSError, TypeError, ValueError, KeyError, AttributeError) as exc:
                journal["status"] = "rejected"
                journal["history"].append("rejected")
                with contextlib.suppress(OSError, Rejected):
                    atomic_json(request_fd, "JOURNAL.json", journal)
                    audit(root, request_id, "rejected", trust)
                if isinstance(exc, (Rejected, OSError)):
                    raise
                raise Rejected("Official metadata could not be safely verified.") from None
    finally:
        if request_fd >= 0:
            os.close(request_fd)
        os.close(root)


def status(state_directory, request_id, trust=_Trust()):
    require(isinstance(request_id, str) and REQUEST.fullmatch(request_id), "Invalid request ID.")
    root = protected_open(state_directory, True, trust, True)
    try:
        with locked(root, trust):
            raw = read_protected(exact_path(state_directory) + "/" + request_id + "/JOURNAL.json", 32 << 10, trust, True)
            result = strict_json(raw, 32 << 10)
            require(isinstance(result, dict) and result.get("request_id") == request_id and result.get("installation_changed") is False and isinstance(result.get("status"), str) and result["status"] in {"staged", "verified", "backup_required", "compatibility_review_required", "awaiting_host_operator", "rejected"}, "Invalid update checkpoint.")
            if result["status"] in {"verified", "backup_required", "compatibility_review_required", "awaiting_host_operator"}:
                component = result.get("component")
                require(isinstance(component, str) and component in {"core", "smtp"} and isinstance(result.get("tag"), str), "Invalid update checkpoint component.")
                version = result["tag"][6:] if component == "smtp" else result["tag"][1:]
                assets = asset_inventory(result.get("assets"), component, version, result.get("distribution_format", "archive-v1"))
                for name, item in assets.items():
                    data = read_protected(state_directory + "/" + request_id + "/" + name, asset_limit(name), trust, True)
                    require(len(data) == item["size"] and digest(data) == item["sha256"], "Staged assets changed after verification.")
            result["operator_notice"] = "No program was installed or executed. Interrupted checkpoints require review; never infer successful application."
            return result
    finally:
        os.close(root)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    staging = commands.add_parser("stage", help="Independently verify and stage a fixed official release.")
    staging.add_argument("--handoff", required=True)
    staging.add_argument("--state-directory", default="/var/lib/aster-updates")
    staging.add_argument("--current-core-version", required=True)
    staging.add_argument("--current-plugin-version", default="")
    staging.add_argument("--github-token-file")
    staging.add_argument("--backup")
    staging.add_argument("--backup-sha256")
    staging.add_argument("--backup-metadata")
    viewing = commands.add_parser("status", help="Read an existing checkpoint without applying it.")
    viewing.add_argument("--state-directory", default="/var/lib/aster-updates")
    viewing.add_argument("--request-id", required=True)
    options = parser.parse_args(argv)
    try:
        require(sys.platform == "linux" and os.geteuid() == 0, "Run this Linux operator tool explicitly as root.")
        os.umask(0o077)
        if options.action == "status":
            result = status(options.state_directory, options.request_id)
        else:
            token = ""
            if options.github_token_file:
                token = load_protected_token(options.github_token_file)
            result = stage(options.handoff, options.state_directory, options.current_core_version, options.current_plugin_version, Official(token), options.backup, options.backup_sha256, options.backup_metadata)
        print(json.dumps(result, sort_keys=True, ensure_ascii=False))
        return 0
    except (Rejected, OSError, TypeError, ValueError, KeyError, AttributeError) as exc:
        print("Update handoff rejected: " + (str(exc) if isinstance(exc, Rejected) else "Protected filesystem operation failed."), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
