#!/usr/bin/env python3
"""Explicit Linux root install/upgrade executor for a source-verified prepared Core.

A private JSON spec selects one dedicated instance. `plan` is read-only. `apply`
and `resume` require its exact digest; no URLs, shell text or command overrides
are accepted. Neither the API nor a web handoff invokes this tool. Database DDL
is irreversible here: recovery never imports a backup or drops database objects.
"""
import argparse
import contextlib
import datetime
import errno
import fcntl
import grp
import hashlib
import http.client
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import socket
import ssl
import stat
import subprocess
import tempfile
import signal
import sys
import time
import urllib.parse


def validate_bootstrap():
    """Check root CLI Python inputs before importing any sibling tool."""
    if sys.platform != "linux" or os.geteuid() != 0:
        raise RuntimeError("Run this Linux operator executor explicitly as root.")
    base = os.path.dirname(os.path.abspath(__file__))
    for name in ("core-operation.py", "core-manager.py", "update-manager.py", "plugin-manager.py"):
        path = base + "/" + name
        descriptor = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
        try:
            parts = path[1:].split("/")
            for index, part in enumerate(parts):
                directory = index < len(parts) - 1
                flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | (os.O_DIRECTORY if directory else 0)
                child = os.open(part, flags, dir_fd=descriptor)
                os.close(descriptor)
                descriptor = child
                info = os.fstat(child)
                sticky = directory and bool(info.st_mode & stat.S_ISVTX)
                if info.st_uid != 0 or info.st_mode & 0o6000 or info.st_mode & 0o022 and not sticky or not directory and (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1):
                    raise RuntimeError("Executor Python files and ancestors must be protected and root-owned.")
        finally:
            os.close(descriptor)


if __name__ == "__main__":
    try:
        validate_bootstrap()
    except (OSError, RuntimeError):
        print("Core operation rejected: Run explicitly as root from a reviewed protected root-owned tool directory.", file=sys.stderr)
        raise SystemExit(1)


def load_module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).resolve().with_name(filename))
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


manager = load_module("aster_operation_core_manager", "core-manager.py")
updater = manager.updater
plugins = load_module("aster_operation_plugin_manager", "plugin-manager.py")
Rejected = updater.Rejected
SAFE_PATH = re.compile(r"/[A-Za-z0-9_./+:-]+\Z")
ACCOUNT = re.compile(r"ast[a-z0-9_-]{1,27}\Z")
UNIT = re.compile(r"aster[a-z0-9_-]{0,110}\.service\Z")
ENV_KEYS = {"APP_MODE", "APP_ORIGIN", "LISTEN_ADDR", "TRUSTED_PROXY_CIDRS", "SESSION_SECRET_FILE", "MYSQL_DSN_FILE", "MYSQL_ALLOW_INSECURE", "PAYMENT_PROVIDER", "EMAIL_PROVIDER", "PLUGIN_DIRECTORY", "PLUGIN_POLICY_FILE", "SMTP_ENCRYPTION_KEY_FILE", "UPDATE_RELEASE_TOKEN_FILE", "HOST_OPERATION_STATUS_FILE"}
COMMON_KEYS = {"schema", "action", "operation_id", "release_directory", "instance_root", "config_root", "state_directory", "origin", "listen_address", "runtime_user", "api_unit", "socket", "database", "mysql_binary", "mysqldump_binary", "nginx_binary", "nginx_sha256", "edge_mode", "edge_listen_address", "certificate", "certificate_key", "ca_file", "external_nginx_config", "external_nginx_sha256", "status_file"}
INSTALL_KEYS = {"dba_client_config", "bootstrap_email", "bootstrap_name", "bootstrap_password_file", "enable_at_boot"}
UPGRADE_KEYS = {"migrator_dsn_file", "backup_client_config", "acknowledge_backup_restore_limits"}
TERMINAL = {"succeeded", "rolled_back"}
UNIT_DIRECTORY = "/etc/systemd/system"
SYSTEM_BUSCTL = "/usr/bin/busctl"
SERVICE_HOOKS = ("ExecCondition", "ExecStartPre", "ExecStartPost", "ExecStop", "ExecStopPost", "ExecReload")


def require(ok, text):
    updater.require(ok, text)


def protected_path(path):
    updater.exact_path(path)
    require(SAFE_PATH.fullmatch(path) and "//" not in path, "Paths must be canonical absolute paths without configuration metacharacters.")
    return path


def read_file(path, limit=1 << 20, trust=updater._Trust(), private=False):
    return updater.read_protected(protected_path(path), limit, trust, private)


def sha_file(path, trust=updater._Trust(), maximum=updater.MAX_FILE):
    return _sha_protected_file(protected_path(path), trust, maximum)


def sha_prepared_member(root, relative, trust=updater._Trust()):
    """Hash only a canonical runtime member, including scoped license names.

    These are the same component rules used by updater.tar_files for verified
    resources. They do not change the stricter operator/configuration grammar.
    The resulting absolute path still passes protected_open's no-link, owner,
    permissions and canonical-path checks before bounded stable-file hashing.
    """
    protected_path(root)
    require(isinstance(relative, str) and 0 < len(relative) <= 1024 and all(re.fullmatch(r"[A-Za-z0-9@_.+-]+", part) and part not in {".", ".."} for part in relative.split("/")), "Prepared member paths must use canonical reviewed resource components.")
    return _sha_protected_file(root + "/" + relative, trust, updater.MAX_FILE)


def _sha_protected_file(path, trust, maximum):
    descriptor = updater.protected_open(path, trust=trust)
    try:
        before = os.fstat(descriptor)
        require(0 < before.st_size <= maximum, "Protected file exceeds its hashing bound.")
        value, total = hashlib.sha256(), 0
        while True:
            raw = os.read(descriptor, 1 << 20)
            if not raw:
                break
            total += len(raw)
            require(total <= maximum, "Protected file exceeds its hashing bound.")
            value.update(raw)
        require(total == before.st_size and updater.unchanged(before, os.fstat(descriptor)), "Protected input changed while hashing.")
        return value.hexdigest()
    finally:
        os.close(descriptor)


def prepared_release(root, trust=updater._Trust()):
    """Recheck every prepared byte and mode without trusting symlink traversal."""
    protected_path(root)
    descriptor = updater.protected_open(root, True, trust)
    os.close(descriptor)
    raw = read_file(root + "/" + manager.RECEIPT, updater.MAX_JSON, trust)
    receipt = updater.strict_json(raw)
    require(isinstance(receipt, dict) and receipt.get("schema") == 1 and receipt.get("status") == "prepared" and receipt.get("receipt_kind") == "local_operator_preparation_receipt" and receipt.get("source_and_integrity_verified") is True and receipt.get("release_directory") == root and receipt.get("program_executed") is False and receipt.get("official_release_inventory") is False and isinstance(receipt.get("source_sha"), str) and updater.SHA.fullmatch(receipt["source_sha"]), "A verified, source-pinned local preparation receipt is required.")
    updater.stable(receipt.get("version"))
    inventory = receipt.get("files_sha256")
    require(isinstance(inventory, dict) and 8 <= len(inventory) <= 20000 and all(isinstance(k, str) and isinstance(v, str) and updater.HASH.fullmatch(v) for k, v in inventory.items()), "Invalid prepared inventory.")
    found = set()
    for directory, names, files in os.walk(root, followlinks=False):
        descriptor = updater.protected_open(directory, True, trust)
        os.close(descriptor)
        require(all(not os.path.islink(directory + "/" + name) for name in names), "Prepared directories cannot contain symlinks.")
        for name in files:
            relative = os.path.relpath(directory + "/" + name, root)
            require(relative in inventory or relative == manager.RECEIPT, "Unexpected prepared release file.")
            fd = updater.protected_open(directory + "/" + name, trust=trust)
            try:
                info = os.fstat(fd)
                expected = 0o755 if relative in {"bin/aster-api", "bin/aster-plugin-host"} or relative.startswith("deploy/scripts/") else 0o644
                require(stat.S_IMODE(info.st_mode) == expected, "Prepared file permissions changed.")
            finally:
                os.close(fd)
            if relative != manager.RECEIPT:
                require(sha_prepared_member(root, relative, trust) == inventory[relative], "Prepared release bytes changed.")
                found.add(relative)
    require(found == set(inventory), "Prepared release inventory is incomplete.")
    for binary in ("bin/aster-api", "bin/aster-plugin-host"):
        updater.static_elf(read_file(root + "/" + binary, updater.MAX_FILE, trust))
    build = updater.strict_json(read_file(root + "/manifests/BUILD.json", updater.MAX_JSON, trust))
    require(build.get("source_commit") == receipt["source_sha"] and build.get("component_versions", {}).get("core") == receipt["version"] and build.get("release_kind") == "core" and build.get("target") == "linux/amd64", "Prepared BUILD identity differs from its receipt.")
    copies = build.get("source_files")
    required_deploy = {"deploy/scripts/backup-native.sh", "deploy/scripts/plugin-manager.py", "deploy/scripts/update-manager.py", "deploy/scripts/core-manager.py", "deploy/systemd/aster-api.service", "deploy/systemd/aster-smtp.service", "deploy/systemd/api.env.example", "deploy/nginx.native.conf"}
    if updater.stable(receipt["version"]) >= (0, 5, 2):
        required_deploy.add("deploy/scripts/core-operation.py")
    if updater.stable(receipt["version"]) >= (0, 5, 3):
        required_deploy.add("deploy/systemd/aster-payment.service")
    expected_copies = {"LICENSE": "LICENSE", "REPOSITORY-README.md": "README.md", "PROJECT-README.md": "vps-billing/README.md", "manifests/go.mod": "vps-billing/backend/go.mod", "manifests/go.sum": "vps-billing/backend/go.sum", "manifests/package.json": "vps-billing/frontend/package.json", "manifests/package-lock.json": "vps-billing/frontend/package-lock.json"}
    expected_copies.update({name: "vps-billing/" + name for name in required_deploy})
    require(build.get("schema") == 3 and build.get("distribution_format") == "raw-binaries-v1" and build.get("cgo_enabled") is False and isinstance(copies, dict) and 1 <= len(copies) <= 512, "A complete raw Core BUILD/source inventory is required.")
    require(all(copies.get(name) == source for name, source in expected_copies.items()) and set(copies) <= set(inventory), "Required source-bound runtime resources are missing.")
    require(all(name in expected_copies or name.startswith(("docs/", "deploy/systemd/")) and source == "vps-billing/" + name or re.fullmatch(r"migrations/[0-9]{3}_[a-z0-9_]+\.sql", name) and source == "vps-billing/backend/" + name for name, source in copies.items()), "Prepared source mapping differs from the reviewed runtime layout.")
    mandatory = {"bin/aster-api", "bin/aster-plugin-host", "manifests/BUILD.json", "manifests/go-dependencies.json", "manifests/npm-installed-dependencies.json", "manifests/aster-api-build.txt", "manifests/aster-plugin-host-build.txt"} | set(copies)
    require(mandatory <= set(inventory) and all(name in mandatory or name.startswith(("web/", "licenses/")) for name in inventory), "Prepared release has an incomplete or unexpected runtime inventory.")
    require(build.get("binary_sha256") == {name: inventory[name] for name in ("bin/aster-api", "bin/aster-plugin-host")} and inventory.get("LICENSE") == updater.LICENSE_SHA256, "Prepared binary or preserved LICENSE identity differs from BUILD.")
    require({name for name in inventory if name.startswith("migrations/")} == {name for name in copies if name.startswith("migrations/")}, "Prepared SQL source inventory differs.")
    migrations = {}
    for path in sorted(p for p in inventory if p.startswith("migrations/")):
        require(re.fullmatch(r"migrations/[0-9]{3}_[a-z0-9_]+\.sql", path) and int(path.split("/")[1][:3]) == len(migrations) + 1 and build.get("source_files", {}).get(path) == "vps-billing/backend/" + path, "Migration resources must be a complete source-bound prefix.")
        migrations[str(len(migrations) + 1)] = inventory[path]
    require(migrations and "web/index.html" in inventory, "Prepared release needs migrations and compiled web assets.")
    return {"version": receipt["version"], "source_sha": receipt["source_sha"], "receipt_sha256": updater.digest(raw), "files_sha256": inventory, "migration_checksums": migrations}


