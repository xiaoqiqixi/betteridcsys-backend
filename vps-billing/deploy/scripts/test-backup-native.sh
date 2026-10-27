#!/usr/bin/env bash
# Fake mysql/mysqldump. Exercises schema evidence and filesystem/pipe behavior,
# never a DB. Physical column metadata below is synthetic, not a restore test.
set -euo pipefail
umask 077
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
# A missing scanner must stop the suite, never mean "no secret matched".
command -v grep >/dev/null 2>&1 || fail 'required leakage scanner grep is unavailable'
script_dir=$(cd "$(dirname "$0")" && pwd -P)
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/aster-native-backup-test.XXXXXXXX")
trap 'rm -rf -- "$test_dir"' EXIT
mkdir -m 700 "$test_dir/bin" "$test_dir/private" "$test_dir/backups" "$test_dir/copied backups" "$test_dir/calls" "$test_dir/fixtures"
export ASTER_TEST_REAL_GZIP ASTER_TEST_REAL_MV ASTER_TEST_REAL_SHA ASTER_TEST_REAL_STAT
ASTER_TEST_REAL_GZIP=$(command -v gzip)
ASTER_TEST_REAL_MV=$(command -v mv)
ASTER_TEST_REAL_SHA=$(command -v sha256sum)
ASTER_TEST_REAL_STAT=$(command -v stat)
export ASTER_TEST_CALLS="$test_dir/calls"
export ASTER_TEST_FIXTURES="$test_dir/fixtures"
python3 - "$script_dir/../../backend/migrations" "$test_dir/fixtures" <<'PY'
import hashlib
import json
from pathlib import Path
import sys

source, fixtures = map(Path, sys.argv[1:])
release = fixtures / "release"
(release / "migrations").mkdir(parents=True)
(release / "manifests").mkdir()
paths = sorted(source.glob("[0-9][0-9][0-9]_*.sql"))
assert len(paths) == 13, "Review the schema fixture for new migrations"
for path in paths:
    (release / "migrations" / path.name).write_bytes(path.read_bytes())
build = {"schema": 3, "distribution_format": "raw-binaries-v1", "release_kind": "core", "release_label": "0.5.0",
         "source_commit": "a" * 40, "component_versions": {"core": "0.5.0", "smtp": "1.0.2"}, "target": "linux/amd64",
         "cgo_enabled": False, "source_files": {"migrations/" + path.name: "vps-billing/backend/migrations/" + path.name for path in paths}}
(release / "manifests/BUILD.json").write_text(json.dumps(build) + "\n")
(fixtures / "BUILD.original.json").write_text(json.dumps(build) + "\n")
# Independent expected inventory: never derive this fixture from the script's
# parser. Hashes use the immutable real migration bytes, as MySQL history does.
tables = {
    "users": "id email name role password_hash created_at disabled revision",
    "sessions": "token_hash user_id expires_at",
    "plans": "id name description cpu memory_gb disk_gb transfer_tb region price_minor currency available type_id region_id node_id",
    "orders": "id number user_id plan_id plan_name hostname status total_minor currency created_at issuer_snapshot currency_snapshot plan_snapshot",
    "invoices": "id number user_id order_id status amount_minor currency issued_at due_at paid_at issuer_snapshot currency_snapshot",
    "order_requests": "user_id request_key fingerprint order_id created_at",
    "services": "id user_id name plan_name region ipv4 status next_due_at price_minor currency cpu memory_gb disk_gb currency_snapshot",
    "audit_events": "id user_id action target created_at actor_email detail",
    "outbox_events": "id aggregate_id event_type payload created_at processed_at attempts",
    "site_settings": "id document", "plan_prices": "plan_id currency amount_minor available",
    "email_settings": "id document", "currency_settings": "id document prices_revision",
    "payment_attempts": "id request_key user_id invoice_id order_id provider_id merchant_account_id amount_minor currency currency_decimal_places state checkout created_at updated_at",
    "payment_settlements": "attempt_id provider_id event_id transaction_id settled_at received_at",
    "schema_migrations": "version checksum applied_at",
    "catalog_types": "id name sort_order enabled",
    "catalog_regions": "id type_id name sort_order enabled",
    "catalog_taxonomy_settings": "id revision",
    "catalog_nodes": "id region_id name sort_order enabled",
    "product_configurations": "plan_id revision spec configuration_sha256 updated_at",
    "order_product_configurations": "order_id plan_id configuration_revision spec configuration_sha256 created_at",
    "provisioning_jobs": "id order_id state request request_sha256 claim_token lease_expires_at result attempts created_at updated_at",
}
identity = ["identity", "8.4.11", "MySQL Community Server - GPL", "aster_fixture", "12345678-1234-1234-1234-123456789abc"]
history = [["migration", str(number), hashlib.sha256(path.read_bytes()).hexdigest()] for number, path in enumerate(paths, 1)]
rows = [identity, ["unsupported", "0"], *history]
rows += [["table", table, "BASE TABLE", "InnoDB"] for table in sorted(tables)]
rows += [["column", table, column, "fixture-type", "YES", "NULL", "", "", "", "", str(index), ""]
         for table in sorted(tables) for index, column in enumerate(tables[table].split(), 1)]
