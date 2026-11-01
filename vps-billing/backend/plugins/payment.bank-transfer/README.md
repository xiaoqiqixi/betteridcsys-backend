# Bank transfer

Instructions flow; no network calls, callbacks, status lookup or refunds. Core
stores the attempt reference. Staff reconcile the bank receipt and use the manual
payment endpoint; viewing instructions never marks an invoice paid.

After reconciling the deposit, staff call
`POST /api/v1/admin/invoices/{id}/payments` with `method: "bank_transfer"`, the
exact `amount_minor`, the displayed `reference`, `received_at` and an
`Idempotency-Key`. Partial receipts reduce the balance due. Full confirmation
records a manual receipt and closes the outstanding checkout: polling reports
`invoice_status: "paid"` and the original attempt's `status: "voided"`. It never
fabricates a provider callback or a gateway settlement record.

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
