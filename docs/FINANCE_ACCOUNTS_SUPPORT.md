# Core 0.6 accounts and support

The pinned API is `vps-billing/contract/openapi.json`. Account, support, content,
staff and settings handlers validate incoming documents and return that contract's
response shapes. `Accounts and support` CI runs their contract/security tests with
real MySQL 8.4 and rejects skipped acceptance tests.

## Authorization and credentials

All `/admin/*` requests pass through `App.staff(permission, handler)`. The mapping
in `rbac.go` is checked against every contract `x-permission`. Each request reads
active membership and role permissions from MySQL. Sessions do not carry authority.
The built-in owner has all known permissions; serialized membership updates protect
the last active owner. Personal bearer tokens cannot enter administrator or account
security routes and require the appropriate read/write scope and allowed source IP.

Login, registration and recovery throttles use IPv4 /32 or IPv6 /64 only. There is
no account lockout or per-email throttle. Trusted proxy CIDRs must already be set
correctly for forwarded addresses to apply. Password/verification links expire in
30 minutes; only their SHA-256 hashes live in the token table. Recovering a password
revokes every browser session. Changing a password preserves only the current one.
Passwords up to the contract's 256 characters are prehashed before bcrypt; existing
bcrypt hashes continue to verify. No second factor, KYC or tax workflow is added.

Client profile/activity responses omit staff identity and private client notes.
Internal ticket messages never appear in client ticket responses or notifications.
Closing/disabling a client removes browser sessions, personal tokens and outstanding
setup links. Email verification can be required before placing an order.

## Outbox integration

`email_outbox` is consumed by the shared scheduler, using the private `email_outbox`
job. `close_tickets` and `release_affiliate` use the public scheduler settings.
SMTP-disabled installations retain pending mail without spending delivery attempts.
Delivery claims use row locks and five-minute leases; failures back off 60, 120,
240 and 480 seconds, then stop after five attempts and leave a redacted audit event.
SMTP acknowledgement loss can cause redelivery, so delivery is at least once.

Commerce producers insert the notification in their own transaction:

```sql
INSERT INTO email_outbox
 (id,user_id,template_key,variables,attempts,next_attempt_at,last_error,created_at)
VALUES (?,?,?, ?,0,UTC_TIMESTAMP(6),'',UTC_TIMESTAMP(6));
```

`variables` is a JSON object of string values. Keys must come from the template's
allow-list returned by `/admin/email-templates`; common values such as recipient,
name and site name are filled by the consumer. Prefer a stable message ID or an
existing transaction replay guard to avoid duplicate inserts. Security link
variables use the account encryption helper, never plaintext producer variables.
Recipient preferences and template enablement are rechecked at delivery. Verification
and password-reset email bypass both opt-outs. SMTP uses the installed SMTP plugin;
the settings API never returns its password.

## Affiliate policy and settlement integration

`recordAffiliateCommissionTx(ctx, tx, invoiceID, now)` must run once when an invoice
becomes fully paid, inside the payment transaction, before commit. The current
settlement path calls it. Future commerce settlement paths must use the same helper
for gateway, credit and administrative settlements. Invoice uniqueness prevents
double recording. Failed settlement rolls back commission and referral conversion.
Top-ups and manual invoices do not earn commission. Account credit has no implicit FX:
an affiliate and referred invoice must use the same currency. First earned commission
locks the affiliate's account currency. Values use integer basis points with rounding
down to the minor currency unit, preserving the invoice's precision.

The design/contract do not specify a commercial rate or first-invoice versus recurring
eligibility. The initial policy therefore has zero basis points; referrals are tracked
but no money is awarded until an operator chooses the policy. The internal
`account_settings` row named `affiliate_policy` has this JSON shape:

```json
{"commission_basis_points": 0, "recurring": false}
```

Basis points range from 0 through 10000. `recurring:false` awards only the first paid
service invoice for each referral; `true` also awards later order/renewal/upgrade
invoices. Changes apply prospectively; earlier conversions are not backfilled.
Policy administration is not an endpoint in the pinned contract. Configure the row
through an operator-reviewed database change once the commercial policy is decided.

`release_affiliate` makes commissions available after `affiliate_hold_days`, adjusting
pending amounts for refunds recorded before release. Partial withdrawals atomically
consume available commission and append `affiliate_payout` to the credit ledger.
Retries use the same idempotency key and cannot append another credit. Refunds after
release require an operator decision; this implementation does not silently debit
credit already earned or spent.