def binary(path, expected_name, trust=updater._Trust(), expected_sha256=None):
    protected_path(path)
    require(os.path.basename(path) == expected_name, "Only a named native tool binary is allowed.")
    raw = read_file(path, updater.MAX_FILE, trust)
    require(len(raw) >= 64 and raw[:7] == b"\x7fELF\x02\x01\x01" and raw[18:20] == b">\0", "A protected Linux amd64 native tool is required.")
    fd = updater.protected_open(path, trust=trust)
    try:
        require(os.fstat(fd).st_mode & 0o111, "Native tool is not executable.")
    finally:
        os.close(fd)
    value = updater.digest(raw)
    require(expected_sha256 is None or value == expected_sha256, "Native tool digest differs from the reviewed spec.")
    return value


def client_config(path, trust=updater._Trust()):
    raw = read_file(path, 16 << 10, trust, True)
    values, group = {}, False
    for line in raw.decode("utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith(("#", ";")):
            continue
        if line == "[client]":
            require(not group, "Database config permits exactly one client group.")
            group = True
        else:
            key, separator, value = line.partition("=")
            key, value = key.strip(), value.strip()
            require(group and separator and key in {"user", "password"} and key not in values and value and not any(ord(c) < 32 for c in value), "Database config permits only an explicit user and password.")
            values[key] = value
    require(re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}", values.get("user", "")), "Database config requires an explicit safe account.")
    return updater.digest(raw)


def read_environment(path, trust=updater._Trust()):
    raw = read_file(path, 32 << 10, trust)
    result = {}
    for line in raw.decode("ascii").splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        require(separator and key in ENV_KEYS and key not in result and value and not any(c.isspace() or c in "'\"`$\\" for c in value), "EnvironmentFile must use the supported literal non-secret settings only.")
        result[key] = value
    require(result.get("APP_MODE") == "production" and result.get("MYSQL_ALLOW_INSECURE") == "true" and result.get("PAYMENT_PROVIDER") == "payment.disabled" and result.get("EMAIL_PROVIDER") == "email.disabled", "Only production local-socket deployment with disabled providers is supported.")
    require("MYSQL_DSN_FILE" in result and "SESSION_SECRET_FILE" in result, "Runtime secrets must use file references.")
    return result, updater.digest(raw)


def dsn_target(path, spec, trust=updater._Trust(), private=False):
    raw = read_file(path, 16 << 10, trust, private)
    text = raw.decode("ascii").strip()
    require(re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}:[^\s@]+@unix\(" + re.escape(spec["socket"]) + r"\)/" + re.escape(spec["database"]) + r"\?parseTime=true", text), "DSN must select exactly the reviewed local socket and database.")
    return updater.digest(raw)


def current_release(instance_root, trust=updater._Trust()):
    fd = updater.protected_open(instance_root, True, trust)
    try:
        info = os.stat("current", dir_fd=fd, follow_symlinks=False)
        require(stat.S_ISLNK(info.st_mode) and info.st_uid == trust.owner, "Current must be an administrator-owned symlink.")
        target = os.readlink("current", dir_fd=fd)
    finally:
        os.close(fd)
    protected_path(target)
    require(target.startswith(instance_root + "/releases/"), "Current must select a release under this instance.")
    return target


def plugin_facts(environment, version, trust):
    try:
        return installed_plugin_facts(environment, version, trust)
    except plugins.Rejected:
        raise Rejected("Installed plugin policy, integrity or selected-version compatibility verification failed.") from None


def installed_plugin_facts(environment, version, trust):
    root = environment.get("PLUGIN_DIRECTORY")
    policy_path = environment.get("PLUGIN_POLICY_FILE")
    if not root or not policy_path:
        return {"policy_sha256": None, "manifests": {}}
    require(os.path.isdir(root), "Configured plugin root must exist and be protected.")
    fd = updater.protected_open(root, True, trust)
    os.close(fd)
    result = {"policy_sha256": sha_file(policy_path, trust) if os.path.lexists(policy_path) else None, "manifests": {}}
    manifests = {}
    # Retained archives may be incompatible with the target Core. Verify their
    # identity and bytes without selecting, executing or approving them.
    for identifier in os.listdir(root):
        if identifier.startswith("."):
            continue
        plugins.plugin_kind(identifier)
        for selected_version in os.listdir(root + "/" + identifier):
            raw = read_file(root + "/" + identifier + "/" + selected_version + "/manifest.json", 8192, trust)
            identity = plugins.strict_json(raw, 8192)
            manifest, value = plugins.installed_manifest(root, identifier, selected_version, identity.get("core_min"), plugins._Trust(trust.owner, trust.ancestor))
            result["manifests"][identifier + "/" + selected_version] = value
            manifests[identifier + "/" + selected_version] = manifest
    policy = plugins.read_policy(policy_path, plugins._Trust(trust.owner, trust.ancestor))
    for kind in ("smtp", "payment"):
        selected = policy.get(kind)
        if selected:
            key = selected["id"] + "/" + selected["version"]
            require(result["manifests"].get(key) == selected["manifest_sha256"], "Approved plugin manifest differs from its installed file.")
            plugins.validate_manifest(manifests[key], version)
    return result


def validate_spec(spec):
    require(isinstance(spec, dict) and type(spec.get("schema")) is int and spec["schema"] == 1 and spec.get("action") in {"install", "upgrade"}, "Spec must select schema 1 install or upgrade.")
    action = spec["action"]
    required = COMMON_KEYS - {"ca_file", "external_nginx_config", "external_nginx_sha256", "status_file"}
    required |= INSTALL_KEYS if action == "install" else UPGRADE_KEYS
    allowed = COMMON_KEYS | (INSTALL_KEYS if action == "install" else UPGRADE_KEYS)
    require(required <= set(spec) <= allowed, "Spec has missing or unsupported fields.")
    require(isinstance(spec["operation_id"], str) and updater.REQUEST.fullmatch(spec["operation_id"]), "A unique 32-character operation ID is required.")
    require(isinstance(spec["runtime_user"], str) and ACCOUNT.fullmatch(spec["runtime_user"]) and UNIT.fullmatch(spec["api_unit"]), "Use dedicated ast service identities and aster service units.")
    require(re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,19}", spec["database"]) and spec["database"].lower() not in {"mysql", "sys", "information_schema", "performance_schema"}, "Use a dedicated non-system database identifier of at most 20 characters.")
    for key in (COMMON_KEYS | INSTALL_KEYS | UPGRADE_KEYS) & set(spec):
        if key.endswith(("_directory", "_root", "_file", "_binary", "_config")) or key in {"certificate", "certificate_key", "socket"}:
            protected_path(spec[key])
    require(spec["release_directory"].startswith(spec["instance_root"] + "/releases/"), "Release must live under this instance's releases directory.")
    roots = [spec[k] for k in ("instance_root", "config_root", "state_directory")]
    require(all(a != b and not a.startswith(b + "/") and not b.startswith(a + "/") for n, a in enumerate(roots) for b in roots[n+1:]), "Instance, configuration and private state roots must be separate.")
    protected_inputs = [spec[key] for key in ("socket", "certificate", "certificate_key", "mysql_binary", "mysqldump_binary", "nginx_binary")]
    protected_inputs += [spec[key] for key in ("ca_file", "dba_client_config", "bootstrap_password_file", "migrator_dsn_file", "backup_client_config", "external_nginx_config") if key in spec]
    require(all(not path.startswith(spec["release_directory"] + "/") for path in protected_inputs), "Secrets, native tools and database targets cannot live inside a release.")
    if action == "install":
        require(type(spec["enable_at_boot"]) is bool, "Fresh installation requires an explicit boot enablement choice.")
        require(all(not path.startswith(spec["config_root"] + "/") and path != spec["config_root"] for path in protected_inputs), "Install inputs must be outside the new configuration root.")
    if "status_file" in spec:
        require(not any(spec["status_file"] == root or spec["status_file"].startswith(root + "/") for root in roots), "Public status must be outside the private state and instance/configuration roots.")
    require(UNIT.fullmatch(edge_unit(spec)), "Edge unit name exceeds its safe length.")
    parsed = urllib.parse.urlsplit(spec["origin"])
    require(parsed.scheme == "https" and parsed.hostname and parsed.hostname == parsed.hostname.lower() and re.fullmatch(r"[a-z0-9][a-z0-9.-]{0,252}", parsed.hostname) and parsed.username is None and not parsed.path and not parsed.query and not parsed.fragment and spec["origin"] == "https://" + parsed.hostname + ((":" + str(parsed.port)) if parsed.port and parsed.port != 443 else ""), "Origin must be a canonical HTTPS DNS origin.")
    host, separator, port = spec["listen_address"].rpartition(":")
    require(separator and host == "127.0.0.1" and port.isdigit() and str(int(port)) == port and 1024 <= int(port) <= 65535, "API requires an explicit 127.0.0.1 unprivileged port.")
    require(isinstance(spec["edge_listen_address"], str) and str(ipaddress.IPv4Address(spec["edge_listen_address"])) == spec["edge_listen_address"], "Edge listen address must be an explicitly reviewed IPv4 address.")
    require(spec["edge_mode"] in {"managed", "external"}, "Edge mode must be managed or external.")
    require(isinstance(spec["nginx_sha256"], str) and updater.HASH.fullmatch(spec["nginx_sha256"]), "Nginx must have an explicitly reviewed SHA-256.")
    if spec["edge_mode"] == "external":
        require({"external_nginx_config", "external_nginx_sha256"} <= set(spec) and updater.HASH.fullmatch(spec["external_nginx_sha256"]), "External edge requires a protected reviewed full nginx configuration and digest.")
    if action == "install":
        require(isinstance(spec["bootstrap_email"], str) and re.fullmatch(r"[a-z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-z0-9.-]+\.[a-z]{2,}", spec["bootstrap_email"]) and not spec["bootstrap_email"].endswith("@aster.local") and isinstance(spec["bootstrap_name"], str) and 1 <= len(spec["bootstrap_name"].encode()) <= 120 and not any(ord(c) < 32 for c in spec["bootstrap_name"]), "Explicit non-demo bootstrap identity is required.")
    return spec


def unit_inputs(spec, previous, trust):
    raw = read_file(UNIT_DIRECTORY + "/" + spec["api_unit"], 64 << 10, trust)
    expected = api_unit_text(spec, trust, previous["release_directory"])
    legacy = expected.replace(b"WorkingDirectory=", b"SupplementaryGroups=aster-plugins\nWorkingDirectory=", 1)
    require(raw in {expected, legacy}, "Existing API unit requires explicit reviewed conversion to the shipped dedicated-instance template.")
    result = {"api": updater.digest(raw)}
    if spec["edge_mode"] == "managed":
        raw = read_file(UNIT_DIRECTORY + "/" + edge_unit(spec), 64 << 10, trust)
        require(raw == edge_unit_text(spec), "Managed edge unit does not belong to this reviewed instance.")
        config = read_file(spec["config_root"] + "/nginx.conf", 1 << 20, trust)
        require(config == nginx_text(spec), "Managed edge configuration requires explicit reviewed conversion.")
        result.update(edge=updater.digest(raw), nginx_config=updater.digest(config))
    return result


def optional_secret_inputs(environment, user, trust):
    result = {}
    for key in ("SMTP_ENCRYPTION_KEY_FILE", "UPDATE_RELEASE_TOKEN_FILE"):
        if key in environment:
            uid = pwd.getpwnam(user).pw_uid
            secret_trust = updater._Trust(uid, trust.ancestor)
            path = environment[key]
            descriptor = updater.protected_open(path, trust=secret_trust)
            try:
                require(stat.S_IMODE(os.fstat(descriptor).st_mode) == 0o400, "Optional API secrets require the runtime owner and mode 0400.")
            finally:
                os.close(descriptor)
            result[key] = updater.digest(read_file(path, 16 << 10, secret_trust))
    return result


def plan(spec_path, trust=updater._Trust()):
    raw = read_file(spec_path, 64 << 10, trust, True)
    spec = validate_spec(updater.strict_json(raw, 64 << 10))
    if "status_file" in spec:
        parent = os.path.dirname(spec["status_file"])
        descriptor = manager.parent_directory(parent, trust)
        os.close(descriptor)
        current = parent
        while current != "/":
            require(not os.lstat(current).st_mode & 0o7022, "Public status ancestors cannot be shared writable or privileged directories.")
            require(os.lstat(current).st_mode & 0o001, "Public status ancestors must be traversable by the non-root API.")
            current = os.path.dirname(current)
        if os.path.lexists(spec["status_file"]):
            previous_export = updater.strict_json(read_file(spec["status_file"], 64 << 10, trust))
            require(previous_export.get("schema") == 1 and previous_export.get("status") in {"succeeded", "rolled_back", "failed"}, "Public status already records an unfinished operation.")
    release = prepared_release(spec["release_directory"], trust)
    inputs = {"spec_sha256": updater.digest(raw), "target": release, "tools": {}}
    inputs["tools"]["busctl"] = binary(SYSTEM_BUSCTL, "busctl")
    if spec["edge_mode"] == "managed":
        web = pwd.getpwnam("www-data")
        require(web.pw_uid != 0 and web.pw_gid != 0, "Managed Nginx requires the existing non-root www-data identity.")
        inputs["nginx_worker"] = {"uid": web.pw_uid, "gid": web.pw_gid}
    for key, name in (("mysql_binary", "mysql"), ("mysqldump_binary", "mysqldump"), ("nginx_binary", "nginx")):
        inputs["tools"][key] = binary(spec[key], name, trust, spec["nginx_sha256"] if name == "nginx" else None)
    for key in ("certificate", "certificate_key", "ca_file"):
        if key in spec:
            inputs[key] = updater.digest(read_file(spec[key], updater.MAX_FILE, trust, key == "certificate_key"))
    # Validate certificate chain/hostname again at the actual HTTPS request.
    ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER).load_cert_chain(spec["certificate"], spec["certificate_key"])
    if spec["edge_mode"] == "external":
        raw_config = read_file(spec["external_nginx_config"], 1 << 20, trust)
        require(updater.digest(raw_config) == spec["external_nginx_sha256"], "External Nginx configuration digest changed.")
        text = raw_config.decode("ascii")
        for value in (spec["instance_root"] + "/current/web", spec["listen_address"], spec["config_root"] + "/maintenance"):
            require(value in text, "External edge must declare this instance's static root, upstream and maintenance gate.")
        inputs["external_nginx_sha256"] = updater.digest(raw_config)
    if spec["action"] == "install":
        inputs["dba_client_config"] = client_config(spec["dba_client_config"], trust)
        password = read_file(spec["bootstrap_password_file"], 256, trust, True).decode("utf-8").strip()
        require(16 <= len(password.encode()) <= 72 and "password" not in password.lower() and "demo" not in password.lower(), "Bootstrap password file must contain a strong non-example password.")
        inputs["bootstrap_password_sha256"] = updater.digest(password.encode())
        inputs["previous"] = None
    else:
        old = current_release(spec["instance_root"], trust)
        previous = prepared_release(old, trust)
        require(updater.stable(previous["version"]) < updater.stable(release["version"]), "Upgrade target must be newer than the current Core.")
        require(all(release["migration_checksums"].get(k) == v for k, v in previous["migration_checksums"].items()), "Target changes an applied migration prefix.")
        inputs["previous"] = dict(previous, release_directory=old)
        inputs["units"] = unit_inputs(spec, inputs["previous"], trust)
        environment, inputs["environment_sha256"] = read_environment(spec["config_root"] + "/api.env", trust)
        require(environment.get("APP_ORIGIN") == spec["origin"] and environment.get("LISTEN_ADDR") == spec["listen_address"], "Spec origin/listener differs from installed configuration.")
        require(environment.get("HOST_OPERATION_STATUS_FILE") == spec.get("status_file"), "Installed status export reference must match the reviewed spec.")
        inputs["runtime_dsn_sha256"] = dsn_target(environment["MYSQL_DSN_FILE"], spec, trust)
        inputs["session_secret_sha256"] = sha_file(environment["SESSION_SECRET_FILE"], trust)
        inputs["optional_secrets"] = optional_secret_inputs(environment, spec["runtime_user"], trust)
        inputs["migrator_dsn_sha256"] = dsn_target(spec["migrator_dsn_file"], spec, trust, True)
        require(not spec["migrator_dsn_file"].startswith(spec["config_root"] + "/"), "Migration credentials must be outside the runtime configuration root.")
        inputs["backup_client_config"] = client_config(spec["backup_client_config"], trust)
        require(spec["acknowledge_backup_restore_limits"] is True, "Explicit acknowledgment of backup/isolated-restore and maintenance limitations is required.")
        inputs["plugins"] = plugin_facts(environment, release["version"], trust)
    canonical = {"schema": 1, "spec": spec, "inputs": inputs}
    value = updater.digest(updater.json_bytes(canonical))
    return dict(canonical, plan_sha256=value, notice="Review this dedicated instance and exact inputs. Applying provisions credentials, changes schema and activates native services; backups are never automatically imported.")


