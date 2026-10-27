#!/usr/bin/env python3
"""Fail-closed runtime release gate. Standard library only; artifact bytes are never executed.

The publishing workflow must run this file from the trusted default-branch commit.
Build code and dependencies run in a separate job with read-only credentials.
"""
import argparse
import base64
import binascii
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import stat
import struct
import sys
import tarfile
import urllib.error
import urllib.parse
import urllib.request
import zipfile

REPOSITORY = "xiaoqiqixi/betteridcsys"
REPOSITORY_ID = 1418618420
BUILD_WORKFLOW = ".github/workflows/release-build.yml"
PUBLISH_WORKFLOW = ".github/workflows/release-publish.yml"
FINANCIAL_CI_JOBS = frozenset({
    "checks", "Native financial acceptance (ubuntu-22.04)", "go-vulnerability",
    "official-release-metadata", "Real MySQL financial browser acceptance",
})
TRUSTED_PATHS = (
    BUILD_WORKFLOW,
    ".github/workflows/ci.yml",
    "vps-billing/deploy/ci/release_pipeline.py",
    "vps-billing/deploy/ci/historical-artifacts.json",
    "vps-billing/deploy/ci/check.sh",
    "vps-billing/deploy/ci/prepare-native-runner.sh",
    "vps-billing/deploy/ci/prepare-browser.py",
    "vps-billing/deploy/ci/accept-native.sh",
    "vps-billing/deploy/ci/accept-native-http.py",
    "vps-billing/deploy/ci/plugin-native.sh",
    "vps-billing/deploy/ci/plugin-native.py",
    "vps-billing/deploy/ci/payment-native.sh",
    "vps-billing/deploy/ci/payment-native.py",
    "vps-billing/deploy/ci/test_payment_native.py",
    "vps-billing/deploy/ci/smtp-loopback-fixture.py",
    "vps-billing/deploy/ci/test_plugin_native.py",
    "vps-billing/deploy/ci/browser-demo.py",
    "vps-billing/deploy/ci/browser-mysql.sh",
    "vps-billing/deploy/ci/browser-mysql.py",
    "vps-billing/deploy/ci/browser-mysql-fixture.gotxt",
    "vps-billing/frontend/tests/browser-mysql.mjs",
    "vps-billing/frontend/tests/ui-copy-boundaries.mjs",
    "vps-billing/deploy/ci/mysql-native.sh",
    "vps-billing/deploy/ci/mysql-distribution.py",
    "vps-billing/deploy/ci/mysql-distribution.json",
    "vps-billing/deploy/ci/mysql-distribution-ubuntu22.json",
    "vps-billing/deploy/scripts/package-release.sh",
    "vps-billing/deploy/scripts/release-manifest.py",
    "vps-billing/deploy/scripts/plugin-manager.py",
    "vps-billing/deploy/scripts/update-manager.py",
    "vps-billing/deploy/scripts/test-update-manager.py",
    "vps-billing/deploy/scripts/core-manager.py",
    "vps-billing/deploy/scripts/test-core-manager.py",
    "vps-billing/deploy/scripts/core-operation.py",
    "vps-billing/deploy/scripts/test-core-operation.py",
    "vps-billing/deploy/ci/core-native.sh",
    "vps-billing/deploy/ci/core-native.py",
    "vps-billing/deploy/systemd/aster-api.service",
    "vps-billing/deploy/systemd/aster-smtp.service",
    "vps-billing/deploy/systemd/aster-payment.service",
)
MAX_ZIP = 256 * 1024 * 1024
MAX_TAR = 256 * 1024 * 1024
MAX_FILE = 64 * 1024 * 1024
MAX_JSON = 2 * 1024 * 1024
MAX_MEMBERS = 20000
MAX_HISTORICAL_FILE = 8 * 1024 * 1024
MAX_HISTORICAL_ZIP = 16 * 1024 * 1024
HISTORICAL_MANIFEST = Path(__file__).with_name("historical-artifacts.json")
SHA = re.compile(r"^[0-9a-f]{40}$")
HASH = re.compile(r"^[0-9a-f]{64}$")
NUMBER = r"(?:0|[1-9][0-9]*)"
TAG = re.compile(rf"^(?:v{NUMBER}\.{NUMBER}\.{NUMBER}(?:-(?:alpha|beta|rc)\.{NUMBER})?|smtp-v{NUMBER}\.{NUMBER}\.{NUMBER})$")
INPUT_PATHS = (
    "LICENSE", "README.md", "vps-billing/backend/go.mod",
    "vps-billing/backend/go.sum", "vps-billing/frontend/package.json",
    "vps-billing/frontend/package-lock.json",
)

CURRENT_INPUT_PATHS = INPUT_PATHS + ("vps-billing/backend/pkg/pluginapi/protocol.go", "vps-billing/backend/plugins/smtp/VERSION")
ROOT_LICENSE_HASH = "c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4"
SMTP_FILES = {"plugin", "manifest.json", "LICENSE", "README.md", "THIRD_PARTY_NOTICES.json", "SHA256SUMS"}
CORE_DEPLOY_FILES = {"deploy/systemd/aster-api.service", "deploy/systemd/aster-smtp.service", "deploy/systemd/api.env.example", "deploy/nginx.native.conf", "deploy/scripts/backup-native.sh", "deploy/scripts/plugin-manager.py", "deploy/scripts/update-manager.py", "deploy/scripts/core-manager.py"}


def core_deploy_files(version):
    selected = tuple(map(int, version.split(".")))
    return CORE_DEPLOY_FILES | ({"deploy/scripts/core-operation.py"} if selected >= (0, 5, 2) else set()) | ({"deploy/systemd/aster-payment.service"} if selected >= (0, 5, 3) else set())


class GuardError(Exception):
    pass


class APIError(GuardError):
    def __init__(self, status, message="GitHub request failed"):
        self.status = status
        super().__init__(f"{message} (HTTP {status})")


def require(condition, message):
    if not condition:
        raise GuardError(message)


def integer(value, name):
    require(type(value) is int and value > 0, f"Invalid {name}")
    return value


def sha(value):
    require(isinstance(value, str) and SHA.fullmatch(value), "Invalid commit/blob SHA")
    return value


def validate_tag(tag):
    require(isinstance(tag, str) and len(tag) <= 64 and TAG.fullmatch(tag),
            "Only exact core vMAJOR.MINOR.PATCH or smtp-vMAJOR.MINOR.PATCH release tags are allowed; historical prereleases remain readable")
    return tag[6:] if tag.startswith("smtp-v") else tag[1:]


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def strict_json(data, limit=MAX_JSON):
    require(isinstance(data, bytes) and len(data) <= limit, "JSON exceeds limit")
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "Duplicate JSON key")
            result[key] = value
        return result
    def invalid_constant(_):
        raise GuardError("Non-finite JSON value")
    try:
        return json.loads(data.decode("utf-8"), object_pairs_hook=pairs,
                          parse_constant=invalid_constant)
    except (ValueError, UnicodeError, RecursionError) as exc:
        raise GuardError("Invalid JSON") from exc


def json_bytes(value):
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class GitHub:
    """Fixed API/upload hosts, no mutation retries, no credential-bearing redirects."""
    def __init__(self, token):
        require(isinstance(token, str) and token and "\n" not in token, "GH_TOKEN is required")
        self.token = token
        self.opener = urllib.request.build_opener(NoRedirect())
        self.created_releases = set()

    def _open(self, method, url, data=None, content_type=None, authenticated=True, limit=MAX_JSON):
        require(isinstance(url, str) and all(32 < ord(char) < 127 for char in url), "Unsafe GitHub URL characters")
        parsed = urllib.parse.urlsplit(url)
        require(parsed.scheme == "https" and not parsed.username and not parsed.password and
                not parsed.fragment and parsed.port in (None, 443), "Unsafe GitHub URL")
        if authenticated:
            require(parsed.hostname in {"api.github.com", "uploads.github.com"}, "Unsafe authenticated host")
        headers = {"User-Agent": "betteridcsys-release-guard", "Accept": "application/vnd.github+json",
                   "X-GitHub-Api-Version": "2022-11-28"}
        if authenticated:
            headers["Authorization"] = "Bearer " + self.token
        if content_type:
            headers["Content-Type"] = content_type
        request = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=60) as response:
                raw = response.read(limit + 1)
                require(len(raw) <= limit, "GitHub response exceeds limit")
                return response.status, response.headers, raw
        except urllib.error.HTTPError as exc:
            if exc.code in (301, 302, 303, 307, 308):
                return exc.code, exc.headers, b""
            # Never expose response bodies, signed URLs, token strings or provider messages.
            raise APIError(exc.code) from None
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            raise GuardError("GitHub request outcome uncertain; no mutation was retried") from None

    def request(self, method, path, data=None, raw=None, content_type=None):
        require(path.startswith("/repos/" + REPOSITORY + "/") or path == "/repos/" + REPOSITORY,
                "API path outside the fixed repository")
        require("#" not in path and "\n" not in path and not path.startswith("//"), "Unsafe API path")
        require(method in {"GET", "POST", "PATCH"}, "Unsupported API method")
        if method == "PATCH":
            match = re.fullmatch(re.escape(api_path("/releases/")) + r"([1-9][0-9]*)", path)
            require(match is not None and int(match[1]) in self.created_releases and
                    isinstance(data, dict) and set(data) == {"draft", "prerelease", "make_latest"} and
                    data["draft"] is False and data["prerelease"] is False and data["make_latest"] in {"true", "false"} and
                    raw is None and content_type is None,
                    "Only publication of this process's newly created verified release is a permitted PATCH")
        require(method != "POST" or path == api_path("/releases"), "Only staging a new release is a permitted POST mutation")
        payload = json_bytes(data) if data is not None else raw
        status, _, response = self._open(method, "https://api.github.com" + path, payload,
                                        "application/json" if data is not None else content_type)
        require(status == (201 if method == "POST" else 200), "Unexpected GitHub API response")
        result = strict_json(response)
        if method == "POST":
            self.created_releases.add(integer(result.get("id"), "created release ID"))
        return result

    def upload(self, release_id, name, data):
        integer(release_id, "release ID")
        require(isinstance(name, str) and "/" not in name and "\\" not in name, "Invalid asset name")
        path = f"/repos/{REPOSITORY}/releases/{release_id}/assets?name=" + urllib.parse.quote(name, safe="")
        status, _, raw = self._open("POST", "https://uploads.github.com" + path, data,
                                    "application/octet-stream")
        require(status == 201, "Unexpected asset upload response")
        return strict_json(raw)

    def historical_blob(self, blob_sha, expected_size):
        """Bounded read of one fixed archival blob; never execute its contents."""
        sha(blob_sha)
        require(type(expected_size) is int and 0 < expected_size <= MAX_HISTORICAL_FILE,
                "Invalid historical blob size")
        limit = expected_size * 2 + 8192  # Base64, line wrapping and fixed metadata.
        status, _, raw = self._open("GET", "https://api.github.com" + api_path("/git/blobs/" + blob_sha), limit=limit)
        require(status == 200, "Historical blob download failed")
        value = strict_json(raw, limit=limit)
        require(isinstance(value, dict) and value.get("sha") == blob_sha and
                type(value.get("size")) is int and value["size"] == expected_size and
                value.get("encoding") == "base64" and isinstance(value.get("content"), str),
                "Historical blob identity/encoding mismatch")
        try:
            data = base64.b64decode(value["content"].replace("\n", "").replace("\r", ""), validate=True)
        except (binascii.Error, ValueError) as exc:
            raise GuardError("Invalid historical blob encoding") from exc
        require(len(data) == expected_size and git_blob_sha(data) == blob_sha,
                "Historical blob size/hash mismatch")
        return data

    def download_artifact(self, artifact):
        artifact_id = integer(artifact["id"], "artifact ID")
        url = f"https://api.github.com/repos/{REPOSITORY}/actions/artifacts/{artifact_id}/zip"
        status, headers, raw = self._open("GET", url, limit=MAX_ZIP)
        if status == 302:
            url = headers.get("Location", "")
            parsed = urllib.parse.urlsplit(url)
            host = parsed.hostname or ""
            # The artifact service redirects to a signed storage URL. The token
            # is never forwarded. Reject further redirects and arbitrary hosts.
            require(host.endswith(".blob.core.windows.net") or host.endswith(".actions.githubusercontent.com"),
                    "Unrecognized signed artifact storage host")
            status, _, raw = self._open("GET", url, authenticated=False, limit=MAX_ZIP)
        require(status == 200, "Artifact download redirect/status rejected")
        verify_zip_digest(raw, artifact)
        return raw


