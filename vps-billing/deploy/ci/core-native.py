#!/usr/bin/env python3
"""Real executor scenarios on fresh, dedicated hosted-runner native instances.

The only candidate-verifier injection lives in this CI helper's Python API.
Production CLI verification stays unchanged. Candidate metadata is explicitly
source-built; no official PREPARED receipt or PROVENANCE is manufactured.
"""
import argparse
import contextlib
import errno
import hashlib
import grp
import http.cookiejar
import importlib.util
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import shutil
import signal
import socket
import ssl
import stat
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.request


class AcceptanceFailure(RuntimeError):
    pass


PUBLISHED_VERSION = "0.5.2"
CANDIDATE_SCHEMA = 13
PUBLISHED_SOURCE = "9ed610a283020f50e2cfc529f8b5145b4a9c8fc3"
PUBLISHED_RELEASE_ID = 413559379
PUBLISHED_BUILD_RUN_ID = 38702170922
PUBLISHED_ASSETS = frozenset({
    "aster-api-0.5.2-linux-amd64", "aster-plugin-host-0.5.2-linux-amd64",
    "aster-core-0.5.2-linux-amd64-resources.tar.gz", "BUILD.json", "PROVENANCE.json", "SHA256SUMS",
})

STAGE = "initializing"
NATIVE_TRACES = {}
SAFE_REJECTIONS = {
    "Public status ancestors must be traversable by the non-root API.": "public_status_parent_not_traversable",
    "Public status ancestors cannot be shared writable or privileged directories.": "public_status_parent_unsafe_mode",
    "A fixed native operation failed; raw diagnostics were suppressed.": "fixed_native_command_failed",
    "Native operation stopped; inspect its redacted status and private journal. No database rollback was performed.": "executor_recorded_recovery_checkpoint",
    "Effective systemd API service differs from the reviewed dedicated instance.": "effective_api_service_mismatch",
    "Effective managed edge service differs from its reviewed unit.": "effective_edge_service_mismatch",
    "API did not become active.": "api_not_active",
    "Application preflight did not return a valid checked report.": "preflight_report_not_checked",
    "A verified, source-pinned local preparation receipt is required.": "prepared_receipt_identity_rejected",
    "Prepared file permissions changed.": "prepared_file_mode_changed",
    "Prepared release bytes changed.": "prepared_file_hash_changed",
    "Required source-bound runtime resources are missing.": "prepared_runtime_resources_missing",
    "Prepared source mapping differs from the reviewed runtime layout.": "prepared_source_layout_rejected",
    "Prepared release has an incomplete or unexpected runtime inventory.": "prepared_runtime_inventory_rejected",
    "Paths must be canonical absolute paths without configuration metacharacters.": "protected_path_unsupported_syntax",
    "A canonical absolute protected path is required.": "protected_path_noncanonical",
    "Protected file exceeds its hashing bound.": "protected_hash_size_exceeded",
    "Protected input changed while hashing.": "protected_hash_input_changed",
    "Prepared member paths must use canonical reviewed resource components.": "prepared_member_path_rejected",
    "Invalid prepared inventory.": "prepared_inventory_invalid",
    "Prepared directories cannot contain symlinks.": "prepared_directory_symlink",
    "Unexpected prepared release file.": "prepared_unexpected_file",
    "Prepared release inventory is incomplete.": "prepared_inventory_incomplete",
    "Prepared BUILD identity differs from its receipt.": "prepared_build_receipt_mismatch",
    "A complete raw Core BUILD/source inventory is required.": "prepared_build_format_rejected",
    "Prepared binary or preserved LICENSE identity differs from BUILD.": "prepared_binary_or_license_mismatch",
    "Prepared SQL source inventory differs.": "prepared_sql_inventory_mismatch",
    "Migration resources must be a complete source-bound prefix.": "prepared_sql_prefix_rejected",
    "Prepared release needs migrations and compiled web assets.": "prepared_migrations_or_web_missing",
    "Paths must be root-owned and protected.": "protected_owner_rejected",
    "Writable or privileged paths are forbidden.": "protected_mode_rejected",
    "Links and special files are forbidden.": "protected_link_or_special_file",
    "Private paths must use mode 0700 or 0600.": "protected_private_mode_rejected",
    "Protected filesystem input could not be opened.": "protected_open_failed",
    "Protected input exceeds its size limit.": "protected_read_size_exceeded",
    "Protected input changed during verification.": "protected_read_input_changed",
    "A stable semantic version is required.": "stable_version_rejected",
    "JSON exceeds its size limit.": "json_size_rejected",
    "JSON contains duplicate or oversized keys.": "json_keys_rejected",
    "Non-finite JSON values are forbidden.": "json_nonfinite_rejected",
    "Invalid JSON input.": "json_syntax_rejected",
    "JSON nesting or array bounds exceeded.": "json_shape_bounds_rejected",
    "A static Linux amd64 ELF binary is required.": "prepared_elf_identity_rejected",
    "Invalid ELF program headers.": "prepared_elf_headers_rejected",
    "Dynamic loaders or invalid ELF segments are forbidden.": "prepared_elf_segments_rejected",
    "ELF entry point is outside executable segments.": "prepared_elf_entry_rejected",
    "EnvironmentFile must use the supported literal non-secret settings only.": "runtime_environment_literal_rejected",
    "Only production local-socket deployment with disabled providers is supported.": "runtime_environment_mode_rejected",
    "Runtime secrets must use file references.": "runtime_environment_secret_reference_missing",
    "New private backup directory ACL could not be normalized.": "new_backup_default_acl_rejected",
    "New private backup directory metadata changed during creation.": "new_backup_directory_metadata_changed",
    "Invalid operation journal structure.": "operation_journal_structure_rejected",
    "Invalid target source in operation journal.": "operation_journal_target_source_rejected",
    "Invalid operation journal state.": "operation_journal_state_rejected",
    "Invalid database migration checkpoint.": "operation_journal_migration_rejected",
    "Invalid operation receipt category.": "operation_receipt_category_rejected",
    "Only completed target activation can claim HTTPS readiness.": "operation_receipt_readiness_rejected",
    "Fresh install journal cannot describe a previous release.": "operation_journal_install_previous_rejected",
    "Upgrade journal requires previous source identity.": "operation_journal_previous_source_rejected",
    "Invalid operation journal timestamp.": "operation_journal_timestamp_rejected",
    "Invalid operation checkpoint.": "operation_checkpoint_rejected",
    "Invalid operation journal flag.": "operation_journal_flag_rejected",
    "Invalid activation checkpoint result.": "activation_checkpoint_result_rejected",
    "Successful operation journal is incomplete or inconsistent.": "operation_success_receipt_inconsistent",
    "Rollback receipt is inconsistent.": "operation_rollback_receipt_inconsistent",
    "Invalid stopped target activation checkpoint.": "stopped_target_checkpoint_rejected",
    "Stopped target activation checkpoint is inconsistent.": "stopped_target_checkpoint_inconsistent",
    "Stopped target activation found an unexpected current release.": "stopped_target_current_release_changed",
    "Stopped target recovery requires verified HTTPS maintenance.": "stopped_target_maintenance_unverified",
    "Stopped target recovery refuses an active service.": "stopped_target_service_not_stopped",
    "Confirmed service or plugin inputs changed before stopped target recovery.": "stopped_target_service_or_plugin_changed",
}


def stage(name):
    global STAGE
    require(re.fullmatch(r"[a-z0-9-]{1,64}", name), "Invalid fixed diagnostic stage")
    STAGE = name
    print("CORE_NATIVE_STAGE=" + name, flush=True)


def require(value, message):
    if not value:
        raise AcceptanceFailure(message)


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    loaded = importlib.util.module_from_spec(spec)
    sys.modules[name] = loaded
    spec.loader.exec_module(loaded)
    return loaded


def sha256(value):
    return hashlib.sha256(value).hexdigest()


def command(args, *, data=None, timeout=120, allowed=(0,), environment=None):
    """Never publish raw command output: SQL, cookies and DSNs are private."""
    try:
        result = subprocess.run(list(map(str, args)), input=data, capture_output=True,
                                timeout=timeout, env=environment, cwd="/")
    except subprocess.TimeoutExpired:
        raise AcceptanceFailure("Native command exceeded its fixed timeout: " + Path(str(args[0])).name) from None
    require(result.returncode in allowed,
            "Native command failed: " + Path(str(args[0])).name + " (exit " + str(result.returncode) + ")")
    return result.stdout


def write(path, value, mode=0o600):
    path = Path(path)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(descriptor, "wb") as output:
        output.write(value.encode() if isinstance(value, str) else value)
        output.flush()
        os.fchmod(output.fileno(), mode)
        os.fsync(output.fileno())


def new_public_directory(path):
    """Set an exact mode on this newly created fixture directory despite umask."""
    os.mkdir(path, 0o755)
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        os.fchmod(descriptor, 0o755)
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def create_public_controls(control, mount):
    # Public binaries/certificates/status and the socket need traversal by the
    # dedicated API/edge identities. Secret files retain their separate modes.
    new_public_directory(control)
    new_public_directory(control / "public")
    new_public_directory(mount)


def create_release_parent(root):
    for path in (root, root / "releases"):
        if not path.exists():
            new_public_directory(path)
        else:
            info = path.lstat()
            require(stat.S_ISDIR(info.st_mode) and info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) == 0o755,
                    "Existing fixture release parent must already be protected and traversable")


def fact(evidence, name, value):
    require(re.fullmatch(r"[a-z][a-z0-9-]{0,63}", name), "Invalid fixed fact name")
    write(Path(evidence) / (name + ".json"), json.dumps(value, sort_keys=True, indent=2) + "\n", 0o644)
    print("CORE_NATIVE_" + name.upper().replace("-", "_") + "_JSON=" + json.dumps(value, sort_keys=True), flush=True)


def diagnostic_native(executor):
    """Trace real native calls with fixed labels; no output or result is faked."""
    class DiagnosticNative(executor.Native):
        def traced(self, label, function):
            trace = NATIVE_TRACES.setdefault(self.spec["operation_id"], {"calls": []})
            trace["calls"] = (trace["calls"] + [{"substep": label, "event": "started"}])[-24:]
            try:
                result = function()
            except BaseException as failure:
                event = {"substep": label, "event": "failed",
                         "reason": SAFE_REJECTIONS.get(str(failure), "failure_details_suppressed")}
                trace.setdefault("first_failure", event)
                trace["calls"] = (trace["calls"] + [event])[-24:]
                raise
            trace["calls"] = (trace["calls"] + [{"substep": label, "event": "completed"}])[-24:]
            return result

        def validate_service(self, release):
            parent = super()
            return self.traced("validate-service", lambda: parent.validate_service(release))

        def verify_empty_service_hooks(self):
            parent = super()
            trace = NATIVE_TRACES.setdefault(self.spec["operation_id"], {"calls": []})
            trace["typed_service_hooks"] = {"source": "systemd-dbus-json", "verified": False}
            value = self.traced("typed-service-hooks", parent.verify_empty_service_hooks)
            trace["typed_service_hooks"] = {"source": "systemd-dbus-json", "verified":
                isinstance(value, dict) and set(value) == {"ExecCondition", "ExecStartPre", "ExecStartPost", "ExecStop", "ExecStopPost", "ExecReload"} and
                all(flag is True for flag in value.values())}
            return value

        def service(self, verb, unit=None):
            require(verb in {"start", "stop", "restart", "daemon-reload", "show", "enable", "is-enabled"}, "Unexpected diagnostic service verb")
            selected = "api" if unit == self.spec["api_unit"] else "edge" if unit == executor.edge_unit(self.spec) else "manager"
            parent = super()
            return self.traced(selected + "-" + verb, lambda: parent.service(verb, unit))

        def edge(self, action):
            require(action in {"test", "start", "reload"}, "Unexpected diagnostic edge action")
            parent = super()
            return self.traced("nginx-" + action, lambda: parent.edge(action))
    return DiagnosticNative


