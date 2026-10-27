#!/usr/bin/env python3
"""Release trust-boundary regression fixtures. Never call a real GitHub API."""
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import stat
import tarfile
import tempfile
import unittest
from unittest import mock
import urllib.error
import warnings
import zipfile


SPEC = importlib.util.spec_from_file_location(
    "release_pipeline", Path(__file__).with_name("release_pipeline.py")
)
pipeline = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(pipeline)

REPOSITORY = "xiaoqiqixi/betteridcsys"
REPOSITORY_ID = 1418618420
SHA = "a" * 40
OTHER_SHA = "b" * 40
VERSION = "1.2.3-rc.1"
TAG = "v" + VERSION
DEFAULT_SHA = "d" * 40
RUN_ID = 700
WORKFLOW_ID = 1234
ARTIFACT_ID = 800
RELEASE_ID = 900
PREFIX = "/repos/" + REPOSITORY


def json_bytes(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


def guard_bytes(path):
    return ("fixture trusted policy: " + path + "\n").encode()


def git_blob_sha(data):
    return hashlib.sha1(b"blob " + str(len(data)).encode() + b"\0" + data).hexdigest()


def zip_bytes(files, extra_entries=()):
    result = io.BytesIO()
    with warnings.catch_warnings():
        warnings.simplefilter("ignore", UserWarning)
        with zipfile.ZipFile(result, "w", zipfile.ZIP_DEFLATED) as archive:
            for name, data in files.items():
                archive.writestr(name, data)
            for entry, data in extra_entries:
                archive.writestr(entry, data)
    return result.getvalue()


def tar_bytes(entries):
    result = io.BytesIO()
    with tarfile.open(fileobj=result, mode="w:gz", format=tarfile.GNU_FORMAT) as archive:
        for name, value in entries.items():
            if isinstance(value, tarfile.TarInfo):
                archive.addfile(value)
                continue
            entry = tarfile.TarInfo(name)
            entry.mode = 0o755 if name.endswith("/bin/aster-api") else 0o644
            entry.size = len(value)
            archive.addfile(entry, io.BytesIO(value))
    return result.getvalue()


def refresh_checksums(files):
    files["SHA256SUMS"] = "".join(
        f"{hashlib.sha256(data).hexdigest()}  {name}\n"
        for name, data in sorted(files.items())
        if name != "SHA256SUMS" and name != "PROVENANCE.json"
    ).encode()
    return files


def package_fixture(version=VERSION, source_sha=SHA, build_overrides=None,
                    binary_entries=None, source_entries=None):
    inputs = {
        "LICENSE": b"fixture license\n",
        "README.md": b"# fixture repository\n",
        "vps-billing/backend/go.mod": b"module aster/billing\n",
        "vps-billing/backend/go.sum": b"",
        "vps-billing/frontend/package.json": b'{"name":"fixture"}\n',
        "vps-billing/frontend/package-lock.json": b'{"lockfileVersion":3}\n',
    }
    build = {
        "release_label": version,
        "source_commit": source_sha,
        "source_date_epoch": 1791936000,
        "target": "linux/amd64",
        "cgo_enabled": False,
        "tools": {"go": "go version go1.27.2 linux/amd64", "node": "v24.19.0"},
        "go_build_flags": ["-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w"],
        "frontend_build": ["npm ci --no-audit --no-fund", "npm run build"],
        "input_sha256": {name: hashlib.sha256(value).hexdigest() for name, value in inputs.items()},
    }
    build.update(build_overrides or {})
    build_raw = json_bytes(build)
    binary_root = f"aster-{version}-linux-amd64"
    source_root = f"aster-{version}-source"
    elf = bytearray(64)
    elf[:6] = b"\x7fELF\x02\x01"
    elf[18:20] = b"\x3e\x00"
    binary = {
        f"{binary_root}/bin/aster-api": bytes(elf),
        f"{binary_root}/web/index.html": b"<!doctype html><title>fixture</title>",
        f"{binary_root}/LICENSE": inputs["LICENSE"],
        f"{binary_root}/REPOSITORY-README.md": inputs["README.md"],
        f"{binary_root}/manifests/BUILD.json": build_raw,
    }
    binary.update(binary_entries or {})
    source = {f"{source_root}/{name}": data for name, data in inputs.items()}
    source.update({f"{source_root}/{path}": guard_bytes(path)
                   for path in pipeline.TRUSTED_PATHS if path.startswith("vps-billing/")})
    source.update(source_entries or {})
    return refresh_checksums({
        "BUILD.json": build_raw,
        f"{binary_root}.tar.gz": tar_bytes(binary),
        f"{source_root}.tar.gz": tar_bytes(source),
    })


class FakeAPI:
    """Deny unspecified requests; responses are isolated copies, never network."""

    def __init__(self, responses=None):
        self.responses = responses or {}
        self.calls = []
        self.uploads = []
        self.fail_upload_number = None

    def request(self, method, path, data=None, raw=None, content_type=None):
        self.calls.append((method, path, copy.deepcopy(data), raw, content_type))
        key = (method, path)
        if key not in self.responses:
            raise AssertionError(f"Unexpected mocked API request: {method} {path}")
        response = self.responses[key]
        if isinstance(response, Exception):
            raise response
        if callable(response):
            response = response(data)
        return copy.deepcopy(response)

    def upload(self, release_id, name, data):
        self.uploads.append((release_id, name, data))
        if len(self.uploads) == self.fail_upload_number:
            raise pipeline.APIError(503, "synthetic interrupted upload")
        result = {
            "id": 1000 + len(self.uploads), "name": name,
            "size": len(data), "state": "uploaded",
            "digest": "sha256:" + hashlib.sha256(data).hexdigest(),
        }
        release = self.responses.get(("GET", PREFIX + f"/releases/{RELEASE_ID}"))
        if isinstance(release, dict):
            release.setdefault("assets", []).append(result)
        return result

    @property
    def mutations(self):
        return [call for call in self.calls if call[0] not in ("GET", "HEAD")]


def flow_fixture():
    repo = {"id": REPOSITORY_ID, "full_name": REPOSITORY, "private": False,
            "archived": False, "disabled": False, "default_branch": "main"}
    blobs = {path: git_blob_sha(guard_bytes(path)) for path in pipeline.TRUSTED_PATHS}
    tree = {"truncated": False, "tree": [
        {"path": path, "sha": digest, "type": "blob", "mode": "100644"}
        for path, digest in blobs.items()
    ]}
    run = {"id": RUN_ID, "run_attempt": 1, "workflow_id": WORKFLOW_ID,
           "path": pipeline.BUILD_WORKFLOW, "head_sha": SHA, "event": "push",
           "status": "completed", "conclusion": "success", "repository": repo,
           "head_repository": repo, "head_branch": TAG}
    build_policy = {
        "schema": 1, "repository": REPOSITORY, "repository_id": REPOSITORY_ID,
        "run_id": RUN_ID, "run_attempt": 1, "event": "push", "tag": TAG,
        "version": VERSION, "source_sha": SHA, "run_head_sha": SHA,
        "workflow_sha": SHA, "workflow_path": pipeline.BUILD_WORKFLOW,
        "default_branch": "main", "default_sha": DEFAULT_SHA,
        "trusted_blobs": blobs, "artifact_name": f"candidate-{RUN_ID}-1",
    }
    files = package_fixture()
    source = pipeline.tar_files(files[f"aster-{VERSION}-source.tar.gz"], f"aster-{VERSION}-source")
    tree["tree"].extend({"path": path, "sha": git_blob_sha(data), "type": "blob", "mode": "100644"}
                        for path, data in source.items() if path not in blobs)
    provenance = dict(build_policy, assets=pipeline.validate_package(files, SHA, VERSION))
    files["PROVENANCE.json"] = json_bytes(provenance)
    archive = zip_bytes(files)
    artifact = {
        "id": ARTIFACT_ID, "name": build_policy["artifact_name"], "expired": False,
        "size_in_bytes": len(archive), "digest": "sha256:" + hashlib.sha256(archive).hexdigest(),
        "workflow_run": {"id": RUN_ID, "repository_id": REPOSITORY_ID,
                         "head_repository_id": REPOSITORY_ID, "head_sha": SHA},
    }
    created = {"id": RELEASE_ID, "draft": True, "prerelease": True,
               "tag_name": TAG, "target_commitish": SHA, "assets": []}
    responses = {
        ("GET", PREFIX): repo,
        ("GET", PREFIX + "/git/ref/heads/main"): {"ref": "refs/heads/main", "object": {"type": "commit", "sha": DEFAULT_SHA}},
        ("GET", PREFIX + "/git/ref/tags/" + TAG): {"ref": "refs/tags/" + TAG, "object": {"type": "commit", "sha": SHA}},
        ("GET", PREFIX + f"/compare/{SHA}...{DEFAULT_SHA}"): {"status": "ahead", "merge_base_commit": {"sha": SHA}},
        ("GET", PREFIX + f"/compare/{DEFAULT_SHA}...{DEFAULT_SHA}"): {"status": "identical", "merge_base_commit": {"sha": DEFAULT_SHA}},
        ("GET", PREFIX + f"/git/trees/{SHA}?recursive=1"): copy.deepcopy(tree),
        ("GET", PREFIX + f"/git/trees/{DEFAULT_SHA}?recursive=1"): copy.deepcopy(tree),
        ("GET", PREFIX + "/actions/workflows/release-build.yml"): {"id": WORKFLOW_ID, "path": pipeline.BUILD_WORKFLOW, "state": "active"},
        ("GET", PREFIX + f"/actions/runs/{RUN_ID}"): run,
        ("GET", PREFIX + f"/actions/runs/{RUN_ID}/artifacts?per_page=100&page=1"): {"artifacts": [artifact]},
        ("GET", PREFIX + "/releases?per_page=100&page=1"): [],
        ("GET", PREFIX + "/releases/tags/" + TAG): pipeline.APIError(404),
        ("POST", PREFIX + "/releases"): copy.deepcopy(created),
        ("GET", PREFIX + f"/releases/{RELEASE_ID}"): copy.deepcopy(created),
    }
    env = {
        "GITHUB_REPOSITORY": REPOSITORY, "GITHUB_REPOSITORY_ID": str(REPOSITORY_ID),
        "GITHUB_RUN_ID": "901", "GITHUB_RUN_ATTEMPT": "1",
        "GITHUB_EVENT_NAME": "workflow_run", "GITHUB_REF": "refs/heads/main",
        "GITHUB_SHA": DEFAULT_SHA, "GITHUB_WORKFLOW_SHA": DEFAULT_SHA,
    }
    event = {"action": "completed", "workflow_run": copy.deepcopy(run)}
    ci_id, ci_workflow = 701, 702
    responses[("GET", PREFIX + "/actions/workflows/ci.yml")] = {"id": ci_workflow, "path": ".github/workflows/ci.yml", "state": "active"}
    for source_sha in (SHA, DEFAULT_SHA):
        ci_run = {"id": ci_id, "run_attempt": 1, "workflow_id": ci_workflow, "head_sha": source_sha,
                  "head_branch": "main", "event": "push", "status": "completed", "conclusion": "success"}
        responses[("GET", PREFIX + "/actions/workflows/ci.yml/runs?branch=main&event=push&head_sha=" + source_sha + "&per_page=100")] = {"total_count": 1, "workflow_runs": [ci_run]}
    responses[("GET", PREFIX + f"/actions/runs/{ci_id}/attempts/1/jobs?per_page=100")] = {
        "total_count": len(pipeline.FINANCIAL_CI_JOBS), "jobs": [{"name": name, "status": "completed", "conclusion": "success"} for name in sorted(pipeline.FINANCIAL_CI_JOBS)]}
    return FakeAPI(responses), env, event, build_policy, files, archive


class TagValidationTest(unittest.TestCase):
    def test_only_exact_release_and_rc_tags(self):
        for tag in ("v0.0.0-alpha.0", "v1.2.3-beta.2", "v1.2.3-rc.1", "v12.34.56-rc.123", "v0.4.0", "smtp-v1.0.0"):
            with self.subTest(tag=tag):
                self.assertEqual(pipeline.validate_tag(tag), tag[6:] if tag.startswith("smtp-v") else tag[1:])

    def test_ref_traversal_shell_and_nonrelease_inputs_rejected(self):
        for tag in ("", "1.2.3", "main", "refs/tags/" + TAG, "v1.2", "v1.2.3.4",
                    "v1.2.3-dev.1", "v1.2.3+build", "v1.2.3-rc.-1", "v1.2.3-rc.x",
                    "v01.2.3", "v1.02.3", "v1.2.03", "v1.2.3-rc.01",
                    "v1.2.3/../../main", "v1.2.3\n", " v1.2.3", "v1.2.3;echo bad",
                    TAG + "\n", TAG + "/../main", TAG + "$(id)", "v１.2.3", "V1.2.3"):
            with self.subTest(tag=tag), self.assertRaises(pipeline.GuardError):
                pipeline.validate_tag(tag)

    def test_asset_names_derive_only_from_validated_version(self):
        self.assertEqual(set(pipeline.package_files(VERSION)), {
            "BUILD.json", f"aster-{VERSION}-linux-amd64.tar.gz",
            f"aster-{VERSION}-source.tar.gz", "SHA256SUMS",
        })


class ArchiveSafetyTest(unittest.TestCase):
    def test_flat_artifact_round_trip(self):
        files = package_fixture()
        files["PROVENANCE.json"] = b"{}"
        self.assertEqual(pipeline.read_artifact_zip(zip_bytes(files)), files)

    def test_malformed_artifact_rejected(self):
        for data in (b"", b"not a zip", b"PK\x03\x04truncated"):
            with self.subTest(data=data), self.assertRaises(pipeline.GuardError):
                pipeline.read_artifact_zip(data)

    def test_archive_paths_rejected_without_extraction(self):
        for name in ("../BUILD.json", "/BUILD.json", "nested/BUILD.json",
                     "nested/../BUILD.json", r"C:\BUILD.json", r"nested\BUILD.json"):
            files = package_fixture()
            files["PROVENANCE.json"] = b"{}"
            files[name] = files.pop("BUILD.json")
            with self.subTest(name=name), self.assertRaises(pipeline.GuardError):
                pipeline.read_artifact_zip(zip_bytes(files))

    def test_duplicate_artifact_member_rejected(self):
        files = package_fixture()
        with self.assertRaises(pipeline.GuardError):
            pipeline.read_artifact_zip(zip_bytes(files, [("BUILD.json", b"second")]))

    def test_artifact_symlink_rejected(self):
        link = zipfile.ZipInfo("BUILD.json")
        link.create_system = 3
        link.external_attr = (stat.S_IFLNK | 0o777) << 16
        files = package_fixture()
        files.pop("BUILD.json")
        files["PROVENANCE.json"] = b"{}"
        with self.assertRaises(pipeline.GuardError):
            pipeline.read_artifact_zip(zip_bytes(files, [(link, b"/etc/passwd")]))

    def test_expanded_zip_and_tar_limits_enforced_without_large_allocations(self):
        _, _, _, _, files, archive = flow_fixture()
        limit = len(archive) + 1
        self.assertLess(limit, sum(len(data) for data in files.values()))
        with mock.patch.object(pipeline, "MAX_ZIP", limit), self.assertRaises(pipeline.GuardError):
            pipeline.read_artifact_zip(archive)
        with mock.patch.object(pipeline, "MAX_TAR", 1024), self.assertRaises(pipeline.GuardError):
            pipeline.tar_files(files[f"aster-{VERSION}-source.tar.gz"], f"aster-{VERSION}-source")
        with mock.patch.object(pipeline, "MAX_FILE", 16), self.assertRaises(pipeline.GuardError):
            pipeline.tar_files(files[f"aster-{VERSION}-source.tar.gz"], f"aster-{VERSION}-source")


class PackageValidationTest(unittest.TestCase):
    def test_consistent_package_returns_exact_asset_digests(self):
        files = package_fixture()
        assets = pipeline.validate_package(files, SHA, VERSION)
        self.assertEqual(set(assets), set(files))
        for name, data in files.items():
            self.assertEqual(assets[name]["sha256"], hashlib.sha256(data).hexdigest())
            self.assertEqual(assets[name]["size"], len(data))

    def test_missing_or_extra_release_files_rejected(self):
        files = package_fixture()
        for missing in files:
            mutated = dict(files)
            del mutated[missing]
            with self.subTest(missing=missing), self.assertRaises(pipeline.GuardError):
                pipeline.validate_package(mutated, SHA, VERSION)
        files["secrets.env"] = b"not actually a secret"
        with self.assertRaises(pipeline.GuardError):
            pipeline.validate_package(files, SHA, VERSION)

    def test_checksum_tampering_and_ambiguous_manifest_rejected(self):
        files = package_fixture()
        variants = {
            "wrong_digest": b"0" * 64 + files["SHA256SUMS"][64:],
            "duplicate": files["SHA256SUMS"] + files["SHA256SUMS"].splitlines(keepends=True)[0],
            "missing": b"".join(files["SHA256SUMS"].splitlines(keepends=True)[1:]),
            "unlisted": files["SHA256SUMS"] + b"0" * 64 + b"  ../secrets.env\n",
            "malformed": b"not a checksum\n",
        }
        for name, sums in variants.items():
            with self.subTest(name=name), self.assertRaises(pipeline.GuardError):
                pipeline.validate_package(dict(files, SHA256SUMS=sums), SHA, VERSION)

    def test_wrong_source_version_architecture_or_cgo_rejected(self):
        for override in ({"source_commit": OTHER_SHA}, {"release_label": "99.0.0"},
                         {"target": "windows/amd64"}, {"target": "linux/arm64"},
                         {"cgo_enabled": True}):
            with self.subTest(override=override), self.assertRaises(pipeline.GuardError):
                pipeline.validate_package(package_fixture(build_overrides=override), SHA, VERSION)

    def test_embedded_build_metadata_must_match_external_metadata(self):
        path = f"aster-{VERSION}-linux-amd64/manifests/BUILD.json"
        files = package_fixture(binary_entries={path: b'{"source_commit":"different"}\n'})
        with self.assertRaises(pipeline.GuardError):
            pipeline.validate_package(files, SHA, VERSION)

    def test_tar_paths_cannot_escape_or_disguise_archive_root(self):
        for path in ("../outside", "/tmp/outside", "wrong-root/file", r"C:\outside",
                     f"aster-{VERSION}-source/../outside"):
            with self.subTest(path=path), self.assertRaises(pipeline.GuardError):
                pipeline.validate_package(package_fixture(source_entries={path: b"unsafe"}), SHA, VERSION)

    def test_tar_symlinks_hardlinks_devices_and_fifos_rejected(self):
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.CHRTYPE, tarfile.BLKTYPE, tarfile.FIFOTYPE):
            path = f"aster-{VERSION}-source/unsafe"
            entry = tarfile.TarInfo(path)
            entry.type = kind
            entry.linkname = "/etc/passwd"
            with self.subTest(kind=kind), self.assertRaises(pipeline.GuardError):
                pipeline.validate_package(package_fixture(source_entries={path: entry}), SHA, VERSION)

    def test_non_json_and_duplicate_build_keys_rejected(self):
        for raw in (b"not json", b'{"source_commit":"one","source_commit":"two"}', b"[]", b"null"):
            files = package_fixture()
            files["BUILD.json"] = raw
            refresh_checksums(files)
            with self.subTest(raw=raw), self.assertRaises(pipeline.GuardError):
                pipeline.validate_package(files, SHA, VERSION)

    def test_duplicate_tar_members_and_corrupt_compression_rejected(self):
        files = package_fixture()
        duplicate = io.BytesIO()
        with tarfile.open(fileobj=duplicate, mode="w:gz") as archive:
            for value in (b"first", b"second"):
                entry = tarfile.TarInfo(f"aster-{VERSION}-source/LICENSE")
                entry.size = len(value)
                archive.addfile(entry, io.BytesIO(value))
        for data in (duplicate.getvalue(), b"invalid gzip/tar data"):
            modified = dict(files)
            modified[f"aster-{VERSION}-source.tar.gz"] = data
            refresh_checksums(modified)
            with self.subTest(duplicate=data == duplicate.getvalue()), self.assertRaises(pipeline.GuardError):
                pipeline.validate_package(modified, SHA, VERSION)

    def test_local_configuration_and_obvious_private_key_material_rejected(self):
        root = f"aster-{VERSION}-source/"
        for path, data in (("vps-billing/.env", b"fixture only"),
                           ("vps-billing/deploy/secrets/password", b"fixture only"),
                           ("vps-billing/trace.log", b"fixture only"),
                           ("vps-billing/notes.txt", b"-----BEGIN PRIVATE KEY-----\nsynthetic fixture\n")):
            with self.subTest(path=path), self.assertRaises(pipeline.GuardError):
                pipeline.validate_package(package_fixture(source_entries={root + path: data}), SHA, VERSION)


