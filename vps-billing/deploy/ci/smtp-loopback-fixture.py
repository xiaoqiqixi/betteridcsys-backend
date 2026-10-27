#!/usr/bin/env python3
"""Bounded TLS SMTP fixture for synthetic native-CI messages only.

The listener never binds outside 127.0.0.1. It neither relays nor writes mail,
credentials, recipients or message contents. Its report contains counters only.
"""
import base64
import binascii
import hmac
import socketserver
import ssl
import threading


class LoopbackSMTP:
    def __init__(self, certificate, key, username, password):
        self.context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        self.context.minimum_version = ssl.TLSVersion.TLSv1_2
        self.context.load_cert_chain(certificate, key)
        self.username = username.encode()
        self.password = password.encode()
        self.lock = threading.Lock()
        self.accepted = self.authenticated = self.tls_connections = 0
        self.rejected = 0
        self.hold_reply = threading.Event()
        self.data_received = threading.Event()
        self.release_reply = threading.Event()
        fixture = self

        class Handler(socketserver.StreamRequestHandler):
            def handle(self):
                self.request.settimeout(12)
                try:
                    secured = fixture.context.wrap_socket(self.request, server_side=True)
                except (ssl.SSLError, OSError):
                    return
                self.connection = secured
                self.rfile = secured.makefile("rb")
                self.wfile = secured.makefile("wb")
                with fixture.lock:
                    fixture.tls_connections += 1
                authenticated = False
                sender = recipient = False

                def reply(line):
                    self.wfile.write(line + b"\r\n")
                    self.wfile.flush()

                try:
                    reply(b"220 localhost isolated fixture")
                    for _ in range(16):
                        line = self.rfile.readline(2049)
                        if not line or len(line) > 2048 or not line.endswith(b"\r\n"):
                            return
                        command, _, argument = line[:-2].partition(b" ")
                        command = command.upper()
                        if command == b"EHLO":
                            reply(b"250-localhost\r\n250 AUTH PLAIN")
                        elif command == b"AUTH":
                            try:
                                mechanism, encoded = argument.split(b" ", 1)
                                fields = base64.b64decode(encoded, validate=True).split(b"\x00")
                                valid = (mechanism == b"PLAIN" and len(fields) == 3 and fields[0] == b""
                                         and hmac.compare_digest(fields[1], fixture.username)
                                         and hmac.compare_digest(fields[2], fixture.password))
                            except (ValueError, binascii.Error):
                                valid = False
                            if not valid:
                                with fixture.lock:
                                    fixture.rejected += 1
                                reply(b"535 fixture authentication rejected")
                                return
                            authenticated = True
                            with fixture.lock:
                                fixture.authenticated += 1
                            reply(b"235 authenticated")
                        elif command == b"MAIL" and authenticated:
                            sender = argument.startswith(b"FROM:<") and argument.endswith(b"@native-ci.invalid>")
                            reply(b"250 sender accepted" if sender else b"550 synthetic sender required")
                        elif command == b"RCPT" and sender:
                            recipient = argument == b"TO:<operator@native-ci.invalid>"
                            reply(b"250 recipient accepted" if recipient else b"550 synthetic recipient required")
                        elif command == b"DATA" and recipient:
                            reply(b"354 bounded synthetic data")
                            total = 0
                            subject = False
                            while True:
                                value = self.rfile.readline(4097)
                                total += len(value)
                                if not value or len(value) > 4096 or total > 65536:
                                    return
                                if value == b".\r\n":
                                    break
                                subject |= value == b"Subject: Aster SMTP configuration test\r\n"
                            if not subject:
                                reply(b"550 fixed administrator test required")
                                return
                            with fixture.lock:
                                fixture.accepted += 1
                            fixture.data_received.set()
                            if fixture.hold_reply.is_set():
                                fixture.release_reply.wait(10)
                            reply(b"250 synthetic message accepted without relay")
                            return
                        elif command == b"QUIT":
                            reply(b"221 closing")
                            return
                        elif command == b"RSET":
                            sender = recipient = False
                            reply(b"250 reset")
                        else:
                            reply(b"500 unsupported fixture command")
                            return
                except (OSError, ssl.SSLError):
                    return
                finally:
                    secured.close()

        class Server(socketserver.ThreadingTCPServer):
            daemon_threads = True
            block_on_close = False
            allow_reuse_address = False

            def handle_error(self, request, client_address):
                # Exceptions must never print an authentication command.
                pass

        self.server = Server(("127.0.0.1", 0), Handler)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    def start(self):
        self.thread.start()
        return self

    def report(self):
        with self.lock:
            return {"accepted": self.accepted, "authenticated": self.authenticated,
                    "tls_connections": self.tls_connections, "rejected": self.rejected,
                    "loopback_only": True, "relayed_messages": 0,
                    "minimum_tls_version": "1.2", "message_content_recorded": False}

    def close(self):
        self.release_reply.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(5)
