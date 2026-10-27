#!/usr/bin/env python3
"""Real process replacement acceptance inside accept-native.sh's owned fixture.

This helper runs without root. Each privileged operation targets the freshly
created CI identities/paths supplied by the parent guard. It never uses an
external SMTP address, database, host or account.
"""
import argparse
import concurrent.futures
import gzip
import hashlib
import http.cookiejar
import http.client
import importlib.util
import io
import json
import os
from pathlib import Path
import pwd
import re
import select
import signal
import ssl
import stat
import subprocess
import tarfile
import time
import urllib.error
import urllib.request


class AcceptanceFailure(RuntimeError):
    pass


class SafeDiagnostics:
    """Only fixed stages, command names, numeric status and file metadata."""
    def __init__(self):
        self.stage = "initializing"
        self.last_command = None
        self.last_api = None
        self.path_metadata = []

    def enter(self, name):
        require(re.fullmatch(r"[a-z0-9_.-]{1,96}", name) is not None, "Invalid fixed diagnostic stage")
        self.stage = name
        print("PLUGIN_NATIVE_STAGE=" + name, flush=True)

    def paths(self, values):
        for path in sorted(set(map(Path, values)), key=str):
            entry = {"path": str(path)}
            try:
                info = path.lstat()
                entry.update(uid=info.st_uid, gid=info.st_gid, mode=oct(stat.S_IMODE(info.st_mode)),
                    kind="directory" if stat.S_ISDIR(info.st_mode) else
                         "symlink" if stat.S_ISLNK(info.st_mode) else "other")
            except OSError as failure:
                entry["error_class"] = type(failure).__name__
            self.path_metadata.append(entry)
        print("PLUGIN_NATIVE_PATH_METADATA_JSON=" + json.dumps(self.path_metadata, sort_keys=True), flush=True)


DIAGNOSTICS = SafeDiagnostics()


CANARY_WRAPPER = """import os,sys
try:
 fd=os.open(sys.argv[4],os.O_RDONLY);os.set_inheritable(fd,True)
 print(str(os.getpid())+' '+str(fd),flush=True)
 os.execv(sys.argv[1],[sys.argv[1],'--root',sys.argv[2],'--policy',sys.argv[3]])
except OSError as error:
 print('CANARY_BOOTSTRAP_ERROR='+type(error).__name__,file=sys.stderr,flush=True)
 raise SystemExit(70)
"""