rows += [["constraint", "users", "PRIMARY", "PRIMARY KEY", "YES", ""],
         ["index", "users", "PRIMARY", "0", "1", "id", "A", "0", "BTREE", "YES", ""]]
for name, columns in (("ix_audit_date_id", ("created_at", "id")),
                      ("ix_audit_actor_date_id", ("actor_email", "created_at", "id")),
                      ("ix_audit_action_date_id", ("action", "created_at", "id"))):
    rows += [["index", "audit_events", name, "1", str(index), column, "A", "0", "BTREE", "YES", ""]
             for index, column in enumerate(columns, 1)]
inventory = [row for row in rows if row[0] != "migration"][2:]
def write(name, contents):
    (fixtures / (name + ".tsv")).write_text("".join("\t".join(row) + "\n" for row in contents))
write("valid", rows)
for name, contents in {
    "empty-history": [row for row in rows if row[0] != "migration"],
    "gap": [row for row in rows if row[:2] != ["migration", "2"]],
    "checksum": [[*row[:2], "0" * 64] if row[:2] == ["migration", "1"] else row for row in rows],
    "unknown-version": rows[:2] + history + [["migration", "14", "0" * 64]] + inventory,
    "duplicate-version": rows[:2] + history[:1] + history + inventory,
    "zero-version": rows[:2] + [["migration", "0", "0" * 64]] + rows[2:],
    "negative-version": rows[:2] + [["migration", "-1", "0" * 64]] + rows[2:],
    "empty-schema": [row for row in rows if row[0] not in {"table", "column"}],
    "missing-table": [row for row in rows if not (row[0] in {"table", "column"} and row[1] == "currency_settings")],
    "missing-column": [row for row in rows if row[:3] != ["column", "orders", "plan_snapshot"]],
    "missing-payment-table": [row for row in rows if not (row[0] in {"table", "column"} and row[1] == "payment_settlements")],
    "missing-payment-column": [row for row in rows if row[:3] != ["column", "payment_attempts", "merchant_account_id"]],
    "missing-account-column": [row for row in rows if row[:3] != ["column", "users", "disabled"]],
    "partial-taxonomy-next-migration": [row for row in rows if row[:2] != ["migration", "11"]],
    "partial-node-next-migration": [row for row in rows if row[:2] != ["migration", "12"]],
    "missing-node-table": [row for row in rows if not (row[0] in {"table", "column"} and row[1] == "catalog_nodes")],
    "missing-node-column": [row for row in rows if row[:3] != ["column", "plans", "node_id"]],
    "missing-taxonomy-table": [row for row in rows if not (row[0] in {"table", "column"} and row[1] == "catalog_regions")],
    "missing-taxonomy-column": [row for row in rows if row[:3] != ["column", "plans", "region_id"]],
    "partial-product-configuration-next-migration": [row for row in rows if row[:2] != ["migration", "13"]],
    "missing-product-configuration-table": [row for row in rows if not (row[0] in {"table", "column"} and row[1] == "product_configurations")],
    "missing-order-configuration-table": [row for row in rows if not (row[0] in {"table", "column"} and row[1] == "order_product_configurations")],
    "missing-provisioning-table": [row for row in rows if not (row[0] in {"table", "column"} and row[1] == "provisioning_jobs")],
    "missing-order-configuration-column": [row for row in rows if row[:3] != ["column", "order_product_configurations", "configuration_sha256"]],
    "missing-provisioning-column": [row for row in rows if row[:3] != ["column", "provisioning_jobs", "claim_token"]],
    "unknown-table": rows + [["table", "unreviewed", "BASE TABLE", "InnoDB"]],
    "extra-column": rows + [["column", "orders", "unreviewed", "int", "YES", "NULL", "", "", "", "", "14", ""]],
    "wrong-engine": [[*row[:3], "MyISAM"] if row[:2] == ["table", "plans"] else row for row in rows],
    "changed-column-type": [[*row[:3], "changed-type", *row[4:]] if row[:3] == ["column", "users", "id"] else row for row in rows],
    "changed-check": [[*row[:-1], "6368616E676564"] if row[0] == "constraint" else row for row in rows],
    "wrong-database": [[*row[:3], "another_database", row[4]] if row[0] == "identity" else row for row in rows],
    "old-mysql": [[row[0], "8.0.44", *row[2:]] if row[0] == "identity" else row for row in rows],
    "mariadb": [[row[0], "10.11.8-MariaDB", "mariadb.org binary distribution", *row[3:]] if row[0] == "identity" else row for row in rows],
    "percona": [[*row[:2], "Percona Server (GPL)", *row[3:]] if row[0] == "identity" else row for row in rows],
}.items():
    write(name, contents)
