#!/usr/bin/env python3
"""Prepare a verified official Core release; never activate or execute it.

The Linux root operator selects a stable version explicitly. Preparation checks
the fixed official repository even when using protected local asset files. It
assembles a new complete directory, with a local receipt, without changing the
current link, services, configuration, identities, secrets or database.
"""
import argparse
import contextlib
import ctypes
import datetime
import fcntl
import importlib.util
import ipaddress
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import stat
import sys

_SPEC = importlib.util.spec_from_file_location("aster_core_update_verifier", Path(__file__).resolve().with_name("update-manager.py"))
updater = importlib.util.module_from_spec(_SPEC)
sys.modules[_SPEC.name] = updater
_SPEC.loader.exec_module(updater)
Rejected = updater.Rejected
RECEIPT = "manifests/PREPARED.json"
AUDIT = "CORE_PREPARE.jsonl"
LOCK = ".core-manager.lock"


def target_path(path):
    updater.exact_path(path)
    parent, name = os.path.split(path)
    updater.require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", name) and name not in {".", ".."}, "A new named release directory is required.")
    return parent, name


def parent_directory(path, trust, create=False):
    """Walk protected ancestors using descriptors; create only output parents."""
    updater.exact_path(path) if path != "/" else None
    descriptor = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    try:
        parts = path[1:].split("/") if path != "/" else []
        for part in parts:
            created = False
            try:
                child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=descriptor)
            except FileNotFoundError:
                if not create:
                    raise
                try:
                    os.mkdir(part, 0o755, dir_fd=descriptor)
                    created = True
                except FileExistsError:
                    pass
                child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=descriptor)
            info = os.fstat(child)
            sticky = info.st_uid == trust.ancestor and info.st_mode & stat.S_ISVTX
            if not (info.st_uid in {trust.owner, trust.ancestor} and stat.S_ISDIR(info.st_mode) and not info.st_mode & (stat.S_ISUID | stat.S_ISGID) and (not info.st_mode & 0o022 or sticky)):
                os.close(child)
                raise Rejected("Release parent paths must be protected and root-owned.")
            if created:
                os.fchmod(child, 0o755)
                os.fsync(child)
                os.fsync(descriptor)
            os.close(descriptor)
            descriptor = child
        # A sticky shared ancestor is permitted, but never the output parent.
        info = os.fstat(descriptor)
        updater.require(info.st_uid == trust.owner and not info.st_mode & 0o022, "The release output parent must be protected and root-owned.")
        result, descriptor = descriptor, -1
        return result
    except OSError:
        raise Rejected("Release parent paths could not be safely opened.") from None
    finally:
        if descriptor >= 0:
            os.close(descriptor)


@contextlib.contextmanager
def locked(parent, trust):
    descriptor = os.open(LOCK, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, 0o600, dir_fd=parent)
    try:
        info = os.fstat(descriptor)
        updater.require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_uid == trust.owner and stat.S_IMODE(info.st_mode) == 0o600, "Unsafe Core preparation lock file.")
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Rejected("Another Core preparation holds the exclusive lock.") from None
        os.fsync(parent)
        yield
    finally:
        os.close(descriptor)


