#!/usr/bin/env python3
"""Validate and publish bounded, synthetic native HTTP performance evidence.

No process, database, network, or benchmark is started by this helper. The
reviewed profile is intentionally fixed; changing it requires a source change.
"""
import argparse
import json
import math
import os
from pathlib import Path
import re
import stat
import sys


MAX_INPUT_BYTES = 2 * 1024 * 1024
MAX_STDOUT_BYTES = 512 * 1024
SCHEMA = "aster.http-performance/2"
SEED = "aster-http-performance-v1"
CONCURRENCY = [1, 8, 32]
PATHS = (
    "/api/v1/session",
    "/api/v1/orders",
    "/api/v1/overview?currency=CNY",
    "/api/v1/admin/overview?currency=CNY",
    "/api/v1/admin/orders",
)
PARAMETERS = {
    "users": 100, "orders": 10000, "services": 1000,
    "concurrency": CONCURRENCY, "repeats": 2,
    "warmup_ns": 1_000_000_000, "duration_ns": 3_000_000_000,
    "sampling_ns": 25_000_000,
    "http_timeouts_ns": {
        "read_header": 5_000_000_000, "read": 15_000_000_000,
        "write": 20_000_000_000, "idle": 60_000_000_000,
        "request": 12_000_000_000,
    },
}
COUNTS = {
    "audit_events": 10000, "catalog_nodes": 2, "catalog_regions": 2, "catalog_taxonomy_settings": 1,
    "catalog_types": 1, "currency_settings": 1, "email_settings": 1,
    "invoices": 10000, "order_requests": 10000, "orders": 10000,
    "outbox_events": 10000, "payment_attempts": 0, "payment_settlements": 0,
    "plan_prices": 3, "plans": 3, "schema_migrations": 13,
    "product_configurations": 0, "order_product_configurations": 0, "provisioning_jobs": 0,
    "services": 1000, "sessions": 2, "site_settings": 1, "users": 101,
}
LOCK_KEYS = {"Innodb_row_lock_waits", "Innodb_row_lock_time", "Table_locks_waited"}
MACHINE_KEYS = {
    "os", "arch", "cpu_model", "logical_cpus", "mem_total", "cgroup_cpu_max",
    "cgroup_memory_max", "cgroup_cpuset", "loadgen_go_version", "loadgen_gomaxprocs",
}
TOP_KEYS = {"schema", "source_sha", "harness_sha256", "seed", "parameters",
            "machine", "mysql_version", "dataset", "epochs", "explain", "cleanup"}
MEASUREMENT_KEYS = {
    "path", "concurrency", "repeat", "requests", "duration_ns", "qps",
    "latency_ms", "transport_errors", "http_errors", "error_rate",
    "http_status_counts", "api", "mysql_lock_delta",
}
ACCESS_TYPES = {"system", "const", "eq_ref", "ref", "fulltext", "ref_or_null",
                "index_merge", "unique_subquery", "index_subquery", "range", "index", "ALL"}
