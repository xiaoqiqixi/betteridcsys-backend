#!/usr/bin/env python3
"""Offline public-download/preparer wiring contracts, not real native acceptance."""
import ast
import contextlib
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("public_core_prepare_contract", HERE / "public-core-prepare.py")
h = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(h)


class PublishedPreparationContract(unittest.TestCase):
    def setUp(self):
        self.controls = h.load_controls()
        self.manager = self.controls.manager
        self.updater = self.manager.updater
        self.source = "a" * 40
        names = ("aster-api-0.5.10-linux-amd64", "aster-plugin-host-0.5.10-linux-amd64",
                 "aster-core-0.5.10-linux-amd64-resources.tar.gz", "BUILD.json", "PROVENANCE.json", "SHA256SUMS")
        self.raw = {index: (name + "-MODEL-ONLY").encode() for index, name in enumerate(names, 1)}
        self.assets = [{"name": name, "id": index, "size": len(self.raw[index]), "sha256": hashlib.sha256(self.raw[index]).hexdigest()}
                       for index, name in enumerate(names, 1)]
        self.descriptor = {"source_sha": self.source, "assets": self.assets, "release_id": 42}
        self.result = {"status": "prepared", "version": h.VERSION, "source_sha": self.source,
                       "source_and_integrity_verified": True, "active_release_changed": False,
                       "service_action": "none", "database_action": "none", "program_executed": False}
        self.receipt = {**self.result, "receipt_kind": "local_operator_preparation_receipt", "release_id": 42,
                        "official_release_inventory": False, "run_id": 8, "run_attempt": 1, "assets": self.assets}
        self.metadata, self.public = mock.Mock(), mock.Mock()
        self.public.asset.side_effect = lambda asset_id, _limit: self.raw[asset_id]
        self.inventory = self.updater.asset_inventory(self.assets, "core", h.VERSION, "raw-binaries-v1")

    @contextlib.contextmanager
    def fixture(self, *, count=13, source=None, final_inventory=None):
        with tempfile.TemporaryDirectory() as directory, contextlib.ExitStack() as stack:
            work = Path(directory).resolve()
            def prepare(version, target, *, assets_directory, api):
                self.assertEqual(version, h.VERSION)
                self.assertIs(api, self.metadata)
                self.assertEqual(Path(assets_directory), work / "assets")
                self.assertEqual({p.name: p.read_bytes() for p in Path(assets_directory).iterdir()},
                                 {item["name"]: self.raw[item["id"]] for item in self.assets})
                migrations = Path(target) / "migrations"
                migrations.mkdir(parents=True)
                for index in range(1, count + 1):
                    (migrations / (str(index).zfill(3) + "_model.sql")).write_text("MODEL ONLY")
                return self.result
            self.preparer = stack.enter_context(mock.patch.object(self.manager, "prepare", side_effect=prepare))
            self.prepared_checker = stack.enter_context(mock.patch.object(self.controls, "prepared_release", side_effect=lambda *_: {
                "version": h.VERSION, "source_sha": self.source,
                "receipt_sha256": hashlib.sha256(json.dumps(self.receipt).encode()).hexdigest(),
                "files_sha256": {"model/" + str(i): "c" * 64 for i in range(8)},
                "migration_checksums": {str(i): "d" * 64 for i in range(1, count + 1)}}))
            stack.enter_context(mock.patch.object(self.manager, "release_descriptor", return_value=self.descriptor))
            self.clients = stack.enter_context(mock.patch.object(self.updater, "Official", side_effect=[self.metadata, self.public]))
            stack.enter_context(mock.patch.object(self.updater, "read_protected", side_effect=lambda *_: json.dumps(self.receipt).encode()))
            stack.enter_context(mock.patch.object(self.updater, "release_assets", return_value=self.inventory if final_inventory is None else final_inventory))
            yield work, lambda: h.verify(self.controls, work, source if source is not None else self.source, "PRIVATE_METADATA_TOKEN")

    def test_separate_anonymous_assets_and_real_preparer_receipt_are_required(self):
        with self.fixture() as (work, verify):
            result = verify()
            self.clients.assert_has_calls([mock.call("PRIVATE_METADATA_TOKEN"), mock.call()])
            self.assertEqual(self.public.asset.call_count, 6)
            self.metadata.asset.assert_not_called()
            self.preparer.assert_called_once()
            self.prepared_checker.assert_called_once_with(str(work / "release"))
            self.assertTrue(result["anonymous_asset_download"])
            self.assertTrue(result["production_prepare"])
            self.assertFalse(result["host_install"])
            self.assertFalse(result["program_executed"])
            self.assertFalse(result["independent_publisher_signature"])
            self.assertEqual(result["migration_files"], 13)
            self.assertNotIn("PRIVATE", json.dumps(result))
            self.assertNotIn(str(work), json.dumps(result))

    def test_invalid_or_different_source_stops_before_asset_download_and_prepare(self):
        for source in ("main", "a" * 39, "b" * 40, "a" * 40 + "\n", "$(PRIVATE)"):
            with self.subTest(source_length=len(source)), self.fixture(source=source) as (work, verify):
                with self.assertRaises(self.updater.Rejected):
                    verify()
                self.public.asset.assert_not_called()
                self.preparer.assert_not_called()
                self.assertFalse((work / "release").exists())
            self.public.reset_mock()

    def test_truncated_or_tampered_public_bytes_never_reach_preparation(self):
        for raw in (b"", b"PRIVATE_TAMPER", self.raw[1][:-1]):
            with self.subTest(size=len(raw)), self.fixture() as (work, verify):
                self.public.asset.side_effect = lambda *_: raw
                with self.assertRaisesRegex(self.updater.Rejected, "PUBLIC_ASSET_BYTES_REJECTED"):
                    verify()
                self.preparer.assert_not_called()
                self.assertFalse((work / "release").exists())

    def test_missing_or_duplicate_assets_remain_rejected(self):
        original = copy.deepcopy(self.assets)
        for assets in (original[:-1], original[:-1] + [original[0]]):
            self.descriptor["assets"] = assets
            with self.subTest(count=len(assets)), self.fixture() as (_, verify):
                with self.assertRaises(self.updater.Rejected):
                    verify()
                self.preparer.assert_not_called()

    def test_preparer_cannot_claim_execution_activation_or_unverified_source(self):
        for key, value in (("status", "started"), ("source_sha", "b" * 40), ("version", "0.5.9"),
                           ("source_and_integrity_verified", False), ("program_executed", True),
                           ("active_release_changed", True), ("service_action", "start"), ("database_action", "migrate")):
            original = copy.deepcopy(self.result)
            self.result[key] = value
            with self.subTest(key=key), self.fixture() as (_, verify):
                with self.assertRaisesRegex(self.updater.Rejected, "REAL_PREPARE_RESULT_REJECTED"):
                    verify()
            self.result = original

    def test_preparation_errors_propagate_without_replacement_receipts(self):
        with self.fixture() as (_, verify):
            self.preparer.side_effect = self.updater.Rejected("MODEL_REAL_PREPARER_DENIAL")
            with self.assertRaisesRegex(self.updater.Rejected, "MODEL_REAL_PREPARER_DENIAL"):
                verify()

    def test_full_readonly_prepared_inventory_check_cannot_be_skipped_on_failure(self):
        with self.fixture() as (_, verify):
            self.prepared_checker.side_effect = self.updater.Rejected("MODEL_PREPARED_BYTES_CHANGED")
            with self.assertRaisesRegex(self.updater.Rejected, "MODEL_PREPARED_BYTES_CHANGED"):
                verify()

    def test_changed_or_not_actually_prepared_receipt_is_rejected(self):
        for key, value in (("source_sha", "b" * 40), ("release_id", 99), ("official_release_inventory", True),
                           ("status", "started"), ("version", "0.5.9"), ("run_id", 0), ("run_attempt", False)):
            original = copy.deepcopy(self.receipt)
            self.receipt[key] = value
            with self.subTest(key=key), self.fixture() as (_, verify):
                with self.assertRaisesRegex(self.updater.Rejected, "LOCAL_RECEIPT_REJECTED"):
                    verify()
            self.receipt = original

    def test_release_changes_after_preparation_are_still_rejected(self):
        with self.fixture(final_inventory={}) as (_, verify):
            with self.assertRaisesRegex(self.updater.Rejected, "PUBLIC_RELEASE_CHANGED"):
                verify()

    def test_schema13_contract_does_not_silently_accept_missing_or_added_sql(self):
        for count in (0, 12, 14):
            with self.subTest(count=count), self.fixture(count=count) as (_, verify):
                with self.assertRaisesRegex(self.updater.Rejected, "MIGRATION_INVENTORY_REJECTED"):
                    verify()

    def test_unsupported_platform_stops_before_loading_controls_or_reading_credentials(self):
        with mock.patch.object(h.platform, "system", return_value="Darwin"), mock.patch.object(h, "load_controls") as loader, \
                mock.patch.object(h.sys, "stdin") as stdin, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(h.main(["--source-sha", self.source]), 1)
        loader.assert_not_called()
        stdin.buffer.read.assert_not_called()
        self.assertEqual(json.loads(output.getvalue()), {"schema": 1, "passed": False, "code": "PUBLIC_CORE_ACCEPTANCE_FAILED"})

    def test_bad_stdin_token_is_never_printed_or_used_for_network(self):
        for raw in (b"PRIVATE_TOKEN\n", b"\xffPRIVATE", b"x" * 4097):
            with self.subTest(size=len(raw)), mock.patch.object(h.platform, "system", return_value="Linux"), \
                    mock.patch.object(h.platform, "machine", return_value="x86_64"), mock.patch.object(h.os, "geteuid", return_value=0), \
                    mock.patch.object(h.Path, "read_text", return_value='ID=ubuntu\nVERSION_ID="22.04"\n'), \
                    mock.patch.object(h, "load_controls", return_value=self.controls), \
                    mock.patch.object(h.sys, "stdin", types.SimpleNamespace(buffer=io.BytesIO(raw))), \
                    mock.patch.object(h.tempfile, "TemporaryDirectory") as temporary, \
                    contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(h.main(["--source-sha", self.source]), 1)
                temporary.assert_not_called()
                self.assertNotIn("PRIVATE", output.getvalue())

    def test_workflow_is_main_only_dual_lts_and_has_no_write_or_install_permissions(self):
        workflow = (HERE.parents[2] / ".github/workflows/core-public-prepare.yml").read_text()
        self.assertIn("github.ref == 'refs/heads/main'", workflow)
        self.assertIn("os: [ubuntu-22.04, ubuntu-24.04]", workflow)
        self.assertNotIn("contents: write", workflow)
        self.assertNotIn("id-token: write", workflow)
        self.assertIn("/usr/bin/env -i", workflow)
        self.assertIn("persist-credentials: false", workflow)
        self.assertNotIn("--allow-", workflow)
        for name in ("public-core-prepare.py", "test_public_core_prepare.py"):
            ast.parse((HERE / name).read_text(), feature_version=(3, 10))


if __name__ == "__main__":
    unittest.main(verbosity=2)
