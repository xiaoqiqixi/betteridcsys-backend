"""Exercise the real TLS SMTP fixture without MySQL, root, or a hypervisor."""
import json
from pathlib import Path
import smtplib
import socketserver
import ssl
import subprocess
import tempfile
import threading
import unittest

from mail_sink import Handler


class MailSinkTest(unittest.TestCase):
    def test_verified_tls_authentication_and_mail_delivery(self):
        artifacts = Path(__file__).resolve().parents[4] / "artifacts"
        artifacts.mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="mail-test-", dir=artifacts) as temporary:
            root = Path(temporary)
            cert, key = root / "ca.crt", root / "ca.key"
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost", "-keyout", str(key), "-out", str(cert)],
                check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(cert, key)
            with socketserver.ThreadingTCPServer(("127.0.0.1", 0), Handler) as server:
                server.mailbox = root
                server.password = b"Mail-fixture-secret-2026"
                server.socket = context.wrap_socket(server.socket, server_side=True)
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                try:
                    with smtplib.SMTP_SSL("localhost", server.server_address[1], context=ssl.create_default_context(cafile=cert), timeout=5) as client:
                        with self.assertRaises(smtplib.SMTPSenderRefused):
                            client.sendmail("sender@e2e.invalid", ["customer@e2e.invalid"], "Subject: blocked\r\n\r\nblocked")
                        with self.assertRaises(smtplib.SMTPAuthenticationError):
                            client.login("acceptance", "incorrect")
                        client.login("acceptance", server.password.decode())
                        client.sendmail("sender@e2e.invalid", ["customer@e2e.invalid"], "Subject: Verify\r\n\r\nhttps://localhost/verify-email?token=fixture\r\n.dot\r\n")
                    messages = list(root.glob("*.json"))
                    self.assertEqual(len(messages), 1)
                    message = json.loads(messages[0].read_text())
                    self.assertEqual(message["to"], "customer@e2e.invalid")
                    self.assertIn(".dot", message["text"])
                    self.assertEqual(messages[0].stat().st_mode & 0o777, 0o600)
                    self.assertNotIn(server.password.decode(), messages[0].read_text())
                finally:
                    server.shutdown()
                    thread.join(timeout=5)


if __name__ == "__main__":
    unittest.main()