INDEXES = {
    "sessions": {"", "PRIMARY", "ix_session_expiry", "user_id"},
    "users": {"", "PRIMARY", "email"},
    "currency_settings": {"", "PRIMARY"},
    "orders": {"", "PRIMARY", "number", "ix_orders_user_date", "plan_id"},
    "services": {"", "PRIMARY", "ix_services_user"},
    "invoices": {"", "PRIMARY", "number", "order_id", "ix_invoices_user_date"},
}
SERVICE_SQL = "SELECT COUNT(*),COALESCE(SUM(status='active'),0),COALESCE(SUM(CASE WHEN status='active' THEN price_minor ELSE 0 END),0) FROM services WHERE currency=?"
INVOICE_SQL = "SELECT COALESCE(SUM(status='unpaid'),0),COALESCE(SUM(CASE WHEN status='paid' THEN amount_minor ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='unpaid' THEN amount_minor ELSE 0 END),0) FROM invoices WHERE currency=?"
# These exact templates are public harness code, never interpolated SQL from a
# server, filename, session hash, customer, or provider payload.
EXPLAIN_SQL = {
    "session_lookup": "SELECT token_hash,user_id,expires_at FROM sessions WHERE token_hash=? AND expires_at>UTC_TIMESTAMP(6)",
    "user_lookup": "SELECT id,email,name,role,password_hash FROM users WHERE id=?",
    "currency_configuration": "SELECT document,prices_revision FROM currency_settings WHERE id=1",
    "customer_orders": "SELECT id,number,user_id,plan_name,status,total_minor,currency,created_at,hostname,issuer_snapshot,currency_snapshot,plan_snapshot FROM orders WHERE user_id=? ORDER BY created_at DESC LIMIT 1000",
    "admin_orders": "SELECT id,number,user_id,plan_name,status,total_minor,currency,created_at,hostname,issuer_snapshot,currency_snapshot,plan_snapshot FROM orders ORDER BY created_at DESC LIMIT 1000",
    "customer_recent_orders": "SELECT id,number,user_id,plan_name,status,total_minor,currency,created_at,hostname,issuer_snapshot,currency_snapshot,plan_snapshot FROM orders WHERE user_id=? ORDER BY created_at DESC,id DESC LIMIT 5",
    "customer_services_summary": SERVICE_SQL + " AND user_id=?",
    "customer_invoices_summary": INVOICE_SQL + " AND user_id=?",
    "admin_services_summary": SERVICE_SQL,
    "admin_invoices_summary": INVOICE_SQL,
    "admin_customer_count": "SELECT COUNT(*) FROM users WHERE role='customer'",
}
EXPLAIN_TABLES = {
    "session_lookup": "sessions", "user_lookup": "users",
    "currency_configuration": "currency_settings", "customer_orders": "orders", "admin_orders": "orders",
    "customer_recent_orders": "orders",
    "customer_services_summary": "services", "customer_invoices_summary": "invoices",
    "admin_services_summary": "services", "admin_invoices_summary": "invoices",
    "admin_customer_count": "users",
}
ERROR_CODES = {"ARGUMENTS", "INPUT", "JSON", "SCHEMA", "IDENTITY", "PARAMETERS",
               "MACHINE", "DATASET", "EPOCHS", "MEASUREMENT", "EXPLAIN", "CLEANUP",
               "OUTPUT", "INTERNAL"}


class Rejected(Exception):
    def __init__(self, code):
        self.code = code if code in ERROR_CODES else "INTERNAL"
        super().__init__(self.code)


def require(condition, code):
    if not condition:
        raise Rejected(code)


def record(value, keys, code):
    require(type(value) is dict and set(value) == set(keys), code)
    return value


def integer(value, minimum=0, maximum=(1 << 63) - 1, code="SCHEMA"):
    require(type(value) is int and minimum <= value <= maximum, code)
    return value


def number(value, minimum=0, maximum=1e18, code="SCHEMA"):
    require(type(value) in (int, float) and math.isfinite(value)
            and minimum <= value <= maximum, code)
    return value


def text(value, pattern, code, maximum=2048):
    require(type(value) is str and len(value) <= maximum
            and re.fullmatch(pattern, value, flags=re.ASCII) is not None, code)
    return value


def digest(value, length, code="IDENTITY"):
    return text(value, "[0-9a-f]{" + str(length) + "}", code, length)


def fixed(value, expected, code):
    """Equality alone would incorrectly accept booleans as integer profile values."""
    require(type(value) is type(expected), code)
    if type(expected) is dict:
        record(value, expected, code)
        for key in expected:
            fixed(value[key], expected[key], code)
    elif type(expected) is list:
        require(len(value) == len(expected), code)
        for item, wanted in zip(value, expected):
            fixed(item, wanted, code)
    else:
        require(value == expected, code)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "JSON")
        result[key] = value
    return result