def selected_unit_diagnostics(spec, role, values):
    """Project systemctl properties into enums, numbers and expected-value checks."""
    api = role == "api"
    expected_unit = spec["api_unit"] if api else spec["api_unit"][:-8] + "-edge.service"
    checks = {
        "fragment_expected": values.get("FragmentPath") == "/etc/systemd/system/" + expected_unit,
        "no_dropins": values.get("DropInPaths") == "",
    }
    if api:
        current = spec["instance_root"] + "/current"
        executable = current + "/bin/aster-api"
        parsed = re.search(r"(?:^|[ {])path=([^ ;]+) ; argv\[\]=([^;]+?) ;", values.get("ExecStart", ""))
        checks.update({"user_expected": values.get("User") == spec["runtime_user"],
            "group_expected": values.get("Group") == spec["runtime_user"],
            "working_directory_expected": values.get("WorkingDirectory") == current,
            "exec_start_parsed": parsed is not None,
            "exec_start_path_expected": parsed is not None and parsed[1] == executable,
            "exec_start_argv_expected": parsed is not None and parsed[2].strip() == executable,
            "environment_files_expected": values.get("EnvironmentFiles") == spec["config_root"] + "/api.env (ignore_errors=no)"})
    known = {
        "LoadState": {"loaded", "not-found", "error", "bad-setting", "masked", "stub", "merged"},
        "ActiveState": {"active", "inactive", "failed", "activating", "deactivating", "reloading", "maintenance", "refreshing"},
        "SubState": {"dead", "running", "failed", "start", "start-pre", "start-post", "stop", "stop-sigterm", "stop-sigkill", "stop-post", "auto-restart", "auto-restart-queued", "exited", "condition", "final-sigterm", "final-sigkill", "listening", "reload", "cleaning"},
        "Result": {"success", "exit-code", "signal", "core-dump", "watchdog", "timeout", "start-limit-hit", "resources", "protocol", "oom-kill", "exec-condition", "condition", "skipped"},
    }
    report = {"role": role, "checks": checks, "properties_present": {key: key in values for key in
        ("LoadState", "ActiveState", "SubState", "Result", "FragmentPath", "DropInPaths", "User", "Group", "WorkingDirectory", "ExecStart", "EnvironmentFiles")}}
    if api:
        report["hook_verification_source"] = "systemd-dbus-json"
        report["cli_hook_serialization_ignored"] = True
    for key, allowed in known.items():
        report[key] = values.get(key) if values.get(key) in allowed else "other_or_absent"
    for key in ("ExecMainCode", "ExecMainStatus", "StatusErrno"):
        value = values.get(key, "")
        report[key] = int(value) if re.fullmatch(r"[0-9]{1,5}", value) and int(value) <= 65535 else None
    return report


def actual_unit_diagnostics(spec):
    properties = ("LoadState", "ActiveState", "SubState", "Result", "ExecMainCode", "ExecMainStatus", "StatusErrno",
                  "FragmentPath", "DropInPaths", "User", "Group", "WorkingDirectory", "ExecStart", "EnvironmentFiles")
    output = []
    for role, unit in (("api", spec["api_unit"]), ("edge", spec["api_unit"][:-8] + "-edge.service")):
        result = subprocess.run(["/usr/bin/systemctl", "show", "--all", unit, *["--property=" + key for key in properties]],
                                capture_output=True, timeout=5, cwd="/")
        require(len(result.stdout) <= 64 << 10, "Selected unit properties exceeded their diagnostic bound")
        values = dict(line.split("=", 1) for line in result.stdout.decode("utf-8", errors="replace").splitlines() if "=" in line)
        output.append(selected_unit_diagnostics(spec, role, values))
    return output


def failure_diagnostics(resources, failure):
    """Only fixed reasons, validated public receipts and file metadata escape."""
    reason = SAFE_REJECTIONS.get(str(failure), "unclassified_failure_details_suppressed")
    report = {"stage": STAGE, "reason": reason, "failure_class": type(failure).__name__, "fixtures": []}
    if hasattr(resources, "legacy_migration_trace"):
        report["legacy_migration"] = resources.legacy_migration_trace
    if hasattr(resources, "restore_paths"):
        report["restore_paths"] = restore_path_diagnostics(resources.restore_paths)
    if hasattr(resources, "restore_filesystem_trace"):
        report["restore_filesystem"] = resources.restore_filesystem_trace
    for fixture in getattr(resources, "fixtures", []):
        entry = {"action": fixture.spec["action"], "operation_id": fixture.spec["operation_id"],
                 "checkpoint_available": False, "path_metadata": []}
        entry["native_trace"] = NATIVE_TRACES.get(fixture.spec["operation_id"], {"calls": []})
        try:
            entry["selected_units"] = actual_unit_diagnostics(fixture.spec)
        except Exception:
            entry["selected_unit_diagnostics_unavailable"] = True
        try:
            entry["checkpoint"] = fixture.status()
            entry["checkpoint_available"] = True
        except Exception:
            pass
        paths = {"instance_root": fixture.root, "releases_parent": fixture.root / "releases",
                 "config_root": fixture.config, "state_directory": fixture.state,
                 "status_parent": Path(fixture.spec["status_file"]).parent,
                 "controls": resources.control, "socket_parent": resources.mount}
        for label, path in paths.items():
            try:
                info = path.lstat()
                entry["path_metadata"].append({"label": label, "uid": info.st_uid, "gid": info.st_gid,
                    "mode": oct(stat.S_IMODE(info.st_mode)), "directory": stat.S_ISDIR(info.st_mode)})
            except OSError:
                entry["path_metadata"].append({"label": label, "unavailable": True})
        report["fixtures"].append(entry)
    return report


def acl_wire_summary(raw):
    """Project Linux ACL version/tags/permissions; never export identity fields."""
    result = {"byte_count": min(len(raw), 65536)}
    if len(raw) < 4 or len(raw) > 65536 or (len(raw) - 4) % 8:
        result["valid_wire_shape"] = False
        return result
    version, = struct.unpack_from("<I", raw)
    result.update(version=version if version == 2 else None, valid_wire_shape=version == 2,
                  entry_count=(len(raw) - 4) // 8)
    if version != 2:
        return result
    tags = {1, 2, 4, 8, 16, 32}
    result["entries"] = []
    for offset in range(4, min(len(raw), 4 + 128 * 8), 8):
        tag, permissions = struct.unpack_from("<HH", raw, offset)
        result["entries"].append({"tag": tag if tag in tags else None,
                                  "permissions": permissions if permissions <= 7 else None})
    result["entries_truncated"] = result["entry_count"] > 128
    return result


def owned_acl_diagnostics(path):
    result = {}
    for label, name in (("default", "system.posix_acl_default"), ("access", "system.posix_acl_access")):
        try:
            raw = os.getxattr(path, name, follow_symlinks=False)
            result[label] = {"present": True, **acl_wire_summary(raw)}
        except OSError as failure:
            result[label] = {"present": False} if failure.errno == errno.ENODATA else {
                "present": None, "unavailable": "unsupported" if failure.errno in {errno.ENOTSUP, errno.EOPNOTSUPP} else "read_failed"}
        except AttributeError:
            result[label] = {"present": None, "unavailable": "unsupported"}
    return result


def restore_path_diagnostics(paths):
    """Only owned restore-path metadata escapes; names and contents stay private."""
    selected = list(paths.items())
    output = paths.get("backup_output")
    if output is not None:
        try:
            info = output.lstat()
            if stat.S_ISDIR(info.st_mode):
                for number, path in zip(range(8), output.iterdir()):
                    prefix = "backup_child_" + str(number)
                    selected.append((prefix, path))
                    if stat.S_ISDIR(path.lstat().st_mode):
                        selected.extend((prefix + "_" + label, path / name) for label, name in (
                            ("archive", "database.sql.gz"), ("metadata", "BACKUP.json"),
                            ("notice", "BACKUP.txt"), ("checksums", "SHA256SUMS")))
        except OSError:
            pass
    result = []
    for label, path in selected:
        try:
            info = path.lstat()
            result.append({"label": label, "uid": info.st_uid, "gid": info.st_gid,
                "mode": oct(stat.S_IMODE(info.st_mode)), "nlink": info.st_nlink,
                "directory": stat.S_ISDIR(info.st_mode), "regular_file": stat.S_ISREG(info.st_mode),
                "acl": owned_acl_diagnostics(path)})
        except OSError:
            result.append({"label": label, "unavailable": True})
    return result


def protected_component_diagnostics(path):
    """Read bounded component metadata through descriptors; export no names."""
    if not isinstance(path, str) or len(path) > 1024 or not path.startswith("/"):
        return {"invalid_path_input": True}
    parts = path[1:].split("/")
    if len(parts) > 64 or any(part in {"", ".", ".."} for part in parts):
        return {"invalid_path_input": True}
    result = {"component_count": len(parts), "components": []}
    descriptor = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
    try:
        for index, part in enumerate(parts):
            try:
                info = os.stat(part, dir_fd=descriptor, follow_symlinks=False)
                kind = "directory" if stat.S_ISDIR(info.st_mode) else "regular_file" if stat.S_ISREG(info.st_mode) else "symlink" if stat.S_ISLNK(info.st_mode) else "other"
                entry = {"index": index, "ancestor": index < len(parts) - 1,
                    "endpoint": index == len(parts) - 1, "uid": info.st_uid, "gid": info.st_gid,
                    "mode": oct(stat.S_IMODE(info.st_mode)), "nlink": info.st_nlink, "type": kind,
                    "privileged_bits": bool(info.st_mode & (stat.S_ISUID | stat.S_ISGID)),
                    "group_or_world_writable": bool(info.st_mode & 0o022),
                    "sticky_directory": stat.S_ISDIR(info.st_mode) and bool(info.st_mode & stat.S_ISVTX)}
                if index >= len(parts) - 12:
                    result["components"].append(entry)
                if index == len(parts) - 1 or kind != "directory":
                    break
                child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=descriptor)
                os.close(descriptor)
                descriptor = child
            except OSError:
                result["metadata_unavailable_at_index"] = index
                break
    finally:
        os.close(descriptor)
    return result


