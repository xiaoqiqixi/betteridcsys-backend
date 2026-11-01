# Payment plugin author guide — Core 0.6

Each payment gateway is a named slot containing an approved independent worker.
Multiple slots can use different plugins or different merchant accounts with the
same plugin. Adding a compatible gateway requires no Core or frontend changes.
Core owns invoices, attempts, integer money, authorization and settlement. Workers
receive payment snapshots and return checkout data or authenticated evidence;
they have no database, provisioning or accounting interface.

## SDK and protocol

The Go SDK is `backend/pkg/pluginapi/paymentv1` and `backend/pkg/pluginserver`.
Start with `backend/plugins/payment-template/`. To use it in a separate module,
pin a reviewed backend source commit and add a local module replacement:

```sh
go mod edit -require=aster/billing@v0.0.0
go mod edit -replace=aster/billing=/absolute/path/to/reviewed/backend
```

Implement `paymentv1.Processor.Create` and `Verify`. Optional `Querier.Query` and
`Refunder.Refund` must match advertised capabilities. The Linux command calls
`pluginserver.RunPayment`; it supplies the handshake, peer UID checks, bounded
HTTP-over-Unix-socket RPC and shutdown. `ReadPaymentCredential` reads worker-only
files from systemd. Other languages can implement the same strict JSON protocol;
see [PLUGIN_PROTOCOL.md](PLUGIN_PROTOCOL.md).

| RPC | Hook and result |
| --- | --- |
| `/v1/handshake` | Match protocol, plugin/version, executable hash and nonce. |
| `/v1/payment/checkout` | `Create(config, intent)` → `checkout`. Use the attempt ID as the provider idempotency key. |
| `/v1/payment/verify` | `Verify(config, callback)` → complete `VerifiedEvent`, after authenticating the raw body. |
| `/v1/payment/query` | Optional `Query(config, {intent, session_id})` → pending/expired/failed, or succeeded with complete evidence. |
| `/v1/payment/refund` | Optional `Refund(config, request)` → refund ID and pending/succeeded/failed. Core supplies a separate durable idempotency key. |

Every operation is bound to `config.provider_id` (plugin ID), `gateway_id` (slot),
`merchant_id` and `manifest_sha256`. Echo the slot in checkout and verified events.
The server handles version, nonce, timeout and fixed error envelopes. Calls are
bounded to 12 seconds, with a two-second handshake; Core never retries a payment
RPC automatically. Each slot has its own concurrency limit and circuit breaker.

## Manifest

This is a structural example; replace the digest with the actual executable hash.

```json
{
  "schema": 1,
  "id": "payment.example",
  "version": "1.0.0",
  "protocol": "aster.payment/1",
  "core_min": "0.6.0",
  "core_max_exclusive": "0.7.0",
  "capabilities": ["payment.checkout", "payment.verify_callback", "payment.query"],
  "executable": "plugin",
  "sha256": "REPLACE_WITH_64_LOWERCASE_HEX_DIGITS",
  "payment": {
    "name": {"en": "Example", "zh-CN": "示例"},
    "description": {"en": "Pay using your card"},
    "flow": "redirect",
    "currencies": [{"code": "USD", "precision": 2}],
    "checkout_hosts": ["checkout.example.com"],
    "fee": {"percent_bp": 100, "fixed": [{"currency": "USD", "amount_minor": 20}]},
    "min": [{"currency": "USD", "amount_minor": 100}],
    "max": [{"currency": "USD", "amount_minor": 1000000}]
  }
}
```

Metadata lives under `payment`. Names/descriptions support `en` and `zh-CN`.
Optional `logo` is a base64 PNG or SVG data URI, at most 16 KiB including the
MIME prefix and base64 encoding. Core
decodes PNGs (maximum 1024×1024) and accepts only passive SVG elements/attributes;
scripts, external references, CSS, animation and entities are rejected. Text is
plain text. Limits and fee entries use supported currencies and integer minor
units. Fees are display metadata; they do not increase the stored invoice amount.

