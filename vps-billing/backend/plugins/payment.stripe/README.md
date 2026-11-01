# Stripe Checkout

An `aster.payment/1` reference gateway for directly owned Stripe accounts. It
creates hosted Checkout Sessions with automatically captured card payments,
queries sessions, verifies webhooks, and issues full or partial refunds. Tax,
discounts, currency conversion, delayed payment methods and Stripe Connect are
outside this plugin's scope. Checkout uses `checkout.stripe.com`; custom checkout
domains require an author-reviewed manifest change.

Create a root-owned, mode 0600 `/etc/aster/payment/<slot>/config.json`:

```json
{
  "account_id": "acct_YOUR_ACCOUNT",
  "api_key": "sk_test_YOUR_KEY",
  "webhook_secret": "whsec_YOUR_ENDPOINT_SECRET",
  "success_url": "https://billing.example/invoices",
  "cancel_url": "https://billing.example/invoices",
  "live_mode": false
}
```

Use a distinct webhook endpoint/secret for each gateway slot at
`https://billing.example/api/v1/payment-callbacks/<slot>`. Subscribe to
`checkout.session.completed`. Configure the approved merchant ID to match
`account_id`, and allow the outbound target `api.stripe.com:443`. Live credentials
require `live_mode: true`. The worker reads systemd's `payment_config.json`;
neither the Core API nor the gateway administration endpoint accepts secrets.
The API key must permit reading its own account (`GET /v1/account`); the worker
verifies that account ID before any payment operation and caches only success
for that worker's immutable credentials. A wrong merchant key cannot create a
checkout even if the local account label and metadata match.

The API version is pinned in `stripe.go`. Request keys include the gateway and
stored attempt ID. Signature verification uses the unmodified body,
`Stripe-Signature`, HMAC-SHA256, a five-minute age limit and 30-second future
tolerance, before contacting Stripe. A verified event is then checked against an
authenticated session, PaymentIntent and captured charge. All identifiers,
amounts, currencies and slot metadata must agree. Polling and webhooks emit the
same settlement event ID (`settled:<payment_intent_id>`), transaction ID and
automatic card charge timestamp, so provider retries and browser polling share
Core's deduplication rules. A browser return never proves payment.

Refunds verify the original PaymentIntent's gateway, merchant and currency before
sending a gateway-scoped idempotent request. Core must authorize and persist the
refund operation; the SDK hook does not expose a customer refund API.

From `backend/`:

```sh
go test -race ./plugins/payment.stripe/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o plugin ./plugins/payment.stripe/cmd/plugin
```

Tests use an in-process HTTP provider and a fixed synthetic signature fixture;
they never contact Stripe or use real credentials. The fixture is generated
independently with the documented algorithm; it is not an official Stripe test
vector. See the [author guide](../../../docs/PAYMENT_PLUGINS.md) for packaging,
approval and slot lifecycle commands.

References: [Checkout Sessions](https://docs.stripe.com/api/checkout/sessions/create),
[webhook signatures](https://docs.stripe.com/webhooks/signature),
[PaymentIntents](https://docs.stripe.com/api/payment_intents/object),
[refunds](https://docs.stripe.com/api/refunds/create).
