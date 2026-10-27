#!/usr/bin/env python3
"""Regression fixtures for lazy Go module graph inventory (no network/build)."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("release_manifest", Path(__file__).with_name("release-manifest.py"))
manifest = importlib.util.module_from_spec(spec)
spec.loader.exec_module(manifest)


class GoDependencyNoticesTest(unittest.TestCase):
    def test_graph_only_module_without_directory_is_explicit(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "notices"
            item = manifest.go_dependency_notices({"Path": "example.org/unused", "Version": "v1.0.0"}, target, set())
            self.assertEqual(item, {"linked_in_binary": False, "source_status": "module_graph_only", "notice_files": []})
            self.assertFalse(target.exists())

    def test_unlinked_graph_metadata_and_notices_ignore_cache_contents(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp) / "source"
            source.mkdir()
            (source / "LICENSE").write_text("unlinked fixture license\n")
            cold = {"Path": "example.org/unused", "Version": "v1.0.0"}
            warm = dict(cold, Dir=str(source), Sum="cache-dependent-sum", GoVersion="1.24")
            cold_target = Path(tmp) / "cold"
            warm_target = Path(tmp) / "warm"
            self.assertEqual(manifest.go_dependency_metadata(cold, set()), manifest.go_dependency_metadata(warm, set()))
            self.assertEqual(manifest.go_dependency_notices(cold, cold_target, set()), manifest.go_dependency_notices(warm, warm_target, set()))
            self.assertFalse(cold_target.exists())
            self.assertFalse(warm_target.exists())

    def test_linked_module_missing_source_fails_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaisesRegex(SystemExit, "Linked Go dependency source unavailable"):
                manifest.go_dependency_notices({"Path": "example.org/linked", "Version": "v1.0.0"}, Path(tmp), {"example.org/linked"})

    def test_available_linked_module_copies_notice_without_local_paths(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp) / "source"
            source.mkdir()
            (source / "LICENSE").write_text("test fixture license\n")
            target = Path(tmp) / "notices"
            item = manifest.go_dependency_notices({"Path": "example.org/linked", "Version": "v1.0.0", "Dir": str(source)}, target, {"example.org/linked"})
            self.assertEqual(item, {"linked_in_binary": True, "source_status": "source_available", "notice_files": ["LICENSE"]})
            self.assertEqual((target / "LICENSE").read_text(), "test fixture license\n")
            self.assertNotIn(str(source), str(item))


class SourceVersionTest(unittest.TestCase):
    def fixture(self, core="0.4.0", smtp="1.0.0"):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        protocol = root / "vps-billing/backend/pkg/pluginapi/protocol.go"
        protocol.parent.mkdir(parents=True)
        protocol.write_text('package pluginapi\nconst CoreVersion = "' + core + '"\n')
        version = root / "vps-billing/backend/plugins/smtp/VERSION"
        version.parent.mkdir(parents=True)
        version.write_text(smtp + "\n")
        return root

    def test_exact_independent_source_versions_allowed(self):
        root = self.fixture()
        self.assertEqual(manifest.selected_versions(root), {"core": "0.4.0", "smtp": "1.0.0"})
        manifest.verify_version(root, "core", "0.4.0")
        manifest.verify_version(root, "smtp", "1.0.0")

    def test_current_checkout_core_and_smtp_are_exact_independent_release_versions(self):
        root = Path(__file__).resolve().parents[3]
        self.assertEqual(manifest.selected_versions(root), {"core": "0.5.10", "smtp": "1.1.0"})
        manifest.verify_version(root, "core", "0.5.10")
        manifest.verify_version(root, "smtp", "1.1.0")
        for kind, version in (("core", "0.5.6"), ("core", "v0.5.7"), ("smtp", "1.1.1")):
            with self.subTest(kind=kind, version=version), self.assertRaises(SystemExit):
                manifest.verify_version(root, kind, version)

    def test_tag_cannot_relabel_other_source_or_component(self):
        root = self.fixture()
        for kind, version in (("core", "1.0.0"), ("smtp", "0.4.0"), ("smtp", "1.0.1"), ("other", "0.4.0")):
            with self.subTest(kind=kind, version=version), self.assertRaises(SystemExit):
                manifest.verify_version(root, kind, version)

    def test_core_declaration_is_distinct_from_payment_minimum_and_comments(self):
        root = self.fixture()
        protocol = root / "vps-billing/backend/pkg/pluginapi/protocol.go"
        for source in ('package pluginapi\nconst (\n PaymentMinCoreVersion = "0.5.3"\n CoreVersion = "0.4.0"\n)\n',
                       'package pluginapi\n// CoreVersion = "9.9.9"\nconst CoreVersion = "0.4.0"\n'):
            with self.subTest(source=source):
                protocol.write_text(source)
                self.assertEqual(manifest.selected_versions(root), {"core": "0.4.0", "smtp": "1.0.0"})

    def test_missing_or_duplicate_core_declaration_cannot_be_replaced_by_similar_name(self):
        root = self.fixture()
        protocol = root / "vps-billing/backend/pkg/pluginapi/protocol.go"
        for source in ('package pluginapi\nconst PaymentMinCoreVersion = "0.4.0"\n',
                       'package pluginapi\n// CoreVersion = "0.4.0"\n',
                       'package pluginapi\nconst CoreVersion = "0.4.0"\nconst CoreVersion = "0.4.0"\n'):
            with self.subTest(source=source):
                protocol.write_text(source)
                with self.assertRaises(SystemExit):
                    manifest.selected_versions(root)

    def test_invalid_or_missing_source_version_fails_closed(self):
        for version in ("01.0.0", "1.0", "1.0.0-rc.1", "1.0.0\nextra"):
            with self.subTest(version=version), self.assertRaises(SystemExit):
                manifest.selected_versions(self.fixture(smtp=version))

    def test_embedded_original_license_text_has_exact_checksum(self):
        root = self.fixture()
        path = root / "licenses/go-runtime/LICENSE"
        path.parent.mkdir(parents=True)
        original = "原始许可证文字\n"
        path.write_text(original)
        items = manifest.embedded_notices(root)
        self.assertEqual(items, [{"path": "go-runtime/LICENSE", "sha256": manifest.hashlib.sha256(original.encode()).hexdigest(), "text": original}])


if __name__ == "__main__":
    unittest.main()
