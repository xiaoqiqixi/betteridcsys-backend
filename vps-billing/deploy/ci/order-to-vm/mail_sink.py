"""Bounded TLS-only SMTP mailbox for the disposable acceptance stack."""
import email
import email.policy
import base64
import hmac
import json
from pathlib import Path
import socketserver
import ssl
import sys
import uuid


class Handler(socketserver.StreamRequestHandler):
    def handle(self):
        self.connection.settimeout(15)
        self.wfile.write(b"220 localhost ESMTP acceptance\r\n")
        self.wfile.flush()
        recipient = ""
        authenticated = False
        while True:
            line = self.rfile.readline(8193)
            if not line or len(line) > 8192:
                return
            verb = line.split(b" ", 1)[0].strip().upper()
            if verb in (b"EHLO", b"HELO"):
                reply = b"250-localhost\r\n250-AUTH PLAIN\r\n250 SIZE 262144\r\n"
            elif verb == b"AUTH":
                fields = line.strip().split()
                raw = base64.b64decode(fields[2], validate=True) if len(fields) == 3 and fields[1] == b"PLAIN" else b""
                authenticated = hmac.compare_digest(raw, b"\x00acceptance\x00" + self.server.password)
                reply = b"235 Authenticated\r\n" if authenticated else b"535 Rejected\r\n"
            elif verb == b"MAIL":
                reply = b"250 OK\r\n" if authenticated else b"530 Authenticate first\r\n"
            elif verb == b"RCPT":
                recipient = line.decode("ascii").strip().split(":", 1)[1].strip("<>")
                reply = b"250 OK\r\n"
            elif verb == b"DATA":
                self.wfile.write(b"354 End with a dot\r\n")
                self.wfile.flush()
                parts, size = [], 0
                while True:
                    part = self.rfile.readline(8193)
                    size += len(part)
                    if not part or size > 262144 or len(part) > 8192:
                        return
                    if part == b".\r\n":
                        break
                    parts.append(part[1:] if part.startswith(b"..") else part)
                message = email.message_from_bytes(b"".join(parts), policy=email.policy.default)
                text = "\n".join(p.get_content() for p in message.walk() if p.get_content_type() in ("text/plain", "text/html"))
                path = self.server.mailbox / (uuid.uuid4().hex + ".json")
                path.write_text(json.dumps({"to": recipient, "subject": str(message["Subject"]), "text": text}))
                path.chmod(0o600)
                reply = b"250 Queued\r\n"
            elif verb == b"QUIT":
                self.wfile.write(b"221 Bye\r\n")
                return
            elif verb in (b"RSET", b"NOOP"):
                reply = b"250 OK\r\n"
            else:
                reply = b"500 Unsupported\r\n"
            self.wfile.write(reply)
            self.wfile.flush()


if __name__ == "__main__":
    root = Path(sys.argv[1])
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(root / "finance.crt", root / "private/finance.key")
    with socketserver.ThreadingTCPServer(("127.0.0.1", 19446), Handler) as server:
        server.daemon_threads = True
        server.mailbox = root / "private/mail"
        server.password = (root / "private/smtp-password").read_bytes()
        server.socket = context.wrap_socket(server.socket, server_side=True)
        server.serve_forever()
