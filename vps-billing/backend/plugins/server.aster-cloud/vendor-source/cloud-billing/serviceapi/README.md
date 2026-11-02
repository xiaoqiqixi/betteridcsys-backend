# aster.service/1

Vendor the entire `contracts/billing` module, including `internal/controllers`.
The service client shares the existing provisioning transport: approved HTTPS
profile, fixed DNS/CIDR, verified TLS, no proxy/redirect/redial. The profile's
provisioning path is replaced with fixed `/v1/images` and `/v1/services` paths
on the same authority. Every call supplies the opaque 32-hex owner reference.

`ActionRequest.BytesAndDigest` is the canonical compact JSON and SHA-256 used
for `Idempotency-Key` and `X-Aster-Request-SHA256`. `Action` returns the first
receipt and a replay boolean (409); query `Operation` for completion. Never
retry an uncertain write automatically. A changed payload under the same key
is an idempotency conflict, not a new operation. Operation IDs are globally
unique 32-hex identifiers. Receipts are immutable, including rejections.

`Params` is a closed union (see `Params.Valid`). Reinstall requires `image_id`
and exactly one of `password` or `ssh_public_key`; rescue_enter requires an
approved rescue `image_id`. Terminate requires `confirmation = resource_id`.
Resize specifies all three final sizes; memory_mb/disk_gb use MiB/GiB, consistent
with provisioning. Optional `set_bandwidth: true` replaces both directional
limits using `bandwidth_mbps` (guest transmit) and `inbound_bandwidth_mbps`
(guest receive), 0–100000 Mbps; zero clears that direction. Omitting the flag
preserves existing limits. Snapshot create's operation ID becomes its snapshot ID;
restore/delete take `snapshot_id`. Unsuspend starts only with `start_after`.

Passwords live only in memory. Never persist/log/serialize an action request
into a job, audit trail, retry queue or exception. Persist only a redacted copy
and the digest; loss of ephemeral credentials leaves an operation uncertain,
requiring an operator decision. `Params.Redacted()` removes the password.

Read models preserve null measurements and unknown snapshot sizes. Catalogs
are arrays; all other read models follow CLOUD_DESIGN §3. Console returns an
HTTPS `/console/#ticket` URL; the page uses WSS with a 60-second single-use
ticket. Capabilities are authoritative for each controller/node combination.