def start_canary_process(arguments):
    # A different UID must never depend on access to the runner's private cwd
    # or stdin. Python -I is supplied by the caller before the fixed wrapper.
    return subprocess.Popen(arguments, cwd="/", stdin=subprocess.DEVNULL,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def canary_failure_class(stderr):
    for name in ("PermissionError", "FileNotFoundError", "OSError", "IsADirectoryError"):
        if ("CANARY_BOOTSTRAP_ERROR=" + name).encode() in stderr:
            return name
    if b"not allowed to execute" in stderr or b"not in the sudoers" in stderr:
        return "SudoAuthorizationRejected"
    if b"a password is required" in stderr:
        return "SudoAuthenticationRequired"
    if b"error evaluating path" in stderr:
        return "PythonStartupPathError"
    return "UnclassifiedBootstrapFailure"


def bounded_pipe_diagnostic(stream):
    ready, _, _ = select.select([stream], [], [], 0)
    return os.read(stream.fileno(), 4096) if ready else b""


def stop_canary_process(process, pid):
    if pid is not None and process.poll() is None:
        subprocess.run(["sudo", "-n", "kill", "-TERM", str(pid)], cwd="/", capture_output=True, timeout=5)
    elif process.poll() is None:
        try:
            process.terminate()
        except PermissionError:
            subprocess.run(["sudo", "-n", "kill", "-TERM", str(process.pid)], cwd="/", capture_output=True, timeout=5)
    try:
        process.communicate(timeout=5)
    except subprocess.TimeoutExpired:
        if pid is not None:
            subprocess.run(["sudo", "-n", "kill", "-KILL", str(pid)], cwd="/", capture_output=True, timeout=5)
        if process.poll() is None:
            try:
                process.kill()
            except PermissionError:
                subprocess.run(["sudo", "-n", "kill", "-KILL", str(process.pid)], cwd="/", capture_output=True, timeout=5)
        process.communicate(timeout=5)


WRONG_UID_PROBE = """import socket,sys
s=socket.socket(socket.AF_UNIX);s.settimeout(2);s.connect(sys.argv[1])
try:
 try:
  s.sendall(b'POST /handshake HTTP/1.1\\r\\nHost: plugin.local\\r\\nContent-Type: application/json\\r\\nContent-Length: 0\\r\\n\\r\\n')
  data=s.recv(512)
 except (ConnectionResetError,BrokenPipeError): data=b''
 if data: raise SystemExit(3)
finally:s.close()
"""


def command_name(arguments):
    """Do not return env values, script source, targets or arbitrary arguments."""
    items = list(map(str, arguments))
    if items and Path(items[0]).name == "sudo":
        items.pop(0)
        while items and items[0].startswith("-"):
            option = items.pop(0)
            if option in ("-u", "-g") and items:
                items.pop(0)
    if items and Path(items[0]).name == "env":
        items.pop(0)
        while items and (items[0].startswith("-") or "=" in items[0]):
            items.pop(0)
    name = Path(items[0]).name if items else "unresolved"
    # Executable paths are restricted by the fixture. Unknown command names
    # are replaced, rather than trusting arbitrary argv text as a diagnostic.
    allowed = {"go", "git", "python3", "systemctl", "systemd-analyze", "install", "test",
               "sha256sum", "stat", "cat", "mv", "ln", "rm", "journalctl", "kill", "timeout",
               "aster-plugin-host", "smtp-1.1.0", "smtp-1.1.1"}
    if name not in allowed:
        name = "fixture-command"
    subcommands = {"go": {"build", "version"}, "git": {"rev-parse"},
                  "systemctl": {"show", "start", "stop", "restart", "kill", "reset-failed", "daemon-reload"},
                  "systemd-analyze": {"verify"}}
    operation = next((item for item in items[1:] if item in subcommands.get(name, set())), None)
    return name + (" " + operation if operation else "")


def completed_command(arguments, returncode, expected):
    DIAGNOSTICS.last_command = {"command": command_name(arguments), "expected_exit_code": expected,
                                "actual_exit_code": returncode}
    return DIAGNOSTICS.last_command


def require(condition, description):
    if not condition:
        raise AcceptanceFailure(description)


def systemctl_kill_scope(help_output):
    # systemd 249 advertises --kill-who; 255 advertises --kill-whom. Select an
    # actual capability before fault injection, never retry the signal or
    # accept a failed command as proof that a process was killed.
    require(isinstance(help_output, bytes) and len(help_output) <= 65536,
            "Systemctl help is unavailable or exceeds the fixture bound")
    for option in ("--kill-whom", "--kill-who"):
        if re.search(rb"(?m)^\s*" + option.encode() + rb"=[^\s]+(?:\s|$)", help_output):
            return option
    raise AcceptanceFailure("Systemctl exposes no supported exact kill selector")


def runner_environment():
    values = dict(line.split("=", 1) for line in Path("/etc/os-release").read_text().splitlines() if "=" in line)
    version = values.get("VERSION_ID", "").strip('"')
    require(values.get("ID", "").strip('"') == "ubuntu" and version in {"22.04", "24.04"},
            "Plugin evidence requires an actual supported Ubuntu runner")
    return "isolated Ubuntu " + version + " GitHub-hosted runner"


def command(arguments, *, expected=0, cwd=None, timeout=30):
    DIAGNOSTICS.last_command = {"command": command_name(arguments), "expected_exit_code": expected,
                                "actual_exit_code": None}
    result = subprocess.run(arguments, cwd=cwd, capture_output=True, timeout=timeout)
    diagnostic = completed_command(arguments, result.returncode, expected)
    require(result.returncode == expected, "Owned fixture command failed: " + diagnostic["command"]
            + "; expected exit " + str(expected) + ", actual exit " + str(result.returncode))
    return result.stdout


def privileged(*arguments, expected=0, timeout=30):
    return command(["sudo", "-n", *map(str, arguments)], expected=expected, timeout=timeout, cwd="/")


def fixture_package(source, scratch, version):
    """Build an independent real executable; test 1.1.1 is never published."""
    project = source / "vps-billing"
    executable = scratch / ("aster-smtp-" + version + "-linux-amd64")
    environment = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64",
                       GOENV="off", GOFLAGS="", GOWORK="off", GOTOOLCHAIN="local")
    arguments = [
        "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags",
        "-s -w -X main.version=" + version, "-o", str(executable), "./plugins/smtp/cmd/plugin",
    ]
    DIAGNOSTICS.last_command = {"command": "go build", "expected_exit_code": 0, "actual_exit_code": None}
    result = subprocess.run(arguments, cwd=project / "backend", env=environment, capture_output=True, timeout=180)
    completed_command(arguments, result.returncode, 0)
    require(result.returncode == 0, "Independent SMTP fixture compilation failed; actual exit " + str(result.returncode))
    binary = executable.read_bytes()
    require(binary.startswith(b"\x7fELF\x02\x01"), "SMTP fixture is not Linux amd64 ELF")
    require(command([str(executable), "--version"]).strip() == ("Aster SMTP plugin " + version).encode(),
            "Independent SMTP fixture version differs")
    manifest = {"schema": 1, "id": "email.smtp", "version": version,
                "protocol": "aster.smtp/1", "core_min": "0.4.0", "core_max_exclusive": "0.6.0",
                "capabilities": ["smtp.send"], "executable": "plugin",
                "sha256": hashlib.sha256(binary).hexdigest()}
    files = {"plugin": binary, "manifest.json": json.dumps(manifest, sort_keys=True).encode() + b"\n",
             "LICENSE": (source / "LICENSE").read_bytes(),
             "README.md": ("Native CI fixture SMTP " + version + "; never publish this test package.\n").encode(),
             "THIRD_PARTY_NOTICES.json": json.dumps({
                 "fixture_only": True, "dependency_scope": "Go standard library",
                 "go_version": command(["go", "version"]).decode().strip(),
             }, sort_keys=True).encode() + b"\n"}
    files["SHA256SUMS"] = "".join(hashlib.sha256(value).hexdigest() + "  " + name + "\n"
                                 for name, value in sorted(files.items())).encode()
    # Checksums describe the logical installed six-file plugin. The resources
    # archive itself deliberately contains no ELF; the raw binary has its own
    # independently approved outer SHA-256 pin.
    del files["plugin"]
    base = "aster-smtp-" + version + "-linux-amd64-resources"
    package = scratch / (base + ".tar.gz")
    with package.open("wb") as output, gzip.GzipFile(fileobj=output, mode="wb", mtime=0) as zipped:
        with tarfile.open(fileobj=zipped, mode="w", format=tarfile.GNU_FORMAT) as archive:
            directory = tarfile.TarInfo(base)
            directory.type = tarfile.DIRTYPE
            directory.mode = 0o755
            archive.addfile(directory)
            for name, value in sorted(files.items()):
                member = tarfile.TarInfo(base + "/" + name)
                member.mode = 0o644
                member.size = len(value)
                archive.addfile(member, io.BytesIO(value))
    return package, executable, manifest