class StrictJSONTest(unittest.TestCase):
    def test_duplicates_at_every_depth_and_nonfinite_values_rejected(self):
        for raw in (b'{"same":1,"same":2}', b'{"outer":{"same":1,"same":2}}',
                    b'{"amount":NaN}', b'{"amount":Infinity}', b'{"amount":-Infinity}',
                    b'{} {}', b'\xff'):
            with self.subTest(raw=raw), self.assertRaises(pipeline.GuardError):
                pipeline.strict_json(raw)

    def test_ordinary_utf8_json_preserved(self):
        self.assertEqual(pipeline.strict_json('{"名称":"星屿云","enabled":false}'.encode()),
                         {"名称": "星屿云", "enabled": False})


class BuildPolicyTest(unittest.TestCase):
    def setUp(self):
        self.api, _, _, self.expected, _, _ = flow_fixture()
        self.env = {
            "GITHUB_REPOSITORY": REPOSITORY, "GITHUB_REPOSITORY_ID": str(REPOSITORY_ID),
            "GITHUB_RUN_ID": str(RUN_ID), "GITHUB_RUN_ATTEMPT": "1", "GITHUB_EVENT_NAME": "push",
            "GITHUB_SHA": SHA, "GITHUB_WORKFLOW_SHA": SHA, "GITHUB_REF": "refs/tags/" + TAG,
        }
        self.event = {"ref": "refs/tags/" + TAG, "deleted": False}

    def test_tag_build_uses_only_contents_and_required_ci_metadata_gets(self):
        self.assertEqual(pipeline.prepare_build(self.api, self.env, self.event), self.expected)
        self.assertTrue(all(call[0] == "GET" for call in self.api.calls))
        actions = {call[1] for call in self.api.calls if "/actions/" in call[1]}
        self.assertEqual(actions, {
            PREFIX + "/actions/workflows/ci.yml",
            PREFIX + "/actions/workflows/ci.yml/runs?branch=main&event=push&head_sha=" + SHA + "&per_page=100",
            PREFIX + "/actions/runs/701/attempts/1/jobs?per_page=100",
        })
        self.assertFalse(self.api.uploads)

    def test_manual_build_uses_default_workflow_and_requested_tag_source(self):
        self.env.update(GITHUB_EVENT_NAME="workflow_dispatch", GITHUB_REF="refs/heads/main",
                        GITHUB_SHA=DEFAULT_SHA, GITHUB_WORKFLOW_SHA=DEFAULT_SHA)
        result = pipeline.prepare_build(self.api, self.env, {"inputs": {"tag": TAG}})
        self.assertEqual(result["source_sha"], SHA)
        self.assertEqual(result["run_head_sha"], DEFAULT_SHA)
        self.assertEqual(result["event"], "workflow_dispatch")
        self.assertFalse(self.api.mutations)

    def test_wrong_environment_deleted_tag_or_branch_push_rejected(self):
        for field, value in (("GITHUB_REPOSITORY", "attacker/fork"), ("GITHUB_REPOSITORY_ID", "1"),
                             ("GITHUB_WORKFLOW_SHA", OTHER_SHA), ("GITHUB_EVENT_NAME", "pull_request"),
                             ("GITHUB_REF", "refs/heads/main")):
            with self.subTest(field=field), self.assertRaises(pipeline.GuardError):
                pipeline.prepare_build(self.api, dict(self.env, **{field: value}), self.event)
        with self.assertRaises(pipeline.GuardError):
            pipeline.prepare_build(self.api, self.env, dict(self.event, deleted=True))
        self.assertFalse(self.api.mutations)

    def test_public_repository_identity_and_state_required(self):
        original = copy.deepcopy(self.api.responses[("GET", PREFIX)])
        for field, value in (("id", 1), ("full_name", "attacker/fork"), ("private", True),
                             ("private", None), ("private", 0),
                             ("archived", True), ("disabled", True), ("default_branch", "../main")):
            self.api.responses[("GET", PREFIX)] = dict(original, **{field: value})
            with self.subTest(field=field), self.assertRaises(pipeline.GuardError):
                pipeline.prepare_build(self.api, self.env, self.event)
        self.assertFalse(self.api.mutations)

    def test_manual_build_from_another_branch_or_old_default_rejected(self):
        for ref, head in (("refs/heads/development", DEFAULT_SHA), ("refs/heads/main", SHA)):
            env = dict(self.env, GITHUB_EVENT_NAME="workflow_dispatch", GITHUB_REF=ref,
                       GITHUB_SHA=head, GITHUB_WORKFLOW_SHA=head)
            with self.subTest(ref=ref, head=head), self.assertRaises(pipeline.GuardError):
                pipeline.prepare_build(self.api, env, {"inputs": {"tag": TAG}})

    def test_unmerged_source_and_wrong_merge_base_rejected(self):
        path = ("GET", PREFIX + f"/compare/{SHA}...{DEFAULT_SHA}")
        for response in ({"status": "diverged", "merge_base_commit": {"sha": SHA}},
                         {"status": "behind", "merge_base_commit": {"sha": SHA}},
                         {"status": "ahead", "merge_base_commit": {"sha": OTHER_SHA}}):
            self.api.responses[path] = response
            with self.subTest(response=response), self.assertRaises(pipeline.GuardError):
                pipeline.prepare_build(self.api, self.env, self.event)

    def test_changed_missing_symlink_or_truncated_trusted_policy_rejected(self):
        path = ("GET", PREFIX + f"/git/trees/{SHA}?recursive=1")
        original = copy.deepcopy(self.api.responses[path])
        variants = []
        changed = copy.deepcopy(original)
        changed["tree"][0]["sha"] = OTHER_SHA
        variants.append(changed)
        variants.append(dict(original, tree=original["tree"][1:]))
        variants.append(dict(original, truncated=True))
        symlink = copy.deepcopy(original)
        symlink["tree"][0]["mode"] = "120000"
        variants.append(symlink)
        variants.append(dict(original, tree=original["tree"] + [original["tree"][0]]))
        for index, tree in enumerate(variants):
            self.api.responses[path] = tree
            with self.subTest(index=index), self.assertRaises(pipeline.GuardError):
                pipeline.prepare_build(self.api, self.env, self.event)
        self.assertFalse(self.api.mutations)

    def test_annotated_tag_resolves_to_commit_and_cycles_rejected(self):
        ref_key = ("GET", PREFIX + "/git/ref/tags/" + TAG)
        self.api.responses[ref_key]["object"] = {"type": "tag", "sha": OTHER_SHA}
        tag_key = ("GET", PREFIX + "/git/tags/" + OTHER_SHA)
        self.api.responses[tag_key] = {"sha": OTHER_SHA, "object": {"type": "commit", "sha": SHA}}
        self.assertEqual(pipeline.prepare_build(self.api, self.env, self.event)["source_sha"], SHA)
        self.api.responses[tag_key]["object"] = {"type": "tag", "sha": OTHER_SHA}
        with self.assertRaises(pipeline.GuardError):
            pipeline.prepare_build(self.api, self.env, self.event)

    def test_seal_adds_exact_provenance_and_refuses_overwrite(self):
        files = package_fixture()
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for name, data in files.items():
                (directory / name).write_bytes(data)
            provenance = pipeline.seal_build(self.expected, directory)
            saved = (directory / "PROVENANCE.json").read_bytes()
            self.assertEqual(pipeline.strict_json(saved), provenance)
            self.assertEqual(provenance["assets"], pipeline.validate_package(files, SHA, VERSION))
            with self.assertRaises(pipeline.GuardError):
                pipeline.seal_build(self.expected, directory)
            self.assertEqual((directory / "PROVENANCE.json").read_bytes(), saved)


