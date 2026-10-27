#!/usr/bin/env python3
"""Explicit non-root Nginx fixture: real headers and private Unix syslog.

No installation, systemd operation, host /dev/log or running API is touched.
Requires an existing nginx >=1.19.4 and openssl; failures never become skips.
The only configuration adaptations are private fixture paths, loopback ports,
the syslog sink and removal of a root-only worker identity for this non-root run.
"""
import argparse
import hashlib
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time


PROJECT = Path(__file__).resolve().parents[2]
HOST = "billing.example.test"
CANARY = "PrivateCanary_OnlySynthetic_92b7"
API_ID, CLIENT_ID, RESOURCE_ID = "a" * 32, "b" * 32, "d" * 32
FIELDS = {"event", "time", "edge_id", "api_request_id", "method", "route",
          "status", "body_bytes", "request_time", "upstream_status", "upstream_response_time"}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def load_operation():
    path = PROJECT / "deploy/scripts/core-operation.py"
    spec = importlib.util.spec_from_file_location("edge_fixture_operation", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def source_hashes():
    return {name: hashlib.sha256((PROJECT / name).read_bytes()).hexdigest() for name in
            ("deploy/scripts/core-operation.py", "deploy/nginx.native.conf", "deploy/ci/edge-observability.py")}


def initial_error_log_options(version_text):
    matched = re.search(r"\bnginx/(\d+)\.(\d+)\.(\d+)\b", version_text)
    require(matched is not None, "Existing Nginx did not report a recognized version")
    version = tuple(map(int, matched.groups()))
    require(version >= (1, 19, 4), "Nginx >=1.19.4 is required; TLS/SNI protections are not relaxed")
    # -e was introduced in 1.19.5, one patch after ssl_reject_handshake.
    # It only replaces the pre-configuration compiled default error destination;
    # -t exit status and stderr still go to the retained private engine log.
    return ["-e", "/dev/null"] if version >= (1, 19, 5) else []


def log_record(datagram):
    require(CANARY.encode() not in datagram and RESOURCE_ID.encode() not in datagram,
            "Private request material entered the syslog datagram")
    record = json.loads(datagram[datagram.index(b"{"):])
    require(set(record) == FIELDS and record["event"] == "edge_request", "Unexpected JSON log schema")
    require(re.fullmatch(r"[0-9a-f]{32}", record["edge_id"]) is not None and
            record["edge_id"] != CLIENT_ID, "Edge ID is not independently generated")
    require(record["api_request_id"] in {"", API_ID}, "Unvalidated upstream request ID entered logs")
    require(record["method"] in {"GET", "POST", "HEAD", "OTHER"}, "Unbounded method entered logs")
    require(type(record["status"]) is int and type(record["body_bytes"]) is int,
            "Status or byte count is not a JSON integer")
    require(float(record["request_time"]) >= 0, "Invalid request duration")
    return record


def port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


class Upstream(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_GET(self):
        self.answer()

    def do_POST(self):
        self.answer()

    def do_PUT(self):
        self.answer()

    def answer(self):
        length = int(self.headers.get("Content-Length", "0"))
        require(length <= 4096, "Fixture request body exceeded its bound")
        self.rfile.read(length)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("X-Request-ID", CANARY if self.path.startswith("/api/invalid-id") else API_ID)
        self.end_headers()
        self.wfile.write(b'{"fixture":true}')


def request(tls_port, certificate, path, method="GET", headers=None, body=None):
    connection = http.client.HTTPConnection(HOST, tls_port, timeout=3)
    context = ssl.create_default_context(cafile=str(certificate))
    connection.sock = context.wrap_socket(socket.create_connection(("127.0.0.1", tls_port), timeout=3),
                                          server_hostname=HOST)
    try:
        connection.request(method, path, body=body, headers=headers or {})
        response = connection.getresponse()
        content = response.read(1 << 20)
        return response.status, {key.lower(): value for key, value in response.getheaders()}, content
    finally:
        connection.close()


def assert_headers(result, expected=200, revalidate=True):
    status, headers, _body = result
    require(status == expected, "Unexpected static/maintenance response status")
    require("script-src 'self'" in headers.get("content-security-policy", "") and
            "frame-ancestors 'none'" in headers.get("content-security-policy", "") and
            headers.get("x-frame-options") == "DENY" and
            headers.get("x-content-type-options") == "nosniff" and
            headers.get("referrer-policy") == "no-referrer", "Static/maintenance security headers lost")
    if expected == 200 and revalidate:
        require("no-cache" in headers.get("cache-control", ""), "HTML was not revalidated")


def profile(name, nginx, openssl, operation, evidence, initial_log_options):
    with tempfile.TemporaryDirectory(prefix="aster-edge-", dir="/tmp") as temporary:
        root = Path(temporary)
        root.chmod(0o700)
        for folder in ("logs", "instance/current/web/assets", "config/nginx-temp"):
            (root / folder).mkdir(parents=True, exist_ok=True)
        (root / "instance/current/web/index.html").write_text("<!doctype html><title>Owned Nginx fixture</title>")
        (root / "instance/current/web/assets/app.js").write_text("/* owned static fixture */")
        certificate, key = root / "cert.pem", root / "key.pem"
        environment = {"PATH": "/usr/bin:/bin", "LC_ALL": "C", "LANG": "C", "HOME": str(root)}
        with evidence.with_name(evidence.name + "-" + name + "-openssl.log").open("xb") as output:
            result = subprocess.run([openssl, "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                                     "-keyout", str(key), "-out", str(certificate), "-days", "1",
                                     "-subj", "/CN=" + HOST, "-addext", "subjectAltName=DNS:" + HOST],
                                    stdin=subprocess.DEVNULL, stdout=output, stderr=output, env=environment,
                                    close_fds=True, timeout=15)
        require(result.returncode == 0, "Fixture certificate generation failed")
        key.chmod(0o600)
        upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        tls_port, http_port = port(), port()
        while http_port == tls_port:
            http_port = port()
        thread = threading.Thread(target=upstream.serve_forever, daemon=True)
        thread.start()
        sink = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
        sink_path = str(root / "syslog.sock")
        sink.bind(sink_path)
        sink.settimeout(0.1)
        datagrams, stopping = [], threading.Event()

        def receive():
            while not stopping.is_set():
                try:
                    datagrams.append(sink.recv(65536))
                except socket.timeout:
                    pass

        collector = threading.Thread(target=receive, daemon=True)
        collector.start()
        config = root / "nginx.conf"
        if name == "generated":
            spec = {"origin": f"https://{HOST}:{tls_port}", "config_root": str(root / "config"),
                    "instance_root": str(root / "instance"), "edge_listen_address": "127.0.0.1",
                    "listen_address": "127.0.0.1:" + str(upstream.server_port),
                    "certificate": str(certificate), "certificate_key": str(key)}
            text = operation.nginx_text(spec).decode().replace("user www-data;\n", "")
        else:
            text = (PROJECT / "deploy/nginx.native.conf").read_text()
            text = text.replace("billing.example.com", HOST).replace("/opt/aster", str(root / "instance"))
            text = text.replace(f"/etc/letsencrypt/live/{HOST}/fullchain.pem", str(certificate))
            text = text.replace(f"/etc/letsencrypt/live/{HOST}/privkey.pem", str(key))
            text = text.replace("127.0.0.1:18080", "127.0.0.1:" + str(upstream.server_port))
            text = re.sub(r"^  listen \[::\].*\n", "", text, flags=re.M)
            text = text.replace("listen 80", f"listen 127.0.0.1:{http_port}")
            text = text.replace("listen 443", f"listen 127.0.0.1:{tls_port}")
            # The real template is included by an installed Nginx http block.
            # An extracted non-root binary has no distribution cache directory;
            # use only private fixture paths, as the generated profile does.
            temp_paths = "".join(f"  {kind}_temp_path {root}/config/nginx-temp/{name};\n"
                                 for kind, name in (("client_body", "client"), ("proxy", "proxy"),
                                                    ("fastcgi", "fastcgi"), ("uwsgi", "uwsgi"), ("scgi", "scgi")))
            text = "events { worker_connections 64; }\nhttp {\n" + temp_paths + text + "\n}\n"
        text = text.replace("unix:/dev/log", "unix:" + sink_path)
        config.write_text("daemon off;\nmaster_process off;\n" +
                          ("error_log /dev/null warn;\npid " + str(root / "nginx.pid") + ";\n"
                           if name == "template" else "") + text)
        output = (root / "nginx-output.log").open("xb")
        process = None
        records, checks = [], []
        try:
            checked = subprocess.run([nginx, *initial_log_options, "-p", str(root) + "/", "-t", "-c", str(config)],
                                     stdin=subprocess.DEVNULL, stdout=output, stderr=output,
                                     env=environment, close_fds=True, timeout=5)
            require(checked.returncode == 0, "Fixture nginx -t rejected the unchanged security capabilities")
            process = subprocess.Popen([nginx, *initial_log_options, "-p", str(root) + "/", "-c", str(config)],
                                       stdin=subprocess.DEVNULL, stdout=output, stderr=output, env=environment,
                                       close_fds=True, start_new_session=True)
            deadline = time.monotonic() + 5
            while True:
                require(process.poll() is None, "Owned Nginx exited before readiness")
                try:
                    with socket.create_connection(("127.0.0.1", tls_port), timeout=0.1):
                        break
                except OSError:
                    require(time.monotonic() < deadline, "Owned Nginx did not listen")
                    time.sleep(0.02)
            for path in ("/index.html", "/settings"):
                assert_headers(request(tls_port, certificate, path))
                checks.append("headers:" + path)
            assert_headers(request(tls_port, certificate, "/assets/app.js"), revalidate=False)
            checks.append("headers:/assets/app.js")
            if name == "generated":
                marker = root / "config/maintenance"
                marker.write_text("Aster maintenance\n")
                assert_headers(request(tls_port, certificate, "/settings"), 503)
                marker.unlink()
                checks.append("maintenance:headers")
            else:
                context = ssl.create_default_context(cafile=str(certificate))
                with socket.create_connection(("127.0.0.1", tls_port), timeout=3) as connection:
                    try:
                        context.wrap_socket(connection, server_hostname="unknown.example.test")
                    except ssl.SSLCertVerificationError:
                        raise RuntimeError("Unknown TLS SNI reached certificate verification instead of rejection") from None
                    except ssl.SSLError:
                        pass
                    else:
                        raise RuntimeError("Unknown TLS SNI was not rejected")
                checks.append("tls:unknown-sni-rejected")
            headers = {"Authorization": "Bearer " + CANARY, "Cookie": "secret=" + CANARY,
                       "Referer": "https://source.invalid/?secret=" + CANARY,
                       "X-Request-ID": CLIENT_ID, "Content-Type": "application/json"}
            for path, method in (("/api/v1/invoices/" + RESOURCE_ID + "?token=" + CANARY, "POST"),
                                 ("/api/" + CANARY + "?token=" + CANARY, "PUT"),
                                 ("/api/invalid-id?token=" + CANARY, "GET")):
                require(request(tls_port, certificate, path, method, headers,
                                '{"secret":"' + CANARY + '"}')[0] == 200, "Fixture upstream request failed")
            management_routes = {
                "/api/v1/admin/orders/" + RESOURCE_ID: "/api/v1/admin/orders/:id",
                "/api/v1/admin/customer-accounts/" + RESOURCE_ID + "/password": "/api/v1/admin/customer-accounts/:id/password",
                "/api/v1/admin/customer-accounts/" + RESOURCE_ID + "/revoke-sessions": "/api/v1/admin/customer-accounts/:id/revoke-sessions",
                "/api/v1/admin/catalog/" + RESOURCE_ID + "/duplicate": "/api/v1/admin/catalog/:id/duplicate",
            }
            for path in management_routes:
                require(request(tls_port, certificate, path + "?secret=" + CANARY, "POST", headers,
                                '{"secret":"' + CANARY + '"}')[0] == 200, "Management upstream fixture failed")
            checks.append("privacy:management-fixed-route-labels")
            checks.append("privacy:query-path-body-headers-and-forged-ids")
            upstream.shutdown()
            upstream.server_close()
            thread.join(2)
            require(request(tls_port, certificate, "/api/failure?secret=" + CANARY,
                            headers=headers)[0] == 502, "Missing upstream did not produce a real edge error")
            checks.append("privacy:real-upstream-failure")
            expected = 12 if name == "generated" else 11
            deadline = time.monotonic() + 3
            while len(datagrams) < expected and time.monotonic() < deadline:
                time.sleep(0.02)
            require(len(datagrams) == expected, "Safe request logs were lost or duplicated")
            records = [log_record(message) for message in datagrams]
            require(any(row["route"] == "/api/v1/invoices/:id" and row["api_request_id"] == API_ID for row in records),
                    "Invoice template/canonical API correlation was not recorded")
            require(all(any(row["route"] == label and row["api_request_id"] == API_ID for row in records)
                        for label in management_routes.values()), "Management fixed route labels were lost")
            require(any(row["method"] == "OTHER" and row["route"] == "OTHER" for row in records),
                    "Unknown method/path did not use fixed labels")
            require(any(row["status"] == 502 and row["api_request_id"] == "" for row in records),
                    "Failed upstream request was not safely observable")
            output.flush()
            require(CANARY.encode() not in (root / "nginx-output.log").read_bytes(),
                    "Raw engine diagnostics leaked request data")
            return {"profile": name, "checks": checks, "requests_logged": len(records), "records": records}
        finally:
            if process is not None and process.poll() is None:
                os.killpg(process.pid, signal.SIGQUIT)
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=3)
            upstream.shutdown()
            upstream.server_close()
            thread.join(2)
            stopping.set()
            collector.join(1)
            sink.close()
            output.flush()
            output.close()
            # Preserve synthetic evidence even on failure; only owned temporary
            # processes/keys/configs are deleted. No host journal is read.
            for suffix, content in (("engine.log", (root / "nginx-output.log").read_bytes()),
                                    ("syslog.log", b"\n".join(datagrams))):
                with evidence.with_name(evidence.name + "-" + name + "-" + suffix).open("xb") as saved:
                    saved.write(content)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--nginx", required=True)
    parser.add_argument("--openssl", default="/usr/bin/openssl")
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    require(os.geteuid() != 0, "Run this fixture as an ordinary user, never root")
    for tool in (args.nginx, args.openssl):
        require(Path(tool).is_absolute() and Path(tool).is_file() and os.access(tool, os.X_OK),
                "Provide existing absolute executable paths; this fixture installs nothing")
    report = Path(args.report)
    require(not report.exists() and report.parent.is_dir(), "Use a new report in an existing private evidence directory")
    info = report.parent.stat()
    require(info.st_uid == os.geteuid() and info.st_mode & 0o077 == 0,
            "Evidence directory must belong to the fixture account and be private")
    result = {"schema": 1, "passed": False, "profiles": [], "inputs_sha256": source_hashes(),
              "scope": "non-root owned Nginx fixtures, not system installation"}
    try:
        version = subprocess.run([args.nginx, "-v"], stdin=subprocess.DEVNULL, capture_output=True,
                                 close_fds=True, timeout=5, env={"PATH": "/usr/bin:/bin", "LC_ALL": "C", "LANG": "C"})
        require(version.returncode == 0, "Existing Nginx version check failed")
        initial_log_options = initial_error_log_options((version.stdout + version.stderr).decode("utf-8", "replace"))
        result["initial_error_log_override"] = bool(initial_log_options)
        operation = load_operation()
        for name in ("generated", "template"):
            result["stage"] = name
            result["profiles"].append(profile(name, args.nginx, args.openssl, operation, report.with_suffix(""), initial_log_options))
        require(source_hashes() == result["inputs_sha256"], "Fixture sources changed during execution")
        result["stage"] = "complete"
        result["passed"] = True
    finally:
        with report.open("x") as output:
            json.dump(result, output, indent=2, sort_keys=True)
            output.write("\n")
    print("PASS: real generated/template Nginx headers, maintenance, safe syslog and request-ID isolation")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as failure:
        print("FAIL: edge fixture: " + type(failure).__name__ + ": " + str(failure), file=sys.stderr)
        sys.exit(1)
