#!/usr/bin/env python3
"""Offline lifecycle tests with fake executables; not real MySQL acceptance."""
import hashlib
import io
import json
import os
from pathlib import Path
import signal
import shutil
import subprocess
import tempfile
import tarfile
import textwrap
import time
import unittest

SCRIPT = Path(__file__).with_name("mysql-native.sh").resolve()
POLICY = SCRIPT.with_name("mysql-distribution.py")
# The behavioral fake lifecycle follows its real Ubuntu host; it must not make
# a Jammy executable report the Noble pin or skip one platform's validation.
OS_FIELDS = dict(line.split("=", 1) for line in (Path("/etc/os-release").read_text().splitlines() if Path("/etc/os-release").exists() else []) if "=" in line)
MANIFEST = SCRIPT.with_name("mysql-distribution-ubuntu22.json" if OS_FIELDS.get("VERSION_ID", "").strip('"') == "22.04" else "mysql-distribution.json")


def executable(path, source):
    path.write_text(textwrap.dedent(source), encoding="utf-8")
    path.chmod(0o700)


class NativeMySQLFixtureTest(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory(prefix="aster-native-test-")
        self.addCleanup(self.scratch.cleanup)
        self.root = Path(self.scratch.name)
        self.controller = self.root / "controller with spaces"
        self.controller.mkdir()
        self.script = self.controller / SCRIPT.name
        self.script.write_bytes(SCRIPT.read_bytes())
        (self.controller / POLICY.name).write_bytes(POLICY.read_bytes())
        self.runner = self.root / "runner"
        self.runner.mkdir()
        self.fake_bin = self.root / "tools"
        self.fake_bin.mkdir()
        executable(self.fake_bin / "curl", '''\
            #!/usr/bin/env python3
            import os
            from pathlib import Path
            Path(os.environ['CI_FIXTURE_DOWNLOAD']).write_text('attempted')
            raise SystemExit(22)
        ''')
        self.real_timeout = shutil.which("timeout")
        self.assertIsNotNone(self.real_timeout)
        self.real_dpkg = shutil.which("dpkg-deb")
        self.assertIsNotNone(self.real_dpkg)
        executable(self.fake_bin / "dpkg-deb", '''\
            #!/usr/bin/env python3
            import os
            from pathlib import Path
            import sys
            with open(os.environ['CI_FIXTURE_DPKG_EVENTS'], 'a') as output:
                output.write(sys.argv[1] + '\\n')
            os.execv(os.environ['CI_FIXTURE_REAL_DPKG'], ['dpkg-deb'] + sys.argv[1:])
        ''')
        executable(self.fake_bin / "timeout", '''\
            #!/usr/bin/env python3
            import os
            import sys
            args = sys.argv[1:]
            assert args[:2] in (['--signal=TERM', '--kill-after=1s'], ['--signal=TERM', '--kill-after=5s'])
            if '--initialize-insecure' in args and os.environ.get('CI_FIXTURE_INIT_HANG'):
                assert args[2] == '120s'
                args[2] = '0.2s'
            os.execv(os.environ['CI_FIXTURE_REAL_TIMEOUT'], ['timeout'] + args)
        ''')
        self.manifest = json.loads(MANIFEST.read_text())
        self.manifest["verification_note"] = "TEST FIXTURE ONLY: fake executables, not Oracle packages"
        self.package_dir = self.root / "offline packages"
        self.package_dir.mkdir()
        self.package_sources = {}
        for package in self.manifest["packages"]:
            source = self.root / package["name"]
            (source / "DEBIAN").mkdir(parents=True)
            (source / "DEBIAN").chmod(0o755)
            (source / "DEBIAN/control").write_text(
                f"Package: {package['name']}\nVersion: {self.manifest['package_version']}\n"
                "Architecture: amd64\nMaintainer: Test <fixture@example.invalid>\nDescription: Offline fake CI fixture\n"
            )
            # A maintainer script must never execute during plain data extraction.
            executable(source / "DEBIAN/postinst", "#!/bin/sh\nexit 99\n")
            (source / "DEBIAN/postinst").chmod(0o755)
            self.package_sources[package["name"]] = source
        server = self.package_sources["mysql-community-server-core"] / "usr"
        (server / "sbin").mkdir(parents=True)
        (server / "share/mysql-8.4/english").mkdir(parents=True)
        (server / "share/mysql-8.4/english/errmsg.sys").write_text("fake message fixture")
        client = self.package_sources["mysql-community-client-core"] / "usr"
        (client / "bin").mkdir(parents=True)
        (self.package_sources["mysql-community-client-plugins"] / "usr/lib/mysql/plugin").mkdir(parents=True)
        dump = self.package_sources["mysql-community-client"] / "usr/bin/mysqldump"
        dump.parent.mkdir(parents=True)
        executable(dump, "#!/bin/sh\nexit 0\n")
        executable(server / "sbin/mysqld", '''\
            #!/usr/bin/env python3
            import os
            from pathlib import Path
            import signal
            import sys
            import time
            assert sys.argv[1] == '--no-defaults'
            assert 'MYSQL_PWD' not in os.environ
            assert 'MYSQL_TEST_LOGIN_FILE' not in os.environ
            assert 'ASTER_MYSQL_TEST_DSN' not in os.environ
            with open(os.environ['CI_FIXTURE_EVENTS'], 'a') as output:
                output.write('mysqld ' + ('version' if '--version' in sys.argv else 'initialize' if '--initialize-insecure' in sys.argv else 'serve') + '\\n')
            if '--version' in sys.argv:
                print('mysqld Ver ' + os.environ.get('CI_FIXTURE_BINARY_VERSION', '8.4.11') + ' for Linux on x86_64')
                raise SystemExit(0)
            if '--initialize-insecure' in sys.argv:
                if os.environ.get('CI_FIXTURE_INIT_HANG'):
                    Path(os.environ['CI_FIXTURE_INIT_PID']).write_text(str(os.getpid()))
                    time.sleep(30)
                if os.environ.get('CI_FIXTURE_INIT_FAIL'):
                    print('[ERROR] [MY-999999] fixture-private-text=inherited-must-not-escape')
                raise SystemExit(2 if os.environ.get('CI_FIXTURE_INIT_FAIL') else 0)
            assert '--bind-address=127.0.0.1' in sys.argv
            assert '--skip-networking' in sys.argv
            assert '--mysqlx=0' in sys.argv
            assert '--skip-name-resolve' in sys.argv
            init = Path(next(arg.split('=', 1)[1] for arg in sys.argv if arg.startswith('--init-file=')))
            assert "ALTER USER 'root'@'localhost' ACCOUNT LOCK;" in init.read_text()
            assert init.stat().st_mode & 0o777 == 0o600
            Path(os.environ['CI_FIXTURE_MYSQL_PID']).write_text(str(os.getpid()))
            if os.environ.get('CI_FIXTURE_START_FAIL'):
                raise SystemExit(2)
            def stop(*_):
                raise SystemExit(0)
            signal.signal(signal.SIGTERM, stop)
            while True:
                time.sleep(0.05)
        ''')
        executable(client / "bin/mysql", '''\
            #!/usr/bin/env python3
            import os
            from pathlib import Path
            import sys
            import time
            assert sys.argv[1:3] == ['--no-defaults', '--no-login-paths']
            assert '--protocol=SOCKET' in sys.argv and '--user=aster_ci' in sys.argv
            assert not any(arg.startswith(('--host=', '--port=', '--ssl-mode=')) for arg in sys.argv)
            socket_path = Path(next(arg.split('=', 1)[1] for arg in sys.argv if arg.startswith('--socket=')))
            assert socket_path.name == 'mysql.sock' and socket_path.parent.parent == Path(os.environ['RUNNER_TEMP'])
            assert os.environ['MYSQL_PWD'] == 'AsterCiFixtureOnly2026'
            assert 'MYSQL_TEST_LOGIN_FILE' not in os.environ
            if not Path(os.environ['CI_FIXTURE_MYSQL_PID']).exists():
                raise SystemExit(1)
            if os.environ.get('CI_FIXTURE_QUERY_HANG_ONCE'):
                marker = Path(os.environ['CI_FIXTURE_QUERY_PID'])
                if not marker.exists():
                    marker.write_text(str(os.getpid()))
                    time.sleep(30)
            print(os.environ.get('CI_FIXTURE_SERVER_VERSION', '8.4.11'))
        ''')
        for package in self.manifest["packages"]:
            self.build_package(package["name"])
        self.write_manifest()
        self.child = self.root / "child.py"
        executable(self.child, '''\
            #!/usr/bin/env python3
            import os
            from pathlib import Path
            import re
            import sys
            import time
            import subprocess
            expected_dsn = 'aster_ci:AsterCiFixtureOnly2026@unix(' + os.environ['ASTER_NATIVE_MYSQL_SOCKET'] + ')/aster_ci_test?parseTime=true'
            assert os.environ['ASTER_MYSQL_TEST_DSN'] == expected_dsn
            assert 'MYSQL_PWD' not in os.environ
            assert 'MYSQL_TEST_LOGIN_FILE' not in os.environ
            fixture = Path(os.environ['ASTER_NATIVE_MYSQL_FIXTURE_ROOT'])
            verified = Path(os.environ['ASTER_NATIVE_MYSQL_ROOT'])
            assert verified == fixture / 'distribution/usr'
            assert (verified / 'bin/mysql').is_file() and (verified / 'bin/mysqldump').is_file()
            assert (verified / 'sbin/mysqld').is_file()
            config = Path(os.environ['ASTER_NATIVE_MYSQL_ADMIN_CNF'])
            assert config.parent == fixture and config.stat().st_mode & 0o777 == 0o600
            assert config.read_text() == '[client]\\nuser=aster_ci\\npassword=AsterCiFixtureOnly2026\\n'
            assert Path(os.environ['ASTER_NATIVE_MYSQL_SOCKET']).parent == fixture
            assert os.environ['ASTER_NATIVE_MYSQL_PORT'].isdigit()
            Path(os.environ['CI_FIXTURE_CHILD_PID']).write_text(str(os.getpid()))
            if os.environ.get('CI_FIXTURE_BACKGROUND'):
                source = 'import time; time.sleep(30)'
                if os.environ.get('CI_FIXTURE_BACKGROUND') == 'ignore-term':
                    source = 'import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(30)'
                descendant = subprocess.Popen([sys.executable, '-c', source])
                Path(os.environ['CI_FIXTURE_BACKGROUND_PID']).write_text(str(descendant.pid))
                time.sleep(0.1)
            while os.environ.get('CI_FIXTURE_CHILD_WAIT'):
                time.sleep(0.05)
            raise SystemExit(int(os.environ.get('CI_FIXTURE_CHILD_EXIT', '0')))
        ''')
        self.env = dict(os.environ)
        self.env.update({
            "PATH": str(self.fake_bin) + os.pathsep + os.environ["PATH"],
            "RUNNER_TEMP": str(self.runner),
            "CI_FIXTURE_EVENTS": str(self.root / "events"),
            "CI_FIXTURE_DOWNLOAD": str(self.root / "download"),
            "CI_FIXTURE_MYSQL_PID": str(self.root / "mysql.pid"),
            "CI_FIXTURE_CHILD_PID": str(self.root / "child.pid"),
            "CI_FIXTURE_BACKGROUND_PID": str(self.root / "background.pid"),
            "CI_FIXTURE_INIT_PID": str(self.root / "initialize.pid"),
            "CI_FIXTURE_QUERY_PID": str(self.root / "query.pid"),
            "CI_FIXTURE_REAL_TIMEOUT": self.real_timeout,
            "CI_FIXTURE_REAL_DPKG": self.real_dpkg,
            "CI_FIXTURE_DPKG_EVENTS": str(self.root / "dpkg-events"),
            "MYSQL_PWD": "inherited-must-not-escape",
            "MYSQL_TEST_LOGIN_FILE": "inherited-must-not-escape",
            "ASTER_MYSQL_TEST_DSN": "inherited-must-not-escape",
            "ASTER_NATIVE_COMMAND_STOP_SECONDS": "1",
            "CI_FIXTURE_BINARY_VERSION": self.manifest["version"],
            "CI_FIXTURE_SERVER_VERSION": self.manifest["version"],
        })

    def write_manifest(self):
        (self.controller / MANIFEST.name).write_text(json.dumps(self.manifest), encoding="utf-8")

    def build_package(self, name):
        package = next(item for item in self.manifest["packages"] if item["name"] == name)
        path = self.package_dir / f"{name}_{self.manifest['package_version']}_amd64.deb"
        result = subprocess.run([self.real_dpkg, "--build", "--root-owner-group", str(self.package_sources[name]), str(path)],
                                text=True, capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        package.update(sha256=hashlib.sha256(path.read_bytes()).hexdigest(), size=path.stat().st_size)

    def command(self, script=None, offline=True):
        command = ["bash", str(script or self.script)]
        if offline:
            command += ["--packages", str(self.package_dir)]
        return command + ["--", str(self.child)]

    def run_fixture(self, script=None, offline=True, **overrides):
        result = subprocess.run(self.command(script, offline), cwd=self.root,
                                env=dict(self.env, **overrides), text=True,
                                capture_output=True, timeout=20)
        self.assert_clean(result)
        return result

    def assert_clean(self, result):
        self.assertEqual(list(self.runner.iterdir()), [], "fixture left temporary state")
        self.assertNotIn("inherited-must-not-escape", result.stdout + result.stderr)
        self.assertNotIn("AsterCiFixtureOnly2026", result.stdout + result.stderr)
        for name in ("mysql.pid", "child.pid", "initialize.pid", "query.pid", "background.pid"):
            pid = self.root / name
            if pid.exists():
                process = Path("/proc") / pid.read_text() / "stat"
                try:
                    state = process.read_text().rsplit(")", 1)[1].split()[0]
                except FileNotFoundError:
                    continue
                self.assertEqual(state, "Z", f"owned {name} still running")

    def test_repository_pins_reject_fake_packages_before_extraction(self):
        result = self.run_fixture(script=SCRIPT)
        self.assertEqual(result.returncode, 1)
        self.assertIn("SHA-256 mismatch", result.stderr)
        self.assertFalse((self.root / "download").exists())
        self.assertFalse((self.root / "events").exists())
        self.assertFalse((self.root / "dpkg-events").exists())

    def test_verified_status_without_checksum_still_blocks(self):
        self.manifest["packages"][0]["sha256"] = None
        self.write_manifest()
        result = self.run_fixture(offline=False)
        self.assertEqual(result.returncode, 1)
        self.assertIn("SHA-256 is required", result.stderr)
        self.assertFalse((self.root / "download").exists())

    def test_download_refusal_stops_without_execution(self):
        result = self.run_fixture(offline=False)
        self.assertEqual(result.returncode, 22)
        self.assertTrue((self.root / "download").exists())
        self.assertFalse((self.root / "events").exists())

    def test_checksum_mismatch_stops_before_execution(self):
        # Even the last package must be verified before extracting the first.
        self.manifest["packages"][-1]["sha256"] = "a" * 64
        self.write_manifest()
        result = self.run_fixture()
        self.assertEqual(result.returncode, 1)
        self.assertIn("SHA-256 mismatch", result.stderr)
        self.assertFalse((self.root / "events").exists())
        self.assertFalse((self.root / "dpkg-events").exists())

    def test_wrong_package_architecture_stops_before_extraction(self):
        name = self.manifest["packages"][0]["name"]
        control = self.package_sources[name] / "DEBIAN/control"
        control.write_text(control.read_text().replace("Architecture: amd64", "Architecture: arm64"))
        self.build_package(name)
        self.write_manifest()
        result = self.run_fixture()
        self.assertEqual(result.returncode, 1)
        self.assertIn("identity/version/architecture", result.stderr)
        self.assertFalse((self.root / "events").exists())
        self.assertNotIn("--fsys-tarfile", (self.root / "dpkg-events").read_text())

    def test_wrong_package_version_stops_before_extraction(self):
        name = self.manifest["packages"][0]["name"]
        control = self.package_sources[name] / "DEBIAN/control"
        control.write_text(control.read_text().replace(self.manifest["package_version"], "8.0.44-1ubuntu24.04"))
        self.build_package(name)
        self.write_manifest()
        result = self.run_fixture()
        self.assertEqual(result.returncode, 1)
        self.assertIn("identity/version/architecture", result.stderr)
        self.assertFalse((self.root / "events").exists())

    def test_escaping_package_symlink_is_rejected(self):
        name = self.manifest["packages"][0]["name"]
        outside = self.root / "must-not-be-written"
        (self.package_sources[name] / "usr/share/escaping-link").symlink_to(outside)
        self.build_package(name)
        self.write_manifest()
        result = self.run_fixture()
        self.assertEqual(result.returncode, 1)
        self.assertIn("AbsoluteLinkError", result.stderr)
        self.assertFalse(outside.exists())
        self.assertFalse((self.root / "events").exists())

    def test_unsafe_pinned_debian_data_members_cannot_escape_or_create_devices(self):
        # Alter only a fake, locally owned package's data member. Its control
        # identity and newly computed TEST pin remain valid, so failures reach
        # the production tar precheck/filter, not an earlier hash mismatch.
        package = self.manifest["packages"][0]
        path = self.package_dir / f"{package['name']}_{self.manifest['package_version']}_amd64.deb"
        original = path.read_bytes()
        self.assertEqual(original[:8], b"!<arch>\n")
        entries, offset = [], 8
        while offset < len(original):
            header = original[offset:offset + 60]
            self.assertEqual(header[58:60], b"`\n")
            size = int(header[48:58])
            entries.append((header[:16].decode().strip().rstrip('/'), original[offset + 60:offset + 60 + size]))
            offset += 60 + size + size % 2
        self.assertEqual(offset, len(original))
        for kind, name, link, rejected in (
                (tarfile.REGTYPE, '../outside', '', 'unsafe path'),
                (tarfile.REGTYPE, '/absolute-outside', '', 'unsafe path'),
                (tarfile.FIFOTYPE, 'usr/share/unsafe-fifo', '', 'SpecialFileError'),
                (tarfile.CHRTYPE, 'usr/share/unsafe-device', '', 'SpecialFileError'),
                (tarfile.SYMTYPE, 'usr/share/unsafe-link', '../../../../outside', 'LinkOutsideDestinationError'),
                (tarfile.LNKTYPE, 'usr/share/unsafe-hardlink', '../../../../outside', 'LinkOutsideDestinationError')):
            with self.subTest(kind=kind, name=name):
                data = io.BytesIO()
                with tarfile.open(fileobj=data, mode='w:') as archive:
                    member = tarfile.TarInfo(name)
                    member.type, member.linkname = kind, link
                    member.mode = 0o600
                    archive.addfile(member)
                changed = bytearray(b'!<arch>\n')
                for entry_name, raw in entries:
                    if entry_name.startswith('data.tar'):
                        entry_name, raw = 'data.tar', data.getvalue()
                    header = (f"{entry_name + '/':<16}{0:<12}{0:<6}{0:<6}{'100644':<8}{len(raw):<10}`\n").encode('ascii')
                    self.assertEqual(len(header), 60)
                    changed.extend(header + raw + (b'\n' if len(raw) % 2 else b''))
                path.write_bytes(changed)
                package['size'], package['sha256'] = len(changed), hashlib.sha256(changed).hexdigest()
                self.write_manifest()
                result = self.run_fixture()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(rejected, result.stderr)
                self.assertFalse((self.root / 'events').exists())
                self.assertFalse((self.root / 'outside').exists())

    def test_wrong_package_size_stops_before_extraction(self):
        self.manifest["packages"][-1]["size"] += 1
        self.write_manifest()
        result = self.run_fixture()
        self.assertEqual(result.returncode, 1)
        self.assertIn("package size", result.stderr)
        self.assertFalse((self.root / "dpkg-events").exists())

    def test_duplicate_package_manifest_is_rejected_before_download(self):
        self.manifest["packages"][-1] = self.manifest["packages"][0]
        self.write_manifest()
        result = self.run_fixture(offline=False)
        self.assertEqual(result.returncode, 1)
        self.assertIn("nonduplicated", result.stderr)
        self.assertFalse((self.root / "download").exists())

    def test_invalid_cleanup_grace_is_rejected_before_download(self):
        for value in ("0", "91", "-1", "1;true", "1.5"):
            with self.subTest(value=value):
                result = self.run_fixture(offline=False, ASTER_NATIVE_COMMAND_STOP_SECONDS=value)
                self.assertEqual(result.returncode, 1)
                self.assertIn("integer from 1 through 90", result.stderr)
                self.assertFalse((self.root / "download").exists())

    def test_offline_fifo_package_is_rejected_without_blocking(self):
        name = self.manifest["packages"][0]["name"]
        path = self.package_dir / f"{name}_{self.manifest['package_version']}_amd64.deb"
        path.unlink()
        os.mkfifo(path)
        result = self.run_fixture()
        self.assertEqual(result.returncode, 1)
        self.assertIn("readable regular file", result.stderr)
        self.assertFalse((self.root / "events").exists())
        self.assertFalse((self.root / "dpkg-events").exists())

    def test_wrong_binary_version_stops_before_initialization(self):
        result = self.run_fixture(CI_FIXTURE_BINARY_VERSION="8.0.44")
        self.assertEqual(result.returncode, 1)
        self.assertIn("version does not match", result.stderr)
        self.assertEqual((self.root / "events").read_text(), "mysqld version\n")

    def test_initialization_failure_stops_without_child(self):
        result = self.run_fixture(CI_FIXTURE_INIT_FAIL="1")
        self.assertEqual(result.returncode, 1)
        self.assertIn("initialization failed", result.stderr)
        self.assertIn("exit 2", result.stderr)
        self.assertIn("MySQL diagnostic codes: MY-999999", result.stderr)
        self.assertFalse((self.root / "mysql.pid").exists())
        self.assertFalse((self.root / "child.pid").exists())

    def test_startup_exit_stops_without_child(self):
        result = self.run_fixture(CI_FIXTURE_START_FAIL="1")
        self.assertEqual(result.returncode, 1)
        self.assertIn("owned native MySQL", result.stderr)
        self.assertFalse((self.root / "child.pid").exists())

    def test_hung_initialization_is_bounded_and_stopped(self):
        result = self.run_fixture(CI_FIXTURE_INIT_HANG="1")
        self.assertEqual(result.returncode, 1)
        self.assertIn("initialization failed", result.stderr)
        self.assertTrue((self.root / "initialize.pid").exists())
        self.assertFalse((self.root / "child.pid").exists())

    def test_hung_readiness_query_times_out_and_can_retry(self):
        result = self.run_fixture(CI_FIXTURE_QUERY_HANG_ONCE="1")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.root / "query.pid").exists())

    def test_wrong_running_version_stops_owned_server(self):
        result = self.run_fixture(CI_FIXTURE_SERVER_VERSION="8.0.44")
        self.assertEqual(result.returncode, 1)
        self.assertIn("Running MySQL version does not match", result.stderr)
        self.assertTrue((self.root / "mysql.pid").exists())
        self.assertFalse((self.root / "child.pid").exists())

    def test_success_scrubs_inherited_settings_and_stops_owned_server(self):
        result = self.run_fixture()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("fixture checks passed", result.stdout)
        self.assertTrue((self.root / "mysql.pid").exists())
        self.assertTrue((self.root / "child.pid").exists())

    def test_child_failure_preserves_exit_status_and_cleans_up(self):
        result = self.run_fixture(CI_FIXTURE_CHILD_EXIT="7")
        self.assertEqual(result.returncode, 7)
        self.assertNotIn("fixture checks passed", result.stdout)
        self.assertTrue((self.root / "mysql.pid").exists())

    def test_background_descendant_is_stopped_after_command_exits(self):
        result = self.run_fixture(CI_FIXTURE_BACKGROUND="normal")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.root / "background.pid").exists())

    def test_background_descendant_ignoring_term_is_killed(self):
        result = self.run_fixture(CI_FIXTURE_BACKGROUND="ignore-term")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.root / "background.pid").exists())

    def test_termination_stops_owned_command_and_server(self):
        process = subprocess.Popen(self.command(), cwd=self.root,
                                   env=dict(self.env, CI_FIXTURE_CHILD_WAIT="1"),
                                   text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 10
            while not (self.root / "child.pid").exists():
                if process.poll() is not None or time.monotonic() >= deadline:
                    self.fail("fixture command did not start")
                time.sleep(0.02)
            process.send_signal(signal.SIGTERM)
            stdout, stderr = process.communicate(timeout=15)
            self.assertEqual(process.returncode, 143, stdout + stderr)
            self.assert_clean(subprocess.CompletedProcess(process.args, process.returncode, stdout, stderr))
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate(timeout=5)


if __name__ == "__main__":
    unittest.main()
