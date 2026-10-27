#!/usr/bin/env python3
"""Production-mode HTTPS checks on the isolated native CI instance only.

The existing HTTP regression suite is reused with a private test CA. No SSL
verification is disabled; proxy environment variables are never used.
"""
import argparse
import http.client
from http.cookies import SimpleCookie
import importlib.util
import json
import os
from pathlib import Path
import ssl
import socket
import sys
import unittest
import urllib.error
import urllib.parse
import urllib.request
import uuid


def loopback_https_request(port, context, path, *, source="127.0.0.1", method="GET", headers=None, body=None):
    """Use a real bound loopback peer, verified localhost TLS and no proxies/redirects."""
    if source not in ("127.0.0.1", "127.0.0.2", "127.0.0.3"):
        raise ValueError("Only acceptance fixture loopback sources are permitted")
    if not 1024 <= port <= 65535:
        raise ValueError("An unprivileged localhost port is required")
    if not context.check_hostname or context.verify_mode != ssl.CERT_REQUIRED:
        raise ValueError("Verified TLS and hostname checking are required")
    connection = http.client.HTTPSConnection(
        "localhost", port, timeout=10, context=context, source_address=(source, 0)
    )
    try:
        connection.request(method, path, body=body, headers=headers or {})
        response = connection.getresponse()
        return response.status, response.read(), response.headers
    finally:
        connection.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--project", required=True)
    parser.add_argument("--port", required=True, type=int)
    parser.add_argument("--http-port", required=True, type=int)
    parser.add_argument("--ca", required=True)
    parser.add_argument("--accounts", required=True)
    parser.add_argument("--report", required=True)
    parser.add_argument("--expect-smtp-settings", action="store_true")
    args = parser.parse_args()
    if not all(1024 <= port <= 65535 for port in (args.port, args.http_port)):
        parser.error("Only an explicit unprivileged localhost port is accepted")
    if args.port == args.http_port:
        parser.error("The HTTP and HTTPS fixture ports must be different")
    origin = f"https://localhost:{args.port}"
    accounts = json.loads(Path(args.accounts).read_text())
    for key, role in (("CUSTOMER", "customer"), ("SECOND", "second"), ("ADMIN", "admin")):
        os.environ[f"TEST_{key}_EMAIL"] = f"{role}@native-ci.invalid"
        os.environ[f"TEST_{key}_PASSWORD"] = accounts[role]
    context = ssl.create_default_context(cafile=args.ca)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    original_builder = urllib.request.build_opener

    def isolated_opener(*handlers):
        return original_builder(
            urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context), *handlers
        )

    urllib.request.build_opener = isolated_opener
    original_args = sys.argv
    sys.argv = ["security_smoke.py", "--base", origin, "--origin", origin]
    if not args.expect_smtp_settings:
        sys.argv.append("--expect-fresh-cny")
    spec = importlib.util.spec_from_file_location(
        "native_existing_security_smoke", Path(args.project) / "tests/security_smoke.py"
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    sys.argv = original_args

    class NativeHTTPSAcceptance(module.SecuritySmoke):
        def test_unknown_host_rejected(self):
            # The real shipped nginx template closes invalid Host requests with
            # 444 before they reach the application's 421 response.
            try:
                status, _, _ = loopback_https_request(
                    args.port, context, "/api/v1/session", headers={"Host": "attacker.invalid"}
                )
            except http.client.RemoteDisconnected:
                return
            self.assertIn(status, (400, 403, 421, 444))

        def test_http_fixed_redirect_and_unknown_host(self):
            path = "/settings?acceptance=redirect"
            for host in (f"localhost:{args.http_port}", "attacker.invalid"):
                with self.subTest(host=host):
                    connection = http.client.HTTPConnection("127.0.0.1", args.http_port, timeout=5)
                    try:
                        connection.request("GET", path, headers={"Host": host})
                        try:
                            response = connection.getresponse()
                        except http.client.RemoteDisconnected:
                            self.assertEqual(host, "attacker.invalid")
                            continue
                        response.read()
                        if host == "attacker.invalid":
                            self.assertIn(response.status, (400, 403, 421, 444))
                            self.assertIsNone(response.getheader("Location"))
                        else:
                            self.assertEqual(response.status, 308)
                            self.assertEqual(response.getheader("Location"), origin + path)
                    finally:
                        connection.close()

        def test_unknown_sni_rejected_during_handshake(self):
            with socket.create_connection(("127.0.0.1", args.port), timeout=5) as connection:
                try:
                    with context.wrap_socket(connection, server_hostname="unknown.native-ci.invalid"):
                        self.fail("Nginx accepted an unknown SNI hostname")
                except ssl.SSLCertVerificationError:
                    self.fail("Unknown SNI reached certificate verification instead of being rejected by nginx")
                except ssl.SSLError as error:
                    self.assertTrue(
                        "UNRECOGNIZED_NAME" in error.reason or "HANDSHAKE_FAILURE" in error.reason,
                        "Unknown SNI failed without the expected server handshake rejection",
                    )

        def test_forwarded_headers_cannot_bypass_independent_client_limit(self):
            # Each attempt uses a fresh unknown email, isolating the IP limiter
            # from the separate per-email limiter. Both peers use genuine
            # session cookies/CSRF tokens obtained through verified HTTPS.
            sessions = {}
            for peer in ("127.0.0.2", "127.0.0.3"):
                status, raw, headers = loopback_https_request(args.port, context, "/api/v1/session", source=peer)
                self.assertEqual(status, 200)
                cookie = SimpleCookie()
                for value in headers.get_all("Set-Cookie", []):
                    cookie.load(value)
                self.assertTrue(cookie)
                sessions[peer] = {
                    "Cookie": "; ".join(f"{key}={value.value}" for key, value in cookie.items()),
                    "X-CSRF-Token": json.loads(raw)["data"]["csrf_token"],
                    "Origin": origin, "Content-Type": "application/json",
                }

            def rejected_login(peer, expected_status, forwarded=None):
                headers = dict(sessions[peer])
                headers.update(forwarded or {})
                body = json.dumps({
                    "email": f"missing-{uuid.uuid4().hex}@native-ci.invalid",
                    "password": "SyntheticUnknownAccount2026!",
                }).encode()
                status, raw, response_headers = loopback_https_request(
                    args.port, context, "/api/v1/auth/login", source=peer,
                    method="POST", headers=headers, body=body,
                )
                self.assertEqual(status, expected_status)
                self.assertEqual(json.loads(raw)["error"]["code"],
                                 "RATE_LIMITED" if expected_status == 429 else "INVALID_CREDENTIALS")
                if expected_status == 429:
                    self.assertEqual(response_headers.get("Retry-After"), "900")

            for attempt in range(20):
                with self.subTest(peer="127.0.0.2", attempt=attempt + 1):
                    rejected_login("127.0.0.2", 401)
            rejected_login("127.0.0.2", 429)
            rejected_login("127.0.0.3", 401)
            for forged in ({"X-Real-IP": "127.0.0.3"}, {"X-Forwarded-For": "127.0.0.3"},
                           {"X-Real-IP": "192.0.2.123", "X-Forwarded-For": "192.0.2.123, 127.0.0.3"}):
                with self.subTest(forged=forged):
                    rejected_login("127.0.0.2", 429, forged)

        def test_production_cookie_and_origin(self):
            session, headers = self.client.session()
            self.assertFalse(session["demo_mode"])
            cookie = headers.get("Set-Cookie", "")
            for attribute in ("Secure", "HttpOnly", "SameSite=Strict"):
                self.assertIn(attribute, cookie)
            self.assertIn("max-age=31536000", headers.get("Strict-Transport-Security", ""))

        def test_tls_rejects_untrusted_ca(self):
            opener = original_builder(urllib.request.ProxyHandler({}))
            with self.assertRaises(urllib.error.URLError) as failure:
                opener.open(origin + "/healthz", timeout=5)
            self.assertIsInstance(failure.exception.reason, ssl.SSLCertVerificationError)

        def test_static_csp_deep_link_and_asset(self):
            client = isolated_opener()
            with client.open(origin + "/settings", timeout=5) as response:
                page = response.read().decode()
                self.assertEqual(response.status, 200)
                self.assertIn("default-src 'self'", response.headers["Content-Security-Policy"])
                self.assertIn("script-src 'self'", response.headers["Content-Security-Policy"])
                self.assertEqual(response.headers["X-Content-Type-Options"], "nosniff")
                self.assertEqual(response.headers["X-Frame-Options"], "DENY")
            import re
            sources = re.findall(r'<script[^>]+src="([^"]+)"', page)
            self.assertTrue(sources)
            for source in sources:
                self.assertTrue(source.startswith("/assets/"))
                with client.open(origin + source, timeout=5) as response:
                    self.assertEqual(response.status, 200)
                    self.assertIn("javascript", response.headers["Content-Type"])
                    self.assertIn("script-src 'self'", response.headers["Content-Security-Policy"])

        def test_database_backed_readiness(self):
            for path in ("/healthz", "/readyz"):
                self.assertEqual(self.client.request(path)[0], 200)

        def test_smtp_disabled_settings_survive_restore_without_exposing_credentials(self):
            admin = module.Client().login(module.ARGS.admin_email, module.ARGS.admin_password)
            path = "/api/v1/admin/email-settings"
            status, response, _ = admin.request(path)
            self.assertEqual(status, 200)
            settings = response["data"]
            self.assertTrue(settings["key_configured"])
            self.assertFalse(settings["enabled"])
            if not args.expect_smtp_settings:
                self.assertFalse(settings["username_configured"])
                self.assertFalse(settings["password_configured"])
                body = {
                    "revision": settings["revision"], "enabled": False,
                    "host": "localhost", "port": 465, "tls_mode": "implicit_tls",
                    "from_address": "smtp@native-ci.invalid", "from_name": "Native SMTP fixture",
                    "username": accounts["smtp_username"], "password": accounts["smtp_password"],
                }
                status, response, _ = admin.request(path, "POST", body)
                self.assertEqual(status, 200)
                settings = response["data"]
                # A stale writer cannot overwrite the encrypted configuration.
                self.assertEqual(admin.request(path, "POST", body)[0], 409)
            self.assertEqual(settings["host"], "localhost")
            self.assertEqual(settings["port"], 465)
            self.assertEqual(settings["tls_mode"], "implicit_tls")
            self.assertTrue(settings["username_configured"])
            self.assertTrue(settings["password_configured"])
            self.assertTrue(settings["key_configured"])
            # This flag requires decryption and config validation, unlike key_configured.
            status, integrations, _ = admin.request("/api/v1/admin/integrations")
            self.assertEqual(status, 200)
            smtp = next(item for item in integrations["data"]["items"] if item["id"] == "email.smtp")
            self.assertTrue(smtp["configured"])
            self.assertFalse(smtp["enabled"])
            self.assertNotIn("username", settings)
            self.assertNotIn("password", settings)
            self.assertNotIn("credentials_ciphertext", settings)
            serialized = json.dumps(response)
            self.assertTrue(all(accounts[key] not in serialized for key in ("smtp_username", "smtp_password")),
                            "SMTP settings response exposed a fixture credential")
            saved = {key: settings[key] for key in (
                "revision", "enabled", "host", "port", "tls_mode", "from_address", "from_name"
            )}
            self.assertEqual(admin.request(path, "POST", saved, csrf=False)[0], 403)
            self.assertEqual(self.customer().request(path, "POST", saved)[0], 403)
            # Disabled means no network delivery is attempted, even by an admin.
            status, failure, _ = admin.request(path + "/test", "POST", {
                "revision": settings["revision"], "to": "operator@native-ci.invalid"
            })
            self.assertEqual(status, 409)
            self.assertEqual(failure["error"]["code"], "SMTP_DISABLED")

    result = unittest.TextTestRunner(verbosity=2).run(
        unittest.defaultTestLoader.loadTestsFromTestCase(NativeHTTPSAcceptance)
    )
    report = {
        "suite": "native-systemd-nginx-tls-http",
        "tests": result.testsRun,
        "failures": len(result.failures),
        "errors": len(result.errors),
        "skipped": len(result.skipped),
        "verified_tls": result.wasSuccessful() and not result.skipped,
        "production_mode": result.wasSuccessful() and not result.skipped,
        "live_providers": False,
        "financial_fixture": NativeHTTPSAcceptance.financial_fixture,
    }
    Path(args.report).write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
    if not result.wasSuccessful() or result.skipped:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