@contextlib.contextmanager
def observe_restore_protected_open(executor, fixture):
    """Observe the actual protected open only during this isolated restore."""
    original = executor.updater.protected_open
    resources = fixture.resources
    trace = resources.restore_filesystem_trace = {"failed_opens": []}
    def observed(path, *args, **kwargs):
        try:
            return original(path, *args, **kwargs)
        except executor.updater.Rejected:
            role = "other_reviewed_input"
            if isinstance(path, str):
                output = getattr(resources, "restore_paths", {}).get("backup_output")
                if path == fixture.baseline or path.startswith(fixture.baseline + "/"):
                    role = "published_release"
                elif output is not None and (path == str(output) or path.startswith(str(output) + "/")):
                    relative = path[len(str(output)):].strip("/")
                    role = "backup_output" if not relative else "backup_folder" if "/" not in relative else {
                        "database.sql.gz": "backup_archive", "BACKUP.json": "backup_metadata",
                        "BACKUP.txt": "backup_notice", "SHA256SUMS": "backup_checksums"}.get(relative.rsplit("/", 1)[-1], "backup_other_member")
                else:
                    for label, selected in getattr(resources, "restore_paths", {}).items():
                        if path == str(selected):
                            role = label
                            break
            try:
                details = protected_component_diagnostics(path)
            except Exception:
                details = {"metadata_unavailable": True}
            trace["failed_opens"] = (trace["failed_opens"] + [{"role": role, **details}])[-8:]
            raise
    executor.updater.protected_open = observed
    try:
        yield
    finally:
        executor.updater.protected_open = original


def verify_source_candidate(source, assets, installed):
    """Verify actual package and every source copy against the selected Git commit."""
    pipeline = module("core_acceptance_pipeline", "release_pipeline.py")
    source, assets, installed = map(Path, (source, assets, installed))
    git = ["git", "-c", "safe.directory=" + str(source), "-C", str(source)]
    commit = command([*git, "rev-parse", "--verify", "HEAD"]).decode().strip()
    require(re.fullmatch(r"[a-f0-9]{40}", commit), "Invalid selected Git commit")
    build = pipeline.strict_json((assets / "BUILD.json").read_bytes())
    version = build["component_versions"]["core"]
    require(tuple(map(int, version.split("."))) > tuple(map(int, PUBLISHED_VERSION.split("."))),
            "Candidate must be newer than the published baseline")
    names = pipeline.package_files(version, "core", "raw-binaries-v1")
    require(set(item.name for item in assets.iterdir()) == set(names), "Source-built assets must have exact local package inventory")
    files = {name: (assets / name).read_bytes() for name in names}
    inventory = pipeline.validate_package(files, commit, version, "core")
    _, _, bundle = pipeline.runtime_bundle(files, version, "core")
    copies = build["source_files"]
    for destination, source_path in copies.items():
        expected = command([*git, "show", commit + ":" + source_path])
        require(bundle[destination] == expected, "Candidate resource differs from selected Git source")
    for source_path, digest in build["input_sha256"].items():
        require(sha256(command([*git, "show", commit + ":" + source_path])) == digest,
                "Candidate build input differs from selected Git source")
    sql_paths = command([*git, "ls-tree", "-r", "--name-only", commit, "vps-billing/backend/migrations"]).decode().splitlines()
    expected_sql = {"migrations/" + Path(name).name: name for name in sql_paths if name.endswith(".sql")}
    require({name: path for name, path in copies.items() if name.startswith("migrations/")} == expected_sql,
            "Candidate is missing selected SQL resources")
    # The installed candidate is root-controlled and exactly the checked bundle;
    # only the parent fixture's harmless marker may be added by its web checks.
    installed_files = {str(path.relative_to(installed)) for path in installed.rglob("*") if path.is_file()}
    require(installed_files - {"web/release-id.txt"} == set(bundle), "Installed source candidate inventory differs")
    for name, raw in bundle.items():
        path = installed / name
        info = path.lstat()
        require(stat.S_ISREG(info.st_mode) and info.st_uid == 0 and info.st_nlink == 1 and not info.st_mode & 0o022,
                "Source candidate file is not protected")
        require(path.read_bytes() == raw, "Installed candidate bytes differ from checked assets")
    for path in (installed, *(path for path in installed.rglob("*") if path.is_dir())):
        info = path.lstat()
        require(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and stat.S_IMODE(info.st_mode) == 0o755,
                "Source candidate directory is not protected")
    return {"version": version, "source_commit": commit, "directory": str(installed),
            "trust": "selected-source-built-candidate-not-official-release",
            "official_provenance_fabricated": False, "package_inventory": inventory,
            "copied_source_files_verified": len(copies), "migration_count": len(expected_sql),
            "binary_sha256": build["binary_sha256"]}


def verify_published_baseline(directory):
    """Read the receipt created by real official prepare; never synthesize it."""
    directory = Path(directory)
    receipt = json.loads((directory / "manifests/PREPARED.json").read_text())
    build = json.loads((directory / "manifests/BUILD.json").read_text())
    require(build["component_versions"]["core"] == PUBLISHED_VERSION, "Baseline is not actual published Core v" + PUBLISHED_VERSION)
    require(build["source_commit"] == PUBLISHED_SOURCE, "Published baseline source changed")
    require(receipt.get("version") == PUBLISHED_VERSION and receipt.get("source_sha") == build["source_commit"], "Published baseline receipt identity mismatch")
    assets = receipt.get("assets", [])
    require(receipt.get("release_id") == PUBLISHED_RELEASE_ID and receipt.get("run_id") == PUBLISHED_BUILD_RUN_ID and
            receipt.get("run_attempt") == 1 and len(assets) == 6 and
            {item.get("name") for item in assets} == PUBLISHED_ASSETS,
            "Published baseline release/build identity changed")
    for name, digest in build["binary_sha256"].items():
        require(sha256((directory / name).read_bytes()) == digest, "Published baseline executable differs from verified BUILD")
    return {"version": PUBLISHED_VERSION, "source_commit": build["source_commit"],
            "directory": str(directory), "trust": "actual-official-published-release-prepared-independently",
            "tag": "v" + PUBLISHED_VERSION, "release_id": receipt["release_id"], "build_run_id": receipt["run_id"],
            "assets": receipt["assets"],
            "binary_sha256": build["binary_sha256"], "prepared_receipt_sha256": sha256((directory / "manifests/PREPARED.json").read_bytes()),
            "official_provenance_fabricated": False}


class Resources:
    """Track only new fixture resources, including partially provisioned ones."""
    def __init__(self, args):
        self.args = args
        self.fixtures = []
        self.restores = []
        self.mount = args.fixture_root / "core-socket"
        self.control = args.fixture_root / "core-control"
        self.bound = False
        self.mysql = self.control / "mysql"
        self.dump = self.control / "mysqldump"
        self.nginx = self.control / "nginx"
        require(not self.control.exists() and not self.mount.exists(), "Refusing to reuse Core fixture controls")
        create_public_controls(self.control, self.mount)
        for source, target in ((args.mysql_root / "bin/mysql", self.mysql),
                               (args.mysql_root / "bin/mysqldump", self.dump), (args.nginx, self.nginx)):
            require(source.is_file() and not source.is_symlink(), "Native distribution tool is unavailable")
            write(target, source.read_bytes(), 0o755)
        write(self.control / "dba.cnf", args.mysql_admin.read_bytes())
        write(self.mount / "mysql.sock", b"")
        command(["/usr/bin/mount", "--bind", args.mysql_socket, self.mount / "mysql.sock"])
        self.bound = True
        self.socket = self.mount / "mysql.sock"
        require(self.sql("SELECT @@datadir;") == str(args.mysql_fixture / "data") + "/", "Native database fixture identity mismatch")
        require(self.sql("SELECT @@skip_networking;") == "1", "Native install fixture must have TCP disabled")
        self.tls()

    def sql(self, query, config=None, database=None):
        arguments = [self.mysql, "--defaults-file=" + str(config or self.control / "dba.cnf"),
                     "--no-login-paths", "--protocol=SOCKET", "--socket=" + str(self.socket),
                     "--batch", "--skip-column-names"]
        if database:
            arguments += ["--database=" + database]
        return command(arguments, data=query.encode()).decode().strip()

    def tls(self):
        c = self.control
        command(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                 "-subj", "/CN=Aster isolated executor CI CA", "-addext", "basicConstraints=critical,CA:TRUE",
                 "-keyout", c / "ca.key", "-out", c / "ca.pem"])
        command(["openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=localhost",
                 "-keyout", c / "server.key", "-out", c / "server.csr"])
        write(c / "server.ext", "subjectAltName=DNS:localhost\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n")
        command(["openssl", "x509", "-req", "-in", c / "server.csr", "-CA", c / "ca.pem", "-CAkey", c / "ca.key",
                 "-CAcreateserial", "-days", "1", "-extfile", c / "server.ext", "-out", c / "server.pem"])
        for name in ("ca.pem", "server.pem"):
            (c / name).chmod(0o644)

    def cleanup(self):
        failed = []
        for fixture in reversed(self.fixtures):
            fixture_stopped = True
            for unit in (fixture.spec["api_unit"], fixture.edge_unit):
                try:
                    command(["systemctl", "disable", unit], allowed=(0, 1, 5), timeout=20)
                    loaded = command(["systemctl", "show", unit, "--property=LoadState", "--value"], allowed=(0, 1)).decode().strip()
                    if loaded != "not-found":
                        command(["systemctl", "stop", unit], allowed=(0, 5), timeout=45)
                        state = command(["systemctl", "show", unit, "--property=ActiveState", "--value"]).decode().strip()
                        require(state in {"inactive", "failed"}, "Owned service did not stop")
                except (AcceptanceFailure, OSError):
                    failed.append("service-stop")
                    fixture_stopped = False
            if not fixture_stopped:
                # Never remove executable/configuration paths from a live unit.
                continue
            for unit in (fixture.spec["api_unit"], fixture.edge_unit):
                with contextlib.suppress(FileNotFoundError):
                    Path("/etc/systemd/system", unit).unlink()
                command(["systemctl", "reset-failed", unit], allowed=(0, 1, 5))
            try:
                if pwd.getpwnam(fixture.spec["runtime_user"]):
                    command(["userdel", fixture.spec["runtime_user"]])
                    command(["groupdel", fixture.spec["runtime_user"]], allowed=(0, 6))
            except KeyError:
                pass
            for path in (fixture.config, fixture.state):
                if path.exists():
                    shutil.rmtree(path)
            database = fixture.spec["database"]
            try:
                self.sql("DROP DATABASE IF EXISTS `" + database + "`;\n" + "\n".join(
                    "DROP USER IF EXISTS '" + database + "_" + role + "'@'localhost';" for role in ("runtime", "migrator", "backup")))
                require(self.sql("SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='" + database + "';") == "0" and
                        self.sql("SELECT COUNT(*) FROM mysql.user WHERE User IN ('" + database + "_runtime','" + database + "_migrator','" + database + "_backup');") == "0", "Core fixture database/accounts remain")
            except AcceptanceFailure:
                failed.append("database-cleanup")
        for database, account in self.restores:
            try:
                self.sql("DROP DATABASE IF EXISTS `" + database + "`; DROP USER IF EXISTS '" + account + "'@'localhost';")
            except AcceptanceFailure:
                failed.append("restore-cleanup")
        command(["systemctl", "daemon-reload"])
        if self.bound and not failed:
            command(["/usr/bin/umount", self.socket])
            self.bound = False
        require(not failed and not self.bound, "Owned Core native fixture cleanup was incomplete")
        for fixture in self.fixtures:
            require(not fixture.config.exists() and not fixture.state.exists(), "Core private fixture paths remain")
            for unit in (fixture.spec["api_unit"], fixture.edge_unit):
                require(not Path("/etc/systemd/system", unit).exists(), "Core fixture unit remains")
                require(not Path("/etc/systemd/system/multi-user.target.wants", unit).is_symlink(), "Core fixture enablement link remains")
            try:
                pwd.getpwnam(fixture.spec["runtime_user"])
            except KeyError:
                pass
            else:
                raise AcceptanceFailure("Core fixture service account remains")
            try:
                grp.getgrnam(fixture.spec["runtime_user"])
            except KeyError:
                pass
            else:
                raise AcceptanceFailure("Core fixture service group remains")
            shutil.rmtree(fixture.root)
        shutil.rmtree(self.mount)
        shutil.rmtree(self.control)