def invalid_constant(_value):
    raise Rejected("JSON")


def bounded_structure(value, depth=0, budget=None):
    if budget is None:
        budget = [20000]
    budget[0] -= 1
    require(depth <= 12 and budget[0] >= 0, "JSON")
    if type(value) is dict:
        require(len(value) <= 128, "JSON")
        for key, item in value.items():
            require(type(key) is str and len(key) <= 64 and key.isascii()
                    and all(32 <= ord(char) <= 126 for char in key), "JSON")
            bounded_structure(item, depth + 1, budget)
    elif type(value) is list:
        require(len(value) <= 256, "JSON")
        for item in value:
            bounded_structure(item, depth + 1, budget)
    elif type(value) is str:
        require(len(value) <= 2048 and value.isascii()
                and all(32 <= ord(char) <= 126 for char in value), "JSON")
    elif type(value) in (int, float):
        require(math.isfinite(value), "JSON")
    else:
        require(type(value) is bool, "JSON")


def file_identity(info):
    return (info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode,
            info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def read_input(path):
    descriptor = None
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
        before = os.fstat(descriptor)
        require(stat.S_ISREG(before.st_mode) and before.st_uid == os.geteuid()
                and stat.S_IMODE(before.st_mode) in (0o400, 0o600)
                and before.st_nlink == 1 and 0 < before.st_size <= MAX_INPUT_BYTES, "INPUT")
        chunks, length = [], 0
        while length <= MAX_INPUT_BYTES:
            chunk = os.read(descriptor, min(65536, MAX_INPUT_BYTES + 1 - length))
            if not chunk:
                break
            chunks.append(chunk)
            length += len(chunk)
        require(length == before.st_size and length <= MAX_INPUT_BYTES
                and file_identity(before) == file_identity(os.fstat(descriptor)), "INPUT")
        raw = b"".join(chunks)
    except OSError:
        raise Rejected("INPUT") from None
    finally:
        if descriptor is not None:
            os.close(descriptor)
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object,
                           parse_constant=invalid_constant)
        bounded_structure(value)
        return value
    except (ValueError, UnicodeError, OverflowError, RecursionError):
        raise Rejected("JSON") from None


def validate_machine(value):
    record(value, MACHINE_KEYS, "MACHINE")
    fixed(value["os"], "linux", "MACHINE")
    fixed(value["arch"], "amd64", "MACHINE")
    text(value["cpu_model"], r"unavailable|(?:Intel\(R\)|AMD)[A-Za-z0-9() .@+_-]{1,150}", "MACHINE", 160)
    for key in ("logical_cpus", "loadgen_gomaxprocs"):
        raw = text(value[key], r"[1-9][0-9]{0,4}", "MACHINE", 5)
        require(int(raw) <= 4096, "MACHINE")
    text(value["mem_total"], r"unavailable|[1-9][0-9]{0,15} kB", "MACHINE", 20)
    text(value["cgroup_cpu_max"], r"unavailable|(?:max|[1-9][0-9]{0,14}) [1-9][0-9]{0,11}", "MACHINE", 32)
    text(value["cgroup_memory_max"], r"unavailable|max|[1-9][0-9]{0,19}", "MACHINE", 20)
    cpuset = text(value["cgroup_cpuset"], r"unavailable|[0-9]{1,5}(?:-[0-9]{1,5})?(?:,[0-9]{1,5}(?:-[0-9]{1,5})?)*", "MACHINE", 1024)
    if cpuset != "unavailable":
        last = -1
        for item in cpuset.split(","):
            limits = [int(part) for part in item.split("-")]
            lower, upper = limits[0], limits[-1]
            require(last < lower <= upper <= 65535, "MACHINE")
            last = upper
    text(value["loadgen_go_version"], r"go[1-9][0-9]?\.[0-9]{1,3}\.[0-9]{1,4}", "MACHINE", 24)
    return int(value["loadgen_gomaxprocs"])


