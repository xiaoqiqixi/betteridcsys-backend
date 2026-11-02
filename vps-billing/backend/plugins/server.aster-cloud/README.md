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
