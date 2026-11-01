#!/usr/bin/env python3
"""Disposable Linux CI: exercise the real template's UID/socket/credential boundary."""
import os
from pathlib import Path
import pwd
import shutil
import socket
import struct
import subprocess
import sys
import time


def run(*args):
    return subprocess.run(args, check=True, stdout=subprocess.DEVNULL)


def worker(slot):
    other = "be5-b" if slot == "be5-a" else "be5-a"
    credentials = Path(os.environ["CREDENTIALS_DIRECTORY"])
    assert (credentials / "payment_secret").read_text() == slot
    for path in (Path("/etc/aster/payment") / other / "secret",
                 credentials.parent / ("aster-be5-payment@" + other + ".service") / "payment_secret"):
        try:
            path.read_bytes()
        except (PermissionError, FileNotFoundError):
            pass
        else:
            raise AssertionError("another slot credential was readable")
    assert os.getuid() == pwd.getpwnam("aster-pay-" + slot).pw_uid
    server = socket.socket(socket.AF_UNIX)
    path = "/run/aster-payment/" + slot + "/plugin.sock"
    server.bind(path)
    os.chmod(path, 0o660)
    server.listen()
    while True:
        conn, _ = server.accept()
        _, uid, _ = struct.unpack("3i", conn.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        conn.sendall((slot if uid == 0 else "denied").encode())
        conn.close()


def main():
    assert sys.platform == "linux" and os.geteuid() == 0
    root = Path(__file__).resolve().parents[3]
    probe = Path("/run/aster-be5-payment-probe.py")
    unit = Path("/run/systemd/system/aster-be5-payment@.service")
    slots = ("be5-a", "be5-b")
    for path in (probe, unit, *(Path("/etc/aster/payment") / s for s in slots)):
        assert not path.exists(), "CI fixture must not overwrite an existing host path"
    text = (root / "vps-billing/deploy/systemd/aster-payment@.service").read_text()
    text = text.replace("WorkingDirectory=/opt/aster/plugins", "WorkingDirectory=/run")
    text = text.replace("RequiresMountsFor=/opt/aster/plugins /etc/aster", "RequiresMountsFor=/run /etc/aster")
    text = "\n".join("ExecStart=/usr/bin/python3 " + str(probe) + " --worker %i" if line.startswith("ExecStart=") else line for line in text.splitlines()) + "\n"
    users = []
    try:
        subprocess.run(["groupadd", "-f", "aster-plugins"], check=True)
        for slot in slots:
            user = "aster-pay-" + slot
            run("useradd", "--system", "--no-create-home", "--gid", "aster-plugins", "--shell", "/usr/sbin/nologin", user)
            users.append(user)
            directory = Path("/etc/aster/payment") / slot
            directory.mkdir(parents=True, mode=0o700)
            (directory / "secret").write_text(slot)
            (directory / "secret").chmod(0o600)
        shutil.copyfile(__file__, probe)
        probe.chmod(0o644)
        unit.write_text(text)
        run("systemctl", "daemon-reload")
        for slot in slots:
            run("systemctl", "start", "aster-be5-payment@" + slot + ".service")
        for slot in slots:
            path = Path("/run/aster-payment") / slot / "plugin.sock"
            for _ in range(100):
                if path.exists():
                    break
                time.sleep(0.1)
            assert path.stat().st_uid == pwd.getpwnam("aster-pay-" + slot).pw_uid
            with socket.socket(socket.AF_UNIX) as client:
                client.settimeout(2)
                client.connect(str(path))
                assert client.recv(32).decode() == slot
            other = users[1] if slot == slots[0] else users[0]
            code = "import socket; s=socket.socket(socket.AF_UNIX); s.settimeout(2); s.connect(" + repr(str(path)) + "); assert s.recv(32)==b'denied'"
            run("runuser", "-u", other, "--", "python3", "-c", code)
        print("PASS: two real systemd instances isolate UIDs, sockets, source credentials and LoadCredential mounts")
    finally:
        for slot in slots:
            subprocess.run(["systemctl", "stop", "aster-be5-payment@" + slot + ".service"], stdout=subprocess.DEVNULL)
            shutil.rmtree(Path("/etc/aster/payment") / slot, ignore_errors=True)
        unit.unlink(missing_ok=True)
        probe.unlink(missing_ok=True)
        run("systemctl", "daemon-reload")
        for user in users:
            run("userdel", user)


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "--worker":
        worker(sys.argv[2])
    else:
        main()
