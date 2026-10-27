#!/usr/bin/env python3
"""Deterministic release inventories; never include local dependency cache paths."""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n")


def prune(root):
    """Exclude build output, local configuration and recorded test output."""
    for base, dirs, files in os.walk(root, topdown=True):
        for name in list(dirs):
            if name in {".git", "node_modules", "dist", "bin", "artifacts", "secrets", "backups", "test-results", "playwright-report", "__pycache__"}:
                shutil.rmtree(Path(base) / name)
                dirs.remove(name)
        for name in files:
            if ((name.startswith(".env") and name != ".env.example") or
                name.endswith((".log", ".tsbuildinfo", ".sql.gz", "-results.txt", ".pyc"))):
                (Path(base) / name).unlink()


def tool(*args):
    return subprocess.check_output(args, text=True).strip()


def notices(source, target):
    target.mkdir(parents=True, exist_ok=True)
    copied = []
    for path in sorted(source.iterdir()):
        if path.is_file() and re.match(r"^(licen[sc]e|copying|notice|copyright)([._-]|$)", path.name, re.I):
            shutil.copyfile(path, target / path.name)
            copied.append(path.name)
    return copied


def go_dependency_notices(entry, target, linked_modules):
    linked = entry["Path"] in linked_modules
    if not linked:
        # Treat graph-only nodes identically whether a shared cache happens to
        # contain their source or not. They are not included in this binary.
        return {"linked_in_binary": False, "source_status": "module_graph_only", "notice_files": []}
    directory = entry.get("Dir")
    if not directory:
        raise SystemExit("Linked Go dependency source unavailable: " + entry["Path"])
    return {"linked_in_binary": True, "source_status": "source_available", "notice_files": notices(Path(directory), target)}


def go_dependency_metadata(entry, linked_modules):
    # GoVersion and Sum for lazy unlinked nodes can depend on what was downloaded
    # by unrelated builds. Record only the graph's authoritative path/version.
    keys = ("Path", "Version") if not entry.get("Main") and entry["Path"] not in linked_modules else ("Path", "Version", "Sum", "GoVersion", "Main")
    return {key: entry[key] for key in keys if key in entry}


def selected_versions(source):
    protocol = (source / "vps-billing/backend/pkg/pluginapi/protocol.go").read_text()
    versions = re.findall(r'(?m)^\s*(?:const\s+)?CoreVersion\s*=\s*"((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))"', protocol)
    if len(versions) != 1:
        raise SystemExit("Selected source must contain one stable Core version")
    smtp = (source / "vps-billing/backend/plugins/smtp/VERSION").read_text().strip()
    if not re.fullmatch(r"(?:0|[1-9][0-9]{0,5})(?:\.(?:0|[1-9][0-9]{0,5})){2}", smtp):
        raise SystemExit("Invalid SMTP source version")
    return {"core": versions[0], "smtp": smtp}


def verify_version(source, kind, version):
    if kind not in {"core", "smtp"} or selected_versions(source)[kind] != version:
        raise SystemExit("Release version differs from selected source")


def source_copies(source, bundle, kind):
    copies = {"LICENSE": "LICENSE"}
    if kind == "core":
        copies.update({"REPOSITORY-README.md": "README.md", "PROJECT-README.md": "vps-billing/README.md"})
        for prefix in ("docs", "deploy/systemd"):
            for path in sorted((source / "vps-billing" / prefix).rglob("*")):
                if path.is_file():
                    copies[str(path.relative_to(source / "vps-billing"))] = str(path.relative_to(source))
        for path in sorted((source / "vps-billing/backend/migrations").glob("*.sql")):
            copies["migrations/" + path.name] = str(path.relative_to(source))
        for path in ("deploy/nginx.native.conf", "deploy/scripts/backup-native.sh", "deploy/scripts/plugin-manager.py", "deploy/scripts/update-manager.py", "deploy/scripts/core-manager.py", "deploy/scripts/core-operation.py"):
            copies[path] = "vps-billing/" + path
        for path in ("backend/go.mod", "backend/go.sum", "frontend/package.json", "frontend/package-lock.json"):
            copies["manifests/" + Path(path).name] = "vps-billing/" + path
    else:
        copies["README.md"] = "vps-billing/backend/plugins/smtp/README.md"
    return copies


