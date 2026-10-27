#!/usr/bin/env python3
"""Bounded real-MySQL UI gate. Requires mysql-native.sh and private Firefox.

No host trust, browser installation, production database, demo adapter or insecure
browser fallback. Export only validated synthetic PNGs and fixed-schema reports.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import stat
import struct
import sys
import tempfile
import time

SPEC = importlib.util.spec_from_file_location("owned_browser_processes", Path(__file__).with_name("browser-demo.py"))
OWNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(OWNER)
Failure = OWNER.Failure
SHOTS = sorted([f"scope-{role}-{size}-{locale}.png"
                for role in ("customer", "admin") for size in ("desktop", "mobile") for locale in ("zh-CN", "en")]
               + [f"audit-{size}-{locale}.png" for size in ("desktop", "mobile") for locale in ("zh-CN", "en")])
BROWSER_PHASES = {
    "browser-start", "tls-untrusted-navigation", "tls-trusted-navigation",
    "reset-preferences", "reset-reload", "default-preferences",
    "login-open", "login-fill", "login-submit", "login-session", "login-cookie",
    "language-open", "language-select", "language-close", "language-assert",
    "currency-open", "currency-select", "currency-assert", "screenshot",
    "audit-navigation", "audit-assert", "read-fault-baseline", "read-fault-inject",
    "read-fault-assert", "read-fault-delivery", "read-fault-locale", "read-fault-message",
    "read-fault-no-write-warning", "read-fault-retry", "read-fault-no-empty", "read-fault-recover", "logout-open", "logout-submit",
    "logout-session", "report-assert", "report-write",
}


CA_CONFIG = """[req]
prompt=no
distinguished_name=subject
x509_extensions=extensions
[subject]
CN=Aster disposable financial browser localhost CA
[extensions]
basicConstraints=critical,CA:TRUE,pathlen:0
keyUsage=critical,keyCertSign,cRLSign
subjectKeyIdentifier=hash
nameConstraints=critical,permitted;DNS:localhost,permitted;IP:127.0.0.1/255.255.255.255
"""
LEAF_CONFIG = """[req]
prompt=no
distinguished_name=subject
[subject]
CN=localhost
[extensions]
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:localhost,IP:127.0.0.1
"""


def private_dir(value):
    path = Path(value)
    info = path.lstat()
    if not path.is_absolute() or path.resolve() != path or not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise Failure("A canonical owned private directory is required")
    return path


def regular(path, maximum=2_000_000):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or path.resolve() != path or info.st_size > maximum:
        raise Failure("A bounded regular non-symlink file is required")
    return path


def validate(args):
    if platform.system() != "Linux" or platform.machine() != "x86_64" or os.geteuid() == 0:
        raise Failure("Real MySQL browser CI requires unprivileged Linux amd64")
    root = Path(args.repo_root)
    if not root.is_absolute() or root.resolve() != root or not root.is_dir():
        raise Failure("An absolute canonical repository root is required")
    temp = private_dir(os.environ.get("RUNNER_TEMP", ""))
    fixture = private_dir(os.environ.get("ASTER_NATIVE_MYSQL_FIXTURE_ROOT", ""))
    if not fixture.is_relative_to(temp) or not fixture.name.startswith("aster-mysql."):
        raise Failure("The native fixture must be owned beneath runner scratch")
    socket = Path(os.environ.get("ASTER_NATIVE_MYSQL_SOCKET", ""))
    if socket != fixture / "mysql.sock" or socket.resolve() != socket or not stat.S_ISSOCK(socket.lstat().st_mode):
        raise Failure("Only the native private MySQL socket is accepted")
    if not os.environ.get("ASTER_MYSQL_TEST_DSN"):
        raise Failure("The private native MySQL DSN is required")
    output = Path(args.output)
    if not output.is_absolute() or output.exists() or output.is_symlink() or output.resolve() != output or not output.parent.is_dir() or not output.is_relative_to(temp) or output.is_relative_to(root) or output.is_relative_to(fixture):
        raise Failure("Output must be a new canonical directory in runner scratch outside source and database fixture")
    parent = output.parent.stat()
    if parent.st_uid != os.geteuid() or parent.st_mode & 0o022:
        raise Failure("Output parent must be owned and not group/world writable")
    billing = root / "vps-billing"
    for relative in ("frontend/dist/index.html", "frontend/tests/browser-mysql.mjs", "frontend/tests/ui-copy-boundaries.mjs", "deploy/ci/browser-mysql-fixture.gotxt"):
        regular(billing / relative, 5_000_000)
    modules = Path(os.environ.get("PLAYWRIGHT_MODULE_DIR", ""))
    browsers = Path(os.environ.get("PLAYWRIGHT_BROWSERS_PATH", ""))
    if not modules.is_absolute() or not browsers.is_absolute() or not browsers.is_dir():
        raise Failure("A pinned private Playwright Firefox installation is required")
    for package in (modules / "playwright/package.json", billing / "frontend/node_modules/@playwright/test/package.json"):
        if json.loads(regular(package).read_text()).get("version") != "1.64.0":
            raise Failure("Both private Firefox driver and test assertions must be Playwright 1.64.0")
    for command in ("go", "node", "openssl", "certutil"):
        if not shutil.which(command):
            raise Failure("A required native browser dependency is unavailable")
    return root, billing, temp, output


def read_browser_progress(scratch):
    """Export only a small fixed diagnostic vocabulary, never errors or page data."""
    path = scratch / "progress.json"
    if not path.exists() and not path.is_symlink():
        return None
    def unique_object(pairs):
        value = {}
        for key, item in pairs:
            if key in value:
                raise Failure("Duplicate browser progress field")
            value[key] = item
        return value
    value = json.loads(regular(path, 1024).read_text(), object_pairs_hook=unique_object)
    allowed = {
        "schema": {"aster-billing-browser-progress-v1"}, "phase": BROWSER_PHASES,
        "role": {"none", "customer", "admin"}, "size": {"none", "desktop", "mobile"},
        "locale": {"none", "zh-CN", "en"},
    }
    counts = {"fault_hits", "fault_http_200", "fault_fulfilled", "fault_failed"}
    if not isinstance(value, dict) or set(value) != set(allowed) | counts or any(type(value[key]) is not str or value[key] not in choices for key, choices in allowed.items()) or any(type(value[key]) is not int or not 0 <= value[key] <= 32 for key in counts):
        raise Failure("Browser progress fields must use the exact bounded vocabulary")
    return value


def acceptance_environment(base, scratch):
    # Firefox NSS trust does not cover Playwright's Node-side route.fetch.
    # Trust only this invocation's generated localhost-constrained CA and only
    # in the acceptance child, never in the negative TLS probe or the host.
    if "NODE_EXTRA_CA_CERTS" in base or "NODE_TLS_REJECT_UNAUTHORIZED" in base:
        raise Failure("Inherited Node trust overrides are forbidden")
    ca = regular(scratch / "ca.pem", 16384)
    if ca.stat().st_uid != os.geteuid() or stat.S_IMODE(ca.stat().st_mode) != 0o600:
        raise Failure("Only the owned private generated CA may be used")
    return {**base, "NODE_EXTRA_CA_CERTS": str(ca)}


def clean_environment(source, scratch, billing):
    # An allowlist keeps repository/provider tokens, proxies, runtime injections,
    # inherited browser sandbox overrides and user trust settings out of children.
    home = Path(source.get("HOME", str(Path.home())))
    env = {key: source[key] for key in ("PATH", "LANG", "LC_ALL", "TZ") if key in source}
    for key in ("ASTER_MYSQL_TEST_DSN", "ASTER_NATIVE_MYSQL_FIXTURE_ROOT", "ASTER_NATIVE_MYSQL_SOCKET", "PLAYWRIGHT_MODULE_DIR", "PLAYWRIGHT_BROWSERS_PATH"):
        env[key] = source[key]
    for name in ("tmp", "cache", "config"):
        (scratch / name).mkdir(mode=0o700)
    env.update({
        "HOME": str(scratch / "home"), "TMPDIR": str(scratch / "tmp"),
        "XDG_CACHE_HOME": str(scratch / "cache"), "XDG_CONFIG_HOME": str(scratch / "config"),
        "GOPATH": source.get("GOPATH", str(home / "go")),
        "GOMODCACHE": source.get("GOMODCACHE", str(home / "go/pkg/mod")),
        "GOCACHE": source.get("GOCACHE", str(home / ".cache/go-build")),
        "GOENV": "off", "GOWORK": "off", "GOTOOLCHAIN": "local", "CGO_ENABLED": "1",
        "GOOS": "linux", "GOARCH": "amd64", "GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org",
        "CI": "true", "PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD": "1", "PYTHONDONTWRITEBYTECODE": "1",
        "ASTER_BILLING_BROWSER_CI": "1", "ASTER_BILLING_BROWSER_CASE": str(scratch),
        "ASTER_BILLING_BROWSER_DIST": str(billing / "frontend/dist"),
        "NO_PROXY": "localhost,127.0.0.1,::1", "no_proxy": "localhost,127.0.0.1,::1",
    })
    return env


def export_public(scratch, output):
    public = scratch / "public"
    names = {entry.name for entry in public.iterdir()}
    if names != set(SHOTS) | {"report.json", "tls.json"}:
        raise Failure("Browser artifact allowlist mismatch")
    report = json.loads(regular(public / "report.json", 50000).read_text())
    expected_cases = {f"{kind}-{role}-{size}-{locale}" for kind in ("currency", "audit")
                      for role in ("customer", "admin") for size in ("desktop", "mobile") for locale in ("zh-CN", "en")}
    expected_cases |= {f"synthetic-get-fault-{size}-{locale}" for size in ("desktop", "mobile") for locale in ("zh-CN", "en")}
    fields = {"schema": "aster-billing-browser-mysql-v1", "passed": True, "api_mode": "production", "mysql_fixture": True,
              "synthetic_get_faults": 4, "business_writes": 0, "authenticated_logins": 4, "authenticated_logouts": 4}
    if not isinstance(report, dict) or set(report) != set(fields) | {"cases", "screenshots"} or any(type(report.get(k)) is not type(v) or report[k] != v for k, v in fields.items()):
        raise Failure("Browser report provenance or fixed fields are invalid")
    cases = report["cases"]
    if not isinstance(cases, list) or len(cases) != 20 or any(not isinstance(case, dict) or set(case) != {"id", "passed"} or case["passed"] is not True or case["id"] not in expected_cases for case in cases) or {case["id"] for case in cases} != expected_cases:
        raise Failure("Required real API UI cases are missing")
    if sorted(report["screenshots"]) != SHOTS:
        raise Failure("Required synthetic screenshot inventory is incomplete")
    tls = json.loads(regular(public / "tls.json", 1000).read_text())
    if not isinstance(tls, dict) or any(type(value) is not bool for value in tls.values()) or tls != {"untrusted_ca_rejected": True, "private_profile_ca_trusted": True, "ignore_https_errors": False, "host_trust_modified": False}:
        raise Failure("Actual browser TLS negative and positive probes must pass")
    for name in SHOTS:
        source = regular(public / name, 20_000_000)
        with source.open("rb") as stream:
            header = stream.read(24)
        if len(header) != 24 or header[:8] != b"\x89PNG\r\n\x1a\n" or header[12:16] != b"IHDR":
            raise Failure("Synthetic screenshot is not a PNG")
        width, height = struct.unpack(">II", header[16:24])
        if width not in (375, 1440) or not 1 <= height <= 16000:
            raise Failure("Synthetic screenshot dimensions are out of bounds")
    (output / "screenshots").mkdir(mode=0o700)
    for name in SHOTS:
        shutil.copyfile(public / name, output / "screenshots" / name)
    for name, value in (("report.json", report), ("tls.json", tls)):
        (output / name).write_text(json.dumps(value, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo-root", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    stage, supervisor, fixture, scratch, output = "input-validation", None, None, None, None
    passed, cleanup_ok, output_owned = False, True, False
    browser_progress, progress_rejected = None, False
    try:
        root, billing, temp, output = validate(args)
        output.mkdir(mode=0o700)
        output_owned = True
        scratch = Path(tempfile.mkdtemp(prefix="aster-billing-browser.", dir=temp))
        for directory in ("public", "firefox-profile", "home"):
            (scratch / directory).mkdir(mode=0o700)
        supervisor = OWNER.Processes()
        def interrupted(_number, _frame):
            raise Failure("Browser acceptance interrupted")
        signal.signal(signal.SIGINT, interrupted)
        signal.signal(signal.SIGTERM, interrupted)
        env = clean_environment(os.environ, scratch, billing)
        serial = 0
        def run(arguments, timeout=120, required=(), child_env=None):
            nonlocal serial
            serial += 1
            child = supervisor.start(arguments, billing / "backend", env if child_env is None else child_env, scratch / f"stage-{serial}.log")
            if supervisor.wait(child, timeout, required) != 0:
                raise Failure("Owned acceptance stage failed; private diagnostics withheld")
        stage = "private-localhost-certificate"
        (scratch / "ca.cnf").write_text(CA_CONFIG)
        (scratch / "server.cnf").write_text(LEAF_CONFIG)
        run(["openssl", "req", "-new", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "1", "-config", str(scratch / "ca.cnf"), "-keyout", str(scratch / "ca.key"), "-out", str(scratch / "ca.pem")])
        run(["openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-sha256", "-config", str(scratch / "server.cnf"), "-keyout", str(scratch / "server.key"), "-out", str(scratch / "server.csr")])
        run(["openssl", "x509", "-req", "-sha256", "-days", "1", "-in", str(scratch / "server.csr"), "-CA", str(scratch / "ca.pem"), "-CAkey", str(scratch / "ca.key"), "-CAcreateserial", "-extfile", str(scratch / "server.cnf"), "-extensions", "extensions", "-out", str(scratch / "server.pem")])
        (scratch / "ca.key").unlink()
        stage = "compile-ci-only-overlay"
        overlay = {"Replace": {str(billing / "backend/internal/billing/owned_browser_mysql_fixture_test.go"): str(billing / "deploy/ci/browser-mysql-fixture.gotxt")}}
        (scratch / "overlay.json").write_text(json.dumps(overlay))
        run(["go", "test", "-mod=readonly", "-race", "-overlay", str(scratch / "overlay.json"), "-c", "-o", str(scratch / "fixture.test"), "./internal/billing"], timeout=360)
        stage = "real-mysql-fixture-startup"
        fixture = supervisor.start([str(scratch / "fixture.test"), "-test.v", "-test.count=1", "-test.timeout=16m", "-test.run=^TestOwnedBrowserMySQLFixture$"], billing / "backend", env, scratch / "fixture.log")
        deadline = time.monotonic() + 90
        while not (scratch / "credentials.json").exists():
            supervisor.sample()
            if fixture.poll() is not None or time.monotonic() >= deadline:
                raise Failure("Owned real MySQL fixture did not become ready")
            time.sleep(0.1)
        script = str(billing / "frontend/tests/browser-mysql.mjs")
        stage = "firefox-must-reject-untrusted-ca"
        run(["node", script, "untrusted"], timeout=90, required=(fixture,))
        stage = "trust-ca-in-private-profile-only"
        profile = "sql:" + str(scratch / "firefox-profile")
        # The first browser launch already created its own NSS database.
        run(["certutil", "-A", "-d", profile, "-n", "Aster disposable financial localhost", "-t", "C,,", "-i", str(scratch / "ca.pem")], required=(fixture,))
        run(["node", script, "trusted"], timeout=90, required=(fixture,))
        stage = "real-api-ui-acceptance"
        run(["node", script, "acceptance"], timeout=600, required=(fixture,), child_env=acceptance_environment(env, scratch))
        stage = "historical-rows-and-owned-database-cleanup"
        (scratch / "stop").touch(mode=0o600)
        if supervisor.wait(fixture, 35) != 0 or (scratch / "fixture-passed").read_text() != "real-mysql-history-and-cleanup-verified\n":
            raise Failure("Real MySQL history or owned database cleanup failed")
        log = regular(scratch / "fixture.log", 5_000_000).read_text(errors="replace")
        if "--- PASS: TestOwnedBrowserMySQLFixture " not in log or re.search(r"--- (?:SKIP|FAIL):|DATA RACE", log):
            raise Failure("Required real MySQL fixture must pass without skipping")
        stage = "validate-public-evidence"
        export_public(scratch, output)
        passed = True
    except (Failure, OSError, ValueError, KeyError, TypeError):
        # Do not expose raw subprocess/driver/credential-bearing exceptions.
        passed = False
    finally:
        # Bounded cleanup steps are independent; one failure cannot skip private
        # key/profile removal or owned-process termination. Never touch an output
        # directory whose exclusive mkdir did not succeed in this invocation.
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        if fixture is not None and scratch is not None:
            try:
                if fixture.poll() is None:
                    (scratch / "stop").touch(mode=0o600)
                    supervisor.wait(fixture, 35)
            except Exception:
                cleanup_ok = False
        if supervisor is not None:
            try:
                cleanup_ok = supervisor.cleanup() and cleanup_ok
            except Exception:
                cleanup_ok = False
        if scratch is not None:
            try:
                browser_progress = read_browser_progress(scratch)
            except (Failure, OSError, ValueError, KeyError, TypeError):
                progress_rejected = True
                passed = False
            try:
                shutil.rmtree(scratch)
            except Exception:
                cleanup_ok = False
        passed = passed and cleanup_ok
        if output_owned:
            summary = {"schema": "aster-billing-browser-mysql-summary-v1", "passed": passed, "stage": "complete" if passed else stage,
                       "owned_process_cleanup": cleanup_ok, "host_trust_modified": False,
                       "actual_mysql_history_verified": passed, "owned_database_removed": passed,
                       "screenshots": SHOTS if passed else [], "financial_business_writes": 0 if passed else None,
                       "browser_progress": browser_progress, "browser_progress_rejected": progress_rejected}
            try:
                (output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
            except Exception:
                passed = False
    print(("PASS" if passed else "FAIL") + ": financial real-MySQL browser gate; stage=" + ("complete" if passed else stage))
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
