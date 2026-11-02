# Commerce operations

The [vendored OpenAPI contract](../contract/openapi.json) defines Core 0.6 HTTP
behavior. Apply all embedded migrations before starting Core. The old `/plans`,
`/admin/catalog` and root provisioning commands remain available for the migration
period; their product bindings use the module-independent representation.

## Approved module connections

See [SERVER_MODULES.md](SERVER_MODULES.md) for module installation and approval.
Core uses `PLUGIN_DIRECTORY` and `PLUGIN_POLICY_FILE`; each approved `servers[]`
slot is one connection to one controller. A product binds directly to that
connection. Placement, nodes and capacity belong to the controller. There are no
finance connection groups or placement policies.

Only the worker reads its connection credentials through systemd. The finance
API cannot supply an endpoint, token, CA or credential path in a product or order.
The Aster Cloud plugin owns the pinned controller/service SDK and transports;
its provenance is under `backend/plugins/server.aster-cloud/vendor-source/`.

## Dispatch and reconciliation

The scheduler registers commerce jobs through `RegisterAutomationJob` and uses
MySQL locks and unique execution keys. `auto_provision` controls new automatic
provisioning. Existing tasks and read-only reconciliation continue when it is off.
Only paid orders create services, one per quantity unit. Services retain their
purchased module configuration and direct connection even after catalog edits.

A module task is committed before its single mutation attempt. Accepted tasks
are polled through `operation_status`. Lost responses, process failures or lost
commit acknowledgements remain uncertain; neither dispatch nor a retrying HTTP
client resends the mutation. Administrator `sync` performs read-only lookup.
Definitively failed provisioning may receive a new explicit operation; when an
allocation exists it is retained and passed to the module for completion.

For Aster Cloud, selected-image initialization is a second durable module stage.
The service becomes active only after that stage completes. Generated passwords
exist only in the send call. Checkout accepts the existing one-way password hash
or owned SSH keys; plaintext never enters task payloads, purchase snapshots,
idempotency records, audit events or logs. Custom action password fields are
split into memory-only `Request.Secrets`. Module-specific checkout secrets cannot
be retained across asynchronous payment; supply them through a direct action.

Images synchronize by connection. New images start disabled; administrators
review and enable them before attaching them to products. Existing enable states
and historical image references survive synchronization.

Manual products have `module: null`. Staff enter hostname and IP addresses using
`manual_info`, then confirm provision/suspend/unsuspend/terminate. Module-backed
services reject these manual information overrides. Module actions update finance
state only after confirmed completion. Renewal payments lift only overdue
suspensions, including payments racing an in-flight suspension.

Root commands accept service task IDs and the retained legacy job IDs:

```text
aster-api -dispatch-provisioning <task_id>
aster-api -reconcile-provisioning <task_id>
```

Set `PROVISIONING_MYSQL_DSN_FILE`, `PROVISIONING_PLUGIN_ROOT`,
`PROVISIONING_PLUGIN_POLICY_FILE`, and optionally `PROVISIONING_TRANSPORT=server`.
The old controller policy and direct HTTPS/resource transports are rejected.
Use the plugin migration tool to convert a configured 0.5.10 controller into one
connection, after reconciling its outstanding jobs.

## Capability and acceptance limits

Power, console, metrics, snapshots and custom actions come from live module
status. Missing metrics and snapshot sizes remain null. The first idempotent
HTTP response identifies the task; it does not claim provider completion.

The pinned Aster SDK accepts one SSH public key per reinstall. It does not expose
the finance rescue-password operation, so this module advertises rescue as false
and the endpoint returns `501 FEATURE_UNAVAILABLE`. Other modules can implement
the standard rescue method. Original-channel refunds remain subject to the
selected payment gateway's capability and separate uncertainty reconciliation.

`Server modules` CI exercises Linux socket/UID isolation and a paid order against
a controller built from cloud main with its fake node runner. It records the
exact cloud commit and allocation/power-cycle evidence. `Commerce core` runs
real MySQL lifecycle, migration, secrets and contract tests. `Order to VM` checks
real libvirt behavior separately. Protected 0.5 upgrade/restore acceptance still
requires the configured legacy baseline; none of these tests replaces that gate.