def embedded_notices(bundle):
    # Preserve original top-level third-party texts, not just SPDX labels.
    texts = []
    for path in sorted((bundle / "licenses").rglob("*")):
        if path.is_file():
            texts.append({"path": str(path.relative_to(bundle / "licenses")), "sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "text": path.read_text(encoding="utf-8", errors="strict")})
    return texts


def manifest(source, bundle, modules_path, build_path, commit, version, epoch, kind="core", output=None, binary_root=None):
    verify_version(source, kind, version)
    binary_root = bundle if binary_root is None else Path(binary_root)
    project = source / "vps-billing"
    build_lines = build_path.read_text().splitlines()
    host_path = build_path.with_name("go-host-build.txt")
    host_lines = host_path.read_text().splitlines() if kind == "core" else []
    linked_modules = {fields[1] for line in build_lines + host_lines if len(fields := line.split()) >= 2 and fields[0] == "dep"}
    modules = []
    decoder = json.JSONDecoder()
    raw = modules_path.read_text()
    while raw.strip():
        entry, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        if entry.get("Replace"):
            raise SystemExit("Release disallows local/replacement Go modules; review and update recipe first")
        item = go_dependency_metadata(entry, linked_modules)
        if not entry.get("Main"):
            dest = bundle / "licenses/go" / (entry["Path"].replace("/", "__") + "@" + entry["Version"])
            item.update(go_dependency_notices(entry, dest, linked_modules))
        modules.append(item)
    npm = []
    if kind == "core":
        for path in sorted((project / "frontend/node_modules").rglob("package.json")):
            if path.parent.parent.name != "node_modules" and not (path.parent.parent.name.startswith("@") and path.parent.parent.parent.name == "node_modules"):
                continue
            value = json.loads(path.read_text())
            if not value.get("name") or not value.get("version"):
                continue
            item = {key: value[key] for key in ("name", "version", "license", "licenses") if key in value}
            package_id = re.sub(r"[^a-zA-Z0-9@._-]", "_", value["name"] + "@" + value["version"])
            item["notice_files"] = notices(path.parent, bundle / "licenses/npm" / package_id)
            npm.append(item)
    notices(Path(tool("go", "env", "GOROOT")), bundle / "licenses/go-runtime")
    inputs = {}
    for relative in ("LICENSE", "README.md", "vps-billing/backend/go.mod", "vps-billing/backend/go.sum", "vps-billing/frontend/package.json", "vps-billing/frontend/package-lock.json", "vps-billing/backend/pkg/pluginapi/protocol.go", "vps-billing/backend/plugins/smtp/VERSION"):
        inputs[relative] = hashlib.sha256((source / relative).read_bytes()).hexdigest()
    binaries = ("bin/aster-api", "bin/aster-plugin-host") if kind == "core" else ("plugin",)
    build_info = {"aster-api" if kind == "core" else "plugin": "\n".join([("aster-api" if kind == "core" else "plugin") + ": " + build_lines[0].split(":", 1)[-1].strip(), *build_lines[1:]]) + "\n"}
    if kind == "core":
        build_info["aster-plugin-host"] = "\n".join(["aster-plugin-host: " + host_lines[0].split(":", 1)[-1].strip(), *host_lines[1:]]) + "\n"
    info = {
        "schema": 3, "distribution_format": "raw-binaries-v1", "release_kind": kind, "release_label": version, "source_commit": commit, "source_date_epoch": int(epoch),
        "component_versions": selected_versions(source), "plugin_protocol": "aster.smtp/1", "smtp_core_compatibility": {"min": "0.4.0", "max_exclusive": "0.6.0"},
        "target": "linux/amd64", "cgo_enabled": False,
        "deployment_model": ("single-server native Linux; separately approved nonprivileged Unix-RPC SMTP and payment workers"
                             if kind == "core" else "independent nonprivileged Unix-RPC SMTP worker"),
        "tools": {"go": tool("go", "version"), "tar": tool("tar", "--version").splitlines()[0], "gzip": tool("gzip", "--version").splitlines()[0], "python": tool("python3", "--version")},
        "go_build_flags": ["-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w"] + (["-X main.version=" + version] if kind == "smtp" else []),
        "frontend_build": ["npm ci --no-audit --no-fund", "npm run build"] if kind == "core" else [],
        "input_sha256": inputs, "source_files": source_copies(source, bundle, kind),
        "binary_sha256": {path: hashlib.sha256((binary_root / path).read_bytes()).hexdigest() for path in binaries},
        "binary_assets": ({"bin/aster-api": "aster-api-" + version + "-linux-amd64", "bin/aster-plugin-host": "aster-plugin-host-" + version + "-linux-amd64"} if kind == "core" else {"plugin": "aster-smtp-" + version + "-linux-amd64"}),
        "resources_asset": ("aster-core-" if kind == "core" else "aster-smtp-") + version + "-linux-amd64-resources.tar.gz",
        "go_build_info": build_info,
        "source_delivery": "No manually uploaded project source archive; source remains in Git and GitHub automatic source downloads",
        "dependency_inventory": "Go module graph, linked module and runtime original notice texts; core also includes npm installed metadata/lockfiles; not a full SPDX/CycloneDX SBOM or legal certification",
        "release_status": ("Versioned core with a runtime payment extension interface; no live payment channel bundled; provisioning unavailable; plugins require explicit installation and approval; no production deployment claim"
                           if kind == "core" else "Independent SMTP distribution requiring explicit installation and approval; no production deployment claim"),
    }
    if kind == "core":
        info["tools"].update(node=tool("node", "--version"), npm=tool("npm", "--version"))
        write_json(bundle / "manifests/go-dependencies.json", sorted(modules, key=lambda v: v["Path"]))
        write_json(bundle / "manifests/npm-installed-dependencies.json", sorted(npm, key=lambda v: (v["name"], v["version"])))
        for name, content in build_info.items():
            (bundle / "manifests" / (name + "-build.txt")).write_text(content)
        (bundle / "licenses/README.txt").write_text("Original top-level notices for linked Go modules, Go runtime and installed npm packages. Unlinked graph entries contain path/version metadata only. Nested or missing notices require separate review. This inventory is not a complete SBOM or legal determination. Repository LICENSE is unchanged.\n")
        write_json(bundle / "manifests/BUILD.json", info)
    else:
        write_json(bundle / "THIRD_PARTY_NOTICES.json", {"schema": 1, "go_modules": sorted(modules, key=lambda v: v["Path"]), "go_runtime": tool("go", "version"), "notices": embedded_notices(bundle), "scope": "Original top-level linked-module and Go runtime license/notice texts; not a complete SPDX/CycloneDX SBOM or legal determination"})
        shutil.rmtree(bundle / "licenses")
        write_json(bundle / "manifest.json", {"schema": 1, "id": "email.smtp", "version": version, "protocol": "aster.smtp/1", "core_min": "0.4.0", "core_max_exclusive": "0.6.0", "capabilities": ["smtp.send"], "executable": "plugin", "sha256": info["binary_sha256"]["plugin"]})
        shutil.copyfile(project / "backend/plugins/smtp/README.md", bundle / "README.md")
        sums = "".join(hashlib.sha256(((binary_root if name == "plugin" else bundle) / name).read_bytes()).hexdigest() + "  " + name + "\n" for name in ("LICENSE", "README.md", "THIRD_PARTY_NOTICES.json", "manifest.json", "plugin"))
        (bundle / "SHA256SUMS").write_text(sums)
    if output is not None:
        write_json(Path(output), info)
    return info


if __name__ == "__main__":
    if sys.argv[1] == "prune":
        prune(Path(sys.argv[2]))
    elif sys.argv[1] == "version":
        verify_version(Path(sys.argv[2]), *sys.argv[3:])
    elif sys.argv[1] == "manifest":
        manifest(*(Path(x) for x in sys.argv[2:6]), *sys.argv[6:])
    else:
        raise SystemExit("Unknown action")
