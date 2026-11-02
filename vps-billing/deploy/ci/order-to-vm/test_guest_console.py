"""Exercise the serial assertion transport without root or a hypervisor."""
import os
import re
import select
import threading
import unittest

from guest_console import verify_boot


class GuestConsoleTest(unittest.TestCase):
    def exercise(self, outcome):
        master, slave = os.openpty()
        os.set_blocking(slave, False)
        stop = threading.Event()
        received = []
        failures = []

        def guest():
            pending = b""

            def line():
                nonlocal pending
                while not stop.is_set():
                    if b"\n" in pending:
                        value, pending = pending.split(b"\n", 1)
                        received.append(value)
                        return value
                    ready, _, _ = select.select([master], [], [], 0.05)
                    if ready:
                        pending += os.read(master, 8192)
                raise TimeoutError("transport ended")

            try:
                self.assertEqual(line(), b"")
                os.write(master, b"\r\nmanual-e2e login: ")
                self.assertEqual(line(), b"root")
                os.write(master, b"root\r\nPassword: ")
                self.assertEqual(line(), b"private-test-password")
                if outcome == "rejected":
                    os.write(master, b"\r\nLogin incorrect\r\nmanual-e2e login: ")
                    return
                os.write(master, b"\r\n\x1b[32mroot@manual-e2e\x1b[0m:~# ")
                command = line()
                marker = re.search(rb"E2E_BOOT_[a-f0-9]+", command).group(0)
                os.write(master, command + b"\r\n")
                if outcome == "echo-only":
                    stop.wait(2)
                    return
                os.write(master, marker + (b"_READY" if outcome == "ready" else b"_FAILED") + b"\r\nlogout\r\nmanual-e2e login: ")
            except Exception as error:
                failures.append(error)

        thread = threading.Thread(target=guest, daemon=True)
        thread.start()
        try:
            if outcome == "ready":
                verify_boot(slave, "private-test-password", timeout=2)
            else:
                expected = {"failed": "initialization", "rejected": "credential", "echo-only": "deadline"}[outcome]
                with self.assertRaisesRegex(RuntimeError, expected):
                    verify_boot(slave, "private-test-password", timeout=0.3 if outcome == "echo-only" else 2)
            self.assertEqual(received[2], b"private-test-password")
            self.assertFalse(failures, failures)
        finally:
            stop.set()
            thread.join(timeout=2)
            os.close(master)
            os.close(slave)

    def test_completed_boot_and_authenticated_logout(self):
        self.exercise("ready")

    def test_failed_initialization_is_not_accepted(self):
        self.exercise("failed")

    def test_wrong_root_password_is_not_accepted(self):
        self.exercise("rejected")

    def test_echoed_command_is_not_completion_evidence(self):
        self.exercise("echo-only")


if __name__ == "__main__":
    unittest.main()