class Fixture:
    def __init__(self, resources, executor, name, candidate, baseline, overrides):
        self.resources, self.executor = resources, executor
        self.name = name
        suffix = resources.args.fixture_root.name.removeprefix("aster-ci-")
        short = sha256((suffix + name).encode())[:10]
        self.root = resources.args.fixture_root / ("core-" + name)
        self.config = Path("/etc/aster-core-ci-" + short)
        self.state = Path("/var/lib/aster-core-ci-" + short)
        self.password = secrets.token_hex(24)
        self.private = resources.control / name
        require(not self.private.exists() and not self.config.exists() and not self.state.exists(), "Refusing existing Core fixture paths")
        self.private.mkdir(mode=0o700)
        create_release_parent(self.root)
        candidate_path = self.root / "releases" / candidate["version"]
        require(not candidate_path.exists(), "Candidate fixture release already exists")
        shutil.copytree(candidate["directory"], candidate_path, copy_function=shutil.copyfile)
        for path in (candidate_path, *candidate_path.rglob("*")):
            path.chmod(0o755 if path.is_dir() or str(path.relative_to(candidate_path)) in {"bin/aster-api", "bin/aster-plugin-host"} or str(path.relative_to(candidate_path)).startswith("deploy/scripts/") else 0o644)
        overrides[str(candidate_path)] = candidate
        self.candidate = str(candidate_path)
        self.baseline = str(self.root / "releases" / PUBLISHED_VERSION)
        with contextlib.ExitStack() as stack:
            sockets = [stack.enter_context(socket.socket()) for _ in range(2)]
            for connection in sockets:
                connection.bind(("127.0.0.1", 0))
            api_port, tls_port = (connection.getsockname()[1] for connection in sockets)
        self.spec = {"schema": 1, "action": "install", "operation_id": secrets.token_hex(16),
            "release_directory": self.candidate, "instance_root": str(self.root), "config_root": str(self.config),
            "state_directory": str(self.state), "origin": "https://localhost:" + str(tls_port),
            "status_file": str(resources.control / "public" / (name + ".json")),
            "listen_address": "127.0.0.1:" + str(api_port), "edge_listen_address": "127.0.0.1",
            "runtime_user": "astcore" + short, "api_unit": "aster-core-ci-" + short + ".service",
            "socket": str(resources.socket), "database": "ac_" + short,
            "mysql_binary": str(resources.mysql), "mysqldump_binary": str(resources.dump),
            "nginx_binary": str(resources.nginx), "nginx_sha256": sha256(resources.nginx.read_bytes()),
            "edge_mode": "managed", "certificate": str(resources.control / "server.pem"),
            "certificate_key": str(resources.control / "server.key"), "ca_file": str(resources.control / "ca.pem"),
            "dba_client_config": str(resources.control / "dba.cnf"), "bootstrap_email": "admin@executor-ci.invalid",
            "enable_at_boot": True,
            "bootstrap_name": "Executor CI Admin", "bootstrap_password_file": str(self.private / "bootstrap")}
        self.edge_unit = executor.edge_unit(self.spec)
        for unit in (self.spec["api_unit"], self.edge_unit):
            require(not Path("/etc/systemd/system", unit).exists(), "Refusing an existing Core fixture unit")
        try:
            pwd.getpwnam(self.spec["runtime_user"])
        except KeyError:
            pass
        else:
            raise AcceptanceFailure("Refusing an existing Core fixture account")
        resources.fixtures.append(self)
        write(self.private / "bootstrap", self.password + "\n")
        self.install_id = self.spec["operation_id"]

    def save_spec(self):
        path = self.private / (self.spec["operation_id"] + ".json")
        if not path.exists():
            write(path, json.dumps(self.spec, sort_keys=True) + "\n")
        return str(path)

    def plan(self):
        stage(self.name + "-plan")
        self.spec_path = self.save_spec()
        self.planned = self.executor.plan(self.spec_path)
        return self.planned

    def execute(self, native=None, resume=False):
        stage(self.name + ("-resume" if resume else "-apply"))
        NATIVE_TRACES[self.spec["operation_id"]] = {"calls": []}
        return self.executor.execute(self.spec_path, self.planned["plan_sha256"], resume=resume, native=native)

    def upgrade(self):
        self.spec = {key: value for key, value in self.spec.items() if key not in self.executor.INSTALL_KEYS}
        self.spec.update(action="upgrade", operation_id=secrets.token_hex(16), release_directory=self.candidate,
            migrator_dsn_file=str(self.state / self.install_id / "credentials/migrator_dsn"),
            backup_client_config=str(self.state / self.install_id / "credentials/backup.cnf"),
            acknowledge_backup_restore_limits=True)
        return self.plan()

    def status(self):
        return self.executor.status(str(self.state), self.spec["operation_id"])

    def checked_journal(self):
        raw = self.executor.read_file(str(self.state / self.spec["operation_id"] / "JOURNAL.json"),
                                      self.executor.updater.MAX_JSON, private=True)
        return self.executor.validate_journal(self.executor.updater.strict_json(raw))

    def login(self):
        return HTTPSClient(self.spec["origin"], self.resources.control / "ca.pem").login("admin@executor-ci.invalid", self.password)

    def verify_status_api(self, expected):
        status, report = self.login().request("/api/v1/admin/updates/operation")
        require(status == 200 and report["data"]["state"] == "available" and report["data"]["operation"] == expected,
                "Read-only API operation receipt differs from actual executor result")
        target = Path(self.spec["status_file"])
        account = pwd.getpwnam(self.spec["runtime_user"])
        require(account.pw_uid != 0 and target.stat().st_uid == 0 and stat.S_IMODE(target.stat().st_mode) == 0o644,
                "Operation receipt ownership/mode is not root controlled")
        for path in (target, *target.parents):
            result = subprocess.run(["/usr/sbin/runuser", "-u", account.pw_name, "--", "/usr/bin/test", "-w", str(path)],
                                    capture_output=True, timeout=5, cwd="/")
            require(result.returncode == 1, "Runtime identity can modify an operation status path")
        return {"admin_get_receipt_exact": True, "runtime_uid": account.pw_uid,
                "runtime_cannot_write_status_or_ancestors": True}

    def verify_enabled(self):
        for unit in (self.spec["api_unit"], self.edge_unit):
            require(command(["systemctl", "is-enabled", unit]).decode().strip() == "enabled", "Owned native unit was not enabled at boot")
        return {"api_and_managed_edge_enabled_at_boot": True, "machine_reboot_tested": False}

    def verify_database_grants(self):
        """Inspect actual stored grant scope and exercise the runtime account."""
        database = self.spec["database"]
        mode = self.resources.sql("SELECT @@GLOBAL.partial_revokes;")
        require(mode in {"0", "1"} and "_" in database, "Expected an explicit MySQL grant mode and underscore fixture")
        pattern = database.replace("_", "\\_") if mode == "0" else database
        roles = [database + "_" + role for role in ("runtime", "migrator", "backup")]
        rows = self.resources.sql("SELECT User,HEX(Db) FROM mysql.db WHERE Host='localhost' AND User IN (" +
            ",".join("'" + role + "'" for role in roles) + ") ORDER BY User;")
        expected = "\n".join(role + "\t" + pattern.encode().hex().upper() for role in sorted(roles))
        require(rows == expected, "Actual MySQL database grant pattern is broader than the fixture database")
        runtime_dsn = (self.config / "secrets/mysql_runtime_dsn").read_text().strip()
        match = re.fullmatch(re.escape(database + "_runtime") + r":([a-f0-9]{64})@unix\(" +
                             re.escape(str(self.resources.socket)) + r"\)/" + re.escape(database) + r"\?parseTime=true", runtime_dsn)
        require(match is not None, "Runtime DSN differs from the owned fixture role/database")
        config = self.private / "runtime-grant-probe.cnf"
        write(config, "[client]\nuser=" + database + "_runtime\npassword=" + match[1] + "\n")
        grants = self.resources.sql("SHOW GRANTS;", config).replace("\\\\", "\\")
        require(" ON `" + pattern + "`.* TO " in grants, "Actual runtime SHOW GRANTS differs from the reviewed database scope")
        observed = self.resources.sql("SELECT DATABASE(); SELECT JSON_UNQUOTE(JSON_EXTRACT(document,'$.default_currency')) FROM currency_settings WHERE id=1; SELECT COUNT(*) FROM plan_prices WHERE currency='CNY';", config, database).splitlines()
        require(len(observed) == 3 and observed[:2] == [database, "CNY"] and observed[2].isdigit() and int(observed[2]) > 0,
                "Actual runtime account could not read the intended fresh CNY database")
        return {"database_name_contains_underscore": True, "partial_revokes": mode == "1",
                "stored_database_grant_pattern": pattern, "all_three_role_scopes_verified": True,
                "runtime_show_grants_scope_verified": True, "runtime_actual_cny_database_read": True}

    def cli_install(self):
        """The published baseline also exercises the unmodified root CLI."""
        self.spec_path = self.save_spec()
        arguments = ["/usr/bin/python3", "-I", self.executor.__file__]
        stage(self.name + "-cli-plan")
        planned = json.loads(command([*arguments, "plan", "--spec", self.spec_path]))
        stage(self.name + "-cli-apply")
        return json.loads(command([*arguments, "apply", "--spec", self.spec_path,
                         "--confirm-plan-sha256", planned["plan_sha256"]], timeout=600))

    def confirm_current(self, directory):
        require(os.readlink(self.root / "current") == directory, "Executor selected an unexpected release")
        pid = int(command(["systemctl", "show", self.spec["api_unit"], "--property=MainPID", "--value"]).strip())
        require(pid > 1 and os.readlink("/proc/" + str(pid) + "/exe") == directory + "/bin/aster-api", "Live native process is not the selected executable")
        require(sha256(Path("/proc/" + str(pid) + "/exe").read_bytes()) == sha256(Path(directory, "bin/aster-api").read_bytes()), "Live process hash differs from selected Core")
        return {"api_pid": pid, "live_api_sha256": sha256(Path("/proc/" + str(pid) + "/exe").read_bytes())}

    def full_data_hash(self, database=None, tables=()):
        raw = command([self.resources.dump, "--defaults-file=" + str(self.resources.control / "dba.cnf"),
            "--no-login-paths", "--protocol=SOCKET", "--socket=" + str(self.resources.socket),
            "--single-transaction", "--no-tablespaces", "--set-gtid-purged=OFF", "--hex-blob", "--no-create-info",
            "--skip-comments", "--compact", "--skip-extended-insert", "--order-by-primary", "--skip-triggers",
            database or self.spec["database"], *tables])
        return sha256(raw)

    def legacy_business_hash(self, database, tables):
        # Migrations10/11 append users state and plan classification columns.
        # Compare every prior business byte using explicit original projections,
        # never SELECT * across different physical column inventories.
        require("users" in tables and "plans" in tables and len(tables) > 2, "Historical user/plan inventory is missing")
        other = self.full_data_hash(database, [table for table in tables if table not in {"users", "plans"}])
        users = self.resources.sql("SELECT HEX(id),HEX(email),HEX(name),HEX(role),HEX(password_hash),DATE_FORMAT(created_at,'%Y-%m-%d %H:%i:%s.%f') FROM users ORDER BY id;", database=database)
        plans = self.resources.sql("SELECT HEX(id),HEX(name),HEX(description),cpu,memory_gb,disk_gb,transfer_tb,HEX(region),price_minor,HEX(currency),available FROM plans ORDER BY BINARY id;", database=database)
        return sha256((other + "\n" + users + "\n" + plans).encode())

    def restore(self):
        with observe_restore_protected_open(self.executor, self):
            return self._restore()

    def _restore(self):
        """Actual old-release backup and restore into another empty dedicated DB."""
        stage("restore-source-data-hash")
        source_hash = self.full_data_hash()
        stage("restore-create-owned-target")
        target = "ar" + secrets.token_hex(5)
        account = target + "_restore"
        password = secrets.token_hex(24)
        self.resources.restores.append((target, account))
        self.resources.sql("CREATE DATABASE `" + target + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n" +
            "CREATE USER '" + account + "'@'localhost' IDENTIFIED BY '" + password + "';\n" +
            "GRANT ALL PRIVILEGES ON `" + target + "`.* TO '" + account + "'@'localhost';")
        require(self.resources.sql("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='" + target + "';") == "0", "Restore target was not empty")
        output = self.private / ("restore-backups-" + secrets.token_hex(4))
        self.executor.create_backup_directory(str(output), self.executor.updater._Trust())
        self.resources.restore_paths = {"private_parent": self.private, "backup_output": output,
            "backup_credentials": Path(self.spec["backup_client_config"])}
        native = self.executor.Native(self.spec)
        stage("restore-actual-published-backup-and-validate")
        backup = native.backup(self.baseline, str(output))
        archive = Path(backup["directory"], "database.sql.gz")
        import gzip
        stage("restore-write-private-import-credentials")
        restore_config = self.private / (target + ".cnf")
        write(restore_config, "[client]\nuser=" + account + "\npassword=" + password + "\n")
        self.resources.restore_paths["restore_credentials"] = restore_config
        stage("restore-import-into-empty-target")
        self.resources.sql(gzip.decompress(archive.read_bytes()).decode(), restore_config, target)
        stage("restore-compare-full-table-data-hash")
        restored_hash = self.full_data_hash(target)
        require(source_hash == restored_hash, "Restored data differs from stopped source")
        stage("restore-write-private-preflight-dsn")
        restored_dsn = self.private / (target + ".dsn")
        write(restored_dsn, account + ":" + password + "@unix(" + str(self.resources.socket) + ")/" + target + "?parseTime=true\n")
        self.resources.restore_paths["restore_dsn"] = restored_dsn
        stage("restore-read-runtime-environment")
        environment = self.executor.read_environment(str(self.config / "api.env"))[0]
        environment["MYSQL_DSN_FILE"] = str(restored_dsn)
        stage("restore-actual-published-preflight")
        restored_report = native.preflight(self.baseline, environment)
        require(restored_report["exit_code"] == 0 and restored_report["database"]["database"] == target and
                restored_report["database"]["status"] == "current" and restored_report["database"]["schema_version"] == 7 and
                restored_report["database"]["admin_count"] == 1,
                "Actual published old program did not validate the restored database")
        candidate_report = native.preflight(self.candidate, environment)
        require(candidate_report["exit_code"] == 0 and candidate_report["database"]["status"] == "pending_migrations" and
                candidate_report["database"]["schema_version"] == 7 and candidate_report["database"]["required_schema_version"] == CANDIDATE_SCHEMA,
                "Restored historical database is not the candidate's unchanged schema-7 prefix")
        return {"backup_source_core_version": PUBLISHED_VERSION, "source_database": self.spec["database"], "restore_database": target,
                "empty_target_verified": True, "full_table_data_sha256": source_hash, "restored_table_data_sha256": restored_hash,
                "actual_published_old_program_preflight_on_restored_database": "current",
                "restored_schema_version": 7, "candidate_preflight_on_restored_database": "pending_migrations",
                "same_mysql_instance_separate_empty_database": True, "independent_server_restore": False}