def validate_measurement(value):
    record(value, MEASUREMENT_KEYS, "MEASUREMENT")
    require(type(value["path"]) is str and value["path"] in PATHS, "MEASUREMENT")
    concurrency = integer(value["concurrency"], 1, 32, "MEASUREMENT")
    require(concurrency in CONCURRENCY, "MEASUREMENT")
    repeat = integer(value["repeat"], 1, PARAMETERS["repeats"], "MEASUREMENT")
    requests = integer(value["requests"], 1, 100_000_000, "MEASUREMENT")
    duration = integer(value["duration_ns"], PARAMETERS["duration_ns"], 60_000_000_000, "MEASUREMENT")
    qps = number(value["qps"], 0, 100_000_000, "MEASUREMENT")
    require(math.isclose(qps, requests * 1_000_000_000 / duration, rel_tol=1e-8, abs_tol=1e-8), "MEASUREMENT")
    latency = record(value["latency_ms"], {"p50", "p95", "p99"}, "MEASUREMENT")
    quantiles = [number(latency[key], 0, 60000, "MEASUREMENT") for key in ("p50", "p95", "p99")]
    require(quantiles == sorted(quantiles), "MEASUREMENT")
    transport = integer(value["transport_errors"], 0, requests, "MEASUREMENT")
    http = integer(value["http_errors"], 0, requests, "MEASUREMENT")
    rate = number(value["error_rate"], 0, 1, "MEASUREMENT")
    statuses = value["http_status_counts"]
    require(type(statuses) is dict and len(statuses) <= 500, "MEASUREMENT")
    for key, count in statuses.items():
        text(key, r"[1-5][0-9]{2}", "MEASUREMENT", 3)
        integer(count, 1, requests, "MEASUREMENT")
    require(sum(statuses.values()) + transport == requests
            and sum(count for key, count in statuses.items() if key != "200") == http
            and transport + http <= requests
            and math.isclose(rate, (transport + http) / requests, rel_tol=1e-9, abs_tol=1e-12), "MEASUREMENT")
    api = record(value["api"], {"cpu_seconds", "rss_peak_bytes", "heap_peak_bytes", "pool"}, "MEASUREMENT")
    number(api["cpu_seconds"], 0, 1_000_000, "MEASUREMENT")
    integer(api["rss_peak_bytes"], 0, 1 << 50, "MEASUREMENT")
    integer(api["heap_peak_bytes"], 0, 1 << 50, "MEASUREMENT")
    pool = record(api["pool"], {"wait_count", "wait_duration_ns", "in_use_peak", "open_connections_peak"}, "MEASUREMENT")
    integer(pool["wait_count"], 0, (1 << 63) - 1, "MEASUREMENT")
    integer(pool["wait_duration_ns"], 0, (1 << 63) - 1, "MEASUREMENT")
    opened = integer(pool["open_connections_peak"], 0, 20, "MEASUREMENT")
    integer(pool["in_use_peak"], 0, opened, "MEASUREMENT")
    locks = record(value["mysql_lock_delta"], LOCK_KEYS, "MEASUREMENT")
    for item in locks.values():
        integer(item, 0, (1 << 64) - 1, "MEASUREMENT")
    return value["path"], concurrency, repeat


