#!/usr/bin/env bash
# Linux / MySQL 8.4 native, same-host backup. No service or database mutations.
# Output is sensitive and UNENCRYPTED; see docs/BACKUP_RECOVERY.md.
set -euo pipefail
umask 077
export LC_ALL=C

# Only this script's fixed-schema diagnostics reach the original stderr. Host
# tools may print paths, SQL or credentials: never forward their raw stderr.
exec 3>&2 2>/dev/null
operation_id=unavailable phase=input_validation tool_role=builtin failure_reason=unexpected_exit staging=''
diagnostic() {
  printf '{"event":"%s","operation_id":"%s","phase":"%s","tool_role":"%s","exit_code":%s,"reason":"%s"}\n' \
    "$1" "$operation_id" "$phase" "$tool_role" "$2" "$3" >&3 || :
}
cleanup() {
  local result=$?
  trap - EXIT
  if [[ -n "$staging" ]] && ! rm -rf -- "$staging"; then
    if ((result == 0)); then result=1; phase=cleanup; tool_role=filesystem; failure_reason=cleanup_failed; fi
  fi
  if ((result != 0)); then diagnostic backup_failed "$result" "$failure_reason"; fi
  exit "$result"
}
trap cleanup EXIT
trap 'failure_reason=interrupted; exit 130' INT
trap 'failure_reason=interrupted; exit 143' TERM
if [[ -r /proc/sys/kernel/random/uuid ]]; then
  IFS= read -r operation_id < /proc/sys/kernel/random/uuid || operation_id=unavailable
  operation_id=${operation_id//-/}
fi
if [[ ! "$operation_id" =~ ^[a-f0-9]{32}$ ]]; then
  operation_id=unavailable failure_reason=operation_id_unavailable
  exit 1
fi
# Only fixed source-selected reason codes may reach the diagnostic. Never
# serialize argument values, arbitrary descriptions or underlying tool errors.
fail() {
  case "$1" in
    missing_argument|repeated_argument|unsupported_argument|invalid_database|unsupported_database|unsafe_path|required_tool|private_client_config|unsafe_output_dir|invalid_release_directory|invalid_client_options|utc_timestamp|schema_changed|empty_archive|publish_collision) failure_reason=$1 ;;
    *) failure_reason=validation_failed ;;
  esac
  exit "${2:-1}"
}
command_failed() { failure_reason=command_failed; exit "$1"; }
usage() {
  printf '%s\n' 'Usage: backup-native.sh --client-config /private/client.cnf --socket /run/mysqld/mysqld.sock --database NAME --output-dir /private/backups --release-directory /verified/assembled-release'
  printf '%s\n' 'Independently verify the assembled release first; prevent migrations and all other DDL throughout the backup window.'
}
client_config='' db_socket='' database='' output_dir='' release_directory=''
declare -A arguments=()
while (($#)); do
  case "$1" in
    --help) usage; exit 0 ;;
    --client-config|--socket|--database|--output-dir|--release-directory)
      (($# >= 2)) || fail missing_argument
      [[ ! ${arguments[$1]+set} ]] || fail repeated_argument
      arguments[$1]=1
      case "$1" in
        --client-config) client_config=$2 ;;
        --socket) db_socket=$2 ;;
        --database) database=$2 ;;
        --output-dir) output_dir=$2 ;;
        --release-directory) release_directory=$2 ;;
      esac
      shift 2 ;;
    *) usage >&2; fail unsupported_argument ;;
  esac