class SourceBindingTest(unittest.TestCase):
    def test_every_retained_source_byte_matches_git_blob(self):
        api, _, _, provenance, files, _ = flow_fixture()
        pipeline.verify_source_archive(api, files, provenance)
        self.assertFalse(api.mutations)

    def test_missing_extra_or_modified_source_rejected_even_with_new_tar_checksums(self):
        for mutation in ("missing", "extra", "modified"):
            api, _, _, provenance, files, _ = flow_fixture()
            root = f"aster-{VERSION}-source"
            source = pipeline.tar_files(files[root + ".tar.gz"], root)
            if mutation == "missing":
                del source["README.md"]
            elif mutation == "extra":
                source["vps-billing/backend/unreviewed.go"] = b"fixture new code"
            else:
                source["vps-billing/deploy/ci/release_pipeline.py"] += b"changed after checkout"
            files[root + ".tar.gz"] = tar_bytes({root + "/" + name: data for name, data in source.items()})
            with self.subTest(mutation=mutation), self.assertRaises(pipeline.GuardError):
                pipeline.verify_source_archive(api, files, provenance)

    def test_git_symlinks_submodules_duplicate_paths_and_truncated_tree_rejected(self):
        for mutation in ("symlink", "submodule", "duplicate", "truncated"):
            api, _, _, provenance, files, _ = flow_fixture()
            tree = api.responses[("GET", PREFIX + f"/git/trees/{SHA}?recursive=1")]
            item = next(entry for entry in tree["tree"] if entry["path"] == "README.md")
            if mutation == "symlink":
                item["mode"] = "120000"
            elif mutation == "submodule":
                item.update(mode="160000", type="commit")
            elif mutation == "duplicate":
                tree["tree"].append(dict(item))
            else:
                tree["truncated"] = True
            with self.subTest(mutation=mutation), self.assertRaises(pipeline.GuardError):
                pipeline.verify_source_archive(api, files, provenance)

    def test_documented_build_and_secret_outputs_pruned_consistently(self):
        api, _, _, provenance, files, _ = flow_fixture()
        tree = api.responses[("GET", PREFIX + f"/git/trees/{SHA}?recursive=1")]
        tree["tree"].extend({"path": path, "sha": OTHER_SHA, "type": "blob", "mode": "100644"} for path in (
            "vps-billing/frontend/node_modules/package/index.js", "vps-billing/frontend/dist/index.html",
            "vps-billing/.env", "vps-billing/deploy/secrets/password", "vps-billing/tests/http-results.txt",
        ))
        pipeline.verify_source_archive(api, files, provenance)


