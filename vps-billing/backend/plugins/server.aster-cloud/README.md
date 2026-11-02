# Aster Cloud server module

`server.aster-cloud` implements `aster.server/1`. Finance supplies an approved
connection, a normalized product configuration, and an operation identity.
Only this worker loads controller credentials or speaks `aster.controller/1`
and `aster.service/1`.

Each connection addresses exactly one controller. The controller selects its
nodes and owns capacity and placement. `preset_id` selects an existing controller
preset. Optional CPU, memory, disk and bandwidth configuration describes the
target of a package change; all four are required to perform that operation.

Use `aster-server@<connection>.service`. Its private credential directory is
`/etc/aster/server/<connection>/`, containing:

- `config.json`: `{ "schema": 1, "controller_id": "primary", "endpoint": "https://controller.example/v1/provisioning", "allowed_cidrs": ["192.0.2.0/24"], "custom_ca": true }`
- `token`: controller billing token.
- `ca.pem`: custom CA when `custom_ca` is true.

The approved plugin policy must list the endpoint host and port. API users
cannot choose destinations or edit these credentials. systemd supplies copies
via `LoadCredential`; the API process cannot read the originals.

Receipts in `/var/lib/aster-server/<connection>/` are private, durable module
state. Keep them across upgrades and rollbacks. The worker acquires an exclusive
lock and syncs a claim before every mutation. On restart it only looks up an
in-flight operation. It never sends an uncertain operation again.

Creation includes image setup. A controller create is followed by one separately
identified reinstall after successful resource creation. Finance sees `accepted`
until setup succeeds. `operation_status` only reads; the worker's background loop
advances known setup stages. Generated passwords exist only in the send call;
customer checkout credentials use the existing one-way hash. Plaintext secrets
are absent from receipts and process output is discarded by systemd.

Run `go test -race ./plugins/server.aster-cloud/...` from the backend module. The
suite includes protocol conformance, process restart recovery, uncertainty,
selected images, and credential absence in durable files.
# Migrate a configured controller

Run the Core schema migrations first, stop the API, and reconcile outstanding
0.5.10 provisioning jobs with the old installation. The migration refuses any
queued or uncertain old provisioning job and any running or uncertain service
task. It never sends a controller mutation or replays an old request.

Build the host-only tool with `go build -o artifacts/migrate-controller
./plugins/server.aster-cloud/cmd/migrate`. Run as root with
`--controller-policy /etc/aster/controllers.json --controller-id primary
--connection-id primary-cloud` to print the nonsecret plan. Add
`--mysql-dsn-file /etc/aster/secrets/mysql.dsn --apply` to create the connection,
copy private credentials into `/etc/aster/server/primary-cloud/`, and migrate
explicit product and service preset bindings in one database transaction.
Each invocation selects exactly one controller. Custom legacy products need
an explicit controller preset before migration; no node selection is inferred.
Old order snapshots and dispatched requests remain unchanged for audit.

The tool does not install, approve or start a worker. Approve the resulting
server slot, test the connection, then start Core. A failed database transaction
leaves only a private prepared credential directory; inspect it before repeating
the migration. An existing connection or credential directory is never replaced.