def audit(parent, event, trust):
    descriptor = os.open(AUDIT, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK, 0o600, dir_fd=parent)
    try:
        info = os.fstat(descriptor)
        updater.require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_uid == trust.owner and stat.S_IMODE(info.st_mode) == 0o600 and info.st_size <= 16 << 20, "Unsafe or full Core preparation log.")
        raw = (json.dumps(dict(event, at=datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")), sort_keys=True) + "\n").encode()
        updater.require(os.write(descriptor, raw) == len(raw), "Core preparation log was incomplete.")
        os.fsync(descriptor)
        os.fsync(parent)
    finally:
        os.close(descriptor)


def release_descriptor(api, version):
    """Read an actual normal Release, without inventing a web request identity."""
    updater.stable(version)
    tag = "v" + version
    release = api.json("/releases/tags/" + tag)
    updater.require(isinstance(release, dict) and updater.positive(release.get("id")) and release.get("tag_name") == tag and release.get("draft") is False and release.get("prerelease") is False and isinstance(release.get("target_commitish"), str) and updater.SHA.fullmatch(release["target_commitish"]) and isinstance(release.get("assets"), list), "A normal official Core release bound to a source commit is required.")
    assets = []
    for item in release["assets"]:
        updater.require(isinstance(item, dict) and item.get("state") == "uploaded" and isinstance(item.get("digest"), str) and re.fullmatch(r"sha256:[a-f0-9]{64}", item["digest"]), "Official Core assets require complete SHA-256 metadata.")
        assets.append({"name": item.get("name"), "id": item.get("id"), "size": item.get("size"), "sha256": item["digest"][7:]})
    updater.asset_inventory(assets, "core", version, "raw-binaries-v1")
    descriptor = {"component": "core", "tag": tag, "version": version, "source_sha": release["target_commitish"], "release_id": release["id"], "distribution_format": "raw-binaries-v1", "assets": assets}
    updater.release_assets(api, descriptor)
    return descriptor


def verified_release(api, version, assets_directory, trust):
    descriptor = release_descriptor(api, version)
    inventory = updater.asset_inventory(descriptor["assets"], "core", version, "raw-binaries-v1")
    files = {}
    directory_fd = -1
    try:
        if assets_directory is not None:
            directory_fd = updater.protected_open(assets_directory, True, trust)
            updater.require(set(os.listdir(directory_fd)) == set(inventory), "Local Core input directory must contain exactly the six official assets.")
        for name, item in sorted(inventory.items()):
            files[name] = (updater.read_protected(assets_directory + "/" + name, updater.asset_limit(name), trust) if directory_fd >= 0 else api.asset(item["id"], min(updater.asset_limit(name), item["size"])))
    finally:
        if directory_fd >= 0:
            os.close(directory_fd)
    provenance = updater.strict_json(files["PROVENANCE.json"])
    updater.require(isinstance(provenance, dict) and updater.positive(provenance.get("run_id")) and updater.positive(provenance.get("run_attempt")), "Official Core build run identity is required.")
    descriptor.update(run_id=provenance["run_id"], run_attempt=provenance["run_attempt"])
    verification, bundle = updater.verify_downloads(api, descriptor, files, version, with_bundle=True)
    updater.require(updater.release_assets(api, descriptor) == inventory, "Official Core release changed during preparation.")
    return descriptor, verification, bundle


def write_file(directory, name, raw, mode):
    descriptor = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600, dir_fd=directory)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(raw)
        stream.flush()
        os.fchmod(stream.fileno(), mode)
        os.fsync(stream.fileno())


def rename_new(source_dir, source, destination_dir, destination):
    function = getattr(ctypes.CDLL(None, use_errno=True), "renameat2", None)
    updater.require(function is not None, "This Linux host lacks atomic no-overwrite preparation.")
    function.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
    function.restype = ctypes.c_int
    updater.require(function(source_dir, source.encode("ascii"), destination_dir, destination.encode("ascii"), 1) == 0, "Prepared release could not be published without overwriting an existing path.")


def populate(directory, files):
    """Create only verified, bounded relative paths under a private descriptor."""
    directories = {""}
    for path in files:
        directories.update("/".join(path.split("/")[:index]) for index in range(1, len(path.split("/"))))
    descriptors = {"": directory}
    try:
        for path in sorted(directories - {""}, key=lambda value: (value.count("/"), value)):
            prefix, _, name = path.rpartition("/")
            os.mkdir(name, 0o700, dir_fd=descriptors[prefix])
            descriptors[path] = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=descriptors[prefix])
        for path, (raw, mode) in sorted(files.items()):
            prefix, _, name = path.rpartition("/")
            write_file(descriptors[prefix], name, raw, mode)
        for path in sorted(descriptors, key=lambda value: value.count("/") + bool(value), reverse=True):
            # The root stays private until it has been atomically published.
            if path:
                os.fchmod(descriptors[path], 0o755)
            os.fsync(descriptors[path])
    finally:
        for path, descriptor in descriptors.items():
            if path:
                os.close(descriptor)


def remove_private_tree(directory):
    # No untrusted process can enter this private staging root. Still use
    # descriptor-relative unlink/rmdir, never follow a link during cleanup.
    for name in os.listdir(directory):
        info = os.stat(name, dir_fd=directory, follow_symlinks=False)
        if stat.S_ISDIR(info.st_mode):
            child = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=directory)
            try:
                remove_private_tree(child)
            finally:
                os.close(child)
            os.rmdir(name, dir_fd=directory)
        else:
            os.unlink(name, dir_fd=directory)


