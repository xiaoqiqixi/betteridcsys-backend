#!/usr/bin/env python3
"""Guard and lifecycle tests using fake Go/Node tools; never launches a browser."""
import importlib.util
import copy
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("browser-demo.py").resolve()
SPEC = importlib.util.spec_from_file_location("browser_demo", SCRIPT)
browser_demo = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(browser_demo)
PNG = b"\x89PNG\r\n\x1a\nsynthetic-fixture"


def boundary_cases():
    cases = []
    for surface, names in browser_demo.BOUNDARY_TARGETS.items():
        for fixture in (("default",) if surface == "breadcrumb" else ("ascii", "cjk")):
            for locale in ("zh-CN", "en"):
                for width in ((721,) if surface == "breadcrumb" else browser_demo.BOUNDARY_WIDTHS):
                    box = {"x": 0, "y": 0, "width": 100, "height": 20}
                    cases.append({
                        "schema": "aster-responsive-boundaries-v1", "mode": "assertion",
                        "surface": surface, "fixture": fixture, "locale": locale,
                        "viewport": {"width": width, "height": 900},
                        "document": {"viewport_width": width, "scroll_width": width},
                        "passed": True,
                        "checks": [{"id": name, "passed": True}
                                   for name in browser_demo.boundary_check_ids(surface)],
                        "measurements": [{"id": name, "visible": True, "rect": dict(box),
                                          "container_rect": dict(box), "text_rect": dict(box),
                                          "line_count": 1, "client_width": 100, "scroll_width": 100,
                                          "content_width": 90, "text_width": 50,
                                          "visible_width": 100, "overflow_left": 0, "overflow_right": 0}
                                         for name in names],
                    })
    return cases


def boundary_lines(cases):
    return "".join("BOUNDARY_PROBE_JSON " + json.dumps(case, separators=(",", ":")) + "\n"
                   for case in cases)


def executable(path, source):
    path.write_text(textwrap.dedent(source), encoding="utf-8")
    path.chmod(0o700)


def wait_for(path, process, timeout=12):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        # write_text creates an empty file before writing the PID. Waiting only
        # for existence can kill the writer early and later mistake /proc/ for
        # a surviving child; require the complete, bounded PID first.
        try:
            with path.open("rb") as marker:
                raw = marker.read(11)
        except FileNotFoundError:
            raw = b""
        if raw and len(raw) <= 10 and raw[:1] != b"0" and raw.isdigit() and int(raw) <= 2147483647:
            return
        if process.poll() is not None:
            raise AssertionError("Fixture helper exited before readiness")
        time.sleep(0.02)
    raise AssertionError("Fixture readiness timed out")


class PidFileReadinessTests(unittest.TestCase):
    def test_empty_or_invalid_pid_file_is_not_ready(self):
        process = mock.Mock()
        process.poll.return_value = None
        with tempfile.TemporaryDirectory(prefix="aster-pid-readiness-") as directory:
            marker = Path(directory) / "fixture.pid"
            for raw in (b"", b"not-a-pid", b"0", b"-1", b"12\n34", b"123\n", b"\xff", b"2147483648", b"12345678901"):
                with self.subTest(raw=raw):
                    marker.write_bytes(raw)
                    with mock.patch.object(time, "monotonic", side_effect=(0, 0, 2)), mock.patch.object(time, "sleep"):
                        with self.assertRaisesRegex(AssertionError, "Fixture readiness timed out"):
                            wait_for(marker, process, timeout=1)

    def test_complete_positive_pid_file_is_ready(self):
        process = mock.Mock()
        process.poll.side_effect = AssertionError("Ready PID must not wait for fixture exit")
        with tempfile.TemporaryDirectory(prefix="aster-pid-readiness-") as directory:
            marker = Path(directory) / "fixture.pid"
            marker.write_bytes(b"12345")
            self.assertIsNone(wait_for(marker, process))


