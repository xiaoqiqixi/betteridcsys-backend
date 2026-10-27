#!/usr/bin/env python3
"""Offline archival data tests. No network, mutations or historical code execution."""
import base64
import copy
import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import zipfile

from test_release_pipeline import (
    FakeAPI, PREFIX, RELEASE_ID, flow_fixture, package_fixture, pipeline,
)


class HistoricalAPI(FakeAPI):
    def __init__(self):
        super().__init__()
        self.blobs = {}
        self.blob_calls = []

    def historical_blob(self, sha, size):
        self.blob_calls.append((sha, size))
        value = self.blobs[sha]
        if isinstance(value, Exception):
            raise value
        return value


def fixture():
    api = HistoricalAPI()
    manifest = {"schema": 1, "repository": pipeline.REPOSITORY,
                "repository_id": pipeline.REPOSITORY_ID,
                "release_tag": "v0.3.0-rc.3", "archives": []}
    originals = {}
    for index in (1, 2):
        version = f"0.3.0-rc.{index}"
        source_sha = str(index + 2) * 40
        artifact_sha = str(index) * 40
        files = dict(package_fixture(version=version, source_sha=source_sha), **{"README.md": b"original historical instructions\n"})
        entry = {"version": version, "original_source_commit": source_sha,
                 "artifact_commit": artifact_sha, "files": {name: {
                     "git_blob": pipeline.git_blob_sha(data), "sha256": pipeline.sha256(data), "size": len(data),
                 } for name, data in files.items()}}
        zipped = pipeline.historical_zip(entry, files)
        entry["attachment"] = {"name": f"historical-v{version}.zip", "sha256": pipeline.sha256(zipped), "size": len(zipped)}
        manifest["archives"].append(entry)
        originals[version] = files
        api.blobs.update({entry["files"][name]["git_blob"]: data for name, data in files.items()})
        api.responses[("GET", PREFIX + "/git/trees/" + artifact_sha + "?recursive=1")] = {
            "truncated": False, "tree": [{"path": name, "mode": "100644", "type": "blob", "sha": meta["git_blob"]}
                                         for name, meta in entry["files"].items()]}
    return manifest, api, originals


class HistoricalPolicyTests(unittest.TestCase):
    def validate(self, value):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "policy.json"
            path.write_bytes(pipeline.json_bytes(value))
            with mock.patch.object(pipeline, "HISTORICAL_MANIFEST", path):
                return pipeline.historical_manifest()

    def test_committed_policy_is_exact_and_trusted(self):
        value = pipeline.historical_manifest()
        self.assertEqual(value["release_tag"], "v0.3.0-rc.3")
        self.assertIn("vps-billing/deploy/ci/historical-artifacts.json", pipeline.TRUSTED_PATHS)
        self.assertEqual({item["version"] for item in value["archives"]}, {"0.3.0-rc.1", "0.3.0-rc.2"})

    def test_synthetic_valid_policy_passes(self):
        value, _, _ = fixture()
        self.assertEqual(self.validate(value), value)

    def test_schema_repository_tag_and_extra_fields_rejected(self):
        for key, value in (("schema", True), ("repository_id", True), ("repository", "other/repo"),
                           ("release_tag", "v0.3.0-rc.4"), ("extra", "override")):
            manifest, _, _ = fixture()
            manifest[key] = value
            with self.subTest(key=key), self.assertRaises(pipeline.GuardError):
                self.validate(manifest)

    def test_missing_duplicate_and_unsupported_versions_rejected(self):
        for change in (lambda m: m["archives"].pop(),
                       lambda m: m["archives"].__setitem__(1, copy.deepcopy(m["archives"][0])),
                       lambda m: m["archives"][0].update(version="0.3.0-rc.99")):
            manifest, _, _ = fixture()
            change(manifest)
            with self.assertRaises(pipeline.GuardError):
                self.validate(manifest)

    def test_file_inventory_paths_sizes_and_hashes_rejected(self):
        changes = (
            lambda e: e["files"].pop("README.md"),
            lambda e: e["files"].update({"../README.md": e["files"].pop("README.md")}),
            lambda e: e["files"]["README.md"].update(size=True),
            lambda e: e["files"]["README.md"].update(size=pipeline.MAX_HISTORICAL_FILE + 1),
            lambda e: e["files"]["README.md"].update(git_blob="A" * 40),
            lambda e: e["files"]["README.md"].update(sha256="a" * 63),
            lambda e: e["attachment"].update(name="../archive.zip"),
            lambda e: e["attachment"].update(size=True),
            lambda e: e["attachment"].update(sha256="A" * 64),
        )
        for change in changes:
            manifest, _, _ = fixture()
            change(manifest["archives"][0])
            with self.assertRaises(pipeline.GuardError):
                self.validate(manifest)