def api_path(suffix=""):
    return "/repos/" + REPOSITORY + suffix


def git_blob_sha(data):
    return hashlib.sha1(b"blob " + str(len(data)).encode("ascii") + b"\0" + data).hexdigest()


def historical_manifest():
    value = strict_json(HISTORICAL_MANIFEST.read_bytes())
    require(isinstance(value, dict) and set(value) == {"schema", "repository", "repository_id", "release_tag", "archives"} and
            type(value["schema"]) is int and value["schema"] == 1 and
            value["repository"] == REPOSITORY and type(value["repository_id"]) is int and
            value["repository_id"] == REPOSITORY_ID and value["release_tag"] == "v0.3.0-rc.3",
            "Invalid historical archive policy")
    require(isinstance(value["archives"], list) and len(value["archives"]) == 2,
            "Historical policy must preserve both candidates")
    versions = set()
    for entry in value["archives"]:
        require(isinstance(entry, dict) and set(entry) == {"version", "original_source_commit", "artifact_commit", "files", "attachment"},
                "Invalid historical archive entry")
        version = entry["version"]
        require(version in {"0.3.0-rc.1", "0.3.0-rc.2"} and version not in versions,
                "Unexpected/duplicate historical version")
        versions.add(version)
        sha(entry["original_source_commit"])
        sha(entry["artifact_commit"])
        expected = set(package_files(version)) | {"README.md"}
        require(isinstance(entry["files"], dict) and set(entry["files"]) == expected,
                "Historical file inventory mismatch")
        for meta in entry["files"].values():
            require(isinstance(meta, dict) and set(meta) == {"git_blob", "sha256", "size"},
                    "Invalid historical file metadata")
            sha(meta["git_blob"])
            require(isinstance(meta["sha256"], str) and HASH.fullmatch(meta["sha256"]) and
                    type(meta["size"]) is int and 0 < meta["size"] <= MAX_HISTORICAL_FILE,
                    "Invalid historical file hash/size")
        attachment = entry["attachment"]
        require(isinstance(attachment, dict) and set(attachment) == {"name", "sha256", "size"} and
                attachment["name"] == f"historical-v{version}.zip" and
                isinstance(attachment["sha256"], str) and HASH.fullmatch(attachment["sha256"]) and
                type(attachment["size"]) is int and 0 < attachment["size"] <= MAX_HISTORICAL_ZIP,
                "Invalid historical attachment metadata")
    return value


def historical_zip(entry, files):
    """Original five files stay byte-exact; metadata labels the wrapper as archival."""
    require(set(files) == set(entry["files"]), "Historical ZIP file set mismatch")
    metadata = {
        "schema": 1, "kind": "historical-unmodified-candidate",
        "version": entry["version"], "original_source_commit": entry["original_source_commit"],
        "original_artifact_commit": entry["artifact_commit"], "files": entry["files"],
        "notice": "Historical preservation only; not rebuilt, not the current release source, and not recommended for installation. Original README and BUILD statements describe their historical snapshot and may be superseded. This archive has no current-build provenance claim.",
    }
    members = {f"v{entry['version']}/" + name: data for name, data in files.items()}
    members["ARCHIVE.json"] = json_bytes(metadata)
    result = io.BytesIO()
    with zipfile.ZipFile(result, "w", compression=zipfile.ZIP_STORED, allowZip64=False) as archive:
        for name, data in sorted(members.items()):
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.create_system = 3
            info.external_attr = (stat.S_IFREG | 0o644) << 16
            archive.writestr(info, data, compress_type=zipfile.ZIP_STORED)
    data = result.getvalue()
    require(len(data) <= MAX_HISTORICAL_ZIP, "Historical ZIP exceeds limit")
    return data


def historical_attachments(api, tag):
    """All network/data validation completes before release creation; no old code runs."""
    manifest = historical_manifest()
    if tag != manifest["release_tag"]:
        return {}, ""
    result, descriptions = {}, []
    for entry in manifest["archives"]:
        tree = api.request("GET", api_path("/git/trees/" + entry["artifact_commit"] + "?recursive=1"))
        require(isinstance(tree, dict) and tree.get("truncated") is False and isinstance(tree.get("tree"), list),
                "Incomplete historical artifact tree")
        actual = {}
        for item in tree["tree"]:
            require(isinstance(item, dict) and item.get("type") == "blob" and item.get("mode") == "100644" and
                    item.get("path") in entry["files"] and item["path"] not in actual,
                    "Historical artifact tree has unexpected files/types")
            actual[item["path"]] = item.get("sha")
        require(actual == {name: meta["git_blob"] for name, meta in entry["files"].items()},
                "Historical artifact tree differs from pinned inventory")
        files = {}
        for name, meta in sorted(entry["files"].items()):
            data = api.historical_blob(meta["git_blob"], meta["size"])
            require(isinstance(data, bytes) and len(data) == meta["size"] and
                    sha256(data) == meta["sha256"] and git_blob_sha(data) == meta["git_blob"],
                    "Historical asset differs from pinned bytes")
            scan_obvious_secrets(data)
            files[name] = data
        build = strict_json(files["BUILD.json"])
        require(isinstance(build, dict) and build.get("source_commit") == entry["original_source_commit"] and
                build.get("release_label") == entry["version"], "Historical BUILD identity mismatch")
        checksums = {}
        for line in files["SHA256SUMS"].decode("ascii").splitlines():
            match = re.fullmatch(r"([0-9a-f]{64})  (?:\./)?([A-Za-z0-9._-]+)", line)
            require(match is not None and match[2] not in checksums, "Invalid historical checksums")
            checksums[match[2]] = match[1]
        require(set(checksums) == set(package_files(entry["version"])) - {"SHA256SUMS"} and
                all(sha256(files[name]) == value for name, value in checksums.items()),
                "Historical checksum inventory/content mismatch")
        data = historical_zip(entry, files)
        meta = entry["attachment"]
        require(len(data) == meta["size"] and sha256(data) == meta["sha256"],
                "Historical ZIP differs from pinned output")
        result[meta["name"]] = data
        descriptions.append(f"- `{meta['name']}`：原始源码 `{entry['original_source_commit']}`；SHA-256 `{meta['sha256']}`。")
    body = "\n\n历史保留附件（不建议安装）：以下 ZIP 原样保留旧候选的五个文件，包括原始 README、BUILD 和校验和；没有重新构建或改写旧 SHA，与当前标签的源码/构建来源记录不同。ZIP 内 ARCHIVE.json 列出每个原文件的哈希、大小和原始 Git blob。\n" + "\n".join(descriptions)
    return result, body


def repository(api):
    repo = api.request("GET", api_path())
    require(repo.get("id") == REPOSITORY_ID and repo.get("full_name") == REPOSITORY and
            repo.get("private") is False and repo.get("archived") is not True and
            repo.get("disabled") is not True, "Repository identity/visibility check failed")
    branch = repo.get("default_branch")
    require(isinstance(branch, str) and re.fullmatch(r"[A-Za-z0-9._/-]{1,200}", branch) and
            ".." not in branch and "//" not in branch, "Invalid default branch")
    return branch


def resolve_ref(api, ref):
    require(ref.startswith(("heads/", "tags/")), "Unsupported ref")
    response = api.request("GET", api_path("/git/ref/" + urllib.parse.quote(ref, safe="/")))
    require(response.get("ref") == "refs/" + ref, "Unexpected Git ref")
    obj = response.get("object", {})
    seen = set()
    for _ in range(8):
        value = sha(obj.get("sha"))
        require(value not in seen, "Cyclic annotated tag")
        seen.add(value)
        if obj.get("type") == "commit":
            return value
        require(obj.get("type") == "tag" and ref.startswith("tags/"), "Ref is not a commit")
        tagged = api.request("GET", api_path("/git/tags/" + value))
        require(tagged.get("sha") == value, "Unexpected annotated tag object")
        obj = tagged.get("object", {})
    raise GuardError("Annotated tag nesting exceeds limit")


def ancestor(api, source, default):
    response = api.request("GET", api_path(f"/compare/{sha(source)}...{sha(default)}"))
    require(response.get("status") in {"ahead", "identical"} and
            response.get("merge_base_commit", {}).get("sha") == source,
            "Release source is not an ancestor of the trusted default commit")


def trusted_blobs(api, commit):
    tree = api.request("GET", api_path("/git/trees/" + sha(commit) + "?recursive=1"))
    require(tree.get("truncated") is False and isinstance(tree.get("tree"), list), "Incomplete Git tree")
    found = {}
    for entry in tree["tree"]:
        path = entry.get("path")
        if path in TRUSTED_PATHS:
            require(path not in found and entry.get("type") == "blob" and
                    entry.get("mode") in {"100644", "100755"}, "Untrusted guard path type")
            found[path] = sha(entry.get("sha"))
    require(set(found) == set(TRUSTED_PATHS), "Trusted build policy files are missing")
    return found


def verify_source_archive(api, files, provenance):
    """Bind copied runtime texts and BUILD inputs to exact Git; legacy source archives remain readable."""
    if "-" not in provenance["version"]:
        return verify_runtime_source(api, files, provenance)
    version = provenance["version"]
    source = tar_files(files[f"aster-{version}-source.tar.gz"], f"aster-{version}-source")
    tree = api.request("GET", api_path("/git/trees/" + sha(provenance["source_sha"]) + "?recursive=1"))
    require(tree.get("truncated") is False and isinstance(tree.get("tree"), list), "Incomplete source Git tree")
    pruned_directories = {".git", "node_modules", "dist", "bin", "artifacts", "secrets", "backups", "test-results", "playwright-report", "__pycache__"}
    expected = {}
    for entry in tree["tree"]:
        path = entry.get("path")
        if not isinstance(path, str) or not (path in {"LICENSE", "README.md"} or path.startswith("vps-billing/")):
            continue
        parts = path.split("/")
        name = parts[-1]
        if pruned_directories.intersection(parts[:-1]) or ((name.startswith(".env") and name != ".env.example") or
                name.endswith((".log", ".tsbuildinfo", ".sql.gz", "-results.txt", ".pyc"))):
            continue
        if entry.get("type") == "tree":
            continue
        require(entry.get("type") == "blob" and entry.get("mode") in {"100644", "100755"} and path not in expected,
                "Source contains links/submodules/duplicate Git paths")
        expected[path] = sha(entry.get("sha"))
    require(set(source) == set(expected), "Source archive differs from the exact retained Git file set")
    for path, value in source.items():
        git_hash = hashlib.sha1(b"blob " + str(len(value)).encode("ascii") + b"\0" + value).hexdigest()
        require(git_hash == expected[path], "Source file differs from the tagged Git blob: " + path)


def build_workflow(api):
    result = api.request("GET", api_path("/actions/workflows/release-build.yml"))
    require(result.get("path") == BUILD_WORKFLOW and result.get("state") == "active",
            "Unexpected or inactive build workflow")
    return integer(result.get("id"), "build workflow ID")


def validate_run(api, run_id, attempt, completed):
    run = api.request("GET", api_path(f"/actions/runs/{integer(run_id, 'run ID')}"))
    require(run.get("id") == run_id and run.get("run_attempt") == attempt and
            run.get("workflow_id") == build_workflow(api) and run.get("path") == BUILD_WORKFLOW,
            "Untrusted workflow run identity or attempt")
    for field in ("repository", "head_repository"):
        identity = run.get(field, {})
        require(identity.get("id") == REPOSITORY_ID and identity.get("full_name") == REPOSITORY,
                "Workflow run originated outside the fixed repository")
    require(run.get("event") in {"push", "workflow_dispatch"}, "Untrusted build event")
    sha(run.get("head_sha"))
    if completed:
        require(run.get("status") == "completed" and run.get("conclusion") == "success",
                "Build run did not complete successfully")
    else:
        require(run.get("status") in {"queued", "in_progress"}, "Build run is not active")
    return run


