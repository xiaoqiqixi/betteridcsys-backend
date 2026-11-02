# Commerce operations

The [OpenAPI contract](../contract/openapi.json) defines the HTTP API. Production
uses 0.6 by default. Legacy callers can send `X-Aster-API-Version: 0.5` for one
release; a deployment may set `COMMERCE_API_VERSION=0.5` during frontend migration.
The old `/plans`, `/admin/catalog`, legacy order body and root provisioning CLI
remain available. Apply the complete embedded migrations before starting Core.
Deprecated checkouts share finite inventory with their migrated products. Their
payment and provisioning formats remain compatible; cancelling one through the
current API preserves its legacy fulfillment checks and invoice void state.

## Approved controller access

`SERVICE_CONTROLLER_POLICY_FILE` is optional. Without it, cloud capabilities
remain false and controller action endpoints return `501 FEATURE_UNAVAILABLE`.
No browser or catalog field can supply an endpoint, token, CA or credential path.

The policy is a root-owned JSON file (0600 or 0644, no group/other write permission)
under protected ancestors. The schema matches the existing controller policy:

```json
{
  "schema": 1,
  "controllers": [{
    "id": "primary",
    "enabled": true,
    "endpoint": "https://controller.example.test/v1/billing/provision",
    "allowed_cidrs": ["192.0.2.0/24"],
    "token_file": "/run/credentials/aster-api.service/cloud-primary-token",
    "ca_file": "/etc/aster/controller-ca.pem"
  }]
}
```

Use the actual approved provisioning path, DNS name and narrow CIDRs. The service
SDK derives fixed `/v1/images` and `/v1/services` routes on that same authority.
Tokens must be private files owned by the API service UID, supplied by the host
(for example through systemd `LoadCredential`). CA files may be root-owned or
service-owned; an empty `ca_file` uses the system roots. Links, extra hard links,
shared-writable ancestors and oversized files are rejected. Restart the API to
load changed credentials. The root CLI keeps its separate private policy and
ownership rules. This change does not modify deployment units or host files.

Transport revalidates DNS against the approved CIDRs on every connection,
verifies TLS, and disallows proxies, redirects and mutation redial. The complete
upstream SDK and SHA-256 provenance are under `contract/cloud-billing` and
`contract/CLOUD_SOURCE.json`.

## Automatic and manual execution

The in-process scheduler registers all commerce jobs through
`RegisterAutomationJob`; account/support jobs share that registry. Each minute
uses a dedicated MySQL connection for `GET_LOCK` and a unique execution key in
`automation_runs`. A controller failure affects that object, with bounded batches
and a failed count in the run history.

`auto_provision` controls new automatic provisioning. Other queued lifecycle
actions and read-only reconciliation continue when it is off. Only paid orders
create service rows, one per quantity unit. An opaque service ID is used as the
controller order reference so units in the same checkout cannot collide.

Cloud products require an approved controller ID and remote preset ID. The
service stores the purchased configuration snapshot. When an OS option is
selected, provisioning is followed by a separate reinstall operation to apply
that image and the owned SSH key. Until it completes, the service stays
`provisioning`. Each phase is durable; a lost response is never a reason to send
it again. Without a selected OS, the approved remote preset controls the image.
No password from an order is retained for asynchronous payment or provisioning.

New images are synchronized from the approved controller catalog, one controller
per scheduler tick. New rows start disabled; administrators enable them and
attach them to products. Sync preserves existing enable/disable decisions and
historical image rows. Use a required OS option on products that accept SSH keys.

A service task is recorded and committed before a mutation is sent. Its operation
ID remains fixed. Accepted operations are polled read-only. A lost commit
acknowledgement, response, process or credential becomes a visible uncertain
state; dispatch does not resend running or uncertain tasks. `sync` in
`POST /admin/services/{id}/actions` performs lookup only. Repeating it does not
repeat notifications or financial effects. A new explicit provision command may
retry a definitively failed provisioning operation with a new ID; ambiguous
operations must be reconciled first.

The existing root commands accept both legacy provisioning job IDs and commerce
service task IDs:

```text
aster-api -dispatch-provisioning <task_id>
aster-api -reconcile-provisioning <task_id>
```

They retain `PROVISIONING_MYSQL_DSN_FILE`, `PROVISIONING_POLICY_FILE` and the
existing transport selection. Resource-plugin mode remains available for legacy
provisioning; it does not silently fall back to HTTPS for service actions.
Manual generic services are confirmed through the admin provision/suspend/
unsuspend/terminate actions. Controller-backed service state changes only after
confirmed completion. Only overdue suspensions are automatically lifted by a
renewal payment, including a payment racing an already dispatched suspension.

The first idempotent action response identifies the durable task. Fetch the
service to see its current task status. A successful submission is not evidence
that the controller completed the operation. Power state and capabilities come
from controller observation, and missing metrics or snapshot sizes remain null.

## Protocol limits requiring upstream coordination

The pinned SDK supports one SSH public key per reinstall. Multiple keys cannot
be silently dropped. Root passwords at checkout return
`PASSWORD_REQUIRES_DIRECT_ACTION`; use an owned SSH key and set a password through
the direct reset-password endpoint after activation. Direct password-bearing
actions send the password only in memory over TLS. SQL, idempotency records,
audit and logs contain no plaintext password.

The frontend rescue request requires a root password, while this SDK's rescue
request accepts only a rescue image ID. Rescue capabilities therefore remain
false and rescue endpoints return `501 FEATURE_UNAVAILABLE`. Supporting this
requires an aligned frontend/cloud protocol; the backend does not pretend to
apply an unsupported password. Other capabilities are enabled only when the
controller reports them.

Original-channel refunds depend on the selected gateway's refund capability.
Ambiguous original refunds remain reserved for operator/provider reconciliation;
they are never automatically dispatched again. Refund-to-credit remains entirely
transactional. Infrastructure acceptance for an upgrade from a protected 0.5
installation requires the separately configured CI legacy baseline.
