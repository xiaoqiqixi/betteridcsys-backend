"""Read-only guest readiness contracts; no VM, root or infrastructure needed."""
import importlib.util
from pathlib import Path
import socket
import threading
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("order_vm_stack", Path(__file__).with_name("stack.py"))
STACK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(STACK)


class GuestReadinessTests(unittest.TestCase):
    def probe(self, payload):
        errors = []
        with socket.socket() as server:
            server.bind(("127.0.0.1", 0))
            server.listen(1)
            server.settimeout(3)
            port = server.getsockname()[1]
            def serve():
                try:
                    with server.accept()[0] as connection:
                        connection.sendall(payload)
                except OSError as error:
                    errors.append(error)
            worker = threading.Thread(target=serve)
            worker.start()
            try:
                result = STACK.guest_ssh_ready("127.0.0.1", port)
            finally:
                worker.join(timeout=4)
            self.assertFalse(worker.is_alive())
            self.assertEqual(errors, [])
            return result

    def test_real_ssh_identification_proves_service_readiness(self):
        for banner in (b"SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.14\r\n",
                       b"SSH-2.0-OpenSSH_9.6p1\n"):
            with self.subTest(banner=banner):
                self.assertTrue(self.probe(banner))

    def test_open_port_without_a_complete_ssh_banner_is_not_ready(self):
        for banner in (b"", b"HTTP/1.1 200 OK\r\n", b"SSH-1.5-legacy\r\n",
                       b"SSH-2.0-\r\n", b"SSH-2.0-OpenSSH", b"SSH-2.0-bad\x00\r\n",
                       b"SSH-2.0-" + b"x" * 248 + b"\r\n"):
            with self.subTest(banner=banner):
                self.assertFalse(self.probe(banner))

    def test_guest_without_a_listening_service_is_not_ready(self):
        with socket.socket() as reserved:
            reserved.bind(("127.0.0.1", 0))
            self.assertFalse(STACK.guest_ssh_ready("127.0.0.1", reserved.getsockname()[1]))

    def test_slow_or_disconnected_guest_is_not_ready(self):
        for failure in (TimeoutError(), ConnectionResetError()):
            with self.subTest(failure=failure), mock.patch.object(STACK.socket, "create_connection", side_effect=failure):
                self.assertFalse(STACK.guest_ssh_ready("127.0.0.1"))


if __name__ == "__main__":
    unittest.main()