Declare up to 32 currencies with explicit precision (0–6) and up to eight exact
checkout hosts. No rounding or conversion is permitted. The first two capabilities
remain `payment.checkout`, `payment.verify_callback`; append `payment.query` and/or
`payment.refund` only when implemented. Missing `flow` retains legacy redirect
behavior. Compatibility bounds are enforced: manifests with
`core_max_exclusive <= 0.6.0` remain refused, including already installed binaries.

## Checkout flows

Every checkout carries `provider_id`, `gateway_id`, `session_id`, `flow`,
`checkout_url` and `expires_at`; unused legacy URL/time fields serialize as empty
string/zero timestamp. Return only the fields belonging to the declared flow.

| Flow | Additional fields and validation |
| --- | --- |
| `redirect` | `checkout_url`: HTTPS, exact approved DNS host, no credentials or port. Explicit Core 0.6 redirect flows preserve provider fragments such as Stripe's; legacy declarations still reject fragments. |
| `qr` | `qr_payload`: plain text, at most 2048 characters. Core/frontend render it as QR data, never HTML. |
| `instructions` | `instructions`: plain text, at most 8000 characters; `reference`: at most 120 characters. Never return HTML or secrets. |

Online checkout expiry must be in the next 24 hours. Instructions may have no
expiry; the public API then returns `expires_at: null`. `Create` must be idempotent
for the supplied attempt ID. A timeout means uncertainty, not failure to charge.
Query success must return evidence identical to a callback for the same payment,
including stable event/transaction IDs and the provider's settlement timestamp.

## Core HTTP API and accounting

`GET /api/v1/payment-gateways?currency=USD` returns only enabled slots whose current
approved worker passes its handshake. Customers call
`POST /api/v1/invoices/{id}/pay` with `{"method":"gateway","gateway_id":"card-us"}`
and an `Idempotency-Key`. Core stores the attempt before contacting the worker.
`GET /api/v1/invoices/{id}/payments/{payment_id}` checks ownership, uses
`payment.query` when available, and otherwise returns that attempt's stored state.
A paid invoice alone does not prove that a particular attempt received funds. Browser
returns and successful checkout creation never settle an invoice.

Callbacks go to `POST /api/v1/payment-callbacks/{gateway_id}`. Only that slot may
verify them. Core forwards the unchanged body (maximum 64 KiB), method, path,
query and bounded headers; it excludes browser cookies and CSRF credentials.
Authenticate provider signatures, merchant, timestamp and replay window before
returning evidence. Compare amount, currency, precision, account, order, invoice
and attempt exactly. The provider ID columns in attempts/settlements contain the
gateway ID, so `(gateway, event)` and `(gateway, transaction)` are durable unique
keys even when two slots use the same plugin and merchant.

Expiry and staff voiding retain historical attempts. Late valid receipts still
follow the existing settlement rules: duplicate receipts on a paid invoice and
receipts for cancelled orders are recorded for review without paying or
provisioning twice. Exact retries return success; conflicting evidence returns
409. Successful callbacks default to `{"data":{"received":true}}`. A verified
event may include `acknowledgement: {"status_code":200,"content_type":"text/plain",
"body":"success"}` for a provider-specific response. Allowed types are
`text/plain`, `application/json`, `application/xml` and `text/xml`; bodies must be
valid UTF-8, at most 4 KiB, with valid JSON/XML where applicable. XML cannot contain
DTDs or processing instructions other than its declaration. Alternatively return
`{"status_code":204}` without content type or body. Core preserves the body bytes,
sets safe response headers and emits the acknowledgement only after settlement
commits, including exact retries. Invalid signatures return 400, missing attempts
404 and unavailable workers/storage 503; plugins cannot override these errors or
supply arbitrary HTTP headers. Query responses never emit an acknowledgement.

`GET /api/v1/admin/integrations/gateways` reports ready/stopped/misconfigured/
incompatible status and last handshake time. `PATCH .../gateways/{id}` requires
`integrations.write`, CSRF and the current revision; it changes enabled, localized
name and display order only. Host approval remains required. Disabling the UI
offer leaves callbacks for existing attempts available; host policy disable stops
all calls to that worker. Neither API accepts credentials.