def environment_identity(env):
    require(env.get("GITHUB_REPOSITORY") == REPOSITORY and
            env.get("GITHUB_REPOSITORY_ID") == str(REPOSITORY_ID), "Environment repository mismatch")
    return integer(int(env.get("GITHUB_RUN_ID", "0")), "run ID"), integer(int(env.get("GITHUB_RUN_ATTEMPT", "0")), "run attempt")


def green_financial_ci(api, source):
    """No mutation: require all native/safety/browser gates at this main SHA.

    An older success, a successful demo substituted for the real API browser,
    a skipped/missing Jammy job or a latest failed attempt cannot authorize a
    release. The current publisher rechecks this before its bounded mutations.
    """
    source = sha(source)
    workflow = api.request("GET", api_path("/actions/workflows/ci.yml"))
    workflow_id = integer(workflow.get("id"), "required CI workflow ID")
    require(workflow.get("path") == ".github/workflows/ci.yml" and workflow.get("state") == "active",
            "Required financial CI workflow identity mismatch")
    result = api.request("GET", api_path("/actions/workflows/ci.yml/runs?branch=main&event=push&head_sha=" + source + "&per_page=100"))
    rows, count = result.get("workflow_runs"), result.get("total_count")
    require(isinstance(rows, list) and type(count) is int and 0 <= count <= 100 and count == len(rows) and
            all(isinstance(row, dict) for row in rows), "Required financial CI run list incomplete")
    matches = [row for row in rows if row.get("head_sha") == source and row.get("head_branch") == "main" and
               row.get("event") == "push" and row.get("workflow_id") == workflow_id]
    require(matches, "No required financial exact-main-SHA CI run")
    run = max(matches, key=lambda row: integer(row.get("id"), "required CI run ID"))
    run_id = integer(run["id"], "required CI run ID")
    attempt = integer(run.get("run_attempt"), "required CI attempt")
    require(run.get("status") == "completed" and run.get("conclusion") == "success",
            "Latest required financial exact-SHA CI run is not successful")
    result = api.request("GET", api_path(f"/actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100"))
    jobs = result.get("jobs")
    require(type(result.get("total_count")) is int and result["total_count"] == len(FINANCIAL_CI_JOBS) and
            isinstance(jobs, list) and len(jobs) == len(FINANCIAL_CI_JOBS) and
            all(isinstance(job, dict) and isinstance(job.get("name"), str) for job in jobs) and
            {job.get("name") for job in jobs} == FINANCIAL_CI_JOBS and
            all(job.get("status") == "completed" and job.get("conclusion") == "success" for job in jobs),
            "Every exact-SHA financial CI job must pass, including Ubuntu22 native and real MySQL browser")
    return {"workflow": "ci.yml", "source_sha": source, "run_id": run_id, "attempt": attempt,
            "jobs": sorted(FINANCIAL_CI_JOBS)}


def prepare_build(api, env, event):
    run_id, attempt = environment_identity(env)
    branch = repository(api)
    default = resolve_ref(api, "heads/" + branch)
    # This step executes before source/dependency code with contents/actions
    # read only. The publisher also verifies the build identity against Actions.
    event_name = env.get("GITHUB_EVENT_NAME")
    require(event_name in {"push", "workflow_dispatch"}, "Untrusted build event")
    run = {"event": event_name, "head_sha": sha(env.get("GITHUB_SHA"))}
    require(env.get("GITHUB_WORKFLOW_SHA") == run["head_sha"], "Build environment/workflow mismatch")
    if run["event"] == "push":
        ref = event.get("ref")
        require(isinstance(ref, str) and ref.startswith("refs/tags/") and env.get("GITHUB_REF") == ref and
                event.get("deleted") is False, "Build requires an existing tag push")
        tag = ref[len("refs/tags/"):]
    else:
        require(env.get("GITHUB_REF") == "refs/heads/" + branch and run["head_sha"] == default,
                "Manual builds must run from the current default branch")
        tag = event.get("inputs", {}).get("tag")
    version = validate_tag(tag)
    source = resolve_ref(api, "tags/" + tag)
    if run["event"] == "push":
        require(source == run["head_sha"], "Tag does not identify this build commit")
    ancestor(api, source, default)
    blobs = trusted_blobs(api, default)
    require(trusted_blobs(api, run["head_sha"]) == blobs and trusted_blobs(api, source) == blobs,
            "Build/source guard policy differs from the default branch")
    green_financial_ci(api, source)
    return {"schema": 1, "repository": REPOSITORY, "repository_id": REPOSITORY_ID,
            "run_id": run_id, "run_attempt": attempt, "event": run["event"],
            "tag": tag, "version": version, "source_sha": source,
            "run_head_sha": run["head_sha"], "workflow_sha": env["GITHUB_WORKFLOW_SHA"],
            "workflow_path": BUILD_WORKFLOW, "default_branch": branch, "default_sha": default,
            "trusted_blobs": blobs, "artifact_name": f"candidate-{run_id}-{attempt}"}


def release_kind(tag):
    validate_tag(tag)
    return "smtp" if tag.startswith("smtp-v") else "core"


def distribution_format(version, kind):
    if "-" in version:
        return "archive-v1"
    parts = tuple(map(int, version.split(".")))
    return "raw-binaries-v1" if parts >= ((0, 5, 0) if kind == "core" else (1, 1, 0)) else "archive-v1"


def binary_assets(version, kind):
    if kind == "core":
        return {"bin/aster-api": f"aster-api-{version}-linux-amd64", "bin/aster-plugin-host": f"aster-plugin-host-{version}-linux-amd64"}
    require(kind == "smtp", "Unknown release component")
    return {"plugin": f"aster-smtp-{version}-linux-amd64"}


def resources_asset(version, kind):
    return ("aster-core" if kind == "core" else "aster-smtp") + f"-{version}-linux-amd64-resources.tar.gz"


def package_files(version, kind="core", format=None):
    require(kind in {"core", "smtp"}, "Unknown release component")
    if "-" in version:
        require(kind == "core", "Historical SMTP prereleases do not exist")
        return ("BUILD.json", f"aster-{version}-linux-amd64.tar.gz", f"aster-{version}-source.tar.gz", "SHA256SUMS")
    format = distribution_format(version, kind) if format is None else format
    if format == "raw-binaries-v1":
        return ("BUILD.json", *binary_assets(version, kind).values(), resources_asset(version, kind), "SHA256SUMS")
    require(format == "archive-v1", "Unknown distribution format")
    prefix = "aster-smtp" if kind == "smtp" else "aster"
    return ("BUILD.json", f"{prefix}-{version}-linux-amd64.tar.gz", "SHA256SUMS")


def safe_path(name):
    require(isinstance(name, str) and name and len(name) <= 1024 and "\\" not in name and
            ":" not in name and not name.startswith("/") and not any(ord(c) < 32 or ord(c) == 127 for c in name),
            "Unsafe archive path")
    parts = name.rstrip("/").split("/")
    require(all(part not in {"", ".", ".."} for part in parts), "Archive path traversal/alias")
    forbidden = {".git", ".aws", ".ssh", "node_modules", "__pycache__", "secrets", "backups", "artifacts", "test-results", "playwright-report"}
    require(not forbidden.intersection(parts), "Forbidden local/secret directory in archive")
    basename = parts[-1].lower()
    require(not basename.endswith((".log", ".sql.gz", ".pyc", ".tsbuildinfo", "-results.txt", ".pem", ".key", ".p12", ".pfx")) and
            not (basename.startswith(".env") and basename != ".env.example") and
            not basename.endswith(".env"), "Forbidden secret/log file in archive")
    return parts


def scan_obvious_secrets(data):
    # A deliberately limited backstop, not a secret-scanning certification.
    require(not re.search(rb"(?m)^-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----\r?$", data) and
            not re.search(rb"\bgh[pousr]_[A-Za-z0-9]{30,}\b", data), "Private key/token material in artifact")


def tar_files(data, root):
    require(isinstance(data, bytes) and 0 < len(data) <= MAX_ZIP, "Invalid tar size")
    try:
        with gzip.GzipFile(fileobj=io.BytesIO(data)) as zipped:
            decoded = zipped.read(MAX_TAR + 1)
        require(len(decoded) <= MAX_TAR, "Expanded tar exceeds limit")
        result = {}
        seen = set()
        total = 0
        with tarfile.open(fileobj=io.BytesIO(decoded), mode="r:") as archive:
            for item in archive:
                require(len(seen) < MAX_MEMBERS, "Too many tar members")
                parts = safe_path(item.name)
                require(parts[0] == root and item.name.rstrip("/") not in seen, "Unexpected/duplicate tar member")
                seen.add(item.name.rstrip("/"))
                require(item.isdir() or item.isfile(), "Archive links/devices are forbidden")
                require(not item.mode & 0o7000 and not item.linkname, "Archive special permission bits/link metadata")
                if item.isdir():
                    require(item.size == 0, "Directory archive payload is forbidden")
                    continue
                require(0 <= item.size <= MAX_FILE, "Tar file exceeds limit")
                total += item.size
                require(total <= MAX_TAR, "Tar payload exceeds limit")
                value = archive.extractfile(item).read(MAX_FILE + 1)
                require(len(value) == item.size, "Truncated tar file")
                scan_obvious_secrets(value)
                result["/".join(parts[1:])] = value
            require(not decoded[archive.offset:].strip(b"\0"), "Trailing tar payload")
        return result
    except (tarfile.TarError, OSError, EOFError, ValueError) as exc:
        raise GuardError("Invalid gzip/tar archive") from exc


def validate_package(files, source_sha, version, kind="core", provenance_data=None):
    if "-" not in version:
        return validate_runtime_package(files, source_sha, version, kind, provenance_data)
    require(kind == "core", "Unexpected legacy component")
    sha(source_sha)
    validate_tag("v" + version)
    require(set(files) == set(package_files(version)), "Release package must contain exactly four expected files")
    require(all(isinstance(value, bytes) and 0 < len(value) <= MAX_ZIP for value in files.values()), "Invalid package file size")
    sums = files["SHA256SUMS"]
    require(len(sums) <= 4096, "Checksum file exceeds limit")
    entries = {}
    try:
        for line in sums.decode("ascii").splitlines():
            match = re.fullmatch(r"([0-9a-f]{64})  (?:\./)?([A-Za-z0-9._-]+)", line)
            require(match is not None and match[2] not in entries, "Invalid/duplicate checksum entry")
            entries[match[2]] = match[1]
    except UnicodeError as exc:
        raise GuardError("Non-ASCII checksum file") from exc
    require(set(entries) == set(files) - {"SHA256SUMS"}, "Checksum inventory mismatch")
    require(all(sha256(files[name]) == digest for name, digest in entries.items()), "Package checksum mismatch")
    build = strict_json(files["BUILD.json"])
    require(isinstance(build, dict) and build.get("source_commit") == source_sha and
            build.get("release_label") == version and build.get("target") == "linux/amd64" and
            build.get("cgo_enabled") is False, "BUILD identity/target mismatch")
    bundle = tar_files(files[f"aster-{version}-linux-amd64.tar.gz"], f"aster-{version}-linux-amd64")
    source = tar_files(files[f"aster-{version}-source.tar.gz"], f"aster-{version}-source")
    require(bundle.get("manifests/BUILD.json") == files["BUILD.json"], "Nested BUILD manifest mismatch")
    require("web/index.html" in bundle and "LICENSE" in bundle, "Runtime bundle is incomplete")
    binary = bundle.get("bin/aster-api", b"")
    require(len(binary) >= 64 and binary[:6] == b"\x7fELF\x02\x01" and binary[18:20] == b"\x3e\x00",
            "Runtime binary is not Linux amd64 ELF64")
    inputs = build.get("input_sha256")
    require(isinstance(inputs, dict) and set(inputs) == set(INPUT_PATHS), "BUILD input inventory mismatch")
    for path in INPUT_PATHS:
        require(path in source and HASH.fullmatch(str(inputs[path])) and sha256(source[path]) == inputs[path],
                "Source input checksum mismatch")
    require(bundle["LICENSE"] == source["LICENSE"], "Root LICENSE differs across archives")
    for name, data in files.items():
        if not name.endswith(".tar.gz"):
            scan_obvious_secrets(data)
    return {name: {"sha256": sha256(data), "size": len(data)} for name, data in sorted(files.items())}


