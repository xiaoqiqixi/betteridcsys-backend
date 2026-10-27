#!/usr/bin/env python3
"""Build an offline, independently installable resource worker (no activation)."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

VERSION = "0.1.1"
ID = "resource.aster-cloud"


def package(binary, license_text, readme, go_license, go_patents, go_version):
    manifest = {"schema": 1, "id": ID, "version": VERSION, "protocol": "aster.resource/1",
                "core_min": "0.5.10", "core_max_exclusive": "0.6.0", "capabilities": ["resource.send", "resource.lookup"],
                "executable": "plugin", "sha256": hashlib.sha256(binary).hexdigest()}
    files = {"plugin": binary, "manifest.json": (json.dumps(manifest, sort_keys=True, indent=2) + "\n").encode(),
             "LICENSE": license_text, "README.md": readme,
             "THIRD_PARTY_NOTICES.json": (json.dumps({"go_runtime": {"version": go_version, "license_text": go_license, "patents_text": go_patents}}, indent=2) + "\n").encode()}
    files["SHA256SUMS"] = "".join(hashlib.sha256(value).hexdigest() + "  " + name + "\n" for name, value in sorted(files.items())).encode()
    output = io.BytesIO()
    root = "aster-resource-aster-cloud-" + VERSION + "-linux-amd64"
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
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[3]
    backend = repo / "vps-billing/backend"
    env = os.environ | {"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"}
    command = "./plugins/resourcebridge/cmd/plugin"
    def go(*arguments):
        return subprocess.check_output([args.go, *arguments], cwd=backend, env=env)
    dependencies = go("list", "-mod=readonly", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", command).decode().splitlines()
    if any(item and not item.startswith("aster/billing/") for item in dependencies):
        raise SystemExit("New nonstandard dependency requires explicit license inventory before packaging.")
    goroot = Path(go("env", "GOROOT").decode().strip())
    with tempfile.TemporaryDirectory(prefix="aster-resource-build-") as temp:
        binary = Path(temp) / "plugin"
        go("build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", str(binary), command)
        raw = package(binary.read_bytes(), (repo / "LICENSE").read_bytes(), (repo / "vps-billing/docs/RESOURCE_PLUGINS.md").read_bytes(),
                      (goroot / "LICENSE").read_text(), (goroot / "PATENTS").read_text(), go("version").decode().strip())
    # Do not silently replace a previously approved package.
    with args.output.open("xb") as stream:
        stream.write(raw)
    print(hashlib.sha256(raw).hexdigest() + "  " + args.output.name)


if __name__ == "__main__":
    main()
