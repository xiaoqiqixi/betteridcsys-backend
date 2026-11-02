"""Read guest readiness through its private libvirt serial console."""
import os
import re
import secrets
import select
import termios
import time
import tty


def verify_boot(fd, password, timeout=180):
    deadline = time.monotonic() + timeout
    settings = termios.tcgetattr(fd)
    pending = b""

    def expect(pattern, phase):
        nonlocal pending
        while time.monotonic() < deadline:
            clean = re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b"", pending).replace(b"\r", b"")
            if b"Login incorrect" in clean:
                raise RuntimeError("guest rejected its configured root credential")
            match = re.search(pattern, clean)
            if match:
                pending = clean[match.end():]
                return match.group(0)
            ready, _, _ = select.select([fd], [], [], min(0.25, max(0, deadline - time.monotonic())))
            if ready:
                try:
                    pending = (pending + os.read(fd, 8192))[-65536:]
                except BlockingIOError:
                    pass
        raise RuntimeError("guest readiness deadline waiting for " + phase)

    try:
        tty.setraw(fd, termios.TCSANOW)
        os.write(fd, b"\n")
        expect(rb"(?m)^[^\n]*login: *$", "serial login")
        os.write(fd, b"root\n")
        expect(rb"(?m)^Password: *$", "password prompt")
        # Only write the secret after getty has disabled password echo. It never
        # enters argv, the environment, a host file, or an exported transcript.
        os.write(fd, password.encode() + b"\n")
        expect(rb"(?m)^root@[^\n]*# *$", "authenticated root shell")
        marker = ("E2E_BOOT_" + secrets.token_hex(12)).encode()
        command = (b"if cloud-init status --wait >/dev/null 2>&1 && systemctl is-active --quiet systemd-logind; "
                   b"then printf '\\n" + marker + b"_READY\\n'; else printf '\\n" + marker + b"_FAILED\\n'; fi; exit\n")
        os.write(fd, command)
        # Require a standalone output line; the shell's echoed command is not
        # evidence that cloud-init or the power-management service has finished.
        outcome = expect(rb"(?m)^" + marker + rb"_(?:READY|FAILED)$", "cloud-init completion and guest power management")
        if not outcome.endswith(b"_READY"):
            raise RuntimeError("guest initialization or power-management service failed")
        expect(rb"(?m)^[^\n]*login: *$", "guest logout")
    finally:
        termios.tcsetattr(fd, termios.TCSANOW, settings)
