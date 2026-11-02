#!/usr/bin/env python3
"""Disposable real-stack acceptance. Run privileged operations only on Actions.

All persistent input is pinned in E2E_STACK.json. Runtime credentials and raw
diagnostics stay in a private owned directory and are removed on every exit.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[4]
HERE = Path(__file__).resolve().parent
ARTIFACTS = ROOT / "artifacts"
DOWNLOADS = ARTIFACTS / "e2e-downloads"
BIN = ARTIFACTS / "e2e-bin"


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
        self.root = Path("/run/aster-e2e-" + run)
        self.root.mkdir(mode=0o755)
        self.private = self.root / "private"
        self.private.mkdir(mode=0o700)
        self.report = ARTIFACTS / "e2e-report"
        self.report.mkdir(parents=True, exist_ok=True)
        self.processes = []
        self.secrets = []
        self.phase = "initialize"
        self.counter = 0
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
        doctor = json.loads(self.command([BIN / "aster-node", "--config", self.node_config, "--doctor"]).stdout)
        require(doctor["ready"], "node doctor not ready")
        self.evidence["node_doctor"] = doctor
        self.spawn("node", [BIN / "aster-node", "--config", self.node_config])
        self.billing_token = self.password()
        self.admin_token = self.password()
        self.write(self.private / "billing.token", self.billing_token)
        self.write(self.private / "admin-read.token", self.admin_token)
        self.write(self.private / "admin-write.token", self.password())
        profile = {"id": "e2e-node", "datacenter_id": "e2e-dc", "group_id": "", "endpoint": "https://127.0.0.1:19444", "allowed_cidrs": ["127.0.0.0/8"], "ca_file": str(self.root / "ca.crt"), "client_cert_file": str(self.root / "node-client.crt"), "client_key_file": str(self.private / "node-client.key"), "server_certificate_sha256": cert_hash("node"), "allow_software_emulation": emulation}
        resources = {"datacenter_id": "e2e-dc", "compute_node_id": "e2e-node", "node_group_id": "", "image_id": "ubuntu-24", "cpu_cores": 1, "memory_mib": 512, "system_disk_gib": 4, "system_storage_id": "local", "network_type": "normal", "traffic_direction": "all", "ipv4_count": 1}
        controller = {"schema": 1, "controller_id": "e2e-cloud", "listen": "127.0.0.1:19443", "tls_cert_file": str(self.root / "controller.crt"), "tls_key_file": str(self.private / "controller.key"), "dsn_file": str(self.private / "controller.dsn"), "billing_token_file": str(self.private / "billing.token"), "admin_read_token_file": str(self.private / "admin-read.token"), "admin_write_token_file": str(self.private / "admin-write.token"), "heartbeat_ttl_seconds": 300, "nodes": [profile], "presets": [{"id": "e2e-small", "node_id": "e2e-node", "resources": resources}], "dispatcher": {"enabled": True}, "service": {"snapshot_limit": 3, "images": [{"id": im["id"], "family": "linux", "name": im["name"], "version": im["version"], "min_disk_gb": im["min_disk_gb"], "sha256": im["sha256"], "node_ids": ["e2e-node"]} for im in self.pin["images"]]}}
        self.controller_config = self.write(self.private / "controller.json", controller)
        for command in ("migrate", "register-nodes"):
            self.command([BIN / "aster-controller", command, "--config", self.controller_config])
        self.wait("mTLS node heartbeat", lambda: self.command([BIN / "aster-controller", "heartbeat", "--config", self.controller_config, "--node", "e2e-node"], check=False).returncode == 0)
        self.spawn("controller", [BIN / "aster-controller", "serve", "--config", self.controller_config, "--console-origin", "https://localhost:19443"])
        self.wait("controller HTTPS", lambda: self.get("https://localhost:19443/v1/health", self.admin_token))

    def get(self, url, token=None):
        request = urllib.request.Request(url, headers={"Authorization": "Bearer " + token} if token else {})
        with urllib.request.urlopen(request, context=self.context, timeout=5) as response:
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
        self.write(self.api_dir / "billing.token", self.billing_token, uid=self.api_uid, gid=self.api_gid)
        self.write(self.api_dir / "smtp.key", secrets.token_bytes(32), uid=self.api_uid, gid=self.api_gid)
        policy = {"schema": 1, "controllers": [{"id": "e2e-cloud", "enabled": True, "endpoint": "https://127.0.0.1:19443/v1/provisioning", "allowed_cidrs": ["127.0.0.0/8"], "token_file": str(self.api_dir / "billing.token"), "ca_file": str(self.root / "ca.crt")}]}
        self.write(self.root / "controllers.json", policy, 0o644)
        self.api_env = dict(self.env, APP_MODE="production", APP_ORIGIN="https://localhost:19445", LISTEN_ADDR="127.0.0.1:18080", MYSQL_DSN_FILE=str(self.api_dir / "mysql.dsn"), SESSION_SECRET_FILE=str(self.api_dir / "session"), SERVICE_CONTROLLER_POLICY_FILE=str(self.root / "controllers.json"), SMTP_ENCRYPTION_KEY_FILE=str(self.api_dir / "smtp.key"), COMMERCE_API_VERSION="0.6", TRUSTED_PROXY_CIDRS="127.0.0.0/8")
        prefix = ["setpriv", "--reuid=" + str(self.api_uid), "--regid=" + str(self.api_gid), "--init-groups"]
        self.command(prefix + [BIN / "aster-api", "-migrate"], env=self.api_env)
        self.admin_password = self.password()
        self.write(self.api_dir / "bootstrap", self.admin_password, uid=self.api_uid, gid=self.api_gid)
        self.command(prefix + [BIN / "aster-api", "-create-user"], env=dict(self.api_env, BOOTSTRAP_EMAIL="staff@e2e.invalid", BOOTSTRAP_PASSWORD_FILE=str(self.api_dir / "bootstrap"), BOOTSTRAP_ROLE="admin", BOOTSTRAP_NAME="E2E staff"))
        self.spawn("finance", prefix + [BIN / "aster-api"], self.api_env)
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
  server {{
    listen 127.0.0.1:19445 ssl;
    server_name localhost;
    ssl_certificate {self.root}/finance.crt;
    ssl_certificate_key {self.private}/finance.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    root {static};
    location / {{ try_files $uri $uri/ /index.html; }}
    location /api/ {{ proxy_pass http://127.0.0.1:18080; proxy_set_header Host $http_host; proxy_set_header X-Forwarded-Proto https; proxy_set_header X-Forwarded-For $remote_addr; }}
    location = /readyz {{ proxy_pass http://127.0.0.1:18080; }}
  }}
}}
"""
        self.write(self.private / "nginx.conf", config)
        self.spawn("nginx", ["nginx", "-c", self.private / "nginx.conf"])
        self.wait("finance HTTPS readiness", lambda: self.get("https://localhost:19445/readyz"))
        require(b"<html" in self.get("https://localhost:19445/"), "frontend unavailable")
        self.evidence["steps"].append({"id": "stack-readiness", "passed": True})

    def cleanup(self):
        for name, process, log in reversed(self.processes):
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)
            log.close()
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
        shutil.rmtree(self.root)

    def run(self):
        try:
            self.tls()
            self.mysql()
            self.network()
            self.cloud()
            self.finance()
            self.evidence["passed"] = True
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