class API:
    def __init__(self, port, ca, accounts):
        self.origin = "https://localhost:" + str(port)
        context = ssl.create_default_context(cafile=ca)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        self.jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=context), urllib.request.HTTPCookieProcessor(self.jar))
        self.csrf = ""
        self.accounts = accounts
        self.request("/api/v1/session", expected=200)
        self.request("/api/v1/auth/login", "POST", {
            "email": "admin@native-ci.invalid", "password": accounts["admin"]}, expected=200)
        self.request("/api/v1/session", expected=200)

    def request(self, path, method="GET", body=None, expected=200):
        headers = {"Accept": "application/json"}
        if method == "POST":
            headers.update({"Content-Type": "application/json", "Origin": self.origin,
                            "X-CSRF-Token": self.csrf})
        request = urllib.request.Request(self.origin + path, method=method, headers=headers,
            data=json.dumps(body).encode() if body is not None else None)
        try:
            response = self.opener.open(request, timeout=15)
        except urllib.error.HTTPError as failure:
            response = failure
        with response:
            DIAGNOSTICS.last_api = {"method": method, "path": path, "expected_status": expected,
                                    "actual_status": response.status}
            raw = response.read(65537)
            require(len(raw) <= 65536, "API response exceeded fixture bound")
            require(all(str(self.accounts[key]).encode() not in raw
                        for key in ("smtp_username", "smtp_password", "session", "admin")),
                    "API response exposed a fixture secret")
            require(response.status == expected, "API acceptance status differs for " + path
                    + "; expected " + str(expected) + ", actual " + str(response.status))
        value = json.loads(raw)
        if path == "/api/v1/session" and expected == 200:
            self.csrf = value["data"]["csrf_token"]
        return value.get("data", value)