class PublisherPreparationTest(unittest.TestCase):
    def test_completed_same_repository_successful_build_accepted_readonly(self):
        api, env, event, _, _, _ = flow_fixture()
        policy = pipeline.prepare_publish(api, env, event)
        self.assertEqual(policy["run_id"], RUN_ID)
        self.assertEqual(policy["artifact"]["id"], ARTIFACT_ID)
        self.assertEqual(policy["trusted_default_sha"], DEFAULT_SHA)
        self.assertFalse(api.mutations)

    def test_publisher_must_execute_current_default_commit(self):
        for field, value in (("GITHUB_EVENT_NAME", "push"), ("GITHUB_REF", "refs/heads/feature"),
                             ("GITHUB_SHA", OTHER_SHA), ("GITHUB_WORKFLOW_SHA", OTHER_SHA)):
            api, env, event, _, _, _ = flow_fixture()
            env[field] = value
            with self.subTest(field=field), self.assertRaises(pipeline.GuardError):
                pipeline.prepare_publish(api, env, event)
            self.assertFalse(api.mutations)

    def test_failed_fork_wrong_attempt_workflow_or_event_rejected(self):
        for field, value in (("status", "in_progress"), ("conclusion", "failure"), ("run_attempt", 2),
                             ("workflow_id", 99), ("path", ".github/workflows/other.yml"),
                             ("event", "pull_request"), ("head_repository", {"id": 99, "full_name": "attacker/fork"})):
            api, env, event, _, _, _ = flow_fixture()
            api.responses[("GET", PREFIX + f"/actions/runs/{RUN_ID}")][field] = value
            with self.subTest(field=field), self.assertRaises(pipeline.GuardError):
                pipeline.prepare_publish(api, env, event)
            self.assertFalse(api.mutations)

    def test_event_payload_must_match_api_run(self):
        for field, value in (("head_sha", OTHER_SHA), ("workflow_id", 2), ("event", "workflow_dispatch"),
                             ("conclusion", "failure")):
            api, env, event, _, _, _ = flow_fixture()
            event["workflow_run"][field] = value
            with self.subTest(field=field), self.assertRaises(pipeline.GuardError):
                pipeline.prepare_publish(api, env, event)

    def test_expired_missing_digest_wrong_owner_head_and_duplicate_artifacts_rejected(self):
        for field, value in (("expired", True), ("digest", None), ("digest", "sha256:bad"),
                             ("size_in_bytes", 0), ("size_in_bytes", pipeline.MAX_ZIP + 1),
                             ("name", f"candidate-{RUN_ID}-2"),
                             ("workflow_run", {"id": RUN_ID, "repository_id": 1, "head_repository_id": 1}),
                             ("workflow_run", {"id": RUN_ID, "repository_id": REPOSITORY_ID,
                                               "head_repository_id": REPOSITORY_ID, "head_sha": OTHER_SHA})):
            api, env, event, _, _, _ = flow_fixture()
            inventory = api.responses[("GET", PREFIX + f"/actions/runs/{RUN_ID}/artifacts?per_page=100&page=1")]
            inventory["artifacts"][0][field] = value
            with self.subTest(field=field, value=value), self.assertRaises(pipeline.GuardError):
                pipeline.prepare_publish(api, env, event)
        api, env, event, _, _, _ = flow_fixture()
        page = PREFIX + f"/actions/runs/{RUN_ID}/artifacts?per_page=100&page="
        candidate = api.responses[("GET", page + "1")]["artifacts"][0]
        api.responses[("GET", page + "1")] = {"artifacts": [candidate] + [{"name": "unrelated"}] * 99}
        api.responses[("GET", page + "2")] = {"artifacts": [candidate]}
        with self.assertRaises(pipeline.GuardError):
            pipeline.prepare_publish(api, env, event)
        self.assertFalse(api.mutations)


