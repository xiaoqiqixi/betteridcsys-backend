#!/usr/bin/env python3
"""Disposable real-stack acceptance. Run privileged operations only on Actions.

All persistent input is pinned in E2E_STACK.json. Runtime credentials and raw
diagnostics stay in a private owned directory and are removed on every exit.
"""
import hashlib
import hmac
import http.cookiejar
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import re
import select
import secrets
import shutil
import signal
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[4]
HERE = Path(__file__).resolve().parent
ARTIFACTS = ROOT / "artifacts"
DOWNLOADS = ARTIFACTS / "e2e-downloads"
BIN = ARTIFACTS / "e2e-bin"


class API:
    def __init__(self, stack):
        self.origin = "https://localhost:19445"
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=stack.context), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.csrf = ""

    def call(self, path, method="GET", body=None):
        headers = {"Accept": "application/json", "Origin": self.origin}
        if method != "GET":
            headers.update({"Content-Type": "application/json", "X-CSRF-Token": self.csrf, "Idempotency-Key": uuid.uuid4().hex})
        request = urllib.request.Request(self.origin + "/api/v1" + path, method=method,
            data=json.dumps(body).encode() if body is not None else None, headers=headers)
        try:
            with self.opener.open(request, timeout=30) as response:
                result = json.load(response)
        except urllib.error.HTTPError as error:
            # Never expose request credentials or email verification material.
            result = json.loads(error.read(65536))
            raise RuntimeError(f"finance {method} {path}: HTTP {error.code}: {result.get('error', {}).get('code', 'unknown')}") from None
        value = result["data"]
        if path == "/session":
            self.csrf = value["csrf_token"]
        return value

    def login(self, email, password):
        self.call("/session")
        self.call("/auth/login", "POST", {"email": email, "password": password})
        self.call("/session")


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def digest(path):
    value = hashlib.sha256()
    with Path(path).open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def pins():
    value = json.loads((ROOT / "E2E_STACK.json").read_text())
    require(value["schema"] == 1 and value["integration"] in ("core", "server.aster-cloud"), "invalid stack pin")
    for component in ("cloud", "frontend"):
        p = value[component]
        require(p["repository"] == "xiaoqiqixi/betteridcsys-" + component and re.fullmatch("[a-f0-9]{40}", p["source_sha"]), "unreviewed source")
    require(len(value["images"]) == 2, "two distinct approved images required")
    require(len({v["sha256"] for v in value["images"]}) == 2, "reinstall must change image bytes")
    for image in value["images"]:
        require(re.fullmatch(r"https://cloud-images\.ubuntu\.com/minimal/releases/(noble|jammy)/release-[0-9]{8}/ubuntu-[0-9.]+-minimal-cloudimg-amd64\.img", image["url"]) and re.fullmatch("[a-f0-9]{64}", image["sha256"]), "unreviewed guest image")
    return value


def download(url, target, sha, size=None):
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.is_file() and digest(target) == sha and (size is None or target.stat().st_size == size):
        return
    temporary = target.with_suffix(".partial")
    try:
        with urllib.request.urlopen(url, timeout=90) as source, temporary.open("wb") as output:
            require(source.url.startswith("https://"), "download downgraded TLS")
            count = 0
            while block := source.read(1024 * 1024):
                count += len(block)
                require(count <= (size or 1024 * 1024 * 1024), "download exceeds pinned bound")
                output.write(block)
        require(digest(temporary) == sha and (size is None or count == size), "download checksum mismatch")
        temporary.replace(target)
    finally:
        temporary.unlink(missing_ok=True)


def mysql_manifest():
    value = json.loads((HERE.parent / "mysql-distribution.json").read_text())
    require(value["version"].startswith("8.4.") and value["distribution"] == "ubuntu-24.04", "MySQL 8.4 pin required")
    return value


def download_inputs():
    for image in pins()["images"]:
        download(image["url"], DOWNLOADS / (image["id"] + ".img"), image["sha256"])
    manifest = mysql_manifest()
    for package in manifest["packages"]:
        name = package["name"] + "_" + manifest["package_version"] + "_amd64.deb"
        download("https://repo.mysql.com/apt/ubuntu/pool/mysql-8.4-lts/m/mysql-community/" + name,
                 DOWNLOADS / name, package["sha256"], package["size"])