class NativePluginAcceptance:
    def __init__(self, source, evidence):
        self.source, self.evidence = source, evidence
        self.project = source / "vps-billing"
        def supplied(name):
            return os.environ["ASTER_PLUGIN_" + name]
        self.opt, self.config, self.run = (Path(supplied(name)) for name in ("OPT", "CONFIG", "RUN"))
        self.scratch = Path(supplied("SCRATCH"))
        self.api_unit, self.worker_unit = supplied("API_UNIT"), supplied("WORKER_UNIT")
        self.api_user, self.worker_user, self.web_user = (supplied(name) for name in ("API_USER", "WORKER_USER", "WEB_USER"))
        self.group = supplied("SOCKET_GROUP")
        self.port = int(supplied("HTTPS_PORT"))
        self.root = self.opt / "plugins"
        self.policy = self.config / "plugin-policy.json"
        self.socket = self.run / "smtp" / "smtp.sock"
        self.core = self.opt / "releases/a/bin/aster-api"
        self.host = self.opt / "releases/a/bin/aster-plugin-host"
        self.installer = self.project / "deploy/scripts/plugin-manager.py"
        self.worker_file = Path("/etc/systemd/system") / self.worker_unit
        self.checks = []
        self.stages = []
        self.fixture = None
        self.api = None
        self.initial_settings = None
        DIAGNOSTICS.enter("validate_owned_fixture_contract")
        DIAGNOSTICS.paths(["/", "/opt", "/etc", "/run", self.opt, self.config, self.run, self.root,
                          self.scratch.parent, self.scratch])
        self.validate_contract()
        self.kill_scope = systemctl_kill_scope(command([
            "env", "LC_ALL=C", "SYSTEMD_PAGER=cat", "SYSTEMD_COLORS=0", "systemctl", "--help"], timeout=5))
        self.api_uid = pwd.getpwnam(self.api_user).pw_uid
        self.worker_uid = pwd.getpwnam(self.worker_user).pw_uid
        require(self.api_uid != self.worker_uid and min(self.api_uid, self.worker_uid) > 0,
                "Core and plugin must have distinct non-root UIDs")
        self.baseline_hash = hashlib.sha256(self.core.read_bytes()).hexdigest()
        self.baseline_pid = self.pid(self.api_unit)
        self.accounts = json.loads((self.scratch / "accounts.json").read_text())
        DIAGNOSTICS.enter("authenticate_existing_https_api")
        self.api = API(self.port, str(self.scratch / "ca.pem"), self.accounts)

    def kill_worker_main(self):
        require(re.fullmatch(r"aster-ci-(?:smtp|payment)-[0-9]+-[0-9]+-[0-9]+\.service", self.worker_unit) is not None
                and self.kill_scope in {"--kill-who", "--kill-whom"}, "Refusing a non-owned plugin crash target")
        privileged("systemctl", "kill", self.kill_scope + "=main", "--signal=KILL", self.worker_unit)

    def validate_contract(self):
        identity = os.environ["GITHUB_RUN_ID"] + "-" + os.environ["GITHUB_RUN_ATTEMPT"] + "-"
        match = re.fullmatch(r"/opt/aster-ci-(\d+-\d+-\d+)", str(self.opt))
        require(match is not None and match[1].startswith(identity), "Owned fixture installation identity differs")
        suffix = match[1]
        require(self.config == Path("/etc/aster-ci-" + suffix) and self.run == Path("/run/aster-ci-" + suffix),
                "Owned fixture configuration/runtime paths differ")
        require(self.api_unit == "aster-ci-api-" + suffix + ".service"
                and self.worker_unit == "aster-ci-smtp-" + suffix + ".service", "Owned fixture units differ")
        for name in (self.api_user, self.worker_user, self.web_user, self.group):
            require(re.fullmatch(r"ast[a-z]+[0-9]+", name) is not None, "Owned fixture identity is invalid")
        for path in (self.opt, self.config, self.run, self.root):
            info = path.lstat()
            require(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and not info.st_mode & 0o022,
                    "Owned fixture path is not root-protected")
        require(self.scratch.is_dir() and self.scratch.stat().st_uid == os.geteuid()
                and self.scratch.stat().st_mode & 0o077 == 0, "Private runner scratch differs")
        require(not self.worker_file.exists() and not self.worker_file.is_symlink(),
                "Refusing to overwrite an existing plugin service")
        require(1024 <= self.port <= 65535 and len(str(self.socket)) <= 100, "Fixture ports/socket are invalid")

    def pid(self, unit):
        value = privileged("systemctl", "show", unit, "-p", "MainPID", "--value").strip()
        require(value.isdigit() and int(value) > 1, "Owned service has no running process")
        return int(value)

    def unchanged_core(self, stage):
        DIAGNOSTICS.enter(stage)
        require(self.pid(self.api_unit) == self.baseline_pid, "Core PID changed during " + stage)
        digest = hashlib.sha256(self.core.read_bytes()).hexdigest()
        require(digest == self.baseline_hash, "Core executable changed during " + stage)
        live_digest = privileged("sha256sum", "/proc/" + str(self.baseline_pid) + "/exe").decode().split()[0]
        require(live_digest == self.baseline_hash, "Live core executable changed during " + stage)
        self.api.request("/healthz")
        self.api.request("/readyz")
        self.api.request("/api/v1/admin/orders")
        self.stages.append({"stage": stage, "core_pid": self.baseline_pid, "core_sha256": digest,
                            "live_core_sha256": live_digest,
                            "healthy": True, "orders_api": True})

    def manager(self, *arguments, succeeds=True):
        action = str(arguments[0]) if arguments else "missing"
        DIAGNOSTICS.enter("plugin_manager_" + action)
        execution = ["sudo", "-n", "python3", str(self.installer), "--root", str(self.root),
            "--policy", str(self.policy), "--core-version", "0.5.0", *map(str, arguments)]
        expected = 0 if succeeds else "nonzero"
        DIAGNOSTICS.last_command = {"command": "plugin-manager " + action, "expected_exit_code": expected,
                                    "actual_exit_code": None}
        result = subprocess.run(execution,
            capture_output=True, timeout=30)
        DIAGNOSTICS.last_command["actual_exit_code"] = result.returncode
        require((result.returncode == 0) == succeeds, "Plugin-manager " + action
                + " acceptance outcome differs; expected exit " + str(expected) + ", actual exit " + str(result.returncode))
        return json.loads(result.stdout) if succeeds else None

    def revision(self):
        return json.loads(self.policy.read_bytes())["revision"] if self.policy.exists() else 0

    def selection(self, version):
        self.manager("select", "--id", "email.smtp", "--version", version, "--worker-uid", self.worker_uid,
                     "--client-uid", self.api_uid, "--socket", self.socket, "--target",
                     "localhost:" + str(self.fixture.port), "--revision", self.revision())

    def check(self, version=None):
        result = self.api.request("/api/v1/admin/plugins/check", "POST", {}, expected=200 if version else 503)
        if version:
            require(result["version"] == version and result["protocol"] == "aster.smtp/1",
                    "Active plugin handshake version differs")
            require(result["sha256"] == self.manifests[version]["sha256"], "Running plugin identity differs")
        return result

    def wait_worker(self, version, previous=None):
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                pid = self.pid(self.worker_unit)
                if previous is None or pid != previous:
                    self.check(version)
                    return pid
            except (AcceptanceFailure, urllib.error.URLError, ConnectionError):
                pass
            time.sleep(0.2)
        raise AcceptanceFailure("Approved plugin did not become ready after bounded systemd restart")

    def start_fixture_and_unit(self):
        DIAGNOSTICS.enter("start_loopback_tls_fixture")
        spec = importlib.util.spec_from_file_location("native_loopback_smtp", Path(__file__).with_name("smtp-loopback-fixture.py"))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.fixture = module.LoopbackSMTP(str(self.scratch / "server.pem"), str(self.scratch / "server.key"),
                                          self.accounts["smtp_username"], self.accounts["smtp_password"]).start()
        privileged("install", "-m", "644", self.scratch / "ca.pem", self.config / "smtp-test-ca.pem")
        template = (self.project / "deploy/systemd/aster-smtp.service").read_text()
        unit = (template.replace("/opt/aster", str(self.opt)).replace("/etc/aster", str(self.config))
                .replace("/run/aster-smtp", str(self.run / "smtp"))
                .replace(str(self.config / "plugins.json"), str(self.policy))
                .replace("User=aster-smtp", "User=" + self.worker_user)
                .replace("Group=aster-plugins", "Group=" + self.group))
        # Only the worker mount namespace trusts the one-day private fixture CA;
        # the runner/global CA store and actual plugin TLS checks are unchanged.
        unit = unit.replace("[Install]", "BindReadOnlyPaths=" + str(self.config / "smtp-test-ca.pem")
                            + ":/etc/ssl/certs/ca-certificates.crt\n"
                            + "Environment=ASTER_CREDENTIAL_SENTINEL=synthetic-not-a-secret\n\n[Install]")
        # RuntimeDirectory is rendered explicitly under the already-owned run
        # root; the production service continues to own its normal directory.
        unit = "\n".join(line for line in unit.splitlines() if not line.startswith("RuntimeDirectory")) + "\n"
        unit = unit.replace(str(self.opt / "current/bin/aster-plugin-host"), str(self.host))
        local = self.scratch / "smtp.service"
        local.write_text(unit)
        DIAGNOSTICS.enter("install_owned_smtp_systemd_unit")
        privileged("install", "-m", "644", local, self.worker_file)
        DIAGNOSTICS.enter("verify_owned_smtp_systemd_unit")
        privileged("systemd-analyze", "verify", self.worker_file)
        DIAGNOSTICS.enter("reload_owned_smtp_systemd_unit")
        privileged("systemctl", "daemon-reload")

    def verify_worker_isolation(self, version, pid):
        DIAGNOSTICS.enter("verify_running_worker_isolation_" + version)
        properties = privileged("systemctl", "show", self.worker_unit, "-p", "User", "-p", "Group",
            "-p", "NoNewPrivileges", "-p", "ProtectSystem", "-p", "StandardOutput", "-p", "StandardError",
            "-p", "CapabilityBoundingSet", "-p", "ExecStart").decode()
        require("NoNewPrivileges=yes" in properties and "ProtectSystem=strict" in properties
                and "StandardOutput=null" in properties and "StandardError=null" in properties,
                "Plugin service hardening/output policy is inactive")
        require(str(self.host) in properties, "Plugin service did not start through verified descriptor host")
        status = privileged("cat", "/proc/" + str(pid) + "/status").decode()
        require(re.search(r"^Uid:\s+" + str(self.worker_uid) + r"\s", status, re.M) is not None
                and re.search(r"^NoNewPrivs:\s+1$", status, re.M) is not None
                and re.search(r"^CapEff:\s+0+$", status, re.M) is not None,
                "Running plugin UID/capabilities/no-new-privileges differ")
        digest = privileged("sha256sum", "/proc/" + str(pid) + "/exe").decode().split()[0]
        require(digest == self.manifests[version]["sha256"], "Host did not exec the pinned approved binary")
        environment = privileged("cat", "/proc/" + str(pid) + "/environ").split(b"\x00")
        require(set(value.split(b"=", 1)[0] for value in environment if value) <= {b"PATH", b"LANG", b"TZ", b"TMPDIR"},
                "Plugin inherited an unapproved environment entry")
        require(all(self.accounts[key].encode() not in b"\x00".join(environment)
                    for key in ("session", "smtp_username", "smtp_password", "runtime")),
                "Plugin process environment inherited a fixture credential")
        for path in (self.config / "api.env", self.config / "secrets/session", self.config / "secrets/smtp-key",
                     self.config / "secrets/runtime.service.dsn"):
            privileged("-u", self.worker_user, "test", "-r", path, expected=1)
        for path in (self.root, self.policy, self.root / "email.smtp" / version / "plugin"):
            privileged("-u", self.api_user, "test", "-w", path, expected=1)
        socket_info = privileged("stat", "-c", "%F %u %a", self.socket).decode().strip().split()
        require(socket_info[0] == "socket" and int(socket_info[1]) == self.worker_uid
                and int(socket_info[2], 8) & 0o007 == 0,
                "Plugin socket ownership/permissions differ")
        # This caller has actual filesystem access through the socket group,
        # but a different peer UID. Failure therefore exercises SO_PEERCRED.
        privileged("-u", self.web_user, "-g", self.group, "env", "-i", "PATH=/usr/bin:/bin",
                   "python3", "-I", "-c", WRONG_UID_PROBE, self.socket)
        self.checks.append("different_uid_systemd_verified_fd_exec_scrubbed_environment_peer_uid_and_secret_denial")

    def unknown_discovery(self):
        DIAGNOSTICS.enter("create_never_execute_unknown_discovery_fixture")
        marker = self.run / "smtp" / "unknown-executed"
        empty = self.scratch / "empty-marker"
        empty.write_bytes(b"")
        privileged("install", "-o", "root", "-g", self.group, "-m", "660", empty, marker)
        directory = self.root / "email.trap" / "9.9.9"
        privileged("install", "-d", "-m", "755", directory)
        trap = self.scratch / "never-execute"
        trap.write_text("#!/bin/sh\nprintf 'EXECUTED' >> '" + str(marker) + "'\n")
        manifest = dict(self.manifests["1.1.0"], id="email.trap", version="9.9.9",
                        sha256=hashlib.sha256(trap.read_bytes()).hexdigest())
        local = self.scratch / "trap-manifest.json"
        local.write_text(json.dumps(manifest))
        privileged("install", "-m", "755", trap, directory / "plugin")
        privileged("install", "-m", "644", local, directory / "manifest.json")
        DIAGNOSTICS.enter("discover_unknown_file_without_execution")
        discovered = self.api.request("/api/v1/admin/plugins")
        require(any(item["id"] == "email.trap" for item in discovered["items"]),
                "Passive discovery did not encounter the unknown fixture")
        self.check()
        require(privileged("stat", "-c", "%s", marker).strip() == b"0", "Discovery executed an unknown file")
        privileged("test", "-S", self.socket, expected=1)
        self.checks.append("passive_discovery_unknown_executable_never_executed")

    def inherited_descriptor_canary(self):
        DIAGNOSTICS.enter("verified_fd_exec_inherited_descriptor_canary")
        canary = self.config / "descriptor-canary"
        local = self.scratch / "descriptor-canary"
        local.write_text("nonsecret inherited descriptor must disappear\n")
        privileged("install", "-m", "644", local, canary)
        process = start_canary_process(["sudo", "-n", "-u", self.worker_user, "-g", self.group,
            "env", "-i", "PATH=/usr/bin:/bin", "python3", "-I", "-c", CANARY_WRAPPER, str(self.host),
            str(self.root), str(self.policy), str(canary)])
        pid = None
        try:
            ready, _, _ = select.select([process.stdout], [], [], 3)
            require(bool(ready), "Inherited descriptor canary did not launch")
            values = process.stdout.readline(128).split()
            if len(values) != 2 or not all(value.isdigit() for value in values):
                try:
                    returncode = process.wait(timeout=0.5)
                except subprocess.TimeoutExpired:
                    returncode = None
                failure_class = canary_failure_class(bounded_pipe_diagnostic(process.stderr)) if returncode is not None else "BootstrapStillRunning"
                DIAGNOSTICS.last_command = {"command": "python3 isolated-canary", "expected_exit_code": 0,
                    "actual_exit_code": returncode, "bootstrap_failure_class": failure_class,
                    "identity_field_count": len(values)}
                raise AcceptanceFailure("Inherited descriptor canary identity differs; process exit "
                                        + str(returncode) + "; bootstrap class " + failure_class)
            pid, descriptor = map(int, values)
            require(descriptor > 2, "Inherited descriptor canary was not an extra descriptor")
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                try:
                    self.check("1.1.0")
                    break
                except (AcceptanceFailure, urllib.error.URLError):
                    time.sleep(0.1)
            else:
                raise AcceptanceFailure("Verified host could not exec with inherited descriptor canary")
            inspect = """import os,sys
for fd in os.listdir('/proc/'+sys.argv[1]+'/fd'):
 try: print(os.readlink('/proc/'+sys.argv[1]+'/fd/'+fd))
 except FileNotFoundError: pass
"""
            inherited = privileged("python3", "-c", inspect, pid).decode().splitlines()
            require(str(canary) not in inherited, "Plugin inherited an unapproved open file descriptor")
            self.checks.append("native_verified_fd_exec_closes_inherited_non_cloexec_canary")
            self.unchanged_core("inherited_descriptor_canary_rejected")
        finally:
            stop_canary_process(process, pid)

    def restore_settings(self):
        if self.api and self.initial_settings:
            current = self.api.request("/api/v1/admin/email-settings")
            body = {key: self.initial_settings[key] for key in
                    ("enabled", "host", "port", "tls_mode", "from_address", "from_name")}
            body.update(revision=current["revision"], enabled=False,
                        username=self.accounts["smtp_username"], password=self.accounts["smtp_password"])
            self.api.request("/api/v1/admin/email-settings", "POST", body)

    def run_all(self):
        self.manifests = {}
        packages = {}
        package_directory = self.opt / "plugin-test-packages"
        DIAGNOSTICS.enter("prepare_protected_test_package_directory")
        privileged("install", "-d", "-m", "755", package_directory)
        for version in ("1.1.0", "1.1.1"):
            DIAGNOSTICS.enter("compile_independent_smtp_fixture_" + version)
            built_package, built_binary, self.manifests[version] = fixture_package(self.source, self.scratch, version)
            resources, executable = package_directory / built_package.name, package_directory / built_binary.name
            privileged("install", "-m", "644", built_package, resources)
            privileged("install", "-m", "755", built_binary, executable)
            packages[version] = (resources, executable)
        require(self.manifests["1.1.0"]["sha256"] != self.manifests["1.1.1"]["sha256"],
                "Independent plugin upgrade did not change the plugin executable")
        self.start_fixture_and_unit()
        self.unchanged_core("before_plugin_install")
        self.unknown_discovery()
        resources, executable = packages["1.1.0"]
        metadata_pin = hashlib.sha256(resources.read_bytes()).hexdigest()
        binary_pin = hashlib.sha256(executable.read_bytes()).hexdigest()
        # Both independent pins must be approved before any version is created.
        self.manager("install", resources, "--sha256", "0" * 64, "--binary", executable,
                     "--binary-sha256", binary_pin, succeeds=False)
        self.manager("install", resources, "--sha256", metadata_pin, "--binary", executable,
                     "--binary-sha256", "0" * 64, succeeds=False)
        privileged("test", "-e", self.root / "email.smtp/1.1.0", expected=1)
        self.checks.append("raw_plugin_resources_and_binary_dual_pins_reject_mismatch_before_install")
        for version, (resources, executable) in packages.items():
            self.manager("install", resources, "--sha256", hashlib.sha256(resources.read_bytes()).hexdigest(),
                         "--binary", executable, "--binary-sha256", hashlib.sha256(executable.read_bytes()).hexdigest())
        resources, executable = packages["1.1.0"]
        self.manager("install", resources, "--sha256", metadata_pin,
                     "--binary", executable, "--binary-sha256", binary_pin, succeeds=False)
        require(not self.policy.exists(), "Installing a package activated a selection")
        privileged("test", "-S", self.socket, expected=1)
        self.unchanged_core("install_without_activation")
        self.selection("1.1.0")
        DIAGNOSTICS.enter("reject_execution_of_disabled_selection")
        disabled = self.api.request("/api/v1/admin/plugins")["smtp"]
        require(disabled["status"] == "disabled", "Selecting a plugin implicitly enabled it")
        privileged("-u", self.worker_user, "env", "-i", "PATH=/usr/bin:/bin", "timeout", "2", self.host,
                   "--root", self.root, "--policy", self.policy, expected=1)
        privileged("test", "-S", self.socket, expected=1)
        self.checks.append("install_immutable_no_overwrite_selection_disabled_explicit_enable")
        self.manager("enable", "--revision", self.revision())
        self.inherited_descriptor_canary()
        DIAGNOSTICS.enter("start_approved_smtp_1.1.0_unit")
        privileged("systemctl", "start", self.worker_unit)
        pid = self.wait_worker("1.1.0")
        self.verify_worker_isolation("1.1.0", pid)
        self.unchanged_core("smtp_1.1.0_active")
        self.initial_settings = self.api.request("/api/v1/admin/email-settings")
        DIAGNOSTICS.enter("save_enabled_synthetic_smtp_configuration")
        body = {key: self.initial_settings[key] for key in ("host", "port", "tls_mode", "from_address", "from_name")}
        body.update(revision=self.initial_settings["revision"], enabled=True, host="localhost", port=self.fixture.port,
                    tls_mode="implicit_tls", username=self.accounts["smtp_username"], password=self.accounts["smtp_password"])
        settings = self.api.request("/api/v1/admin/email-settings", "POST", body)
        DIAGNOSTICS.enter("send_first_fixed_synthetic_tls_message")
        result = self.api.request("/api/v1/admin/email-settings/test", "POST", {
            "revision": settings["revision"], "to": "operator@native-ci.invalid"})
        require(result["status"] == "accepted" and self.fixture.report()["accepted"] == 1,
                "Independent plugin did not complete the verified loopback TLS SMTP transaction")
        first_sent = time.monotonic()
        self.checks.append("real_loopback_tls_authenticated_fixed_message_accepted_no_external_relay")
        self.selection("1.1.1")
        privileged("systemctl", "stop", self.worker_unit)
        self.manager("enable", "--revision", self.revision())
        privileged("systemctl", "start", self.worker_unit)
        self.wait_worker("1.1.1")
        self.unchanged_core("smtp_1.1.1_independent_upgrade")
        self.manager("rollback", "--version", "1.1.0", "--revision", self.revision())
        self.check()  # The still-running newer identity cannot satisfy old policy.
        self.unchanged_core("rollback_rejects_previous_running_identity")
        privileged("systemctl", "restart", self.worker_unit)
        self.wait_worker("1.1.0")
        # Planned operator install/upgrade/rollback starts are distinct from
        # the crash-recovery burst exercise. Keep the production restart limits
        # and explicitly reset only this owned unit's start counter here.
        privileged("systemctl", "reset-failed", self.worker_unit)
        self.unchanged_core("smtp_1.1.0_independent_rollback")
        # Symlinked approval and modified installed files fail closed while the
        # API PID and unrelated authenticated business routes remain healthy.
        saved_policy = self.config / "plugin-policy.saved"
        privileged("mv", self.policy, saved_policy)
        privileged("ln", "-s", saved_policy, self.policy)
        try:
            self.check()
            self.unchanged_core("symlink_policy_rejected")
        finally:
            privileged("rm", self.policy)
            privileged("mv", saved_policy, self.policy)
        binary = self.root / "email.smtp/1.1.0/plugin"
        original = self.scratch / "approved-plugin-original"
        original.write_bytes(binary.read_bytes())
        altered = self.scratch / "modified-plugin"
        altered.write_bytes(original.read_bytes() + b"invalid-integrity-fixture")
        privileged("install", "-m", "755", altered, binary)
        try:
            self.check()
            self.unchanged_core("modified_binary_rejected")
        finally:
            privileged("install", "-m", "755", original, binary)
        self.check("1.1.0")
        self.checks.append("approval_symlink_and_modified_hash_rejected_while_core_healthy")
        # Respect the real administrator 30-second rate limit; keep verifying
        # the unchanged live core during the bounded wait.
        while time.monotonic() - first_sent < 30.5:
            self.api.request("/healthz")
            time.sleep(0.2)
        self.fixture.data_received.clear()
        self.fixture.hold_reply.set()
        crash_pid = self.pid(self.worker_unit)
        settings = self.api.request("/api/v1/admin/email-settings")
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            outcome = pool.submit(self.api.request, "/api/v1/admin/email-settings/test", "POST", {
                "revision": settings["revision"], "to": "operator@native-ci.invalid"}, 502)
            require(self.fixture.data_received.wait(8), "Crash fixture never reached the SMTP DATA boundary")
            DIAGNOSTICS.enter("inject_smtp_crash_after_data")
            self.kill_worker_main()
            response = outcome.result(timeout=15)
            require(response["error"]["code"] == "SMTP_DELIVERY_UNCERTAIN",
                    "Plugin crash after DATA was not reported conservatively")
        self.fixture.release_reply.set()
        new_pid = self.wait_worker("1.1.0", previous=crash_pid)
        require(new_pid != crash_pid and self.fixture.report()["accepted"] == 2,
                "Plugin crash recovered incorrectly or replayed a synthetic message")
        self.unchanged_core("plugin_crash_restart_no_core_restart_no_delivery_retry")
        self.checks.append("post_data_crash_uncertain_no_auto_retry_systemd_restart_core_survives")
        self.manager("disable", "--revision", self.revision())
        privileged("systemctl", "stop", self.worker_unit)
        self.check()
        self.restore_settings()
        self.unchanged_core("disabled_plugin_core_remains_healthy")
        marker = self.run / "smtp/unknown-executed"
        require(privileged("stat", "-c", "%s", marker).strip() == b"0",
                "An unknown executable ran during lifecycle checks")
        # Journald is read only into memory; do not publish arbitrary log lines.
        logs = privileged("journalctl", "-u", self.api_unit, "-u", self.worker_unit,
                          "--no-pager", "--output=cat", timeout=15)
        require(all(self.accounts[key].encode() not in logs
                    for key in ("smtp_username", "smtp_password", "session", "runtime", "admin")),
                "Native service journal exposed a fixture credential")
        self.checks.append("journal_and_api_responses_contain_no_fixture_credentials")
        report = {"suite": "native-independent-runtime-plugins", "source_commit": command([
            "git", "-C", str(self.source), "rev-parse", "HEAD"]).decode().strip(),
            "environment": runner_environment(), "systemctl_kill_selector": self.kill_scope, "checks": self.checks,
            "checks_passed": len(self.checks), "stages": self.stages,
            "core_binary_sha256": self.baseline_hash, "core_live_pid_unchanged": True,
            "core_recompiled_for_plugin_install_upgrade_rollback": False,
            "core_restarted_for_plugin_install_upgrade_rollback_crash": False,
            "core_uid": self.api_uid, "plugin_uid": self.worker_uid,
            "plugin_versions_exercised": ["1.1.0", "1.1.1", "1.1.0"],
            "plugin_binary_sha256": {version: manifest["sha256"] for version, manifest in self.manifests.items()},
            "test_only_unpublished_upgrade_version": "1.1.1", "smtp_fixture": self.fixture.report(),
            "plugin_install_input": "separate raw ELF and resources archive with independent SHA-256 pins",
            "plugin_process_restarted_after_crash": True, "external_email_or_payment": False,
            "production_deployment": False, "skipped": 0, "failures": 0}
        (self.evidence / "plugin-native.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
        print("PLUGIN_NATIVE_JSON=" + json.dumps(report, sort_keys=True))

    def close(self):
        # The parent owns and removes unit/account/path resources. This helper
        # stops its worker and socket fixture even if a negative check fails.
        subprocess.run(["sudo", "-n", "systemctl", "stop", self.worker_unit], capture_output=True, timeout=20)
        if self.fixture:
            self.fixture.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    acceptance = None
    try:
        acceptance = NativePluginAcceptance(args.source, args.evidence)
        acceptance.run_all()
    except (AcceptanceFailure, OSError, ValueError, KeyError, subprocess.TimeoutExpired,
            urllib.error.URLError, http.client.HTTPException, concurrent.futures.TimeoutError) as failure:
        # Do not serialize exception details or command outputs, which may
        # contain a fixture credential. Failure evidence is deliberately fixed.
        diagnostic = {"suite": "native-independent-runtime-plugins", "success": False,
                      "reason": str(failure) if isinstance(failure, AcceptanceFailure) else type(failure).__name__,
                      "completed_checks": acceptance.checks if acceptance else [],
                      "completed_stages": acceptance.stages if acceptance else [],
                      "last_stage": DIAGNOSTICS.stage, "last_command": DIAGNOSTICS.last_command,
                      "last_api": DIAGNOSTICS.last_api, "path_metadata": DIAGNOSTICS.path_metadata}
        (args.evidence / "plugin-native-failure.json").write_text(json.dumps(diagnostic) + "\n")
        print("PLUGIN_NATIVE_FAILURE_JSON=" + json.dumps(diagnostic, sort_keys=True), flush=True)
        raise SystemExit("FAIL: native runtime plugin acceptance did not pass")
    finally:
        if acceptance:
            acceptance.close()


if __name__ == "__main__":
    main()