def safe_environment(values=None):
    return dict({"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "HOME": "/nonexistent"}, **(values or {}))


class Native:
    """Fixed native operations; test doubles exist only through the Python API."""
    def __init__(self, spec, trust=updater._Trust()):
        self.spec, self.trust = spec, trust

    def command_diagnostic(self, argv, exit_code, reason):
        identity = self.spec.get("operation_id", "")
        if not isinstance(identity, str) or not updater.REQUEST.fullmatch(identity):
            identity = "unavailable"
        role = os.path.basename(argv[0]) if argv else "OTHER"
        if role not in {"aster-api", "mysql", "mysqldump", "nginx", "bash", "systemctl", "systemd-analyze", "busctl", "useradd"}:
            role = "OTHER"
        if reason not in {"launch_failed", "guard_failed", "command_failed"}:
            reason = "native_failed"
        try:
            print(json.dumps({"event": "native_command_failed", "operation_id": identity, "phase": "native_command",
                              "tool_role": role, "exit_code": exit_code if type(exit_code) is int else None,
                              "reason": reason}, sort_keys=True), file=sys.stderr)
        except OSError:
            pass  # Logging failure must not replace the operation's real failure.

    def run(self, argv, env=None, data=None, timeout=90, allow_failure=False, identity=None):
        # A bounded private file prevents hostile output from exhausting RAM.
        # Kill the owned process group on deadline; command outcome can still be
        # uncertain, and the durable started checkpoint is retained.
        with tempfile.TemporaryFile() as output, tempfile.TemporaryFile() as source:
            if data is not None:
                source.write(data)
                source.seek(0)
            process = None
            try:
                kwargs = {}
                if identity is not None:
                    account = pwd.getpwnam(identity)
                    require(account.pw_uid != 0 and account.pw_gid != 0, "Runtime inspection requires a non-root identity.")
                    kwargs = {"user": account.pw_uid, "group": account.pw_gid, "extra_groups": []}
                process = subprocess.Popen(argv, stdin=source if data is not None else subprocess.DEVNULL, stdout=output, stderr=subprocess.DEVNULL, env=safe_environment(env), close_fds=True, start_new_session=True, **kwargs)
                deadline = time.monotonic() + timeout
                while process.poll() is None:
                    require(os.fstat(output.fileno()).st_size <= 4 << 20, "Native command output exceeded its bound.")
                    require(time.monotonic() < deadline, "A fixed native operation timed out; its checkpoint requires reconciliation.")
                    time.sleep(0.05)
                require(os.fstat(output.fileno()).st_size <= 4 << 20, "Native command output exceeded its bound.")
                output.seek(0)
                result = subprocess.CompletedProcess(argv, process.returncode, output.read((4 << 20) + 1))
            except OSError:
                self.command_diagnostic(argv, None, "launch_failed")
                raise Rejected("A fixed native operation could not run; inspect the private checkpoint.") from None
            except Rejected:
                self.command_diagnostic(argv, getattr(process, "returncode", None), "guard_failed")
                raise
            finally:
                if process is not None and process.poll() is None:
                    with contextlib.suppress(ProcessLookupError):
                        os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
            if not allow_failure and result.returncode != 0:
                self.command_diagnostic(argv, result.returncode, "command_failed")
                raise Rejected("A fixed native operation failed; raw diagnostics were suppressed.")
            return result

    def sql(self, config, query):
        client_config(config, self.trust)
        binary(self.spec["mysql_binary"], "mysql", self.trust)
        return self.run([self.spec["mysql_binary"], "--defaults-file=" + config, "--no-login-paths", "--protocol=SOCKET", "--socket=" + self.spec["socket"], "--batch", "--skip-column-names"], data=query.encode(), timeout=120).stdout.decode("utf-8").strip()

    def service(self, verb, unit=None):
        require(verb in {"start", "stop", "restart", "daemon-reload", "show", "enable", "is-enabled"}, "Unsupported service action.")
        argv = ["/usr/bin/systemctl", verb]
        if unit:
            require(UNIT.fullmatch(unit), "Unsupported service unit.")
            argv += [unit]
        if verb == "show":
            argv += ["--property=ActiveState", "--value"]
        return self.run(argv).stdout.decode("ascii").strip()

    def unit_absent(self, unit):
        result = self.run(["/usr/bin/systemctl", "show", unit, "--property=LoadState", "--value"], allow_failure=True)
        require(result.stdout.decode("ascii").strip() == "not-found", "Fresh install refuses any existing loaded service unit.")
        return True

    def verify_empty_service_hooks(self):
        """Read all six typed service hook arrays; CLI omission never proves empty.

        v255 src/core/dbus.c:253-315 resolves the exact encoded object path by
        loading unit metadata; src/core/dbus-manager.c:424-503 GetUnit instead
        only finds an already loaded unit, which can be garbage collected.
        Verify typed Unit.Id before its Service arrays. v255
        man/org.freedesktop.systemd1.xml defines Id:s and a(sasbttttuii) hooks;
        src/busctl/busctl.c:1983-2019 unwraps each property variant, so Id has
        data:unit_name and an empty hook has data:[]. No service is started.
        """
        unit = self.spec["api_unit"]
        require(isinstance(unit, str) and UNIT.fullmatch(unit), "Unsupported service unit for typed hook inspection.")
        binary(SYSTEM_BUSCTL, "busctl")
        base = [SYSTEM_BUSCTL, "--system", "--json=short", "--no-pager", "--auto-start=no", "--allow-interactive-authorization=no", "--timeout=10"]
        service = "org.freedesktop.systemd1"
        encoded = "".join(character if character.isascii() and character.isalnum() else "_" + format(ord(character), "02x") for character in unit)
        expected = "/org/freedesktop/systemd1/unit/" + encoded
        raw = self.run(base + ["get-property", service, expected, service + ".Unit", "Id"], timeout=15).stdout
        reply = updater.strict_json(raw, 8192)
        require(isinstance(reply, dict) and set(reply) == {"type", "data"} and reply["type"] == "s" and reply["data"] == unit, "Typed systemd unit identity does not match the reviewed service.")
        result = {}
        for hook in SERVICE_HOOKS:
            raw = self.run(base + ["get-property", service, expected, service + ".Service", hook], timeout=15).stdout
            value = updater.strict_json(raw, 8192)
            require(isinstance(value, dict) and set(value) == {"type", "data"} and value["type"] == "a(sasbttttuii)" and value["data"] == [], "Typed systemd service hooks are missing, nonempty or malformed.")
            result[hook] = True
        return result

    def validate_service(self, release):
        account = pwd.getpwnam(self.spec["runtime_user"])
        require(account.pw_uid != 0 and account.pw_gid != 0, "Existing runtime identity must be non-root.")
        if self.spec["edge_mode"] == "managed":
            web = pwd.getpwnam("www-data")
            require(web.pw_uid != 0 and web.pw_gid != 0 and web.pw_uid != account.pw_uid and web.pw_gid != account.pw_gid, "API and Nginx worker must have separate non-root identities.")
        properties = ("LoadState", "FragmentPath", "DropInPaths", "User", "Group", "WorkingDirectory", "ExecStart", "EnvironmentFiles")
        argv = ["/usr/bin/systemctl", "show", "--all", self.spec["api_unit"]] + ["--property=" + name for name in properties]
        values = dict(line.split("=", 1) for line in self.run(argv).stdout.decode("utf-8").splitlines() if "=" in line)
        current = self.spec["instance_root"] + "/current"
        executable = current + "/bin/aster-api"
        command = re.search(r"(?:^|[ {])path=([^ ;]+) ; argv\[\]=([^;]+?) ;", values.get("ExecStart", ""))
        require(values.get("LoadState") == "loaded" and values.get("FragmentPath") == UNIT_DIRECTORY + "/" + self.spec["api_unit"] and values.get("DropInPaths") == "" and values.get("User") == values.get("Group") == self.spec["runtime_user"] and values.get("WorkingDirectory") == current and command is not None and command[1] == executable and command[2].strip() == executable and values.get("EnvironmentFiles") == self.spec["config_root"] + "/api.env (ignore_errors=no)", "Effective systemd API service differs from the reviewed dedicated instance.")
        self.verify_empty_service_hooks()
        if self.spec["edge_mode"] == "managed":
            argv = ["/usr/bin/systemctl", "show", "--all", edge_unit(self.spec), "--property=FragmentPath", "--property=DropInPaths"]
            edge = dict(line.split("=", 1) for line in self.run(argv).stdout.decode("utf-8").splitlines() if "=" in line)
            require(edge.get("FragmentPath") == UNIT_DIRECTORY + "/" + edge_unit(self.spec) and edge.get("DropInPaths") == "", "Effective managed edge service differs from its reviewed unit.")
        return {"service_configuration_verified": True}

    def api(self, release, environment, command, allow_failure=False):
        require(command in {"-preflight", "-migrate", "-create-user"}, "Unsupported application command.")
        prepared_release(release, self.trust)
        runtime = read_environment(self.spec["config_root"] + "/api.env", self.trust)[0]["MYSQL_DSN_FILE"]
        identity = self.spec["runtime_user"] if command == "-preflight" and environment.get("MYSQL_DSN_FILE") == runtime else None
        return self.run([release + "/bin/aster-api", command], env=environment, timeout=180, allow_failure=allow_failure, identity=identity)

    def preflight(self, release, environment):
        result = self.api(release, environment, "-preflight", allow_failure=True)
        report = updater.strict_json(result.stdout, 128 << 10)
        require(isinstance(report, dict) and report.get("schema") == 1 and report.get("read_only") is True and report.get("configuration_checked") is True, "Application preflight did not return a valid checked report.")
        report["exit_code"] = result.returncode
        return report

    def process_identity(self, release):
        unit = self.spec["api_unit"]
        value = self.run(["/usr/bin/systemctl", "show", unit, "--property=MainPID", "--value"]).stdout.decode("ascii").strip()
        require(value.isdigit() and int(value) > 0, "API service has no live main process.")
        process = "/proc/" + value
        expected = release + "/bin/aster-api"
        require(os.readlink(process + "/exe") == expected, "Running API executable differs from the selected release.")
        expected_info, live_info = os.stat(expected), os.stat(process + "/exe")
        require((expected_info.st_dev, expected_info.st_ino) == (live_info.st_dev, live_info.st_ino), "Running API executable inode differs from the selected release.")
        identity = pwd.getpwnam(self.spec["runtime_user"])
        status_text = Path(process + "/status").read_text()
        uid = next((line.split()[1:] for line in status_text.splitlines() if line.startswith("Uid:")), [])
        require(uid == [str(identity.pw_uid)] * 4 and identity.pw_uid != 0, "API process does not use the dedicated non-root UID.")
        require(sha_file(expected, self.trust) == prepared_release(release, self.trust)["files_sha256"]["bin/aster-api"], "Running release bytes changed.")
        return {"running_executable_verified": True, "runtime_uid_verified": True}

    def edge(self, action):
        require(action in {"test", "start", "reload"}, "Unsupported edge action.")
        binary(self.spec["nginx_binary"], "nginx", self.trust, self.spec["nginx_sha256"])
        config = self.spec["config_root"] + "/nginx.conf" if self.spec["edge_mode"] == "managed" else self.spec["external_nginx_config"]
        if self.spec["edge_mode"] == "external":
            require(sha_file(config, self.trust) == self.spec["external_nginx_sha256"], "External Nginx configuration changed after confirmation.")
        if action == "test":
            self.run([self.spec["nginx_binary"], "-t", "-c", config])
        elif self.spec["edge_mode"] == "managed":
            self.service("start" if action == "start" else "restart", edge_unit(self.spec))
        else:
            self.run([self.spec["nginx_binary"], "-s", "reload", "-c", config])

    def https(self, path):
        parsed = urllib.parse.urlsplit(self.spec["origin"])
        context = ssl.create_default_context(cafile=self.spec.get("ca_file"))
        connection = http.client.HTTPSConnection(parsed.hostname, parsed.port or 443, timeout=5, context=context)
        try:
            connection.request("GET", path, headers={"Host": parsed.netloc, "Connection": "close"})
            response = connection.getresponse()
            content = response.read((2 << 20) + 1)
            require(len(content) <= 2 << 20, "HTTPS health response exceeds its bound.")
            return response.status, content
        except (OSError, ssl.SSLError, http.client.HTTPException):
            raise Rejected("HTTPS certificate, hostname or readiness verification failed.") from None
        finally:
            connection.close()

    def backup(self, old_release, output):
        spec = self.spec
        # Only the independently verified old release's source-bound script runs.
        prepared_release(old_release, self.trust)
        env = {"PATH": os.path.dirname(spec["mysql_binary"]) + ":" + os.path.dirname(spec["mysqldump_binary"]) + ":/usr/bin:/bin"}
        self.run(["/usr/bin/bash", old_release + "/deploy/scripts/backup-native.sh", "--client-config", spec["backup_client_config"], "--socket", spec["socket"], "--database", spec["database"], "--output-dir", output, "--release-directory", old_release], env=env, timeout=600)
        return self.completed_backup(old_release, output)

    def completed_backup(self, old_release, output):
        descriptor = updater.protected_open(output, True, self.trust, True)
        os.close(descriptor)
        folders = os.listdir(output)
        require(len(folders) == 1 and not folders[0].startswith("."), "Fresh complete backup was not published.")
        folder = output + "/" + folders[0]
        descriptor = updater.protected_open(folder, True, self.trust, True)
        try:
            require(set(os.listdir(descriptor)) == {"database.sql.gz", "BACKUP.json", "BACKUP.txt", "SHA256SUMS"}, "Completed backup inventory differs from its fixed format.")
        finally:
            os.close(descriptor)
        for name in ("database.sql.gz", "BACKUP.json", "BACKUP.txt", "SHA256SUMS"):
            descriptor = updater.protected_open(folder + "/" + name, trust=self.trust, private=True)
            os.close(descriptor)
        checksums = updater.checksums(read_file(folder + "/SHA256SUMS", 2048, self.trust, True), {"database.sql.gz", "BACKUP.json", "BACKUP.txt"})
        for name, expected in checksums.items():
            require(sha_file(folder + "/" + name, self.trust, updater.MAX_BACKUP) == expected, "Completed backup file differs from its checksum inventory.")
        release = prepared_release(old_release, self.trust)
        evidence = updater.backup_evidence(folder + "/database.sql.gz", checksums["database.sql.gz"], folder + "/BACKUP.json", release["version"], self.trust)
        metadata = updater.strict_json(read_file(folder + "/BACKUP.json", 16 << 10, self.trust, True))
        require(metadata["database"] == self.spec["database"] and metadata["source_commit"] == release["source_sha"] and metadata["migration_checksums"] == release["migration_checksums"], "Backup selected a different database/source/schema.")
        return dict(evidence, directory=folder, migration_checksums=metadata["migration_checksums"])

    def verify_backup(self, saved, old_release):
        require(isinstance(saved, dict) and isinstance(saved.get("directory"), str) and saved["directory"].startswith(self.spec["state_directory"] + "/" + self.spec["operation_id"] + "/backups/"), "Saved backup is outside this operation's private directory.")
        actual = self.completed_backup(old_release, os.path.dirname(saved["directory"]))
        require(actual == saved, "Saved backup evidence changed after its durable checkpoint.")
        return actual


def edge_unit(spec):
    return spec["api_unit"][:-8] + "-edge.service"


def create_directory(path, mode, trust):
    parent, name = os.path.split(protected_path(path))
    fd = manager.parent_directory(parent, trust, create=True)
    try:
        os.mkdir(name, mode, dir_fd=fd)
        child = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=fd)
        try:
            info = os.fstat(child)
            require(info.st_uid == trust.owner and stat.S_ISDIR(info.st_mode), "New directory ownership changed during creation.")
            os.fchmod(child, mode)
            os.fsync(child)
        finally:
            os.close(child)
        os.fsync(fd)
    finally:
        os.close(fd)


def create_backup_directory(path, trust):
    """Create a private backup output without an inherited default POSIX ACL.

    A default ACL can determine child file permissions instead of umask. Only
    this newly created directory is changed; source-bound historical backup
    scripts and existing administrator directories remain untouched.
    """
    descriptor = -1
    try:
        create_directory(path, 0o700, trust)
        descriptor = updater.protected_open(path, True, trust, True)
        before = os.fstat(descriptor)
        absent = {errno.ENODATA, errno.EOPNOTSUPP}
        try:
            os.removexattr(descriptor, "system.posix_acl_default")
        except OSError as failure:
            require(failure.errno in absent, "New private backup directory ACL could not be normalized.")
        try:
            os.getxattr(descriptor, "system.posix_acl_default")
        except OSError as failure:
            require(failure.errno in absent, "New private backup directory ACL could not be normalized.")
        else:
            raise Rejected("New private backup directory ACL could not be normalized.")
        os.fchmod(descriptor, 0o700)
        after = os.fstat(descriptor)
        require(all(getattr(before, key) == getattr(after, key) for key in ("st_dev", "st_ino", "st_uid", "st_gid", "st_nlink")) and after.st_uid == trust.owner and stat.S_ISDIR(after.st_mode) and stat.S_IMODE(after.st_mode) == 0o700, "New private backup directory metadata changed during creation.")
        os.fsync(descriptor)
    except OSError:
        raise Rejected("New private backup directory ACL could not be normalized.") from None
    finally:
        if descriptor >= 0:
            os.close(descriptor)


def new_file(path, raw, mode=0o600, gid=None, trust=updater._Trust()):
    parent, name = os.path.split(protected_path(path))
    fd = updater.protected_open(parent, True, trust)
    try:
        manager.write_file(fd, name, raw, mode)
        if gid is not None:
            os.chown(name, trust.owner, gid, dir_fd=fd, follow_symlinks=False)
        os.fsync(fd)
    finally:
        os.close(fd)


def set_current(spec, target, expected, trust):
    fd = updater.protected_open(spec["instance_root"], True, trust)
    temporary = ".current-" + secrets.token_hex(16)
    try:
        if expected is None:
            require(not os.path.lexists(spec["instance_root"] + "/current"), "Install cannot replace an existing current path.")
        else:
            require(current_release(spec["instance_root"], trust) == expected, "Active release changed outside this operation.")
        os.symlink(target, temporary, dir_fd=fd)
        if expected is None:
            manager.rename_new(fd, temporary, fd, "current")
        else:
            os.replace(temporary, "current", src_dir_fd=fd, dst_dir_fd=fd)
        os.fsync(fd)
    finally:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(temporary, dir_fd=fd)
        os.close(fd)


def runtime_environment(spec):
    config = spec["config_root"]
    result = {"APP_MODE": "production", "APP_ORIGIN": spec["origin"], "LISTEN_ADDR": spec["listen_address"], "TRUSTED_PROXY_CIDRS": "127.0.0.1/32", "SESSION_SECRET_FILE": config + "/secrets/session_secret", "MYSQL_DSN_FILE": config + "/secrets/mysql_runtime_dsn", "MYSQL_ALLOW_INSECURE": "true", "PAYMENT_PROVIDER": "payment.disabled", "EMAIL_PROVIDER": "email.disabled", "PLUGIN_DIRECTORY": spec["instance_root"] + "/plugins", "PLUGIN_POLICY_FILE": config + "/plugins.json"}
    if spec.get("status_file"):
        result["HOST_OPERATION_STATUS_FILE"] = spec["status_file"]
    return result


def api_unit_text(spec, trust=updater._Trust(), release=None):
    text = read_file((release or spec["release_directory"]) + "/deploy/systemd/aster-api.service", trust=trust).decode("ascii")
    text = text.replace("/opt/aster", spec["instance_root"]).replace("/etc/aster", spec["config_root"])
    text = text.replace("User=aster\n", "User=" + spec["runtime_user"] + "\n").replace("Group=aster\n", "Group=" + spec["runtime_user"] + "\n")
    # Independent plugin installation may add its explicitly reviewed socket group.
    text = text.replace("SupplementaryGroups=aster-plugins\n", "")
    return text.encode()


def nginx_logging_text():
    # Keep this block identical to nginx.native.conf. Only fixed route/method
    # labels leave Nginx; request payloads and raw engine diagnostics do not.
    return '''# BEGIN ASTER PRIVACY LOGGING
map $request_method $aster_log_method {
  default OTHER;
  "~^GET$" GET;
  "~^POST$" POST;
  "~^HEAD$" HEAD;
}
map $uri $aster_log_route {
  default OTHER;
  / /web;
  /index.html /web;
  "~^/(overview|services|plans|orders|invoices|customers|audit|integrations|finance|settings|updates|account)$" /web;
  "~^/assets/" /assets/:asset;
  /healthz /healthz;
  /readyz /readyz;
  /api/v1/session /api/v1/session;
  /api/v1/currencies /api/v1/currencies;
  /api/v1/plans /api/v1/plans;
  /api/v1/site-settings /api/v1/site-settings;
  /api/v1/services /api/v1/services;
  /api/v1/orders /api/v1/orders;
  /api/v1/invoices /api/v1/invoices;
  /api/v1/audit /api/v1/audit;
  /api/v1/audit-events /api/v1/audit-events;
  /api/v1/admin/audit-events /api/v1/admin/audit-events;
  /api/v1/overview /api/v1/overview;
  /api/v1/auth/login /api/v1/auth/login;
  /api/v1/auth/logout /api/v1/auth/logout;
  /api/v1/admin/overview /api/v1/admin/overview;
  /api/v1/admin/customers /api/v1/admin/customers;
  /api/v1/admin/customer-accounts /api/v1/admin/customer-accounts;
  /api/v1/admin/catalog /api/v1/admin/catalog;
  /api/v1/admin/services /api/v1/admin/services;
  /api/v1/admin/orders /api/v1/admin/orders;
  /api/v1/admin/invoices /api/v1/admin/invoices;
  /api/v1/admin/currencies /api/v1/admin/currencies;
  /api/v1/admin/plan-prices /api/v1/admin/plan-prices;
  /api/v1/admin/site-settings /api/v1/admin/site-settings;
  /api/v1/admin/email-settings /api/v1/admin/email-settings;
  /api/v1/admin/email-settings/test /api/v1/admin/email-settings/test;
  /api/v1/admin/updates /api/v1/admin/updates;
  /api/v1/admin/updates/check /api/v1/admin/updates/check;
  /api/v1/admin/updates/handoff /api/v1/admin/updates/handoff;
  /api/v1/admin/updates/operation /api/v1/admin/updates/operation;
  /api/v1/admin/plugins /api/v1/admin/plugins;
  /api/v1/admin/plugins/check /api/v1/admin/plugins/check;
  /api/v1/admin/plugins/payment/check /api/v1/admin/plugins/payment/check;
  /api/v1/admin/integrations /api/v1/admin/integrations;
  "~^/api/v1/invoices/[0-9a-f]{32}$" /api/v1/invoices/:id;
  "~^/api/v1/admin/invoices/[0-9a-f]{32}$" /api/v1/admin/invoices/:id;
  "~^/api/v1/invoices/[0-9a-f]{32}/checkout$" /api/v1/invoices/:id/checkout;
  "~^/api/v1/admin/invoices/[0-9a-f]{32}/checkout$" /api/v1/admin/invoices/:id/checkout;
  "~^/api/v1/orders/[0-9a-f]{32}$" /api/v1/orders/:id;
  "~^/api/v1/admin/orders/[0-9a-f]{32}$" /api/v1/admin/orders/:id;
  "~^/api/v1/admin/customer-accounts/[0-9a-f]{32}$" /api/v1/admin/customer-accounts/:id;
  "~^/api/v1/admin/customer-accounts/[0-9a-f]{32}/password$" /api/v1/admin/customer-accounts/:id/password;
  "~^/api/v1/admin/customer-accounts/[0-9a-f]{32}/revoke-sessions$" /api/v1/admin/customer-accounts/:id/revoke-sessions;
  "~^/api/v1/admin/catalog/[A-Za-z0-9_.-]{1,64}$" /api/v1/admin/catalog/:id;
  "~^/api/v1/admin/catalog/[A-Za-z0-9_.-]{1,64}/duplicate$" /api/v1/admin/catalog/:id/duplicate;
  "~^/api/v1/admin/catalog/[A-Za-z0-9_.-]{1,64}/product-configuration$" /api/v1/admin/catalog/:id/product-configuration;
  "~^/api/v1/admin/orders/[0-9a-f]{32}/provisioning-jobs$" /api/v1/admin/orders/:id/provisioning-jobs;
  "~^/api/v1/admin/provisioning-jobs/[0-9a-f]{32}$" /api/v1/admin/provisioning-jobs/:id;
  "~^/api/v1/orders/[0-9a-f]{32}/cancel$" /api/v1/orders/:id/cancel;
  "~^/api/v1/admin/orders/[0-9a-f]{32}/cancel$" /api/v1/admin/orders/:id/cancel;
  "~^/api/v1/payment-callbacks/[a-z][a-z0-9_.-]{1,63}$" /api/v1/payment-callbacks/:provider;
}
map $upstream_http_x_request_id $aster_api_request_id {
  default "";
  "~^[0-9a-f]{32}$" $upstream_http_x_request_id;
}
log_format aster_access escape=json
  '{"event":"edge_request","time":"$time_iso8601",'
  '"edge_id":"$request_id","api_request_id":"$aster_api_request_id",'
  '"method":"$aster_log_method","route":"$aster_log_route",'
  '"status":$status,"body_bytes":$body_bytes_sent,"request_time":"$request_time",'
  '"upstream_status":"$upstream_status","upstream_response_time":"$upstream_response_time"}';
# END ASTER PRIVACY LOGGING'''


def nginx_text(spec):
    hostname = urllib.parse.urlsplit(spec["origin"]).hostname
    port = urllib.parse.urlsplit(spec["origin"]).port or 443
    config = spec["config_root"]
    headers = '\n'.join("proxy_set_header " + line + ";" for line in ('Host $http_host', 'X-Real-IP $remote_addr', 'X-Forwarded-For $remote_addr', 'X-Forwarded-Proto https', 'X-Forwarded-Host ""', 'Forwarded ""'))
    return f'''user www-data;
pid {config}/nginx.pid;
error_log /dev/null warn;
worker_processes 1;
events {{ worker_connections 512; }}
http {{
{nginx_logging_text()}
  client_body_temp_path {config}/nginx-temp/client;
  proxy_temp_path {config}/nginx-temp/proxy;
  fastcgi_temp_path {config}/nginx-temp/fastcgi;
  uwsgi_temp_path {config}/nginx-temp/uwsgi;
  scgi_temp_path {config}/nginx-temp/scgi;
  default_type application/octet-stream;
  types {{ text/html html; text/css css; application/javascript js; image/svg+xml svg; image/png png; }}
  server {{
    listen {spec["edge_listen_address"]}:{port} ssl;
    server_name {hostname};
    ssl_certificate {spec['certificate']};
    ssl_certificate_key {spec['certificate_key']};
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_tickets off;
    server_tokens off;
    # Engine errors can contain the raw request/query/referrer. Keep only the
    # allowlisted JSON request summary; inspect service/config failures separately.
    error_log /dev/null warn;
    access_log syslog:server=unix:/dev/log,tag=aster_edge,severity=info,nohostname aster_access;
    if ($host != {hostname}) {{ return 444; }}
    root {spec['instance_root']}/current/web;
    add_header X-Content-Type-Options nosniff always;
    add_header X-Frame-Options DENY always;
    add_header Referrer-Policy no-referrer always;
    add_header Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'" always;
    client_max_body_size 64k;
    location /api/ {{
      if (-f {config}/maintenance) {{ return 503; }}
      proxy_pass http://{spec['listen_address']};
      {headers}
      proxy_connect_timeout 5s;
      proxy_read_timeout 30s;
      proxy_buffering off;
    }}
    location ~ ^/(readyz|healthz)$ {{
      proxy_pass http://{spec['listen_address']};
      {headers}
      proxy_connect_timeout 5s;
      proxy_read_timeout 30s;
    }}
    location ~ /\\. {{ deny all; }}
    location / {{
      if (-f {config}/maintenance) {{ return 503; }}
      # Unlike add_header, expires does not discard the server security headers.
      expires -1;
      try_files $uri $uri/ /index.html;
    }}
  }}
}}
'''.encode()


def edge_unit_text(spec):
    return f'''[Unit]
Description=Aster dedicated HTTPS edge
After=network.target
[Service]
Type=forking
PIDFile={spec['config_root']}/nginx.pid
ExecStart={spec['nginx_binary']} -c {spec['config_root']}/nginx.conf
ExecReload={spec['nginx_binary']} -s reload -c {spec['config_root']}/nginx.conf
ExecStop={spec['nginx_binary']} -s quit -c {spec['config_root']}/nginx.conf
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths={spec['config_root']}
NoNewPrivileges=true
LimitCORE=0
StandardOutput=journal
StandardError=journal
SyslogIdentifier=aster-edge-control
LogRateLimitIntervalSec=30s
LogRateLimitBurst=10000
[Install]
WantedBy=multi-user.target
'''.encode()


def schema(report, release, database, allowed):
    state = report.get("database")
    require(report.get("exit_code") == 0 and report.get("core_version") == release["version"] and isinstance(state, dict) and state.get("read_only") is True and state.get("status") in allowed and state.get("database") == database and state.get("vendor") == "MySQL" and state.get("transport") == "local-unix-socket" and state.get("table_visibility_verified") is True, "Database preflight is not compatible with the reviewed operation.")
    applied = state.get("migration_checksums")
    require(isinstance(applied, dict) and set(applied) == {str(n) for n in range(1, state.get("schema_version", -1) + 1)} and all(release["migration_checksums"].get(k) == v for k, v in applied.items()) and state.get("required_schema_version") == len(release["migration_checksums"]), "Database migration checksums differ from the verified source.")
    if state["status"] == "current":
        require(applied == release["migration_checksums"], "Current schema must contain the full verified migration history.")
    return state


def identity_absent(user):
    try:
        pwd.getpwnam(user)
    except KeyError:
        pass
    else:
        raise Rejected("Install refuses an existing runtime identity.")
    try:
        grp.getgrnam(user)
    except KeyError:
        return
    raise Rejected("Install refuses an existing runtime group.")


def utc_now():
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def public_status(journal):
    result = {key: journal.get(key) for key in ("schema", "operation_id", "action", "status", "phase", "target_core_version", "target_source_sha", "previous_core_version", "previous_source_sha", "maintenance", "database_migration", "active_release_changed", "service_action", "program_rollback", "https_ready", "business_validation_required", "started_at", "updated_at", "error_code")}
    result["status"] = {"running": "executing", "recovery_required": "needs_manual_recovery"}.get(result["status"], result["status"])
    return result


def validate_journal(value):
    required = {"schema", "operation_id", "action", "plan_sha256", "status", "phase", "target_core_version", "target_source_sha", "previous_core_version", "previous_source_sha", "maintenance", "database_migration", "active_release_changed", "service_action", "program_rollback", "https_ready", "business_validation_required", "started_at", "updated_at", "error_code", "steps"}
    require(isinstance(value, dict) and required <= set(value) <= required | {"backup"} and type(value["schema"]) is int and value["schema"] == 1 and isinstance(value["operation_id"], str) and updater.REQUEST.fullmatch(value["operation_id"]) and isinstance(value["plan_sha256"], str) and updater.HASH.fullmatch(value["plan_sha256"]) and value["action"] in {"install", "upgrade"}, "Invalid operation journal structure.")
    updater.stable(value["target_core_version"])
    require(isinstance(value["target_source_sha"], str) and updater.SHA.fullmatch(value["target_source_sha"]), "Invalid target source in operation journal.")
    if value["action"] == "install":
        require(value["previous_core_version"] == value["previous_source_sha"] == "", "Fresh install journal cannot describe a previous release.")
    else:
        updater.stable(value["previous_core_version"])
        require(isinstance(value["previous_source_sha"], str) and updater.SHA.fullmatch(value["previous_source_sha"]), "Upgrade journal requires previous source identity.")
    for key in ("started_at", "updated_at"):
        require(isinstance(value[key], str) and re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z", value[key]), "Invalid operation journal timestamp.")
        datetime.datetime.strptime(value[key], "%Y-%m-%dT%H:%M:%SZ")
    names = {"install_preflight", "provision", "configure", "old_schema_preflight", "maintenance", "backup", "migrate", "first_admin", "activate", "readiness", "boot_enablement"}
    require(value["status"] in {"running", "recovery_required", "succeeded", "rolled_back"} and value["phase"] in names - {"boot_enablement"} | {"confirmed", "complete"} and isinstance(value["steps"], dict) and set(value["steps"]) <= names, "Invalid operation journal state.")
    for record in value["steps"].values():
        require(isinstance(record, dict) and {"status"} <= set(record) <= {"status", "result"} and record["status"] in {"started", "done"}, "Invalid operation checkpoint.")
    for key in ("maintenance", "active_release_changed", "https_ready", "business_validation_required"):
        require(type(value[key]) is bool, "Invalid operation journal flag.")
    migration = value["database_migration"]
    require(isinstance(migration, dict) and set(migration) == {"started", "completed"} and all(type(flag) is bool for flag in migration.values()) and (not migration["completed"] or migration["started"]), "Invalid database migration checkpoint.")
    activation = value["steps"].get("activate", {})
    if "result" in activation:
        result = activation["result"]
        require(isinstance(result, dict), "Invalid activation checkpoint result.")
        if "recovery_stopped" in result:
            require(set(result) == {"recovery_stopped"} and result["recovery_stopped"] is True and activation["status"] == "started" and value["action"] == "upgrade" and migration["completed"], "Invalid stopped target activation checkpoint.")
    require(value["service_action"] in {"none", "stopped", "started", "previous_started"} and value["program_rollback"] in {"none", "verified_old_schema", "blocked_schema_or_readiness"} and value["error_code"] in {"none", "operation_failed", "interrupted_requires_recovery"}, "Invalid operation receipt category.")
    if value["status"] == "succeeded":
        finished = {"maintenance", "migrate", "activate", "readiness"} | ({"install_preflight", "provision", "first_admin", "configure", "boot_enablement"} if value["action"] == "install" else {"old_schema_preflight", "backup"})
        require(all(value["steps"].get(name, {}).get("status") == "done" for name in finished) and value["phase"] == "complete" and migration["completed"] and value["https_ready"] and not value["maintenance"] and value["active_release_changed"] and value["service_action"] == "started" and value["program_rollback"] == "none" and value["error_code"] == "none", "Successful operation journal is incomplete or inconsistent.")
    if value["status"] == "rolled_back":
        require(value["action"] == "upgrade" and not value["active_release_changed"] and value["service_action"] == "previous_started" and value["program_rollback"] == "verified_old_schema", "Rollback receipt is inconsistent.")
    require(not value["https_ready"] or value["status"] == "succeeded", "Only completed target activation can claim HTTPS readiness.")
    return value


def export_status(path, receipt, trust):
    parent, name = os.path.split(path)
    fd = manager.parent_directory(parent, trust)
    temporary = ".operation-status-" + secrets.token_hex(16)
    try:
        if os.path.lexists(path):
            existing = updater.strict_json(read_file(path, 64 << 10, trust))
            require(existing.get("schema") == 1 and (existing.get("operation_id") == receipt["operation_id"] or existing.get("status") in {"succeeded", "rolled_back", "failed"}), "Status export belongs to another operation; choose a new export path.")
        manager.write_file(fd, temporary, updater.json_bytes(receipt), 0o644)
        os.replace(temporary, name, src_dir_fd=fd, dst_dir_fd=fd)
        os.fsync(fd)
    finally:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(temporary, dir_fd=fd)
        os.close(fd)


@contextlib.contextmanager
def operation_lock(state, trust):
    descriptor = os.open(".operation.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=state)
    try:
        info = os.fstat(descriptor)
        require(stat.S_ISREG(info.st_mode) and info.st_uid == trust.owner and info.st_nlink == 1 and stat.S_IMODE(info.st_mode) == 0o600, "Unsafe operation lock.")
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Rejected("Another native operation holds this instance lock.") from None
        yield
    finally:
        os.close(descriptor)


class Operation:
    def __init__(self, planned, state, directory, journal, native, trust):
        self.planned, self.spec, self.state, self.directory, self.journal = planned, planned["spec"], state, directory, journal
        self.native, self.trust = native, trust

    def save(self):
        self.journal["updated_at"] = utc_now()
        updater.atomic_json(self.directory, "JOURNAL.json", self.journal)
        updater.atomic_json(self.state, "STATUS.json", public_status(self.journal))
        if self.spec.get("status_file"):
            export_status(self.spec["status_file"], public_status(self.journal), self.trust)
        # STATUS is a bounded redacted operator export. The API never reads the
        # private root journal or receives permission to change state.

    def step(self, name, function, reconcile=None):
        record = self.journal["steps"].get(name)
        if record and record["status"] == "done":
            return record.get("result")
        if record:
            require(reconcile is not None, "Interrupted side effect needs operator recovery; it will not be repeated blindly.")
            value = reconcile()
        else:
            self.journal["phase"] = "activate" if name == "boot_enablement" else name
            self.journal["steps"][name] = {"status": "started"}
            self.save()
            value = function()
        self.journal["steps"][name] = {"status": "done", "result": value}
        self.save()
        return value

    def environment(self):
        return runtime_environment(self.spec) if self.spec["action"] == "install" else read_environment(self.spec["config_root"] + "/api.env", self.trust)[0]

    def migrator(self):
        env = self.environment()
        env["MYSQL_DSN_FILE"] = (self.operation_path + "/credentials/migrator_dsn") if self.spec["action"] == "install" else self.spec["migrator_dsn_file"]
        return env

    @property
    def operation_path(self):
        return self.spec["state_directory"] + "/" + self.spec["operation_id"]

    def install_preflight(self):
        spec = self.spec
        identity_absent(spec["runtime_user"])
        for path in (spec["instance_root"] + "/current", spec["config_root"], UNIT_DIRECTORY + "/" + spec["api_unit"], UNIT_DIRECTORY + "/" + edge_unit(spec)):
            require(not os.path.lexists(path), "Fresh install refuses existing instance configuration, current link or unit.")
        for unit in (spec["api_unit"], edge_unit(spec)):
            for parent in {UNIT_DIRECTORY, "/run/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"}:
                require(not os.path.lexists(parent + "/" + unit) and not os.path.lexists(parent + "/" + unit + ".d"), "Fresh install refuses existing service aliases or drop-ins.")
            self.native.unit_absent(unit)
        state = self.native.sql(spec["dba_client_config"], "SELECT VERSION(),@@version_comment,@@skip_networking,@@partial_revokes;\nSELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='" + spec["database"] + "';\nSELECT COUNT(*) FROM mysql.user WHERE User IN ('" + spec["database"] + "_runtime','" + spec["database"] + "_migrator','" + spec["database"] + "_backup');\n")
        lines = state.splitlines()
        require(len(lines) == 3 and re.fullmatch(r"8\.4\.[0-9]+(?:[-+][A-Za-z0-9_.-]+)?\tMySQL (?:Community|Enterprise) Server(?: - (?:GPL|Commercial|Advanced Edition))?\t1\t[01]", lines[0]) and lines[1:] == ["0", "0"], "Install requires absent database/accounts on reviewed MySQL 8.4 with TCP disabled.")
        return {"database_absent": True, "identities_absent": True, "partial_revokes": lines[0].rsplit("\t", 1)[1] == "1"}

    def provision(self):
        spec = self.spec
        self.native.run(["/usr/sbin/useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", spec["runtime_user"]])
        account = pwd.getpwnam(spec["runtime_user"])
        require(account.pw_uid != 0 and account.pw_gid != 0, "Runtime identity must be non-root.")
        create_directory(spec["config_root"], 0o755, self.trust)
        create_directory(spec["config_root"] + "/secrets", 0o750, self.trust)
        os.chown(spec["config_root"] + "/secrets", self.trust.owner, account.pw_gid)
        if not os.path.exists(spec["instance_root"] + "/plugins"):
            create_directory(spec["instance_root"] + "/plugins", 0o755, self.trust)
        create_directory(self.operation_path + "/credentials", 0o700, self.trust)
        names = {role: spec["database"] + "_" + role for role in ("runtime", "migrator", "backup")}
        passwords = {role: secrets.token_hex(32) for role in names}
        def dsn(role):
            return f"{names[role]}:{passwords[role]}@unix({spec['socket']})/{spec['database']}?parseTime=true\n".encode()
        new_file(spec["config_root"] + "/secrets/mysql_runtime_dsn", dsn("runtime"), 0o640, account.pw_gid, self.trust)
        new_file(spec["config_root"] + "/secrets/session_secret", (secrets.token_hex(48) + "\n").encode(), 0o640, account.pw_gid, self.trust)
        new_file(self.operation_path + "/credentials/migrator_dsn", dsn("migrator"), trust=self.trust)
        new_file(self.operation_path + "/credentials/backup.cnf", f"[client]\nuser={names['backup']}\npassword={passwords['backup']}\n".encode(), trust=self.trust)
        new_file(spec["config_root"] + "/api.env", "".join(k + "=" + v + "\n" for k, v in sorted(runtime_environment(spec).items())).encode(), 0o640, account.pw_gid, self.trust)
        new_file(spec["config_root"] + "/plugins.json", updater.json_bytes({"schema": 1, "revision": 1}), 0o644, trust=self.trust)
        partial_revokes = self.journal["steps"]["install_preflight"]["result"]["partial_revokes"]
        require(self.native.sql(spec["dba_client_config"], "SELECT @@partial_revokes;\n") == ("1" if partial_revokes else "0"), "Database grant interpretation changed after preflight; no accounts or grants were created.")
        # With partial_revokes OFF MySQL treats '_' in database grants as a
        # wildcard even inside backticks. ON uses literal database identifiers.
        grant_database = spec["database"] if partial_revokes else spec["database"].replace("_", "\\_")
        query = "CREATE DATABASE `" + spec["database"] + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n"
        for role in names:
            query += f"CREATE USER '{names[role]}'@'localhost' IDENTIFIED BY '{passwords[role]}';\n"
        for role, grant in (("runtime", "SELECT,INSERT,UPDATE,DELETE"), ("migrator", "SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,REFERENCES"), ("backup", "SELECT,SHOW VIEW,TRIGGER,EVENT")):
            query += f"GRANT {grant} ON `{grant_database}`.* TO '{names[role]}'@'localhost';\n"
        query += f"GRANT SHOW_ROUTINE ON *.* TO '{names['backup']}'@'localhost';\n"
        self.native.sql(spec["dba_client_config"], query)
        return {"runtime_uid": account.pw_uid, "runtime_gid": account.pw_gid, "database_created": True, "credentials_written": True}

    def configure(self, reconcile=False):
        spec = self.spec
        if os.path.lexists(spec["instance_root"] + "/current"):
            require(reconcile and current_release(spec["instance_root"], self.trust) == spec["release_directory"], "Configuration recovery found an unexpected current link.")
        else:
            set_current(spec, spec["release_directory"], None, self.trust)
        self.journal["active_release_changed"] = True
        self.save()
        def configuration_file(path, raw):
            if os.path.lexists(path):
                require(reconcile and read_file(path, 1 << 20, self.trust) == raw and stat.S_IMODE(os.lstat(path).st_mode) == 0o644, "Configuration recovery refuses an existing file that differs from its exact generated content.")
            else:
                new_file(path, raw, 0o644, trust=self.trust)
        configuration_file(UNIT_DIRECTORY + "/" + spec["api_unit"], api_unit_text(spec, self.trust))
        if spec["edge_mode"] == "managed":
            temporary_root = spec["config_root"] + "/nginx-temp"
            if os.path.lexists(temporary_root):
                require(reconcile, "Fresh configuration refuses an existing Nginx temporary root.")
                descriptor = updater.protected_open(temporary_root, True, self.trust)
                os.close(descriptor)
            else:
                create_directory(temporary_root, 0o755, self.trust)
            configuration_file(spec["config_root"] + "/nginx.conf", nginx_text(spec))
            configuration_file(UNIT_DIRECTORY + "/" + edge_unit(spec), edge_unit_text(spec))
        self.native.run(["/usr/bin/systemd-analyze", "verify", UNIT_DIRECTORY + "/" + spec["api_unit"]] + ([UNIT_DIRECTORY + "/" + edge_unit(spec)] if spec["edge_mode"] == "managed" else []))
        self.native.edge("test")
        self.native.service("daemon-reload")
        return {"configuration_created": True}

    def maintain(self):
        if self.spec["action"] == "upgrade":
            current = current_release(self.spec["instance_root"], self.trust)
            allowed = {self.planned["inputs"]["previous"]["release_directory"]}
            if "activate" in self.journal["steps"]:
                allowed.add(self.spec["release_directory"])
            require(current in allowed, "Current changed outside the confirmed operation.")
            require(unit_inputs(self.spec, self.planned["inputs"]["previous"], self.trust) == self.planned["inputs"]["units"], "Service files changed before entering maintenance.")
            self.native.validate_service(self.planned["inputs"]["previous"]["release_directory"])
        path = self.spec["config_root"] + "/maintenance"
        if not os.path.lexists(path):
            new_file(path, b"Aster maintenance\n", 0o644, trust=self.trust)
        else:
            require(read_file(path, 128, self.trust) == b"Aster maintenance\n", "Unknown maintenance file.")
        self.journal["maintenance"] = True
        self.save()
        if self.spec["action"] == "upgrade":
            status, _ = self.native.https("/api/__aster_maintenance_probe")
            require(status == 503, "HTTPS edge did not enforce maintenance before stopping writes.")
        if self.spec["action"] == "upgrade":
            self.native.service("stop", self.spec["api_unit"])
            require(self.native.service("show", self.spec["api_unit"]) in {"inactive", "failed"}, "API did not stop; database changes are forbidden.")
        else:
            self.native.unit_absent(self.spec["api_unit"])
        self.journal["service_action"] = "stopped"
        return {"writes_stopped": True}

    def observe(self, release, allowed):
        return schema(self.native.preflight(release, self.migrator()), prepared_release(release, self.trust), self.spec["database"], allowed)

    def migrate(self):
        target = self.spec["release_directory"]
        self.observe(target, {"empty"} if self.spec["action"] == "install" else {"current", "pending_migrations"})
        self.journal["database_migration"]["started"] = True
        self.save()
        self.native.api(target, self.migrator(), "-migrate")
        result = self.observe(target, {"current"})
        self.journal["database_migration"]["completed"] = True
        self.save()
        return {"schema_version": result["schema_version"], "migration_checksums": result["migration_checksums"]}

    def reconcile_migration(self):
        # A started migration is never executed twice by recovery. Only a full
        # target schema proves it completed despite an interrupted receipt.
        result = self.observe(self.spec["release_directory"], {"current"})
        self.journal["database_migration"] = {"started": True, "completed": True}
        return {"schema_version": result["schema_version"], "migration_checksums": result["migration_checksums"]}

    def bootstrap(self):
        target = self.spec["release_directory"]
        require(self.observe(target, {"current"})["admin_count"] == 0, "Fresh bootstrap refuses an existing administrator.")
        env = self.environment()
        env.update(BOOTSTRAP_EMAIL=self.spec["bootstrap_email"], BOOTSTRAP_NAME=self.spec["bootstrap_name"], BOOTSTRAP_PASSWORD_FILE=self.spec["bootstrap_password_file"], BOOTSTRAP_ROLE="admin")
        self.native.api(target, env, "-create-user")
        require(self.observe(target, {"current"})["admin_count"] == 1, "First administrator was not verified.")
        return {"administrator_verified": True}

    def reconcile_bootstrap(self):
        require(self.observe(self.spec["release_directory"], {"current"})["admin_count"] == 1, "Interrupted bootstrap is uncertain; no password or user mutation will be retried.")
        return {"administrator_verified": True}

    def activate(self):
        spec = self.spec
        expected = self.planned["inputs"]["previous"]
        target = current_release(spec["instance_root"], self.trust) if os.path.lexists(spec["instance_root"] + "/current") else None
        require(target in {None if expected is None else expected["release_directory"], spec["release_directory"]}, "Current link has an unexpected target.")
        if target != spec["release_directory"]:
            set_current(spec, spec["release_directory"], target, self.trust)
        self.journal["active_release_changed"] = True
        self.save()
        if spec["action"] == "upgrade":
            require(unit_inputs(spec, self.planned["inputs"]["previous"], self.trust) == self.planned["inputs"]["units"] and plugin_facts(self.environment(), self.planned["inputs"]["target"]["version"], self.trust) == self.planned["inputs"]["plugins"], "Confirmed service or plugin inputs changed before activation.")
        self.native.validate_service(spec["release_directory"])
        self.native.service("start", spec["api_unit"])
        require(self.native.service("show", spec["api_unit"]) == "active", "API did not become active.")
        self.native.edge("test")
        self.native.edge("start" if spec["action"] == "install" else "reload")
        self.journal["service_action"] = "started"
        return {"active_release": spec["release_directory"]}

    def reconcile_activation(self):
        record = self.journal["steps"]["activate"]
        if "recovery_stopped" in record.get("result", {}):
            # A later failure stopped an already activated target. Reconcile
            # only that durable fact, after independent operator schema repair;
            # never infer that a completed migration makes the current DB safe.
            require(self.journal["maintenance"] and self.journal["service_action"] == "stopped" and self.journal["database_migration"]["completed"], "Stopped target activation checkpoint is inconsistent.")
            require(current_release(self.spec["instance_root"], self.trust) == self.spec["release_directory"], "Stopped target activation found an unexpected current release.")
            require(read_file(self.spec["config_root"] + "/maintenance", 128, self.trust) == b"Aster maintenance\n" and self.native.https("/api/__aster_maintenance_probe")[0] == 503, "Stopped target recovery requires verified HTTPS maintenance.")
            require(self.native.service("show", self.spec["api_unit"]) in {"inactive", "failed"}, "Stopped target recovery refuses an active service.")
            require(unit_inputs(self.spec, self.planned["inputs"]["previous"], self.trust) == self.planned["inputs"]["units"] and plugin_facts(self.environment(), self.planned["inputs"]["target"]["version"], self.trust) == self.planned["inputs"]["plugins"], "Confirmed service or plugin inputs changed before stopped target recovery.")
            self.native.validate_service(self.spec["release_directory"])
            self.observe(self.spec["release_directory"], {"current"})
        return self.activate()

    def readiness(self, static=False):
        for attempt in range(15):
            try:
                self.native.process_identity(self.spec["release_directory"])
                status, raw = self.native.https("/readyz")
                require(status == 200, "HTTPS readiness is not successful.")
                report = schema(self.native.preflight(self.spec["release_directory"], self.environment()), self.planned["inputs"]["target"], self.spec["database"], {"current"})
                require(report.get("admin_count", 0) >= 1, "No administrator is available.")
                if static:
                    status, raw = self.native.https("/index.html")
                    require(status == 200 and updater.digest(raw) == self.planned["inputs"]["target"]["files_sha256"]["web/index.html"], "HTTPS static entry differs from the verified target release.")
                return {"https_ready": True, "tls_certificate_and_hostname_verified": True}
            except Rejected:
                if attempt == 14:
                    raise
                time.sleep(1)

    def finish(self):
        self.readiness()
        path = self.spec["config_root"] + "/maintenance"
        require(read_file(path, 128, self.trust) == b"Aster maintenance\n", "Maintenance marker changed.")
        os.unlink(path)
        fd = updater.protected_open(self.spec["config_root"], True, self.trust)
        os.fsync(fd)
        os.close(fd)
        self.journal["maintenance"] = False
        self.save()
        self.readiness(static=True)
        # A repaired/resumed target has now passed process, schema, HTTPS and
        # asset verification. Its receipt must no longer describe a blocked
        # recovery decision from an earlier attempt.
        self.journal.update(status="succeeded", phase="complete", https_ready=True, error_code="none", program_rollback="none")
        self.save()
        return public_status(self.journal)

    def enable_boot(self):
        if not self.spec["enable_at_boot"]:
            return {"enabled": False}
        units = [self.spec["api_unit"]]
        if self.spec["edge_mode"] == "managed":
            units.append(edge_unit(self.spec))
        self.native.validate_service(self.spec["release_directory"])
        for unit in units:
            self.native.service("enable", unit)
            require(self.native.service("is-enabled", unit) == "enabled", "Dedicated service boot activation was not verified.")
        return {"enabled": True, "unit_count": len(units)}

    def failure(self):
        spec = self.spec
        self.journal.update(status="recovery_required", https_ready=False, error_code="operation_failed")
        marker = spec["config_root"] + "/maintenance"
        maintenance_started = self.journal["steps"].get("maintenance", {}).get("status") == "done" or os.path.lexists(marker)
        if maintenance_started and os.path.isdir(spec["config_root"]):
            with contextlib.suppress(OSError, Rejected):
                if not os.path.lexists(spec["config_root"] + "/maintenance"):
                    new_file(spec["config_root"] + "/maintenance", b"Aster maintenance\n", 0o644, trust=self.trust)
                self.journal["maintenance"] = True
        previous = self.planned["inputs"].get("previous")
        if previous and "maintenance" in self.journal["steps"]:
            try:
                require(not self.journal["database_migration"]["started"] or self.journal["database_migration"]["completed"], "Uncertain migration outcome forbids automatic program rollback.")
                # A stopped old API and old-source checksums/current shape are
                # mandatory even when migrate returned a failure. No DB restore.
                require(unit_inputs(spec, previous, self.trust) == self.planned["inputs"]["units"], "Confirmed service files changed before recovery.")
                self.native.validate_service(previous["release_directory"])
                target = current_release(spec["instance_root"], self.trust)
                require(target in {spec["release_directory"], previous["release_directory"]}, "Unexpected recovery link target.")
                self.native.service("stop", spec["api_unit"])
                require(self.native.service("show", spec["api_unit"]) in {"inactive", "failed"}, "New service did not stop.")
                if target == spec["release_directory"] and self.journal["database_migration"]["completed"] and self.journal["steps"].get("activate", {}).get("status") == "done":
                    self.journal["steps"]["activate"] = {"status": "started", "result": {"recovery_stopped": True}}
                    self.journal["service_action"] = "stopped"
                    self.save()
                schema(self.native.preflight(previous["release_directory"], self.migrator()), previous, spec["database"], {"current"})
                target = current_release(spec["instance_root"], self.trust)
                require(target in {spec["release_directory"], previous["release_directory"]}, "Unexpected recovery link target.")
                if target != previous["release_directory"]:
                    set_current(spec, previous["release_directory"], target, self.trust)
                self.native.service("start", spec["api_unit"])
                require(self.native.service("show", spec["api_unit"]) == "active", "Previous service did not start.")
                self.native.process_identity(previous["release_directory"])
                require(self.native.https("/readyz")[0] == 200, "Previous HTTPS readiness failed.")
                self.journal.update(program_rollback="verified_old_schema", status="rolled_back", active_release_changed=False, service_action="previous_started")
                # Keep maintenance visible until explicit business validation.
            except (Rejected, OSError, TypeError, KeyError):
                self.journal["program_rollback"] = "blocked_schema_or_readiness"
        self.save()

    def run(self):
        spec = self.spec
        if spec["action"] == "install":
            self.step("install_preflight", self.install_preflight)
            self.step("provision", self.provision)
        else:
            previous = self.planned["inputs"]["previous"]
            self.step("old_schema_preflight", lambda: self.observe(previous["release_directory"], {"current"}), lambda: self.observe(previous["release_directory"], {"current"}))
        self.step("maintenance", self.maintain, self.maintain)
        if spec["action"] == "upgrade":
            def backup():
                path = self.operation_path + "/backups"
                create_backup_directory(path, self.trust)
                value = self.native.backup(self.planned["inputs"]["previous"]["release_directory"], path)
                require(value["migration_checksums"] == self.planned["inputs"]["previous"]["migration_checksums"], "Backup schema does not match the old source.")
                self.journal["backup"] = value
                return value
            saved_backup = self.step("backup", backup, lambda: self.native.completed_backup(self.planned["inputs"]["previous"]["release_directory"], self.operation_path + "/backups"))
            self.native.verify_backup(saved_backup, self.planned["inputs"]["previous"]["release_directory"])
            require(plugin_facts(self.environment(), self.planned["inputs"]["target"]["version"], self.trust) == self.planned["inputs"]["plugins"], "Installed plugin policy or manifests changed after confirmation.")
        if self.journal["steps"].get("migrate", {}).get("status") != "done":
            self.maintain()
        self.step("migrate", self.migrate, self.reconcile_migration)
        if spec["action"] == "install":
            self.step("first_admin", self.bootstrap, self.reconcile_bootstrap)
            self.step("configure", self.configure, lambda: self.configure(reconcile=True))
        self.step("activate", self.activate, self.reconcile_activation)
        self.step("readiness", self.readiness, self.readiness)
        if spec["action"] == "install":
            self.step("boot_enablement", self.enable_boot, self.enable_boot)
        return self.finish()


def execute(spec_path, confirm_plan_sha256, resume=False, trust=updater._Trust(), native=None):
    # Resume loads the original immutable plan because current/schema may already
    # have changed. Every source/config/tool input is rechecked below.
    spec = validate_spec(updater.strict_json(read_file(spec_path, 64 << 10, trust, True), 64 << 10))
    native = Native(spec, trust) if native is None else native
    initially_planned = None
    if not resume:
        initially_planned = plan(spec_path, trust)
        require(initially_planned["spec"] == spec, "Spec changed during plan verification.")
        require(isinstance(confirm_plan_sha256, str) and updater.HASH.fullmatch(confirm_plan_sha256) and confirm_plan_sha256 == initially_planned["plan_sha256"], "Explicit confirmation of the current plan SHA-256 is required.")
    state_path = spec["state_directory"]
    if not os.path.lexists(state_path):
        require(not resume, "No operation state exists to resume.")
        create_directory(state_path, 0o700, trust)
    state = updater.protected_open(state_path, True, trust, True)
    operation_directory = -1
    try:
        instance_fd = updater.protected_open(spec["instance_root"], True, trust)
        with contextlib.ExitStack() as locks:
            locks.callback(os.close, instance_fd)
            locks.enter_context(operation_lock(instance_fd, trust))
            locks.enter_context(operation_lock(state, trust))
            operation_path = state_path + "/" + spec["operation_id"]
            if resume:
                operation_directory = updater.protected_open(operation_path, True, trust, True)
                planned = updater.strict_json(read_file(operation_path + "/PLAN.json", updater.MAX_JSON, trust, True))
                journal = updater.strict_json(read_file(operation_path + "/JOURNAL.json", updater.MAX_JSON, trust, True))
                validate_journal(journal)
                require(planned.get("schema") == 1 and planned.get("plan_sha256") == updater.digest(updater.json_bytes({key: planned[key] for key in ("schema", "spec", "inputs")})), "Stored operation plan digest is inconsistent.")
                require(planned["plan_sha256"] == confirm_plan_sha256 == journal["plan_sha256"] and planned["spec"] == spec and planned["inputs"]["spec_sha256"] == updater.digest(read_file(spec_path, 64 << 10, trust, True)), "Resume must use the exact original private spec and confirmed plan.")
                require(journal["operation_id"] == spec["operation_id"] and journal["action"] == spec["action"], "Operation journal identity differs.")
                require(journal["status"] in TERMINAL | {"running", "recovery_required"}, "Invalid resumable journal state.")
                require(prepared_release(spec["release_directory"], trust) == planned["inputs"]["target"], "Target prepared files changed before resume.")
                if planned["inputs"]["previous"]:
                    previous = planned["inputs"]["previous"]
                    require(prepared_release(previous["release_directory"], trust) == {k: v for k, v in previous.items() if k != "release_directory"}, "Previous prepared files changed before resume.")
                for key, name in (("mysql_binary", "mysql"), ("mysqldump_binary", "mysqldump"), ("nginx_binary", "nginx")):
                    require(binary(spec[key], name, trust) == planned["inputs"]["tools"][key], "A confirmed native tool changed before resume.")
                require(binary(SYSTEM_BUSCTL, "busctl") == planned["inputs"]["tools"]["busctl"], "The confirmed typed service-inspection tool changed before resume.")
                if spec["edge_mode"] == "managed":
                    web = pwd.getpwnam("www-data")
                    require({"uid": web.pw_uid, "gid": web.pw_gid} == planned["inputs"]["nginx_worker"], "Managed Nginx identity changed before resume.")
                for key in ("certificate", "certificate_key", "ca_file"):
                    if key in spec:
                        require(sha_file(spec[key], trust) == planned["inputs"][key], "Confirmed TLS material changed before resume.")
                if spec["action"] == "upgrade":
                    environment, checksum = read_environment(spec["config_root"] + "/api.env", trust)
                    require(environment.get("HOST_OPERATION_STATUS_FILE") == spec.get("status_file") and unit_inputs(spec, planned["inputs"]["previous"], trust) == planned["inputs"]["units"] and sha_file(environment["SESSION_SECRET_FILE"], trust) == planned["inputs"]["session_secret_sha256"] and optional_secret_inputs(environment, spec["runtime_user"], trust) == planned["inputs"]["optional_secrets"] and client_config(spec["backup_client_config"], trust) == planned["inputs"]["backup_client_config"], "Confirmed service or private inputs changed before resume.")
                    require(checksum == planned["inputs"]["environment_sha256"] and dsn_target(environment["MYSQL_DSN_FILE"], spec, trust) == planned["inputs"]["runtime_dsn_sha256"] and dsn_target(spec["migrator_dsn_file"], spec, trust, True) == planned["inputs"]["migrator_dsn_sha256"] and plugin_facts(environment, planned["inputs"]["target"]["version"], trust) == planned["inputs"]["plugins"], "Confirmed configuration, credentials or plugins changed before resume.")
                    if journal["steps"].get("backup", {}).get("status") == "done":
                        native.verify_backup(journal["steps"]["backup"]["result"], planned["inputs"]["previous"]["release_directory"])
                if journal["status"] in TERMINAL:
                    expected_current = planned["inputs"]["previous"]["release_directory"] if journal["status"] == "rolled_back" else spec["release_directory"]
                    require(current_release(spec["instance_root"], trust) == expected_current, "Completed operation receipt no longer matches the active release.")
                    return public_status(journal)
                journal["status"] = "running"
            else:
                planned = plan(spec_path, trust)
                require(planned["spec"] == spec, "Spec changed before the locked operation was recorded.")
                require(isinstance(confirm_plan_sha256, str) and updater.HASH.fullmatch(confirm_plan_sha256) and confirm_plan_sha256 == planned["plan_sha256"], "Explicit confirmation of the current plan SHA-256 is required.")
                os.mkdir(spec["operation_id"], 0o700, dir_fd=state)
                os.fsync(state)
                operation_directory = updater.protected_open(operation_path, True, trust, True)
                updater.new_file(operation_directory, "PLAN.json", updater.json_bytes(planned))
                previous = planned["inputs"].get("previous")
                journal = {"schema": 1, "operation_id": spec["operation_id"], "action": spec["action"], "plan_sha256": confirm_plan_sha256, "status": "running", "phase": "confirmed", "target_core_version": planned["inputs"]["target"]["version"], "target_source_sha": planned["inputs"]["target"]["source_sha"], "previous_core_version": previous["version"] if previous else "", "previous_source_sha": previous["source_sha"] if previous else "", "maintenance": False, "database_migration": {"started": False, "completed": False}, "active_release_changed": False, "service_action": "none", "program_rollback": "none", "https_ready": False, "business_validation_required": True, "started_at": utc_now(), "updated_at": utc_now(), "error_code": "none", "steps": {}}
            operation = Operation(planned, state, operation_directory, journal, native, trust)
            operation.save()
            try:
                return operation.run()
            except (Rejected, OSError, TypeError, ValueError, KeyError, AttributeError, KeyboardInterrupt):
                operation.failure()
                raise Rejected("Native operation stopped; inspect its redacted status and private journal. No database rollback was performed.") from None
    finally:
        if operation_directory >= 0:
            os.close(operation_directory)
        os.close(state)


def status(state_directory, operation_id, trust=updater._Trust()):
    require(updater.REQUEST.fullmatch(operation_id), "Invalid operation ID.")
    value = updater.strict_json(read_file(state_directory + "/" + operation_id + "/JOURNAL.json", updater.MAX_JSON, trust, True))
    validate_journal(value)
    require(value.get("schema") == 1 and value.get("operation_id") == operation_id, "Invalid operation receipt.")
    return public_status(value)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for action in ("plan", "apply", "resume"):
        child = commands.add_parser(action)
        child.add_argument("--spec", required=True)
        if action != "plan":
            child.add_argument("--confirm-plan-sha256", required=True)
    child = commands.add_parser("status")
    child.add_argument("--state-directory", required=True)
    child.add_argument("--operation-id", required=True)
    options = parser.parse_args(argv)
    try:
        require(sys.platform == "linux" and os.geteuid() == 0, "Run this Linux operator executor explicitly as root.")
        os.umask(0o077)
        if options.command == "plan":
            full = plan(options.spec)
            spec = full["spec"]
            previous = full["inputs"]["previous"]
            result = {"schema": 1, "status": "prepared", "operation_id": spec["operation_id"], "action": spec["action"], "plan_sha256": full["plan_sha256"], "target_core_version": full["inputs"]["target"]["version"], "target_source_sha": full["inputs"]["target"]["source_sha"], "previous_core_version": previous["version"] if previous else "", "previous_source_sha": previous["source_sha"] if previous else "", "instance_root": spec["instance_root"], "config_root": spec["config_root"], "state_directory": spec["state_directory"], "status_file": spec.get("status_file"), "origin": spec["origin"], "listen_address": spec["listen_address"], "edge_mode": spec["edge_mode"], "edge_listen_address": spec["edge_listen_address"], "api_unit": spec["api_unit"], "database": spec["database"], "runtime_user": spec["runtime_user"], "enable_at_boot": spec.get("enable_at_boot"), "native_tool_sha256": full["inputs"]["tools"], "tls_certificate_sha256": full["inputs"]["certificate"], "migration_versions": list(full["inputs"]["target"]["migration_checksums"]), "actions": ["verify_instance_and_source", "provision_dedicated_identity_database_config" if spec["action"] == "install" else "verify_old_schema_plugins_and_take_fresh_backup", "stop_writes_and_migrate", "create_first_admin" if spec["action"] == "install" else "preserve_existing_administrators", "activate_verified_release", "verify_process_https_and_static_assets"], "notice": full["notice"]}
        elif options.command == "status":
            result = status(options.state_directory, options.operation_id)
        else:
            result = execute(options.spec, options.confirm_plan_sha256, options.command == "resume")
        print(json.dumps(result, sort_keys=True, ensure_ascii=False))
        return 1 if result.get("status") in {"needs_manual_recovery", "failed"} else 0
    except (Rejected, OSError, TypeError, ValueError, KeyError, AttributeError, UnicodeError) as exc:
        print("Core operation rejected: " + (str(exc) if isinstance(exc, Rejected) else "Protected input or native operation could not be validated."), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
