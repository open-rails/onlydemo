#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
: "${SMOKE_DATABASE_URL:?Set SMOKE_DATABASE_URL to a loopback PostgreSQL administrator connection}"
exec go run -tags smoke .