def validate_explain(values):
    require(type(values) is list and len(values) == len(EXPLAIN_TABLES), "EXPLAIN")
    require(set(EXPLAIN_SQL) == set(EXPLAIN_TABLES), "INTERNAL")
    names = set()
    for value in values:
        record(value, {"name", "sql", "plan"}, "EXPLAIN")
        name = value["name"]
        require(type(name) is str and name in EXPLAIN_TABLES and name not in names, "EXPLAIN")
        names.add(name)
        fixed(value["sql"], EXPLAIN_SQL[name], "EXPLAIN")
        plan = record(value["plan"], {"query_cost", "tables"}, "EXPLAIN")
        number(plan["query_cost"], 0, 1e18, "EXPLAIN")
        require(type(plan["tables"]) is list and 0 <= len(plan["tables"]) <= 8, "EXPLAIN")
        seen = set()
        for table in plan["tables"]:
            record(table, {"table_name", "access_type", "key", "rows_examined_per_scan",
                           "rows_produced_per_join", "filtered", "using_index"}, "EXPLAIN")
            fixed(table["table_name"], EXPLAIN_TABLES[name], "EXPLAIN")
            require(table["table_name"] not in seen, "EXPLAIN")
            seen.add(table["table_name"])
            require(type(table["access_type"]) is str and table["access_type"] in ACCESS_TYPES
                    and type(table["key"]) is str and table["key"] in INDEXES[table["table_name"]], "EXPLAIN")
            integer(table["rows_examined_per_scan"], 0, (1 << 64) - 1, "EXPLAIN")
            integer(table["rows_produced_per_join"], 0, (1 << 64) - 1, "EXPLAIN")
            number(table["filtered"], 0, 100, "EXPLAIN")
            require(type(table["using_index"]) is bool, "EXPLAIN")
    require(names == set(EXPLAIN_TABLES), "EXPLAIN")


def validate_report(value, source_sha, harness_sha256, baseline_sha=None):
    digest(source_sha, 40)
    digest(harness_sha256, 64)
    if baseline_sha is not None:
        digest(baseline_sha, 40)
    record(value, TOP_KEYS, "SCHEMA")
    fixed(value["schema"], SCHEMA, "SCHEMA")
    fixed(value["source_sha"], source_sha, "IDENTITY")
    fixed(value["harness_sha256"], harness_sha256, "IDENTITY")
    fixed(value["seed"], SEED, "PARAMETERS")
    fixed(value["parameters"], PARAMETERS, "PARAMETERS")
    gomaxprocs = validate_machine(value["machine"])
    text(value["mysql_version"], r"8\.4\.[0-9]{1,4}(?:-[A-Za-z0-9._-]{1,32})?", "SCHEMA", 48)
    dataset = record(value["dataset"], {"counts", "sha256", "before_after_equal"}, "DATASET")
    fixed(dataset["counts"], COUNTS, "DATASET")
    digest(dataset["sha256"], 64, "DATASET")
    fixed(dataset["before_after_equal"], True, "DATASET")
    labels = ["candidate"] if baseline_sha is None else ["baseline_before", "candidate", "baseline_after"]
    epochs = value["epochs"]
    require(type(epochs) is list and len(epochs) == len(labels), "EPOCHS")
    expected = {(path, concurrency, repeat) for path in PATHS for concurrency in CONCURRENCY
                for repeat in range(1, PARAMETERS["repeats"] + 1)}
    baseline_binary = None
    for epoch, label in zip(epochs, labels):
        record(epoch, {"label", "source_sha", "binary_sha256", "dataset_sha256",
                       "go_version", "gomaxprocs", "measurements"}, "EPOCHS")
        fixed(epoch["label"], label, "EPOCHS")
        fixed(epoch["source_sha"], source_sha if label == "candidate" else baseline_sha, "IDENTITY")
        digest(epoch["binary_sha256"], 64, "EPOCHS")
        fixed(epoch["dataset_sha256"], dataset["sha256"], "DATASET")
        fixed(epoch["go_version"], value["machine"]["loadgen_go_version"], "EPOCHS")
        fixed(epoch["gomaxprocs"], gomaxprocs, "EPOCHS")
        measurements = epoch["measurements"]
        require(type(measurements) is list and len(measurements) == len(expected), "EPOCHS")
        seen = set()
        for item in measurements:
            identity = validate_measurement(item)
            require(identity not in seen, "EPOCHS")
            seen.add(identity)
        require(seen == expected, "EPOCHS")
        if label == "baseline_before":
            baseline_binary = epoch["binary_sha256"]
        elif label == "baseline_after":
            fixed(epoch["binary_sha256"], baseline_binary, "EPOCHS")
    validate_explain(value["explain"])
    fixed(value["cleanup"], {"database_dropped": True, "workers_stopped": True}, "CLEANUP")
    # Re-encoding only the exact validated schema removes formatting and escapes;
    # no raw JSON, exception, log, or unknown field can reach the public artifact.
    return json.loads(json.dumps(value, sort_keys=True, ensure_ascii=True, allow_nan=False))


