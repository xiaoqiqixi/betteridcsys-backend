# Payment template

Copy this directory into your own module and depend on the Go SDK packages
`pkg/pluginapi/paymentv1` and `pkg/pluginserver`. Replace `payment.template` with
your plugin ID and update the template import in `cmd/plugin/main.go` to your
module path. The template deliberately creates instructions that collect no
money and rejects every callback.

Hooks: `Create` receives a durable Core attempt and must use its idempotency key;
`Verify` receives the original HTTP body and bounded headers, and must authenticate
the provider before returning complete settlement evidence. Optional `Query`
returns pending/expired/failed or complete settlement evidence. Optional `Refund`
uses a separate durable idempotency key and returns pending/succeeded/failed.
Declare optional capabilities only when their interfaces are implemented.

From `backend/`:

```sh
go test -race ./plugins/payment-template/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -o plugin ./plugins/payment-template/cmd/plugin
```

`template_test.go` demonstrates `conformance.Test`. Third-party plugins use the
same runner with their own fake HTTP provider and signed callback fixture. Never
point CI at a real payment account. Linux CI additionally tests the actual Unix
socket handshake, optional operations and systemd slot isolation.

`pluginserver.RunPayment` handles host flags, executable identity, peer UID checks,
bounded requests and shutdown. It forwards no API environment. Read worker-only
configuration with `ReadPaymentCredential("payment_config.json")`; this corresponds
to `/etc/aster/payment/<gateway-id>/config.json` in the template unit.

See [the author guide](../../../docs/PAYMENT_PLUGINS.md) for packaging and approval.
