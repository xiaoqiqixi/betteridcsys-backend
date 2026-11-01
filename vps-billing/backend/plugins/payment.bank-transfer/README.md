# Bank transfer

Instructions flow; no network calls, callbacks, status lookup or refunds. Core
stores the attempt reference. Staff reconcile the bank receipt and use the manual
payment endpoint; viewing instructions never marks an invoice paid.

Write a root-owned, mode 0600 `/etc/aster/payment/<slot>/config.json`:

```json
{"merchant_id":"bank-account","instructions":"Bank name\nAccount holder and account number\nSWIFT/routing details\nInclude the payment reference."}
```

Instructions are public to the paying customer. Supply receiving accounts for
the advertised USD/EUR/GBP/CNY/JPY currencies, or narrow the plugin declarations
and rebuild before approving it. No conversion is performed. References are
stable per attempt, and instructions have no automatic expiry.

From `backend/`, run `go test -race ./plugins/payment.bank-transfer/...` and build
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o plugin ./plugins/payment.bank-transfer/cmd/plugin`.
The command uses the standard host flags and systemd `payment_config.json`
credential. Bank details stay outside Core's credential storage.
