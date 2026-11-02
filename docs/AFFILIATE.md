# Configurable affiliate commissions

The frontend owns the API contract. This implementation vendors the AFF-1 contract from frontend commit `f3784c3` in `vps-billing/contract/openapi.json`.

Settings are edited at `/admin/affiliate` with `settings.write`. Listing, editing and manually settling affiliates requires `clients.write`. The settings singleton starts disabled, with a zero default rate, order-only scope, 12 months, a 30-day hold, 30-day attribution, no currency limits, self-referrals disabled and `reverse_all`. Updates must echo the current revision.

## Calculation and accounting

- Only settled cash from gateways or manual receipts counts. Credit and top-ups do not. Legacy settlement receipts are counted once, even after materializing an invoice payment.
- Cash, after discounts and credit, is allocated proportionally across positive invoice lines. Cumulative integer division preserves the full cash pool. Each line earns `floor(cash_minor * rate_bp / 10000)`.
- Rates resolve from affiliate override to product override to default. Zero is an explicit override. Order lines retain their quote item/product association, including options and setup fees.
- `first_payment` includes order invoices; `all_payments` includes order, renewal and upgrade invoices. `first_months` uses each service's billing start and the line's period start with an exclusive calendar-month endpoint. Missing renewal service dates cannot restart that window.
- The affiliate user lock serializes earning, caps, refunds and payouts. The invoice's unique commission record prevents repeated settlement from earning twice. Settings revision, scope, hold deadline, clawback policy, rates and source lines are snapshotted in the settlement transaction.
- Caps use the original lifetime awards for that referred customer and currency. Refunds do not reopen a used cap. No currency conversion occurs.
- `release_affiliate` applies successful cash refunds and releases expired holds. Refund reversals use cumulative proportional rounding, so repeated small refunds cannot evade clawback. `reverse_pending` considers refunds completed before the snapshotted hold deadline; `reverse_all` also recovers released or paid commissions. Duplicate/unallocated gateway receipts are excluded.
- Released earnings minus refunds and payouts produce either available funds or debt. Available funds never become negative. Later releases offset debt before becoming withdrawable.
- Client withdrawals credit only the account currency. Admin manual payouts can settle any earned currency, require a reference, and support idempotent retries. The reference and amount appear in the audit log. Disabling the programme or suspending an affiliate leaves existing balances withdrawable.
- Attribution is fixed at registration. The server issues an authenticated, expiring referral cookie; settings control its lifetime. Self-referral checks compare normalized emails and IPv4 `/24` or IPv6 `/64` prefixes. Historical accounts fall back to their oldest retained session prefix.

Migration 028 adds separate settings, profile, snapshot, source-line, adjustment, payout and registration-prefix tables. Existing commission IDs, amounts and withdrawals remain intact. Legacy holds use the automation policy at migration; new holds use affiliate settings. Replay/resume validation checks columns, checks, unique keys and foreign keys.

## Test matrix

| Area | Automated coverage |
| --- | --- |
| Scopes | Order / renewal / upgrade / top-up / manual, inclusive start and exclusive calendar-month boundary |
| Rates and cash | Affiliate / product / default precedence, explicit zero, partial cash plus credit, credit-only, per-line floor |
| Caps and currency | Truncated/exhausted/zero caps, independent currencies, client credit currency and foreign manual payout |
| Clawback | Pending / available / withdrawn × both policies, refund replay, debt offset by future earnings, disabled-account release |
| History | Settings changes do not change recorded rate, scope or hold; settlement replay preserves original snapshots |
| Administration | Revision conflicts, permission negatives, suspension, note and rate overrides, payout minimum and idempotency, audit reference |
| Attribution | Cookie expiry, registration cookie, same/different IP prefixes, self-referral toggle, normalized email |
| Migration | Interrupted DDL resume, repeated migration, preserved historical amounts/withdrawals and one legacy snapshot |
| Frontend | Contract boundaries, i18n completeness, admin activation/settings/detail browser workflow, all-route accessibility audit |

Pure calculation tests run with `go test -race ./internal/affiliate ./internal/commerce ./internal/financeapi ./internal/contracttest`. Linux compilation and vet validate the billing package locally on macOS; the existing CI runs all real MySQL, transaction, concurrency and migration tests without skips. Frontend browser audits include 375, 768 and 1280 widths in both languages.