class HTTPSClient:
    """Real verified localhost HTTPS, cookie/CSRF login, no proxy or redirects."""
    def __init__(self, origin, ca):
        self.origin = origin
        self.csrf = None
        context = ssl.create_default_context(cafile=str(ca))
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, path, body=None):
        headers = {"Origin": self.origin}
        if body is not None:
            headers.update({"Content-Type": "application/json", "X-CSRF-Token": self.csrf or ""})
        request = urllib.request.Request(self.origin + path,
                    data=json.dumps(body).encode() if body is not None else None, headers=headers)
        try:
            with self.opener.open(request, timeout=5) as response:
                raw = response.read(1 << 20)
                return response.status, json.loads(raw) if "json" in response.headers.get("Content-Type", "") else raw
        except urllib.error.HTTPError as failure:
            return failure.code, failure.read(4096)

    def login(self, email, password):
        status, session = self.request("/api/v1/session")
        require(status == 200 and session["data"]["demo_mode"] is False, "Production HTTPS session failed")
        self.csrf = session["data"]["csrf_token"]
        status, _ = self.request("/api/v1/auth/login", {"email": email, "password": password})
        require(status == 200, "Bootstrap administrator HTTPS login failed")
        return self


def redacted_preflight_report(raw):
    """Project actual API output into fixed flags/counts, never arbitrary values."""
    result = {"json_valid": False}
    if len(raw) > 128 << 10:
        result["exceeded_json_bound"] = True
        return result
    try:
        value = json.loads(raw)
    except (ValueError, UnicodeError, RecursionError):
        return result
    result["json_valid"] = True
    result["object"] = isinstance(value, dict)
    if not isinstance(value, dict):
        return result
    for name in ("read_only", "configuration_checked"):
        result[name] = value.get(name) if type(value.get(name)) is bool else None
    result["schema_is_one"] = type(value.get("schema")) is int and value["schema"] == 1
    result["error_present"] = bool(value.get("error"))
    database = value.get("database")
    result["database_object"] = isinstance(database, dict)
    if isinstance(database, dict):
        state = database.get("status")
        result["database_status"] = state if isinstance(state, str) and state in {"empty", "pending_migrations", "current", "incompatible"} else "other_or_absent"
        for name in ("schema_version", "required_schema_version"):
            number = database.get(name)
            result[name] = number if type(number) is int and 0 <= number <= 64 else None
        result["database_error_present"] = bool(database.get("error"))
    return result


def legacy_native(executor, spec, candidate, trace):
    class ObservedLegacy(executor.Native):
        def api(self, release, environment, command_name, allow_failure=False):
            require(command_name in {"-preflight", "-migrate"}, "Unexpected legacy API command")
            entry = {"command": command_name, "release": "candidate" if release == candidate else "published-baseline",
                     "completed": False}
            trace["calls"] = (trace["calls"] + [entry])[-8:]
            try:
                result = super().api(release, environment, command_name, allow_failure=True)
                entry.update(completed=True, exit_code=result.returncode if -255 <= result.returncode <= 255 else None,
                             report=redacted_preflight_report(result.stdout))
                executor.require(allow_failure or result.returncode == 0, "A fixed native operation failed; raw diagnostics were suppressed.")
                return result
            except BaseException as failure:
                entry["rejection"] = SAFE_REJECTIONS.get(str(failure), "prepared_or_native_input_details_suppressed")
                raise
    return ObservedLegacy(spec)