def prepare(version, release_directory, assets_directory=None, api=None, trust=updater._Trust()):
    updater.stable(version)
    parent_path, name = target_path(release_directory)
    if assets_directory is not None:
        updater.exact_path(assets_directory)
    parent = parent_directory(parent_path, trust, create=True)
    stage_name, stage_fd, published = ".prepare-" + secrets.token_hex(16), -1, False
    event = {"schema": 1, "operation_id": secrets.token_hex(16), "version": version, "release_directory": release_directory, "status": "started"}
    try:
        with locked(parent, trust):
            try:
                os.stat(name, dir_fd=parent, follow_symlinks=False)
            except FileNotFoundError:
                pass
            else:
                raise Rejected("The release directory already exists; it cannot be overwritten.")
            audit(parent, event, trust)
            try:
                descriptor, verification, bundle = verified_release(updater.Official() if api is None else api, version, assets_directory, trust)
                updater.require(RECEIPT not in bundle, "Official resources collide with the local preparation receipt.")
                result = {"schema": 1, "status": "prepared", "operation_id": event["operation_id"], "version": version, "source_sha": descriptor["source_sha"], "release_directory": release_directory, "release_directory_created": True, "active_release_changed": False, "service_action": "none", "database_action": "none", "program_executed": False, "source_and_integrity_verified": verification["source_and_integrity_verified"], "independent_publisher_signature": False, "required_operator_checks": ["host_environment_and_database_preflight", "configuration_secrets_and_permissions", "backup_and_isolated_restore_for_existing_installation", "installed_plugin_manifest_compatibility", "explicit_database_migration_and_first_admin", "reviewed_service_configuration_and_activation", "https_readiness_and_business_verification"]}
                receipt = dict(result, receipt_kind="local_operator_preparation_receipt", official_release_inventory=False, operation_id=event["operation_id"], release_id=descriptor["release_id"], run_id=descriptor["run_id"], run_attempt=descriptor["run_attempt"], distribution_format="raw-binaries-v1", assets=descriptor["assets"], files_sha256={path: updater.digest(raw) for path, (raw, _) in sorted(bundle.items())})
                files = dict(bundle)
                files[RECEIPT] = (updater.json_bytes(receipt), 0o644)
                os.mkdir(stage_name, 0o700, dir_fd=parent)
                stage_fd = os.open(stage_name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent)
                populate(stage_fd, files)
                rename_new(parent, stage_name, parent, name)
                published = True
                os.fchmod(stage_fd, 0o755)
                os.fsync(stage_fd)
                os.fsync(parent)
                audit(parent, dict(event, status="prepared", source_sha=descriptor["source_sha"]), trust)
                return result
            except (Rejected, OSError, TypeError, ValueError, KeyError, AttributeError):
                with contextlib.suppress(OSError, Rejected):
                    audit(parent, dict(event, status="publication_uncertain" if published else "rejected"), trust)
                if published:
                    raise Rejected("The release directory was published but final durability or logging checks failed; inspect the local receipt before retrying.") from None
                raise
    except OSError:
        raise Rejected("Protected Core preparation failed; inspect the private operation log.") from None
    finally:
        if stage_fd >= 0:
            if not published:
                with contextlib.suppress(OSError):
                    remove_private_tree(stage_fd)
            os.close(stage_fd)
        if not published:
            with contextlib.suppress(OSError):
                os.rmdir(stage_name, dir_fd=parent)
        os.close(parent)


def operating_system_support(distribution, version):
    """Report support scope, not permission to install or host acceptance."""
    baseline = (distribution, version) == ("ubuntu", "24.04")
    supported = distribution == "ubuntu" and version in {"22.04", "24.04"}
    return {
        "status": "ok" if supported else "warning",
        "distribution": distribution, "version": version,
        "supported_baseline": "ubuntu 24.04",
        "deployment_targets": ["ubuntu 22.04", "ubuntu 24.04"],
        "native_ci_baseline": "ubuntu 24.04",
        "support_status": "native_ci_baseline" if baseline else
                          "supported_target" if supported else "unsupported",
        "this_host_validated": False,
        "notice": "Official release requires successful exact-source native CI on both Ubuntu 22.04 and 24.04. Ubuntu 26.04 and other distributions are not supported targets. This report does not query CI, change installer host guards or verify this host.",
    }