product_tables = {"product_configurations", "order_product_configurations", "provisioning_jobs"}
prefix12 = [row for row in rows if row[:2] != ["migration", "13"] and not
            (row[0] in {"table", "column"} and row[1] in product_tables)]
write("prefix12", prefix12)
prefix11 = [row for row in prefix12 if row[:2] != ["migration", "12"] and not
            (row[0] in {"table", "column"} and row[1] == "catalog_nodes") and not
            (row[0] == "column" and row[1] == "plans" and row[2] == "node_id")]
write("prefix11", prefix11)
prefix10 = [row for row in prefix11 if row[:2] != ["migration", "11"] and not
            (row[0] in {"table", "column"} and row[1] in {"catalog_types", "catalog_regions", "catalog_taxonomy_settings"}) and not
            (row[0] == "column" and row[1] == "plans" and row[2] in {"type_id", "region_id"})]
write("prefix10", prefix10)
write("partial-account-next-migration", [row for row in prefix10 if row[:2] != ["migration", "10"]])
prefix9 = [row for row in prefix10 if row[:2] != ["migration", "10"] and not
           (row[0] == "column" and row[1] == "users" and row[2] in {"disabled", "revision"})]
write("prefix9", prefix9)
prefix8 = [row for row in prefix9 if row[:2] != ["migration", "9"] and not
           (row[0] == "index" and row[1] == "audit_events")]
write("prefix8", prefix8)
prefix7 = [row for row in prefix8 if row[:2] != ["migration", "8"] and not
           (row[0] in {"table", "column"} and row[1] in {"payment_attempts", "payment_settlements"})]
write("prefix7", prefix7)
prefix6 = [row for row in prefix7 if row[:2] != ["migration", "7"] and not
           (row[0] in {"table", "column"} and row[1] == "currency_settings") and not
           (row[0] == "column" and row[2] in {"currency_snapshot", "plan_snapshot"})]
write("prefix6", prefix6)
for version, prefix in ((6, prefix6), (7, prefix7), (8, prefix8), (9, prefix9), (10, prefix10), (11, prefix11), (12, prefix12)):
    assert [row[1] for row in prefix if row[0] == "migration"] == [str(number) for number in range(1, version + 1)]
    assert [row[2] for row in prefix if row[:2] == ["column", "users"]] == (["id", "email", "name", "role", "password_hash", "created_at"] + (["disabled", "revision"] if version >= 10 else []))
    if version <= 10:
        assert not any(row[0] in {"table", "column"} and row[1].startswith("catalog_") for row in prefix)
        assert not any(row[:2] == ["column", "plans"] and row[2] in {"type_id", "region_id"} for row in prefix)
    if version < 12:
        assert not any(row[0] in {"table", "column"} and row[1] == "catalog_nodes" for row in prefix)
        assert not any(row[:3] == ["column", "plans", "node_id"] for row in prefix)
    assert not any(row[0] in {"table", "column"} and row[1] in product_tables for row in prefix)
assert [row[2] for row in rows if row[:2] == ["column", "users"]] == tables["users"].split()
write("partial-next-migration", [row for row in prefix8 if row[:2] != ["migration", "8"]])
write("partial-payment-next-migration", [row for row in prefix8 if row[:2] != ["migration", "8"] and not
           (row[0] in {"table", "column"} and row[1] == "payment_settlements")])