class HistoricalDataTests(unittest.TestCase):
    def attachments(self, manifest, api, tag="v0.3.0-rc.3"):
        with mock.patch.object(pipeline, "historical_manifest", return_value=manifest):
            return pipeline.historical_attachments(api, tag)

    def test_all_original_files_preserved_and_zip_deterministic(self):
        manifest, api, originals = fixture()
        files, body = self.attachments(manifest, api)
        again, _ = self.attachments(manifest, api)
        self.assertEqual(files, again)
        self.assertEqual(len(api.blob_calls), 20)
        for entry in manifest["archives"]:
            with zipfile.ZipFile(io.BytesIO(files[entry["attachment"]["name"]])) as archive:
                self.assertEqual(len(archive.infolist()), 6)
                self.assertEqual(archive.read("ARCHIVE.json"), pipeline.json_bytes(json.loads(archive.read("ARCHIVE.json"))))
                metadata = json.loads(archive.read("ARCHIVE.json"))
                self.assertEqual(metadata["original_source_commit"], entry["original_source_commit"])
                self.assertEqual(metadata["files"], entry["files"])
                self.assertIn(entry["original_source_commit"], body)
                for name, data in originals[entry["version"]].items():
                    self.assertEqual(archive.read("v" + entry["version"] + "/" + name), data)
                for info in archive.infolist():
                    self.assertEqual(info.date_time, (1980, 1, 1, 0, 0, 0))
                    self.assertEqual(info.compress_type, zipfile.ZIP_STORED)
                    self.assertEqual(info.external_attr >> 16, 0o100644)
        self.assertFalse(api.mutations)
        self.assertFalse(api.uploads)

    def test_unrelated_candidate_does_not_read_historical_api_data(self):
        manifest, api, _ = fixture()
        self.assertEqual(self.attachments(manifest, api, "v0.3.0-rc.4"), ({}, ""))
        self.assertFalse(api.calls)
        self.assertFalse(api.blob_calls)

    def test_truncated_duplicate_extra_and_link_tree_rejected(self):
        for kind in ("truncated", "duplicate", "extra", "symlink", "wrong_blob"):
            manifest, api, _ = fixture()
            tree = next(iter(api.responses.values()))
            if kind == "truncated":
                tree["truncated"] = True
            elif kind == "duplicate":
                tree["tree"].append(copy.deepcopy(tree["tree"][0]))
            elif kind == "extra":
                tree["tree"].append({"path": "extra", "mode": "100644", "type": "blob", "sha": "a" * 40})
            elif kind == "symlink":
                tree["tree"][0]["mode"] = "120000"
            else:
                tree["tree"][0]["sha"] = "a" * 40
            with self.subTest(kind=kind), self.assertRaises(pipeline.GuardError):
                self.attachments(manifest, api)
            self.assertFalse(api.blob_calls)

    def test_tampered_missing_or_denied_blob_stops(self):
        for value in (b"wrong bytes", pipeline.APIError(404), pipeline.APIError(403)):
            manifest, api, _ = fixture()
            key = manifest["archives"][0]["files"]["BUILD.json"]["git_blob"]
            api.blobs[key] = value
            with self.assertRaises(pipeline.GuardError):
                self.attachments(manifest, api)
            self.assertFalse(api.mutations)

    def test_final_zip_size_or_hash_mismatch_rejected(self):
        for key, value in (("size", 1), ("sha256", "0" * 64)):
            manifest, api, _ = fixture()
            manifest["archives"][0]["attachment"][key] = value
            with self.subTest(key=key), self.assertRaises(pipeline.GuardError):
                self.attachments(manifest, api)

    def test_original_build_source_or_version_mismatch_rejected(self):
        manifest, api, _ = fixture()
        manifest["archives"][0]["original_source_commit"] = "9" * 40
        with self.assertRaisesRegex(pipeline.GuardError, "BUILD identity"):
            self.attachments(manifest, api)

    def test_original_checksum_file_is_actually_validated(self):
        manifest, api, _ = fixture()
        entry = manifest["archives"][0]
        bad = b"0" * 64 + b"  BUILD.json\n"
        meta = {"git_blob": pipeline.git_blob_sha(bad), "sha256": pipeline.sha256(bad), "size": len(bad)}
        entry["files"]["SHA256SUMS"] = meta
        api.blobs[meta["git_blob"]] = bad
        for item in next(iter(api.responses.values()))["tree"]:
            if item["path"] == "SHA256SUMS":
                item["sha"] = meta["git_blob"]
        with self.assertRaisesRegex(pipeline.GuardError, "checksum"):
            self.attachments(manifest, api)


