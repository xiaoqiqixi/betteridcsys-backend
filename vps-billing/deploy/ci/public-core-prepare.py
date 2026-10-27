#!/usr/bin/env python3
"""Verify public Core 0.5.10 assets and real preparation; never install/execute.

The optional read-only metadata token enters on stdin, never an argv or artifact.
Asset downloads use a separate credential-free production Official client.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import signal
import sys
import tempfile

VERSION = "0.5.10"
HERE = Path(__file__).resolve().parent


def load_controls():
    # Reviewed main controls, never code from downloaded release resources.
    spec = importlib.util.spec_from_file_location("aster_public_core_controls", HERE.parent / "scripts/core-operation.py")
    controls = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(controls)
    return controls


def verify(controls, work, source_sha, token):
    """Real production verifier/preparer, with no trust or platform override."""
    manager = controls.manager
    updater = manager.updater
    updater.require(isinstance(source_sha, str) and re.fullmatch(r"[0-9a-f]{40}", source_sha), "SOURCE_SHA_REQUIRED")
    metadata, public = updater.Official(token), updater.Official()
    descriptor = manager.release_descriptor(metadata, VERSION)
    updater.require(descriptor["source_sha"] == source_sha, "RELEASE_SOURCE_MISMATCH")
    inventory = updater.asset_inventory(descriptor["assets"], "core", VERSION, "raw-binaries-v1")
    assets = work / "assets"
    assets.mkdir(mode=0o700)
    for name, item in sorted(inventory.items()):
        raw = public.asset(item["id"], min(updater.asset_limit(name), item["size"]))
        updater.require(len(raw) == item["size"] and hashlib.sha256(raw).hexdigest() == item["sha256"], "PUBLIC_ASSET_BYTES_REJECTED")
        with (assets / name).open("xb") as stream:
            stream.write(raw)
    target = work / "release"
    result = manager.prepare(VERSION, str(target), assets_directory=str(assets), api=metadata)
    updater.require(result["status"] == "prepared" and result["version"] == VERSION and result["source_sha"] == source_sha and
                    result["source_and_integrity_verified"] is True and result["active_release_changed"] is False and
                    result["service_action"] == result["database_action"] == "none" and result["program_executed"] is False,
                    "REAL_PREPARE_RESULT_REJECTED")
    prepared = controls.prepared_release(str(target))
    receipt_raw = updater.read_protected(str(target / manager.RECEIPT), updater.MAX_JSON)
    receipt = updater.strict_json(receipt_raw)
    updater.require(receipt["receipt_kind"] == "local_operator_preparation_receipt" and receipt["status"] == "prepared" and
                    receipt["source_sha"] == source_sha and receipt["version"] == VERSION and
                    receipt["release_id"] == descriptor["release_id"] and receipt["official_release_inventory"] is False and
                    updater.asset_inventory(receipt["assets"], "core", VERSION, "raw-binaries-v1") == inventory and
                    updater.positive(receipt["run_id"]) and updater.positive(receipt["run_attempt"]) and
                    prepared["receipt_sha256"] == hashlib.sha256(receipt_raw).hexdigest() and
                    prepared["version"] == VERSION and prepared["source_sha"] == source_sha, "LOCAL_RECEIPT_REJECTED")
    updater.require(updater.release_assets(metadata, descriptor) == inventory, "PUBLIC_RELEASE_CHANGED")
    updater.require(set(prepared["migration_checksums"]) == {str(index) for index in range(1, 14)}, "MIGRATION_INVENTORY_REJECTED")
    return {"schema": 1, "passed": True, "version": VERSION, "source_sha": source_sha,
            "release_id": descriptor["release_id"], "build_run_id": receipt["run_id"], "build_attempt": receipt["run_attempt"],
            "assets": [{"name": name, "size": item["size"], "sha256": item["sha256"]} for name, item in sorted(inventory.items())],
            "anonymous_asset_download": True, "production_prepare": True, "source_and_integrity_verified": True,
            "local_receipt_verified": True, "prepared_file_count": len(prepared["files_sha256"]),
            "migration_files": 13, "program_executed": False,
            "service_action": "none", "database_action": "none", "host_install": False,
            "independent_publisher_signature": False,
            "scope": "Public assets, source verification and fresh protected preparation only; not installation, activation or production acceptance."}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-sha", required=True)
    args = parser.parse_args(argv)
    try:
        if platform.system() != "Linux" or platform.machine() not in {"x86_64", "amd64"} or os.geteuid() != 0:
            raise ValueError("unsupported platform")
        release = dict(line.split("=", 1) for line in Path("/etc/os-release").read_text().splitlines() if "=" in line)
        ubuntu = release.get("VERSION_ID", "").strip('"')
        if release.get("ID", "").strip('"') != "ubuntu" or ubuntu not in {"22.04", "24.04"}:
            raise ValueError("unsupported release")
        controls = load_controls()
        manager = controls.manager
        token_bytes = sys.stdin.buffer.read(manager.updater.MAX_GITHUB_TOKEN + 1)
        token = token_bytes.decode("ascii")
        if len(token_bytes) > manager.updater.MAX_GITHUB_TOKEN or token and not manager.updater.valid_github_token(token):
            raise ValueError("invalid metadata token")
        os.umask(0o077)
        with tempfile.TemporaryDirectory(prefix="aster-public-core.", dir="/run") as temporary:
            result = verify(controls, Path(temporary), args.source_sha, token)
        if Path(temporary).exists():
            raise ValueError("cleanup incomplete")
        print(json.dumps({**result, "ubuntu": ubuntu, "owned_resources_removed": True}, sort_keys=True))
        return 0
    except Exception:
        print(json.dumps({"schema": 1, "passed": False, "code": "PUBLIC_CORE_ACCEPTANCE_FAILED"}, sort_keys=True))
        return 1


if __name__ == "__main__":
    def interrupted(_number, _frame):
        raise RuntimeError("interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    sys.exit(main())