PY
cat > "$test_dir/private/client.cnf" <<'CNF'
# Test fixture only; not real credentials.
[client]
user=fixture_backup
password="fixture-not-a-real-secret"
CNF
chmod 600 "$test_dir/private/client.cnf"
# The cloud sandbox maps system-directory ownership to uid65534. Model a
# conventional root-owned Linux / and /tmp in this fixture only; endpoint and
# nested ancestor ownership/modes continue to use real filesystem metadata.
cat > "$test_dir/bin/stat" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
path=${!#}
if [[ $1 == -c && $2 == %u && ( "$path" == / || "$path" == /tmp ) ]]; then
  printf '%s\n' 0
else
  exec "$ASTER_TEST_REAL_STAT" "$@"
fi
MOCK
cat > "$test_dir/bin/mysql" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
[[ $1 == --defaults-file=* ]] || exit 81
[[ ! ${MYSQL_PWD+set} && ! ${MYSQL_HOST+set} && ! ${MYSQL_GROUP_SUFFIX+set} ]] || exit 82
args=" $* "
[[ "$args" == *' --protocol=SOCKET '* && "$args" == *' --socket='* && "$args" == *' --no-login-paths '* && "$args" == *'--execute=SELECT'* ]] || exit 84
[[ ${!#} == aster_fixture ]] || exit 85
query=''
for arg; do
  case "$arg" in --execute=*) query=${arg#--execute=} ;; esac
done
statements=0
while IFS= read -r -d ';' statement; do
  [[ "$statement" =~ ^[[:space:]]*SELECT[[:space:]] ]] || exit 86
  statements=$((statements + 1))
done <<< "$query"
[[ "$statements" == 9 ]] || exit 87
[[ ${ASTER_TEST_METADATA_FAIL:-0} == 0 ]] || exit 45
snapshot=${ASTER_TEST_ACTIVE_SNAPSHOT:-$ASTER_TEST_FIXTURES/${ASTER_TEST_SNAPSHOT:-valid}.tsv}
while IFS= read -r line; do
  if [[ "$line" == unsupported$'\t'* && ${ASTER_TEST_SCHEMA_COUNT+set} ]]; then
    printf 'unsupported\t%s\n' "$ASTER_TEST_SCHEMA_COUNT"
  else
    printf '%s\n' "$line"
  fi
done < "$snapshot"
MOCK
cat > "$test_dir/bin/mysqldump" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
[[ $1 == --defaults-file=* ]] || exit 81
[[ ! ${MYSQL_PWD+set} && ! ${MYSQL_HOST+set} && ! ${MYSQL_GROUP_SUFFIX+set} ]] || exit 82
printf '%s\n' "$@" > "$ASTER_TEST_CALLS/$$"
for arg; do
  case "$arg" in --password*|-p*|--databases|--all-databases|--force|--add-drop-table) exit 83 ;; esac
done
args=" $* "
[[ "$args" == *' --no-login-paths '* && "$args" == *' --protocol=SOCKET '* && "$args" == *' --socket='* && "$args" == *' --skip-add-drop-table '* && "$args" == *' --single-transaction '* ]] || exit 84
printf '%s\n' '-- fake SQL fixture; contains no database data' 'CREATE TABLE fixture (id INT);' 'INSERT INTO fixture VALUES (1);'
if [[ ${ASTER_TEST_AFTER_SNAPSHOT+set} ]]; then
  cp "$ASTER_TEST_FIXTURES/$ASTER_TEST_AFTER_SNAPSHOT.tsv" "$ASTER_TEST_ACTIVE_SNAPSHOT"
fi
if [[ ${ASTER_TEST_RELEASE_CHANGE+set} ]]; then
  printf '\n' >> "$ASTER_TEST_FIXTURES/release/migrations/001_initial.sql"
fi
if [[ ${ASTER_TEST_DUMP_WARN:-0} == 1 ]]; then printf '%s\n' 'fixture-private-diagnostic-do-not-log' >&2; fi
if [[ ${ASTER_TEST_DUMP_FAIL:-0} == 1 ]]; then
  printf '%s\n' 'fixture-private-diagnostic-do-not-log' >&2
  exit 42
fi
MOCK
cat > "$test_dir/bin/gzip" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
if [[ ${ASTER_TEST_GZIP_FAIL:-0} == 1 && $1 != -t ]]; then
  cat >/dev/null
  printf '%s\n' 'fixture-private-diagnostic-do-not-log' >&2
  exit 43
fi
if [[ ${ASTER_TEST_GZIP_CORRUPT:-0} == 1 && $1 != -t ]]; then
  cat >/dev/null
  printf '%s\n' 'not a valid gzip archive'
  exit 0
fi
exec "$ASTER_TEST_REAL_GZIP" "$@"
MOCK
cat > "$test_dir/bin/mv" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
if [[ ${ASTER_TEST_COLLISION:-0} == 1 ]]; then
  destination=${!#}
  mkdir -m 700 -- "$destination"
  printf '%s\n' 'existing-backup-must-survive' > "$destination/sentinel"
fi
exec "$ASTER_TEST_REAL_MV" "$@"
MOCK
cat > "$test_dir/bin/sha256sum" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
[[ ${ASTER_TEST_SHA_FAIL:-0} == 0 ]] || exit 44
exec "$ASTER_TEST_REAL_SHA" "$@"
MOCK
cat > "$test_dir/bin/date" <<'MOCK'
#!/usr/bin/env bash
printf '%s\n' '20261014T000000Z'
MOCK
chmod 755 "$test_dir/bin/"*
export PATH="$test_dir/bin:$PATH"
script="$script_dir/backup-native.sh"
release_args=(--release-directory "$test_dir/fixtures/release")
base_args=(--client-config "$test_dir/private/client.cnf" --socket "$test_dir/fake-mysql.sock" --database aster_fixture --output-dir "$test_dir/backups" "${release_args[@]}")
assert_no_private_markers() {
  local status=0
  # Do not use -q: GNU grep may return success after a match despite a later
  # read error. Suppress the actual matching text so fixtures stay private.
  grep -a -E -r -- 'fixture-not-a-real-secret|fixture-private-diagnostic-do-not-log' "$@" >/dev/null 2>&1 || status=$?
  case "$status" in
    0) fail 'private diagnostic or credential leaked' ;;
    1) return 0 ;;
    *) fail "leakage scanner failed (exit $status); result is not a clean scan" ;;
  esac
}
assert_clean() {
  [[ -z $(find "$test_dir/backups" -name '*.partial' -print -quit) ]] || fail 'staging data survived a handled failure'
}
expect_failure() {
  local archives_before
  archives_before=$(find "$test_dir/backups" -name database.sql.gz -type f | wc -l)
  local result=0
  "$@" > "$test_dir/result.log" 2>&1 || result=$?
  ((result != 0)) || fail 'operation unexpectedly succeeded'
  python3 - "$test_dir/result.log" "$result" <<'PYDIAGNOSTIC'
import json
from pathlib import Path
import re
import sys
lines = Path(sys.argv[1]).read_text().splitlines()
assert len(lines) == 1, "Expected one safe failure event"
event = json.loads(lines[0])
assert set(event) == {"event", "operation_id", "phase", "tool_role", "exit_code", "reason"}
assert event["event"] == "backup_failed" and event["exit_code"] == int(sys.argv[2])
assert re.fullmatch(r"[a-f0-9]{32}", event["operation_id"])
assert re.fullmatch(r"[a-z_]+", event["phase"])
assert event["tool_role"] in {"builtin", "filesystem", "python", "metadata_validator", "mysql", "mysqldump", "gzip", "sha256sum"}
assert event["reason"] in {"validation_failed", "command_failed", "unexpected_exit", "interrupted", "cleanup_failed",
    "missing_argument", "repeated_argument", "unsupported_argument", "invalid_database", "unsupported_database",
    "unsafe_path", "required_tool", "private_client_config", "unsafe_output_dir", "invalid_release_directory",
    "invalid_client_options", "utc_timestamp", "schema_changed", "empty_archive", "publish_collision"}
PYDIAGNOSTIC
  assert_no_private_markers "$test_dir/result.log"
  assert_clean
  [[ $(find "$test_dir/backups" -name database.sql.gz -type f | wc -l) == "$archives_before" ]] || fail 'a failed operation published a backup'
}
expect_preflight_failure() {
  local dumps_before
  dumps_before=$(find "$test_dir/calls" -type f | wc -l)
  expect_failure "$@"
  [[ $(find "$test_dir/calls" -type f | wc -l) == "$dumps_before" ]] || fail 'preflight failure reached mysqldump'
}
# Predictable timestamp ensures that concurrency depends on random identity,
# not on waiting for the clock. Poisoned env must not influence client options.
pids=()
for _ in {1..8}; do
  MYSQL_PWD=fixture-not-a-real-secret MYSQL_HOST=not-a-real-host MYSQL_GROUP_SUFFIX=_bad \
    bash "$script" "${base_args[@]}" >/dev/null &
  pids+=("$!")
done
concurrent_failed=0
for pid in "${pids[@]}"; do wait "$pid" || concurrent_failed=1; done
((concurrent_failed == 0)) || fail 'concurrent backup failed'
[[ $(find "$test_dir/backups" -name database.sql.gz -type f | wc -l) == 8 ]] || fail 'concurrent runs overwrote a backup'
assert_clean
cp -R "$test_dir/backups/." "$test_dir/copied backups/"
for directory in "$test_dir/copied backups/"*; do
  (cd "$directory" && sha256sum --check --status SHA256SUMS && gzip -t database.sql.gz) || fail 'portable checksum/gzip validation failed'
  python3 - "$directory" "$test_dir/fixtures/release" <<'PY'
import hashlib
import json
from pathlib import Path
import sys
backup, release = map(Path, sys.argv[1:])
metadata = json.loads((backup / "BACKUP.json").read_bytes())
assert metadata == {"schema": 1, "source_commit": "a" * 40, "core_version": "0.5.0", "database": "aster_fixture", "schema_version": 13,
                    "migration_checksums": {str(number): hashlib.sha256(path.read_bytes()).hexdigest()
                                            for number, path in enumerate(sorted((release / "migrations").glob("*.sql")), 1)}}
assert set(path.name for path in backup.iterdir()) == {"database.sql.gz", "BACKUP.txt", "BACKUP.json", "SHA256SUMS"}
assert {line.split("  ")[1] for line in (backup / "SHA256SUMS").read_text().splitlines()} == {"database.sql.gz", "BACKUP.txt", "BACKUP.json"}
assert "restore" not in metadata and "verified" not in metadata
PY
done
[[ -z $(find "$test_dir/backups" -type d ! -perm 700 -print -quit) ]] || fail 'non-private output directory'
[[ -z $(find "$test_dir/backups" -type f ! -perm 600 -print -quit) ]] || fail 'non-private output file'
assert_no_private_markers "$test_dir/backups" "$test_dir/calls"
printf '%s\n' 'PASS: 8 concurrent same-second backups, private permissions, fixed socket/options, environment isolation, portable checksums (fake mysqldump)'

expect_failure env ASTER_TEST_DUMP_FAIL=1 bash "$script" "${base_args[@]}"
python3 - "$test_dir/result.log" <<'PYDIAGNOSTIC'
import json, sys
from pathlib import Path
value = json.loads(Path(sys.argv[1]).read_text())
assert (value["phase"], value["tool_role"], value["exit_code"]) == ("dump", "mysqldump", 42)
PYDIAGNOSTIC
expect_failure env ASTER_TEST_GZIP_FAIL=1 bash "$script" "${base_args[@]}"
python3 - "$test_dir/result.log" <<'PYDIAGNOSTIC'
import json, sys
from pathlib import Path
value = json.loads(Path(sys.argv[1]).read_text())
assert (value["phase"], value["tool_role"], value["exit_code"]) == ("dump", "gzip", 43)
PYDIAGNOSTIC
expect_failure env ASTER_TEST_GZIP_CORRUPT=1 bash "$script" "${base_args[@]}"
expect_failure env ASTER_TEST_SHA_FAIL=1 bash "$script" "${base_args[@]}"
python3 - "$test_dir/result.log" <<'PYDIAGNOSTIC'
import json, sys
from pathlib import Path
value = json.loads(Path(sys.argv[1]).read_text())
assert (value["phase"], value["tool_role"], value["exit_code"]) == ("checksum_creation", "sha256sum", 44)
PYDIAGNOSTIC
[[ $(find "$test_dir/backups" -name database.sql.gz -type f | wc -l) == 8 ]] || fail 'a failed run published a backup'
printf '%s\n' 'PASS: dump, compression, compressed-data validation and checksum failures publish nothing; private diagnostics are not echoed'
expect_preflight_failure env ASTER_TEST_METADATA_FAIL=1 bash "$script" "${base_args[@]}"
expect_preflight_failure env ASTER_TEST_SCHEMA_COUNT=1 bash "$script" "${base_args[@]}"
expect_preflight_failure env ASTER_TEST_SCHEMA_COUNT=invalid bash "$script" "${base_args[@]}"
for snapshot in empty-history gap checksum unknown-version duplicate-version zero-version negative-version empty-schema missing-table missing-column missing-payment-table missing-payment-column missing-account-column partial-account-next-migration missing-taxonomy-table missing-taxonomy-column partial-taxonomy-next-migration partial-node-next-migration missing-node-table missing-node-column partial-product-configuration-next-migration missing-product-configuration-table missing-order-configuration-table missing-provisioning-table missing-order-configuration-column missing-provisioning-column unknown-table extra-column wrong-engine wrong-database old-mysql mariadb percona partial-next-migration partial-payment-next-migration; do
  expect_preflight_failure env ASTER_TEST_SNAPSHOT="$snapshot" bash "$script" "${base_args[@]}"
done
printf '%s\n' 'PASS: unsupported server/database identity, empty/gapped/changed/unknown migration history and unknown/partial tables or columns stop before dumping'

for snapshot in prefix6 changed-column-type changed-check wrong-database; do
  cp "$test_dir/fixtures/valid.tsv" "$test_dir/fixtures/active.tsv"
  expect_failure env ASTER_TEST_ACTIVE_SNAPSHOT="$test_dir/fixtures/active.tsv" ASTER_TEST_AFTER_SNAPSHOT="$snapshot" bash "$script" "${base_args[@]}"
done
expect_failure env ASTER_TEST_RELEASE_CHANGE=1 bash "$script" "${base_args[@]}"
cp "$script_dir/../../backend/migrations/001_initial.sql" "$test_dir/fixtures/release/migrations/001_initial.sql"
printf '%s\n' 'PASS: changes to migration history, observed column definitions, checks, server identity or release SQL during dump publish nothing'

expect_preflight_failure bash "$script" "${base_args[@]:0:8}"
expect_preflight_failure bash "$script" "${base_args[@]}" "${release_args[@]}"
cp -R "$test_dir/fixtures/release" "$test_dir/fixtures/release.original"
for mutation in missing-build malformed-build duplicate-keys build-schema bool-schema distribution component version commit target cgo source-missing source-mapping source-extra missing-sql duplicate-number unknown-file sql-symlink digest grammar; do
  python3 - "$test_dir/fixtures/release" "$mutation" <<'PY'
import json
from pathlib import Path
import sys
root, mutation = Path(sys.argv[1]), sys.argv[2]
path = root / "manifests/BUILD.json"
build = json.loads(path.read_bytes())
if mutation in {"missing-build", "malformed-build", "duplicate-keys"}:
    if mutation == "missing-build":
        path.unlink()
    else:
        path.write_text("invalid json" if mutation == "malformed-build" else '{"schema":3,"schema":3}')
elif mutation in {"build-schema", "bool-schema", "distribution", "component", "version", "commit", "target", "cgo"}:
    key, value = {"build-schema": ("schema", 99), "bool-schema": ("schema", True), "distribution": ("distribution_format", "archive-v1"),
                  "component": ("release_kind", "smtp"), "version": ("release_label", "0.5.1"), "commit": ("source_commit", "not-a-commit"),
                  "target": ("target", "linux/arm64"), "cgo": ("cgo_enabled", True)}[mutation]
    build[key] = value
elif mutation == "source-missing":
    del build["source_files"]["migrations/001_initial.sql"]
elif mutation == "source-mapping":
    build["source_files"]["migrations/001_initial.sql"] = "vps-billing/backend/migrations/002_site_settings.sql"
elif mutation == "source-extra":
    build["source_files"]["migrations/010_unknown.sql"] = "vps-billing/backend/migrations/010_unknown.sql"
elif mutation == "missing-sql":
    (root / "migrations/003_currencies.sql").unlink()
elif mutation == "duplicate-number":
    (root / "migrations/001_duplicate.sql").write_bytes((root / "migrations/001_initial.sql").read_bytes())
elif mutation == "unknown-file":
    (root / "migrations/unreviewed.txt").write_text("unreviewed")
elif mutation == "sql-symlink":
    sql = root / "migrations/001_initial.sql"
    sql.unlink()
    sql.symlink_to(root.parent / "release.original/migrations/001_initial.sql")
elif mutation == "digest":
    build["input_sha256"] = {"vps-billing/backend/migrations/001_initial.sql": "0" * 64}
elif mutation == "grammar":
    (root / "migrations/007_currency_registry.sql").write_text("DROP TABLE users;\n")
else:
    raise AssertionError(mutation)
if path.exists() and mutation not in {"malformed-build", "duplicate-keys"}:
    path.write_text(json.dumps(build))
PY
  expect_preflight_failure bash "$script" "${base_args[@]}"
  rm -rf "$test_dir/fixtures/release"
  cp -R "$test_dir/fixtures/release.original" "$test_dir/fixtures/release"
done
printf '%s\n' 'PASS: missing release argument, invalid BUILD identity/schema, incomplete source mappings, missing/duplicate/linked SQL and digest/grammar mismatches stop before dumping'

for mode in 644 640 700; do
  chmod "$mode" "$test_dir/private/client.cnf"
  expect_preflight_failure bash "$script" "${base_args[@]}"
done
chmod 600 "$test_dir/private/client.cnf"
ln -s "$test_dir/private/client.cnf" "$test_dir/private/link.cnf"
expect_preflight_failure bash "$script" --client-config "$test_dir/private/link.cnf" --socket "$test_dir/fake-mysql.sock" --database aster_fixture --output-dir "$test_dir/backups" "${release_args[@]}"
ln "$test_dir/private/client.cnf" "$test_dir/private/hard.cnf"
expect_preflight_failure bash "$script" "${base_args[@]}"
rm "$test_dir/private/hard.cnf"
chmod 770 "$test_dir/private"
expect_preflight_failure bash "$script" "${base_args[@]}"
chmod 700 "$test_dir/private"
cp "$test_dir/private/client.cnf" "$test_dir/original.cnf"
for invalid in '!include /etc/mysql/my.cnf' '[mysqldump]' 'force' 'host=remote.example' 'init-command=SELECT 1' 'user=second_user' 'user='; do
  cp "$test_dir/original.cnf" "$test_dir/private/client.cnf"
  printf '%s\n' "$invalid" >> "$test_dir/private/client.cnf"
  expect_preflight_failure bash "$script" "${base_args[@]}"
done
cp "$test_dir/original.cnf" "$test_dir/private/client.cnf"
for db in '--all-databases' 'aster;DROP' 'aster/name' 'aster name' 'mysql' 'information_schema'; do
  expect_preflight_failure bash "$script" --client-config "$test_dir/private/client.cnf" --socket "$test_dir/fake-mysql.sock" --database "$db" --output-dir "$test_dir/backups" "${release_args[@]}"
done
chmod 755 "$test_dir/backups"
expect_preflight_failure bash "$script" "${base_args[@]}"
chmod 700 "$test_dir/backups"
expect_preflight_failure bash "$script" --client-config "$test_dir/private/client.cnf" --socket "$test_dir/fake-mysql.sock" --database aster_fixture --output-dir "$test_dir/not-created" "${release_args[@]}"
[[ ! -e "$test_dir/not-created" ]] || fail 'script created an unapproved output directory'
# Private endpoints below a non-sticky writable ancestor are still unsafe.
mkdir -m 700 "$test_dir/loose" "$test_dir/loose/private" "$test_dir/loose/output"
cp "$test_dir/original.cnf" "$test_dir/loose/private/client.cnf"
chmod 777 "$test_dir/loose"
expect_preflight_failure bash "$script" --client-config "$test_dir/loose/private/client.cnf" --socket "$test_dir/fake-mysql.sock" --database aster_fixture --output-dir "$test_dir/backups" "${release_args[@]}"
expect_preflight_failure bash "$script" --client-config "$test_dir/private/client.cnf" --socket "$test_dir/fake-mysql.sock" --database aster_fixture --output-dir "$test_dir/loose/output" "${release_args[@]}"
ln -s "$test_dir/backups" "$test_dir/output-link"
expect_preflight_failure bash "$script" --client-config "$test_dir/private/client.cnf" --socket "$test_dir/fake-mysql.sock" --database aster_fixture --output-dir "$test_dir/output-link/" "${release_args[@]}"
printf '%s\n' 'PASS: unsafe config modes/links/options, writable config directory/ancestors, output symlink, unsafe identifiers and output paths rejected before dump'

expect_failure env ASTER_TEST_COLLISION=1 bash "$script" "${base_args[@]}"
[[ $(find "$test_dir/backups" -name sentinel -type f | wc -l) == 1 ]] || fail 'collision target was replaced'
sentinel=$(find "$test_dir/backups" -name sentinel -type f)
[[ $(cat "$sentinel") == existing-backup-must-survive ]] || fail 'collision target contents changed'
[[ $(find "$test_dir/backups" -name database.sql.gz -type f | wc -l) == 8 ]] || fail 'collision published a new archive'
printf '%s\n' 'PASS: deterministic publish collision preserves the existing directory and cleans only this run staging'
chmod 400 "$test_dir/private/client.cnf"
bash "$script" "${base_args[@]}" >/dev/null
assert_clean
env ASTER_TEST_SNAPSHOT=prefix6 bash "$script" "${base_args[@]}" >/dev/null
env ASTER_TEST_SNAPSHOT=prefix7 bash "$script" "${base_args[@]}" >/dev/null
env ASTER_TEST_SNAPSHOT=prefix8 bash "$script" "${base_args[@]}" >/dev/null
env ASTER_TEST_SNAPSHOT=prefix9 bash "$script" "${base_args[@]}" >/dev/null
env ASTER_TEST_SNAPSHOT=prefix10 bash "$script" "${base_args[@]}" >/dev/null
env ASTER_TEST_SNAPSHOT=prefix11 bash "$script" "${base_args[@]}" >/dev/null
env ASTER_TEST_SNAPSHOT=prefix12 bash "$script" "${base_args[@]}" >/dev/null
python3 - "$test_dir/backups" "$test_dir/fixtures" "$script" <<'PY'
import json
from pathlib import Path
import sys
backups, fixtures, script = map(Path, sys.argv[1:])
# Load only the existing metadata grammar functions, as test_backup_native.py
# does; compare their shapes with the independent physical fixture inventory.
source = script.read_text().split('metadata() {\n  python3 - "$release_directory" "$database" "$@" <<\'PY\'\n', 1)[1].split("\nPY\n}", 1)[0]
namespace = {}
exec(compile(source.rsplit("\ntry:\n", 1)[0], str(script), "exec"), namespace)
expected = namespace["release"](fixtures / "release")
metadata = [json.loads(path.read_bytes()) for path in backups.glob("*/BACKUP.json")]
# Eight concurrent current backups + one readonly current + seven old prefixes.
assert len(metadata) == 16
assert {item["schema_version"] for item in metadata} == set(range(6, 14))
for version in range(6, 14):
    rows = [line.split("\t") for line in (fixtures / ("valid.tsv" if version == 13 else f"prefix{version}.tsv")).read_text().splitlines()]
    shape = {row[1]: [] for row in rows if row[0] == "table"}
    for row in rows:
        if row[0] == "column":
            shape[row[1]].append(row[2])
    assert shape == expected["shapes"][str(version)], f"Unexpected physical prefix {version}"
    if version < 12:
        assert "catalog_nodes" not in shape and "node_id" not in shape["plans"]
    if version == 11:
        assert len(shape) == 19
    if version == 12:
        assert len(shape) == 20
    if version < 13:
        assert not {"product_configurations", "order_product_configurations", "provisioning_jobs"}.intersection(shape)
    else:
        assert len(shape) == 23
    prefix = [item for item in metadata if item["schema_version"] == version]
    assert len(prefix) == (9 if version == 13 else 1)
    checksums = {str(number): expected["migration_checksums"][str(number)] for number in range(1, version + 1)}
    for item in prefix:
        assert item == {"schema": 1, "source_commit": "a" * 40, "core_version": "0.5.0", "database": "aster_fixture", "schema_version": version, "migration_checksums": checksums}
PY
assert_clean
printf '%s\n' 'PASS: BACKUP.json records the actual complete applied prefix, including an older schema, without inventing unapplied migrations'
env ASTER_TEST_DUMP_WARN=1 bash "$script" "${base_args[@]}" >"$test_dir/warning.stdout" 2>"$test_dir/warning.log"
assert_no_private_markers "$test_dir/warning.stdout" "$test_dir/warning.log"
python3 - "$test_dir/warning.log" <<'PYDIAGNOSTIC'
import json, sys
from pathlib import Path
value = json.loads(Path(sys.argv[1]).read_text())
assert value["event"] == "backup_warning" and value["exit_code"] == 0
assert value["reason"] == "dump_warning_raw_diagnostics_discarded"
PYDIAGNOSTIC
printf '%s\n' 'PASS: failed CLI emits one safe NDJSON event with actual status; raw stderr is discarded, including successful dump warnings'
printf '%s\n' 'PASS: read-only private config accepted. All tests use fakebin with modeled system-root ownership; no MySQL connection or native service boot was tested.'
