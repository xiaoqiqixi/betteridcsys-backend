"""Offline platform/pin validation; never downloads, extracts or runs MySQL."""
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("mysql_distribution_policy", HERE / "mysql-distribution.py")
POLICY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(POLICY)


class DistributionPolicyTests(unittest.TestCase):
    def manifest(self, version):
        return json.loads((HERE / POLICY.PLATFORMS[version][1]).read_text())

    def test_independent_reviewed_pins_match_both_exact_platforms(self):
        for version, mysql in (("22.04", "8.4.13"), ("24.04", "8.4.11")):
            with self.subTest(version=version):
                lines = POLICY.validate_manifest(self.manifest(version), version)
                self.assertEqual(lines[0], mysql)
                self.assertEqual(len(lines), 13)
                self.assertEqual(len(set(lines[1::3])), 4)
                self.assertTrue(all(name.endswith(f"_{mysql}-1ubuntu{version}_amd64.deb") for name in lines[1::3]))

    def test_only_two_ubuntu_lts_identities_are_supported(self):
        for version in ("22.04", "24.04"):
            self.assertEqual(POLICY.ubuntu_version(f'NAME="Ubuntu"\nID=ubuntu\nVERSION_ID="{version}"\n'), version)
        for text in ('ID=debian\nVERSION_ID="22.04"', 'ID=mint\nID_LIKE=ubuntu\nVERSION_ID=22.04',
                     'ID=ubuntu\nVERSION_ID=26.04', 'ID=ubuntu\nVERSION_ID=22.10',
                     'ID=ubuntu\nVERSION_ID=24.04\nVERSION_ID=22.04', 'ID=ubuntu\nVERSION_ID="22.04;cmd"', ''):
            with self.subTest(text=text), self.assertRaises(ValueError):
                POLICY.ubuntu_version(text)

    def test_cross_platform_pins_are_never_substituted(self):
        for host, other in (("22.04", "24.04"), ("24.04", "22.04")):
            with self.subTest(host=host), self.assertRaises(ValueError):
                POLICY.validate_manifest(self.manifest(other), host)

    def test_manifest_header_types_origin_and_exact_architecture_are_enforced(self):
        for version in POLICY.PLATFORMS:
            for field, value in (("schema", True), ("schema", 1), ("status", "verified"),
                                 ("version", "8.0.44"), ("version", 8.4), ("architecture", "arm64"),
                                 ("package_version", "8.4.13-1ubuntu26.04"), ("distribution", "ubuntu-26.04"),
                                 ("metadata_url", "https://mirror.example.invalid/Packages"),
                                 ("metadata_url", "http://repo.mysql.com/apt/ubuntu/dists/jammy/mysql-8.4-lts/binary-amd64/Packages")):
                with self.subTest(version=version, field=field, value=value):
                    manifest = self.manifest(version)
                    manifest[field] = value
                    with self.assertRaises(ValueError):
                        POLICY.validate_manifest(manifest, version)

    def test_all_four_unique_package_names_are_required(self):
        manifest = self.manifest("22.04")
        packages = manifest["packages"]
        for bad in (None, {}, packages[:-1], packages + [packages[0]],
                    [packages[0]] * 4, [packages[0], packages[1], packages[2], {}],
                    [packages[0], packages[1], packages[2], {"name": ["nested"]}]):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                POLICY.validate_manifest({**manifest, "packages": bad}, "22.04")

    def test_package_hash_and_size_are_exact_and_bounded(self):
        for field, values in (("sha256", [None, "", "0" * 64, "A" * 64, "a" * 63, "a" * 65]),
                              ("size", [None, True, 0, -1, "1", 1.5, 1024 ** 3 + 1])):
            for value in values:
                with self.subTest(field=field, value=value):
                    manifest = self.manifest("22.04")
                    manifest["packages"][-1][field] = value
                    with self.assertRaises(ValueError):
                        POLICY.validate_manifest(manifest, "22.04")

    def test_duplicate_json_fields_cannot_shadow_reviewed_pins(self):
        for raw in ('{"schema":2,"schema":2}', '{"packages":[{"sha256":"a","sha256":"b"}]}'):
            with self.assertRaisesRegex(ValueError, "duplicate manifest"):
                json.loads(raw, object_pairs_hook=POLICY.strict_pairs)

    def test_security_backport_capability_is_required_not_a_python_major_version(self):
        def extractall(self, path=".", *, filter=None):
            pass
        backport = SimpleNamespace(data_filter=lambda member, path: member,
                                   TarFile=SimpleNamespace(extractall=extractall))
        with patch.object(POLICY, "tarfile", backport):
            POLICY.require_data_filter()
        for missing in (SimpleNamespace(TarFile=backport.TarFile),
                        SimpleNamespace(data_filter=None, TarFile=backport.TarFile),
                        SimpleNamespace(data_filter=backport.data_filter,
                                        TarFile=SimpleNamespace(extractall=lambda self, path=".": None))):
            with patch.object(POLICY, "tarfile", missing), self.assertRaisesRegex(ValueError, "before any download"):
                POLICY.require_data_filter()

    def test_cli_rejects_distribution_overrides_before_any_action(self):
        result = subprocess.run([sys.executable, '-I', str(HERE / 'mysql-distribution.py'), '--manifest', '/tmp/foreign.json'],
                                capture_output=True, text=True, timeout=5)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('takes no arguments', result.stderr)

    def test_actual_native_wrapper_os_guards_accept_both_supported_versions_only(self):
        for script in ('accept-native.sh', 'core-native.sh', 'plugin-native.sh', 'payment-native.sh'):
            source = (HERE / script).read_text()
            guard = source.split("python3 - <<'PY'\n", 1)[1].split('\nPY\n', 1)[0]
            for distro, version in (("ubuntu", "22.04"), ("ubuntu", "24.04"), ("ubuntu", "26.04"), ("debian", "22.04")):
                with self.subTest(script=script, distro=distro, version=version), \
                        patch.object(Path, 'read_text', return_value=f'ID={distro}\nVERSION_ID="{version}"\n'):
                    if distro == 'ubuntu' and version in POLICY.PLATFORMS:
                        exec(compile(guard, script + '-os-guard', 'exec'), {})
                    else:
                        with self.assertRaises(SystemExit):
                            exec(compile(guard, script + '-os-guard', 'exec'), {})

    def test_download_verify_extract_order_and_filter_remain_mandatory(self):
        source = (HERE / 'mysql-native.sh').read_text()
        self.assertLess(source.index('mysql-distribution.py'), source.index('curl --fail'))
        self.assertLess(source.index('SHA-256 mismatch'), source.index('archive.extractall('))
        self.assertIn('archive.extractall(destination, members=members, filter="data")', source)
        self.assertNotIn('fully_trusted', source)
        self.assertNotIn('--allow-unauthenticated', (HERE / 'prepare-native-runner.sh').read_text())

    def test_real_preparation_platform_switch_selects_native_libraries_and_binary_package(self):
        source = (HERE / 'prepare-native-runner.sh').read_text()
        switch = re.search(r'^case \$VERSION_ID in\n.*?^esac$', source, re.M | re.S)
        self.assertIsNotNone(switch)
        command = 'set -euo pipefail\nfail() { exit 9; }\nVERSION_ID=$1\n' + switch.group()
        command += '\nprintf "%s\\n" "$nginx_package" "${platform_libraries[@]}"\n'
        for version, expected in (('22.04', ['nginx', 'libaio1', 'libssl3', 'libtirpc3']),
                                  ('24.04', ['nginx', 'libaio1t64', 'libssl3t64', 'libtirpc3t64']),
                                  ('26.04', None)):
            with self.subTest(version=version):
                result = subprocess.run(['bash', '-c', command, 'platform-only-test', version],
                                        text=True, capture_output=True, timeout=5)
                self.assertEqual(result.returncode, 0 if expected else 9)
                if expected:
                    self.assertEqual(result.stdout.splitlines(), expected)

    def test_jammy_nginx_pin_is_checked_before_package_extraction_without_lowering_tls(self):
        source = (HERE / 'prepare-native-runner.sh').read_text()
        self.assertIn("nginx_package_version='1.30.6-1~jammy'", source)
        self.assertIn('nginx_package_size=1224970', source)
        self.assertIn("nginx_package_sha256='ddafa6d5decef787c7942e6c4a9c62cf881d7ea598ec42b9997ecbf298e1bde3'", source)
        self.assertIn('https://nginx.org/packages/ubuntu/pool/nginx/n/nginx/$nginx_filename', source)
        self.assertLess(source.index('Pinned Jammy Nginx SHA256 mismatch'), source.index('dpkg-deb --field'))
        self.assertLess(source.index('Pinned Jammy Nginx SHA256 mismatch'), source.index('dpkg-deb --extract'))
        self.assertLess(source.index('Pinned Jammy Nginx version mismatch'), source.index('dpkg-deb --extract'))
        self.assertNotIn('--insecure', source)
        edge = (HERE / 'edge-observability.py').read_text()
        self.assertIn('version >= (1, 19, 4)', edge)
        self.assertIn('ssl_reject_handshake on;', (HERE.parent / 'nginx.native.conf').read_text())


if __name__ == '__main__':
    unittest.main()
