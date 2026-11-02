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

The original `BenchmarkHTTPPerformance` remains unchanged. The separate
`TestMySQLCommerceFinanceRuntimePerformance` builds a plain child binary (no race
instrumentation), seeds the full dataset, preflights all reads, and measures 23
read routes plus a top-up write. It uses five warmup requests and 100 timed
requests per route, at concurrency 1 and GOMAXPROCS=1. Each write has a fresh
idempotency key. All requests must succeed. CPU/block/mutex profiles are collected
in a separate three-second read phase so profiling does not perturb percentiles.
The child process and load generator share the one Go scheduler; MySQL runs on
an unrestricted four-CPU hosted runner. This is not dedicated 1-vCPU acceptance.

The existing commerce and complete MySQL CI suites run the new test. Their JSON
test artifacts contain `PERF1_RESULT`, `PERF1_EXPLAIN`, and base64 native
`PERF1_PROFILE` records. Concatenate test `Output` fragments before parsing them:
Go's JSON test stream can split long lines. The benchmark does not start provider,
email, scheduler or provisioning workers.

## Full-size measured baseline

[Successful MySQL run 41390951013](https://github.com/xiaoqiqixi/betteridcsys-backend/actions/runs/41390951013)
measured `721d6f2`, Go 1.27.3 and MySQL 8.4.13. The preceding measurement attempt
failed preflight because it used a hidden legacy product for public detail; it
is not used as the latency baseline. The corrected run uses a visible product.

The following ranking uses nearest-rank p50/p95 from 100 successful requests.
The first twenty rows are the slowest measured routes, with the remaining four
included for completeness. Times are milliseconds.

| Method and endpoint | Before p50 | Before p95 |
| --- | ---: | ---: |
| GET /api/v1/admin/products | 209.591 | 224.958 |
| GET /api/v1/products | 208.356 | 222.222 |
| GET /api/v1/admin/overview?currency=CNY | 103.063 | 107.582 |
| GET /api/v1/admin/invoices | 58.577 | 65.931 |
| GET /api/v1/admin/currencies | 53.880 | 55.087 |
| GET /api/v1/admin/orders | 47.368 | 52.369 |
| GET /api/v1/invoices | 44.016 | 50.881 |
| GET /api/v1/admin/services | 31.617 | 38.008 |
| GET /api/v1/orders | 30.917 | 35.762 |
| GET /api/v1/overview | 14.356 | 16.509 |
| GET /api/v1/services | 13.022 | 15.815 |
| GET /api/v1/admin/audit-events | 6.797 | 7.050 |
| POST /api/v1/credit/top-up | 4.992 | 6.142 |
| GET /api/v1/admin/products/perf-product-0 | 2.971 | 3.492 |
| GET /api/v1/admin/customer-accounts | 1.814 | 2.375 |
| GET /api/v1/admin/catalog-taxonomy | 2.226 | 2.349 |
| GET /api/v1/invoices/{id} | 1.632 | 2.303 |
| GET /api/v1/products/perf-product-0 | 2.068 | 2.198 |
| GET /api/v1/services/{id} | 1.623 | 1.676 |
| GET /api/v1/orders/{id} | 1.565 | 1.649 |
| GET /api/v1/audit-events | 1.161 | 1.548 |
| GET /api/v1/session | 0.981 | 1.020 |
| GET /api/v1/catalog-taxonomy | 0.685 | 0.879 |
| GET /api/v1/currencies | 0.364 | 0.407 |

[Raw measurements](performance/721d6f2/result.json),
[14 query plans](performance/721d6f2/explain.json),
[CPU profile](performance/721d6f2/cpu.pprof),
[block profile](performance/721d6f2/block.pprof), and
[mutex profile](performance/721d6f2/mutex.pprof) are retained with the source.
These plans cover the measured commerce/audit list selectors and invoice child
queries; they do not yet cover every list/filter combination in the repository.

CPU sampling recorded 830 ms over 3.36 s: 48.2% flat time was in Linux syscalls;
MySQL packet reads accounted for 22.9% cumulatively. Block samples primarily
show HTTP client, SQL transaction and driver watcher waits, not proof of a pool
bottleneck at concurrency 1. Total sampled mutex delay was only 168 microseconds.
This evidence favors fewer database round trips and less SQL scanning over
changing Go mutexes or increasing the connection limit.

Staff order/invoice selectors scan approximately 49,000 index entries or rows.
Their user-leading indexes cannot satisfy the global chronological ordering.
The product service-count subquery scans roughly 2,000 services for each product.
Invoice line/payment lookups use existing invoice-leading indexes. New global
and status/date indexes need a new migration; registering it also requires the
existing HTTP seed test's hard-coded migration count to be updated by agreement
with CI-2.

## Product-list optimization under validation

List reads now aggregate service counts once and load prices, options, choices,
option prices and OS images in batches of at most 100 products. For the seeded
200-product staff list, child-query count falls from 600 to six, with a single
service aggregation instead of 200 scans. Detail/locking reads retain their
existing path. A real-MySQL equivalence test compares batched output to detail
reads across batch boundaries, mixed visibility, unknown currencies, option
prices, disabled images and case-insensitive child keys. The candidate CI run
will supply the corresponding latency comparison before acceptance is claimed.


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
