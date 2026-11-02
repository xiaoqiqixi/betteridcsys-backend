# Order to VM acceptance

The `Order to VM` workflow runs `e2e-order-to-vm` on a disposable Ubuntu runner.
Run it against `main` with `gh workflow run e2e-order-to-vm.yml --ref main`.
Do not run `stack.py run` on a developer machine: it creates Linux identities,
two MySQL instances, systemd workers, a bridge, and real libvirt guests.

`E2E_STACK.json` pins the cloud and frontend commits and the two Ubuntu minimal
NoCloud images by SHA-256. The finance source is the workflow commit. Downloads
and native binaries are cached after verification, before the scenario starts.
The node uses KVM when available and explicitly approved QEMU software emulation
otherwise. No controller or node request is mocked.

The fixture creates an isolated certificate authority. Browsers trust it through
their NSS profiles; nginx verifies the finance HTTPS upstream; MySQL requires
TLS; the module verifies the controller certificate; and the controller and node
authenticate each other with mTLS and certificate pins. The server module,
payment fixture, SMTP worker, and finance API run as separate unprivileged users.
Controller credentials are delivered only to the server worker with systemd
credentials. The mail sink accepts authenticated SMTP over TLS.

## Scenario

Customer and staff interactions use the pinned frontend in Firefox. API reads,
controller audit and operation records, the node journal, `virsh`, `qemu-img`,
and guest-network ping provide independent assertions.

| Step | Required evidence |
| --- | --- |
| Register, verify email, sign in | Verification mail received by the TLS sink; verified finance account |
| Configure, quote, order | Approved module image and root password selected; unpaid invoice |
| Manual staff payment | Paid invoice; active service; successful provisioning and guest setup; running reachable domain and customer IP |
| Payment fixture on another order | Real plugin checkout and signed callback; separate active VM and IP |
| Power, password, reinstall | Successful controller operations; matching finance and libvirt state; second approved image |
| Stopped snapshot and restore | Snapshot lifecycle succeeds; domain remains stopped |
| Console | TLS websocket receives the real node VNC `RFB` banner |
| Plan change | Paid upgrade; controller and finance resource values match domain CPU, memory, and disk size |
| Overdue and settlement | Renewal invoice; suspended and stopped domain; start rejected; paid invoice automatically resumes VM |
| Immediate cancellation | Terminated service; undefined domain; removed disks; released IP |
| Secret retention | Generated password canaries absent from both database dumps, logs, and durable module/node receipts |

The scheduler test binary contains a CI-only overlay that invokes the real
registered automation jobs with an explicit clock. It adds no production clock
endpoint. Every job must produce a successful receipt. Polling observes actual
state and has bounded deadlines; no fixed delay stands in for readiness.

The fixture also runs the checkout credential, overdue-clock, and billing
automation regression tests against real MySQL 8.4. These must pass without skips.

## Failure evidence

The `order-to-vm-<run>-<attempt>` artifact contains `summary.json` and bounded,
redacted `diagnostics.txt`. The summary identifies the exact source commits,
hypervisor mode, node readiness and capabilities, completed steps, and the last
observed service tasks. A step is recorded only after its assertions pass; the
overall result remains false until the final secret-retention check succeeds.
Private credentials, mail bodies, and database dumps are never uploaded.
Cleanup happens after assertions and does not count as successful termination.
