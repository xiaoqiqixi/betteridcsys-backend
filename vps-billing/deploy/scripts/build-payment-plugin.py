#!/usr/bin/env python3
"""Build a payment template archive; never install or approve it."""
import argparse
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile

PLUGINS = {"template": "payment-template"}


def package(manifest, binary, license_text, readme, go_license, go_patents, go_version):
    files = {"plugin": binary, "manifest.json": (json.dumps(manifest, sort_keys=True, indent=2) + "\n").encode(),
             "LICENSE": license_text, "README.md": readme,
             "THIRD_PARTY_NOTICES.json": (json.dumps({"go_runtime": {"version": go_version, "license_text": go_license, "patents_text": go_patents}}, indent=2) + "\n").encode()}
    files["SHA256SUMS"] = "".join(hashlib.sha256(value).hexdigest() + "  " + name + "\n" for name, value in sorted(files.items())).encode()
    root = "aster-payment-" + manifest["id"].removeprefix("payment.") + "-" + manifest["version"] + "-linux-amd64"
    output = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=output, mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as archive:
            directory = tarfile.TarInfo(root)
            directory.type, directory.mode = tarfile.DIRTYPE, 0o755
            archive.addfile(directory)
            for name, value in sorted(files.items()):
                member = tarfile.TarInfo(root + "/" + name)
                member.mode, member.size = (0o755 if name == "plugin" else 0o644), len(value)
                archive.addfile(member, io.BytesIO(value))
    return output.getvalue()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plugin", choices=PLUGINS, required=True)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[3]
    backend = repo / "vps-billing/backend"
    native_env = os.environ | {"CGO_ENABLED": "0"}
    native_env.pop("GOOS", None)
    native_env.pop("GOARCH", None)
    linux_env = native_env | {"GOOS": "linux", "GOARCH": "amd64"}
    command = "./plugins/" + PLUGINS[args.plugin] + "/cmd/plugin"

    def go(*arguments, env=linux_env):
        return subprocess.check_output([args.go, *arguments], cwd=backend, env=env)

    dependencies = go("list", "-mod=readonly", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", command).decode().splitlines()
    if any(item and not item.startswith("aster/billing/") for item in dependencies):
        raise SystemExit("New dependencies require explicit license inventory before packaging.")
    goroot = Path(go("env", "GOROOT").decode().strip())
    with tempfile.TemporaryDirectory(prefix="aster-payment-build-") as temporary:
        binary = Path(temporary) / "plugin"
        go("build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", str(binary), command)
        manifest = json.loads(go("run", "-mod=readonly", "./cmd/payment-manifest", "--plugin", args.plugin, "--binary", str(binary), env=native_env))
        raw = package(manifest, binary.read_bytes(), (repo / "LICENSE").read_bytes(), (backend / "plugins" / PLUGINS[args.plugin] / "README.md").read_bytes(),
                      (goroot / "LICENSE").read_text(), (goroot / "PATENTS").read_text(), go("version").decode().strip())
    # Validate with the installer's real parser before publishing local output.
    spec = importlib.util.spec_from_file_location("payment_package_manager", Path(__file__).with_name("plugin-manager.py"))
    manager = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = manager
    spec.loader.exec_module(manager)
    _, files = manager.unpack_archive(raw)
    manager.validate_manifest(manager.strict_json(files["manifest.json"], manager.MAX_MANIFEST), manager.CORE_VERSION)
    manager.validate_elf(files["plugin"])
    if hashlib.sha256(files["plugin"]).hexdigest() != manifest["sha256"]:
        raise SystemExit("Executable changed during packaging.")
    # Never replace a previously reviewed archive.
    with args.output.open("xb") as stream:
        stream.write(raw)
    print(hashlib.sha256(raw).hexdigest() + "  " + args.output.name)


if __name__ == "__main__":
    main()