def legacy_schema_migration(resources, executor, fresh, baseline):
    """Unchanged SQL 1..6, published 6 -> 7, then candidate 7 -> current.

    The published binary must reject the current schema. Historical restore is verified
    separately while the source and restored databases are still schema 7.
    """
    resources.legacy_migration_trace = {"calls": []}
    stage("legacy-create-owned-prefix-database")
    database = "al" + secrets.token_hex(5)
    account = database + "_migrate"
    password = secrets.token_hex(24)
    resources.restores.append((database, account))
    resources.sql("CREATE DATABASE `" + database + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n" +
        "CREATE USER '" + account + "'@'localhost' IDENTIFIED BY '" + password + "';\n" +
        "GRANT ALL PRIVILEGES ON `" + database + "`.* TO '" + account + "'@'localhost';")
    resources.sql("CREATE TABLE schema_migrations(version INT PRIMARY KEY, checksum CHAR(64) NOT NULL, applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB;", database=database)
    files = sorted(Path(fresh.candidate, "migrations").glob("*.sql"))
    require(len(files) == CANDIDATE_SCHEMA, "Legacy fixture requires the reviewed current migration inventory")
    for number, path in enumerate(files[:6], 1):
        stage("legacy-apply-prefix-" + str(number))
        if number == 3:
            # Match the actual migration runner's documented legacy USD check
            # removal. SQL resources remain byte-identical and fully hashed.
            resources.sql("ALTER TABLE orders DROP CHECK orders_chk_2; ALTER TABLE invoices DROP CHECK invoices_chk_2;", database=database)
        resources.sql(path.read_text(), database=database)
        if number == 1:
            resources.sql("INSERT INTO plans(id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,price_minor,currency,available) " +
                "SELECT id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,500,'USD',FALSE FROM `" + fresh.spec["database"] + "`.plans;", database=database)
        if number in (2, 6):
            table = "site_settings" if number == 2 else "email_settings"
            resources.sql("INSERT INTO " + table + "(id,document) SELECT id,document FROM `" + fresh.spec["database"] + "`." + table + ";", database=database)
        resources.sql("INSERT INTO schema_migrations(version,checksum) VALUES(" + str(number) + ",'" + sha256(path.read_bytes()) + "');", database=database)
    dsn = resources.control / "legacy-migrator-dsn"
    write(dsn, account + ":" + password + "@unix(" + str(resources.socket) + ")/" + database + "?parseTime=true\n")
    environment = executor.runtime_environment(fresh.spec)
    environment["MYSQL_DSN_FILE"] = str(dsn)
    native = legacy_native(executor, fresh.spec, fresh.candidate, resources.legacy_migration_trace)
    stage("legacy-preflight-prefix")
    before = native.preflight(fresh.candidate, environment)
    require(before["exit_code"] == 0 and before["database"]["status"] == "pending_migrations" and
            before["database"]["schema_version"] == 6, "Unchanged legacy prefix did not pass actual candidate preflight")
    count_before = resources.sql("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE();", database=database)
    usd_before = resources.sql("SELECT SUM(amount_minor) FROM plan_prices WHERE currency='USD';", database=database)
    stage("legacy-published-apply-registry-migration")
    native.api(baseline["directory"], environment, "-migrate")
    published = native.preflight(baseline["directory"], environment)
    pending = native.preflight(fresh.candidate, environment)
    require(published["exit_code"] == pending["exit_code"] == 0 and
            published["database"]["status"] == "current" and pending["database"]["status"] == "pending_migrations" and
            published["database"]["schema_version"] == pending["database"]["schema_version"] == 7,
            "Actual published registry migration did not leave the candidate's unchanged schema prefix")
    count_prefix7 = resources.sql("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE();", database=database)
    historical_tables = resources.sql("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME<>'schema_migrations' ORDER BY TABLE_NAME;", database=database).splitlines()
    require(len(historical_tables) == 13 and all(re.fullmatch(r"[a-z_]+", name) for name in historical_tables),
            "Historical schema table inventory is unexpected")
    historical_hash = fresh.legacy_business_hash(database, historical_tables)
    stage("legacy-candidate-apply-payment-migration")
    native.api(fresh.candidate, environment, "-migrate")
    stage("legacy-preflight-candidate")
    after = native.preflight(fresh.candidate, environment)
    stage("legacy-preflight-published")
    old = native.preflight(baseline["directory"], environment)
    require(after["exit_code"] == 0 and after["database"]["status"] == "current" and after["database"]["schema_version"] == CANDIDATE_SCHEMA and
            old["exit_code"] != 0 and old["database"]["status"] == "incompatible",
            "Payment schema migration must be current for the candidate and rejected by the published old program")
    count_after = resources.sql("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE();", database=database)
    require(count_before == "13" and count_prefix7 == "14" and count_after == "23", "Actual migrations did not add the currency, payment, catalog taxonomy and product configuration tables")
    require(fresh.legacy_business_hash(database, historical_tables) == historical_hash,
            "Additive payment migration changed existing business records")
    require(resources.sql("SELECT COUNT(*) FROM users WHERE disabled<>0 OR revision<>1;", database=database) == "0",
            "Account migration did not initialize historic users to enabled revision1")
    require(resources.sql("SELECT COUNT(*) FROM plans p LEFT JOIN catalog_regions r ON r.id=p.region_id LEFT JOIN catalog_types t ON t.id=p.type_id LEFT JOIN catalog_nodes n ON n.id=p.node_id WHERE p.type_id<>'generic' OR r.id IS NULL OR t.id IS NULL OR r.type_id<>p.type_id OR BINARY r.name<>BINARY p.region OR n.id IS NULL OR n.region_id<>p.region_id OR BINARY n.name<>BINARY '默认节点' OR n.id<>CONCAT('n-',LEFT(SHA2(BINARY p.region_id,256),60));", database=database) == "0",
            "Taxonomy migration did not preserve exact legacy regions, generic type and default-node relationships")
    require(resources.sql("SELECT (SELECT COUNT(*) FROM payment_attempts)+(SELECT COUNT(*) FROM payment_settlements);", database=database) == "0",
            "Migration invented a payment attempt or settlement")
    require(resources.sql("SELECT (SELECT COUNT(*) FROM product_configurations)+(SELECT COUNT(*) FROM order_product_configurations)+(SELECT COUNT(*) FROM provisioning_jobs);", database=database) == "0",
            "Migration invented product configuration, order configuration or provisioning work")
    require(resources.sql("SELECT SUM(amount_minor) FROM plan_prices WHERE currency='USD';", database=database) == usd_before,
            "Actual migration rewrote historical independent USD prices")
    digest_before = fresh.full_data_hash(database)
    stage("legacy-repeat-migration")
    native.api(fresh.candidate, environment, "-migrate")
    require(fresh.full_data_hash(database) == digest_before, "Repeated migration changed actual database data")
    return {"schema_before": 6, "published_schema": 7, "schema_after": CANDIDATE_SCHEMA, "tables_before": 13, "tables_published": 14, "tables_after": 23,
            "actual_candidate_binary_migrate": True, "unchanged_selected_sql_prefix": True,
            "actual_published_binary_migrate_6_to_7": True, "actual_candidate_binary_migrate_7_to_current": True,
            "audit_query_indexes_added": CANDIDATE_SCHEMA >= 9,
            "account_state_columns_added_with_enabled_revision1": CANDIDATE_SCHEMA >= 10,
            "taxonomy_tables_and_exact_legacy_relationships_added": CANDIDATE_SCHEMA >= 11,
            "published_baseline_version": PUBLISHED_VERSION, "published_baseline_preflight_before_payment_migration": "current",
            "published_baseline_preflight_after_payment_migration": "incompatible", "historical_usd_prices_preserved": True,
            "existing_business_table_data_sha256": historical_hash, "additive_payment_migration_preserved_existing_data": True,
            "new_payment_tables_empty": True, "new_product_configuration_tables_empty": True, "repeat_migration_data_unchanged": True,
            "scope": "separate-historical-prefix-and-additive-payment-migration"}


def late_schema_native(executor, fixture):
    """Change only this fixture after actual target activation, exactly once."""
    class LateSchema(executor.Native):
        def __init__(self, spec):
            super().__init__(spec)
            self.injected = False
            self.activation_was_done = False
            self.migration_calls = 0
            self.target_starts = 0
            self.data_before_fault = None

        def api(self, release, environment, command_name, allow_failure=False):
            if command_name == "-migrate" and release == fixture.candidate:
                self.migration_calls += 1
            return super().api(release, environment, command_name, allow_failure)

        def service(self, verb, unit=None):
            if verb == "start" and unit == self.spec["api_unit"] and os.readlink(fixture.root / "current") == fixture.candidate:
                self.target_starts += 1
            return super().service(verb, unit)

        def preflight(self, release, environment):
            if not self.injected and release == fixture.candidate and environment.get("MYSQL_DSN_FILE") == str(fixture.config / "secrets/mysql_runtime_dsn"):
                journal = fixture.checked_journal()
                require(journal["steps"].get("activate", {}).get("status") == "done" and
                        journal["database_migration"] == {"started": True, "completed": True} and
                        self.target_starts == 1, "Late schema fixture did not reach completed candidate activation")
                fixture.confirm_current(fixture.candidate)
                require(super().service("show", self.spec["api_unit"]) == "active", "Late schema fixture candidate was not active")
                self.activation_was_done = True
                self.data_before_fault = fixture.full_data_hash()
                fixture.resources.sql("ALTER TABLE plans MODIFY COLUMN cpu SMALLINT NOT NULL;", database=self.spec["database"])
                self.injected = True
            return super().preflight(release, environment)
    return LateSchema(fixture.spec)


def late_schema_recovery(args, resources, executor, candidate, baseline, overrides):
    stage("late-schema-official-baseline-install")
    late = Fixture(resources, executor, "late", candidate, baseline, overrides)
    token = executor.updater.load_protected_token(str(args.github_token_file), executor.updater._Trust())
    prepared = executor.manager.prepare(PUBLISHED_VERSION, late.baseline, api=executor.updater.Official(token))
    require(prepared["status"] == "prepared", "Late-schema actual official baseline preparation failed")
    verify_published_baseline(late.baseline)
    late.spec["release_directory"] = late.baseline
    late.plan()
    require(late.execute()["status"] == "succeeded", "Late-schema fixture baseline install failed")
    late.upgrade()
    fault = late_schema_native(executor, late)
    for resume in (False, True):
        stage("late-schema-failed-resume" if resume else "late-schema-fault-after-activation")
        try:
            late.execute(native=fault, resume=resume)
            raise AcceptanceFailure("Late incompatible physical schema unexpectedly passed recovery")
        except executor.Rejected:
            pass
        hold = late.status()
        journal = late.checked_journal()
        activation = journal["steps"].get("activate", {})
        require(fault.injected and fault.activation_was_done and fault.migration_calls == 1 and fault.target_starts == 1,
                "Late-schema held retry repeated migration or target startup")
        require(hold["status"] == "needs_manual_recovery" and hold["maintenance"] is True and
                hold["program_rollback"] == "blocked_schema_or_readiness" and
                hold["service_action"] == "stopped" and hold["https_ready"] is False and
                hold["active_release_changed"] is True and
                hold["database_migration"] == {"started": True, "completed": True},
                "Late incompatible schema did not retain completed migration under maintenance")
        require(activation.get("status") == "started" and activation.get("result", {}).get("recovery_stopped") is True,
                "Late-schema hold lacks the explicit stopped-activation recovery checkpoint")
        require(os.readlink(late.root / "current") == late.candidate and
                executor.Native(late.spec).service("show", late.spec["api_unit"]) in {"inactive", "failed"} and
                fault.https("/api/v1/session")[0] == 503,
                "Late-schema hold did not preserve target selection with stopped writes and HTTPS maintenance")
        require(resources.sql("SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='plans' AND COLUMN_NAME='cpu';",
                              database=late.spec["database"]) == "smallint" and late.full_data_hash() == fault.data_before_fault,
                "Late-schema failure changed fixture data or blindly restored database state")
        environment = executor.read_environment(str(late.config / "api.env"))[0]
        environment["MYSQL_DSN_FILE"] = late.spec["migrator_dsn_file"]
        old = fault.preflight(late.baseline, environment)
        require(old["exit_code"] != 0 and old["database"]["status"] == "incompatible",
                "Actual published old program accepted the late incompatible schema")
    fact(args.evidence, "late-schema-hold", dict(hold,
         fixture_fault="real-required-cpu-column-changed-to-smallint-after-completed-activation",
         activation_was_done_at_injection=True, stopped_activation_recovery_checkpoint=True,
         failed_resume_did_not_restart_target=True, migration_command_count=1, target_start_attempts=1,
         actual_published_old_schema_guard="incompatible", retained_database_data_unchanged=True,
         failed_resume_retained_incompatible_column_type=True, full_table_data_sha256=fault.data_before_fault,
         automatic_database_restore=False))
    stage("late-schema-reviewed-manual-repair")
    resources.sql("ALTER TABLE plans MODIFY COLUMN cpu INT NOT NULL;", database=late.spec["database"])
    repaired = late.execute(native=fault, resume=True)
    require(repaired["status"] == "succeeded" and repaired["program_rollback"] == "none" and
            repaired["operation_id"] == hold["operation_id"] and
            fault.migration_calls == 1 and fault.target_starts == 2,
            "Late-schema manual repair did not reactivate the target without repeating migration")
    require(late.status() == repaired, "Late-schema repaired journal failed independent status validation")
    require(late.full_data_hash() == fault.data_before_fault, "Late-schema repaired recovery changed fixture data")
    live = late.confirm_current(late.candidate)
    receipt = late.verify_status_api(repaired)
    require(late.execute(native=fault, resume=True) == repaired and fault.migration_calls == 1 and fault.target_starts == 2 and
            late.confirm_current(late.candidate)["api_pid"] == live["api_pid"],
            "Late-schema repaired terminal retry changed the receipt or restarted the target")
    return dict(repaired, **live, **receipt, activation_was_done_at_injection=True,
                stopped_activation_recovery_checkpoint=True, migration_command_count=1,
                target_start_attempts=2, independently_validated_journal=True, terminal_resume_idempotent=True,
                repaired_database_data_unchanged=True, full_table_data_sha256=fault.data_before_fault,
                manual_fixture_repair="required-cpu-column-restored-to-int", automatic_database_restore=False)


