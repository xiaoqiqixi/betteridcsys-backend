#!/usr/bin/env python3
"""Preserved resource-slot regression suite, isolated from the Core installer."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch
HERE = Path(__file__).resolve().parent
VPS = HERE.parents[4]
def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module
fixtures = load("resource_fixtures", VPS / "deploy/scripts/test-plugin-manager.py")
manager = load("resource_manager", HERE / "plugin-manager.py")
fixtures.manager = manager
contents = fixtures.contents
class ResourceManagerTest(unittest.TestCase):
    # Reuse fixtures; the complete existing hostile package suite runs above.
    setUp = fixtures.ManagerTest.setUp
    tearDown = fixtures.ManagerTest.tearDown
    package = fixtures.ManagerTest.package
    verify = fixtures.ManagerTest.verify
    install = fixtures.ManagerTest.install
    selection = fixtures.ManagerTest.selection
    mutate = fixtures.ManagerTest.mutate
    def install_resource(self, version="0.1.0"):
        files = contents(version)
        manifest = json.loads(files["manifest.json"])
        manifest.update(id="resource.aster-cloud", protocol="aster.resource/1", core_min="0.5.10", capabilities=["resource.send", "resource.lookup"])
        files["manifest.json"] = json.dumps(manifest).encode()
        package = self.verify(files=files, version=version, root="aster-resource-aster-cloud-" + version + "-linux-amd64")
        manager.install_package(str(self.root), package, self.trust)
        return package

    def resource_selection(self, version="0.1.0"):
        return {"id": "resource.aster-cloud", "version": version, "worker_uid": 2300, "client_uid": 0,
                "socket": "/run/aster-resource/plugin.sock", "allowed_targets": [{"host": "cloud.example.test", "port": 443}], "controller_id": "primary"}

    def test_resource_install_approve_rollback_preserves_other_slots(self):
        self.install()
        self.mutate("select", 0, selection=self.selection())
        self.mutate("enable", 1)
        original = manager.read_policy(str(self.policy), self.trust)["smtp"]
        self.install_resource()
        with patch.object(subprocess, "run", side_effect=AssertionError("package execution forbidden")):
            selected = self.mutate("select", 2, selection=self.resource_selection(), kind="resource")
            self.assertFalse(selected["resource"]["enabled"])
            enabled = self.mutate("enable", 3, kind="resource")
        self.assertEqual(original, enabled["smtp"])
        self.assertEqual(enabled["resource"]["client_uid"], 0)
        with self.assertRaises(manager.Rejected):
            self.mutate("remove", 4, kind="resource")
        self.install_resource("0.2.0")
        self.mutate("select", 4, selection=self.resource_selection("0.2.0"), kind="resource")
        rolled = self.mutate("rollback", 5, version="0.1.0", kind="resource")
        self.assertEqual(rolled["resource"]["version"], "0.1.0")
        self.assertFalse(rolled["resource"]["enabled"])
        self.assertEqual(rolled["smtp"], original)
        self.mutate("enable", 6, kind="resource")
        disabled = self.mutate("disable", 7, kind="resource")
        self.assertFalse(disabled["resource"]["enabled"])
        removed = self.mutate("remove", 8, kind="resource")
        self.assertNotIn("resource", removed)
        self.assertEqual(original, removed["smtp"])
        self.assertTrue((self.root / "resource.aster-cloud/0.1.0/plugin").is_file())
        with self.assertRaises(manager.Rejected):
            self.mutate("remove", 9, kind="smtp")

    def test_resource_root_client_controller_and_isolation_required(self):
        self.install_resource()
        for change in ({"client_uid": 2301}, {"worker_uid": 0}, {"controller_id": "../bad"}, {"merchant_id": "not-a-payment"}):
            with self.subTest(change=change), self.assertRaises(manager.Rejected):
                self.mutate("select", 0, selection=self.resource_selection() | change, kind="resource")
        self.assertFalse(self.policy.exists())
        self.install()
        self.mutate("select", 0, selection=self.selection())
        for change in ({"worker_uid": 2001}, {"worker_uid": 2002}, {"socket": "/run/aster-smtp/plugin.sock"}):
            with self.subTest(change=change), self.assertRaises(manager.Rejected):
                self.mutate("select", 1, selection=self.resource_selection() | change, kind="resource")
        self.assertNotIn("resource", manager.read_policy(str(self.policy), self.trust))

    def test_resource_capability_and_legacy_core_rejected(self):
        package = self.install_resource()
        for change in ({"capabilities": ["resource.send", "resource.lookup", "resource.exec"]}, {"protocol": "aster.payment/1"}, {"core_min": "0.5.8"}, {"core_min": "0.5.9"}):
            with self.subTest(change=change), self.assertRaises(manager.Rejected):
                manager.validate_manifest(package.manifest | change, manager.CORE_VERSION)
        for core in ("0.5.8", "0.5.9"):
            with self.subTest(core=core), self.assertRaises(manager.Rejected):
                manager.validate_manifest(package.manifest, core)

    def test_resource_first_release_rejects_old_core_and_old_manifest_minimum(self):
        manifest = json.loads(contents(version="0.1.1")["manifest.json"])
        manifest.update(id="resource.aster-cloud", protocol="aster.resource/1", core_min="0.5.10",
                        capabilities=["resource.send", "resource.lookup"])
        manager.validate_manifest(manifest, "0.5.10")
        for core in ("0.5.8", "0.5.9", "0.7.0"):
            with self.subTest(core=core), self.assertRaises(manager.Rejected):
                manager.validate_manifest(manifest, core)
        for minimum in ("0.4.0", "0.5.9"):
            with self.subTest(minimum=minimum), self.assertRaises(manager.Rejected):
                manager.validate_manifest(manifest | {"core_min": minimum}, "0.5.10")


    def test_resource_minimum_is_pinned(self):
        self.assertEqual(manager.RESOURCE_MIN_CORE_VERSION, "0.5.10")

if __name__ == "__main__":
    unittest.main(verbosity=2)