def write_output(path, value):
    descriptor, parent_fd, created = None, None, None
    target = Path(path)
    try:
        parent_fd = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        parent = os.fstat(parent_fd)
        require(parent.st_uid == os.geteuid() and stat.S_IMODE(parent.st_mode) == 0o700, "OUTPUT")
        raw = (json.dumps(value, sort_keys=True, ensure_ascii=True, allow_nan=False,
                          separators=(",", ":")) + "\n").encode("ascii")
        require(len(raw) <= MAX_INPUT_BYTES, "OUTPUT")
        descriptor = os.open(target.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC,
                             0o600, dir_fd=parent_fd)
        created = os.fstat(descriptor)
        os.fchmod(descriptor, 0o600)
        remaining = memoryview(raw)
        while remaining:
            written = os.write(descriptor, remaining)
            require(written > 0, "OUTPUT")
            remaining = remaining[written:]
        os.fsync(descriptor)
    except (OSError, ValueError, Rejected):
        if created is not None and parent_fd is not None:
            try:
                current = os.stat(target.name, dir_fd=parent_fd, follow_symlinks=False)
                if (current.st_dev, current.st_ino) == (created.st_dev, created.st_ino):
                    os.unlink(target.name, dir_fd=parent_fd)
            except OSError:
                pass
        raise Rejected("OUTPUT") from None
    finally:
        if descriptor is not None:
            os.close(descriptor)
        if parent_fd is not None:
            os.close(parent_fd)


class FixedArgumentParser(argparse.ArgumentParser):
    def error(self, _message):
        raise Rejected("ARGUMENTS")


def main(arguments=None):
    try:
        parser = FixedArgumentParser(prog="performance-report", allow_abbrev=False)
        for name in ("input", "output", "source-sha", "harness-sha256"):
            parser.add_argument("--" + name, required=True)
        parser.add_argument("--baseline-sha")
        args = parser.parse_args(arguments)
        digest(args.source_sha, 40, "ARGUMENTS")
        digest(args.harness_sha256, 64, "ARGUMENTS")
        if args.baseline_sha is not None:
            digest(args.baseline_sha, 40, "ARGUMENTS")
        report = validate_report(read_input(args.input), args.source_sha, args.harness_sha256, args.baseline_sha)
        # Only this reconstructed, fully validated object is eligible for the
        # public log. Prepare every line before creating output or printing any
        # data, so malformed/oversized evidence always has empty stdout.
        lines = ["PERFORMANCE_REPORT_JSON " + json.dumps(
            report, sort_keys=True, ensure_ascii=True, allow_nan=False, separators=(",", ":"))]
        for index, epoch in enumerate(report["epochs"]):
            measurements = epoch["measurements"]
            lines.append("PERFORMANCE_REPORT_EPOCH index=%d measurements=%d requests=%d errors=%d" % (
                index, len(measurements), sum(item["requests"] for item in measurements),
                sum(item["http_errors"] + item["transport_errors"] for item in measurements)))
        public_output = "\n".join(lines) + "\n"
        require(len(public_output.encode("ascii")) <= MAX_STDOUT_BYTES, "SCHEMA")
        write_output(args.output, report)
        print(public_output, end="", flush=True)
        return 0
    except Rejected as error:
        print("PERFORMANCE_REPORT_ERROR=" + error.code, file=sys.stderr)
        return 1
    except Exception:
        print("PERFORMANCE_REPORT_ERROR=INTERNAL", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
