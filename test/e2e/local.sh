#!/usr/bin/env bash
# Runs the API and browser suites against racksail dev, without a virtual
# machine. It starts racksail dev on its own address and data directory, so
# it runs next to a development instance, and stops it afterwards.
#
#   test/e2e/local.sh [BINARY]
#
# E2E_ADDR and E2E_DATA choose the address and data directory; the binary is
# bin/racksail by default, which make e2e-local builds with the UI.
set -euo pipefail

cd "$(dirname "$0")/../.."
binary=${1:-bin/racksail}
addr=${E2E_ADDR:-127.0.0.1:7170}
data=${E2E_DATA:-.dev-e2e}
logs=build/e2e-local
mkdir -p "$logs"

if [ ! -x "$binary" ]; then
	echo "local.sh: $binary isn't built; run make build first" >&2
	exit 1
fi

"$binary" dev --addr "$addr" --edge-http 127.0.0.1:0 --edge-https 127.0.0.1:0 --data "$data" >"$logs/racksail-dev.log" 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true' EXIT

for _ in $(seq 1 60); do
	if curl --fail --silent "http://$addr/api/v1/health" >/dev/null 2>&1; then
		break
	fi
	if ! kill -0 "$pid" 2>/dev/null; then
		echo "local.sh: racksail dev stopped; its log is $logs/racksail-dev.log" >&2
		tail -n 20 "$logs/racksail-dev.log" >&2
		exit 1
	fi
	sleep 1
done
curl --fail --silent --show-error "http://$addr/api/v1/health" >/dev/null

export RACKSAIL_E2E_URL="http://$addr"
status=0
go test -count=1 ./test/e2e/api || status=1
(
	cd test/e2e
	RACKSAIL_E2E_REPORT="../../$logs/playwright-report" RACKSAIL_E2E_RESULTS="../../$logs/playwright-results" npx playwright test
) || status=1
exit "$status"
