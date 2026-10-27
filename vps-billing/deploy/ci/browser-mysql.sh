#!/usr/bin/env bash
# Run only beneath mysql-native.sh. Never installs host trust or a browser.
set +x
set -euo pipefail
umask 077
exec python3 "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/browser-mysql.py" "$@"