## Build, approve and operate

Reference plugins are `payment.bank-transfer` (offline instructions) and
`payment.stripe` (Stripe Checkout, query and refund). The template collects no
funds. From the repository root, build a reviewable archive without activation:

```sh
python3 -B vps-billing/deploy/scripts/build-payment-plugin.py \
  --plugin bank-transfer --output /path/to/new-bank-transfer.tar.gz
```

Use `--plugin stripe` or `template` for the other examples. The helper builds static
Linux amd64 code, derives the manifest from the plugin, includes dependency notices
and verifies the installer's archive format. Output is exclusive; existing files
are never overwritten. Third-party archives use the same six-file inventory and
protected-input rules in [PLUGINS.md](PLUGINS.md). Review source, dependencies and
provenance before trusting any digest.

The following root commands assume a reviewed archive in a root-owned protected
directory, a dedicated non-login `aster-pay-bank` user, shared socket group
`aster-plugins`, and actual worker/API numeric UIDs. The worker must not belong to
the API secret-reading group. Create each worker separately; UIDs and sockets
cannot be shared. Use short slot IDs with the supplied service user naming, or
provide a per-instance `User=` override for longer IDs.

```sh
python3 /opt/aster/current/deploy/scripts/plugin-manager.py install \
  /var/lib/aster-install/bank-transfer.tar.gz --sha256 "$REVIEWED_ARCHIVE_SHA256"
python3 /opt/aster/current/deploy/scripts/plugin-manager.py select \
  --kind payment --slot bank --revision 0 --id payment.bank-transfer --version 1.0.0 \
  --worker-uid 1102 --client-uid 1100 --merchant-id bank-account
python3 /opt/aster/current/deploy/scripts/plugin-manager.py enable \
  --kind payment --slot bank --revision 1
systemctl enable --now aster-payment@bank.service
```

Revisions belong to the whole approval policy; read `status` and substitute the
current revision before each mutation. Selection starts disabled. For Stripe add
`--target api.stripe.com:443`; offline plugins need no targets. The socket is
`/run/aster-payment/<slot>/plugin.sock`. The template uses
`LoadCredential=payment:/etc/aster/payment/<slot>/`; a root-owned mode 0600
`config.json` becomes the worker's `payment_config.json`. See each plugin README
for its config. No database, session or SMTP secrets are inherited.

Use `disable`, `enable`, `rollback --version X.Y.Z` or `remove`, always with
`--kind payment --slot ID --revision N`. `remove` requires a disabled selection
and retains installed versions and accounting history. The manager never starts
or stops services: coordinate `systemctl stop/restart aster-payment@ID` yourself.
Network target approval is an application boundary; enforce stricter egress at
the host. Handshake readiness proves worker identity/protocol, not provider funds
or connectivity.

To migrate a legacy single payment selection, stop `aster-payment.service`, then
run `migrate --kind payment --slot <old-plugin-id> --revision N`. The slot ID must
equal the previous plugin ID so existing dedupe namespaces remain intact. The
manager preserves executable/manifest approvals, merchant and UID, changes the
socket path and writes `payments[]`; no hash re-approval occurs. Configure the
matching template instance's `User=` to retain that UID, move its credentials into
the new per-slot directory and start that instance. Migration never relaxes a
plugin's Core compatibility bounds.

## Conformance and acceptance

Call `paymentv1/conformance.Test` from a normal Go test with a manifest, processor,
config and intent; online providers also supply a signed success callback and an
optional refund fixture. Use a fake provider transport. The runner checks checkout
idempotency, flow validation, raw-body tampering, evidence and declared optional
hooks. Run `go test -race ./...` in the author's module.

Repository CI runs the plugin fixtures and SDK on Linux, actual systemd workers
with separate UIDs/sockets/credential mounts, and payment contracts/settlement
tests against native MySQL. macOS cross-compilation is build evidence only.
Third-party authors must additionally provide official API sources, signature
fixtures, timeout/replay behavior and sandbox-account acceptance. CI never contacts
a real payment provider. Core trusts approved verification code; process isolation
cannot make incorrect signature verification trustworthy.