def checksum_inventory(data, expected):
    require(isinstance(data, bytes) and len(data) <= 4096, "Checksum file exceeds limit")
    entries = {}
    try:
        for line in data.decode("ascii").splitlines():
            match = re.fullmatch(r"([0-9a-f]{64})  (?:\./)?([A-Za-z0-9._-]+)", line)
            require(match is not None and match[2] not in entries, "Invalid/duplicate checksum entry")
            entries[match[2]] = match[1]
    except UnicodeError as exc:
        raise GuardError("Non-ASCII checksum file") from exc
    require(set(entries) == set(expected), "Checksum inventory mismatch")
    return entries


def static_amd64(data):
    require(64 <= len(data) <= MAX_FILE and data[:7] == b"\x7fELF\x02\x01\x01" and data[7] in (0, 3), "Binary is not Linux amd64 ELF64 executable")
    kind, machine, revision, entry, programs, _, _, header_size, program_size, program_count = struct.unpack_from("<HHIQQQIHHH", data, 16)
    require(kind == 2 and machine == 62 and revision == 1 and header_size == 64 and program_size == 56 and
            1 <= program_count <= 128 and programs >= 64 and programs + program_size * program_count <= len(data), "Invalid ELF program headers")
    executable_entry = False
    for index in range(program_count):
        segment, flags, offset, address, _, size, memory, alignment = struct.unpack_from("<IIQQQQQQ", data, programs + index * program_size)
        require(segment not in (2, 3), "Dynamic executable loaders/dependencies are forbidden")
        require(offset + size <= len(data) and size <= memory and (alignment in (0, 1) or not alignment & (alignment - 1)), "Invalid ELF segment bounds")
        if segment == 1 and flags & 1 and address <= entry < address + memory:
            executable_entry = True
    require(executable_entry, "ELF entry point must be in an executable load segment")


def archive_permissions(data, root, executables):
    # tar_files performs the bounded parse first. Every shipped executable has an
    # explicit permission, and no ancillary file may carry executable bits.
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        for item in archive:
            if item.isfile():
                path = item.name[len(root) + 1:]
                require(item.uid == 0 and item.gid == 0 and item.mode == (0o755 if path in executables else 0o644), "Runtime archive file permission mismatch")
            elif item.isdir():
                require(item.uid == 0 and item.gid == 0 and item.mode == 0o755, "Runtime directory permission/owner mismatch")


def runtime_bundle(files, version, kind):
    build = strict_json(files["BUILD.json"])
    format = build.get("distribution_format", "archive-v1")
    if format == "raw-binaries-v1":
        name = resources_asset(version, kind)
        root = name[:-7]
        bundle = tar_files(files[name], root)
        require(not any(data.startswith(b"\x7fELF") for data in bundle.values()), "Resource archives cannot contain ELF executables")
        for logical, asset in binary_assets(version, kind).items():
            require(logical not in bundle, "Resource/raw executable inventory collision")
            bundle[logical] = files[asset]
        return root, files[name], bundle
    require(format == "archive-v1", "Unknown distribution format")
    root = ("aster-smtp" if kind == "smtp" else "aster") + "-" + version + "-linux-amd64"
    return root, files[root + ".tar.gz"], tar_files(files[root + ".tar.gz"], root)


def validate_runtime_package(files, source_sha, version, kind, provenance_data=None):
    sha(source_sha)
    require(kind in {"core", "smtp"}, "Unknown release component")
    validate_tag(("smtp-v" if kind == "smtp" else "v") + version)
    build = strict_json(files.get("BUILD.json", b""))
    require(isinstance(build, dict), "Invalid BUILD metadata")
    format = build.get("distribution_format", "archive-v1")
    raw_distribution = format == "raw-binaries-v1"
    require(isinstance(format, str) and format in {"archive-v1", "raw-binaries-v1"} and not (format == "archive-v1" and "distribution_format" in build), "Unknown/ambiguous distribution format")
    require(set(files) == set(package_files(version, kind, format)), "Runtime release asset inventory mismatch")
    require(all(isinstance(value, bytes) and 0 < len(value) <= MAX_ZIP for value in files.values()), "Invalid package file size")
    checksum_data = dict(files)
    if provenance_data is not None:
        require(isinstance(provenance_data, bytes) and 0 < len(provenance_data) <= MAX_JSON, "Invalid provenance data")
        checksum_data["PROVENANCE.json"] = provenance_data
    sums = checksum_inventory(files["SHA256SUMS"], set(checksum_data) - {"SHA256SUMS"})
    require(all(sha256(checksum_data[name]) == digest for name, digest in sums.items()), "Package checksum mismatch")
    build = strict_json(files["BUILD.json"])
    require(isinstance(build, dict) and type(build.get("schema")) is int and build["schema"] == (3 if raw_distribution else 2) and
            build.get("release_kind") == kind and build.get("source_commit") == source_sha and
            build.get("release_label") == version and build.get("target") == "linux/amd64" and build.get("cgo_enabled") is False,
            "BUILD identity/target/component mismatch")
    versions = build.get("component_versions")
    require(isinstance(versions, dict) and set(versions) == {"core", "smtp"} and versions.get(kind) == version,
            "Source component version mismatch")
    for key, value in versions.items():
        validate_tag(("smtp-v" if key == "smtp" else "v") + value)
        require("-" not in value, "Component must use stable semantic version")
    max_core = "0.6.0" if raw_distribution else "0.5.0"
    require(build.get("plugin_protocol") == "aster.smtp/1" and build.get("smtp_core_compatibility") == {"min": "0.4.0", "max_exclusive": max_core} and
            tuple(int(x) for x in versions["core"].split(".")) >= (0, 4, 0) and tuple(int(x) for x in versions["core"].split(".")) < tuple(map(int, max_core.split("."))),
            "Plugin protocol/core compatibility mismatch")
    if raw_distribution:
        require(build.get("binary_assets") == binary_assets(version, kind) and build.get("resources_asset") == resources_asset(version, kind), "Raw asset names or logical mapping mismatch")
        require(not set(build).intersection({"resources_sha256", "resources_size", "resource_sha256", "resource_size"}), "Resource self-hash metadata is forbidden")
    root, raw, bundle = runtime_bundle(files, version, kind)
    require(sha256(bundle.get("LICENSE", b"")) == ROOT_LICENSE_HASH, "Root LICENSE must remain unchanged")
    inputs = build.get("input_sha256")
    require(isinstance(inputs, dict) and set(inputs) == set(CURRENT_INPUT_PATHS) and all(isinstance(v, str) and HASH.fullmatch(v) for v in inputs.values()), "BUILD input inventory mismatch")
    require(inputs["LICENSE"] == ROOT_LICENSE_HASH, "Root LICENSE input mismatch")
    copies = build.get("source_files")
    require(isinstance(copies, dict) and copies.get("LICENSE") == "LICENSE" and all(isinstance(k, str) and isinstance(v, str) for k, v in copies.items()), "BUILD copied source inventory mismatch")
    for path, source in copies.items():
        safe_path(path); safe_path(source)
        require(path in bundle and (source in {"LICENSE", "README.md"} or source.startswith("vps-billing/")), "Invalid copied source path")
    binaries = build.get("binary_sha256")
    required_binaries = {"bin/aster-api", "bin/aster-plugin-host"} if kind == "core" else {"plugin"}
    require(isinstance(binaries, dict) and set(binaries) == required_binaries and all(isinstance(v, str) and HASH.fullmatch(v) for v in binaries.values()), "Binary inventory mismatch")
    for path, digest in binaries.items():
        static_amd64(bundle.get(path, b""))
        require(sha256(bundle[path]) == digest, "Binary checksum mismatch")
    if kind == "core":
        deploy_files = core_deploy_files(version)
        require(bundle.get("manifests/BUILD.json") == files["BUILD.json"] and "web/index.html" in bundle and deploy_files.issubset(bundle), "Core runtime bundle is incomplete")
        require(copies.get("REPOSITORY-README.md") == "README.md" and copies.get("PROJECT-README.md") == "vps-billing/README.md", "README copy must bind to selected source")
        allowed = required_binaries | set(copies) | {"manifests/BUILD.json", "manifests/go-dependencies.json", "manifests/npm-installed-dependencies.json", "manifests/aster-api-build.txt", "manifests/aster-plugin-host-build.txt"}
        require(all(path in allowed or path.startswith(("web/", "licenses/")) for path in bundle), "Unexpected project source/runtime member")
        require(all(path in copies for path in deploy_files), "Deployment inputs must bind to source")
        require(all(path in bundle for path in allowed), "Required core inventory file missing")
        for src in INPUT_PATHS:
            dest = "LICENSE" if src == "LICENSE" else "REPOSITORY-README.md" if src == "README.md" else "manifests/" + src.rsplit("/", 1)[1]
            require(sha256(bundle[dest]) == inputs[src], "Runtime input checksum mismatch")
        if raw_distribution:
            migrations = {path for path in bundle if path.startswith("migrations/")}
            require(migrations and all(re.fullmatch(r"migrations/[0-9]{3}_[a-z0-9_]+\.sql", path) and copies.get(path) == "vps-billing/backend/" + path for path in migrations), "Core SQL migration resources missing/unbound")
        archive_permissions(raw, root, (set() if raw_distribution else required_binaries) | {path for path in deploy_files if path.startswith("deploy/scripts/")})
    else:
        require(set(bundle) == SMTP_FILES and copies == {"LICENSE": "LICENSE", "README.md": "vps-billing/backend/plugins/smtp/README.md"}, "SMTP archive must contain exactly six fixed files")
        manifest = strict_json(bundle["manifest.json"], limit=8192)
        expected = {"schema": 1, "id": "email.smtp", "version": version, "protocol": "aster.smtp/1", "core_min": "0.4.0", "core_max_exclusive": max_core, "capabilities": ["smtp.send"], "executable": "plugin", "sha256": binaries["plugin"]}
        require(manifest == expected and type(manifest.get("schema")) is int, "SMTP manifest identity/compatibility mismatch")
        inside = checksum_inventory(bundle["SHA256SUMS"], SMTP_FILES - {"SHA256SUMS"})
        require(all(sha256(bundle[name]) == digest for name, digest in inside.items()), "SMTP inner checksum mismatch")
        notices = strict_json(bundle["THIRD_PARTY_NOTICES.json"])
        require(isinstance(notices, dict) and notices.get("schema") == 1 and isinstance(notices.get("notices"), list) and notices["notices"], "Original SMTP third-party notices missing")
        for item in notices["notices"]:
            require(isinstance(item, dict) and set(item) == {"path", "sha256", "text"} and isinstance(item["text"], str) and sha256(item["text"].encode()) == item["sha256"], "Original third-party notice checksum mismatch")
            safe_path(item["path"])
        archive_permissions(raw, root, set() if raw_distribution else {"plugin"})
    for data in files.values():
        scan_obvious_secrets(data)
    return {name: {"sha256": sha256(data), "size": len(data)} for name, data in sorted(files.items())}


