#!/usr/bin/env python3
"""Keep copied README contracts useful after publication; no network reads."""
from pathlib import Path
import re
from urllib.parse import unquote, urlsplit
import unittest

ROOT = Path(__file__).resolve().parents[3]
ENTRY_DOCUMENTS = (
    "README.md", "docs/README.md", "vps-billing/README.md",
    "vps-billing/docs/GETTING_STARTED.md", "aster-cloud/README.md",
    "aster-cloud/docs/GETTING_STARTED.md",
)


def local_link_targets(text):
    # Entry documents use inline Markdown links. Code examples are not links.
    prose = re.sub(r"(?ms)^```.*?^```[^\n]*$", "", text)
    for target in re.findall(r"\[[^\]\n]+\]\(([^\s)]+)\)", prose):
        parsed = urlsplit(target)
        if not parsed.scheme and not parsed.netloc and parsed.path:
            yield unquote(parsed.path)


def current_cloud_schema():
    versions = sorted(int(path.name.split("_")[0]) for path in
                      (ROOT / "aster-cloud/migrations").glob("[0-9][0-9][0-9]_*.sql"))
    if versions != list(range(1, len(versions) + 1)):
        raise AssertionError("Cloud migration sequence is not contiguous")
    return versions[-1]


def assert_timeless_readme(text):
    # These observed declarations were true before a tag, then remained false
    # forever in the immutable resource README. Conditions and dated evidence
    # are fine; this guard does not require a not-yet-published release to exist.
    for phrase in ("该版本尚未正式发布", "当前已发布版本仍为", "候选源码（未发布）", "候选路径；**该版本",
                   "**候选未发布", "当前仅为未发布候选"):
        if phrase in text:
            raise AssertionError("Copied README freezes a prepublication state: " + phrase)


