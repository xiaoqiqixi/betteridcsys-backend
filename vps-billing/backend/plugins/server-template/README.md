# Server module template

This offline fixture implements `aster.server/1` using `pkg/pluginserver.RunServer`.
It simulates services in memory and is not a production provider. Build the Linux
entry point in `cmd/plugin`, then package it with a `server.template` manifest.
Each installed connection runs with a separate UID, socket and credential directory.

Use `pkg/pluginapi/serverv1/conformance.Test` with an isolated provider fixture.
For a production module, reserve each operation ID and digest durably before any
provider mutation. Persist only receipts and resource references. Never retain
`Request.Secrets`, provider credentials, request bodies or password-bearing errors.
Resolve ambiguous receipts through a read-only provider operation lookup. Do not
retry an ambiguous mutation, including after a worker restart. Return normalized
configuration from `validate_product` and advertise only supported capabilities.