def verify_runtime_source(api, files, provenance):
    kind = release_kind(provenance["tag"])
    version = provenance["version"]
    build = strict_json(files["BUILD.json"])
    _, _, bundle = runtime_bundle(files, version, kind)
    tree = api.request("GET", api_path("/git/trees/" + sha(provenance["source_sha"]) + "?recursive=1"))
    require(tree.get("truncated") is False and isinstance(tree.get("tree"), list), "Incomplete source Git tree")
    expected, sizes, migrations = {}, {}, {}
    raw_core = build.get("distribution_format") == "raw-binaries-v1" and kind == "core"
    migration_prefix = "vps-billing/backend/migrations/"
    for entry in tree["tree"]:
        require(isinstance(entry, dict), "Invalid source Git tree member")
        path = entry.get("path")
        require(isinstance(path, str), "Invalid source Git tree path")
        if raw_core and path.startswith(migration_prefix) and path.endswith(".sql") and "/" not in path[len(migration_prefix):]:
            require(entry.get("type") == "blob" and entry.get("mode") in {"100644", "100755"} and path not in migrations, "Invalid tagged SQL migration inventory")
            migrations[path] = sha(entry.get("sha"))
        if path in set(build["source_files"].values()) | set(CURRENT_INPUT_PATHS):
            require(entry.get("type") == "blob" and entry.get("mode") in {"100644", "100755"} and path not in expected, "Source contains links/submodules/duplicate Git paths")
            expected[path] = sha(entry.get("sha")); sizes[path] = entry.get("size")
    if raw_core:
        expected_migrations = {"migrations/" + path[len(migration_prefix):]: path for path in migrations}
        copied_migrations = {path: source for path, source in build["source_files"].items() if path.startswith("migrations/")}
        require(expected_migrations and copied_migrations == expected_migrations and {path for path in bundle if path.startswith("migrations/")} == set(expected_migrations), "Core SQL resources must contain the complete tagged migration inventory")
    require(set(expected) == set(build["source_files"].values()) | set(CURRENT_INPUT_PATHS), "Copied/source input missing from selected Git tree")
    for path, source in build["source_files"].items():
        require(git_blob_sha(bundle[path]) == expected[source], "Runtime text differs from tagged Git blob: " + source)
    input_bytes = {}
    for source in CURRENT_INPUT_PATHS:
        blob = api.request("GET", api_path("/git/blobs/" + expected[source]))
        require(isinstance(blob, dict) and blob.get("sha") == expected[source] and type(blob.get("size")) is int and 0 <= blob["size"] <= MAX_JSON // 2 and blob.get("encoding") == "base64" and isinstance(blob.get("content"), str), "Invalid selected input Git blob")
        try:
            data = base64.b64decode(blob["content"].replace("\n", "").replace("\r", ""), validate=True)
        except (binascii.Error, ValueError) as exc:
            raise GuardError("Invalid selected input Git encoding") from exc
        require(len(data) == blob["size"] and git_blob_sha(data) == expected[source] and sha256(data) == build["input_sha256"][source], "BUILD input differs from tagged Git blob")
        input_bytes[source] = data
    core = re.findall(rb'(?m)^\s*(?:const\s+)?CoreVersion\s*=\s*"((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))"', input_bytes["vps-billing/backend/pkg/pluginapi/protocol.go"])
    require(len(core) == 1 and core[0].decode() == build["component_versions"]["core"] and
            input_bytes["vps-billing/backend/plugins/smtp/VERSION"].decode().strip() == build["component_versions"]["smtp"], "Declared component version differs from Git source")


def latest_release_id(api):
    try:
        result = api.request("GET", api_path("/releases/latest"))
    except APIError as exc:
        if exc.status == 404:
            return None
        raise
    return integer(result.get("id"), "latest release ID")


def seal_build(policy, directory):
    directory = Path(directory)
    require(directory.is_dir() and not directory.is_symlink(), "Invalid package directory")
    paths = list(directory.iterdir())
    require(all(path.is_file() and not path.is_symlink() for path in paths), "Package directory contains non-files")
    require(all(path.stat().st_size <= MAX_ZIP for path in paths), "Package file exceeds limit")
    files = {path.name: path.read_bytes() for path in paths}
    assets = validate_package(files, policy["source_sha"], policy["version"], release_kind(policy["tag"]))
    normal = "-" not in policy["version"]
    provenance = dict(policy, assets={name: meta for name, meta in assets.items() if name != "SHA256SUMS"} if normal else assets)
    raw_provenance = json_bytes(provenance)
    with (directory / "PROVENANCE.json").open("xb") as stream:
        stream.write(raw_provenance)
    if normal:
        # No circular hash dependency: provenance binds BUILD/archive; finalized
        # SHA256SUMS binds BUILD/archive/provenance and excludes itself.
        covered = {name: data for name, data in files.items() if name != "SHA256SUMS"}
        covered["PROVENANCE.json"] = raw_provenance
        (directory / "SHA256SUMS").write_bytes("".join(sha256(data) + "  " + name + "\n" for name, data in sorted(covered.items())).encode("ascii"))
    return provenance


def read_artifact_zip(data):
    require(isinstance(data, bytes) and 0 < len(data) <= MAX_ZIP, "Invalid artifact ZIP size")
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            members = archive.infolist()
            require(len(members) in {4, 5, 6}, "Artifact ZIP must contain exactly four legacy, five SMTP raw or six Core raw files")
            require(not archive.comment and data[-22:-18] == b"PK\x05\x06" and
                    min(item.header_offset for item in members) == 0,
                    "ZIP comments/prefix/trailing payload are forbidden")
            result = {}
            total = 0
            for member in members:
                parts = safe_path(member.filename)
                require(len(parts) == 1 and not member.is_dir() and member.filename not in result,
                        "Nested/duplicate ZIP path")
                require(not member.comment, "ZIP member comments are forbidden")
                mode = member.external_attr >> 16
                require(stat.S_IFMT(mode) in {0, stat.S_IFREG} and not mode & 0o7000 and not member.flag_bits & 1,
                        "ZIP links/special/encrypted files are forbidden")
                require(member.compress_type in {zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED} and
                        0 < member.file_size <= MAX_ZIP, "Invalid ZIP compression/size")
                total += member.file_size
                require(total <= MAX_ZIP, "Expanded ZIP exceeds limit")
                with archive.open(member) as stream:
                    value = stream.read(member.file_size + 1)
                require(len(value) == member.file_size, "Truncated ZIP entry")
                result[member.filename] = value
            require("PROVENANCE.json" in result, "Artifact provenance is missing")
            return result
    except (zipfile.BadZipFile, RuntimeError, OSError, ValueError) as exc:
        raise GuardError("Invalid artifact ZIP") from exc


def artifact_for_run(api, run_id, attempt):
    expected = f"candidate-{run_id}-{attempt}"
    matches = []
    for page in range(1, 101):
        response = api.request("GET", api_path(f"/actions/runs/{run_id}/artifacts?per_page=100&page={page}"))
        items = response.get("artifacts")
        require(isinstance(items, list) and len(items) <= 100, "Invalid artifact inventory")
        matches.extend(item for item in items if item.get("name") == expected)
        if len(items) < 100:
            break
    else:
        raise GuardError("Artifact inventory exceeds pagination limit")
    require(len(matches) == 1, "Missing/duplicate artifact for this run attempt")
    artifact = matches[0]
    integer(artifact.get("id"), "artifact ID")
    require(artifact.get("expired") is False and type(artifact.get("size_in_bytes")) is int and
            0 < artifact["size_in_bytes"] <= MAX_ZIP, "Artifact expired/oversized")
    require(re.fullmatch(r"sha256:[0-9a-f]{64}", str(artifact.get("digest", ""))), "Artifact SHA-256 digest is required")
    linked = artifact.get("workflow_run", {})
    require(linked.get("id") == run_id and linked.get("repository_id") == REPOSITORY_ID and
            linked.get("head_repository_id") == REPOSITORY_ID, "Artifact/run repository mismatch")
    return artifact


def prepare_publish(api, env, event):
    environment_identity(env)
    require(env.get("GITHUB_EVENT_NAME") == "workflow_run" and event.get("action") == "completed",
            "Publisher only accepts completed workflow_run events")
    branch = repository(api)
    default = sha(env.get("GITHUB_SHA"))
    require(env.get("GITHUB_WORKFLOW_SHA") == default and env.get("GITHUB_REF") == "refs/heads/" + branch and
            resolve_ref(api, "heads/" + branch) == default, "Publisher must execute the current trusted default commit")
    event_run = event.get("workflow_run", {})
    run_id, attempt = integer(event_run.get("id"), "upstream run ID"), integer(event_run.get("run_attempt"), "upstream attempt")
    run = validate_run(api, run_id, attempt, True)
    require(event_run.get("head_sha") == run["head_sha"] and event_run.get("workflow_id") == run["workflow_id"] and
            event_run.get("event") == run["event"] and event_run.get("conclusion") == "success",
            "Workflow event does not match the current successful run")
    blobs = trusted_blobs(api, default)
    require(trusted_blobs(api, run["head_sha"]) == blobs, "Upstream workflow/policy is not trusted")
    ancestor(api, run["head_sha"], default)
    artifact = artifact_for_run(api, run_id, attempt)
    require(artifact["workflow_run"].get("head_sha") == run["head_sha"], "Artifact head mismatch")
    return {"schema": 1, "repository": REPOSITORY, "repository_id": REPOSITORY_ID,
            "trusted_default_sha": default, "default_branch": branch, "trusted_blobs": blobs,
            "run_id": run_id, "run_attempt": attempt, "event": run["event"],
            "run_head_sha": run["head_sha"], "run_head_branch": run.get("head_branch"),
            "workflow_id": run["workflow_id"], "artifact": artifact}


def verify_zip_digest(data, artifact):
    require(len(data) == artifact["size_in_bytes"] and "sha256:" + sha256(data) == artifact["digest"],
            "Downloaded artifact size/digest mismatch")


def assert_no_release(api, tag):
    # List authenticated releases because a draft may not be returned by /tags/.
    for page in range(1, 101):
        releases = api.request("GET", api_path(f"/releases?per_page=100&page={page}"))
        require(isinstance(releases, list) and len(releases) <= 100, "Invalid release inventory")
        require(not any(item.get("tag_name") == tag for item in releases), "A release already exists for this tag")
        if len(releases) < 100:
            break
    else:
        raise GuardError("Release inventory exceeds pagination limit")
    try:
        api.request("GET", api_path("/releases/tags/" + urllib.parse.quote(tag, safe="")))
    except APIError as exc:
        if exc.status == 404:
            return
        raise
    raise GuardError("A release already exists for this tag")


def verify_provenance(files, policy, api):
    provenance = strict_json(files["PROVENANCE.json"])
    expected_keys = {"schema", "repository", "repository_id", "run_id", "run_attempt", "event", "tag", "version", "source_sha",
                     "run_head_sha", "workflow_sha", "workflow_path", "default_branch", "default_sha", "trusted_blobs", "artifact_name", "assets"}
    require(isinstance(provenance, dict) and set(provenance) == expected_keys, "Invalid provenance fields")
    for key in ("schema", "repository_id", "run_id", "run_attempt"):
        integer(provenance[key], "provenance " + key)
    require(isinstance(provenance["assets"], dict), "Invalid provenance assets")
    for metadata in provenance["assets"].values():
        require(isinstance(metadata, dict) and set(metadata) == {"sha256", "size"}, "Invalid asset metadata")
        integer(metadata["size"], "asset size")
        require(isinstance(metadata["sha256"], str) and HASH.fullmatch(metadata["sha256"]), "Invalid asset digest")
    for key in ("schema", "repository", "repository_id", "run_id", "run_attempt", "event", "run_head_sha", "default_branch", "trusted_blobs"):
        require(provenance[key] == policy[key], "Provenance/run policy mismatch: " + key)
    require(provenance["workflow_path"] == BUILD_WORKFLOW and provenance["workflow_sha"] == policy["run_head_sha"] and
            provenance["artifact_name"] == policy["artifact"]["name"], "Provenance workflow/artifact mismatch")
    tag = provenance["tag"]
    require(validate_tag(tag) == provenance["version"], "Provenance version/tag mismatch")
    source = sha(provenance["source_sha"])
    if policy["event"] == "push":
        require(source == policy["run_head_sha"] and tag == policy["run_head_branch"],
                "Tag-push source/tag does not match upstream head")
    else:
        require(policy["run_head_branch"] == policy["default_branch"], "Manual upstream run was not on the default branch")
    require(resolve_ref(api, "tags/" + tag) == source, "Release tag moved")
    ancestor(api, source, policy["trusted_default_sha"])
    ancestor(api, sha(provenance["default_sha"]), policy["trusted_default_sha"])
    require(trusted_blobs(api, source) == policy["trusted_blobs"] and
            trusted_blobs(api, provenance["default_sha"]) == policy["trusted_blobs"], "Source/build policy differs from trusted default")
    green_financial_ci(api, source)
    package = {key: value for key, value in files.items() if key != "PROVENANCE.json"}
    normal = "-" not in provenance["version"]
    assets = validate_package(package, source, provenance["version"], release_kind(tag), files["PROVENANCE.json"] if normal else None)
    if normal:
        assets = {name: meta for name, meta in assets.items() if name != "SHA256SUMS"}
    require(provenance["assets"] == assets, "Provenance asset inventory mismatch")
    verify_source_archive(api, package, provenance)
    return provenance


def recheck(api, policy, provenance):
    require(repository(api) == policy["default_branch"] and
            resolve_ref(api, "heads/" + policy["default_branch"]) == policy["trusted_default_sha"], "Trusted default ref changed")
    require(resolve_ref(api, "tags/" + provenance["tag"]) == provenance["source_sha"], "Release tag moved")
    run = validate_run(api, policy["run_id"], policy["run_attempt"], True)
    require(run["head_sha"] == policy["run_head_sha"] and run["event"] == policy["event"] and
            run.get("head_branch") == policy["run_head_branch"], "Upstream run changed")
    green_financial_ci(api, provenance["source_sha"])


def publish(api, policy, archive_bytes):
    verify_zip_digest(archive_bytes, policy["artifact"])
    files = read_artifact_zip(archive_bytes)
    provenance = verify_provenance(files, policy, api)
    recheck(api, policy, provenance)
    current_artifact = artifact_for_run(api, policy["run_id"], policy["run_attempt"])
    require(all(current_artifact[key] == policy["artifact"][key] for key in ("id", "name", "size_in_bytes", "digest")),
            "Artifact identity changed after download")
    historical, historical_body = historical_attachments(api, provenance["tag"])
    require(not set(files).intersection(historical), "Historical/current asset name collision")
    files = dict(files, **historical)
    assert_no_release(api, provenance["tag"])
    recheck(api, policy, provenance)
    # No code from the artifact is extracted, imported, sourced or executed.
    # Never retry any mutation, even on timeout/502/422. A tag cannot be locked
    # atomically with GitHub Releases; repeated checks bound, but cannot erase,
    # this external TOCTOU window. Concurrent movers must be prohibited by policy.
    normal = "-" not in provenance["version"]
    kind = release_kind(provenance["tag"])
    prior_latest = latest_release_id(api) if normal and kind == "smtp" else None
    fields = {"tag_name": provenance["tag"], "target_commitish": provenance["source_sha"],
              "name": "星屿云 " + provenance["version"],
              "body": f"Linux amd64 预发布。源码提交：`{provenance['source_sha']}`。\n\n选购页提供实际地区筛选、套餐规格与实时订单摘要。支付适配器保持禁用；官方 SMTP 默认关闭，支持加密配置及管理员测试邮件，尚未接入业务通知或密码找回。该版本已通过流水线中的原生安装、业务、备份恢复和同 schema 版本回退检查。\n\n[构建与验收运行 {policy['run_id']}](https://github.com/{REPOSITORY}/actions/runs/{policy['run_id']})，尝试：{policy['run_attempt']}。附件包含安装包、源码、校验和及构建来源记录；环境与功能限制见包内文档。" + historical_body,
              "draft": True, "prerelease": not normal, "make_latest": "false", "generate_release_notes": False}
    if normal:
        component = "Aster Core" if kind == "core" else "Aster official SMTP plugin"
        fields["name"] = component + " " + provenance["version"]
        fields["body"] = f"{component} {provenance['version']}，Linux amd64。源码提交：`{provenance['source_sha']}`。\n\n核心与官方 SMTP 独立分发，协议 `aster.smtp/1`，SMTP 支持声明的 Core 兼容范围。插件需明确安装、固定哈希批准和单独的非特权进程；运行包不附手工源码归档。核心提供官方版本检查和审计交接，由主机管理员明确暂存与预检；网页不执行 root 替换、迁移或服务控制。支付渠道由运营者与开发者按支付适配器接口接入，官方默认关闭；主控/被控与自动开通需独立接入；SMTP 只提供主动管理员测试，不代表业务通知或收件箱投递。\n\n本版本通过所链接构建的回归、原生 systemd/HTTPS/备份恢复、独立插件替换与崩溃隔离、浏览器和依赖扫描门槛；这些结果只覆盖合成回环测试，不构成生产就绪声明。\n\n[构建与验收运行 {policy['run_id']}](https://github.com/{REPOSITORY}/actions/runs/{policy['run_id']})，尝试 {policy['run_attempt']}。附件包含原始 Linux amd64 程序及必须配套的资源包、SHA-256、BUILD 与 PROVENANCE；安装、升级与限制见核心文档。"
        raw_distribution = distribution_format(provenance["version"], kind) == "raw-binaries-v1"
        if not raw_distribution:
            fields["body"] = fields["body"].replace("原始 Linux amd64 程序及必须配套的资源包", "Linux amd64 运行归档")
        source_docs = f"https://github.com/{REPOSITORY}/blob/{provenance['source_sha']}/vps-billing/docs/"
        if raw_distribution and kind == "core":
            fields["body"] += f"\n\n财务更新：新安装仅启用 CNY，运营者配置其他币种与独立报价；没有汇率或历史金额转换。已使用精度锁定，未完成财务义务阻止停用，历史金额与套餐快照保留；新界面确认报价在事务内校验，价格变化要求重新确认。升级须按来源和目标版本停写并验证备份恢复，不进行破坏性数据库降级。附件为两个裸程序、资源和三份元数据，共六项。[财务规则]({source_docs}FINANCIAL_MODEL.md) · [安装]({source_docs}INSTALL.md) · [升级恢复]({source_docs}UPGRADE.md)。"
        if raw_distribution and kind == "core" and tuple(map(int, provenance["version"].split("."))) >= (0, 5, 2):
            fields["body"] += f"\n\n安装升级：管理员在主机审查私密配置后，以计划 SHA-256 明确确认 `core-operation.py apply`，执行专用实例的数据库和运行身份初始化、迁移、首次管理员、systemd/API 与 HTTPS 激活。升级停写、验证新备份和版本/插件/schema，持久化锁与检查点；中断由 `resume` 核实现场。旧程序仅在数据库仍完全兼容时回退；不自动回退或导入数据库。后台只读真实执行回执，无 root、命令或服务控制入口。构建门禁包含真实正式 0.5.2 起点与新源码构建候选的隔离安装升级、故障和恢复；候选测试不伪造正式来源。升级核对实际版本与数据库兼容性；公共证书、系统软件与现有面板配置须由管理员先行准备。[安装]({source_docs}INSTALL.md) · [升级恢复]({source_docs}UPGRADE.md) · [安全边界]({source_docs}SECURITY.md)。"
        elif raw_distribution and kind == "core" and tuple(map(int, provenance["version"].split("."))) >= (0, 5, 1):
            fields["body"] += f"\n\n安装与维护：主机 `core-manager.py prepare` 校验并自动组装完整版本目录，`doctor` 和 API `-preflight` / `-schema-status` 提供只读检查；备份自动输出 BACKUP.json。准备不激活服务或执行数据库操作，Web 仍不提权。付款禁用时可原子取消符合条件的未付订单并作废配对账单，金额和快照保留；没有收款、退款或资源操作。保留匹配版本的数据库结构和恢复资料。旧 0.5.0 主机校验器首次进入新策略须按文档核验并引导新资源内工具。[安装]({source_docs}INSTALL.md) · [支付适配器]({source_docs}PAYMENT_PLUGINS.md)。"
        elif raw_distribution and kind != "core":
            build = strict_json(files["BUILD.json"])
            compatibility = build["smtp_core_compatibility"]
            fields["body"] += f"\n\n附件为一个裸 SMTP 程序、资源和三份元数据，共五项；协议保持 `aster.smtp/1`，声明 Core `>={compatibility['min']},<{compatibility['max_exclusive']}`。资源与程序须分别核验；旧工具不会自动支持新拆分输入，首次升级使用已核验的新管理工具。安装不自动批准或启用，仅支持管理员固定测试邮件。[插件安装与回退]({source_docs}PLUGINS.md)。"
        if kind == "core" and tuple(map(int, provenance["version"].split("."))) >= (0, 5, 3):
            fields["body"] = (
                f"Aster Core {provenance['version']}，原生 Linux amd64。"
                f"[源码提交 `{provenance['source_sha']}`](https://github.com/{REPOSITORY}/commit/{provenance['source_sha']})。\n\n"
                "支付扩展新增独立运行时插槽 `aster.payment/1`，兼容插件安装或替换无需重新编译 Core。"
                "Core 读取插件元数据；主机管理员须明确批准插件身份、manifest 与程序的 SHA-256，以及商户身份，"
                "再由独立的非特权进程通过 Unix socket 提供结账和回调验签能力。"
                "支付默认关闭，第三方渠道由开发者依据渠道官方协议实现；仓库仅提供不连接支付平台的合成示例。"
                "尚未提供渠道查询、撤销、退款或渠道特定 ACK，不能据此宣称生产支付可用。\n\n"
                "Core 根据自己的订单、账单、金额、币种精度和所有权核对已验签证据，持久化支付尝试，"
                "在数据库事务内完成回调重放保护、渠道交易 ID 唯一性检查和幂等账务状态转换。"
                "取消订单与在途支付互斥；结果不确定或链接过期不会被当作未付款，也不会自动释放支付尝试。"
                "客户端成功提示和跳转不能改变账务状态。\n\n"
                "界面修复了页头高度、路由切换滚动、币种标签、按钮样式和财务副标题。"
                f"自动化检查与人工视觉复验的具体范围分别记录在[验证记录]({source_docs}VALIDATION.md)，不互相替代。\n\n"
                "发行附件共六项：裸 API 程序、裸插件宿主、配套资源包，以及 `BUILD.json`、`PROVENANCE.json`、`SHA256SUMS`。"
                "运行无需 Docker、Go 或 Node.js；两个程序和资源须来自同一版本并核验摘要与来源，单个 API 程序不足以完成安装。"
                "官方 SMTP 仍独立分发，使用 `aster.smtp/1` 并遵守声明的 Core 兼容范围；"
                "默认关闭，仅支持管理员固定测试邮件，尚无业务通知或密码找回。\n\n"
                "管理员在主机审查私密配置和计划 SHA-256 后执行安装或升级；网页没有 root、迁移或服务控制权限。"
                "升级须停写、核对版本与插件兼容性，并验证备份和恢复资料；中断后由原操作的 `resume` 核实现场。"
                "数据库升级后不能直接换回不兼容的旧程序，也不能用旧程序降低 schema；"
                "没有自动向下迁移或自动导入数据库，遇到不兼容状态须保持维护并按恢复文档处理。"
                f"[安装]({source_docs}INSTALL.md) · [升级恢复]({source_docs}UPGRADE.md) · [支付接入]({source_docs}PAYMENT_PLUGINS.md)。\n\n"
                f"[构建与验收运行 {policy['run_id']}](https://github.com/{REPOSITORY}/actions/runs/{policy['run_id']})，"
                f"尝试 {policy['run_attempt']}。本版本通过该构建的回归、原生 systemd/HTTPS、备份恢复、独立插件生命周期、浏览器与依赖扫描门槛；"
                "支付结算和邮件测试均使用隔离的合成环境，不代表真实渠道或生产部署验收。"
            )
        if kind == "core" and provenance["version"] == "0.5.4":
            heading, inherited = fields["body"].split("\n\n", 1)
            inherited = inherited.replace("支付扩展新增独立运行时插槽", "支付运行时自 Core 0.5.3 已提供独立插槽", 1)
            inherited = inherited.replace(
                "界面修复了页头高度、路由切换滚动、币种标签、按钮样式和财务副标题。",
                "Core 0.5.3 的页头高度、路由切换滚动、币种标签、按钮样式和财务副标题修复继续保留。", 1)
            fields["body"] = (
                heading + "\n\n"
                "这是 Core 0.5.3 之后的维护补丁。登录页已有错误随当前语言显示，保留加载与重试行为。"
                "本次一并交付主干积累的 721px 面包屑、长品牌与公司名称、金额输入可见性边界修复，"
                "以及支付超时的保守提示和熔断隔离修复。客户概览减少无用订单读取，"
                "订单列表在单次调用内复用经严格校验的币种快照解码结果，不放宽逐行历史数据校验。\n\n"
                "人工视觉复验仅覆盖此前固定提交的静态布局及移动导航、焦点和滚动；"
                "支付异常仅检查静态文案布局，不代表真实付款或真实超时测试。"
                "此次登录语言修复由组件行为回归验证，没有布局改动，未重新查看截图。"
                "性能结果仅来自隔离的合成只读负载，不承诺生产容量或响应时间。\n\n"
                + inherited
                + f"\n\n[宝塔部署]({source_docs}BAOTA.md) · [插件安装与回退]({source_docs}PLUGINS.md)。"
            )
        if kind == "core" and provenance["version"] == "0.5.5":
            heading, inherited = fields["body"].split("\n\n", 1)
            inherited = inherited.replace("支付扩展新增独立运行时插槽", "支付运行时自 Core 0.5.3 已提供独立插槽", 1)
            inherited = inherited.replace(
                "界面修复了页头高度、路由切换滚动、币种标签、按钮样式和财务副标题。",
                "既有的页头高度、路由切换滚动、币种标签、按钮样式和财务副标题修复继续保留。", 1)
            inherited = inherited.replace(
                "本版本通过该构建的回归、原生 systemd/HTTPS、备份恢复、独立插件生命周期、浏览器与依赖扫描门槛；",
                "各项门禁的实际执行范围和结果以对应构建及验证记录为准；", 1)
            fields["body"] = (
                heading + "\n\n"
                "这是 Core 0.5.4 之后的界面维护补丁。客户首页重新组织服务、订单与待处理账单，"
                "套餐页统一区域筛选、规格比较和订单摘要。导航轨道贯穿页面，桌面侧栏独立滚动，"
                "1024px 及以下使用抽屉导航，保留键盘焦点、关闭和返回行为。"
                "账单在窄屏重新排布，中文、英文、长名称和长金额沿用统一的层级与排版规则。\n\n"
                "本次不新增业务功能，不改变订单与付款校验、金额精度、安全或多币种历史格式，不自动换汇。"
                "客户与后台沿用同一设计体系。人工视觉复验、自动化检查和原生安装验收分别记录，"
                "本地预览或单项检查不能代替其他门禁，也不表示正常 Release 已完成发布。\n\n"
                + inherited
                + f"\n\n[宝塔部署]({source_docs}BAOTA.md) · [插件安装与回退]({source_docs}PLUGINS.md)。"
            )
        if kind == "core" and provenance["version"] == "0.5.6":
            fields["body"] = (
                "## Core 0.5.6\n\n"
                "以正式 0.5.4 为对照，保留客户首页、套餐比较、手机底栏及统一抽屉和弹窗。"
                "新增客户与管理员角色管理、账户停用、密码重置和会话撤销；写入要求原因、并发版本与实际管理员复核，"
                "保护最后一个启用管理员，禁止危险自降权/自停用。"
                "新增单笔订单详情，关联真实客户、账单、支付尝试和审计，不开放任意订单状态改写。"
                "新增商品创建、复制、编辑、草稿和上下架，各币种独立报价；商品和币种配置双版本校验阻止旧精度金额写入，商品变更令旧报价失效，历史快照不变。\n\n"
                "生产诊断使用脱敏 JSON/journald，业务审计支持权限隔离的游标分页。"
                "迁移 009 增加审计索引，010 增加账户状态及版本；正式 001–008 不重写。"
                "迁移到 schema 10 后不能直接切回只认识旧 schema 的程序；须使用已验证备份及匹配版本恢复。"
                "修复支付 Unix RPC 测试的物理连接重拨问题，不放宽生产握手或重放约束。"
                "Go 1.27.3、x/crypto v0.56.0；不改变金额、支付幂等、多币种及历史快照规则。\n\n"
                "原生 Linux amd64、Go + Vue/TypeScript + MySQL；不使用 Docker，不添加 Redis、真实资源开通或新支付渠道。"
                "Ubuntu 22 与 24 的实际验证范围分别记录，不宣称所有 Linux 均已支持。"
                "旧 v0.5.5 构建失败并保留原标签；本次使用独立新标签。"
                "官方 SMTP 仍为独立 1.1.0、默认关闭；支付协议仍为 aster.payment/1、最低 Core 0.5.3。\n\n"
                f"[源码提交](https://github.com/{REPOSITORY}/commit/{provenance['source_sha']}) · "
                f"[构建运行 {policy['run_id']}](https://github.com/{REPOSITORY}/actions/runs/{policy['run_id']})，尝试 {policy['run_attempt']}。"
                "精确构建的回归、原生 systemd/TLS、备份恢复、浏览器和依赖扫描范围以运行证据为准。"
                "合成支付/邮件及测试 CA 不代表真实渠道、公共证书续期或生产容量验收。\n\n"
                f"[验证记录]({source_docs}VALIDATION.md) · [管理边界]({source_docs}ADMIN_MANAGEMENT.md) · "
                f"[日志]({source_docs}LOGGING.md) · [安装]({source_docs}INSTALL.md) · "
                f"[升级恢复]({source_docs}UPGRADE.md) · [宝塔]({source_docs}BAOTA.md) · [插件]({source_docs}PLUGINS.md)。"
            )
        if kind == "core" and provenance["version"] == "0.5.7":
            fields["body"] = (
                "## Core 0.5.7\n\n"
                "以正式 Core 0.5.6 为基线，整合独立实现的新客户与管理员前端，随 Core 同版本分发。"
                "云服务器购买、订单、账单、账户资料和管理设置保留真实认证与既有 API；"
                "API 失败不切换为演示成功，客户与管理员导航按权限隔离。"
                "顶部“语言与币种”只管理语言与购买/报表币种，侧栏及手机导航底部“账户”管理资料、外观和退出，"
                "移除重复的偏好入口，保留手机导航、键盘焦点、关闭与返回操作。\n\n"
                "修复浅色对比度：正文、状态/错误/警告、主操作、表头、表单和弹层使用统一语义颜色角色，"
                "同时维护深色及蓝/青/紫品牌。静态 token 测量不等于实际浏览器视觉验收，"
                "加载、空态、长金额和真实失败状态均须按对应源码与运行证据核对。"
                "CNY 优先展示；全新安装仅启用 CNY，其他币种由运营者配置独立报价。"
                "历史订单和账单保留原币、精度与金额格式，不自动换汇、不重算历史快照。\n\n"
                "不改变订单报价校验、金额精度、支付幂等、认证/CSRF、角色与所有权权限边界，"
                "最后管理员保护和脱敏审计保持。未确定的登录、退出、付款或写入结果不能当作成功，"
                "不因界面更新而自动重放业务写入。"
                "本次不新增数据库迁移，继续 schema 10；正式 001–010 迁移及其校验和不重写。"
                "升级仍须核对插件、schema、备份及恢复资料，不支持自动向下迁移或自动导入数据库。\n\n"
                "原生 Linux amd64，Go + Vue/TypeScript + MySQL；不使用 Docker，运行无需 Go 或 Node.js。"
                "API、插件宿主、资源及 BUILD/PROVENANCE/SHA256SUMS 共六资产须来自同一版本并核验来源。"
                "官方 SMTP 仍独立 1.1.0、aster.smtp/1、Core >=0.4.0,<0.6.0，默认关闭，仅支持管理员固定测试邮件。"
                "支付运行时自 Core 0.5.3 已提供 aster.payment/1；不新增真实渠道、退款或真实 VPS 开通。\n\n"
                f"[源码提交](https://github.com/{REPOSITORY}/commit/{provenance['source_sha']}) · "
                f"[构建与验收运行 {policy['run_id']}](https://github.com/{REPOSITORY}/actions/runs/{policy['run_id']})，"
                f"尝试 {policy['run_attempt']}。"
                "实际执行范围和结果以本次运行及验证记录为准，不从历史绿灯推导本版已通过。"
                "Ubuntu 22/24 与其他发行版分别记录，不宣称所有 Linux 均已支持；"
                "合成支付/邮件、回环 HTTPS 和测试 CA 不代表真实渠道、公共证书续期或生产容量验收。\n\n"
                f"[v0.5.7 发布说明与边界]({source_docs}RELEASE-v0.5.7.md) · "
                f"[验证记录]({source_docs}VALIDATION.md) · [安装]({source_docs}INSTALL.md) · "
                f"[升级恢复]({source_docs}UPGRADE.md) · [宝塔]({source_docs}BAOTA.md) · [插件]({source_docs}PLUGINS.md)。"
            )
        if kind == "core" and provenance["version"] == "0.5.8":
            fields["body"] = (
                "## Core 0.5.8\n\n"
                "以正式 Core 0.5.7 为基线，实现类型→地区→节点→套餐的真实商品目录。"
                "管理员维护分类名称、排序、启停和商品归属；节点只是逻辑购买分类，不是实际资源或线路。"
                "客户按完整层级选择，默认选中当前节点的首个套餐，有效报价同步显示于桌面摘要与手机底栏；"
                "未报价不跨套餐替代，确认购买仍需明确操作。\n\n"
                "正式 0.5.7 的 schema 10 升级需应用 011、012，最终要求 schema 12；"
                "既有 001–011 迁移原文与校验和不改。011 保留旧商品的地区原文并映射至通用类型；"
                "012 为每个已有地区建立确定的“默认节点”，仅回填当前商品归属，初始化不递增 taxonomy 或报价 revision。"
                "后续分类编辑在事务内同步递增分类与报价 revision，旧报价失效，须重读后重新确认。"
                "停用、删除或移动被引用分类须遵守商品保护；商品发布和新订单校验完整启用父链，不能用 plan_id 绕过。\n\n"
                "默认中文/CNY，币种独立定价；不自动换汇，不改金额精度、支付幂等或历史快照。"
                "既有合法幂等重放仍返回原订单；不从当前分类补造历史字段。"
                "认证/CSRF、角色与所有权权限边界、最后管理员保护及脱敏审计保持；"
                "写入冲突和不确定结果不显示成功，不自动重放业务操作。"
                "升级前维护停写并停止旧 API，验证备份与隔离恢复，再迁移和同时切换匹配前后端。"
                "schema 10/11 程序不能直接接回 schema 12；不自动向下迁移或导入数据库，回滚须恢复匹配备份与旧程序。\n\n"
                "原生 Linux amd64，Go + Vue/TypeScript + MySQL，不使用 Docker。"
                "API、插件宿主、资源及 BUILD/PROVENANCE/SHA256SUMS 共六资产须同版本并核验来源。"
                "官方 SMTP 仍独立 1.1.0、aster.smtp/1、Core >=0.4.0,<0.6.0，默认关闭，仅支持管理员固定测试邮件。"
                "不新增真实 VPS 开通、资源管理或新支付渠道；支付协议仍为 aster.payment/1。\n\n"
                f"[源码提交](https://github.com/{REPOSITORY}/commit/{provenance['source_sha']}) · "
                f"[构建与验收运行 {policy['run_id']}](https://github.com/{REPOSITORY}/actions/runs/{policy['run_id']})，"
                f"尝试 {policy['run_attempt']}。"
                "实际执行范围和结果以本次精确提交的运行及验证记录为准，历史绿灯不代替本版验收。"
                "Ubuntu 22/24 与其他发行版分别记录，不宣称所有 Linux 均已支持；"
                "合成支付/邮件、回环 HTTPS 和测试 CA 不代表真实渠道、公共证书续期或生产容量验收。\n\n"
                f"[v0.5.8 发布说明与边界]({source_docs}RELEASE-v0.5.8.md) · "
                f"[商品分类]({source_docs}CATALOG_TAXONOMY.md) · [验证记录]({source_docs}VALIDATION.md) · "
                f"[安装]({source_docs}INSTALL.md) · [升级恢复]({source_docs}UPGRADE.md) · "
                f"[宝塔]({source_docs}BAOTA.md) · [插件]({source_docs}PLUGINS.md)。"
            )
        if kind == "core" and provenance["version"] == "0.5.9":
            fields["body"] = (
                "## Core 0.5.9\n\n"
                "以正式 Core 0.5.8 为基线，新增独立商品产品配置、不可变订单私密配置快照和已付款订单的唯一手动任务。"
                "013_product_configurations.sql 追加三个表，最终要求 schema 13 / 23 表；001–012 原文与校验和不改，"
                "迁移不回填旧单、不创建任务、不改价格与金额。配置保存提升配置和 prices_revision，使旧报价确认重新核对；"
                "旧合法幂等重放仍返回原订单，原财务 PlanSnapshot、币种格式快照和支付幂等不变。\n\n"
                "学习的是 IDCsmart 的分层思路，使用本系统自有 aster.controller/1，"
                "公开稳定 Go 合同位于 backend/controllerapi，不含财务数据库或付款操作。"
                "不对接 IDCsmart API，不附带独立主控/KVM、真实接收端或 VM 创建能力。"
                "默认无 worker、无调度器，保存配置不发送请求；Web 不读取主控端点凭据或执行特权命令。"
                "管理员仅为 paid 订单与账单准备任务，root 显式一次性下发；uncertain 必须核对，不自动重发。"
                "HTTP 409 仅接受完整绑定原 operation、order 与请求摘要的既有结果。"
                "认证/CSRF、角色与所有权权限边界和同事务审计保留，不提供无需付款开通或 TLS 降级。\n\n"
                "原生 Linux amd64，Go + Vue/TypeScript + MySQL，不使用 Docker。"
                "API、插件宿主、资源及 BUILD/PROVENANCE/SHA256SUMS 共六资产须同版本并核验来源。"
                "SMTP 仍独立 1.1.0，aster.smtp/1、Core >=0.4.0,<0.6.0，默认关闭，仅支持管理员固定测试邮件。"
                "不新增真实付款或新支付渠道，支付协议仍为 aster.payment/1。"
                "升级须停写、停止旧 API、验证备份与隔离恢复后迁移和切换匹配前后端；"
                "schema12 程序不能直接运行 schema13 库。回滚须恢复匹配备份与旧程序，不自动向下迁移或导入数据库。\n\n"
                f"[源码提交](https://github.com/{REPOSITORY}/commit/{provenance['source_sha']}) · "
                f"[构建与验收运行 {policy['run_id']}](https://github.com/{REPOSITORY}/actions/runs/{policy['run_id']})，"
                f"尝试 {policy['run_attempt']}。"
                "实际执行范围和结果以本次精确提交的运行与验证记录为准；历史绿灯和本地分组测试不代替最终完整门禁。"
                "Ubuntu 22/24 分别记录，不宣称所有 Linux 均已支持；合成测试不代表真实 VM、渠道、公共证书续期或生产容量验收。\n\n"
                f"[v0.5.9 发布说明与边界]({source_docs}RELEASE-v0.5.9.md) · "
                f"[产品配置与主机命令]({source_docs}PRODUCT_CONFIGURATION.md) · [验证记录]({source_docs}VALIDATION.md) · "
                f"[安装]({source_docs}INSTALL.md) · [升级恢复]({source_docs}UPGRADE.md) · "
                f"[宝塔]({source_docs}BAOTA.md) · [插件]({source_docs}PLUGINS.md)。"
            )
        if kind == "core" and provenance["version"] == "0.5.10":
            fields["body"] = (
                "## Core 0.5.10\n\n"
                "本版从历史作者归属调整后的主线构建；prepare/stage 继续要求源码与构建策略信任点为当前 main 祖先，"
                "没有旧新 SHA 白名单或来源校验例外。旧 v0.5.6–v0.5.9 标签和附件不覆盖，"
                "这些旧版本的新 prepare/stage 仍会因来源分叉被拒绝。"
                "已有受保护旧目录仅在真实 PREPARED.json、完整文件摘要、版本与迁移校验和通过时，"
                "才可用本版工具审查升级计划；缺失收据不得伪造、补签或跳过。\n\n"
                "没有新增数据库迁移，仍为 schema 13；001–013 原文与校验和保持不变。"
                "金额、支付幂等和历史快照不变，CNY 默认及独立币种报价不变。"
                "升级仍须停写、停止旧 API、验证备份和隔离恢复，再切换匹配前后端。"
                "不自动降低 schema 或导入数据库；同 schema 也不等于可以省略备份与兼容检查。\n\n"
                "独立资源插件 resource.aster-cloud 0.1.1 使用 aster.resource/1，要求 Core >=0.5.10,<0.6.0；"
                "旧正式 0.5.9 不支持此协议。插件需单独构建、核验、安装批准和启用，Core 六资产不包含资源插件包。"
                "保留已有显式任务派发与只读核对边界，不增加自动开通 worker，不把主控 accepted 当作虚拟机运行成功。"
                "SMTP 1.1.0 仍独立分发、默认关闭，aster.smtp/1、Core >=0.4.0,<0.6.0；"
                "支付协议仍为 aster.payment/1，不新增真实支付渠道。\n\n"
                "账户创建、更新、重置密码与撤销会话在事务内再次核对原会话和当前管理员权限；"
                "等锁期间注销、过期或轮换的新旧会话不能混用，不增加staff权限组或MFA。\n\n"
                "部署目标为 Ubuntu 22.04 / Ubuntu 24.04 amd64，发行要求同一源码的两个平台完整原生检查成功；"
                "具体检查与清理结果以链接运行和验证记录为准，不把隔离CI当全新生产主机验收。"
                "Ubuntu 26.04 与其他发行版暂不支持。"
                "doctor 区分支持目标与验收基线，不代表所有非目标系统都被安装器硬拒绝。"
                "原生 Go + Vue/TypeScript + MySQL，不用 Docker。发行共六资产："
                "裸 API、裸插件宿主、配套资源包、BUILD.json、PROVENANCE.json、SHA256SUMS。\n\n"
                f"[源码提交](https://github.com/{REPOSITORY}/commit/{provenance['source_sha']}) · "
                f"[构建与验收运行 {policy['run_id']}](https://github.com/{REPOSITORY}/actions/runs/{policy['run_id']})，尝试 {policy['run_attempt']}。"
                "实际结果绑定该提交与运行；离线来源/升级夹具不等于公开下载验收，"
                "正式附件发布后仍须独立完成公共六资产下载、摘要及 prepare 检查。"
                "真实 KVM、跨节点、公网上联、生产容量与真实支付渠道不在本版合成验收声明内。\n\n"
                f"[发布说明]({source_docs}RELEASE-v0.5.10.md) · [升级恢复]({source_docs}UPGRADE.md) · "
                f"[安装]({source_docs}INSTALL.md) · [资源插件]({source_docs}RESOURCE_PLUGINS.md) · "
                f"[验证记录]({source_docs}VALIDATION.md)。"
            )
    release_id = None
    try:
        created = api.request("POST", api_path("/releases"), data=fields)
        release_id = integer(created.get("id"), "created release ID")
        require(created.get("draft") is True and created.get("prerelease") is (not normal) and
                created.get("tag_name") == provenance["tag"], "Unexpected created release state")
        for name in sorted(files):
            recheck(api, policy, provenance)
            current = api.request("GET", api_path(f"/releases/{release_id}"))
            require(current.get("id") == release_id and current.get("draft") is True and current.get("prerelease") is (not normal) and
                    current.get("tag_name") == provenance["tag"] and isinstance(current.get("assets"), list),
                    "Draft changed during upload")
            require(not any(asset.get("name") == name for asset in current["assets"]), "Asset already exists; overwrite forbidden")
            uploaded = api.upload(release_id, name, files[name])
            require(uploaded.get("name") == name and uploaded.get("size") == len(files[name]) and
                    uploaded.get("state") == "uploaded" and uploaded.get("digest") == "sha256:" + sha256(files[name]),
                    "Asset upload verification failed")
        recheck(api, policy, provenance)
        final = api.request("GET", api_path(f"/releases/{release_id}"))
        require(final.get("id") == release_id and isinstance(final.get("assets"), list), "Final draft identity mismatch")
        asset_ids = []
        for asset in final["assets"]:
            asset_ids.append(integer(asset.get("id"), "uploaded asset ID"))
            require(asset.get("state") == "uploaded", "Incomplete release asset")
        require(len(set(asset_ids)) == len(asset_ids), "Duplicate release asset IDs")
        expected = {name: (len(data), "sha256:" + sha256(data)) for name, data in files.items()}
        actual = {asset.get("name"): (asset.get("size"), asset.get("digest")) for asset in final.get("assets", [])}
        require(final.get("draft") is True and final.get("prerelease") is (not normal) and
                final.get("tag_name") == provenance["tag"] and actual == expected and len(final["assets"]) == len(expected),
                "Final draft asset inventory/state mismatch")
        if normal:
            # Only this freshly created release may transition to public. All
            # bytes, IDs, tag, controller and successful run are rechecked first.
            recheck(api, policy, provenance)
            published = api.request("PATCH", api_path(f"/releases/{release_id}"), data={"draft": False, "prerelease": False, "make_latest": "true" if kind == "core" else "false"})
            require(published.get("id") == release_id and published.get("draft") is False and published.get("prerelease") is False and published.get("tag_name") == provenance["tag"], "Unexpected public release response")
            recheck(api, policy, provenance)
            public = api.request("GET", api_path(f"/releases/{release_id}"))
            require(all(asset.get("state") == "uploaded" for asset in public.get("assets", [])) and len({integer(asset.get("id"), "public asset ID") for asset in public.get("assets", [])}) == len(expected), "Public asset IDs/states mismatch")
            public_assets = {asset.get("name"): (asset.get("size"), asset.get("digest")) for asset in public.get("assets", [])}
            require(public.get("id") == release_id and public.get("draft") is False and public.get("prerelease") is False and public.get("tag_name") == provenance["tag"] and
                    public.get("target_commitish") == provenance["source_sha"] and public_assets == expected and len(public.get("assets", [])) == len(expected), "Public release identity/assets mismatch")
            require(latest_release_id(api) == (release_id if kind == "core" else prior_latest), "Public latest-release policy mismatch")
        return release_id
    except (GuardError, KeyError, TypeError, ValueError) as exc:
        if release_id is not None:
            raise GuardError(f"Publication stopped; inspect release ID {release_id}; it may still be draft or publication outcome may be uncertain. No further mutation/delete/retry performed. {exc}") from None
        raise GuardError("Release creation failed or has an uncertain outcome; inspect repository releases for tag " + provenance["tag"] + " before any new attempt. No mutation retry performed. " + str(exc)) from None


def outputs(values):
    destination = os.environ.get("GITHUB_OUTPUT")
    if destination:
        with open(destination, "a", encoding="utf-8") as stream:
            for key, value in values.items():
                text = str(value)
                require(re.fullmatch(r"[a-z_]+", key) and "\n" not in text and "\r" not in text, "Unsafe workflow output")
                stream.write(f"{key}={text}\n")


def write_new(path, data):
    with Path(path).open("xb") as stream:
        stream.write(data)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    for command in ("prepare-build", "seal-build", "prepare-publish", "publish"):
        cmd = sub.add_parser(command)
        cmd.add_argument("--policy", required=True)
        if command == "seal-build":
            cmd.add_argument("--directory", required=True)
        if command == "prepare-publish":
            cmd.add_argument("--download", required=True)
        if command == "publish":
            cmd.add_argument("--artifact", required=True)
    args = parser.parse_args(argv)
    if args.command == "seal-build":
        seal_build(strict_json(Path(args.policy).read_bytes()), args.directory)
        print("Release package sealed; no publication performed.")
        return
    api = GitHub(os.environ.get("GH_TOKEN"))
    if args.command in {"prepare-build", "prepare-publish"}:
        event = strict_json(Path(os.environ["GITHUB_EVENT_PATH"]).read_bytes())
        if args.command == "prepare-build":
            policy = prepare_build(api, os.environ, event)
            outputs({**{key: policy[key] for key in ("source_sha", "tag", "version", "artifact_name")}, "kind": release_kind(policy["tag"])})
        else:
            policy = prepare_publish(api, os.environ, event)
            data = api.download_artifact(policy["artifact"])
            # Reject all malformed/untrusted payloads before any mutation.
            # Token permissions remain job-wide, including this read-only stage.
            verify_provenance(read_artifact_zip(data), policy, api)
            write_new(args.download, data)
            outputs({"artifact_id": policy["artifact"]["id"], "artifact_name": policy["artifact"]["name"],
                     "run_id": policy["run_id"], "run_attempt": policy["run_attempt"]})
        write_new(args.policy, json_bytes(policy))
        print("Release policy validated; no publication performed.")
    else:
        policy = strict_json(Path(args.policy).read_bytes())
        artifact = Path(args.artifact)
        require(artifact.is_file() and not artifact.is_symlink() and artifact.stat().st_size <= MAX_ZIP, "Unsafe artifact file")
        release_id = publish(api, policy, artifact.read_bytes())
        outputs({"release_id": release_id})
        print(f"Release ID {release_id} verified; normal runtime tags published, historical prerelease tags remain draft; no deployment performed.")


if __name__ == "__main__":
    try:
        main()
    except (GuardError, KeyError, TypeError, ValueError, OSError) as exc:
        print("Release guard stopped: " + str(exc), file=sys.stderr)
        sys.exit(1)