class ReleaseReadmeContract(unittest.TestCase):
    def test_root_readme_does_not_freeze_a_prepublication_state(self):
        assert_timeless_readme((ROOT / "README.md").read_text())

    def test_runtime_entry_documents_use_conditions_not_frozen_candidate_status(self):
        for name in ("README.md", "vps-billing/README.md", "vps-billing/docs/INSTALL.md",
                     "vps-billing/docs/UPDATES.md", "vps-billing/docs/UPGRADE.md", "vps-billing/docs/BAOTA.md"):
            with self.subTest(path=name):
                assert_timeless_readme((ROOT / name).read_text())

    def test_guard_rejects_the_observed_old_declarations(self):
        for text in ("该版本尚未正式发布，不是现在可执行的下载承诺", "当前已发布版本仍为 `0.5.9`",
                     "本页对应 Core `0.5.10` 候选源码（未发布）", "> **候选未发布。**", "当前仅为未发布候选"):
            with self.subTest(text=text), self.assertRaises(AssertionError):
                assert_timeless_readme(text)

    def test_conditional_contracts_and_dated_evidence_are_not_publication_claims(self):
        assert_timeless_readme("以正常Release、完整资产及来源校验为安装前提；不存在或验证失败时不得安装。\n"
                               "2026-10-27曾复验0.5.10；后续源码不改变旧资产。")

    def test_root_and_project_readme_version_contract_matches_core_source(self):
        protocol = (ROOT / "vps-billing/backend/pkg/pluginapi/protocol.go").read_text()
        versions = re.findall(r'(?m)^\s*CoreVersion\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)"', protocol)
        self.assertEqual(len(versions), 1)
        version = versions[0]
        self.assertIn("Core `" + version + "`", (ROOT / "README.md").read_text())
        self.assertIn("Core **" + version + "**", (ROOT / "vps-billing/README.md").read_text())

    def test_task_guides_and_documentation_hub_are_reachable(self):
        expected = {
            "README.md": "docs/README.md",
            "docs/README.md": "../vps-billing/docs/GETTING_STARTED.md",
            "vps-billing/README.md": "docs/GETTING_STARTED.md",
            "aster-cloud/README.md": "docs/GETTING_STARTED.md",
        }
        for name, target in expected.items():
            with self.subTest(path=name):
                self.assertIn(target, list(local_link_targets((ROOT / name).read_text())))
        for name in ENTRY_DOCUMENTS:
            with self.subTest(path=name):
                self.assertTrue((ROOT / name).is_file())

    def test_entry_document_local_links_resolve_inside_repository(self):
        for name in ENTRY_DOCUMENTS:
            path = ROOT / name
            for target in local_link_targets(path.read_text()):
                with self.subTest(path=name, target=target):
                    resolved = (path.parent / target).resolve()
                    self.assertIn(ROOT, resolved.parents)
                    self.assertTrue(resolved.is_file(), "Missing documentation target: " + target)

    def test_local_link_reader_ignores_web_and_fenced_examples(self):
        text = ("[Guide](docs/GETTING_STARTED.md#login) [Web](https://example.invalid/doc)\n"
                "[Chinese](docs/%E4%B8%AD%E6%96%87.md)\n```sh\n[not-a-link](missing.md)\n```\n")
        self.assertEqual(list(local_link_targets(text)), ["docs/GETTING_STARTED.md", "docs/中文.md"])

    def test_operational_cloud_schema_matches_migrations(self):
        schema = str(current_cloud_schema())
        self.assertIn("主控 MySQL：schema " + schema, (ROOT / "aster-cloud/README.md").read_text())
        self.assertIn("当前 schema **" + schema + "**", (ROOT / "aster-cloud/docs/ARCHITECTURE.md").read_text())
        self.assertIn("当前主控数据库为 schema **" + schema + "**",
                      (ROOT / "aster-cloud/docs/KVM_ENHANCED_PARITY.md").read_text())
        operations = (ROOT / "aster-cloud/deploy/controller-OPERATIONS.md").read_text()
        self.assertIn("## 当前 schema " + schema, operations)
        self.assertIn("012_identity_operations.sql", operations)

    def test_active_resource_docs_do_not_call_core_unpublished(self):
        paths = ("vps-billing/docs/PLUGINS.md", "vps-billing/docs/RESOURCE_PLUGINS.md",
                 "vps-billing/docs/PRODUCT_CONFIGURATION.md", "aster-cloud/README.md",
                 "aster-cloud/docs/ARCHITECTURE.md", "aster-cloud/docs/KVM_ENHANCED_PARITY.md",
                 "aster-cloud/plugins/billingconnector/README.md")
        stale = ("Core 0.5.10 未发布候选", "Core **0.5.10** 当前只是未发布候选",
                 "最低Core 0.5.10目前为未发布候选", "最低财务 Core 为 **0.5.10**（当前候选，未发布）",
                 "最低Core **0.5.10**，当前未发布候选", "当前源码为未发布 0.5.10 候选",
                 "以下安装流程针对未发布候选")
        for name in paths:
            text = (ROOT / name).read_text()
            for phrase in stale:
                with self.subTest(path=name, phrase=phrase):
                    self.assertNotIn(phrase, text)

    def test_first_use_paths_and_bootstrap_fields_match_source(self):
        finance = (ROOT / "vps-billing/docs/GETTING_STARTED.md").read_text()
        router = (ROOT / "vps-billing/frontend/src/router.ts").read_text()
        for path in ("/home", "/catalog", "/admin/catalog", "/admin/settings"):
            with self.subTest(path=path):
                self.assertIn('"' + path + '"', router)
                self.assertIn("#" + path, finance)
        installation = (ROOT / "vps-billing/docs/INSTALL.md").read_text()
        for field in ("bootstrap_email", "bootstrap_password_file"):
            self.assertIn(field, installation)
            self.assertIn(field, finance)
        cloud = (ROOT / "aster-cloud/docs/GETTING_STARTED.md").read_text()
        cloud_router = (ROOT / "aster-cloud/frontend/src/main.ts").read_text()
        for path in ("/instances", "/nodes", "/operations", "/audit", "/users", "/networks/overlays"):
            with self.subTest(path=path):
                self.assertIn('"' + path + '"', cloud_router)
                self.assertIn("#" + path, cloud)


if __name__ == "__main__":
    unittest.main(verbosity=2)