done
[[ -n "$client_config" && -n "$db_socket" && -n "$database" && -n "$output_dir" && -n "$release_directory" ]] || { usage >&2; fail missing_argument; }
[[ "$database" =~ ^[A-Za-z_][A-Za-z0-9_]{0,63}$ ]] || fail invalid_database
case "${database,,}" in mysql|sys|information_schema|performance_schema) fail unsupported_database ;; esac
for path in "$client_config" "$db_socket" "$output_dir" "$release_directory"; do
  [[ "$path" == /* && ! "$path" =~ [[:cntrl:]] ]] || fail unsafe_path
done
for command in mysql mysqldump gzip sha256sum mktemp stat realpath mv date cmp python3; do
  command -v "$command" >/dev/null 2>&1 || fail required_tool
done
[[ -f "$client_config" && ! -L "$client_config" && -r "$client_config" ]] || fail private_client_config
[[ $(stat -c %u -- "$client_config") == "$EUID" ]] || fail private_client_config
case "$(stat -c %a -- "$client_config")" in 400|600) ;; *) fail private_client_config ;; esac
[[ $(stat -c %h -- "$client_config") == 1 ]] || fail private_client_config
while [[ "$output_dir" != / && "$output_dir" == */ ]]; do output_dir=${output_dir%/}; done
[[ -d "$output_dir" && ! -L "$output_dir" ]] || fail unsafe_output_dir
[[ $(stat -c %u -- "$output_dir") == "$EUID" && $(stat -c %a -- "$output_dir") == 700 ]] || fail unsafe_output_dir
[[ -w "$output_dir" && -x "$output_dir" ]] || fail unsafe_output_dir
client_config=$(realpath -e -- "$client_config")
output_dir=$(realpath -e -- "$output_dir")
[[ -d "$release_directory" ]] || fail invalid_release_directory
release_directory=$(realpath -e -- "$release_directory")
# Check all canonical ancestors, not only the immediate parent. A writable
# grandparent can otherwise rename an apparently private child. Root-owned
# sticky directories (notably /tmp) are safe only because the protected child
# was already checked while walking from the inside out.
check_directory_chain() {
  local directory=$1 owner mode
  [[ -n "$directory" ]] || directory=/
  while :; do
    owner=$(stat -c %u -- "$directory")
    mode=$(stat -c %a -- "$directory")
    [[ "$owner" == "$EUID" || "$owner" == 0 ]] || fail unsafe_path
    if (( (8#$mode & 0022) != 0 )); then
      [[ "$owner" == 0 ]] && (( (8#$mode & 01000) != 0 )) || fail unsafe_path
    fi
    [[ "$directory" != / ]] || break
    directory=${directory%/*}
    [[ -n "$directory" ]] || directory=/
  done
}
check_directory_chain "${client_config%/*}"
check_directory_chain "$output_dir"
# Recheck canonical endpoints so a redirected original path cannot bypass the
# ownership/permissions checks above. This is not isolation from the same UID.
[[ -f "$client_config" && $(stat -c %u -- "$client_config") == "$EUID" && $(stat -c %h -- "$client_config") == 1 ]] || fail private_client_config
case "$(stat -c %a -- "$client_config")" in 400|600) ;; *) fail private_client_config ;; esac
[[ $(stat -c %u -- "$output_dir") == "$EUID" && $(stat -c %a -- "$output_dir") == 700 ]] || fail unsafe_output_dir

# Do not source this file or print values. Only credentials are accepted; extra
# MySQL options could silently omit tables, change targets, run SQL or hide errors.
seen_group=0
declare -A options=()
while IFS= read -r line || [[ -n "$line" ]]; do
  [[ "$line" =~ ^[[:space:]]*$ || "$line" =~ ^[[:space:]]*[#\;] ]] && continue
  if [[ "$line" =~ ^[[:space:]]*\[client\][[:space:]]*$ ]]; then
    ((seen_group == 0)) || fail invalid_client_options
    seen_group=1
  elif [[ "$line" =~ ^[[:space:]]*(user|password)[[:space:]]*=[[:space:]]*(.+)$ ]]; then
    ((seen_group == 1)) || fail invalid_client_options
    option=${BASH_REMATCH[1]}
    if [[ "$option" == user ]]; then
      [[ ${BASH_REMATCH[2]} =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}[[:space:]]*$ ]] || fail invalid_client_options
    fi
    [[ ! ${options[$option]+set} ]] || fail invalid_client_options
    options[$option]=1
  else
    fail invalid_client_options
  fi
done < "$client_config"
[[ ${options[user]+set} ]] || fail invalid_client_options
unset line

phase=staging tool_role=filesystem
stamp=$(date -u +%Y%m%dT%H%M%SZ)
[[ "$stamp" =~ ^[0-9]{8}T[0-9]{6}Z$ ]] || fail utc_timestamp
staging=$(mktemp -d "$output_dir/.aster-$database-$stamp-XXXXXXXX.partial")
# A default ACL can override umask when files are created. Clear it only on
# this new private staging directory before writing any backup contents.
phase=staging_privacy tool_role=python
if python3 - "$staging" 2>/dev/null <<'PY'
import errno
import os
import stat
import sys

try:
    descriptor = os.open(sys.argv[1], os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        info = os.fstat(descriptor)
        if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
            raise OSError("Unexpected private staging directory metadata")
        absent = {errno.ENODATA, errno.EOPNOTSUPP}
        try:
            os.removexattr(descriptor, "system.posix_acl_default")
        except OSError as failure:
            if failure.errno not in absent:
                raise
        try:
            os.getxattr(descriptor, "system.posix_acl_default")
        except OSError as failure:
            if failure.errno not in absent:
                raise
        else:
            raise OSError("Private staging directory still has a default ACL")
        os.fchmod(descriptor, 0o700)
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
except OSError:
    raise SystemExit(1) from None
PY
then :
else
  command_failed "$?"
fi
name=${staging##*/}
final="$output_dir/${name#.}"
final=${final%.partial}

# The operator supplies an ALREADY independently verified assembled release.
# BUILD/source_files bind its local layout, not its publisher authenticity:
# current BUILD schemas do not carry migration hashes or source blob records.
# Re-read the local manifest/SQL at each observation to reject a changed input.
metadata() {
  python3 - "$release_directory" "$database" "$@" <<'PY'
import hashlib
import json
from pathlib import Path
import re
import sys

class Rejected(Exception):
    pass

def require(ok, message):
    if not ok:
        raise Rejected(message)

def pairs(items):
    result = {}
    for key, value in items:
        require(key not in result, "Duplicate release metadata keys are unsupported.")
        result[key] = value
    return result

def read(path, limit=1 << 20):
    require(path.is_file() and not path.is_symlink() and 0 < path.stat().st_size <= limit,
            "Release and schema metadata must be bounded regular files without symlinks.")
    raw = path.read_bytes()
    require(0 < len(raw) <= limit, "Release or schema metadata exceeds its size bound.")
    return raw

def definitions(raw):
    # Only the shipped migration grammar is supported. Do not execute SQL or
    # mistake commas inside type/constraint expressions for column boundaries.
    result, start, depth, quoted = [], 0, 0, False
    for index, char in enumerate(raw):
        if char == "'":
            quoted = not quoted
        elif not quoted:
            depth += (char == "(") - (char == ")")
            require(depth >= 0, "Unsupported migration definition.")
            if char == "," and depth == 0:
                result.append(raw[start:index].strip())
                start = index + 1
    require(not quoted and depth == 0, "Unsupported migration definition.")
    return result + [raw[start:].strip()]

def extend_shape(shape, sql):
    for statement in sql.split(";"):
        statement = statement.strip()
        if not statement:
            continue
        create = re.fullmatch(r"CREATE TABLE IF NOT EXISTS ([a-z_][a-z0-9_]*)\s*\((.*)\)\s*ENGINE=InnoDB", statement, re.S)
        add = re.fullmatch(r"ALTER TABLE ([a-z_][a-z0-9_]*) ADD COLUMN ([a-z_][a-z0-9_]*)\s+.+", statement, re.S)
        other = re.fullmatch(r"ALTER TABLE ([a-z_][a-z0-9_]*) (?:ADD CONSTRAINT [a-z_][a-z0-9_]* CHECK\s*\(.+\)|DROP CHECK [a-z_][a-z0-9_]*)", statement, re.S)
        insert = re.match(r"INSERT IGNORE INTO ([a-z_][a-z0-9_]*)\s*\(", statement)
        if create:
            table, body = create.groups()
            require(table not in shape, "Duplicate migration table definition.")
            columns = []
            for definition in definitions(body):
                if re.match(r"(?:PRIMARY|FOREIGN|KEY|UNIQUE|CHECK|CONSTRAINT)\b", definition):
                    continue
                column = re.fullmatch(r"([a-z_][a-z0-9_]*)\s+.+", definition, re.S)
                require(column is not None and column[1] not in columns, "Unsupported migration column definition.")
                columns.append(column[1])
            require(columns, "Empty migration table definition.")
            shape[table] = columns
        elif add:
            table, column = add.groups()
            require(table in shape and column not in shape[table], "Unknown or repeated migration column.")
            shape[table].append(column)
        elif index := re.fullmatch(r"CREATE INDEX ([a-z_][a-z0-9_]*) ON ([a-z_][a-z0-9_]*)\(([a-z_][a-z0-9_]*(?:,[a-z_][a-z0-9_]*)*)\)", statement):
            _, table, columns = index.groups()
            require(table in shape and all(column in shape[table] for column in columns.split(",")), "Index migration refers to an unknown table or column.")
        elif other or insert:
            require((other or insert)[1] in shape, "Unknown migration table.")
        elif modify := re.fullmatch(r"ALTER TABLE plans MODIFY COLUMN (type_id|region_id|node_id) VARCHAR\(64\) CHARACTER SET ascii COLLATE ascii_bin NOT NULL", statement):
            require("plans" in shape and modify[1] in shape["plans"], "Taxonomy migration modifies an unknown column.")
        elif relation := re.fullmatch(r"ALTER TABLE plans ADD CONSTRAINT plans_catalog_(type|region|node) FOREIGN KEY\((type_id|region_id|node_id)\) REFERENCES (catalog_types|catalog_regions|catalog_nodes)\(id\)", statement):
            kind, column, target = relation.groups()
            require((kind, column, target) in {("type", "type_id", "catalog_types"), ("region", "region_id", "catalog_regions"), ("node", "node_id", "catalog_nodes")} and
                    "plans" in shape and column in shape["plans"] and target in shape and "id" in shape[target], "Invalid taxonomy foreign key migration.")
        elif " ".join(statement.split()) in {
            "INSERT INTO catalog_types(id,name,sort_order,enabled) VALUES('generic','通用服务器',0,TRUE) ON DUPLICATE KEY UPDATE id=id",
            "INSERT INTO catalog_regions(id,type_id,name,sort_order,enabled) SELECT CONCAT('r-',LEFT(SHA2(MIN(BINARY region),256),60)),'generic',MIN(region),0,TRUE FROM plans GROUP BY BINARY region ON DUPLICATE KEY UPDATE id=catalog_regions.id",
            "INSERT INTO catalog_taxonomy_settings(id,revision) VALUES(1,1) ON DUPLICATE KEY UPDATE id=id",
            "UPDATE plans SET type_id='generic',region_id=CONCAT('r-',LEFT(SHA2(BINARY region,256),60)) WHERE type_id IS NULL OR region_id IS NULL",
        }:
            require(all(table in shape for table in ("catalog_types", "catalog_regions", "catalog_taxonomy_settings", "plans")) and
                    all(column in shape["plans"] for column in ("region", "type_id", "region_id")), "Taxonomy initialization requires its reviewed physical schema.")
        elif " ".join(statement.split()) in {
            "INSERT INTO catalog_nodes(id,region_id,name,sort_order,enabled) SELECT CONCAT('n-',LEFT(SHA2(BINARY id,256),60)),id,'默认节点',0,TRUE FROM catalog_regions ON DUPLICATE KEY UPDATE id=catalog_nodes.id",
            "UPDATE plans SET node_id=CONCAT('n-',LEFT(SHA2(BINARY region_id,256),60)) WHERE node_id IS NULL",
        }:
            require(all(table in shape for table in ("catalog_nodes", "catalog_regions", "plans")) and
                    all(column in shape["plans"] for column in ("region_id", "node_id")) and
                    all(column in shape["catalog_nodes"] for column in ("id", "region_id", "name", "sort_order", "enabled")), "Node initialization requires its reviewed physical schema.")
        else:
            raise Rejected("Migration grammar needs a separately reviewed backup procedure.")

def release(root):
    require(not (root / "manifests").is_symlink() and not (root / "migrations").is_symlink(), "Release metadata directories must not be symlinks.")
    build_raw = read(root / "manifests/BUILD.json")
    build = json.loads(build_raw, object_pairs_hook=pairs)
    require(isinstance(build, dict) and type(build.get("schema")) is int and build["schema"] in {2, 3}, "Unsupported release BUILD schema.")
    require((build["schema"] == 3 and build.get("distribution_format") == "raw-binaries-v1") or
            (build["schema"] == 2 and "distribution_format" not in build), "Release distribution format does not match BUILD schema.")
    versions = build.get("component_versions")
    require(build.get("release_kind") == "core" and build.get("target") == "linux/amd64" and build.get("cgo_enabled") is False and
            isinstance(build.get("source_commit"), str) and re.fullmatch(r"[a-f0-9]{40}", build["source_commit"]) and
            isinstance(versions, dict) and isinstance(versions.get("core"), str) and
            re.fullmatch(r"(?:0|[1-9][0-9]{0,5})(?:\.(?:0|[1-9][0-9]{0,5})){2}", versions["core"]) and
            build.get("release_label") == versions["core"], "Release core identity is incomplete or invalid.")
    copies = build.get("source_files")
    require(isinstance(copies, dict) and 1 <= len(copies) <= 512 and all(isinstance(k, str) and isinstance(v, str) for k, v in copies.items()), "Release source file inventory is invalid.")
    paths = sorted((root / "migrations").iterdir())
    require(0 < len(paths) <= 64, "Release requires a nonempty bounded SQL migration inventory.")
    hashes, shapes = {}, {}
    shape = {"schema_migrations": ["version", "checksum", "applied_at"]}
    for number, path in enumerate(paths, 1):
        require(re.fullmatch(r"[0-9]{3}_[a-z0-9_]+\.sql", path.name) and int(path.name[:3]) == number,
                "Release SQL migrations must form an unchanged numbered prefix beginning at 001.")
        destination = "migrations/" + path.name
        source = "vps-billing/backend/" + destination
        require(copies.get(destination) == source, "Release migration source mapping is missing or invalid.")
        raw = read(path)
        checksum = hashlib.sha256(raw).hexdigest()
        # If a BUILD includes this source in its input digest records, require
        # the file to match. Current official manifests map paths only.
        inputs = build.get("input_sha256", {})
        require(isinstance(inputs, dict) and (source not in inputs or inputs[source] == checksum), "Release migration source digest mismatch.")
        extend_shape(shape, raw.decode("utf-8"))
        hashes[str(number)] = checksum
        shapes[str(number)] = {table: list(columns) for table, columns in shape.items()}
    require({key for key in copies if key.startswith("migrations/")} == {"migrations/" + path.name for path in paths}, "Release SQL inventory differs from its source mapping.")
    return {"build_sha256": hashlib.sha256(build_raw).hexdigest(), "source_commit": build["source_commit"],
            "core_version": versions["core"], "migration_checksums": hashes, "shapes": shapes}

def validate(expected, snapshot, database):
    lines = [line.split("\t") for line in read(snapshot).decode("utf-8").splitlines()]
    groups = {}
    for fields in lines:
        require(fields[0] in {"identity", "unsupported", "migration", "table", "column", "constraint", "index", "key", "reference"}, "Malformed database schema metadata.")
        require(len(fields) == {"identity": 5, "unsupported": 2, "migration": 3, "table": 4, "column": 12,
                                "constraint": 6, "index": 11, "key": 9, "reference": 9}[fields[0]], "Malformed database schema metadata.")
        groups.setdefault(fields[0], []).append(fields[1:])
    identity = groups.get("identity", [])
    require(len(identity) == 1 and len(identity[0]) == 4, "Database server identity is missing or malformed.")
    version, vendor, actual_database, server_uuid = identity[0]
    require(re.fullmatch(r"8\.4\.[0-9]+(?:[-+][A-Za-z0-9.+~_-]+)?", version) and
            re.fullmatch(r"MySQL (?:Community|Enterprise) Server(?: - (?:GPL|Commercial|Advanced Edition))?", vendor),
            "Only the reviewed Oracle MySQL 8.4 server family is supported.")
    require(actual_database == database and re.fullmatch(r"[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}", server_uuid), "Database or server identity does not match the requested target.")
    require(groups.get("unsupported") == [["0"]], "Only InnoDB base tables without custom database objects are supported.")
    history = groups.get("migration", [])
    require(0 < len(history) <= len(expected["migration_checksums"]), "Database migration history must be a nonempty release prefix.")
    migrations = {}
    for number, fields in enumerate(history, 1):
        require(len(fields) == 2 and fields[0] == str(number) and fields[1] == expected["migration_checksums"][str(number)],
                "Database migrations must match an unchanged contiguous release SQL prefix.")
        migrations[str(number)] = fields[1]
    shape = expected["shapes"][str(len(history))]
    require(groups.get("table") == [[name, "BASE TABLE", "InnoDB"] for name in sorted(shape)], "Unknown or partial database table inventory.")
    columns = groups.get("column", [])
    require(all(len(fields) == 11 for fields in columns) and [[fields[0], fields[1]] for fields in columns] ==
            [[table, column] for table in sorted(shape) for column in shape[table]], "Unknown or partial database column inventory.")
    return {"schema": 1, "source_commit": expected["source_commit"], "core_version": expected["core_version"],
            "database": database, "schema_version": len(history), "migration_checksums": migrations}

try:
    root, database, action, prepared, *rest = sys.argv[1:]
    expected = release(Path(root))
    if action == "prepare":
        Path(prepared).write_text(json.dumps(expected, sort_keys=True) + "\n")
    else:
        require(action == "validate" and len(rest) == 2 and json.loads(read(Path(prepared))) == expected, "Release metadata changed during backup.")
        snapshot, output = rest
        metadata = validate(expected, Path(snapshot), database)
        Path(output).write_text(json.dumps(metadata, sort_keys=True, indent=2) + "\n")
except Rejected as error:
    print("Backup failed: " + str(error), file=sys.stderr)
    sys.exit(1)
except (OSError, ValueError, TypeError, KeyError, UnicodeError):
    print("Backup failed: Release or database metadata could not be read or validated.", file=sys.stderr)
    sys.exit(1)
PY
}
phase=release_metadata tool_role=metadata_validator
metadata prepare "$staging/release.json"

# --defaults-file must be first. Explicit socket transport and disabling login
# paths prevent unrelated personal settings from selecting another server.
# Only the current project schema (InnoDB base tables) is supported. Refuse
# custom objects instead of omitting them silently or exporting qualified SQL
# and DROP statements. The backup role must have the metadata visibility
# documented in BACKUP_RECOVERY.md, including SHOW_ROUTINE.
schema_query="SELECT 'identity', VERSION(), @@version_comment, DATABASE(), @@server_uuid;
SELECT 'unsupported',
  (SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND (TABLE_TYPE <> 'BASE TABLE' OR COALESCE(ENGINE, '') <> 'InnoDB')) +
  (SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=DATABASE()) +
  (SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA=DATABASE()) +
  (SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA=DATABASE());
SELECT 'migration', version, checksum FROM schema_migrations ORDER BY version;
SELECT 'table', TABLE_NAME, TABLE_TYPE, COALESCE(ENGINE,'') FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME;
SELECT 'column', TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COALESCE(HEX(COLUMN_DEFAULT),'NULL'), HEX(EXTRA), HEX(GENERATION_EXPRESSION), COALESCE(CHARACTER_SET_NAME,''), COALESCE(COLLATION_NAME,''), ORDINAL_POSITION, COLUMN_KEY FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME, ORDINAL_POSITION;
SELECT 'constraint', tc.TABLE_NAME, tc.CONSTRAINT_NAME, tc.CONSTRAINT_TYPE, tc.ENFORCED, COALESCE(HEX(cc.CHECK_CLAUSE),'') FROM information_schema.TABLE_CONSTRAINTS tc LEFT JOIN information_schema.CHECK_CONSTRAINTS cc ON tc.CONSTRAINT_SCHEMA=cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME=cc.CONSTRAINT_NAME WHERE tc.CONSTRAINT_SCHEMA=DATABASE() ORDER BY tc.TABLE_NAME, tc.CONSTRAINT_NAME;
SELECT 'index', TABLE_NAME, INDEX_NAME, NON_UNIQUE, SEQ_IN_INDEX, COALESCE(COLUMN_NAME,''), COALESCE(COLLATION,''), COALESCE(SUB_PART,0), INDEX_TYPE, IS_VISIBLE, COALESCE(HEX(EXPRESSION),'') FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX;
SELECT 'key', TABLE_NAME, CONSTRAINT_NAME, COLUMN_NAME, ORDINAL_POSITION, COALESCE(POSITION_IN_UNIQUE_CONSTRAINT,0), COALESCE(REFERENCED_TABLE_SCHEMA,''), COALESCE(REFERENCED_TABLE_NAME,''), COALESCE(REFERENCED_COLUMN_NAME,'') FROM information_schema.KEY_COLUMN_USAGE WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME, CONSTRAINT_NAME, ORDINAL_POSITION;
SELECT 'reference', TABLE_NAME, CONSTRAINT_NAME, UNIQUE_CONSTRAINT_SCHEMA, UNIQUE_CONSTRAINT_NAME, MATCH_OPTION, UPDATE_RULE, DELETE_RULE, REFERENCED_TABLE_NAME FROM information_schema.REFERENTIAL_CONSTRAINTS WHERE CONSTRAINT_SCHEMA=DATABASE() ORDER BY TABLE_NAME, CONSTRAINT_NAME;"
capture_schema() {
  tool_role=mysql
  if (
    unset MYSQL_PWD MYSQL_HOST MYSQL_TCP_PORT MYSQL_UNIX_PORT MYSQL_GROUP_SUFFIX MYSQL_TEST_LOGIN_FILE
    mysql --defaults-file="$client_config" --no-login-paths \
      --protocol=SOCKET --socket="$db_socket" --batch --skip-column-names \
      --execute="$schema_query" "$database" 2>"$staging/schema.stderr"
  ) > "$1"; then :
  else
    command_failed "$?"
  fi
}
phase=schema_before
capture_schema "$staging/schema.before"
phase=schema_validate_before tool_role=metadata_validator
metadata validate "$staging/release.json" "$staging/schema.before" "$staging/BACKUP.json"
# No --databases: no generated CREATE DATABASE or USE source-database clause.
phase=dump tool_role=mysqldump
if (
  unset MYSQL_PWD MYSQL_HOST MYSQL_TCP_PORT MYSQL_UNIX_PORT MYSQL_GROUP_SUFFIX MYSQL_TEST_LOGIN_FILE
  mysqldump --defaults-file="$client_config" --no-login-paths \
    --protocol=SOCKET --socket="$db_socket" \
    --single-transaction --quick --skip-routines --skip-events --skip-triggers \
    --no-tablespaces --set-gtid-purged=OFF --hex-blob \
    --default-character-set=utf8mb4 --skip-add-drop-table --skip-add-locks \
    "$database" 2>"$staging/dump.stderr"
) | gzip -n -c > "$staging/database.sql.gz" 2>"$staging/gzip.stderr"; then :
else
  pipeline_status=("${PIPESTATUS[@]}")
  # Never echo server/config diagnostics or a partially successful SQL dump.
  if ((pipeline_status[1] != 0)); then tool_role=gzip; command_failed "${pipeline_status[1]}"; fi
  command_failed "${pipeline_status[0]}"
fi
phase=schema_after
capture_schema "$staging/schema.after"
phase=schema_validate_after tool_role=metadata_validator
metadata validate "$staging/release.json" "$staging/schema.after" "$staging/BACKUP.after.json"
phase=schema_stability tool_role=filesystem
cmp --silent "$staging/schema.before" "$staging/schema.after" && cmp --silent "$staging/BACKUP.json" "$staging/BACKUP.after.json" || fail schema_changed
# This detects observed changes; it does not lock out concurrent migrations or
# DDL that changes and changes back between observations. The operator must
# prevent migrations/DDL for the entire backup window.
[[ -s "$staging/database.sql.gz" ]] || fail empty_archive
phase=archive_validation tool_role=gzip
if gzip -t "$staging/database.sql.gz" 2>"$staging/gzip.stderr"; then :
else
  command_failed "$?"
fi
# Preserve a non-sensitive warning indicator rather than possibly secret stderr.
warning=no
[[ ! -s "$staging/dump.stderr" ]] || warning=yes
phase=finalize_metadata tool_role=filesystem
rm -- "$staging/dump.stderr" "$staging/gzip.stderr" "$staging/schema.stderr" "$staging/release.json" "$staging/schema.before" "$staging/schema.after" "$staging/BACKUP.after.json"
printf 'format=aster-native-logical-v1\ncreated_utc=%s\ndatabase=%s\ntransport=local-unix-socket\ndump_warnings=%s\nencrypted=no\n' "$stamp" "$database" "$warning" > "$staging/BACKUP.txt"
phase=checksum_creation tool_role=sha256sum
(cd "$staging" && sha256sum database.sql.gz BACKUP.txt BACKUP.json > SHA256SUMS)
phase=checksum_validation
(cd "$staging" && sha256sum --check --status SHA256SUMS)
# Publish the complete inventory as one directory on the same filesystem. GNU mv -n
# may report success on collision, so also prove that the staging name vanished.
phase=publish tool_role=filesystem
mv --no-clobber --no-target-directory -- "$staging" "$final"
[[ ! -e "$staging" ]] || fail publish_collision
staging=''
printf 'Created %s (unencrypted). Verify, encrypt and copy off-host; this is not a restore test.\n' "$final"
if [[ "$warning" != no ]]; then phase=dump; tool_role=mysqldump; diagnostic backup_warning 0 dump_warning_raw_diagnostics_discarded; fi