class Stack:
    def __init__(self):
        require(os.geteuid() == 0 and os.environ.get("GITHUB_ACTIONS") == "true" and sys.platform == "linux", "real-stack runner requires disposable Linux Actions root")
        run = os.environ.get("GITHUB_RUN_ID", "") + "-" + os.environ.get("GITHUB_RUN_ATTEMPT", "")
        require(re.fullmatch(r"[0-9]+-[0-9]+", run), "invalid Actions identity")
        # VM disks, snapshots and MySQL need disk-backed capacity, not /run tmpfs.
        self.root = Path("/var/lib/aster-e2e-" + run)
        self.root.mkdir(mode=0o755)
        self.private = self.root / "private"
        self.private.mkdir(mode=0o700)
        self.report = ARTIFACTS / "e2e-report"
        self.report.mkdir(parents=True, exist_ok=True)
        self.processes = []
        self.secrets = []
        self.phase = "initialize"
        self.counter = 0
        self.units = []
        self.scheduler_at = datetime.now(timezone.utc).replace(second=0, microsecond=0) + timedelta(minutes=2)
        self.env = dict(os.environ, SSL_CERT_FILE=str(self.root / "ca.crt"), SSL_CERT_DIR=str(self.root / "empty-ca"))
        self.pin = pins()
        self.evidence = {"schema": 1, "finance": os.environ["GITHUB_SHA"], "cloud": self.pin["cloud"]["source_sha"], "frontend": self.pin["frontend"]["source_sha"], "integration": self.pin["integration"], "steps": [], "passed": False}

    def password(self):
        value = secrets.token_urlsafe(32)
        self.secrets.append(value)
        return value

    def write(self, path, value, mode=0o600, uid=None, gid=None):
        path = Path(path)
        if isinstance(value, (dict, list)):
            value = json.dumps(value, indent=2) + "\n"
        path.write_bytes(value.encode() if isinstance(value, str) else value)
        path.chmod(mode)
        if uid is not None:
            os.chown(path, uid, gid if gid is not None else uid)
        return path

    def log(self, phase):
        self.phase = phase
        print("order-to-vm: " + phase, flush=True)
        self.write(self.report / "summary.json", self.evidence, 0o644)

    def command(self, argv, *, data=None, env=None, timeout=90, check=True):
        self.counter += 1
        result = subprocess.run(list(map(str, argv)), input=data.encode() if isinstance(data, str) else data,
                                env=env or self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        self.write(self.private / f"command-{self.counter}.log", result.stderr)
        if check and result.returncode:
            raise RuntimeError(f"{self.phase}: {Path(str(argv[0])).name} exited {result.returncode}; command-{self.counter}")
        return result

    def spawn(self, name, argv, env=None):
        log = (self.private / (name + ".log")).open("wb")
        process = subprocess.Popen(list(map(str, argv)), env=env or self.env, stdout=log, stderr=log, start_new_session=True)
        self.processes.append((name, process, log))
        return process

    def wait(self, description, probe, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            for name, process, _ in self.processes:
                require(process.poll() is None, "process exited: " + name)
            try:
                value = probe()
                if value:
                    return value
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(0.25)  # Poll an observed condition; never assume readiness.
        raise RuntimeError("deadline waiting for " + description)

    def stage_binaries(self):
        self.bin = self.root / "bin"
        self.bin.mkdir(mode=0o755)
        for source in BIN.iterdir():
            require(source.is_file() and not source.is_symlink(), "unexpected cached binary")
            target = self.bin / source.name
            shutil.copyfile(source, target)
            target.chmod(0o555)

    def tls(self):
        self.log("private TLS authority")
        self.command(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-subj", "/CN=Order to VM CI", "-keyout", self.private / "ca.key", "-out", self.root / "ca.crt"])
        for name, usage in (("finance", "serverAuth"), ("controller", "serverAuth"), ("node", "serverAuth"), ("node-client", "clientAuth"), ("mysql", "serverAuth")):
            self.command(["openssl", "req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=localhost", "-keyout", self.private / (name + ".key"), "-out", self.private / (name + ".csr")])
            extension = self.write(self.private / (name + ".ext"), "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=" + usage + "\n")
            self.command(["openssl", "x509", "-req", "-days", "2", "-in", self.private / (name + ".csr"), "-CA", self.root / "ca.crt", "-CAkey", self.private / "ca.key", "-CAcreateserial", "-extfile", extension, "-out", self.root / (name + ".crt")])
        self.context = ssl.create_default_context(cafile=self.root / "ca.crt")
        self.evidence["tls_verification"] = True

    def mysql(self):
        self.log("two independent MySQL 8.4 servers")
        destination = self.root / "mysql-distribution"
        destination.mkdir()
        manifest = mysql_manifest()
        for package in manifest["packages"]:
            archive = DOWNLOADS / (package["name"] + "_" + manifest["package_version"] + "_amd64.deb")
            require(digest(archive) == package["sha256"], "cached MySQL changed")
            self.command(["dpkg-deb", "--extract", archive, destination])
        self.mysql_root = destination / "usr"
        self.mysql_client = self.mysql_root / "bin/mysql"
        self.db_password = self.password()
        for name, port, database in (("finance", 33071, "aster_e2e"), ("controller", 33072, "aster_controller_e2e")):
            directory = self.private / ("mysql-" + name)
            directory.mkdir()
            data = directory / "data"
            data.mkdir()
            base = [self.mysql_root / "sbin/mysqld", "--no-defaults", "--user=root", "--basedir=" + str(self.mysql_root), "--datadir=" + str(data), "--lc-messages-dir=" + str(self.mysql_root / "share/mysql-8.4"), "--plugin-dir=" + str(self.mysql_root / "lib/mysql/plugin")]
            self.command(base + ["--initialize-insecure"], timeout=120)
            self.spawn("mysql-" + name, base + ["--bind-address=127.0.0.1", "--port=" + str(port), "--socket=" + str(directory / "mysql.sock"), "--pid-file=" + str(directory / "mysql.pid"), "--mysqlx=OFF", "--skip-log-bin", "--skip-name-resolve", "--innodb-buffer-pool-size=64M", "--ssl-ca=" + str(self.root / "ca.crt"), "--ssl-cert=" + str(self.root / "mysql.crt"), "--ssl-key=" + str(self.private / "mysql.key"), "--require-secure-transport=ON"])
            client = [self.mysql_client, "--no-defaults", "--protocol=SOCKET", "--socket=" + str(directory / "mysql.sock"), "--user=root", "--batch", "--skip-column-names"]
            self.wait(name + " MySQL ready", lambda: self.command(client, data="SELECT 1", check=False).returncode == 0)
            self.command(client, data=f"CREATE DATABASE {database} CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci; CREATE USER 'aster'@'127.0.0.1' IDENTIFIED BY '{self.db_password}' REQUIRE SSL; GRANT ALL ON {database}.* TO 'aster'@'127.0.0.1';")
            self.write(self.private / (name + ".dsn"), f"aster:{self.db_password}@tcp(127.0.0.1:{port})/{database}?tls=true&parseTime=true")
        self.evidence["mysql_version"] = manifest["version"]

    def sql(self, name, query):
        socket = self.private / ("mysql-" + name) / "mysql.sock"
        database = "aster_e2e" if name == "finance" else "aster_controller_e2e"
        return self.command([self.mysql_client, "--no-defaults", "--protocol=SOCKET", "--socket=" + str(socket), "--user=root", "--batch", "--skip-column-names", database], data=query).stdout.decode().strip()

    def regressions(self):
        self.log("checkout credential and overdue billing regression tests on MySQL 8.4")
        self.sql("finance", "CREATE DATABASE aster_e2e_regression_test CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci")
        result = self.command([self.bin / "scheduler.test", "-test.v", "-test.timeout=4m",
            "-test.run=^(TestMySQLCheckoutPasswordSurvivesPaymentAndBootstrap|TestOverdueDecisionSurvivesSchedulerClockAndRenewalRace|TestMySQLCommerceAutomationBillingJobsAndFailureIsolation)$"],
            env=dict(self.env, ASTER_MYSQL_TEST_DSN="root@unix(" + str(self.private / "mysql-finance/mysql.sock") + ")/aster_e2e_regression_test?parseTime=true"), check=False, timeout=250)
        self.write(self.private / "regressions.log", result.stdout + result.stderr)
        require(result.returncode == 0 and b"--- SKIP:" not in result.stdout and result.stdout.count(b"--- PASS:") >= 3, "real MySQL regression tests failed or skipped")
        self.complete_step("mysql-regressions", tests=3)

    def network(self):
        self.log("dedicated libvirt bridge and pinned IPv4 filter")
        self.command(["ip", "link", "add", "e2evm0", "type", "bridge"])
        self.command(["ip", "addr", "add", "192.0.2.1/24", "dev", "e2evm0"])
        self.command(["ip", "link", "set", "e2evm0", "up"])
        # The exact node-approved template; node validates its semantics again.
        source = (ARTIFACTS / "e2e-cloud/aster-cloud/internal/hypervisor/network.go").read_text()
        template = source.split('return []byte(`', 1)[1].split('`)\n}', 1)[0]
        template = template.replace('` + name + `', "aster-e2e-filter")
        self.write(self.root / "filter.xml", template)
        self.command(["virsh", "-c", "qemu:///system", "nwfilter-define", self.root / "filter.xml"])
        live = self.command(["virsh", "-c", "qemu:///system", "nwfilter-dumpxml", "aster-e2e-filter"]).stdout
        self.filter_hash = hashlib.sha256(live).hexdigest()
        self.command(["ssh-keygen", "-t", "ed25519", "-N", "", "-f", self.private / "guest-key"])

    def cloud(self):
        self.log("real node with mTLS")
        for name in ("images", "disks", "node-state", "snapshots", "inbox"):
            (self.root / name).mkdir(mode=0o755 if name in ("images", "disks") else 0o700)
        images = {}
        for image in self.pin["images"]:
            source = DOWNLOADS / (image["id"] + ".img")
            require(digest(source) == image["sha256"], "cached image changed")
            target = self.root / "images" / source.name
            shutil.copyfile(source, target)
            target.chmod(0o444)
            images[image["id"]] = {"path": str(target), "sha256": image["sha256"]}
        def cert_hash(name):
            der = self.command(["openssl", "x509", "-in", self.root / (name + ".crt"), "-outform", "DER"]).stdout
            return hashlib.sha256(der).hexdigest()
        try:
            with open("/dev/kvm", "r+b"):
                emulation = False
        except OSError:
            emulation = True
        self.evidence["hypervisor"] = "tcg" if emulation else "kvm"
        node = {"node_id": "e2e-node", "datacenter_id": "e2e-dc", "node_group_id": "", "listen": "127.0.0.1:19444", "certificate": str(self.root / "node.crt"), "private_key": str(self.private / "node.key"), "controller_ca": str(self.root / "ca.crt"), "expected_controller_certificate_sha256": cert_hash("node-client"), "state_directory": str(self.root / "node-state"), "storage_id": "local", "storage_directory": str(self.root / "disks"), "images": images, "allow_software_emulation": emulation, "allow_unmetered_default": True, "allow_delete": True, "cpu_allocation_limit": 8, "memory_allocation_limit_mib": 4096, "disk_allocation_limit_gib": 32, "shutdown_timeout_seconds": 60,
                "network": {"enabled": True, "dedicated_bridge_acknowledged": True, "bridge": "e2evm0", "cidr": "192.0.2.0/24", "gateway": "192.0.2.1", "addresses": ["192.0.2.10", "192.0.2.11"], "dns": ["192.0.2.1"], "filter": "aster-e2e-filter", "filter_sha256": self.filter_hash, "ssh_authorized_keys": [(self.private / "guest-key.pub").read_text().strip()]},
                "guest": {"enabled": True, "families": {i: "linux" for i in images}},
                "storage": {"enabled": True, "directory": str(self.root / "snapshots"), "inbox_directory": str(self.root / "inbox"), "max_artifact_bytes": 12 << 30, "max_total_bytes": 24 << 30}}
        self.node_config = self.write(self.private / "node.json", node)
        result = self.command([self.bin / "aster-node", "--config", self.node_config, "--doctor"], check=False)
        require(result.stdout.strip(), "node configuration rejected before readiness inspection")
        doctor = json.loads(result.stdout)
        self.evidence["node_doctor"] = doctor
        self.write(self.report / "summary.json", self.evidence, 0o644)
        failed = [check["name"] for check in doctor["checks"] if not check["passed"]]
        require(result.returncode == 0 and doctor["ready"], "node doctor failed: " + ", ".join(failed))
        self.spawn("node", [self.bin / "aster-node", "--config", self.node_config])
        node_context = ssl.create_default_context(cafile=self.root / "ca.crt")
        node_context.load_cert_chain(self.root / "node-client.crt", self.private / "node-client.key")
        self.evidence["node_capabilities"] = self.wait("authenticated node capabilities", lambda: json.loads(self.get("https://127.0.0.1:19444/v1/capabilities", context=node_context)))
        self.billing_token = self.password()
        self.admin_token = self.password()
        self.write(self.private / "billing.token", self.billing_token)
        self.write(self.private / "admin-read.token", self.admin_token)
        self.write(self.private / "admin-write.token", self.password())
        profile = {"id": "e2e-node", "datacenter_id": "e2e-dc", "group_id": "", "endpoint": "https://127.0.0.1:19444", "allowed_cidrs": ["127.0.0.0/8"], "ca_file": str(self.root / "ca.crt"), "client_cert_file": str(self.root / "node-client.crt"), "client_key_file": str(self.private / "node-client.key"), "server_certificate_sha256": cert_hash("node"), "allow_software_emulation": emulation}
        resources = {"datacenter_id": "e2e-dc", "compute_node_id": "e2e-node", "node_group_id": "", "image_id": "ubuntu-24", "cpu_cores": 1, "memory_mib": 512, "system_disk_gib": 4, "system_storage_id": "local", "data_disk_gib": 0, "data_storage_id": "", "network_type": "normal", "bandwidth_mbps": 0, "inbound_bandwidth_mbps": 0, "transfer_gib": 0, "traffic_direction": "all", "ipv4_count": 1, "ipv6_count": 0}
        controller = {"schema": 1, "controller_id": "e2e-cloud", "listen": "127.0.0.1:19443", "tls_cert_file": str(self.root / "controller.crt"), "tls_key_file": str(self.private / "controller.key"), "dsn_file": str(self.private / "controller.dsn"), "billing_token_file": str(self.private / "billing.token"), "admin_read_token_file": str(self.private / "admin-read.token"), "admin_write_token_file": str(self.private / "admin-write.token"), "heartbeat_ttl_seconds": 300, "nodes": [profile], "presets": [{"id": "e2e-small", "node_id": "e2e-node", "resources": resources}, {"id": "e2e-large", "node_id": "e2e-node", "resources": dict(resources, cpu_cores=2, memory_mib=768, system_disk_gib=6)}], "dispatcher": {"enabled": True}, "service": {"snapshot_limit": 3, "images": [{"id": im["id"], "family": "linux", "name": im["name"], "version": im["version"], "min_disk_gb": im["min_disk_gb"], "sha256": im["sha256"], "node_ids": ["e2e-node"]} for im in self.pin["images"]]}}
        self.controller_config = self.write(self.private / "controller.json", controller)
        for command in ("migrate", "register-nodes"):
            self.command([self.bin / "aster-controller", command, "--config", self.controller_config])
        self.wait("mTLS node heartbeat", lambda: self.command([self.bin / "aster-controller", "heartbeat", "--config", self.controller_config, "--node", "e2e-node"], check=False).returncode == 0)
        # Registration starts in maintenance; admit this fixture only after its verified heartbeat.
        self.command([self.bin / "aster-controller", "maintenance", "--config", self.controller_config, "--node", "e2e-node", "--on=false"])
        self.spawn("controller", [self.bin / "aster-controller", "serve", "--config", self.controller_config, "--console-origin", "https://localhost:19443"])
        self.wait("controller HTTPS", lambda: self.get("https://localhost:19443/v1/health", self.admin_token))
        self.complete_step("controller-node-mtls-and-admission", node="e2e-node")

    def get(self, url, token=None, context=None):
        request = urllib.request.Request(url, headers={"Authorization": "Bearer " + token} if token else {})
        with urllib.request.urlopen(request, context=context or self.context, timeout=5) as response:
            return response.read()

    def finance(self):
        self.log("real finance API and pinned frontend")
        self.command(["useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "aster-e2e-api"])
        self.api_uid = int(self.command(["id", "-u", "aster-e2e-api"]).stdout)
        self.api_gid = int(self.command(["id", "-g", "aster-e2e-api"]).stdout)
        self.api_dir = self.root / "api"
        self.api_dir.mkdir(mode=0o700)
        os.chown(self.api_dir, self.api_uid, self.api_gid)
        self.write(self.api_dir / "mysql.dsn", (self.private / "finance.dsn").read_bytes(), uid=self.api_uid, gid=self.api_gid)
        self.write(self.api_dir / "session", self.password(), uid=self.api_uid, gid=self.api_gid)
        self.write(self.api_dir / "smtp.key", secrets.token_bytes(32), uid=self.api_uid, gid=self.api_gid)
        self.write(self.api_dir / "finance.key", (self.private / "finance.key").read_bytes(), uid=self.api_uid, gid=self.api_gid)
        self.plugins()
        self.api_env = dict(self.env, APP_MODE="production", APP_ORIGIN="https://localhost:19445", LISTEN_ADDR="127.0.0.1:18080", MYSQL_DSN_FILE=str(self.api_dir / "mysql.dsn"), SESSION_SECRET_FILE=str(self.api_dir / "session"), SMTP_ENCRYPTION_KEY_FILE=str(self.api_dir / "smtp.key"), COMMERCE_API_VERSION="0.6", TRUSTED_PROXY_CIDRS="127.0.0.0/8", PLUGIN_DIRECTORY=str(self.root / "plugins"), PLUGIN_POLICY_FILE=str(self.root / "plugins.json"))
        self.api_env.update(API_TLS_CERT_FILE=str(self.root / "finance.crt"), API_TLS_KEY_FILE=str(self.api_dir / "finance.key"))
        prefix = ["setpriv", "--reuid=" + str(self.api_uid), "--regid=" + str(self.api_gid), "--init-groups"]
        self.command(prefix + [self.bin / "aster-api", "-migrate"], env=self.api_env)
        self.admin_password = self.password()
        self.write(self.api_dir / "bootstrap", self.admin_password, uid=self.api_uid, gid=self.api_gid)
        self.command(prefix + [self.bin / "aster-api", "-create-user"], env=dict(self.api_env, BOOTSTRAP_EMAIL="staff@e2e.invalid", BOOTSTRAP_PASSWORD_FILE=str(self.api_dir / "bootstrap"), BOOTSTRAP_ROLE="admin", BOOTSTRAP_NAME="E2E staff"))
        self.spawn("finance", prefix + [self.bin / "aster-api"], self.api_env)
        static = self.root / "frontend"
        shutil.copytree(ARTIFACTS / "e2e-frontend/dist", static)
        config = f"""daemon off;
pid {self.private}/nginx.pid;
error_log {self.private}/nginx-error.log warn;
events {{ worker_connections 128; }}
http {{
  include /etc/nginx/mime.types;
  access_log off;
  client_body_temp_path {self.private}/nginx-body;
  proxy_temp_path {self.private}/nginx-proxy;
  proxy_ssl_verify on;
  proxy_ssl_trusted_certificate {self.root}/ca.crt;
  proxy_ssl_server_name on;
  proxy_ssl_name localhost;
  server {{
    listen 127.0.0.1:19445 ssl;
    server_name localhost;
    ssl_certificate {self.root}/finance.crt;
    ssl_certificate_key {self.private}/finance.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    root {static};
    location / {{ try_files $uri $uri/ /index.html; }}
    location /api/ {{ proxy_pass https://127.0.0.1:18080; proxy_set_header Host $http_host; proxy_set_header X-Forwarded-Proto https; proxy_set_header X-Forwarded-For $remote_addr; }}
    location = /readyz {{ proxy_pass https://127.0.0.1:18080; }}
  }}
}}
"""
        self.write(self.private / "nginx.conf", config)
        self.spawn("nginx", ["nginx", "-c", self.private / "nginx.conf"])
        self.wait("finance HTTPS readiness", lambda: self.get("https://localhost:19445/readyz"))
        require(b"<html" in self.get("https://localhost:19445/"), "frontend unavailable")
        self.evidence["steps"].append({"id": "stack-readiness", "passed": True})

    def plugins(self):
        self.log("isolated server.aster-cloud, SMTP and paymentfixture workers")
        (self.private / "mail").mkdir(mode=0o700)
        self.smtp_password = self.password()
        self.write(self.private / "smtp-password", self.smtp_password)
        self.spawn("mail-sink", [sys.executable, "-B", HERE / "mail_sink.py", self.root])
        self.payment_key = secrets.token_bytes(32)
        self.write(self.private / "payment-fixture-key", self.payment_key)
        self.write(self.private / "server-config.json", {"schema": 1, "controller_id": "e2e-cloud", "endpoint": "https://127.0.0.1:19443/v1/provisioning", "allowed_cidrs": ["127.0.0.0/8"], "custom_ca": True})
        policy = {"schema": 1, "revision": 1}
        specifications = [
            ("smtp", "email.smtp", "1.1.0", "aster.smtp/1", ["smtp.send"], 19446),
            ("payment", "payment.fixture", "0.1.0", "aster.payment/1", ["payment.checkout", "payment.verify_callback"], 443),
            ("server", "server.aster-cloud", "1.0.0", "aster.server/1", ["server.call"], 19443),
        ]
        for kind, identifier, version, protocol, capabilities, port in specifications:
            user = "aster-e2e-" + kind
            self.command(["useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--gid", str(self.api_gid), user])
            uid = int(self.command(["id", "-u", user]).stdout)
            directory = self.root / "plugins" / identifier / version
            directory.mkdir(parents=True, mode=0o755)
            binary = directory / "plugin"
            shutil.copyfile(self.bin / {"payment": "paymentfixture", "server": "server.aster-cloud", "smtp": "smtp"}[kind], binary)
            binary.chmod(0o555)
            manifest = {"schema": 1, "id": identifier, "version": version, "protocol": protocol,
                "core_min": {"payment": "0.5.3", "smtp": "0.4.0", "server": "0.6.0"}[kind], "core_max_exclusive": "0.7.0",
                "capabilities": capabilities, "executable": "plugin", "sha256": digest(binary)}
            if kind == "payment":
                manifest["payment"] = {"currencies": [{"code": "CNY", "precision": 2}, {"code": "USD", "precision": 2}, {"code": "JPY", "precision": 0}], "checkout_hosts": ["checkout.example.test"]}
            path = self.write(directory / "manifest.json", manifest, 0o644)
            sockets = Path("/run/aster-server/e2e-cloud") if kind == "server" else self.root / (kind + "-socket")
            sockets.parent.mkdir(parents=True, exist_ok=True)
            sockets.mkdir(mode=0o750)
            os.chown(sockets, uid, self.api_gid)
            selection = {"enabled": True, "id": identifier, "version": version, "manifest_sha256": digest(path), "worker_uid": uid,
                "client_uid": self.api_uid, "socket": str(sockets / "plugin.sock"), "allowed_targets": [{"host": "127.0.0.1" if kind == "server" else "localhost", "port": port}]}
            if kind == "payment":
                selection["merchant_id"] = "e2e-merchant"
            if kind == "server":
                selection["slot"] = "e2e-cloud"
                policy["servers"] = [selection]
                self.module_state = Path("/var/lib/aster-server/e2e-cloud")
                self.module_state.mkdir(parents=True, mode=0o700)
                os.chown(self.module_state, uid, self.api_gid)
            else:
                policy[kind] = selection
        self.write(self.root / "plugins.json", policy, 0o644)
        for kind, *_ in specifications:
            unit = "aster-e2e-" + kind + "-" + os.environ["GITHUB_RUN_ID"]
            args = ["systemd-run", "--unit=" + unit, "--collect", "--property=User=aster-e2e-" + kind, "--property=Group=" + str(self.api_gid),
                "--property=NoNewPrivileges=yes", "--property=ProtectSystem=strict", "--property=ReadWritePaths=" + str(Path("/run/aster-server/e2e-cloud") if kind == "server" else self.root / (kind + "-socket")),
                "--property=BindReadOnlyPaths=" + str(self.root / "ca.crt") + ":/etc/ssl/certs/ca-certificates.crt"]
            if kind == "payment":
                args += ["--property=LoadCredential=payment-fixture-key:" + str(self.private / "payment-fixture-key")]
            if kind == "server":
                args += ["--property=ReadWritePaths=" + str(self.module_state),
                    "--property=LoadCredential=server_config.json:" + str(self.private / "server-config.json"),
                    "--property=LoadCredential=server_token:" + str(self.private / "billing.token"),
                    "--property=LoadCredential=server_ca.pem:" + str(self.root / "ca.crt")]
            args += [str(self.bin / "aster-plugin-host"), "--root", str(self.root / "plugins"), "--policy", str(self.root / "plugins.json"), "--kind", kind]
            if kind == "server":
                args += ["--slot", "e2e-cloud"]
            self.command(args)
            self.units.append(unit)
            socket = Path(policy["servers"][0]["socket"] if kind == "server" else policy[kind]["socket"])
            def ready():
                if socket.is_socket():
                    return True
                active = self.command(["systemctl", "show", unit, "--property=ActiveState", "--value"], check=False).stdout.strip()
                require(active in (b"active", b"activating"), kind + " worker exited before opening its socket")
                return False
            self.wait(kind + " worker socket", ready)

    def tick(self, job, at=None):
        if at is not None:
            self.scheduler_at = max(self.scheduler_at, at)
        else:
            self.scheduler_at += timedelta(minutes=1)
        self.command([self.bin / "aster-controller", "heartbeat", "--config", self.controller_config, "--node", "e2e-node"])
        prefix = ["setpriv", "--reuid=" + str(self.api_uid), "--regid=" + str(self.api_gid), "--init-groups"]
        result = self.command(prefix + [self.bin / "scheduler.test", "-test.run=^TestOrderToVMScheduler$", "-test.timeout=95s"], timeout=100, check=False,
            env=dict(self.api_env, ASTER_E2E_SCHEDULER="1", ASTER_E2E_JOB=job, ASTER_E2E_AT=self.scheduler_at.isoformat(timespec="seconds").replace("+00:00", "Z")))
        self.write(self.private / "scheduler-last.log", result.stdout + result.stderr)
        require(result.returncode == 0, "real scheduler receipt failed: " + job)

    def configure(self):
        self.log("staff configuration through finance API")
        self.admin = API(self)
        self.admin.login("staff@e2e.invalid", self.admin_password)
        settings = self.admin.call("/admin/site-settings")
        settings.update(default_locale="en", enabled_locales=["en"], registration_open=True, email_verification_required=True,
            site_name="Order to VM", company_name="Acceptance", company_address="CI", support_email="support@e2e.invalid")
        self.admin.call("/admin/site-settings", "PATCH", settings)
        smtp = self.admin.call("/admin/integrations/smtp")
        self.admin.call("/admin/integrations/smtp", "PATCH", {"revision": smtp["revision"], "enabled": True, "host": "localhost", "port": 19446,
            "security": "tls", "username": "acceptance", "password": self.smtp_password, "from_email": "support@e2e.invalid", "from_name": "Acceptance"})
        self.admin.call("/admin/integrations/smtp/test", "POST", {"to": "probe@e2e.invalid"})
        automation = self.admin.call("/admin/automation")
        automation.update(auto_provision=True, auto_apply_credit=False, suspend_after_days=1, terminate_after_days=30)
        self.admin.call("/admin/automation", "PATCH", automation)
        for gateway in self.admin.call("/admin/integrations/gateways"):
            self.admin.call("/admin/integrations/gateways/" + gateway["id"], "PATCH", {"revision": gateway["revision"], "enabled": True})
        require(any(g["id"] == "payment.fixture" for g in self.admin.call("/payment-gateways?currency=CNY")), "payment worker unavailable")
        self.evidence["steps"].append({"id": "isolated-payment-and-smtp-workers", "passed": True})

    def verification_link(self, address):
        self.tick("email_outbox")
        def delivered():
            for path in (self.private / "mail").glob("*.json"):
                mail = json.loads(path.read_text())
                if mail["to"] == address:
                    match = re.search(r"https://localhost:19445/verify-email\?token=[A-Za-z0-9_-]+", mail["text"])
                    if match:
                        return match.group(0)
            return False
        return self.wait("verification delivered over real SMTP TLS", delivered)

    def catalogue(self):
        self.log("approved Aster Cloud module connection and two product plans")
        connections = self.admin.call("/admin/integrations/connections")
        require(len(connections) == 1 and connections[0]["id"] == "e2e-cloud" and connections[0]["module_id"] == "server.aster-cloud", "scenario is not bound to the real module")
        modules = self.admin.call("/admin/integrations/modules")
        require(len(modules) == 1 and modules[0]["status"] == "ready", "Aster Cloud module unavailable")
        for action in ("test", "sync-images"):
            self.admin.call("/admin/integrations/connections/e2e-cloud/" + action, "POST", {})
        images = self.admin.call("/admin/os-images")
        require(len(images) == 2, "approved controller image catalog differs")
        for image in images:
            self.admin.call("/admin/os-images/" + image["id"], "PATCH", {"enabled": True})
        taxonomy = self.admin.call("/admin/catalog-taxonomy")
        for section in ("types", "regions", "nodes"):
            require(bool(taxonomy[section]), "seed taxonomy missing " + section)
            for row in taxonomy[section]:
                row["visible"] = True
        self.admin.call("/admin/catalog-taxonomy", "POST", taxonomy)
        self.products = []
        for name, cpu, memory, disk, price in (("Small", 1, 512, 4, 100), ("Large", 2, 768, 6, 200)):
            config = {"preset_id": "e2e-" + name.lower(), "cpu_cores": cpu, "memory_mb": memory, "disk_gb": disk, "bandwidth_mbps": 0}
            product = {"slug": "e2e-" + name.lower(), "kind": "cloud_server", "node_id": taxonomy["nodes"][0]["id"],
                "name": {"en": "E2E " + name}, "description": {"en": "Real libvirt acceptance"}, "badges": [],
                "specs": {"cpu_cores": cpu, "memory_mb": memory, "disk_gb": disk, "bandwidth_mbps": 0, "traffic_gb": None, "ipv4": 1, "ipv6": False},
                "prices": [{"cycle": "monthly", "currency": "CNY", "recurring_minor": price, "setup_minor": 0, "decimal_places": 2}],
                "options": [{"id": uuid.uuid4().hex, "key": "os", "kind": "os_image", "label": {"en": "Operating system"}, "required": True, "choices": []}],
                "os_image_ids": [image["id"] for image in images], "visibility": "public", "stock": None, "sort_order": len(self.products),
                "module": {"module_id": "server.aster-cloud", "connection_id": "e2e-cloud", "config": config}}
            self.products.append(self.admin.call("/admin/products", "POST", product))
        self.complete_step("module-product-binding", module="server.aster-cloud", connection="e2e-cloud")

    def complete_step(self, name, **evidence):
        self.evidence["steps"].append(dict(id=name, passed=True, **evidence))
        self.log(name + " passed")
        return evidence

    def service_state(self, args):
        sid = args["id"]
        require(re.fullmatch("[a-f0-9]{32}", sid), "invalid service assertion identity")
        def observed():
            self.tick("dispatch_provisioning")
            view = self.admin.call("/admin/services/" + sid)
            require(view.get("module_id") == "server.aster-cloud" and view.get("connection_id") == "e2e-cloud", "service bypassed the Aster Cloud module")
            rows = self.sql("finance", f"SELECT JSON_OBJECT('id',id,'action',action,'status',status,'operation',operation_id) FROM service_tasks WHERE service_id='{sid}' ORDER BY created_at,id")
            tasks = [json.loads(row) for row in rows.splitlines()]
            observation = {"id": sid, "status": view["status"], "power": view.get("power"), "tasks": tasks}
            self.evidence["last_service_observation"] = observation
            require(not any(t["status"] in ("failed", "uncertain") for t in tasks), "service task definitively failed or became uncertain")
            if any(t["status"] != "succeeded" for t in tasks) or view["status"] != args["state"]:
                observation["waiting_for"] = "finance_task_and_state"
                return False
            if args.get("task") and not any(t["id"] == args["task"] for t in tasks):
                observation["waiting_for"] = "requested_task"
                return False
            resource = view.get("resource_id")
            if not resource:
                observation["waiting_for"] = "resource_identity"
                return False
            require(re.fullmatch("[a-f0-9]{32}", resource), "invalid controller resource")
            observation["resource"] = resource
            ledger = json.loads((self.root / "node-state/journal.json").read_text())
            instance = ledger["instances"].get(resource)
            if not instance:
                observation["waiting_for"] = "node_instance"
                return False
            domain = "aster-" + resource
            live = self.command(["virsh", "-c", "qemu:///system", "domstate", domain], check=False)
            if args["state"] == "terminated":
                require(live.returncode != 0 and instance["state"] == "deleted", "terminated domain still defined")
                require(not Path(instance["directory"]).exists(), "terminated disks still exist")
                require(not any(i["state"] != "deleted" and i["ipv4"] == instance["ipv4"] for i in ledger["instances"].values()), "IP is still allocated")
            else:
                actual = live.stdout.decode().strip()
                if actual != ("shut off" if args["power"] == "stopped" else "running") or view.get("power") != args["power"]:
                    observation["waiting_for"] = "libvirt_and_finance_power"
                    observation["domain_state"] = actual if actual in ("running", "shut off", "paused") else "unconfirmed"
                    return False
                require(view.get("primary_ipv4") == instance["ipv4"] and bool(instance["ipv4"]), "customer IP differs from node allocation")
                require(bool(instance.get("suspended")) == (args["state"] == "suspended"), "node suspension flag differs from finance")
                xml = ET.fromstring(self.command(["virsh", "-c", "qemu:///system", "dumpxml", domain]).stdout)
                if args.get("image"):
                    require(instance["resources"]["image_id"] == args["image"], "reinstall did not change actual image")
                if args.get("cpu"):
                    require(int(xml.findtext("vcpu")) == args["cpu"] and int(xml.findtext("memory")) == args["memory"] * 1024, "domain resize differs")
                    disk = json.loads(self.command(["qemu-img", "info", "--force-share", "--output=json", instance["disk"]]).stdout)
                    require(disk["virtual-size"] == args["disk"] * (1 << 30), "disk resize differs")
                    require(view["specs"]["cpu_cores"] == args["cpu"] and view["specs"]["memory_mb"] == args["memory"] and view["specs"]["disk_gb"] == args["disk"], "controller resize projection differs")
                if args["power"] == "running":
                    reachable = self.command(["ping", "-n", "-c", "1", "-W", "1", instance["ipv4"]], check=False)
                    if reachable.returncode:
                        observation["waiting_for"] = "guest_network"
                        return False
            operations = []
            for task in tasks:
                operation = task["operation"]
                require(operation and re.fullmatch("[a-f0-9]{32}", operation), "task missing remote operation identity")
                state = self.sql("controller", f"SELECT state FROM controller_jobs WHERE operation_id='{operation}' UNION ALL SELECT state FROM controller_operations WHERE id='{operation}'")
                require(state == "succeeded", "controller operation did not succeed: " + task["action"])
                audit = int(self.sql("controller", f"SELECT COUNT(*) FROM controller_audit WHERE target='{operation}'"))
                require(audit > 0, "controller operation has no audit evidence")
                operations.append({"id": operation, "action": task["action"], "state": state, "audits": audit})
                if task["action"] == "provision":
                    setup = hashlib.sha256((operation + ":setup").encode()).hexdigest()[:32]
                    require(self.sql("controller", f"SELECT state FROM controller_operations WHERE id='{setup}'") == "succeeded", "initial image and credential setup did not succeed")
                    require(int(self.sql("controller", f"SELECT COUNT(*) FROM controller_audit WHERE target='{setup}'")) > 0, "initial guest setup has no audit")
                    operations.append({"id": setup, "action": "initial-guest-setup", "state": "succeeded"})
            return {"id": sid, "resource": resource, "ip": instance["ipv4"], "finance": view["status"], "power": args.get("power"), "operations": operations}
        result = self.wait(args["step"] + " finance/controller/libvirt agreement", observed, timeout=300)
        self.complete_step(args["step"], service=sid, **{key: value for key, value in result.items() if key != "id"})
        return result

    def gateway_callback(self, invoice):
        require(re.fullmatch("[a-f0-9]{32}", invoice), "invalid fixture invoice")
        raw = self.sql("finance", "SELECT JSON_OBJECT('attempt_id',id,'account_id',user_id,'order_id',order_id,'invoice_id',invoice_id,"
            "'amount_minor',amount_minor,'currency',currency,'currency_decimal_places',currency_decimal_places) FROM payment_attempts WHERE invoice_id='" + invoice + "'")
        event = json.loads(raw)
        event.update(provider_id="payment.fixture", event_id="e2e-event-" + uuid.uuid4().hex,
            transaction_id="e2e-transaction-" + uuid.uuid4().hex, merchant_account_id="e2e-merchant", status="settled",
            settled_at=datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z"))
        body = json.dumps(event, sort_keys=True, separators=(",", ":")).encode()
        request = urllib.request.Request("https://localhost:19445/api/v1/payment-callbacks/payment.fixture", method="POST", data=body,
            headers={"Content-Type": "application/json", "X-Fixture-Signature": hmac.new(self.payment_key, body, hashlib.sha256).hexdigest()})
        with urllib.request.urlopen(request, context=self.context, timeout=30) as response:
            require(response.status == 200, "signed real payment callback rejected")
        require(self.admin.call("/admin/invoices/" + invoice)["status"] == "paid", "gateway callback did not settle invoice")
        return self.complete_step("paymentfixture-signed-callback", invoice=invoice)

    def no_plaintext(self):
        for name, database in (("finance", "aster_e2e"), ("controller", "aster_controller_e2e")):
            raw = self.command([self.mysql_root / "bin/mysqldump", "--no-defaults", "--protocol=SOCKET", "--socket=" + str(self.private / ("mysql-" + name) / "mysql.sock"), "--user=root", "--skip-comments", "--hex-blob", "--set-gtid-purged=OFF", database], timeout=60).stdout
            for password in self.secrets:
                require(password.encode() not in raw and password.encode().hex().upper().encode() not in raw.upper(), "plaintext secret persisted in " + name)
        logs = list(self.private.glob("*.log")) + list(Path("/var/log/libvirt/qemu").glob("aster-*.log")) + [self.root / "node-state/journal.json"] + list(self.module_state.glob("*.json"))
        for unit in self.units:
            self.write(self.private / (unit + ".log"), self.command(["journalctl", "--no-pager", "-u", unit, "-n", "1000"], check=False).stdout)
        for path in logs + list(self.private.glob("aster-e2e-*.log")):
            if path.is_file():
                raw = path.read_bytes()
                for password in self.secrets:
                    require(password.encode() not in raw, "plaintext secret found in log or journal")
        return self.complete_step("no-plaintext-passwords", databases=2, logs=len(logs))

    def browser_request(self, method, args):
        if method == "setup":
            return self.browser_setup
        if method == "mail":
            return self.verification_link(args["address"])
        if method == "step":
            return self.complete_step(args.pop("id"), **args)
        if method == "registered":
            require(re.fullmatch("[a-f0-9]{32}", args["id"]), "invalid customer")
            require(self.sql("finance", "SELECT email_verified FROM account_profiles WHERE user_id='" + args["id"] + "'") == "1", "email verification missing")
            return self.complete_step("register-verify-sign-in", customer=args["id"])
        if method == "gateway":
            return self.gateway_callback(args["invoice"])
        if method == "state":
            return self.service_state(args)
        if method == "provisioned":
            order = self.admin.call("/admin/orders/" + args["order"])
            ids = [sid for item in order["items"] for sid in item["service_ids"]]
            require(len(ids) == 1, "paid order did not create one service")
            return self.service_state({"id": ids[0], "state": "active", "power": "running", "image": "ubuntu-24", "step": args["step"]})
        if method == "overdue":
            service = self.admin.call("/admin/services/" + args["id"])
            due = datetime.fromisoformat(service["next_due_at"].replace("Z", "+00:00"))
            self.tick("generate_renewals", due + timedelta(days=2))
            self.tick("suspend_overdue")
            self.service_state({"id": args["id"], "state": "suspended", "power": "stopped", "step": "renewal-and-overdue-suspension"})
            invoice = self.sql("finance", "SELECT invoice_id FROM invoice_service_changes WHERE service_id='" + args["id"] + "' AND kind='renewal'")
            return self.admin.call("/admin/invoices/" + invoice)
        if method == "terminate":
            self.tick("terminate_overdue")
            return self.service_state({"id": args["id"], "state": "terminated", "step": "terminate-" + args["id"]})
        if method == "finish":
            self.no_plaintext()
            require(self.pin["integration"] == "server.aster-cloud", "final acceptance requires server module")
            self.evidence["passed"] = True
            return True
        if method == "failed":
            raise RuntimeError("browser scenario: " + args["message"][:6000])
        raise RuntimeError("unknown fixture operation")

    def browser(self):
        self.log("Playwright customer and staff journey")
        browser_uid = int(self.command(["id", "-u", "runner"]).stdout)
        browser_gid = int(self.command(["id", "-g", "runner"]).stdout)
        browser_directory = self.root / "browser"
        browser_directory.mkdir(mode=0o700)
        os.chown(browser_directory, browser_uid, browser_gid)
        profiles = {}
        for name in ("customer", "staff"):
            profile = browser_directory / ("firefox-" + name)
            profile.mkdir(mode=0o700)
            self.command(["certutil", "-N", "--empty-password", "-d", "sql:" + str(profile)])
            self.command(["certutil", "-A", "-d", "sql:" + str(profile), "-n", "Acceptance", "-t", "C,,", "-i", self.root / "ca.crt"])
            for path in [profile, *profile.iterdir()]:
                os.chown(path, browser_uid, browser_gid)
            profiles[name] = str(profile)
        self.browser_setup = {"origin": "https://localhost:19445", "profiles": profiles, "product": self.products[0]["id"],
            "staffPassword": self.admin_password, **{name + "Password": self.password() for name in ("customer", "root", "second", "reset", "reinstall")}}
        # Frontend password policy requires mixed classes; append fixed classes to
        # cryptographically random material and retain every exact canary.
        for key in self.browser_setup:
            if key.endswith("Password") and key != "staffPassword":
                self.browser_setup[key] += "aA1!"
                self.secrets.append(self.browser_setup[key])
        log = (self.private / "browser.log").open("wb")
        process = subprocess.Popen(["runuser", "-u", "runner", "--", shutil.which("node"), str(HERE / "customer.mjs")], cwd=ROOT,
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log, text=True, start_new_session=True,
            env=dict(self.env, NODE_EXTRA_CA_CERTS=str(self.root / "ca.crt")))
        self.processes.append(("browser", process, log))
        deadline = time.monotonic() + 20 * 60
        while process.poll() is None and time.monotonic() < deadline:
            ready, _, _ = select.select([process.stdout], [], [], 0.5)
            if not ready:
                continue
            line = process.stdout.readline(65537)
            if not line:
                break
            require(len(line) <= 65536, "browser fixture message too large")
            request = json.loads(line)
            value = self.browser_request(request["method"], request["args"])
            process.stdin.write(json.dumps({"id": request["id"], "value": value}) + "\n")
            process.stdin.flush()
        require(process.wait(timeout=10) == 0 and self.evidence["passed"], "browser journey did not finish")

    def cleanup(self):
        for unit in self.units:
            self.write(self.private / (unit + ".log"), self.command(["journalctl", "--no-pager", "-u", unit, "-n", "60"], check=False).stdout)
            self.command(["systemctl", "stop", unit], check=False)
        for name, process, log in reversed(self.processes):
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)
            log.close()
        ledger = self.root / "node-state/journal.json"
        if ledger.is_file():
            state = json.loads(ledger.read_text())
            self.evidence["node_operations"] = [{"id": identifier, "action": operation["task"]["action"],
                "status": operation["result"]["status"], "error_code": operation.get("error_code", "")}
                for kind in ("operations", "storage_operations") for identifier, operation in state.get(kind, {}).items()]
            for instance in state["instances"].values():
                if re.fullmatch("aster-[a-f0-9]{32}", instance["domain"]):
                    log = Path("/var/log/libvirt/qemu") / (instance["domain"] + ".log")
                    if log.is_file():
                        self.write(self.private / (instance["domain"] + ".log"), log.read_bytes()[-16384:])
                    self.command(["virsh", "-c", "qemu:///system", "destroy", instance["domain"]], check=False)
                    self.command(["virsh", "-c", "qemu:///system", "undefine", instance["domain"]], check=False)
        self.command(["virsh", "-c", "qemu:///system", "nwfilter-undefine", "aster-e2e-filter"], check=False)
        self.command(["ip", "link", "del", "e2evm0"], check=False)
        # Only bounded redacted diagnostic tails are exported; never DBs/keys/mail.
        lines = []
        for path in sorted(self.private.glob("*.log")):
            tail = path.read_bytes()[-4096:].decode(errors="replace")
            for secret in self.secrets:
                tail = tail.replace(secret, "<REDACTED>")
            if tail.strip():
                lines.append(path.name + "\n" + tail)
        self.write(self.report / "diagnostics.txt", "\n".join(lines)[-60000:], 0o644)
        self.write(self.report / "summary.json", self.evidence, 0o644)
        if hasattr(self, "module_state"):
            shutil.rmtree(self.module_state)
            shutil.rmtree("/run/aster-server/e2e-cloud")
        shutil.rmtree(self.root)

    def run(self):
        try:
            self.stage_binaries()
            self.tls()
            self.mysql()
            self.regressions()
            self.network()
            self.cloud()
            self.finance()
            self.configure()
            self.catalogue()
            self.browser()
        except Exception as error:
            self.evidence["failure"] = {"phase": self.phase, "error": str(error)}
            raise
        finally:
            self.cleanup()


def main():
    command = sys.argv[1] if len(sys.argv) == 2 else ""
    if command == "pins":
        value = pins()
        for name in ("cloud", "frontend"):
            print(name + "=" + value[name]["source_sha"])
    elif command == "download":
        download_inputs()
    elif command == "run":
        Stack().run()
    else:
        raise SystemExit("usage: stack.py pins|download|run")


if __name__ == "__main__":
    main()