def run_scenarios(args, candidate, baseline):
    executor = module("core_acceptance_executor", "core-operation.py")
    executor.Native = diagnostic_native(executor)
    original_prepared = executor.prepared_release
    overrides = {}

    def prepared(root, trust=executor.updater._Trust()):
        if root not in overrides:
            return original_prepared(root, trust)
        proof = overrides[root]
        # Recheck actual protected bytes on every executor preflight/activation.
        # This internal descriptor is CI trust, never an official receipt file.
        actual = verify_source_candidate(args.source, args.candidate_assets, root)
        require(actual["version"] == proof["version"] and actual["source_commit"] == proof["source_commit"], "Candidate identity changed")
        files = {str(path.relative_to(root)): sha256(path.read_bytes()) for path in Path(root).rglob("*") if path.is_file()}
        migrations = {str(int(Path(name).name[:3])): value for name, value in files.items() if name.startswith("migrations/")}
        return {"version": proof["version"], "source_sha": proof["source_commit"],
                "receipt_sha256": sha256(("CI selected-source candidate\n" + proof["source_commit"]).encode()),
                "files_sha256": files, "migration_checksums": migrations}

    executor.prepared_release = prepared
    resources = Resources.__new__(Resources)
    complete = False
    try:
        stage("owned-native-controls")
        resources.__init__(args)
        fact(args.evidence, "native-tools", {name: sha256(path.read_bytes()) for name, path in
             (("mysql", resources.mysql), ("mysqldump", resources.dump), ("nginx", resources.nginx))})
        stage("fresh-install")
        fresh = Fixture(resources, executor, "fresh", candidate, baseline, overrides)
        fresh.plan()
        result = fresh.execute()
        require(result["status"] == "succeeded" and result["https_ready"] is True, "Fresh actual executor install failed")
        stage("fresh-verify-live-and-cny")
        live = fresh.confirm_current(fresh.candidate)
        status, config = fresh.login().request("/api/v1/admin/currencies")
        require(status == 200 and config["data"]["default_currency"] == "CNY" and
                len(config["data"]["currencies"]) == 1 and config["data"]["currencies"][0]["code"] == "CNY",
                "Fresh executor install is not CNY-only")
        require(resources.sql("SELECT COUNT(*) FROM users WHERE role='admin';", database=fresh.spec["database"]) == "1", "First administrator was duplicated")
        require(fresh.execute(resume=True) == result, "Successful install resume changed its result")
        require(fresh.confirm_current(fresh.candidate)["api_pid"] == live["api_pid"], "Idempotent install resume restarted the API")
        stage("fresh-status-permissions")
        receipt = fresh.verify_status_api(result)
        enabled = fresh.verify_enabled()
        stage("fresh-database-grants")
        grants = fresh.verify_database_grants()
        fact(args.evidence, "fresh-install", dict(result, **live, **receipt, **enabled, **grants, production_https_login=True, fresh_cny_only=True,
             first_administrator_count=1, terminal_resume_idempotent=True, candidate_trust=candidate["trust"]))
        stage("legacy-schema-migration")
        fact(args.evidence, "legacy-schema-migration", legacy_schema_migration(resources, executor, fresh, baseline))

        stage("published-baseline-cli-install-and-restore")
        upgrade = Fixture(resources, executor, "upgrade", candidate, baseline, overrides)
        require(upgrade.baseline == baseline["directory"], "Published baseline must have its actual prepare target")
        upgrade.spec["release_directory"] = upgrade.baseline
        old_install = upgrade.cli_install()
        require(old_install["status"] == "succeeded", "Actual published Core v" + PUBLISHED_VERSION + " install failed")
        old_live = upgrade.confirm_current(upgrade.baseline)
        upgrade.verify_enabled()
        upgrade.login()
        upgrade.upgrade()
        executor.Native(upgrade.spec).service("stop", upgrade.spec["api_unit"])
        restore = upgrade.restore()
        executor.Native(upgrade.spec).service("start", upgrade.spec["api_unit"])
        wait_ready(executor.Native(upgrade.spec))
        fact(args.evidence, "backup-restore", restore)

        class FailAfterBackup(executor.Native):
            def __init__(self, spec):
                super().__init__(spec)
                self.backup_completed = False
                self.migration_calls = 0

            def backup(self, old_release, output):
                super().backup(old_release, output)
                self.backup_completed = True
                raise executor.Rejected("Synthetic failure after completed backup and before migration")

            def api(self, release, environment, command_name, allow_failure=False):
                if command_name == "-migrate":
                    self.migration_calls += 1
                return super().api(release, environment, command_name, allow_failure)

        stage("compatible-pre-migration-program-rollback")
        pre_migration_hash = upgrade.full_data_hash()
        early_fault = FailAfterBackup(upgrade.spec)
        try:
            upgrade.execute(native=early_fault)
            raise AcceptanceFailure("Pre-migration failure unexpectedly succeeded")
        except executor.Rejected:
            pass
        rollback = upgrade.status()
        old_recovered = upgrade.confirm_current(upgrade.baseline)
        old_environment = executor.read_environment(str(upgrade.config / "api.env"))[0]
        old_schema = early_fault.preflight(upgrade.baseline, old_environment)
        require(early_fault.backup_completed and early_fault.migration_calls == 0 and
                rollback["status"] == "rolled_back" and rollback["program_rollback"] == "verified_old_schema" and
                rollback["database_migration"] == {"started": False, "completed": False} and
                rollback["maintenance"] is True and early_fault.https("/api/v1/session")[0] == 503 and
                old_schema["exit_code"] == 0 and old_schema["database"]["status"] == "current" and old_schema["database"]["schema_version"] == 7 and
                upgrade.full_data_hash() == pre_migration_hash,
                "Compatible rollback must recover the published program only before payment schema migration")
        require(upgrade.execute(resume=True) == rollback, "Terminal compatible rollback retry changed state")
        fact(args.evidence, "compatible-rollback", dict(rollback, **old_recovered,
             old_program_was_actual_published_baseline=True, published_baseline_version=PUBLISHED_VERSION,
             fault="after-real-backup-before-database-migration", migration_command_started=False,
             schema_before=7, schema_after=7, new_schema_migration_added=False,
             existing_database_data_unchanged=True, automatic_database_restore=False,
             maintenance_until_explicit_validation=True))
        # Only the independent schema-7 rollback was verified. Start a new
        # reviewed upgrade after explicitly clearing this fixture's maintenance.
        (upgrade.config / "maintenance").unlink()
        upgrade.login()
        upgrade.upgrade()

        class PauseAfterStop(executor.Native):
            def service(self, verb, unit=None):
                value = super().service(verb, unit)
                if verb == "stop" and unit == self.spec["api_unit"]:
                    # The real stop completed. SIGKILL below bypasses Python
                    # exception recovery and leaves a genuine durable checkpoint.
                    os.kill(os.getpid(), signal.SIGSTOP)
                return value

        stage("interrupted-checkpoint")
        child = os.fork()
        if child == 0:
            try:
                upgrade.execute(native=PauseAfterStop(upgrade.spec))
            except BaseException:
                os._exit(70)
            os._exit(0)
        stopped = False
        try:
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                observed, child_status = os.waitpid(child, os.WNOHANG | os.WUNTRACED)
                if observed:
                    require(os.WIFSTOPPED(child_status), "Interrupt fixture child exited before the real service stop")
                    stopped = True
                    break
                time.sleep(0.1)
            require(stopped, "Interrupt fixture did not reach its bounded checkpoint")
            interrupted = upgrade.status()
            require(interrupted["maintenance"] is True and interrupted["database_migration"]["started"] is False,
                    "Interruption happened after database migration began")
            require(executor.Native(upgrade.spec).service("show", upgrade.spec["api_unit"]) in {"inactive", "failed"}, "Interrupted API was not stopped")
        finally:
            with contextlib.suppress(ProcessLookupError):
                os.kill(child, signal.SIGKILL)
            with contextlib.suppress(ChildProcessError):
                os.waitpid(child, 0)

        class OccupiedAPIPort(executor.Native):
            def __init__(self, spec):
                super().__init__(spec)
                self.blocker = None
                self.injected = False
                self.migration_calls = 0
                self.target_starts = 0
            def api(self, release, environment, command_name, allow_failure=False):
                if release == upgrade.candidate and command_name == "-migrate":
                    self.migration_calls += 1
                return super().api(release, environment, command_name, allow_failure)
            def service(self, verb, unit=None):
                if verb == "stop" and self.blocker is not None:
                    self.blocker.close()
                    self.blocker = None
                if verb == "start" and unit == self.spec["api_unit"] and not self.injected and os.readlink(upgrade.root / "current") == upgrade.candidate:
                    self.blocker = socket.socket()
                    self.blocker.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                    self.blocker.bind(("127.0.0.1", int(self.spec["listen_address"].split(":")[1])))
                    self.blocker.listen(1)
                    self.injected = True
                if verb == "start" and unit == self.spec["api_unit"] and os.readlink(upgrade.root / "current") == upgrade.candidate:
                    self.target_starts += 1
                value = super().service(verb, unit)
                if verb == "stop" and unit == self.spec["api_unit"] and self.injected:
                    # The intentionally crashing fixture may have exhausted its
                    # restart burst. Reset only this owned unit's failure count
                    # after the port condition is gone, before reviewed resume.
                    command(["systemctl", "reset-failed", unit])
                if verb == "start" and unit == self.spec["api_unit"] and self.injected and self.blocker is None:
                    wait_ready(self)
                return value

        stage("resume-and-occupied-port-schema-hold")
        fault = OccupiedAPIPort(upgrade.spec)
        try:
            upgrade.execute(native=fault, resume=True)
            raise AcceptanceFailure("Real occupied-port candidate startup unexpectedly succeeded")
        except executor.Rejected:
            pass
        finally:
            if fault.blocker is not None:
                fault.blocker.close()
        hold = upgrade.status()
        require(fault.injected and fault.migration_calls == 1 and fault.target_starts == 1 and
                hold["status"] == "needs_manual_recovery" and hold["program_rollback"] == "blocked_schema_or_readiness" and
                hold["database_migration"] == {"started": True, "completed": True} and
                hold["maintenance"] is True and hold["https_ready"] is False and
                os.readlink(upgrade.root / "current") == upgrade.candidate and
                executor.Native(upgrade.spec).service("show", upgrade.spec["api_unit"]) in {"inactive", "failed"} and
                fault.https("/api/v1/session")[0] == 503,
                "Completed payment migration must hold stopped writes instead of restarting an incompatible old program")
        held_environment = executor.read_environment(str(upgrade.config / "api.env"))[0]
        held_environment["MYSQL_DSN_FILE"] = upgrade.spec["migrator_dsn_file"]
        candidate_schema = fault.preflight(upgrade.candidate, held_environment)
        rejected_old = fault.preflight(upgrade.baseline, held_environment)
        require(candidate_schema["exit_code"] == 0 and candidate_schema["database"]["status"] == "current" and
                candidate_schema["database"]["schema_version"] == CANDIDATE_SCHEMA and
                rejected_old["exit_code"] != 0 and rejected_old["database"]["status"] == "incompatible",
                "Occupied-port recovery did not independently verify the current-schema rollback boundary")
        held_data_hash = upgrade.full_data_hash()
        fact(args.evidence, "interrupt-resume", {"process_killed_after_real_service_stop": True,
             "before_database_migration": interrupted["database_migration"], "original_operation_id_reused": True,
             "resume_reached_migration_and_candidate_start": True, "fault": "actual-bound-api-port"})
        fact(args.evidence, "payment-schema-rollback-blocked", dict(hold,
             fault="actual-bound-api-port-after-payment-migration", schema_before=7, schema_after=CANDIDATE_SCHEMA,
             new_schema_migration_added=True, migration_command_count=1, target_start_attempts=1,
             actual_published_old_schema_guard="incompatible", target_schema="current",
             automatic_database_restore=False, api_stopped=True, https_maintenance=True))
        # The synthetic port fault is now gone. Resume the original operation
        # and preserved current-schema data; never downgrade tables or open writes by
        # deleting the maintenance marker before candidate checks succeed.
        stage("reviewed-port-recovery-and-version-upgrade")
        upgraded = upgrade.execute(native=fault, resume=True)
        require(upgraded["status"] == "succeeded", "Published Core v" + PUBLISHED_VERSION + " to candidate upgrade failed")
        require(upgraded["operation_id"] == hold["operation_id"] and fault.migration_calls == 1 and fault.target_starts == 2 and
                upgrade.full_data_hash() == held_data_hash,
                "Reviewed current-schema recovery repeated migration or changed existing database data")
        live = upgrade.confirm_current(upgrade.candidate)
        upgrade.login()
        require(upgrade.execute(resume=True) == upgraded and upgrade.confirm_current(upgrade.candidate)["api_pid"] == live["api_pid"], "Successful upgrade retry repeated activation")
        receipt = upgrade.verify_status_api(upgraded)
        fact(args.evidence, "published-upgrade", dict(upgraded, **live, **receipt, previous_live_api_sha256=old_live["live_api_sha256"],
             actual_published_previous_version=PUBLISHED_VERSION, candidate_trust=candidate["trust"], https_login=True,
             previous_install_used_unmodified_root_cli=True,
             terminal_resume_idempotent=True, no_fabricated_official_metadata=True))

        stage("unknown-schema-hold-and-manual-recovery")
        unknown = Fixture(resources, executor, "unknown", candidate, baseline, overrides)
        token = executor.updater.load_protected_token(str(args.github_token_file), executor.updater._Trust())
        actual = executor.manager.prepare(PUBLISHED_VERSION, unknown.baseline, api=executor.updater.Official(token))
        require(actual["status"] == "prepared", "Second actual official baseline preparation failed")
        verify_published_baseline(unknown.baseline)
        unknown.spec["release_directory"] = unknown.baseline
        unknown.plan()
        require(unknown.execute()["status"] == "succeeded", "Unknown-schema fixture baseline install failed")
        unknown.upgrade()

        class UnknownSchema(executor.Native):
            def __init__(self, spec):
                super().__init__(spec)
                self.migration_calls = 0
            def api(self, release, environment, command_name, allow_failure=False):
                value = super().api(release, environment, command_name, allow_failure)
                if command_name == "-migrate" and release == unknown.candidate:
                    self.migration_calls += 1
                    resources.sql("ALTER TABLE plans MODIFY COLUMN cpu SMALLINT NOT NULL;", database=self.spec["database"])
                return value

        schema_fault = UnknownSchema(unknown.spec)
        for resume in (False, True):
            try:
                unknown.execute(native=schema_fault, resume=resume)
                raise AcceptanceFailure("Unknown physical schema unexpectedly passed executor recovery")
            except executor.Rejected:
                pass
            hold = unknown.status()
            require(hold["status"] == "needs_manual_recovery" and hold["maintenance"] is True and
                    hold["program_rollback"] == "blocked_schema_or_readiness", "Unknown schema did not hold for manual recovery")
            require(executor.Native(unknown.spec).service("show", unknown.spec["api_unit"]) in {"inactive", "failed"}, "Uncertain migration left API writes running")
            require(resources.sql("SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='plans' AND COLUMN_NAME='cpu';", database=unknown.spec["database"]) == "smallint", "Executor blindly restored or removed uncertain database state")
        require(schema_fault.migration_calls == 1, "Uncertain migration command was repeated blindly")
        fact(args.evidence, "unknown-schema-hold", dict(hold, fixture_fault="real-required-cpu-column-changed-to-smallint-after-migrate",
             failed_resume_retained_incompatible_column_type=True, migration_command_count=1, automatic_database_restore=False,
             incompatible_released_schema_claimed=False))
        # Synthetic canary removal is the explicitly reviewed manual repair.
        resources.sql("ALTER TABLE plans MODIFY COLUMN cpu INT NOT NULL;", database=unknown.spec["database"])
        repaired = unknown.execute(resume=True)
        require(repaired["status"] == "succeeded" and repaired["program_rollback"] == "none",
                "Manual fixture schema repair did not produce a consistent successful receipt")
        require(unknown.status() == repaired, "Repaired operation journal failed independent status validation")
        repaired_live = unknown.confirm_current(unknown.candidate)
        repaired_api = unknown.verify_status_api(repaired)
        require(unknown.execute(resume=True) == repaired and
                unknown.confirm_current(unknown.candidate)["api_pid"] == repaired_live["api_pid"],
                "Repaired terminal resume repeated activation or changed its receipt")
        fact(args.evidence, "unknown-schema-recovered", dict(repaired, **repaired_live, **repaired_api,
             independently_validated_journal=True, terminal_resume_idempotent=True,
             manual_fixture_repair="required-cpu-column-restored-to-int", automatic_database_restore=False))
        fact(args.evidence, "late-schema-recovered", late_schema_recovery(args, resources, executor, candidate, baseline, overrides))
        fact(args.evidence, "core-native", {"status": "passed", "candidate_source_commit": candidate["source_commit"],
             "candidate_version": candidate["version"], "published_baseline": PUBLISHED_VERSION, "actual_native_executor": True,
             "fresh_install": True, "published_to_candidate_upgrade": True, "interrupt_resume": True,
             "compatible_program_rollback_before_migration": True, "unknown_schema_manual_recovery": True,
             "payment_schema_upgrade_7_to_8": True, "incompatible_old_program_rollback_blocked": True,
             "manual_recovery_receipt_and_terminal_resume_verified": True,
             "late_readiness_schema_hold_and_reactivation_verified": True,
             "separate_actual_legacy_schema_6_to_8_migration": True,
             "separate_empty_database_restore": True, "historical_restore_schema_version": 7, "new_release_schema_delta": True,
             "production_deployment": False, "real_mail_or_payment": False})
        complete = True
    except BaseException as failure:
        # Export before cleanup removes the private checkpoint. Never expose the
        # journal, command stderr, provider replies, SQL or arbitrary exceptions.
        fact(args.evidence, "failure", failure_diagnostics(resources, failure))
        raise
    finally:
        try:
            if hasattr(resources, "fixtures"):
                resources.cleanup()
            fact(args.evidence, "cleanup", {"owned_resources_removed": True, "test_completed": complete})
        except BaseException:
            fact(args.evidence, "cleanup", {"owned_resources_removed": False, "test_completed": complete})
            raise