class PublishLifecycleTest(unittest.TestCase):
    def setUp(self):
        self.api, env, event, _, self.files, self.archive = flow_fixture()
        self.policy = pipeline.prepare_publish(self.api, env, event)
        self.api.calls.clear()

    def changed_provenance_archive(self, update):
        files = dict(self.files)
        provenance = pipeline.strict_json(files["PROVENANCE.json"])
        update(provenance)
        files["PROVENANCE.json"] = json_bytes(provenance)
        archive = zip_bytes(files)
        self.policy["artifact"].update(size_in_bytes=len(archive), digest="sha256:" + hashlib.sha256(archive).hexdigest())
        return archive

    def assert_no_write(self):
        self.assertFalse(self.api.mutations)
        self.assertFalse(self.api.uploads)

    def test_new_draft_prerelease_created_once_with_verified_five_assets(self):
        self.assertEqual(pipeline.publish(self.api, self.policy, self.archive), RELEASE_ID)
        self.assertEqual(len(self.api.mutations), 1)
        method, path, fields, _, _ = self.api.mutations[0]
        self.assertEqual((method, path), ("POST", PREFIX + "/releases"))
        self.assertIs(fields["draft"], True)
        self.assertIs(fields["prerelease"], True)
        self.assertEqual(fields["make_latest"], "false")
        self.assertIs(fields["generate_release_notes"], False)
        self.assertEqual((fields["tag_name"], fields["target_commitish"]), (TAG, SHA))
        self.assertEqual({name: data for _, name, data in self.api.uploads}, self.files)
        self.assertTrue(all(release_id == RELEASE_ID for release_id, _, _ in self.api.uploads))

    def test_archive_bytes_must_match_actions_digest_and_size(self):
        with self.assertRaises(pipeline.GuardError):
            pipeline.publish(self.api, self.policy, self.archive + b"tampered")
        self.assert_no_write()

    def test_manual_default_branch_run_can_publish_its_verified_older_tag(self):
        archive = self.changed_provenance_archive(lambda value: value.update(
            event="workflow_dispatch", run_head_sha=DEFAULT_SHA, workflow_sha=DEFAULT_SHA))
        run = self.api.responses[("GET", PREFIX + f"/actions/runs/{RUN_ID}")]
        run.update(event="workflow_dispatch", head_sha=DEFAULT_SHA, head_branch="main")
        artifact = self.api.responses[("GET", PREFIX + f"/actions/runs/{RUN_ID}/artifacts?per_page=100&page=1")]["artifacts"][0]
        artifact.update(size_in_bytes=len(archive), digest="sha256:" + hashlib.sha256(archive).hexdigest())
        artifact["workflow_run"]["head_sha"] = DEFAULT_SHA
        self.policy.update(event="workflow_dispatch", run_head_sha=DEFAULT_SHA, run_head_branch="main", artifact=copy.deepcopy(artifact))
        self.assertEqual(pipeline.publish(self.api, self.policy, archive), RELEASE_ID)
        self.assertEqual(self.api.mutations[0][2]["target_commitish"], SHA)

    def test_artifact_replacement_or_untrusted_run_branch_stops_before_create(self):
        artifact = self.api.responses[("GET", PREFIX + f"/actions/runs/{RUN_ID}/artifacts?per_page=100&page=1")]["artifacts"][0]
        artifact["id"] = ARTIFACT_ID + 1
        with self.assertRaisesRegex(pipeline.GuardError, "Artifact identity changed"):
            pipeline.publish(self.api, self.policy, self.archive)
        self.assert_no_write()
        self.setUp()
        self.policy["run_head_branch"] = "main"
        self.api.responses[("GET", PREFIX + f"/actions/runs/{RUN_ID}")]["head_branch"] = "main"
        with self.assertRaises(pipeline.GuardError):
            pipeline.publish(self.api, self.policy, self.archive)
        self.assert_no_write()

    def test_provenance_mismatch_never_creates_release(self):
        for field, value in (("schema", 2), ("repository", "attacker/fork"), ("repository_id", 1),
                             ("run_id", 1), ("run_attempt", 2), ("event", "pull_request"),
                             ("run_head_sha", OTHER_SHA), ("workflow_sha", OTHER_SHA),
                             ("workflow_path", ".github/workflows/untrusted.yml"),
                             ("artifact_name", "candidate-other"), ("version", "9.0.0-rc.1")):
            self.setUp()
            archive = self.changed_provenance_archive(lambda value_map: value_map.update({field: value}))
            with self.subTest(field=field), self.assertRaises(pipeline.GuardError):
                pipeline.publish(self.api, self.policy, archive)
            self.assert_no_write()

    def test_unknown_provenance_fields_and_asset_hash_mismatch_rejected(self):
        for update in (lambda provenance: provenance.update(extra="untrusted"),
                       lambda provenance: provenance["assets"]["BUILD.json"].update(sha256="0" * 64)):
            self.setUp()
            archive = self.changed_provenance_archive(update)
            with self.assertRaises(pipeline.GuardError):
                pipeline.publish(self.api, self.policy, archive)
            self.assert_no_write()

    def test_boolean_numeric_provenance_cannot_impersonate_integer_identity(self):
        for field in ("schema", "run_attempt"):
            self.setUp()
            archive = self.changed_provenance_archive(lambda provenance: provenance.update({field: True}))
            with self.subTest(field=field), self.assertRaises(pipeline.GuardError):
                pipeline.publish(self.api, self.policy, archive)
            self.assert_no_write()

    def test_tag_moved_before_validation_or_immediately_before_create_rejected(self):
        for change_at in (1, 3):
            self.setUp()
            calls = 0
            def changing_tag(_):
                nonlocal calls
                calls += 1
                return {"ref": "refs/tags/" + TAG, "object": {"type": "commit", "sha": OTHER_SHA if calls >= change_at else SHA}}
            self.api.responses[("GET", PREFIX + "/git/ref/tags/" + TAG)] = changing_tag
            with self.subTest(change_at=change_at), self.assertRaises(pipeline.GuardError):
                pipeline.publish(self.api, self.policy, self.archive)
            self.assert_no_write()

    def test_any_existing_draft_or_published_release_prevents_creation(self):
        for draft, page in ((True, 1), (False, 1), (True, 2)):
            self.setUp()
            if page == 2:
                self.api.responses[("GET", PREFIX + "/releases?per_page=100&page=1")] = [
                    {"tag_name": f"v0.0.{i}-rc.1", "draft": True} for i in range(100)
                ]
            self.api.responses[("GET", PREFIX + f"/releases?per_page=100&page={page}")] = [{"tag_name": TAG, "draft": draft}]
            with self.subTest(draft=draft, page=page), self.assertRaises(pipeline.GuardError):
                pipeline.publish(self.api, self.policy, self.archive)
            self.assert_no_write()

    def test_tag_endpoint_existing_release_and_permission_failure_do_not_mean_absent(self):
        for response in ({"tag_name": TAG, "draft": True}, pipeline.APIError(403), pipeline.APIError(503)):
            self.setUp()
            self.api.responses[("GET", PREFIX + "/releases/tags/" + TAG)] = response
            with self.assertRaises(pipeline.GuardError):
                pipeline.publish(self.api, self.policy, self.archive)
            self.assert_no_write()

    def test_uncertain_creation_is_not_retried_or_followed_by_upload(self):
        for failure in (pipeline.APIError(503), pipeline.GuardError("synthetic outcome uncertain")):
            self.setUp()
            self.api.responses[("POST", PREFIX + "/releases")] = failure
            with self.assertRaisesRegex(pipeline.GuardError, "uncertain outcome"):
                pipeline.publish(self.api, self.policy, self.archive)
            self.assertEqual(len(self.api.mutations), 1)
            self.assertFalse(self.api.uploads)

    def test_unexpected_created_release_state_stops_before_upload(self):
        for field, value in (("draft", False), ("prerelease", False), ("tag_name", "v9.0.0-rc.1")):
            self.setUp()
            self.api.responses[("POST", PREFIX + "/releases")][field] = value
            with self.subTest(field=field), self.assertRaisesRegex(pipeline.GuardError, str(RELEASE_ID)):
                pipeline.publish(self.api, self.policy, self.archive)
            self.assertEqual(len(self.api.mutations), 1)
            self.assertFalse(self.api.uploads)

    def test_partial_upload_stops_without_retry_edit_delete_or_publication(self):
        self.api.fail_upload_number = 2
        with self.assertRaisesRegex(pipeline.GuardError, str(RELEASE_ID)):
            pipeline.publish(self.api, self.policy, self.archive)
        self.assertEqual(len(self.api.uploads), 2)
        self.assertEqual(len(self.api.mutations), 1)
        draft = self.api.responses[("GET", PREFIX + f"/releases/{RELEASE_ID}")]
        self.assertIs(draft["draft"], True)
        self.assertEqual(len(draft["assets"]), 1)

    def test_asset_overwrite_and_wrong_upload_digest_stop(self):
        self.api.responses[("GET", PREFIX + f"/releases/{RELEASE_ID}")]["assets"] = [{"name": sorted(self.files)[0]}]
        with self.assertRaises(pipeline.GuardError):
            pipeline.publish(self.api, self.policy, self.archive)
        self.assertFalse(self.api.uploads)
        self.setUp()
        original = self.api.upload
        def wrong_digest(release_id, name, data):
            result = original(release_id, name, data)
            result["digest"] = "sha256:" + "0" * 64
            return result
        with mock.patch.object(self.api, "upload", side_effect=wrong_digest), self.assertRaises(pipeline.GuardError):
            pipeline.publish(self.api, self.policy, self.archive)
        self.assertEqual(len(self.api.uploads), 1)
        self.assertEqual(len(self.api.mutations), 1)

    def test_tag_movement_after_first_upload_stops_remaining_assets(self):
        def moving_tag(_):
            return {"ref": "refs/tags/" + TAG, "object": {"type": "commit", "sha": OTHER_SHA if self.api.uploads else SHA}}
        self.api.responses[("GET", PREFIX + "/git/ref/tags/" + TAG)] = moving_tag
        with self.assertRaisesRegex(pipeline.GuardError, str(RELEASE_ID)):
            pipeline.publish(self.api, self.policy, self.archive)
        self.assertEqual(len(self.api.uploads), 1)
        self.assertEqual(len(self.api.mutations), 1)

    def test_final_asset_state_identity_and_inventory_are_rechecked(self):
        for mutation in ("duplicate_id", "unfinished", "extra", "not_draft", "wrong_release_id"):
            self.setUp()
            original = self.api.upload
            def alter_final_state(release_id, name, data):
                result = original(release_id, name, data)
                if len(self.api.uploads) == 5:
                    state = self.api.responses[("GET", PREFIX + f"/releases/{RELEASE_ID}")]
                    if mutation == "duplicate_id":
                        state["assets"][1]["id"] = state["assets"][0]["id"]
                    elif mutation == "unfinished":
                        state["assets"][0]["state"] = "new"
                    elif mutation == "extra":
                        state["assets"].append({"id": 9999, "state": "uploaded", "name": "unreviewed.txt", "size": 1, "digest": "sha256:" + "0" * 64})
                    elif mutation == "not_draft":
                        state["draft"] = False
                    else:
                        state["id"] = RELEASE_ID + 1
                return result
            with mock.patch.object(self.api, "upload", side_effect=alter_final_state), self.subTest(mutation=mutation), self.assertRaisesRegex(pipeline.GuardError, str(RELEASE_ID)):
                pipeline.publish(self.api, self.policy, self.archive)
            self.assertEqual(len(self.api.uploads), 5)
            self.assertEqual(len(self.api.mutations), 1)


