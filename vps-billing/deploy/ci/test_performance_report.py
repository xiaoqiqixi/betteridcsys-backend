#!/usr/bin/env python3
"""Synthetic validator tests; no Go, database, network, or load is executed."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("performance-report.py").resolve()
SPEC = importlib.util.spec_from_file_location("performance_report", SCRIPT)
REPORT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(REPORT)
SOURCE = "a" * 40
BASELINE = "b" * 40
HARNESS = "c" * 64
DATASET = "d" * 64
CANARY = "secretcanary-password@unix(/private/customer.sock)/customer_data"
PATHS = (
    "/api/v1/session", "/api/v1/orders", "/api/v1/overview?currency=CNY",
    "/api/v1/admin/overview?currency=CNY",
    "/api/v1/admin/orders",
)


def measurement(path, concurrency, repeat):
    return {
        "path": path, "concurrency": concurrency, "repeat": repeat,
        "requests": 100, "duration_ns": 3_000_000_000, "qps": 100 / 3,
        "latency_ms": {"p50": 1.25, "p95": 2.5, "p99": 4.75},
        "transport_errors": 0, "http_errors": 0, "error_rate": 0.0,
        "http_status_counts": {"200": 100},
        "api": {"cpu_seconds": 0.25, "rss_peak_bytes": 64 << 20,
                "heap_peak_bytes": 16 << 20,
                "pool": {"wait_count": 1, "wait_duration_ns": 10_000_000,
                         "in_use_peak": 4, "open_connections_peak": 5}},
        "mysql_lock_delta": {"Innodb_row_lock_waits": 0, "Innodb_row_lock_time": 0,
                             "Table_locks_waited": 0},
    }


def epoch(label, source, binary):
    return {
        "label": label, "source_sha": source, "binary_sha256": binary,
        "dataset_sha256": DATASET, "go_version": "go1.27.2", "gomaxprocs": 4,
        "measurements": [measurement(path, concurrency, repeat)
                         for path in PATHS for concurrency in (1, 8, 32)
                         for repeat in (1, 2)],
    }


def fixture(baseline=False):
    plan_tables = {
        "session_lookup": "sessions", "user_lookup": "users",
        "currency_configuration": "currency_settings", "customer_orders": "orders", "admin_orders": "orders",
        "customer_recent_orders": "orders",
        "customer_services_summary": "services", "customer_invoices_summary": "invoices",
        "admin_services_summary": "services", "admin_invoices_summary": "invoices",
        "admin_customer_count": "users",
    }
    candidate = epoch("candidate", SOURCE, "e" * 64)
    return {
        "schema": "aster.http-performance/2", "source_sha": SOURCE,
        "harness_sha256": HARNESS, "seed": "aster-http-performance-v1",
        "parameters": {
            "users": 100, "orders": 10000, "services": 1000,
            "concurrency": [1, 8, 32], "repeats": 2, "warmup_ns": 1_000_000_000,
            "duration_ns": 3_000_000_000, "sampling_ns": 25_000_000,
            "http_timeouts_ns": {"read_header": 5_000_000_000, "read": 15_000_000_000,
                                 "write": 20_000_000_000, "idle": 60_000_000_000,
                                 "request": 12_000_000_000},
        },
        "machine": {
            "os": "linux", "arch": "amd64", "cpu_model": "Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz",
            "logical_cpus": "4", "mem_total": "16333412 kB", "cgroup_cpu_max": "max 100000",
            "cgroup_memory_max": "max", "cgroup_cpuset": "0-3",
            "loadgen_go_version": "go1.27.2", "loadgen_gomaxprocs": "4",
        },
        "mysql_version": "8.4.10",
        "dataset": {
            "counts": {"audit_events": 10000, "catalog_nodes": 2, "catalog_regions": 2,
                       "catalog_taxonomy_settings": 1, "catalog_types": 1,
                       "currency_settings": 1, "email_settings": 1,
                       "invoices": 10000, "order_requests": 10000, "orders": 10000,
                       "outbox_events": 10000, "payment_attempts": 0, "payment_settlements": 0,
                       "plan_prices": 3, "plans": 3, "schema_migrations": 13, "services": 1000,
                       "product_configurations": 0, "order_product_configurations": 0, "provisioning_jobs": 0,
                       "sessions": 2, "site_settings": 1, "users": 101},
            "sha256": DATASET, "before_after_equal": True,
        },
        "epochs": ([epoch("baseline_before", BASELINE, "f" * 64), candidate,
                    epoch("baseline_after", BASELINE, "f" * 64)] if baseline else [candidate]),
        "explain": [{"name": name, "sql": REPORT.EXPLAIN_SQL[name],
                     "plan": {"query_cost": 2.75, "tables": [{
                         "table_name": table, "access_type": "ref", "key": "PRIMARY",
                         "rows_examined_per_scan": 100, "rows_produced_per_join": 100,
                         "filtered": 100.0, "using_index": False,
                     }]}} for name, table in plan_tables.items()],
        "cleanup": {"database_dropped": True, "workers_stopped": True},
    }


class PerformanceReportTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="aster-performance-report-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.input = self.root / "private-input.json"
        self.output = self.root / "new-report.json"

    def write(self, value=None, raw=None):
        if raw is None:
            raw = json.dumps(fixture() if value is None else value, separators=(",", ":")).encode()
        self.input.write_bytes(raw)
        self.input.chmod(0o600)

    def command(self, baseline=False, extra=()):
        args = [sys.executable, "-I", str(SCRIPT), "--input", str(self.input), "--output", str(self.output),
                "--source-sha", SOURCE, "--harness-sha256", HARNESS]
        if baseline:
            args.extend(("--baseline-sha", BASELINE))
        return args + list(extra)

    def run_cli(self, baseline=False, extra=()):
        return subprocess.run(self.command(baseline, extra), capture_output=True, text=True,
                              timeout=5, env=dict(os.environ, MYSQL_DSN=CANARY, GITHUB_TOKEN=CANARY))

    def assert_rejected(self, result, code=None):
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertEqual(result.stdout, "")
        self.assertRegex(result.stderr, r"^PERFORMANCE_REPORT_ERROR=[A-Z]+\n$")
        if code is not None:
            self.assertEqual(result.stderr, "PERFORMANCE_REPORT_ERROR=" + code + "\n")
        self.assertNotIn("secretcanary", result.stdout + result.stderr)
        self.assertNotIn("customer.sock", result.stdout + result.stderr)
        self.assertNotIn("Traceback", result.stdout + result.stderr)
        self.assertFalse(self.output.exists())

    def test_candidate_is_canonical_private_and_bounded_on_stdout(self):
        value = fixture()
        self.write(value)
        result = self.run_cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        lines = result.stdout.splitlines()
        self.assertEqual(len(lines), 2)
        self.assertEqual(lines[1], "PERFORMANCE_REPORT_EPOCH index=0 measurements=30 requests=3000 errors=0")
        self.assertTrue(lines[0].startswith("PERFORMANCE_REPORT_JSON "))
        public = json.loads(lines[0].removeprefix("PERFORMANCE_REPORT_JSON "))
        self.assertEqual(public, value)
        self.assertEqual(public["epochs"][0]["measurements"][0]["qps"], 100 / 3)
        self.assertEqual(public["epochs"][0]["measurements"][0]["latency_ms"]["p95"], 2.5)
        self.assertEqual(public["machine"], value["machine"])
        self.assertEqual(public["explain"], value["explain"])
        self.assertEqual(json.loads(self.output.read_text()), value)
        self.assertEqual(stat.S_IMODE(self.output.stat().st_mode), 0o600)
        self.assertEqual(self.output.stat().st_uid, os.geteuid())
        canonical = json.dumps(value, sort_keys=True, ensure_ascii=True, allow_nan=False,
                               separators=(",", ":")) + "\n"
        self.assertEqual(self.output.read_text(), canonical)
        self.assertLess(len(result.stdout.encode()), 512 * 1024)
        self.assertNotIn("secretcanary", result.stdout)

    def test_baseline_candidate_baseline_complete_matrix(self):
        value = fixture(baseline=True)
        self.write(value)
        result = self.run_cli(baseline=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(result.stdout.splitlines()), 4)
        self.assertTrue(all(re.fullmatch(r"PERFORMANCE_REPORT_EPOCH index=[012] measurements=30 requests=3000 errors=0", line)
                            for line in result.stdout.splitlines()[1:]))
        self.assertEqual(json.loads(result.stdout.splitlines()[0].removeprefix("PERFORMANCE_REPORT_JSON ")), value)
        self.assertEqual(json.loads(self.output.read_text()), value)

    def test_v2_requires_admin_order_measurements_in_every_epoch(self):
        value = fixture(baseline=True)
        self.assertEqual([len(item["measurements"]) for item in value["epochs"]], [30, 30, 30])
        for index in range(3):
            with self.subTest(epoch=index):
                changed = copy.deepcopy(value)
                changed["epochs"][index]["measurements"] = [
                    item for item in changed["epochs"][index]["measurements"]
                    if item["path"] != "/api/v1/admin/orders"
                ]
                self.write(changed)
                self.assert_rejected(self.run_cli(baseline=True), "EPOCHS")
        value["schema"] = "aster.http-performance/1"
        self.write(value)
        self.assert_rejected(self.run_cli(baseline=True), "SCHEMA")

    def test_valid_error_counts_remain_visible(self):
        value = fixture()
        item = value["epochs"][0]["measurements"][0]
        item.update(transport_errors=2, http_errors=2, error_rate=0.04,
                    http_status_counts={"200": 96, "503": 2})
        self.write(value)
        result = self.run_cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("errors=4\n", result.stdout)

    def test_schema_13_inventory_requires_exact_empty_product_configuration_tables(self):
        value = fixture()
        expected = {"catalog_nodes": 2, "catalog_types": 1, "catalog_regions": 2,
                    "catalog_taxonomy_settings": 1, "schema_migrations": 13,
                    "product_configurations": 0, "order_product_configurations": 0, "provisioning_jobs": 0}
        self.assertEqual({key: value["dataset"]["counts"][key] for key in expected}, expected)
        self.assertEqual(value["dataset"]["counts"], REPORT.COUNTS)
        self.assertEqual(len(value["dataset"]["counts"]), 23)

        legacy = copy.deepcopy(value)
        for key in ("catalog_nodes", "catalog_types", "catalog_regions", "catalog_taxonomy_settings",
                    "product_configurations", "order_product_configurations", "provisioning_jobs"):
            legacy["dataset"]["counts"].pop(key)
        legacy["dataset"]["counts"]["schema_migrations"] = 10
        self.write(legacy)
        self.assert_rejected(self.run_cli(), "DATASET")

        schema12 = copy.deepcopy(value)
        for key in ("product_configurations", "order_product_configurations", "provisioning_jobs"):
            schema12["dataset"]["counts"].pop(key)
        schema12["dataset"]["counts"]["schema_migrations"] = 12
        self.write(schema12)
        self.assert_rejected(self.run_cli(), "DATASET")

        for key, wanted in expected.items():
            for wrong in (None, wanted + 1, True):
                with self.subTest(table=key, wrong=wrong):
                    changed = copy.deepcopy(value)
                    if wrong is None:
                        changed["dataset"]["counts"].pop(key)
                    else:
                        changed["dataset"]["counts"][key] = wrong
                    self.write(changed)
                    self.assert_rejected(self.run_cli(), "DATASET")

    def test_unavailable_optional_machine_metadata_and_optimized_away_plan(self):
        value = fixture()
        value["machine"]["cpu_model"] = "AMD EPYC 7763 64-Core Processor"
        for name in ("mem_total", "cgroup_cpu_max", "cgroup_memory_max", "cgroup_cpuset"):
            value["machine"][name] = "unavailable"
        value["explain"][0]["plan"]["tables"] = []
        self.write(value)
        result = self.run_cli()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_schema_identity_profile_dataset_cleanup_and_matrix_fail_closed(self):
        mutations = {
            "unknown top field": lambda v: v.update(private_payload=CANARY),
            "schema": lambda v: v.update(schema=CANARY),
            "source mismatch": lambda v: v.update(source_sha="b" * 40),
            "harness mismatch": lambda v: v.update(harness_sha256="b" * 64),
            "seed mismatch": lambda v: v.update(seed=CANARY),
            "smaller dataset profile": lambda v: v["parameters"].update(orders=10),
            "warmup": lambda v: v["parameters"].update(warmup_ns=0),
            "duration": lambda v: v["parameters"].update(duration_ns=1),
            "sampling": lambda v: v["parameters"].update(sampling_ns=1),
            "concurrency": lambda v: v["parameters"].update(concurrency=[1, 8]),
            "repeats": lambda v: v["parameters"].update(repeats=True),
            "timeout": lambda v: v["parameters"]["http_timeouts_ns"].update(request=1),
            "wrong table count": lambda v: v["dataset"]["counts"].update(users=100),
            "missing table": lambda v: v["dataset"]["counts"].pop("sessions"),
            "unknown table": lambda v: v["dataset"]["counts"].update(secretcanary=1),
            "mutation observed": lambda v: v["dataset"].update(before_after_equal=False),
            "epoch wrong dataset": lambda v: v["epochs"][0].update(dataset_sha256="f" * 64),
            "epoch wrong source": lambda v: v["epochs"][0].update(source_sha="f" * 40),
            "epoch wrong binary": lambda v: v["epochs"][0].update(binary_sha256=CANARY),
            "epoch missing measurement": lambda v: v["epochs"][0]["measurements"].pop(),
            "epoch duplicate measurement": lambda v: v["epochs"][0]["measurements"].__setitem__(1, copy.deepcopy(v["epochs"][0]["measurements"][0])),
            "epoch wrong label": lambda v: v["epochs"][0].update(label=CANARY),
            "epoch extra field": lambda v: v["epochs"][0].update(payload=CANARY),
            "different compiler": lambda v: v["epochs"][0].update(go_version="go1.26.1"),
            "different gomaxprocs": lambda v: v["epochs"][0].update(gomaxprocs=1),
            "database not dropped": lambda v: v["cleanup"].update(database_dropped=False),
            "workers not stopped": lambda v: v["cleanup"].update(workers_stopped=False),
            "cleanup omitted": lambda v: v.pop("cleanup"),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                value = fixture()
                mutate(value)
                self.write(value)
                self.assert_rejected(self.run_cli())

    def test_measurement_numbers_types_and_accounting_fail_closed(self):
        mutations = {
            "unknown path": lambda m: m.update(path="/private/" + CANARY),
            "unknown metric": lambda m: m.update(payload=CANARY),
            "zero requests": lambda m: m.update(requests=0),
            "bool requests": lambda m: m.update(requests=True),
            "negative requests": lambda m: m.update(requests=-1),
            "fractional repeat": lambda m: m.update(repeat=1.5),
            "qps mismatch": lambda m: m.update(qps=0),
            "short duration": lambda m: m.update(duration_ns=1),
            "quantile order": lambda m: m["latency_ms"].update(p50=9),
            "negative quantile": lambda m: m["latency_ms"].update(p50=-1),
            "unbounded quantile": lambda m: m["latency_ms"].update(p99=1e300),
            "fake status field": lambda m: m["http_status_counts"].update(secretcanary=1),
            "status above 599": lambda m: m.update(http_status_counts={"600": 100}),
            "status count sum": lambda m: m.update(http_status_counts={"200": 99}),
            "wrong HTTP errors": lambda m: m.update(http_status_counts={"503": 100}),
            "double count transport": lambda m: m.update(transport_errors=1),
            "error fraction mismatch": lambda m: m.update(error_rate=0.5),
            "negative CPU": lambda m: m["api"].update(cpu_seconds=-1),
            "unsafe RSS string": lambda m: m["api"].update(rss_peak_bytes=CANARY),
            "unknown API field": lambda m: m["api"].update(dsn=CANARY),
            "pool above core cap": lambda m: m["api"]["pool"].update(open_connections_peak=21),
            "pool in use above open": lambda m: m["api"]["pool"].update(in_use_peak=6),
            "negative lock delta": lambda m: m["mysql_lock_delta"].update(Innodb_row_lock_waits=-1),
            "unknown lock key": lambda m: m["mysql_lock_delta"].update(secretcanary=0),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                value = fixture()
                mutate(value["epochs"][0]["measurements"][0])
                self.write(value)
                self.assert_rejected(self.run_cli())

    def test_explain_accepts_only_reviewed_templates_and_safe_plan_fields(self):
        mutations = {
            "SQL literal": lambda v: v[0].update(sql="SELECT '" + CANARY + "'"),
            "unknown SQL name": lambda v: v[0].update(name=CANARY),
            "duplicate plan": lambda v: v.__setitem__(1, copy.deepcopy(v[0])),
            "missing plan": lambda v: v.pop(),
            "raw plan condition": lambda v: v[0]["plan"].update(attached_condition=CANARY),
            "raw plan message": lambda v: v[0]["plan"].update(message=CANARY),
            "unknown table": lambda v: v[0]["plan"]["tables"][0].update(table_name=CANARY),
            "unknown index": lambda v: v[0]["plan"]["tables"][0].update(key=CANARY),
            "unknown access": lambda v: v[0]["plan"]["tables"][0].update(access_type=CANARY),
            "null index flag": lambda v: v[0]["plan"]["tables"][0].update(using_index=None),
            "invalid filter": lambda v: v[0]["plan"]["tables"][0].update(filtered=101),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                value = fixture()
                mutate(value["explain"])
                self.write(value)
                self.assert_rejected(self.run_cli())

    def test_recent_orders_exact_limit_five_template_retains_legacy_order_query(self):
        columns = "id,number,user_id,plan_name,status,total_minor,currency,created_at,hostname,issuer_snapshot,currency_snapshot,plan_snapshot"
        legacy = "SELECT " + columns + " FROM orders WHERE user_id=? ORDER BY created_at DESC LIMIT 1000"
        recent = "SELECT " + columns + " FROM orders WHERE user_id=? ORDER BY created_at DESC,id DESC LIMIT 5"
        self.assertEqual(REPORT.EXPLAIN_SQL["customer_orders"], legacy)
        self.assertEqual(REPORT.EXPLAIN_SQL["customer_recent_orders"], recent)
        self.assertEqual(REPORT.EXPLAIN_TABLES["customer_orders"], "orders")
        self.assertEqual(REPORT.EXPLAIN_TABLES["customer_recent_orders"], "orders")
        self.write(fixture())
        result = self.run_cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        public = json.loads(result.stdout.splitlines()[0].removeprefix("PERFORMANCE_REPORT_JSON "))
        plans = {item["name"]: item for item in public["explain"]}
        self.assertEqual(len(plans), 11)
        self.assertEqual(plans["customer_orders"]["sql"], legacy)
        self.assertEqual(plans["customer_recent_orders"]["sql"], recent)

    def test_admin_order_plan_has_global_scope_and_original_limit(self):
        columns = "id,number,user_id,plan_name,status,total_minor,currency,created_at,hostname,issuer_snapshot,currency_snapshot,plan_snapshot"
        expected = "SELECT " + columns + " FROM orders ORDER BY created_at DESC LIMIT 1000"
        self.assertEqual(REPORT.EXPLAIN_SQL["admin_orders"], expected)
        self.assertEqual(REPORT.EXPLAIN_TABLES["admin_orders"], "orders")
        value = fixture()
        value["explain"] = [item for item in value["explain"] if item["name"] != "admin_orders"]
        self.write(value)
        self.assert_rejected(self.run_cli(), "EXPLAIN")

    def test_order_explain_templates_reject_changed_limit_or_tie_break_order(self):
        mutations = (
            ("customer_orders", lambda sql: sql.replace("LIMIT 1000", "LIMIT 5")),
            ("customer_orders", lambda sql: sql.replace("created_at DESC", "created_at DESC,id DESC")),
            ("customer_recent_orders", lambda sql: sql.replace("LIMIT 5", "LIMIT 1000")),
            ("customer_recent_orders", lambda sql: sql.replace("DESC,id DESC", "DESC")),
            ("customer_recent_orders", lambda sql: sql.replace("id DESC LIMIT", "id ASC LIMIT")),
            ("customer_recent_orders", lambda sql: sql.replace("user_id=?", "user_id='" + CANARY + "'")),
            ("admin_orders", lambda sql: sql.replace("FROM orders ORDER", "FROM orders WHERE user_id=? ORDER")),
            ("admin_orders", lambda sql: sql.replace("LIMIT 1000", "LIMIT 1001")),
            ("admin_orders", lambda sql: sql.replace("created_at DESC", "created_at ASC")),
        )
        for index, (name, mutate) in enumerate(mutations):
            with self.subTest(name=name, mutation=index):
                value = fixture()
                plan = next(item for item in value["explain"] if item["name"] == name)
                plan["sql"] = mutate(plan["sql"])
                self.write(value)
                self.assert_rejected(self.run_cli(), "EXPLAIN")

    def test_machine_strings_are_bounded_reviewed_formats(self):
        for key, invalid in {
            "os": "windows", "arch": "arm64", "cpu_model": CANARY,
            "logical_cpus": "-1", "mem_total": CANARY, "cgroup_cpu_max": CANARY,
            "cgroup_memory_max": "../" + CANARY, "cgroup_cpuset": "3-0",
            "loadgen_go_version": CANARY, "loadgen_gomaxprocs": "0",
        }.items():
            with self.subTest(key=key):
                value = fixture()
                value["machine"][key] = invalid
                self.write(value)
                self.assert_rejected(self.run_cli(), "MACHINE")

    def test_baseline_identity_order_and_binary_are_pinned(self):
        for name, mutate in {
            "wrong baseline source": lambda v: v["epochs"][0].update(source_sha=SOURCE),
            "changed baseline binary": lambda v: v["epochs"][2].update(binary_sha256="1" * 64),
            "missing bracket epoch": lambda v: v["epochs"].pop(),
            "reordered bracket": lambda v: v["epochs"].reverse(),
        }.items():
            with self.subTest(name=name):
                value = fixture(baseline=True)
                mutate(value)
                self.write(value)
                self.assert_rejected(self.run_cli(baseline=True))
        self.write(fixture(baseline=True))
        self.assert_rejected(self.run_cli())
        self.write(fixture())
        self.assert_rejected(self.run_cli(baseline=True))

    def test_duplicate_keys_nonfinite_unicode_and_deep_json_are_rejected(self):
        valid = json.dumps(fixture(), separators=(",", ":")).encode()
        payloads = {
            "duplicate top": b'{"schema":"' + CANARY.encode() + b'",' + valid[1:],
            "duplicate nested": valid.replace(b'"os":"linux"', b'"os":"linux","os":"' + CANARY.encode() + b'"'),
            "NaN": valid.replace(b'"qps":33.333333333333336', b'"qps":NaN'),
            "Infinity": valid.replace(b'"qps":33.333333333333336', b'"qps":Infinity'),
            "overflow exponent": valid.replace(b'"qps":33.333333333333336', b'"qps":1e999'),
            "non UTF8": b'\xff' + valid,
            "trailing object": valid + b'{}',
            "deep nesting": b'[' * 20 + b'0' + b']' * 20,
            "unpaired surrogate": valid.replace(b'"os":"linux"', b'"os":"\\ud800"'),
            "escaped control": valid.replace(b'"os":"linux"', b'"os":"lin\\nux"'),
        }
        for name, raw in payloads.items():
            with self.subTest(name=name):
                self.assertNotEqual(raw, valid)
                self.write(raw=raw)
                self.assert_rejected(self.run_cli(), "JSON")

    def test_input_must_be_private_owned_single_link_regular_and_bounded(self):
        for name in ("public", "symlink", "hardlink", "directory", "fifo", "oversize", "empty"):
            with self.subTest(name=name):
                if os.path.lexists(self.input):
                    if self.input.is_dir():
                        self.input.rmdir()
                    else:
                        self.input.unlink()
                target = self.root / (name + "-target")
                if name == "public":
                    self.write()
                    self.input.chmod(0o644)
                elif name in ("symlink", "hardlink"):
                    target.write_text(json.dumps(fixture()))
                    target.chmod(0o600)
                    os.symlink(target, self.input) if name == "symlink" else os.link(target, self.input)
                elif name == "directory":
                    self.input.mkdir(mode=0o700)
                elif name == "fifo":
                    os.mkfifo(self.input, 0o600)
                else:
                    self.write(raw=b"" if name == "empty" else b" " * (2 * 1024 * 1024 + 1))
                self.assert_rejected(self.run_cli(), "INPUT")

    def test_owner_mismatch_and_input_mutation_fail_closed(self):
        self.write()
        with mock.patch.object(REPORT.os, "geteuid", return_value=os.geteuid() + 1):
            with self.assertRaises(REPORT.Rejected) as rejected:
                REPORT.read_input(self.input)
            self.assertEqual(rejected.exception.code, "INPUT")
        actual_stat = REPORT.os.fstat
        calls = 0
        def changed_metadata(descriptor):
            nonlocal calls
            calls += 1
            info = actual_stat(descriptor)
            if calls == 2:
                attributes = {name: getattr(info, name) for name in (
                    "st_dev", "st_ino", "st_uid", "st_gid", "st_mode", "st_nlink",
                    "st_size", "st_mtime_ns", "st_ctime_ns")}
                attributes["st_ctime_ns"] += 1
                return type("ChangedStat", (), attributes)()
            return info
        with mock.patch.object(REPORT.os, "fstat", side_effect=changed_metadata):
            with self.assertRaises(REPORT.Rejected) as rejected:
                REPORT.read_input(self.input)
            self.assertEqual(rejected.exception.code, "INPUT")

    def test_output_is_exclusive_and_never_changes_existing_file_or_symlink(self):
        self.write()
        original = self.root / "original.json"
        original.write_text(CANARY)
        for symlink in (False, True):
            with self.subTest(symlink=symlink):
                if os.path.lexists(self.output):
                    self.output.unlink()
                os.symlink(original, self.output) if symlink else self.output.write_text(CANARY)
                result = self.run_cli()
                self.assertEqual(result.returncode, 1)
                self.assertEqual(result.stderr, "PERFORMANCE_REPORT_ERROR=OUTPUT\n")
                self.assertEqual(result.stdout, "")
                self.assertNotIn("secretcanary", result.stderr)
                self.assertEqual(original.read_text(), CANARY)
                self.assertEqual(self.output.read_text(), CANARY)

    def test_output_parent_must_be_private_and_not_a_symlink(self):
        self.write()
        directory = self.root / "public-parent"
        directory.mkdir(mode=0o755)
        directory.chmod(0o755)
        self.output = directory / "report.json"
        self.assert_rejected(self.run_cli(), "OUTPUT")
        private = self.root / "private-parent"
        private.mkdir(mode=0o700)
        link = self.root / "linked-parent"
        os.symlink(private, link)
        self.output = link / "report.json"
        self.assert_rejected(self.run_cli(), "OUTPUT")

    def test_partial_output_is_removed_on_write_failure(self):
        with mock.patch.object(REPORT.os, "write", side_effect=OSError(CANARY)):
            with self.assertRaises(REPORT.Rejected) as rejected:
                REPORT.write_output(self.output, fixture())
            self.assertEqual(rejected.exception.code, "OUTPUT")
        self.assertFalse(self.output.exists())

    def test_argument_errors_never_echo_input_or_payload(self):
        self.write()
        self.assert_rejected(self.run_cli(extra=("--unknown", CANARY)), "ARGUMENTS")
        self.assert_rejected(self.run_cli(extra=("--source-sha", CANARY)), "ARGUMENTS")
        result = subprocess.run([sys.executable, "-I", str(SCRIPT), "--input", CANARY],
                                capture_output=True, text=True, timeout=5)
        self.assert_rejected(result, "ARGUMENTS")


if __name__ == "__main__":
    unittest.main()
