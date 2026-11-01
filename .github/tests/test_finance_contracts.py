"""Repository documentation and PR CI entry-point regressions (stdlib only)."""
from pathlib import Path
import json
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]

class FinanceContracts(unittest.TestCase):
    def test_migration_docs_identify_the_current_paired_source_and_artifact(self):
        pin = json.loads((ROOT / 'FRONTEND_PAIR.json').read_text())
        for name in ('REPOSITORY_MIGRATION.md', 'RELEASE_TOOLING.md'):
            with self.subTest(document=name):
                document = (ROOT / 'docs/development' / name).read_text()
                self.assertIn('`' + pin['source_sha'] + '`', document)
                self.assertIn('`' + str(pin['artifact_id']) + '`', document)
                self.assertIn('Core `' + pin['core_version'] + '`', document)

    def test_pull_requests_run_without_repository_secrets(self):
        workflow = (ROOT / '.github/workflows/ci.yml').read_text()
        self.assertRegex(workflow, r'(?m)^  pull_request:\s*$')
        self.assertNotIn('pull_request_target:', workflow)
        self.assertIn('github.event.pull_request.number || github.ref', workflow)
        self.assertIn("cancel-in-progress: ${{ github.event_name == 'pull_request' }}", workflow)
        # All existing jobs use public sources, synthetic credentials and the
        # automatic read-only github.token, also available to fork PRs.
        self.assertNotIn('secrets.', workflow)
        self.assertIn('contents: read', workflow)
        self.assertIn('persist-credentials: false', workflow)

    def test_docs_track_current_core_and_owned_sources(self):
        protocol = (ROOT / 'vps-billing/backend/pkg/pluginapi/protocol.go').read_text()
        version = re.search(r'CoreVersion\s*=\s*"([^"]+)"', protocol).group(1)
        readme = (ROOT / 'vps-billing/backend/README.md').read_text()
        contract = (ROOT / 'vps-billing/docs/CONTRACT.md').read_text()
        tests = (ROOT / 'vps-billing/tests/README.md').read_text()
        self.assertIn('当前代码 Core 为 ' + version, readme)
        self.assertIn('Core ' + version, contract.splitlines()[0])
        self.assertNotIn('不提供套餐规格创建/修改', readme)
        self.assertNotIn('没有公开注册、密码找回、自助改密、MFA、角色修改或完整账号管理 API', readme)
        self.assertIn('/api/v1/admin/customer-accounts', readme)
        self.assertIn('/api/v1/admin/catalog', readme)
        self.assertIn('betteridcsys-frontend', tests)
        self.assertNotIn('前端检查从 `frontend/` 执行', tests)

if __name__ == '__main__':
    unittest.main()