class BrowserVersionTextTests(unittest.TestCase):
    def test_upstream_formats_and_debian_build_annotation_are_recognized(self):
        values = ["Google Chrome 1.0.0.0", "Google Chrome 154.0.8037.97",
                  "Chromium 154.0.8037.57",
                  "Chromium 154.0.8037.57 built on Debian GNU/Linux 13 (trixie)"]
        values += [f"{brand} 155.0.8059.39 {channel}"
                   for brand in ("Google Chrome", "Chromium")
                   for channel in ("unknown", "extended", "beta", "dev", "canary")]
        for value in values:
            with self.subTest(value=value):
                self.assertTrue(browser_demo.valid_browser_version_text(value))

    def test_unrecognized_or_unbounded_version_text_is_rejected(self):
        debian = "Chromium 154.0.8037.57 built on Debian GNU/Linux 13 (trixie)"
        values = ["Firefox 154.0.8037.57", "HeadlessChrome 154.0.8037.57",
                  "Google Chrome 154.0.8037", "Google Chrome 154.0.8037.57.1",
                  "Chromium 123456789.0.8037.57", "Chromium 154.-1.8037.57",
                  "Chromium 154.0.x.57", "Chromium １５４.0.8037.57",
                  "Google Chrome 154.0.8037.57 stable", "Chromium 154.0.8037.57 nightly",
                  debian.replace("Chromium", "Google Chrome", 1),
                  debian.replace(" built on", " unknown built on", 1),
                  debian.replace("Linux 13", "Linux 1000"),
                  debian.replace("Linux 13", "Linux 13.1.2"),
                  debian.replace("(trixie)", "()"), debian.replace("(trixie)", "(Trixie)"),
                  debian.replace("(trixie)", "(trixie extra)"),
                  debian.replace("(trixie)", "(" + "a" * 33 + ")"),
                  debian + " forged", debian + " built on Debian GNU/Linux 13 (trixie)",
                  debian + "\nextra line", debian.replace("trixie", "tri\x00xie")]
        for value in values:
            with self.subTest(value=value):
                self.assertFalse(browser_demo.valid_browser_version_text(value))


class BrowserDemoTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="aster-browser-helper-test-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.repo = self.root / "source checkout"
        frontend = self.repo / "vps-billing/frontend"
        backend = self.repo / "vps-billing/backend"
        for directory in (backend, frontend / "dist", frontend / "tests", frontend / "node_modules/vite/bin"):
            directory.mkdir(parents=True)
        (backend / "go.mod").write_text("module fixture.invalid/test\n")
        (frontend / "dist/index.html").write_text("synthetic frontend\n")
        (frontend / "tests/e2e.mjs").write_text('const config={chromiumSandbox: true}; await shot("fixture");\n')
        (frontend / "node_modules/vite/bin/vite.js").write_text("// fixture\n")
        self.chrome = self.root / "trusted-chrome"
        executable(self.chrome, "#!/bin/sh\nprintf 'Google Chrome 154.0.0.0\\n'\n")
        self.output = self.root / "new-evidence"
        self.tools = self.root / "tools"
        self.tools.mkdir()
        self.mode = self.root / "mode"
        self.mode.write_text("success")
        self.api_script = self.root / "api.py"
        self.api_pid = self.root / "api.pid"
        self.preview_pid = self.root / "preview.pid"
        self.browser_pid = self.root / "browser.pid"
        self.detached_pid = self.root / "detached.pid"
        self.temporary = self.root / "private-temporary"
        self.temporary.mkdir()
        common = f'''
            import json, os, sys, time
            from pathlib import Path
            assert os.environ.get('HOME') == {os.environ.get('HOME')!r}
            for name in ('MYSQL_DSN', 'ASTER_MYSQL_TEST_DSN', 'SMTP_ENCRYPTION_KEY_FILE',
                         'GITHUB_TOKEN', 'GH_TOKEN', 'NODE_OPTIONS', 'HTTP_PROXY',
                         'TEST_ADMIN_PASSWORD', 'PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH'):
                assert name not in os.environ, name
        '''
        executable(self.api_script, "#!/usr/bin/env python3\n" + textwrap.dedent(common) + textwrap.dedent(f'''
            from http.server import BaseHTTPRequestHandler, HTTPServer
            assert os.environ['APP_MODE']=='demo'
            assert os.environ['EMAIL_PROVIDER']=='email.disabled'
            assert os.environ['PAYMENT_PROVIDER']=='payment.disabled'
            assert 'SESSION_SECRET' not in os.environ
            Path({str(self.api_pid)!r}).write_text(str(os.getpid()))
            class Handler(BaseHTTPRequestHandler):
                def do_GET(self):
                    self.send_response(200); self.end_headers()
                    self.wfile.write(json.dumps({{'data':{{'status':'ready','demo_mode':True}}}}).encode())
                def log_message(self,*args):pass
            host,port=os.environ['LISTEN_ADDR'].rsplit(':',1)
            assert host=='127.0.0.1'
            HTTPServer((host,int(port)),Handler).serve_forever()
        '''))
        executable(self.tools / "go", "#!/usr/bin/env python3\n" + textwrap.dedent(common) + textwrap.dedent(f'''
            import shutil
            assert os.environ['GOENV']=='off' and os.environ['GOPROXY']=='off'
            assert '-mod=readonly' in sys.argv
            assert 'APP_MODE' not in os.environ
            if Path({str(self.mode)!r}).read_text()=='build-fail':sys.exit(9)
            target=Path(sys.argv[sys.argv.index('-o')+1])
            shutil.copyfile({str(self.api_script)!r},target);target.chmod(0o700)
        '''))
        executable(self.tools / "git", "#!/usr/bin/env python3\n" + textwrap.dedent(f'''
            import sys
            from pathlib import Path
            if sys.argv[1]=='rev-parse':
                print({'a'*40!r})
            else:
                assert sys.argv[1:]==['status','--porcelain','--untracked-files=normal']
                if Path({str(backend/'untracked.go')!r}).exists():
                    print('?? vps-billing/backend/untracked.go')
        '''))
        executable(self.tools / "node", "#!/usr/bin/env python3\n" + textwrap.dedent(common) + textwrap.dedent(f'''
            import subprocess
            if sys.argv[1].endswith('vite.js'):
                from http.server import BaseHTTPRequestHandler,HTTPServer
                import urllib.request
                assert sys.argv[2]=='preview' and '--strictPort' in sys.argv
                config=Path(sys.argv[sys.argv.index('--config')+1]).read_text()
                config=json.loads(config.removeprefix('export default ').strip().removesuffix(';'))
                assert config['envDir'] is False
                preview=config['preview']; assert preview['host']=='127.0.0.1'
                assert preview['proxy']['/api']['changeOrigin'] is False
                target=preview['proxy']['/api']['target']
                assert int(target.rsplit(':',1)[1]) != preview['port']
                Path({str(self.preview_pid)!r}).write_text(str(os.getpid()))
                class Handler(BaseHTTPRequestHandler):
                    def do_GET(self):
                        with urllib.request.build_opener(urllib.request.ProxyHandler({{}})).open(target+self.path) as response:
                            raw=response.read()
                        self.send_response(200);self.end_headers();self.wfile.write(raw)
                    def log_message(self,*args):pass
                HTTPServer(('127.0.0.1',preview['port']),Handler).serve_forever()
            assert sys.argv[1].endswith('e2e.mjs')
            assert os.environ['CHROMIUM_PATH']=={str(self.chrome)!r}
            assert os.environ['TEST_BASE_URL'].startswith('http://127.0.0.1:')
            assert 'APP_MODE' not in os.environ
            Path({str(self.browser_pid)!r}).write_text(str(os.getpid()))
            mode=Path({str(self.mode)!r}).read_text()
            if mode=='sandbox-fail':
                print('No usable sandbox!');sys.exit(6)
            if mode=='assertion-fail':
                print('Error: synthetic assertion expected a visible button');sys.exit(7)
            child=subprocess.Popen([sys.executable,'-c','import time;time.sleep(120)'],start_new_session=True)
            Path({str(self.detached_pid)!r}).write_text(str(child.pid))
            if mode=='interrupt':time.sleep(120)
            time.sleep(0.1)
            output=Path(os.environ['E2E_OUTPUT_DIR']);output.mkdir()
            (output/'fixture.png').write_bytes({PNG!r})
            diagnostic=Path({str(self.root / 'boundary-output.log')!r})
            if diagnostic.exists():print(diagnostic.read_text(),end='')
        '''))
        self.env = dict(os.environ, PATH=str(self.tools) + os.pathsep + os.environ["PATH"], TMPDIR=str(self.temporary))
        for name in ("MYSQL_DSN", "ASTER_MYSQL_TEST_DSN", "SMTP_ENCRYPTION_KEY_FILE", "GITHUB_TOKEN", "GH_TOKEN",
                     "NODE_OPTIONS", "HTTP_PROXY", "TEST_ADMIN_PASSWORD", "PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH",
                     "APP_MODE", "SESSION_SECRET", "PAYMENT_PROVIDER", "EMAIL_PROVIDER"):
            self.env[name] = "must-not-reach-demo"

    def command(self):
        return [sys.executable, "-I", str(SCRIPT), "--repo-root", str(self.repo), "--output", str(self.output), "--chromium-path", str(self.chrome)]

    def run_helper(self):
        result = subprocess.run(self.command(), env=self.env, capture_output=True, text=True, timeout=25)
        summary = json.loads((self.output / "summary.json").read_text())
        self.assertEqual(summary["cleanup"], True, result.stdout + result.stderr)
        self.assertEqual(list(self.temporary.iterdir()), [])
        for marker in (self.api_pid, self.preview_pid, self.browser_pid, self.detached_pid):
            if marker.exists():
                self.assertFalse(Path('/proc/' + marker.read_text()).exists(), marker.name)
        self.assertNotIn("must-not-reach-demo", result.stdout + result.stderr)
        return result, summary

    def test_success_cleans_detached_descendant_and_keeps_only_synthetic_evidence(self):
        foreign = subprocess.Popen([sys.executable, "-c", "import time;time.sleep(30)"])
        def stop_foreign():
            if foreign.poll() is None:
                foreign.terminate()
            foreign.wait(timeout=5)
        self.addCleanup(stop_foreign)
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(summary["passed"])
        self.assertEqual(summary["source_commit"], "a" * 40)
        self.assertEqual(summary["stage"], "complete")
        self.assertEqual(summary["browser_version"], "Google Chrome 154.0.0.0")
        self.assertEqual({p.name for p in self.output.iterdir()}, {"summary.json", "screenshots"})
        self.assertEqual((self.output / "screenshots/fixture.png").read_bytes(), PNG)
        self.assertIsNone(foreign.poll(), "unrelated process was terminated")
        foreign.terminate();foreign.wait(timeout=5)

    def test_sandbox_failure_never_falls_back(self):
        self.mode.write_text("sandbox-fail")
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 1)
        self.assertFalse(summary["passed"])
        self.assertEqual(summary["stage"], "browser")
        self.assertIn("no sandbox bypass", summary["reason"])
        self.assertFalse((self.output / "screenshots").exists())

    def test_direct_binary_unknown_channel_is_retained_without_environment_override(self):
        executable(self.chrome, "#!/bin/sh\nprintf 'Google Chrome 155.0.8059.39 unknown\\n'\n")
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(summary["browser_version"], "Google Chrome 155.0.8059.39 unknown")
        self.assertTrue(summary["sandbox_required"])

    def test_debian_version_continues_the_normal_fake_lifecycle_and_keeps_raw_output(self):
        version = "Chromium 154.0.8037.57 built on Debian GNU/Linux 13 (trixie)"
        executable(self.chrome, "#!/bin/sh\nprintf '" + version + "\\n'\n")
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(summary["browser_version"], version)
        self.assertTrue(summary["sandbox_required"])
        self.assertTrue(summary["functional_passed"])
        self.assertTrue(summary["passed"])
        self.assertEqual(summary["stage"], "complete")
        for marker in (self.api_pid, self.preview_pid, self.browser_pid):
            self.assertTrue(marker.exists(), marker.name)

    def test_debian_version_does_not_bypass_a_sandbox_failure(self):
        executable(self.chrome, "#!/bin/sh\nprintf 'Chromium 154.0.8037.57 built on Debian GNU/Linux 13 (trixie)\\n'\n")
        self.mode.write_text("sandbox-fail")
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 1)
        self.assertFalse(summary["passed"])
        self.assertFalse(summary["functional_passed"])
        self.assertEqual(summary["stage"], "browser")
        self.assertTrue(summary["sandbox_required"])
        self.assertIn("no sandbox bypass", summary["reason"])
        self.assertFalse((self.output / "screenshots").exists())

    def test_malformed_version_is_rejected_before_demo_services_start(self):
        executable(self.chrome, "#!/bin/sh\nprintf 'Google Chrome 155.0.8059.39 unknown\\nextra line\\n'\n")
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 1)
        self.assertEqual(summary["stage"], "browser-version")
        self.assertIn("unexpected version", summary["reason"])
        self.assertFalse(self.api_pid.exists())
        self.assertFalse(self.preview_pid.exists())

    def test_version_stderr_warning_is_not_treated_as_version_or_published(self):
        executable(self.chrome, "#!/bin/sh\nprintf 'private startup warning\\n' >&2\nprintf 'Google Chrome 154.0.8037.97\\n'\n")
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(summary["browser_version"], "Google Chrome 154.0.8037.97")
        self.assertNotIn("private startup warning", result.stdout + result.stderr + json.dumps(summary))
        self.assertEqual({p.name for p in self.output.iterdir()}, {"summary.json", "screenshots"})

    def test_version_on_stderr_without_stdout_is_rejected(self):
        executable(self.chrome, "#!/bin/sh\nprintf 'Google Chrome 154.0.8037.97\\n' >&2\n")
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 1)
        self.assertEqual(summary["stage"], "browser-version")
        self.assertFalse(self.api_pid.exists())

    def test_untrusted_browser_is_never_executed_even_for_version(self):
        marker = self.root / "untrusted-browser-ran"
        executable(self.chrome, "#!/usr/bin/env python3\nfrom pathlib import Path\nPath(" + repr(str(marker)) + ").touch()\n")
        self.chrome.chmod(0o777)
        result = subprocess.run(self.command(), env=self.env, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 1)
        self.assertIn("mode=0777", result.stderr)
        self.assertFalse(marker.exists())
        self.assertFalse(self.output.exists())

    def test_build_failure_starts_no_demo_or_browser(self):
        self.mode.write_text("build-fail")
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 1)
        self.assertEqual(summary["stage"], "build-api")
        self.assertFalse(self.api_pid.exists())
        self.assertFalse(self.browser_pid.exists())

    def test_untracked_source_refuses_to_claim_head_or_run_build(self):
        (self.repo/'vps-billing/backend/untracked.go').write_text('package main\n')
        result,summary=self.run_helper()
        self.assertEqual(result.returncode,1)
        self.assertEqual(summary['stage'],'source-before')
        self.assertIn('untracked',summary['reason'])
        self.assertFalse(self.api_pid.exists());self.assertFalse(self.browser_pid.exists())

    def test_assertion_failure_is_bounded_and_has_stage(self):
        self.mode.write_text("assertion-fail")
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 1)
        self.assertEqual(summary["stage"], "browser")
        self.assertIn("synthetic assertion", summary["reason"])
        self.assertLessEqual(len(summary["reason"]), 1600)

    def test_signal_cleans_owned_sessions_including_detached_child(self):
        self.mode.write_text("interrupt")
        process = subprocess.Popen(self.command(), env=self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            wait_for(self.detached_pid, process)
            process.send_signal(signal.SIGTERM)
            output, error = process.communicate(timeout=15)
        finally:
            if process.poll() is None:
                process.kill();process.wait(timeout=5)
        self.assertEqual(process.returncode, 1, output + error)
        summary = json.loads((self.output / "summary.json").read_text())
        self.assertFalse(summary["passed"])
        self.assertTrue(summary["cleanup"])
        self.assertIn("SIGTERM", summary["reason"])
        self.assertEqual(list(self.temporary.iterdir()), [])
        for marker in (self.api_pid, self.preview_pid, self.browser_pid, self.detached_pid):
            self.assertFalse(Path('/proc/' + marker.read_text()).exists())

    def test_input_guards_reject_reused_output_missing_assets_and_unsafe_browser(self):
        self.output.mkdir()
        with self.assertRaises(browser_demo.Failure):browser_demo.validate_inputs(self.repo,self.chrome,self.output)
        self.output.rmdir()
        self.chrome.chmod(0o777)
        with self.assertRaises(browser_demo.Failure):browser_demo.validate_inputs(self.repo,self.chrome,self.output)
        self.chrome.chmod(0o700)
        index=self.repo/'vps-billing/frontend/dist/index.html';index.unlink()
        with self.assertRaises(browser_demo.Failure):browser_demo.validate_inputs(self.repo,self.chrome,self.output)

    def test_sandbox_source_guard_and_output_containment(self):
        test=self.repo/'vps-billing/frontend/tests/e2e.mjs'
        test.write_text('chromiumSandbox: false; await shot("fixture");')
        with self.assertRaises(browser_demo.Failure):browser_demo.validate_inputs(self.repo,self.chrome,self.output)
        test.write_text('chromiumSandbox: true; await shot("fixture");')
        with self.assertRaises(browser_demo.Failure):browser_demo.validate_inputs(self.repo,self.chrome,self.repo/'evidence')

    def test_shipped_e2e_screenshot_inventory_fits_the_release_guard(self):
        source = SCRIPT.parents[2] / "frontend/tests/e2e.mjs"
        (self.repo / "vps-billing/frontend/tests/e2e.mjs").write_text(source.read_text())
        _, _, _, screenshots = browser_demo.validate_inputs(self.repo, self.chrome, self.output)
        self.assertIn("dashboard-desktop.png", screenshots)
        self.assertIn("plans-mobile.png", screenshots)
        self.assertLessEqual(len(screenshots), 30)

    def test_screenshot_inventory_still_rejects_more_than_thirty_names(self):
        source = "chromiumSandbox: true;\n" + "\n".join(
            f'await shot("fixture-{index}");' for index in range(31))
        (self.repo / "vps-billing/frontend/tests/e2e.mjs").write_text(source)
        with self.assertRaisesRegex(browser_demo.Failure, "bounded set"):
            browser_demo.validate_inputs(self.repo, self.chrome, self.output)

    def test_exact_screenshots_reject_links_extra_files_and_missing_files(self):
        source=self.root/'screens';source.mkdir();target=self.root/'retained'
        with self.assertRaises(browser_demo.Failure):browser_demo.retain_screenshots(source,target,{'fixture.png'})
        (source/'fixture.png').symlink_to(self.chrome)
        with self.assertRaises((browser_demo.Failure,OSError)):browser_demo.retain_screenshots(source,target,{'fixture.png'})
        (source/'fixture.png').unlink();(source/'fixture.png').write_bytes(PNG);(source/'private.log').write_text('private')
        with self.assertRaises(browser_demo.Failure):browser_demo.retain_screenshots(source,target,{'fixture.png'})

    def test_listener_guard_rejects_response_from_unowned_port(self):
        listeners,ports=browser_demo.reserve_ports()
        try:
            self.assertNotEqual(*ports)
            fake=mock.Mock(pid=os.getpid());fake.poll.return_value=None
            self.assertFalse(browser_demo.listener_owned(fake,ports[0]))
        finally:
            for listener in listeners:listener.close()

    def test_cleanup_uses_identity_handles_never_recycled_numeric_pids(self):
        supervisor=browser_demo.Processes.__new__(browser_demo.Processes)
        supervisor.owned={1234:('old-birth',91)}
        supervisor.sample=mock.Mock()
        with mock.patch.object(signal,'pidfd_send_signal',side_effect=ProcessLookupError) as send, mock.patch.object(os,'kill') as kill, mock.patch.object(os,'killpg') as killpg:
            self.assertTrue(supervisor.signal_owned(signal.SIGTERM))
            send.assert_called_once_with(91,signal.SIGTERM)
            kill.assert_not_called();killpg.assert_not_called()

    def test_timeout_and_ignored_term_are_forcibly_cleaned(self):
        supervisor=browser_demo.Processes()
        marker=self.root/'ignoring-term.pid'
        child=supervisor.start([sys.executable,'-c',
            'import os,signal,time;from pathlib import Path;signal.signal(signal.SIGTERM,signal.SIG_IGN);Path('+repr(str(marker))+').write_text(str(os.getpid()));time.sleep(60)'],
            self.root,dict(os.environ),self.root/'child-private.log')
        try:
            wait_for(marker,child)
            with self.assertRaises(browser_demo.Failure):supervisor.wait(child,0.05)
        finally:
            self.assertTrue(supervisor.cleanup(timeout=0.1))
        self.assertEqual(child.returncode,-signal.SIGKILL)
        self.assertFalse(Path('/proc/'+str(child.pid)).exists())

    def test_cleanup_failure_is_explicit_and_fails_an_otherwise_successful_run(self):
        program = textwrap.dedent('''\
            import importlib.util,sys
            from pathlib import Path
            spec=importlib.util.spec_from_file_location('fixture_browser',sys.argv[1])
            module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
            original=module.Processes.cleanup
            def incomplete(self,*args,**kwargs):
                original(self,*args,**kwargs)
                return False
            module.Processes.cleanup=incomplete
            raise SystemExit(module.run(Path(sys.argv[2]),Path(sys.argv[3]),Path(sys.argv[4])))
        ''')
        result=subprocess.run([sys.executable,'-I','-c',program,str(SCRIPT),str(self.repo),str(self.chrome),str(self.output)],env=self.env,capture_output=True,text=True,timeout=25)
        summary=json.loads((self.output/'summary.json').read_text())
        self.assertEqual(result.returncode,1,result.stdout+result.stderr)
        self.assertFalse(summary['passed']);self.assertFalse(summary['cleanup'])
        self.assertIn('cleanup failed',summary['reason'])
        self.assertEqual(list(self.temporary.iterdir()),[])

    def test_descendant_discovery_rechecks_direct_parent_after_opening_handle(self):
        supervisor=browser_demo.Processes.__new__(browser_demo.Processes);supervisor.owned={}
        snapshots=[{9876:(os.getpid(),'original','S')},{9876:(1111,'original','S')}]
        with mock.patch.object(browser_demo,'process_snapshot',side_effect=snapshots),mock.patch.object(os,'pidfd_open',return_value=71),mock.patch.object(os,'close') as close:
            supervisor.sample()
            self.assertEqual(supervisor.owned,{})
            close.assert_called_once_with(71)

    def test_live_checks_kernel_handle_even_when_proc_snapshot_omits_process(self):
        supervisor=browser_demo.Processes.__new__(browser_demo.Processes);supervisor.owned={123:('birth',17)};supervisor.sample=mock.Mock()
        with mock.patch.object(browser_demo.select,'poll') as poll:
            poll.return_value.poll.return_value=[]
            self.assertTrue(supervisor.live())
            poll.return_value.poll.return_value=[(17,browser_demo.select.POLLIN)]
            self.assertFalse(supervisor.live())

    def test_required_boundary_failure_retains_geometry_screenshots_and_cleanup_before_failing(self):
        cases = boundary_cases()
        cases[0]["document"]["scroll_width"] += 40
        cases[0]["checks"][0]["passed"] = False
        cases[0]["passed"] = False
        (self.root / "boundary-output.log").write_text("private arbitrary response must stay private\n" + boundary_lines(cases))
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertFalse(summary["passed"])
        self.assertTrue(summary["functional_passed"])
        self.assertEqual(summary["stage"], "boundary-assertions")
        self.assertIn("Required responsive boundary assertions failed", summary["reason"])
        self.assertEqual(summary["boundary_probe"]["mode"], "assertion")
        self.assertTrue(summary["boundary_probe"]["required"])
        self.assertFalse(summary["boundary_probe"]["passed"])
        self.assertEqual(summary["boundary_probe"]["case_count"], 50)
        self.assertEqual(summary["boundary_probe"]["failed_cases"], 1)
        lines = [line for line in result.stdout.splitlines() if line.startswith("BOUNDARY_PROBE_JSON ")]
        self.assertEqual(len(lines), 50)
        self.assertEqual([json.loads(line.split(" ", 1)[1]) for line in lines], cases)
        self.assertEqual((self.output / "screenshots/fixture.png").read_bytes(), PNG)
        self.assertNotIn("private arbitrary response", result.stdout + result.stderr + json.dumps(summary))

    def test_required_boundary_success_and_original_functional_checks_must_both_pass(self):
        (self.root / "boundary-output.log").write_text(boundary_lines(boundary_cases()))
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(summary["passed"])
        self.assertTrue(summary["functional_passed"])
        self.assertTrue(summary["boundary_probe"]["passed"])
        self.assertTrue(summary["boundary_probe"]["required"])
        self.assertEqual(summary["boundary_probe"]["case_count"], 50)

    def test_required_boundary_evidence_cannot_replace_a_missing_original_screenshot(self):
        (self.root / "boundary-output.log").write_text(boundary_lines(boundary_cases()))
        # The original screenshot inventory remains exact, even when every
        # boundary assertion claims success.
        test = self.repo / "vps-billing/frontend/tests/e2e.mjs"
        test.write_text(test.read_text() + '\nawait shot("missing-original");\n')
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 1)
        self.assertFalse(summary["passed"])
        self.assertFalse(summary["functional_passed"])
        self.assertTrue(summary["boundary_probe"]["passed"])
        self.assertIn("Screenshot evidence is incomplete", summary["reason"])

    def test_required_probe_matrix_cannot_disappear_without_failing_acceptance(self):
        test = self.repo / "vps-billing/frontend/tests/e2e.mjs"
        test.write_text(test.read_text() + '\nimport {probeLoginBoundaries} from "./responsive-boundaries.mjs";\n')
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 1)
        self.assertFalse(summary["passed"])
        self.assertIn("matrix is incomplete", summary["reason"])
        self.assertNotIn("boundary_probe", summary)

    def test_invalid_probe_never_forwards_payload_or_original_process_logs(self):
        (self.root / "boundary-output.log").write_text(
            'private raw response\nBOUNDARY_PROBE_JSON {"secret":"must-remain-private"}\n')
        result, summary = self.run_helper()
        self.assertEqual(result.returncode, 1)
        self.assertIn("exact synthetic schema", summary["reason"])
        self.assertNotIn("must-remain-private", result.stdout + result.stderr + json.dumps(summary))
        self.assertNotIn("private raw response", result.stdout + result.stderr + json.dumps(summary))


class BoundaryEvidenceTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="aster-boundary-parser-test-")
        self.addCleanup(self.directory.cleanup)
        self.log = Path(self.directory.name) / "browser.log"

    def parse(self, cases=None, raw=None, required=True):
        self.log.write_text(boundary_lines(boundary_cases() if cases is None else cases)
                            if raw is None else raw)
        return browser_demo.read_boundary_probes(self.log, required=required)

    def test_complete_numeric_matrix_is_retained(self):
        result = self.parse()
        self.assertTrue(result["passed"])
        self.assertEqual(result["mode"], "assertion")
        self.assertTrue(result["required"])
        self.assertEqual(result["case_count"], 50)
        self.assertEqual(result["failed_cases"], 0)
        self.assertEqual(result["cases"], boundary_cases())

    def test_document_width_may_exclude_a_classic_vertical_scrollbar(self):
        cases = boundary_cases()
        cases[0]["document"]["scroll_width"] = cases[0]["viewport"]["width"] - 15
        result = self.parse(cases)
        self.assertTrue(result["passed"])
        self.assertEqual(result["case_count"], 50)

    def test_native_input_scroll_metrics_determine_full_value_assertion_not_canvas(self):
        cases = boundary_cases()
        row = next(item for item in cases if item["surface"] == "financial")
        field = next(item for item in row["measurements"] if item["id"] == "price-input")
        field["text_width"] = 999_999  # Canvas estimate alone is not a clipping verdict.
        self.assertTrue(self.parse(cases)["passed"])
        field["scroll_width"] = field["client_width"] + 1
        self.assertTrue(self.parse(cases)["passed"])
        field["scroll_width"] += 1
        with self.assertRaisesRegex(browser_demo.Failure, "exact synthetic schema"):
            self.parse(cases)  # A claimed pass cannot contradict native metrics.
        check = next(item for item in row["checks"] if item["id"] == "price-input-full-value-width")
        check["passed"] = False
        row["passed"] = False
        result = self.parse(cases)
        self.assertFalse(result["passed"])
        self.assertEqual(result["failed_cases"], 1)

    def test_optional_absence_only_for_existing_nonprobe_source(self):
        self.assertIsNone(self.parse(raw="private non-diagnostic log\n", required=False))
        with self.assertRaisesRegex(browser_demo.Failure, "matrix is incomplete"):
            self.parse(raw="private non-diagnostic log\n")

    def test_duplicate_and_missing_cases_fail(self):
        cases = boundary_cases()
        cases[1] = copy.deepcopy(cases[0])
        with self.assertRaisesRegex(browser_demo.Failure, "Duplicate"):
            self.parse(cases)
        with self.assertRaisesRegex(browser_demo.Failure, "incomplete"):
            self.parse(boundary_cases()[:-1])

    def test_malformed_json_prefix_duplicate_keys_and_nonfinite_numbers_fail(self):
        samples = ["BOUNDARY_PROBE_JSON{}\n", "BOUNDARY_PROBE_JSON {\n",
                   'BOUNDARY_PROBE_JSON {"schema":1,"schema":2}\n',
                   'BOUNDARY_PROBE_JSON {"rect":NaN}\n',
                   'BOUNDARY_PROBE_JSON {"rect":Infinity}\n']
        for raw in samples:
            with self.subTest(raw=raw), self.assertRaises(browser_demo.Failure):
                self.parse(raw=raw)

    def test_schema_rejects_text_urls_extra_fields_and_arbitrary_identifiers(self):
        for field, value in (("url", "https://private.invalid"), ("text", "private text"),
                             ("fixture", "arbitrary"), ("locale", "other"),
                             ("surface", "other"), ("mode", "diagnostic")):
            cases = boundary_cases()
            cases[0][field] = value
            with self.subTest(field=field), self.assertRaises(browser_demo.Failure):
                self.parse(cases)

    def test_rect_number_lines_and_boolean_bounds_are_strict(self):
        mutations = [
            lambda row: row["measurements"][0]["rect"].update(x=1_000_001),
            lambda row: row["measurements"][0]["rect"].update(width=-1),
            lambda row: row["measurements"][0].update(line_count=513),
            lambda row: row["measurements"][0].update(visible="true"),
            lambda row: row["measurements"][0].update(content_width=True),
            lambda row: row["measurements"][0].update(text_width="private"),
            lambda row: row["checks"][0].update(passed=1),
            lambda row: row["viewport"].update(width=True),
            lambda row: row["document"].update(viewport_width=321),
            lambda row: row.update(passed=False),
        ]
        for index, mutate in enumerate(mutations):
            cases = boundary_cases()
            mutate(cases[0])
            with self.subTest(index=index), self.assertRaises(browser_demo.Failure):
                self.parse(cases)

    def test_duplicate_measurement_or_check_identifiers_fail(self):
        for field in ("measurements", "checks"):
            cases = boundary_cases()
            cases[0][field][1] = copy.deepcopy(cases[0][field][0])
            with self.subTest(field=field), self.assertRaises(browser_demo.Failure):
                self.parse(cases)

    def test_single_line_total_log_and_diagnostic_byte_limits_fail(self):
        samples = ["BOUNDARY_PROBE_JSON " + " " * (32 * 1024),
                   ("x" * (16 * 1024) + "\n") * 257]
        # Legal JSON whitespace cannot bypass the aggregate diagnostic bound.
        samples.append("".join(line + " " * 12_000 + "\n"
                               for line in boundary_lines(boundary_cases()).splitlines()))
        for index, raw in enumerate(samples):
            with self.subTest(index=index), self.assertRaises(browser_demo.Failure):
                self.parse(raw=raw)


if __name__ == '__main__':
    unittest.main()