class TransportBoundaryTest(unittest.TestCase):
    def test_authenticated_requests_never_leave_fixed_hosts_or_repository(self):
        api = pipeline.GitHub("fixture-token")
        with mock.patch.object(api.opener, "open") as open_mock:
            for url in ("https://attacker.invalid/collect", "http://api.github.com/repos/x",
                        "https://api.github.com.attacker.invalid/collect", "https://user@api.github.com/path"):
                with self.subTest(url=url), self.assertRaises(pipeline.GuardError):
                    api._open("GET", url)
            with self.assertRaises(pipeline.GuardError):
                api.request("POST", "/repos/attacker/fork/releases", data={})
            open_mock.assert_not_called()

    def test_artifact_redirect_drops_authorization_and_checks_download_digest(self):
        _, _, _, _, _, archive = flow_fixture()
        artifact = {"id": ARTIFACT_ID, "size_in_bytes": len(archive), "digest": "sha256:" + hashlib.sha256(archive).hexdigest()}
        api = pipeline.GitHub("fixture-token")
        destination = "https://fixture.blob.core.windows.net/private?sig=synthetic"
        with mock.patch.object(api, "_open", side_effect=[(302, {"Location": destination}, b""), (200, {}, archive)]) as opened:
            self.assertEqual(api.download_artifact(artifact), archive)
            self.assertIs(opened.call_args_list[1].kwargs["authenticated"], False)
            self.assertEqual(opened.call_args_list[1].args[1], destination)

    def test_untrusted_artifact_redirect_never_followed(self):
        api = pipeline.GitHub("fixture-token")
        artifact = {"id": ARTIFACT_ID, "size_in_bytes": 1, "digest": "sha256:" + "0" * 64}
        for destination in ("https://attacker.invalid/collect", "https://blob.core.windows.net.attacker.invalid/file"):
            with mock.patch.object(api, "_open", return_value=(302, {"Location": destination}, b"")) as opened:
                with self.subTest(destination=destination), self.assertRaises(pipeline.GuardError):
                    api.download_artifact(artifact)
                self.assertEqual(opened.call_count, 1)

    def test_network_failure_never_retries_mutation_or_exposes_exception_details(self):
        api = pipeline.GitHub("fixture-token")
        with mock.patch.object(api.opener, "open", side_effect=urllib.error.URLError("fixture-token sensitive details")) as opened:
            with self.assertRaises(pipeline.GuardError) as raised:
                api.request("POST", PREFIX + "/releases", data={"draft": True})
            self.assertEqual(opened.call_count, 1)
            self.assertNotIn("fixture-token", str(raised.exception))
            self.assertIn("uncertain", str(raised.exception))