class HistoricalBlobTransportTests(unittest.TestCase):
    def setUp(self):
        self.data = b"preserved original bytes\n"
        self.sha = pipeline.git_blob_sha(self.data)
        self.response = {"sha": self.sha, "size": len(self.data), "encoding": "base64", "content": base64.encodebytes(self.data).decode()}
        self.api = pipeline.GitHub("fixture-only-token")

    def download(self, response=None, status=200):
        with mock.patch.object(self.api, "_open", return_value=(status, {}, pipeline.json_bytes(response or self.response))) as request:
            data = self.api.historical_blob(self.sha, len(self.data))
            self.assertEqual(request.call_args.args, ("GET", "https://api.github.com" + PREFIX + "/git/blobs/" + self.sha))
            self.assertEqual(request.call_args.kwargs["limit"], len(self.data) * 2 + 8192)
            return data

    def test_exact_base64_blob_passes_bounded_read(self):
        self.assertEqual(self.download(), self.data)

    def test_wrong_identity_size_encoding_and_content_rejected(self):
        for key, value in (("sha", "f" * 40), ("size", True), ("size", 1), ("encoding", "utf-8"), ("content", "???"), ("content", base64.b64encode(b"tampered").decode())):
            changed = dict(self.response, **{key: value})
            with self.subTest(key=key), self.assertRaises(pipeline.GuardError):
                self.download(changed)

    def test_redirect_and_size_bound_rejected(self):
        with self.assertRaises(pipeline.GuardError):
            self.download(status=302)
        for size in (True, 0, pipeline.MAX_HISTORICAL_FILE + 1):
            with self.subTest(size=size), self.assertRaises(pipeline.GuardError):
                self.api.historical_blob(self.sha, size)


class HistoricalPublishIntegrationTests(unittest.TestCase):
    def setUp(self):
        self.api, env, event, _, self.files, self.archive = flow_fixture()
        self.policy = pipeline.prepare_publish(self.api, env, event)
        self.api.calls.clear()
        self.history = {"historical-v0.3.0-rc.1.zip": b"verified historical one", "historical-v0.3.0-rc.2.zip": b"verified historical two"}

    def test_history_loaded_before_creation_and_exact_seven_uploaded(self):
        def before_write(_api, _tag):
            self.assertFalse(self.api.mutations)
            self.assertFalse(self.api.uploads)
            return self.history, "\nExplicit historical source identities"
        with mock.patch.object(pipeline, "historical_attachments", side_effect=before_write):
            self.assertEqual(pipeline.publish(self.api, self.policy, self.archive), RELEASE_ID)
        self.assertEqual(len(self.api.mutations), 1)
        self.assertEqual({name: data for _, name, data in self.api.uploads}, dict(self.files, **self.history))
        self.assertIn("Explicit historical source identities", self.api.mutations[0][2]["body"])

    def test_historical_validation_failure_causes_no_release_write(self):
        with mock.patch.object(pipeline, "historical_attachments", side_effect=pipeline.GuardError("historical hash mismatch")), self.assertRaises(pipeline.GuardError):
            pipeline.publish(self.api, self.policy, self.archive)
        self.assertFalse(self.api.mutations)
        self.assertFalse(self.api.uploads)

    def test_attachment_name_collision_causes_no_release_write(self):
        with mock.patch.object(pipeline, "historical_attachments", return_value=({"BUILD.json": b"collision"}, "")), self.assertRaises(pipeline.GuardError):
            pipeline.publish(self.api, self.policy, self.archive)
        self.assertFalse(self.api.mutations)

    def test_uncertain_historical_upload_stops_without_retry(self):
        all_names = sorted(set(self.files) | set(self.history))
        stop = all_names.index(sorted(self.history)[0]) + 1
        self.api.fail_upload_number = stop
        with mock.patch.object(pipeline, "historical_attachments", return_value=(self.history, "")), self.assertRaisesRegex(pipeline.GuardError, str(RELEASE_ID)):
            pipeline.publish(self.api, self.policy, self.archive)
        self.assertEqual(len(self.api.uploads), stop)
        self.assertEqual(len(self.api.mutations), 1)


if __name__ == "__main__":
    unittest.main()
