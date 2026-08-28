#!/usr/bin/env bash
# Coverage gate for CI (.github/workflows/ci.yml): the WHOLE-MODULE
# aggregate covered-statement percentage -- go tool cover -func's "total:"
# line over a -coverpkg=./... profile spanning the untagged, integration,
# and contract test tiers together -- must be at least THRESHOLD. This is
# an aggregate gate, not a per-package one: an individual package's own
# coverage view (e.g. a thin adapter like gittest) can sit well below this
# number as long as the module-wide total clears it.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

# Single, easily-adjustable knob: bump this as coverage improves.
THRESHOLD=90.0

go test -race -tags=integration,contract -coverpkg=./... -coverprofile=coverage.out ./...

total=$(go tool cover -func=coverage.out | awk '/^total:/ {gsub("%", "", $3); print $3}')

echo "Aggregate coverage: ${total}% (threshold: ${THRESHOLD}%)"

if ! awk -v total="$total" -v threshold="$THRESHOLD" 'BEGIN { exit (total + 0 >= threshold + 0) ? 0 : 1 }'; then
  echo "Aggregate coverage ${total}% is below the ${THRESHOLD}% threshold" >&2
  exit 1
fi
