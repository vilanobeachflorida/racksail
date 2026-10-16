#!/usr/bin/env bash
# Runs the API and browser suites against racksail dev, without a virtual
# machine. It starts racksail dev on its own address and data directory, so
# it runs next to a development instance, and stops it afterwards.
#
#   test/e2e/local.sh [BINARY [PLAYWRIGHT ARGUMENTS...]]
#
# E2E_ADDR and E2E_DATA choose the address and data directory; the binary is
# bin/racksail by default, which make e2e-local builds with the UI. Further
# arguments go to Playwright, such as the files to run.
set -euo pipefail

cd "$(dirname "$0")/../.." || exit 1
binary=${1:-bin/racksail}
[ $# -eq 0 ] || shift
addr=${E2E_ADDR:-127.0.0.1:7170}
data=${E2E_DATA:-.dev-e2e}
# A second panel signs people in as a server does, for the sign-in suite;
# it starts afresh every time, with no admin yet.
signin_addr=${E2E_SIGNIN_ADDR:-127.0.0.1:7171}
signin_data=${E2E_SIGNIN_DATA:-.dev-e2e-sign-in}
logs=build/e2e-local
mkdir -p "$logs"

if [ ! -x "$binary" ]; then
	echo "local.sh: $binary isn't built; run make build first" >&2
	exit 1
fi

"$binary" dev --addr "$addr" --edge-http 127.0.0.1:0 --edge-https 127.0.0.1:0 --data "$data" >"$logs/racksail-dev.log" 2>&1 &
pid=$!
mkdir -p "$signin_data"
"$binary" dev --sign-in --fresh --addr "$signin_addr" --edge-http 127.0.0.1:0 --edge-https 127.0.0.1:0 --data "$signin_data" \
	>"$logs/racksail-dev-sign-in.log" 2>&1 &
signin_pid=$!
trap 'kill "$pid" "$signin_pid" 2>/dev/null || true; wait "$pid" "$signin_pid" 2>/dev/null || true' EXIT

# ready waits for a panel to answer, or says why it never will.
ready() {
	for _ in $(seq 1 60); do
		if curl --fail --silent "http://$1/api/v1/health" >/dev/null 2>&1; then
			return 0
		fi
		if ! kill -0 "$2" 2>/dev/null; then
			echo "local.sh: racksail dev stopped; its log is $3" >&2
			tail -n 20 "$3" >&2
			exit 1
		fi
		sleep 1
	done
	curl --fail --silent --show-error "http://$1/api/v1/health" >/dev/null
}
ready "$addr" "$pid" "$logs/racksail-dev.log"
ready "$signin_addr" "$signin_pid" "$logs/racksail-dev-sign-in.log"

export RACKSAIL_E2E_URL="http://$addr" RACKSAIL_E2E_DEV=1
export RACKSAIL_E2E_SIGNIN_URL="http://$signin_addr" RACKSAIL_E2E_SIGNIN_DATA="$(cd "$signin_data" && pwd)"
status=0
go test -count=1 ./test/e2e/api || status=1
(
	cd test/e2e || exit 1
	RACKSAIL_E2E_REPORT="../../$logs/playwright-report" RACKSAIL_E2E_RESULTS="../../$logs/playwright-results" npx playwright test "$@"
) || status=1
exit "$status"
