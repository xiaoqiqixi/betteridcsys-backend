#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
mkdir -p "$root/artifacts/mod-bin"
export CGO_ENABLED=0 GOOS=linux GOARCH=amd64
python3 -B - "$root" <<'PY'
import json, pathlib, sys
r=pathlib.Path(sys.argv[1]); fixtures=r/'vps-billing/deploy/ci/server-modules'
for name, target, source in [('node',r/'artifacts/mod-cloud/aster-cloud/internal/hypervisor/server_module_smoke_test.go','fake-node.gotxt'),('finance',r/'vps-billing/backend/internal/billing/server_module_smoke_test.go','paid-order.gotxt')]:
    (r/f'artifacts/mod-{name}-overlay.json').write_text(json.dumps({'Replace':{str(target):str(fixtures/source)}}))
PY
cd "$root/vps-billing/backend"
go build -trimpath -buildvcs=false -o "$root/artifacts/mod-bin/aster-plugin-host" ./cmd/plugin-host
go build -trimpath -buildvcs=false -o "$root/artifacts/mod-bin/server-module" ./plugins/server.aster-cloud/cmd/plugin
go test -c -overlay "$root/artifacts/mod-finance-overlay.json" -o "$root/artifacts/mod-bin/finance.test" ./internal/billing
cd "$root/artifacts/mod-cloud/aster-cloud"
go build -trimpath -buildvcs=false -o "$root/artifacts/mod-bin/controller" ./cmd/aster-controller
go test -c -overlay "$root/artifacts/mod-node-overlay.json" -o "$root/artifacts/mod-bin/node.test" ./internal/hypervisor
git rev-parse HEAD > "$root/artifacts/mod-cloud-commit.txt"