class PatchReleaseNotesTest(unittest.TestCase):
    def published_notes(self, kind, version):
        from test_runtime_release import normal_flow, pipeline as runtime_pipeline
        api, env, event, _, artifact, _, _ = normal_flow(kind, raw=True, version=version)
        runtime_pipeline.publish(api, runtime_pipeline.prepare_publish(api, env, event), artifact)
        return api, api.mutations[0][2]["body"]

    def test_059_notes_cover_own_product_contract_schema13_and_explicit_manual_boundary(self):
        api, body = self.published_notes("core", "0.5.9")
        self.assertTrue(body.startswith("## Core 0.5.9\n\n"))
        for text in ("正式 Core 0.5.8", "产品配置", "不可变订单私密配置快照", "013_product_configurations.sql",
                     "schema 13 / 23 表", "001–012", "不回填旧单", "prices_revision", "旧合法幂等重放", "PlanSnapshot",
                     "aster.controller/1", "backend/controllerapi", "不对接 IDCsmart API", "默认无 worker", "paid",
                     "uncertain", "不自动重发", "HTTP 409", "认证/CSRF", "权限边界", "TLS", "六资产",
                     "SMTP 仍独立 1.1.0", "Core >=0.4.0,<0.6.0", "不新增真实付款或新支付渠道", "不使用 Docker",
                     "schema12 程序不能直接运行 schema13", "不宣称所有 Linux 均已支持"):
            self.assertIn(text, body)
        for misleading in ("不新增数据库迁移", "最终要求 schema 12", "已完成真实开通", "已对接魔方云 API", "本版本通过该构建的"):
            self.assertNotIn(misleading, body)
        for document in ("RELEASE-v0.5.9.md", "PRODUCT_CONFIGURATION.md", "VALIDATION.md", "INSTALL.md", "UPGRADE.md", "BAOTA.md", "PLUGINS.md"):
            self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/blob/{SHA}/vps-billing/docs/{document}", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/commit/{SHA}", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/actions/runs/{RUN_ID}", body)
        self.assertEqual(len(api.uploads), 6)
        self.assertEqual([call[0] for call in api.mutations], ["POST", "PATCH"])

    def test_058_notes_cover_four_level_purchase_schema12_and_preserved_financial_boundaries(self):
        api, body = self.published_notes("core", "0.5.8")
        self.assertTrue(body.startswith("## Core 0.5.8\n\n"))
        for text in ("正式 Core 0.5.7", "类型→地区→节点→套餐", "011", "012", "schema 12",
                     "001–011", "默认节点", "旧报价", "失效", "历史快照", "支付幂等",
                     "认证/CSRF", "权限边界", "CNY", "六资产", "SMTP", "1.1.0", "不使用 Docker"):
            self.assertIn(text, body)
        self.assertRegex(body, r"(?:不提升|不增加|不改变|不递增).{0,40}(?:报价修订|报价 revision|price_revision|prices_revision)")
        self.assertRegex(body, r"(?:分类编辑|分类修改|编辑分类|修改分类).{0,100}旧报价.{0,40}失效")
        self.assertRegex(body, r"(?:不新增|不提供|不包含|不引入).{0,60}(?:真实资源开通|真实 VPS 开通|真实开通)")
        self.assertRegex(body, r"(?:不新增|不提供|不包含|不引入).{0,60}新支付渠道")
        for misleading in ("本次不新增数据库迁移", "继续 schema 10", "该版本已通过流水线"):
            self.assertNotIn(misleading, body)
        for document in ("RELEASE-v0.5.8.md", "VALIDATION.md", "INSTALL.md", "UPGRADE.md", "BAOTA.md", "PLUGINS.md"):
            self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/blob/{SHA}/vps-billing/docs/{document}", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/commit/{SHA}", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/actions/runs/{RUN_ID}", body)
        self.assertEqual(len(api.uploads), 6)
        self.assertEqual([call[0] for call in api.mutations], ["POST", "PATCH"])

    def test_057_notes_cover_new_frontend_contrast_and_exact_financial_release_boundaries(self):
        api, body = self.published_notes("core", "0.5.7")
        self.assertTrue(body.startswith("## Core 0.5.7\n\n"))
        for text in ("正式 Core 0.5.6", "独立实现的新客户与管理员前端", "真实认证", "API 失败不切换为演示成功",
                     "语言与币种", "账户", "浅色对比度", "深色", "CNY 优先", "全新安装仅启用 CNY",
                     "保留原币", "不自动换汇", "历史快照", "支付幂等", "认证/CSRF", "权限边界",
                     "不新增数据库迁移", "schema 10", "001–010", "不使用 Docker", "六资产", "SMTP 仍独立 1.1.0",
                     "Core >=0.4.0,<0.6.0", "实际执行范围和结果", "不宣称所有 Linux 均已支持", "生产容量验收"):
            self.assertIn(text, body)
        for misleading in ("本版本通过该构建的", "该版本已通过流水线", "新增客户与管理员角色管理", "支付扩展新增独立运行时插槽"):
            self.assertNotIn(misleading, body)
        for document in ("RELEASE-v0.5.7.md", "VALIDATION.md", "INSTALL.md", "UPGRADE.md", "BAOTA.md", "PLUGINS.md"):
            self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/blob/{SHA}/vps-billing/docs/{document}", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/commit/{SHA}", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/actions/runs/{RUN_ID}", body)
        self.assertEqual(len(api.uploads), 6)
        self.assertEqual([call[0] for call in api.mutations], ["POST", "PATCH"])
        self.assertEqual(api.mutations[-1][2], {"draft": False, "prerelease": False, "make_latest": "true"})

    def test_056_notes_cover_management_and_preserve_financial_boundaries(self):
        _, body = self.published_notes("core", "0.5.6")
        for text in ("客户与管理员角色管理", "最后一个启用管理员", "单笔订单详情", "商品创建", "历史快照不变", "迁移 009", "010", "schema 10", "v0.5.5", "Go 1.27.3", "不使用 Docker", "实际验证范围分别记录"):
            self.assertIn(text, body)
        self.assertNotIn("不新增业务功能", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/commit/{SHA}", body)

    def test_055_notes_describe_only_the_ui_patch_without_claiming_release_completion(self):
        api, body = self.published_notes("core", "0.5.5")
        self.assertIn("Core 0.5.4 之后的界面维护补丁", body)
        for text in ("客户首页", "规格比较和订单摘要", "桌面侧栏独立滚动", "1024px", "键盘焦点",
                     "账单在窄屏", "不新增业务功能", "金额精度、安全或多币种历史格式", "不自动换汇",
                     "人工视觉复验、自动化检查和原生安装验收分别记录", "不表示正常 Release 已完成发布"):
            self.assertIn(text, body)
        self.assertRegex(body, r"支付运行时自 Core 0\.5\.3.*aster\.payment/1")
        self.assertNotIn("支付扩展新增独立运行时插槽", body)
        self.assertNotIn("已有错误随当前语言显示", body)
        self.assertNotIn("本版本通过该构建的", body)
        for document in ("VALIDATION.md", "INSTALL.md", "BAOTA.md", "UPGRADE.md", "PLUGINS.md"):
            self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/blob/{SHA}/vps-billing/docs/{document}", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/commit/{SHA}", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/actions/runs/{RUN_ID}", body)
        self.assertEqual(len(api.uploads), 6)
        self.assertEqual([call[0] for call in api.mutations], ["POST", "PATCH"])
        self.assertEqual(api.mutations[-1][2], {"draft": False, "prerelease": False, "make_latest": "true"})

    def test_054_notes_describe_a_bounded_maintenance_patch(self):
        api, body = self.published_notes("core", "0.5.4")
        self.assertIn("维护补丁", body)
        self.assertRegex(body, r"登录页.*当前语言.*加载与重试")
        self.assertRegex(body, r"支付运行时自 Core 0\.5\.3.*aster\.payment/1")
        self.assertRegex(body, r"人工视觉复验仅覆盖此前固定提交[\s\S]*组件行为回归验证[\s\S]*未重新查看截图")
        self.assertIn("合成只读负载", body)
        self.assertIn("不承诺生产容量", body)
        for document in ("VALIDATION.md", "INSTALL.md", "BAOTA.md", "UPGRADE.md", "PLUGINS.md"):
            self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/blob/{SHA}/vps-billing/docs/{document}", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/commit/{SHA}", body)
        self.assertIn(f"https://github.com/{pipeline.REPOSITORY}/actions/runs/{RUN_ID}", body)
        self.assertNotIn("支付扩展新增独立运行时插槽", body)
        self.assertEqual(len(api.uploads), 6)
        self.assertEqual([call[0] for call in api.mutations], ["POST", "PATCH"])
        self.assertEqual(api.mutations[-1][2], {"draft": False, "prerelease": False, "make_latest": "true"})

    def test_053_notes_keep_the_existing_description(self):
        api, body = self.published_notes("core", "0.5.3")
        self.assertIn("Aster Core 0.5.3", body)
        self.assertIn("支付扩展新增独立运行时插槽", body)
        self.assertIn("界面修复了页头高度", body)
        self.assertNotIn("维护补丁", body)
        self.assertNotIn("已有错误随当前语言显示", body)
        self.assertEqual(len(api.uploads), 6)
        self.assertEqual(api.mutations[-1][2]["make_latest"], "true")

    def test_smtp_notes_keep_the_existing_independent_description(self):
        api, body = self.published_notes("smtp", "1.1.0")
        self.assertIn("Aster official SMTP plugin 1.1.0", body)
        self.assertIn("aster.smtp/1", body)
        self.assertIn(">=0.4.0,<0.6.0", body)
        self.assertIn("共五项", body)
        self.assertNotIn("维护补丁", body)
        self.assertNotIn("aster.payment/1", body)
        self.assertEqual(len(api.uploads), 5)
        self.assertEqual(api.mutations[-1][2]["make_latest"], "false")


