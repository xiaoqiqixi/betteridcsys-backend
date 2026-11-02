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

## Measured changes

The latest probe on [0b9d74e, run 41399855457](https://github.com/xiaoqiqixi/betteridcsys-backend/actions/runs/41399855457)
passed the complete Commerce core MySQL suite, including all new equivalence,
boundary, index and diagnostic tests. It uses the same seed, Go/MySQL versions,
concurrency, scheduler limit and sample count as the baseline, and includes the
overview aggregate change. Main also contains concurrent non-PERF changes.
These are complete-revision comparisons on hosted runners, not isolated
attribution of every millisecond.

Times are milliseconds, in the original baseline ranking. The first twenty
rows are the baseline's twenty slowest routes.

| Method and endpoint | Before p50 | Before p95 | After p50 | After p95 |
| --- | ---: | ---: | ---: | ---: |
| GET /api/v1/admin/products | 209.591 | 224.958 | 13.684 | 14.981 |
| GET /api/v1/products | 208.356 | 222.222 | 12.411 | 13.460 |
| GET /api/v1/admin/overview?currency=CNY | 103.063 | 107.582 | 88.704 | 91.030 |
| GET /api/v1/admin/invoices | 58.577 | 65.931 | 17.198 | 18.365 |
| GET /api/v1/admin/currencies | 53.880 | 55.087 | 54.553 | 55.841 |
| GET /api/v1/admin/orders | 47.368 | 52.369 | 19.610 | 21.379 |
| GET /api/v1/invoices | 44.016 | 50.881 | 1.908 | 2.587 |
| GET /api/v1/admin/services | 31.617 | 38.008 | 2.360 | 3.049 |
| GET /api/v1/orders | 30.917 | 35.762 | 3.843 | 4.658 |
| GET /api/v1/overview | 14.356 | 16.509 | 7.181 | 7.812 |
| GET /api/v1/services | 13.022 | 15.815 | 1.921 | 2.364 |
| GET /api/v1/admin/audit-events | 6.797 | 7.050 | 1.794 | 2.405 |
| POST /api/v1/credit/top-up | 4.992 | 6.142 | 5.436 | 7.307 |
| GET /api/v1/admin/products/perf-product-0 | 2.971 | 3.492 | 2.687 | 3.153 |
| GET /api/v1/admin/customer-accounts | 1.814 | 2.375 | 1.459 | 1.859 |
| GET /api/v1/admin/catalog-taxonomy | 2.226 | 2.349 | 1.398 | 1.491 |
| GET /api/v1/invoices/{id} | 1.632 | 2.303 | 1.595 | 1.928 |
| GET /api/v1/products/perf-product-0 | 2.068 | 2.198 | 1.957 | 2.264 |
| GET /api/v1/services/{id} | 1.623 | 1.676 | 1.416 | 1.743 |
| GET /api/v1/orders/{id} | 1.565 | 1.649 | 1.346 | 1.647 |
| GET /api/v1/audit-events | 1.161 | 1.548 | 1.231 | 1.745 |
| GET /api/v1/session | 0.981 | 1.020 | 0.969 | 1.098 |
| GET /api/v1/catalog-taxonomy | 0.685 | 0.879 | 0.749 | 0.923 |
| GET /api/v1/currencies | 0.364 | 0.407 | 0.439 | 0.499 |

**Dedicated 1-vCPU acceptance remains unverified.** All 23 reads in this latest
probe have p95 below 100 ms, and the single measured write has p95 below 200 ms.
However, overview p95 varied from 574.487 ms on the preceding post-change run
`b7e84ca` to 91.030 ms here. This run still had two overview requests above
100 ms, with a maximum of 803.206 ms. Similar spikes predate the aggregate
rewrite: overview p95 was 756.817 ms on `1749131` and 605.382 ms on `30477fd`.
These observations do not isolate the cause of the tail and must not be
discarded as noise. Login and other write operations were not benchmarked.

[Latest measurements and all samples](performance/0b9d74e/result.json),
[81 query plans](performance/0b9d74e/explain.json),
[CPU](performance/0b9d74e/cpu.pprof),
[block](performance/0b9d74e/block.pprof),
[mutex](performance/0b9d74e/mutex.pprof), and
[statement diagnostics](performance/0b9d74e/statements.json) are retained.
The plans contain 14 explicit list/filter cases and all 67 distinct SELECT
forms observed during HTTP read preflights, including child queries. They are
not an exhaustive matrix of every filter and endpoint in the repository.

The [preceding complete probe](performance/b7e84ca/result.json) and its profiles
remain available, as do the [intermediate results](performance/2aeba26/result.json).
The b7e84ca CPU profile sampled 330 ms over 3.08 s, with Linux syscalls accounting
for 24.2% flat time versus 48.2% in the original baseline. Fixed-duration profiles
complete more cycles after optimization, so these are not CPU/request measures.
Block stacks primarily show driver, SQL transaction and HTTP waits.

The diagnostic phase reads schema-scoped performance_schema SELECT digests
without resetting shared server instrumentation. On 0b9d74e the server returned
**zero digest rows**, so server-side statement timing is unavailable in that
artifact; the cause of the empty digest result was not determined. The Go pool
reported **zero waits and zero wait duration**. This does not attribute the
803 ms request to a specific query. EXPLAIN does show recent-revenue calculation
scanning about 35,000 payment rows and 50,000 invoice rows despite applying date
filters. Further SQL timing and index work remain necessary.

### Implementation and evidence by change

| Commit | Change | Before → after p95 (ms) | Regression evidence |
| --- | --- | --- | --- |
| `1749131` | Batch catalog children; aggregate service counts once | Staff products 224.958 → 12.996; public products 222.222 → 8.172 | Real-MySQL batch/detail equivalence passed on that commit |
| `30477fd` | Cache identity and staff policy within one HTTP request | Staff audit 7.050 → 5.103; session 1.020 → 1.061 | One session, user and policy read per guarded request; revocation and failures refresh on the next request |
| `4a59836` | Read invoice summaries without detail children | Staff invoices 65.931 → 13.202; client invoices 50.881 → 2.463 | All summary fields, historical precision, legacy paid/void states, pagination and ownership matched detail reads |
| `f2e240d` | Batch service summaries and reuse their base scanner | Staff services 38.008 → 2.968; client services 15.815 → 2.518 | Full summary equivalence, one-time cycles, cloud data, tasks and owner scoping passed |
| `ca9657e` | Batch order headers, items and service links | Staff orders 52.369 → 16.247; client orders 35.762 → 4.632 | Full detail equivalence, legacy options, mixed-case child keys and cursor ownership passed |
| `e555274` | Resolve audit actor roles in one query | Staff audit 7.050 → 2.125 | MySQL account collation, historical actors, Unicode and one-query assertion passed |
| `f6ebdd7` | Combine duplicate overview aggregate scans | p50 103.063 → 61.485; p95 107.582 → 574.487 (target unmet) | Empty sets, month/current-time boundaries, currencies and balances passed on real MySQL |

The first two rows use their own retained probe runs:
[product measurements](performance/1749131/result.json) and
[authorization measurements](performance/30477fd/result.json). The invoice/service/order/audit rows use the
combined 2aeba26 probe; overview uses b7e84ca. Request caching is supported by deterministic lookup-count
and revocation checks; noisy session latency does not establish a speedup for
that endpoint. The authorization-only run also recorded a 605.382 ms overview
p95, retained in the raw evidence rather than discarded.

For the 200-product staff list, child queries fell from 600 to six. Service counts
now scan the service relation once instead of once per product. Invoice and
service hydration each use a bounded query; order hydration uses three queries.
All retain their existing cursor selector and repeatable-read transaction.
Customer responses explicitly project public fields. Detail and mutation paths
retain their transaction-specific checks. The audit batch preserves original
actor spelling and lets MySQL apply account email collation.

The 2aeba26 workflow was red in two catalog tests after the concurrent server
connection migration. The latency probe, authorization test and invoice/service/
order/audit equivalence tests passed. `0cba120` fixes the new product fixture's
positional image insert by naming columns and supplying a connection identity.
The existing image endpoint contract failure (`connection_id` empty) is separate
from PERF-1 and its fixture was repaired by concurrent commit `0d770d2`; the
complete Commerce core suite subsequently passed on b7e84ca.

## Index migration dependency

Global chronological invoice and order selectors still scan roughly 50,000 and
46,000 entries and sort: their existing indexes lead with `user_id`.
`TestMySQLCommerceFinanceListIndexCandidates` measures these four proposed indexes
inside a disposable full-size database and verifies that ordered results match:

```sql
CREATE INDEX ix_orders_date_id ON orders(created_at,id);
CREATE INDEX ix_orders_status_date_id ON orders(status,created_at,id);
CREATE INDEX ix_invoices_date_id ON invoices(issued_at,id);
CREATE INDEX ix_invoices_status_date_id ON invoices(status,issued_at,id);
```

Its `PERF1_INDEX_RESULT` record includes before/after p50/p95 and EXPLAIN JSON for
global and status-filtered selectors. This SQL experiment runs inside the ordinary
race-enabled MySQL suite; its timings are not HTTP acceptance measurements.
No candidate index is installed in production by this test.

The [successful experiment](performance/b7e84ca/index-candidates.json) measured:

| Selector | Before p50 | Before p95 | Indexed p50 | Indexed p95 |
| --- | ---: | ---: | ---: | ---: |
| Orders, global | 8.005 | 8.892 | 0.196 | 0.231 |
| Orders, pending payment | 9.516 | 11.155 | 0.207 | 0.277 |
| Invoices, global | 7.816 | 8.359 | 0.176 | 0.203 |
| Invoices, unpaid | 9.005 | 10.934 | 0.208 | 0.238 |

All four plans select the proposed indexes and eliminate filesort. Global
selectors estimate 51 entries instead of approximately 43,000/45,000. Filtered
selectors use `ref` access; their row estimates remain approximately 19,600
qualifying entries before LIMIT. The experiment checks unchanged ordered IDs;
production write overhead and migration recovery still need validation.

A second [successful experiment on 0b9d74e](performance/0b9d74e/index-candidates.json)
recorded unindexed p95 of 15.347–17.122 ms and indexed p95 of 0.403–0.472 ms.
The absolute times vary with the runner; both experiments show the same indexed
plans, unchanged ordered results and a substantial reduction in selector cost.

Registering a new migration also changes the exact migration count asserted by
`internal/billing/http_performance_seed_test.go`. The task explicitly forbids
editing that existing harness. The inventories now expect 29 migrations after
concurrent changes. Besides the HTTP seed, fixed versions also appear in
`performance-report.py`, `test_performance_report.py`, `accept-native.sh`,
`core-native.py`, `test_accept_native.py` and `test_runtime_release.py` under
`vps-billing/deploy/ci`. The migration therefore awaits permission for these
matching inventory/version updates, or updates by CI-2. Existing migrations and
assertions have not been weakened. Additional observed scans include currency usage aggregation,
revenue-date filtering and service-count grouping; further indexes need their
own measured tradeoffs, including write overhead.

## Existing behavior to preserve

The database already sets 20 maximum open connections, five idle connections and
a five-minute maximum lifetime. Financial emails already enter an outbox inside
the financial transaction. Password verification must finish before a login can
be authorized; reducing bcrypt cost or returning success before verification is
not an acceptable latency optimization. Transactional session and permission
checks protecting mutations remain fresh; the request memo does not replace
store checks performed within financial transactions. The measured cursor lists
already omit global totals and do not run a count query per page, so no contract
change is needed to remove default totals. Dashboard aggregates and currency
usage have distinct semantics and are not silently omitted.

## CI baseline

Before PERF-1 changes, both native financial acceptance jobs on `3d3663c` failed
because the protected existing 0.5.2 installation was unavailable. The jobs
require `ASTER_INSTALLED_LEGACY_BASELINE`; this is separate from runtime benchmark
or MySQL regression results and cannot be repaired by weakening acceptance.

## Remaining work and validation status

- Full-size fixture, 24-route measurements, top-20 ranking, profiles, request
  caching and catalog/invoice/service/order/audit batching: implemented and
  exercised with real MySQL in Commerce core. The fixture's independent row-count
  and financial-consistency test also passed in Accounts run 41390087726.
- Query plans: 81 retained forms; exhaustive endpoint/filter coverage is partial.
- Chronological indexes: measured successfully in disposable databases, not
  registered in a production migration because the matching protected harness
  inventories require permission or coordinated CI-2 changes.
- Overview: fewer aggregate scans and lower typical latency, but large outliers
  remain unresolved. The latest numeric p95 pass is not a dedicated 1-vCPU SLA
  result and does not override earlier failing percentiles.
- Existing defaults: cursor lists omit totals, mail is queued transactionally,
  and the connection pool is bounded. Authentication still verifies passwords
  before success. No bcrypt cost, tests or financial invariants were weakened.
- Local checks: Linux amd64 billing test compilation, Linux `go vet ./...`, and
  native commerce, staffaccess, affiliate, financeapi and contracttest tests pass.
  MySQL acceptance ran in CI because the local host is macOS and the billing
  package has Linux-only runtime dependencies.
- Last runtime/diagnostic push `2c13e1d` had its Commerce run cancelled while
  queued. Its descendant `0b9d74e` includes it and passed Commerce core and Release
  tooling. Server modules failed while turning off controller maintenance, and
  Order to VM also failed; those integration failures are outside these changes.
  Accounts was still running when this evidence was collected. This is not a
  claim that every main gate is green. The older missing-legacy-installation
  native failure described above also remains a separate infrastructure issue.