def wait_ready(native):
    for attempt in range(50):
        try:
            if native.https("/readyz")[0] == 200:
                return
        except Exception as failure:
            if type(failure).__name__ != "Rejected":
                raise
        time.sleep(0.1)
    raise AcceptanceFailure("Actual HTTPS service did not become ready")


def parser():
    result = argparse.ArgumentParser(description=__doc__)
    for name in ("source", "evidence", "fixture-root", "candidate-assets", "candidate-release", "baseline-release",
                 "nginx", "mysql-root", "mysql-socket", "mysql-admin", "mysql-fixture", "github-token-file"):
        result.add_argument("--" + name, type=Path, required=True)
    return result


def main():
    args = parser().parse_args()
    require(os.geteuid() == 0 and os.environ.get("GITHUB_ACTIONS") == "true" and
            os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted" and
            os.environ.get("GITHUB_REPOSITORY") == "xiaoqiqixi/betteridcsys", "Hosted native CI guard required")
    require(re.fullmatch(r"/opt/aster-ci-[0-9]+-[0-9]+-[0-9]+", str(args.fixture_root)), "Unexpected parent fixture root")
    candidate = verify_source_candidate(args.source, args.candidate_assets, args.candidate_release)
    baseline = verify_published_baseline(args.baseline_release)
    fact(args.evidence, "artifact-trust", {"candidate": candidate, "baseline": baseline})
    run_scenarios(args, candidate, baseline)


if __name__ == "__main__":
    try:
        main()
    except Exception as failure:
        # Raw errors may contain a database URL, token, password or SQL text.
        detail = (": " + str(failure)) if isinstance(failure, AcceptanceFailure) else ""
        print("FAIL Core native executor acceptance: " + STAGE + ": " + type(failure).__name__ + detail, file=sys.stderr)
        raise SystemExit(1)