def doctor(release_directory, socket_path=None, listen_address="127.0.0.1:18080", trust=updater._Trust()):
    """Read host facts only: no execution, network connection, bind or SQL."""
    parent_path, name = target_path(release_directory)
    checks = []
    def check(identifier, state, **facts):
        checks.append(dict(check=identifier, status=state, **facts))
    check("platform", "ok" if sys.platform == "linux" and platform.machine() == "x86_64" else "blocked", system=platform.system(), architecture=platform.machine())
    try:
        values = dict(line.split("=", 1) for line in Path("/etc/os-release").read_text().splitlines() if "=" in line)
        distro, release = values.get("ID", "").strip('"'), values.get("VERSION_ID", "").strip('"')
        facts = operating_system_support(distro, release)
        check("operating_system", facts.pop("status"), **facts)
    except (OSError, ValueError):
        check("operating_system", "warning", reason="OS release metadata could not be read.")
    available = {name: shutil.which(name, path=os.defpath + ":/usr/sbin:/sbin") is not None for name in ("python3", "systemctl", "systemd-analyze", "nginx", "mysql", "mysqldump", "openssl", "curl")}
    check("tools", "ok" if all(available.values()) else "warning", available=available, notice="Panel-managed tools can use separately verified paths; no program was executed.")
    try:
        systemd = Path("/proc/1/comm").read_text().strip() == "systemd"
        check("system_manager", "ok" if systemd else "warning", systemd_is_pid_one=systemd, notice="No service manager action was requested.")
    except OSError:
        check("system_manager", "warning", reason="The active system manager could not be inspected.")
    ancestor = parent_path
    while not os.path.lexists(ancestor) and ancestor != "/":
        ancestor = os.path.dirname(ancestor)
    try:
        descriptor = parent_directory(ancestor, trust)
        os.close(descriptor)
        existing = os.path.lexists(release_directory)
        check("release_path", "blocked" if existing else "ok", exists=existing, parent_exists=ancestor == parent_path)
        free = shutil.disk_usage(ancestor).free
        minimum = updater.MAX_EXPANDED + 2 * updater.MAX_FILE + (16 << 20)
        check("disk_space", "ok" if free >= minimum else "blocked", free_bytes=free, minimum_bytes=minimum, notice="Preparation estimate only; database and backup capacity are separate.")
    except (OSError, Rejected):
        check("release_path", "blocked", reason="Output ancestors are not protected root-owned directories.")
    if socket_path is not None:
        updater.exact_path(socket_path)
        try:
            info = os.lstat(socket_path)
            check("database_socket", "ok" if stat.S_ISSOCK(info.st_mode) else "blocked", is_socket=stat.S_ISSOCK(info.st_mode), owner_uid=info.st_uid, mode=oct(stat.S_IMODE(info.st_mode)), notice="No connection or database validation was performed; review ancestor permissions and service namespace separately.")
        except OSError:
            check("database_socket", "blocked", reason="The declared socket could not be inspected.")
    else:
        check("database_socket", "warning", reason="No explicit host socket was supplied; database readiness is unknown.")
    try:
        host, separator, raw_port = listen_address.rpartition(":")
        host = host[1:-1] if host.startswith("[") and host.endswith("]") else host
        updater.require(bool(separator) and raw_port.isascii() and raw_port.isdigit() and 1 <= int(raw_port) <= 65535 and ipaddress.ip_address(host).is_loopback, "A loopback IP listener with a valid port is required.")
        port, occupied, readable = int(raw_port), False, True
        for source in ("/proc/net/tcp", "/proc/net/tcp6"):
            try:
                for line in Path(source).read_text().splitlines()[1:]:
                    fields = line.split()
                    if len(fields) >= 4 and fields[3] == "0A" and int(fields[1].rsplit(":", 1)[1], 16) == port:
                        occupied = True
            except (OSError, ValueError, IndexError):
                readable = False
        check("api_listener", "warning" if occupied or not readable else "ok", port=port, port_in_use=occupied, listener_table_readable=readable, notice="Read-only port observation; an existing listener may be the current API. No bind was attempted.")
    except (Rejected, ValueError):
        check("api_listener", "blocked", reason="A loopback IP listener with a valid port is required.")
    state = "blocked" if any(item["status"] == "blocked" for item in checks) else "warning" if any(item["status"] == "warning" for item in checks) else "ok"
    return {"schema": 1, "status": state, "mode": "read_only_host_facts", "checks": checks, "installation_changed": False, "service_action": "none", "database_action": "none", "program_executed": False, "database_readiness_verified": False, "application_readiness_verified": False}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    preparing = commands.add_parser("prepare", help="Verify and assemble an explicit official Core version, without activation.")
    preparing.add_argument("--version", required=True)
    preparing.add_argument("--release-directory")
    preparing.add_argument("--assets-directory")
    preparing.add_argument("--github-token-file")
    checking = commands.add_parser("doctor", help="Read host environment facts without running programs or connecting to a database.")
    checking.add_argument("--release-directory", required=True)
    checking.add_argument("--socket")
    checking.add_argument("--listen-address", default="127.0.0.1:18080")
    options = parser.parse_args(argv)
    try:
        updater.require(sys.platform == "linux" and os.geteuid() == 0, "Run this Linux host tool explicitly as root.")
        os.umask(0o077)
        if options.action == "doctor":
            result = doctor(options.release_directory, options.socket, options.listen_address)
        else:
            updater.require(platform.machine() == "x86_64", "Core preparation supports Linux amd64 hosts only.")
            updater.stable(options.version)
            token = ""
            if options.github_token_file:
                token = updater.load_protected_token(options.github_token_file)
            result = prepare(options.version, options.release_directory or "/opt/aster/releases/" + options.version, options.assets_directory, updater.Official(token))
        print(json.dumps(result, sort_keys=True, ensure_ascii=False))
        return 1 if result["status"] == "blocked" else 0
    except (Rejected, OSError, TypeError, ValueError, KeyError, AttributeError) as exc:
        print("Core preparation rejected: " + (str(exc) if isinstance(exc, Rejected) else "Official release or protected filesystem checks failed."), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