class FinancialCIGateTests(unittest.TestCase):
    runs_path = PREFIX + "/actions/workflows/ci.yml/runs?branch=main&event=push&head_sha=" + SHA + "&per_page=100"
    jobs_path = PREFIX + "/actions/runs/701/attempts/1/jobs?per_page=100"

    def test_all_five_exact_jobs_are_required_and_reads_never_mutate(self):
        api, *_ = flow_fixture()
        result = pipeline.green_financial_ci(api, SHA)
        self.assertEqual(result["source_sha"], SHA)
        self.assertEqual(len(result["jobs"]), 5)
        self.assertIn("Native financial acceptance (ubuntu-22.04)", result["jobs"])
        self.assertIn("Real MySQL financial browser acceptance", result["jobs"])
        self.assertTrue(all(call[0] == "GET" for call in api.calls))
        self.assertFalse(api.uploads)

    def test_missing_duplicate_extra_and_substitute_jobs_are_rejected(self):
        original, *_ = flow_fixture()
        baseline = original.responses[("GET", self.jobs_path)]["jobs"]
        for missing in range(len(baseline)):
            for replacement in (None, {"name": "Synthetic demo only", "status": "completed", "conclusion": "success"}, baseline[(missing + 1) % len(baseline)]):
                api, *_ = flow_fixture()
                jobs = copy.deepcopy(baseline)
                jobs.pop(missing)
                if replacement:
                    jobs.append(copy.deepcopy(replacement))
                api.responses[("GET", self.jobs_path)] = {"total_count": len(jobs), "jobs": jobs}
                with self.subTest(missing=missing, replacement=replacement), self.assertRaises(pipeline.GuardError):
                    pipeline.green_financial_ci(api, SHA)

    def test_every_job_must_succeed_without_skips(self):
        for index in range(5):
            for conclusion in ("skipped", "failure", "cancelled", None):
                api, *_ = flow_fixture()
                api.responses[("GET", self.jobs_path)]["jobs"][index]["conclusion"] = conclusion
                with self.subTest(index=index, conclusion=conclusion), self.assertRaises(pipeline.GuardError):
                    pipeline.green_financial_ci(api, SHA)

    def test_latest_failed_run_never_falls_back_to_an_older_success(self):
        api, *_ = flow_fixture()
        old = api.responses[("GET", self.runs_path)]["workflow_runs"][0]
        latest = {**old, "id": 703, "conclusion": "failure"}
        api.responses[("GET", self.runs_path)] = {"total_count": 2, "workflow_runs": [old, latest]}
        with self.assertRaisesRegex(pipeline.GuardError, "Latest required"):
            pipeline.green_financial_ci(api, SHA)

    def test_foreign_sha_branch_event_workflow_and_incomplete_run_lists_do_not_authorize(self):
        for field, value in (("head_sha", "f" * 40), ("head_branch", "other"), ("event", "workflow_dispatch"), ("workflow_id", 999)):
            api, *_ = flow_fixture()
            api.responses[("GET", self.runs_path)]["workflow_runs"][0][field] = value
            with self.subTest(field=field), self.assertRaises(pipeline.GuardError):
                pipeline.green_financial_ci(api, SHA)
        for count in (None, True, 0, 101, "1"):
            api, *_ = flow_fixture()
            api.responses[("GET", self.runs_path)]["total_count"] = count
            with self.subTest(count=count), self.assertRaises(pipeline.GuardError):
                pipeline.green_financial_ci(api, SHA)

    def test_no_release_mutation_or_upload_precedes_the_required_ubuntu22_gate(self):
        api, env, event, *_rest, archive = flow_fixture()
        policy = pipeline.prepare_publish(api, env, event)
        jobs = api.responses[("GET", self.jobs_path)]["jobs"]
        next(job for job in jobs if job["name"] == "Native financial acceptance (ubuntu-22.04)")["conclusion"] = "failure"
        with self.assertRaises(pipeline.GuardError):
            pipeline.publish(api, policy, archive)
        self.assertTrue(all(call[0] == "GET" for call in api.calls))
        self.assertFalse(api.uploads)

    def test_financial_and_cloud_required_finance_job_sets_stay_identical(self):
        # Read the constant expression only; never execute the other publisher.
        import ast
        cloud = Path(__file__).resolve().parents[3] / 'aster-cloud/deploy/ci/cloud_release.py'
        tree = ast.parse(cloud.read_text())
        node = next(node for node in tree.body if isinstance(node, ast.Assign) and
                    any(isinstance(target, ast.Name) and target.id == 'FINANCE_JOBS' for target in node.targets))
        self.assertEqual(ast.literal_eval(node.value), pipeline.FINANCIAL_CI_JOBS)


if __name__ == "__main__":
    unittest.main()
