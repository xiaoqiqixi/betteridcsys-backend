#!/usr/bin/env python3
"""Synthetic payment process and durable HTTP acceptance on an owned CI instance.

Only accept-native.sh's disposable GitHub-hosted fixture may invoke this helper.
The independent adapter uses a local random HMAC key and reserved .test URLs;
it never connects to a payment platform or moves money.
"""
import argparse
import concurrent.futures
from datetime import datetime, timezone
import gzip
import hashlib
import hmac
import http.client
import importlib.util
import io
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import ssl
import stat
import subprocess
import sys
import tarfile
import threading
import urllib.error
import urllib.request
import uuid


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("payment_native_shared", HERE / "plugin-native.py")
shared = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = shared
SPEC.loader.exec_module(shared)
require, privileged = shared.require, shared.privileged


def fixture_package(source, scratch, version):
    binary = scratch / ("aster-payment-fixture-" + version + "-linux-amd64")
    environment = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64", GOENV="off",
                       GOFLAGS="", GOWORK="off", GOTOOLCHAIN="local", GOPROXY="off", GOSUMDB="off")
    result = subprocess.run(["go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags",
                             "-s -w -X main.version=" + version, "-o", str(binary), "./plugins/paymentfixture/cmd/plugin"],
                            cwd=source / "vps-billing/backend", env=environment, capture_output=True, timeout=180)
    require(result.returncode == 0, "Synthetic payment fixture compilation failed")
    require(binary.read_bytes().startswith(b"\x7fELF\x02\x01"), "Synthetic payment fixture is not Linux amd64 ELF")
    require(shared.command([str(binary), "--version"]).strip() == ("Aster synthetic payment fixture " + version).encode(),
            "Independent payment fixture version differs")
    manifest = {"schema": 1, "id": "payment.fixture", "version": version, "protocol": "aster.payment/1",
                "core_min": "0.5.3", "core_max_exclusive": "0.6.0",
                "capabilities": ["payment.checkout", "payment.verify_callback"], "executable": "plugin",
                "sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
                "payment": {"currencies": [{"code": "CNY", "precision": 2}, {"code": "USD", "precision": 2},
                                             {"code": "JPY", "precision": 0}], "checkout_hosts": ["checkout.example.test"]}}
    files = {"manifest.json": (json.dumps(manifest, sort_keys=True) + "\n").encode(),
             "LICENSE": (source / "LICENSE").read_bytes(),
             "README.md": b"Synthetic CI payment fixture only; no live channel or real money.\n"}
    # This unpublished fixture inventory records the actual preserved Go license,
    # without claiming to be an official third-party payment distribution.
    runtime_license = Path(shared.command(["go", "env", "GOROOT"]).decode().strip()) / "LICENSE"
    files["THIRD_PARTY_NOTICES.json"] = json.dumps({"go_runtime": {"license": runtime_license.read_text()}}).encode()
    checksums = {**files, "plugin": binary.read_bytes()}
    files["SHA256SUMS"] = "".join(hashlib.sha256(data).hexdigest() + "  " + name + "\n"
                                   for name, data in sorted(checksums.items())).encode()
    base = "aster-payment-fixture-" + version + "-linux-amd64-resources"
    package = scratch / (base + ".tar.gz")
    with package.open("wb") as output, gzip.GzipFile(fileobj=output, mode="wb", mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as archive:
            directory = tarfile.TarInfo(base)
            directory.type, directory.mode = tarfile.DIRTYPE, 0o755
            archive.addfile(directory)
            for name, data in sorted(files.items()):
                member = tarfile.TarInfo(base + "/" + name)
                member.mode, member.size = 0o644, len(data)
                archive.addfile(member, io.BytesIO(data))
    return package, binary, manifest


class CustomerAPI(shared.API):
    def __init__(self, port, ca, accounts):
        # The shared transport authenticates once as administrator. Replace that
        # session explicitly, using the same verified loopback TLS transport.
        super().__init__(port, ca, accounts)
        self.request("/api/v1/auth/logout", "POST", {})
        self.request("/api/v1/session")
        self.request("/api/v1/auth/login", "POST", {
            "email": "customer@native-ci.invalid", "password": accounts["customer"]})
        self.request("/api/v1/session")

    def create_order(self):
        plans = self.request("/api/v1/plans")
        plan = next((row for row in plans if row["available"] and row["currency"] == "CNY"), None)
        require(plan is not None, "Synthetic CNY plan unavailable")
        key = str(uuid.uuid4())
        headers = {"Accept": "application/json", "Content-Type": "application/json", "Origin": self.origin,
                   "X-CSRF-Token": self.csrf, "Idempotency-Key": key}
        raw = json.dumps({"plan_id": plan["id"], "hostname": "payment-ci-" + key[:8], "currency": "CNY"}).encode()
        request = urllib.request.Request(self.origin + "/api/v1/orders", method="POST", headers=headers, data=raw)
        with self.opener.open(request, timeout=15) as response:
            require(response.status in (200, 201), "Synthetic order creation failed")
            order = json.load(response)["data"]
        invoice = next(row for row in self.request("/api/v1/invoices") if row["order_id"] == order["id"])
        return order, invoice

    def race_request(self, path, body):
        request = urllib.request.Request(self.origin + path, method="POST", data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json", "Origin": self.origin, "X-CSRF-Token": self.csrf})
        try:
            response = self.opener.open(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(65537)
            require(len(raw) <= 65536 and response.status in (200, 409), "Checkout/cancellation race produced an unexpected result")
            return response.status


class NativePaymentAcceptance(shared.NativePluginAcceptance):
    def validate_contract(self):
        identity = os.environ["GITHUB_RUN_ID"] + "-" + os.environ["GITHUB_RUN_ATTEMPT"] + "-"
        match = re.fullmatch(r"/opt/aster-ci-(\d+-\d+-\d+)", str(self.opt))
        require(match is not None and match[1].startswith(identity), "Owned payment fixture identity differs")
        suffix = match[1]
        require(self.config == Path("/etc/aster-ci-" + suffix) and self.run == Path("/run/aster-ci-" + suffix),
                "Owned payment fixture paths differ")
        require(self.api_unit == "aster-ci-api-" + suffix + ".service"
                and self.worker_unit == "aster-ci-payment-" + suffix + ".service", "Owned payment units differ")
        for name in (self.api_user, self.worker_user, self.web_user, self.group, os.environ["ASTER_PLUGIN_SMTP_USER"]):
            require(re.fullmatch(r"ast[a-z]+[0-9]+", name) is not None, "Owned payment identity is invalid")
        for path in (self.opt, self.config, self.run, self.root):
            info = path.lstat()
            require(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and not info.st_mode & 0o022,
                    "Owned payment path is not root-protected")
        require(self.scratch.is_dir() and self.scratch.stat().st_uid == os.geteuid()
                and self.scratch.stat().st_mode & 0o077 == 0, "Private payment scratch differs")
        require(not self.worker_file.exists() and not self.worker_file.is_symlink(), "Refusing existing payment service")
        self.socket = self.run / "payment/plugin.sock"
        require(1024 <= self.port <= 65535 and len(str(self.socket)) <= 100, "Payment port/socket is invalid")
        self.database = os.environ["ASTER_PLUGIN_DATABASE"]
        require(self.database == "aster_native_test_" + os.environ["GITHUB_RUN_ID"] + "_" + suffix.rsplit("-", 1)[1],
                "Payment database is outside the owned native fixture")
        self.mysql_root = Path(os.environ["ASTER_NATIVE_MYSQL_ROOT"])
        self.mysql_socket = Path(os.environ["ASTER_NATIVE_MYSQL_SOCKET"])
        fixture = Path(os.environ["ASTER_NATIVE_MYSQL_FIXTURE_ROOT"])
        require(self.mysql_root.resolve() == fixture.resolve() / "distribution/usr"
                and self.mysql_socket.resolve().is_relative_to(fixture.resolve()), "Payment database fixture paths differ")
        credentials = self.scratch / "runtime.cnf"
        info = credentials.lstat()
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) == 0o600
                and info.st_nlink == 1, "Payment SQL reader requires the private runtime fixture credential")

    def manager(self, *arguments, succeeds=True):
        execution = ["sudo", "-n", "python3", str(self.installer), "--root", str(self.root), "--policy", str(self.policy),
                     "--core-version", "0.5.3", *map(str, arguments)]
        shared.DIAGNOSTICS.enter("payment_manager_" + str(arguments[0]))
        result = subprocess.run(execution, capture_output=True, timeout=30, cwd="/")
        require((result.returncode == 0) == succeeds, "Payment manager action outcome differs")
        return json.loads(result.stdout) if succeeds else None

    def selection(self, version):
        self.manager("select", "--id", "payment.fixture", "--version", version, "--worker-uid", self.worker_uid,
                     "--client-uid", self.api_uid, "--socket", self.socket, "--target", "localhost:443",
                     "--merchant-id", "synthetic-ci-merchant", "--revision", self.revision())

    def check(self, version=None):
        value = self.api.request("/api/v1/admin/plugins/payment/check", "POST", {}, expected=200 if version else 503)
        if version:
            require(value["id"] == "payment.fixture" and value["version"] == version and value["protocol"] == "aster.payment/1"
                    and value["sha256"] == self.manifests[version]["sha256"], "Payment runtime identity differs")
        return value

    def sql(self, query):
        # Queries are fixed SELECTs. Runtime credentials have no schema privileges.
        require(query.startswith("SELECT ") and ";" not in query, "Payment evidence query must be one SELECT")
        result = subprocess.run([str(self.mysql_root / "bin/mysql"), "--defaults-file=" + str(self.scratch / "runtime.cnf"),
                                 "--no-login-paths", "--protocol=SOCKET", "--socket=" + str(self.mysql_socket),
                                 "--batch", "--skip-column-names", self.database, "--execute=" + query],
                                capture_output=True, timeout=15, cwd="/")
        require(result.returncode == 0 and len(result.stdout) <= 65536, "Payment evidence SELECT failed")
        return result.stdout.decode().strip()

    def callback_event(self, invoice):
        require(re.fullmatch(r"[a-f0-9]{32}", invoice["id"]) is not None, "Unexpected fixture invoice identity")
        value = json.loads(self.sql("SELECT JSON_OBJECT('attempt_id',id,'account_id',user_id,'order_id',order_id,"
            "'invoice_id',invoice_id,'amount_minor',amount_minor,'currency',currency,'currency_decimal_places',currency_decimal_places)"
            " FROM payment_attempts WHERE invoice_id='" + invoice["id"] + "'"))
        value.update(provider_id="payment.fixture", event_id="native-event-" + uuid.uuid4().hex,
                     transaction_id="native-transaction-" + uuid.uuid4().hex, merchant_account_id="synthetic-ci-merchant",
                     settled_at=datetime.now(timezone.utc).isoformat(timespec="microseconds").replace("+00:00", "Z"), status="settled")
        return value

    def callback(self, event, expected=200, invalid=False):
        raw = json.dumps(event, sort_keys=True, separators=(",", ":")).encode()
        signature = "0" * 64 if invalid else hmac.new(self.key, raw, hashlib.sha256).hexdigest()
        context = ssl.create_default_context(cafile=str(self.scratch / "ca.pem"))
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        # No browser cookies/CSRF token; the channel signature is the authority
        # presented to the independent verifier, then core owns the settlement.
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
        request = urllib.request.Request(self.api.origin + "/api/v1/payment-callbacks/payment.fixture", method="POST", data=raw,
            headers={"Content-Type": "application/json", "X-Fixture-Signature": signature})
        try:
            response = opener.open(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            require(response.status == expected, "Synthetic callback HTTP outcome differs; expected " + str(expected)
                    + ", actual " + str(response.status))
            data = response.read(65537)
            require(len(data) <= 65536 and self.key not in data, "Callback output exceeded bounds or exposed fixture key")
            return json.loads(data)

    def start_fixture_and_unit(self):
        self.key = secrets.token_bytes(32)
        key_file = self.scratch / "payment-fixture-key"
        key_file.write_bytes(self.key)
        key_file.chmod(0o600)
        self.protected_key = self.config / "payment-fixture-key"
        privileged("install", "-m", "600", key_file, self.protected_key)
        template = (self.project / "deploy/systemd/aster-payment.service").read_text()
        unit = (template.replace("/opt/aster", str(self.opt)).replace("/etc/aster", str(self.config))
                .replace("/run/aster-payment", str(self.run / "payment")).replace("/run/aster-smtp", str(self.run / "smtp"))
                .replace(str(self.config / "plugins.json"), str(self.policy))
                .replace("User=aster-payment", "User=" + self.worker_user).replace("Group=aster-plugins", "Group=" + self.group))
        unit = "\n".join(line for line in unit.splitlines() if not line.startswith("RuntimeDirectory")) + "\n"
        unit = unit.replace(str(self.opt / "current/bin/aster-plugin-host"), str(self.host))
        unit = unit.replace("[Install]", "LoadCredential=payment-fixture-key:" + str(self.protected_key)
            + "\nInaccessiblePaths=-" + str(self.mysql_socket) + " -" + str(self.run / "mysql.sock")
            + "\nEnvironment=ASTER_CREDENTIAL_SENTINEL=synthetic-not-a-secret\n\n[Install]")
        local = self.scratch / "payment.service"
        local.write_text(unit)
        privileged("install", "-m", "644", local, self.worker_file)
        privileged("systemd-analyze", "verify", self.worker_file)
        privileged("systemctl", "daemon-reload")

    def verify_worker_isolation(self, version, pid):
        properties = privileged("systemctl", "show", self.worker_unit, "-p", "User", "-p", "Group", "-p", "NoNewPrivileges",
            "-p", "ProtectSystem", "-p", "StandardOutput", "-p", "StandardError", "-p", "CapabilityBoundingSet", "-p", "ExecStart").decode()
        require("NoNewPrivileges=yes" in properties and "ProtectSystem=strict" in properties
                and "StandardOutput=null" in properties and "StandardError=null" in properties
                and str(self.host) in properties, "Payment service hardening/verified host is inactive")
        status = privileged("cat", "/proc/" + str(pid) + "/status").decode()
        require(re.search(r"^Uid:\s+" + str(self.worker_uid) + r"\s", status, re.M) is not None
                and re.search(r"^NoNewPrivs:\s+1$", status, re.M) is not None
                and re.search(r"^CapEff:\s+0+$", status, re.M) is not None, "Payment worker identity/capabilities differ")
        require(privileged("sha256sum", "/proc/" + str(pid) + "/exe").decode().split()[0] == self.manifests[version]["sha256"],
                "Payment host did not exec the pinned descriptor")
        environment = privileged("cat", "/proc/" + str(pid) + "/environ").split(b"\x00")
        require(set(value.split(b"=", 1)[0] for value in environment if value)
                <= {b"PATH", b"LANG", b"TZ", b"TMPDIR", b"CREDENTIALS_DIRECTORY"}, "Payment worker inherited unapproved environment")
        require(any(value.startswith(b"CREDENTIALS_DIRECTORY=/run/credentials/") for value in environment),
                "Payment systemd credential directory was not forwarded")
        require(self.key not in b"\x00".join(environment), "Payment key was inherited as an environment value")
        for path in (self.config / "api.env", self.config / "secrets/session", self.config / "secrets/smtp-key",
                     self.config / "secrets/runtime.service.dsn", self.protected_key):
            privileged("-u", self.worker_user, "test", "-r", path, expected=1)
        for user in (self.api_user, os.environ["ASTER_PLUGIN_SMTP_USER"]):
            privileged("-u", user, "test", "-r", self.protected_key, expected=1)
        for path in (self.root, self.policy, self.root / "payment.fixture" / version / "plugin"):
            privileged("-u", self.api_user, "test", "-w", path, expected=1)
        info = privileged("stat", "-c", "%F %u %a", self.socket).decode().split()
        require(info[0] == "socket" and int(info[1]) == self.worker_uid and int(info[2], 8) & 7 == 0,
                "Payment socket owner/mode differs")
        privileged("-u", self.web_user, "-g", self.group, "env", "-i", "PATH=/usr/bin:/bin", "python3", "-I", "-c",
                   shared.WRONG_UID_PROBE, self.socket)
        self.checks.append("distinct_api_smtp_payment_uids_verified_fd_exec_private_credential_scrubbed_environment_wrong_peer_denied")

    def failure_diagnostics(self):
        """Collect fixed metadata before close stops the owned worker.

        Neither process output nor environment/credential contents are published.
        A failed probe must not hide the original acceptance failure or prevent
        the parent's registered service/path cleanup.
        """
        def probe(*arguments):
            try:
                result = subprocess.run(["sudo", "-n", *map(str, arguments)], cwd="/", capture_output=True, timeout=5)
            except (OSError, subprocess.TimeoutExpired):
                return None
            return result.stdout if result.returncode == 0 and len(result.stdout) <= 65536 else None

        def metadata(path):
            raw = probe("stat", "--format=%f %u %g %a %h", "--", path)
            if raw is None:
                return {"status": "unavailable"}
            fields = raw.decode("ascii", errors="replace").strip().split()
            if len(fields) != 5 or not re.fullmatch(r"[a-f0-9]{1,8}", fields[0]) or not all(re.fullmatch(r"[0-9]{1,20}", item) for item in fields[1:]) or not re.fullmatch(r"[0-7]{3,4}", fields[3]):
                return {"status": "unavailable"}
            mode = int(fields[0], 16)
            kind = "socket" if stat.S_ISSOCK(mode) else "regular" if stat.S_ISREG(mode) else "directory" if stat.S_ISDIR(mode) else "other"
            return {"status": "available", "kind": kind, "uid": int(fields[1]), "gid": int(fields[2]),
                    "mode": fields[3], "links": int(fields[4])}

        result = {"socket": metadata(self.socket), "credential_source": metadata(getattr(self, "protected_key", self.config / "payment-fixture-key"))}
        properties = ("LoadState", "ActiveState", "SubState", "Result", "MainPID", "NRestarts", "ExecMainCode", "ExecMainStatus", "StatusErrno")
        arguments = ["systemctl", "show", self.worker_unit]
        for name in properties:
            arguments.extend(("-p", name))
        raw = probe(*arguments)
        service = {}
        enums = {
            "LoadState": {"loaded", "not-found", "bad-setting", "error", "masked"},
            "ActiveState": {"active", "inactive", "activating", "deactivating", "failed", "reloading", "maintenance"},
            "SubState": {"running", "dead", "failed", "start", "start-pre", "start-post", "exited", "auto-restart", "auto-restart-queued", "stop", "stop-sigterm", "stop-sigkill", "stop-post"},
            "Result": {"success", "exit-code", "signal", "core-dump", "timeout", "watchdog", "resources", "protocol", "start-limit-hit", "oom-kill"},
        }
        if raw is not None:
            for line in raw.decode("ascii", errors="replace").splitlines():
                key, separator, value = line.partition("=")
                if not separator or key not in properties:
                    continue
                if key in enums:
                    service[key] = value if value in enums[key] else "other"
                elif re.fullmatch(r"[0-9]{1,10}", value):
                    service[key] = int(value)
        result["service"] = service
        pid = service.get("MainPID", 0)
        if isinstance(pid, int) and 1 < pid < 1 << 31:
            raw = probe("cat", "/proc/" + str(pid) + "/status")
            uid = re.search(rb"(?m)^Uid:\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s*$", raw or b"")
            if uid is not None:
                result["process_uids"] = [int(item) for item in uid.groups()]
            raw = probe("sha256sum", "/proc/" + str(pid) + "/exe")
            digest = (raw or b"").split(maxsplit=1)
            if digest and re.fullmatch(rb"[a-f0-9]{64}", digest[0]):
                result["process_executable_sha256"] = digest[0].decode("ascii")
            result["credential_file"] = metadata(Path("/proc") / str(pid) / "root/run/credentials" / self.worker_unit / "payment-fixture-key")
        return result

    def durable_http(self):
        customer = CustomerAPI(self.port, str(self.scratch / "ca.pem"), self.accounts)
        order, invoice = customer.create_order()
        path = "/api/v1/invoices/" + invoice["id"]
        checkout = customer.request(path + "/checkout", "POST", {})
        require(checkout["provider_id"] == "payment.fixture" and checkout["checkout_url"].startswith("https://checkout.example.test/"),
                "Synthetic checkout did not cross the actual core/runtime boundary")
        require(customer.request(path + "/checkout", "POST", {}) == checkout, "Durable checkout replay changed its session")
        customer.request("/api/v1/orders/" + order["id"] + "/cancel", "POST", {"reason": "synthetic pending payment"}, expected=409)
        event = self.callback_event(invoice)
        self.callback(event, expected=400, invalid=True)
        altered = dict(event, amount_minor=event["amount_minor"] + 1)
        self.callback(altered, expected=409)
        require(customer.request(path)["status"] == "unpaid", "Invalid payment evidence changed the invoice")
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            results = list(pool.map(lambda _: self.callback(event), range(4)))
        require(all(row.get("data", {}).get("received") is True for row in results), "Concurrent identical callback did not settle idempotently")
        require(customer.request(path)["status"] == "paid", "Verified callback did not durably settle the invoice")
        require(self.sql("SELECT COUNT(*) FROM payment_settlements WHERE attempt_id='" + event["attempt_id"] + "'") == "1",
                "Callback replay duplicated a settlement")
        require(self.sql("SELECT COUNT(*) FROM outbox_events WHERE event_type='payment.settled' AND aggregate_id='" + order["id"] + "'") == "1",
                "Callback replay duplicated the financial outbox event")
        customer.request("/api/v1/orders/" + order["id"] + "/cancel", "POST", {"reason": "synthetic settled payment"}, expected=409)
        other_order, other_invoice = customer.create_order()
        other_path = "/api/v1/invoices/" + other_invoice["id"]
        customer.request(other_path + "/checkout", "POST", {})
        conflicting = self.callback_event(other_invoice)
        conflicting["transaction_id"] = event["transaction_id"]
        self.callback(conflicting, expected=409)
        require(customer.request(other_path)["status"] == "unpaid", "Reused channel transaction settled another invoice")
        self.checks.append("real_https_core_mysql_callback_hmac_amount_authority_checkout_replay_concurrent_dedup_unique_transaction_cancel_barrier")
        race_order, race_invoice = customer.create_order()
        race_path = "/api/v1/invoices/" + race_invoice["id"]
        barrier = threading.Barrier(2)
        def race(path, body):
            barrier.wait(timeout=5)
            return customer.race_request(path, body)
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            checkout_result = pool.submit(race, race_path + "/checkout", {})
            cancel_result = pool.submit(race, "/api/v1/orders/" + race_order["id"] + "/cancel", {"reason": "synthetic concurrent cancellation"})
            statuses = checkout_result.result(timeout=20), cancel_result.result(timeout=20)
        require(statuses in ((200, 409), (409, 200)), "Checkout and cancellation did not exclude one another atomically")
        if statuses[0] == 200:
            self.callback(self.callback_event(race_invoice))
            require(customer.request(race_path)["status"] == "paid", "Winning checkout could not settle after cancellation exclusion")
        else:
            require(customer.request(race_path)["status"] == "void" and self.sql(
                "SELECT COUNT(*) FROM payment_attempts WHERE invoice_id='" + race_invoice["id"] + "'") == "0",
                "Winning cancellation left an in-flight payment")
        self.checks.append("real_mysql_checkout_cancel_concurrent_lock_exclusion")
        # Keep the evidence to replay after the independently restarted worker.
        self.settled_event = event

    def run_all(self):
        smtp_uid = pwd.getpwnam(os.environ["ASTER_PLUGIN_SMTP_USER"]).pw_uid
        require(len({self.api_uid, self.worker_uid, smtp_uid}) == 3 and min(self.api_uid, self.worker_uid, smtp_uid) > 0,
                "Payment/API/SMTP must use distinct non-root UIDs")
        self.manifests = {}
        packages = self.opt / "payment-test-packages"
        privileged("install", "-d", "-m", "755", packages)
        for version in ("0.1.0", "0.1.1"):
            resource, binary, manifest = fixture_package(self.source, self.scratch, version)
            self.manifests[version] = manifest
            target_resource, target_binary = packages / resource.name, packages / binary.name
            privileged("install", "-m", "644", resource, target_resource)
            privileged("install", "-m", "755", binary, target_binary)
            self.manager("install", target_resource, "--sha256", hashlib.sha256(resource.read_bytes()).hexdigest(),
                         "--binary", target_binary, "--binary-sha256", manifest["sha256"])
        self.start_fixture_and_unit()
        self.unchanged_core("payment_installed_without_activation")
        self.selection("0.1.0")
        require(self.api.request("/api/v1/admin/plugins")["payment"]["status"] == "disabled", "Payment selection implicitly enabled it")
        privileged("-u", self.worker_user, "-g", self.group, "env", "-i", "PATH=/usr/bin:/bin", "timeout", "2", self.host,
                   "--kind", "payment", "--root", self.root, "--policy", self.policy, expected=1)
        privileged("test", "-S", self.socket, expected=1)
        self.manager("enable", "--kind", "payment", "--revision", self.revision())
        privileged("systemctl", "start", self.worker_unit)
        self.verify_worker_isolation("0.1.0", self.wait_worker("0.1.0"))
        self.unchanged_core("payment_0.1.0_active")
        self.durable_http()
        self.selection("0.1.1")
        privileged("systemctl", "stop", self.worker_unit)
        self.manager("enable", "--kind", "payment", "--revision", self.revision())
        privileged("systemctl", "start", self.worker_unit)
        self.wait_worker("0.1.1")
        self.unchanged_core("payment_independent_upgrade")
        self.manager("rollback", "--kind", "payment", "--version", "0.1.0", "--revision", self.revision())
        self.check()
        privileged("systemctl", "restart", self.worker_unit)
        self.wait_worker("0.1.0")
        privileged("systemctl", "reset-failed", self.worker_unit)
        self.callback(self.settled_event)
        self.unchanged_core("payment_independent_rollback_durable_replay")
        executable = self.root / "payment.fixture/0.1.0/plugin"
        original = self.scratch / "payment-original"
        original.write_bytes(executable.read_bytes())
        altered = self.scratch / "payment-altered"
        altered.write_bytes(original.read_bytes() + b"synthetic-integrity-tampering")
        privileged("install", "-m", "755", altered, executable)
        try:
            self.check()
            privileged("-u", self.worker_user, "-g", self.group, "env", "-i", "PATH=/usr/bin:/bin", "timeout", "2", self.host,
                       "--kind", "payment", "--root", self.root, "--policy", self.policy, expected=1)
            self.unchanged_core("payment_tampered_binary_rejected")
        finally:
            privileged("install", "-m", "555", original, executable)
        crash = self.pid(self.worker_unit)
        shared.DIAGNOSTICS.enter("inject_payment_worker_crash")
        self.kill_worker_main()
        require(self.wait_worker("0.1.0", previous=crash) != crash, "Payment worker did not recover independently")
        self.callback(self.settled_event)
        self.unchanged_core("payment_crash_restart_durable_replay")
        self.checks.append("payment_install_enable_independent_upgrade_rollback_hash_tamper_rejection_crash_restart_core_pid_unchanged")
        self.manager("disable", "--kind", "payment", "--revision", self.revision())
        privileged("systemctl", "stop", self.worker_unit)
        self.check()
        self.callback(self.settled_event, expected=503)
        self.unchanged_core("payment_disabled_core_healthy")
        logs = privileged("journalctl", "-u", self.api_unit, "-u", self.worker_unit, "--no-pager", "--output=cat", timeout=15)
        require(self.key not in logs and all(self.accounts[key].encode() not in logs
                for key in ("session", "runtime", "smtp_password", "admin", "customer")), "Native payment journal exposed a fixture credential")
        report = {"suite": "native-synthetic-runtime-payment", "source_commit": shared.command([
            "git", "-C", str(self.source), "rev-parse", "HEAD"]).decode().strip(),
            "environment": shared.runner_environment(), "systemctl_kill_selector": self.kill_scope,
            "checks": self.checks, "stages": self.stages,
            "core_uid": self.api_uid, "smtp_uid": smtp_uid, "payment_uid": self.worker_uid,
            "core_live_pid_unchanged": True, "core_binary_sha256": self.baseline_hash,
            "unpublished_synthetic_versions": ["0.1.0", "0.1.1", "0.1.0"], "actual_https_mysql_settlement": True,
            "external_payment_or_email": False, "production_deployment": False, "skipped": 0, "failures": 0}
        (self.evidence / "payment-native.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
        print("PAYMENT_NATIVE_JSON=" + json.dumps(report, sort_keys=True))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    acceptance = None
    try:
        acceptance = NativePaymentAcceptance(args.source, args.evidence)
        acceptance.run_all()
    except (shared.AcceptanceFailure, OSError, ValueError, KeyError, StopIteration, subprocess.TimeoutExpired,
            concurrent.futures.TimeoutError, threading.BrokenBarrierError, http.client.HTTPException) as failure:
        diagnostic = {"suite": "native-synthetic-runtime-payment", "success": False,
                      "stage": shared.DIAGNOSTICS.stage, "error_class": type(failure).__name__,
                      "last_command": shared.DIAGNOSTICS.last_command, "last_api": shared.DIAGNOSTICS.last_api,
                      "external_payment_or_email": False, "production_deployment": False}
        if acceptance is not None:
            diagnostic["worker_before_stop"] = acceptance.failure_diagnostics()
        (args.evidence / "payment-native-failure.json").write_text(json.dumps(diagnostic, sort_keys=True, indent=2) + "\n")
        print("PAYMENT_NATIVE_FAILURE_JSON=" + json.dumps(diagnostic, sort_keys=True))
        return 1
    finally:
        if acceptance is not None:
            acceptance.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
