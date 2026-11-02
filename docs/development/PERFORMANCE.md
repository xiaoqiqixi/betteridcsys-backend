# Finance runtime performance (PERF-1)

## Baseline and scope

Runtime changes must follow measurement. The existing synthetic HTTP runner and
CI workflows are owned by CI-2 and are unchanged by the finance fixture below.

The retained [baseline run 41387695979](https://github.com/xiaoqiqixi/betteridcsys-backend/actions/runs/41387695979)
measured source `3db78feea313c05e0c1bc73a2fbf2be587e3f122`, Go 1.27.3,
Linux amd64, MySQL, four logical CPUs, GOMAXPROCS=4. Its fixture has 100 clients,
10,000 orders/invoices, 10,000 audit events and 1,000 services. Invoice lines and
payments are empty. Each endpoint has two three-second trials after a one-second
warmup, at concurrency 1, 8 and 32. All measured requests succeeded.

These are the larger of the two trial percentiles, in milliseconds; they are not
percentiles of pooled samples. This is historical context, **not** the requested
full-size, 1-vCPU acceptance result or a before/after comparison.

| Endpoint | C=1 p50 | C=1 p95 | C=32 p95 |
| --- | ---: | ---: | ---: |
| GET /api/v1/admin/orders | 17.548 | 19.423 | 346.782 |
| GET /api/v1/admin/overview?currency=CNY | 5.546 | 6.583 | 96.622 |
| GET /api/v1/overview?currency=CNY | 1.827 | 2.221 | 22.819 |
| GET /api/v1/orders | 1.303 | 1.772 | 28.947 |
| GET /api/v1/session | 0.633 | 0.743 | 9.230 |

The existing EXPLAIN evidence reports `ALL` for the admin order query (10,000
estimated rows), admin invoice summary (10,000), admin service summary (1,000)
and customer count (101). Customer orders use `ix_orders_user_date`; session and
user lookups use primary keys. At concurrency 32 the 20-connection pool waits.
Increasing the connection limit requires a new measurement, not an assumption.

## Reusable full-size fixture

`internal/billing/finance_performance_fixture_test.go` adds
`seedFinancePerformance(ctx, fixture)` and `TestMySQLFinanceScaleFixture`.
It uses the existing isolated database helper, actual migrations and existing
session/staff seed. It creates:

| Entity | Rows |
| --- | ---: |
| Clients (plus one staff owner) | 10,000 |
| Orders and invoices | 50,000 each |
| Invoice lines | 100,000 |
| Successful manual payment receipts | 37,500 |
| Audit events | 100,000 |
| Services | 2,000 |
| Products with monthly prices | 200 |

Invoices are 75% paid, 20% unpaid and 5% cancelled. Each has two lines whose sum
equals the invoice amount. Payment sums equal the stored paid amount. One client
has 500 invoices; other clients also have history. Services are synthetic
historical services, independent of order fulfillment. IDs and timestamps are
deterministic, domains use `.invalid`, and no provider, SMTP or provisioner runs.
The integration test verifies row counts and financial consistency. It owns and
drops only its new disposable database. The numeric seed table is temporary.

With the disposable MySQL environment supplied by CI:

```sh
go test -mod=readonly -count=1 -run '^TestMySQLFinanceScaleFixture$' ./internal/billing
```

The original `BenchmarkHTTPPerformance` currently covers only five read routes,
limits users to 1,000, and does not emit CPU/block/mutex profiles. Completing the
twenty-endpoint ranking and 1-vCPU read/write targets requires extended
measurement coverage; the historical table above must not stand in for it.

## Existing behavior to preserve

The database already sets 20 maximum open connections, five idle connections and
a five-minute maximum lifetime. Financial emails already enter an outbox inside
the financial transaction. Password verification must finish before a login can
be authorized; reducing bcrypt cost or returning success before verification is
not an acceptable latency optimization. Transactional session and permission
checks protecting mutations must remain fresh even if read-side identity lookup
is reused within a request.

## CI baseline

Before PERF-1 changes, both native financial acceptance jobs on `3d3663c` failed
because the protected existing 0.5.2 installation was unavailable. The jobs
require `ASTER_INSTALLED_LEGACY_BASELINE`; this is separate from runtime benchmark
or MySQL regression results and cannot be repaired by weakening acceptance.
