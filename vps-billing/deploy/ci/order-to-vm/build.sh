#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
out="$root/artifacts/e2e-bin"
mkdir -p "$out"
export CGO_ENABLED=0 GOOS=linux GOARCH=amd64
cd "$root/vps-billing/backend"
go build -trimpath -buildvcs=false -o "$out/aster-api" ./cmd/server
go build -trimpath -buildvcs=false -o "$out/aster-plugin-host" ./cmd/plugin-host
go build -trimpath -buildvcs=false -o "$out/paymentfixture" ./plugins/paymentfixture/cmd/plugin
go build -trimpath -buildvcs=false -o "$out/smtp" ./plugins/smtp/cmd/plugin
go build -trimpath -buildvcs=false -o "$out/server.aster-cloud" ./plugins/server.aster-cloud/cmd/plugin
python3 -B - "$root" <<'PY'
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
(root / "artifacts/e2e-scheduler-overlay.json").write_text(json.dumps({"Replace": {
    str(root / "vps-billing/backend/internal/billing/order_to_vm_scheduler_test.go"):
    str(root / "vps-billing/deploy/ci/order-to-vm/scheduler.gotxt")}}))
PY
go test -c -overlay "$root/artifacts/e2e-scheduler-overlay.json" -o "$out/scheduler.test" ./internal/billing
cd "$root/artifacts/e2e-cloud/aster-cloud"
go build -trimpath -buildvcs=false -o "$out/aster-controller" ./cmd/aster-controller
go build -trimpath -buildvcs=false -o "$out/aster-node" ./cmd/aster-node
