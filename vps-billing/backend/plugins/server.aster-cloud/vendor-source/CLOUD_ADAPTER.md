# Cloud SDK provenance

`cloud-billing/` is an unchanged snapshot of the complete upstream Go SDK,
identified by commit and individual SHA-256 hashes in `CLOUD_SOURCE.json`.
The billing build copies `serviceapi/` and `internal/controllers/service.go`
byte for byte; `TestPinnedCloudServiceSDK` verifies both copies against the pin.

The existing local controller transport remains shared by provisioning and
service actions. Its connection path revalidates every DNS answer against the
host-approved CIDRs before each single dial. This preserves newer billing
transport hardening that is absent from the pinned upstream transport. The
only transport refactor extracts that existing HTTP call into `Client.do`.
The original ownership, credential, TLS, redirect, proxy, response validation,
and uncertain-outcome rules remain in force.

Do not copy the upstream transport over the hardened local transport when
updating this SDK. Review the transport difference and update the complete
snapshot, hashes, public copies, and protocol tests together.
