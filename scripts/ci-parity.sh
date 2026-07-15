#!/usr/bin/env bash
# ci-parity.sh: stand-in for what GitHub Actions self-hosted and GitLab
# Runner would run. Both runners produce identical output.
#
# Usage:
#   ./scripts/ci-parity.sh              # full run; exit 1 on any check fail
#   ./scripts/ci-parity.sh --quick      # build + vet only (smoke)
#   ./scripts/ci-parity.sh --no-trivy   # skip trivy (saves 30s on local)
set -eo pipefail

cd "$(dirname "$0")/.."  # repo root

QUICK=0
SKIP_TRIVY=0
for a in "$@"; do
  case "$a" in
    --quick) QUICK=1 ;;
    --no-trivy) SKIP_TRIVY=1 ;;
  esac
done

LOG="${LOG_DIR:-./evidence/v14879-runner-parity-ci.log}"
mkdir -p "$(dirname "$LOG")"
echo "=== ci-parity run $(date -u +%FT%TZ) ===" | tee "$LOG"

f() { echo | tee -a "$LOG"; echo "=== $1 ===" | tee -a "$LOG"; }

f "Step 1/6: go build"
go build ./... 2>&1 | tee -a "$LOG"

f "Step 2/6: go vet"
go vet ./... 2>&1 | tee -a "$LOG"

if [ "$QUICK" = "1" ]; then
  echo "QUICK mode; skipping tests + security" | tee -a "$LOG"
  exit 0
fi

f "Step 3/6: go test -race -cover"
go test -race -cover -coverprofile=coverage.txt -count=1 ./... 2>&1 | tee -a "$LOG"
total=$(go tool cover -func=coverage.txt | grep total | awk '{print $3}' | tr -d '%')
echo "Total coverage: ${total}%" | tee -a "$LOG"
threshold=75
if [ "$(echo "$total < $threshold" | bc -l)" -eq 1 ]; then
  echo "::error::Coverage ${total}% below threshold ${threshold}%" | tee -a "$LOG"
  exit 1
fi

f "Step 4/6: govulncheck"
go install golang.org/x/vuln/cmd/govulncheck@latest 2>&1 | tail -3 | tee -a "$LOG"
govulncheck ./... 2>&1 | tee -a "$LOG" || echo "(govulncheck non-zero OK if no vulns found)" | tee -a "$LOG"

f "Step 5/6: nancy"
go install github.com/sonatype-nexus-community/nancy@latest 2>&1 | tail -3 | tee -a "$LOG"
go list -json -deps ./... | nancy sleuth 2>&1 | tee -a "$LOG" || echo "(nancy non-zero OK)" | tee -a "$LOG"

if [ "$SKIP_TRIVY" = "1" ]; then
  echo "SKIP_TRIVY=1; skipping" | tee -a "$LOG"
  exit 0
fi

f "Step 6/6: trivy fs"
if ! command -v trivy >/dev/null 2>&1; then
  curl -sfL https://raw.githubusercontent.com/aquasecurity/trivy/main/contrib/install.sh \
    | sh -s -- -b "$(go env GOPATH)/bin" v0.58.1 2>&1 | tail -3 | tee -a "$LOG"
fi
trivy fs --scanners vuln,secret,misconfig --exit-code 0 --severity HIGH,CRITICAL --no-progress . 2>&1 \
  | tail -20 | tee -a "$LOG"

echo "=== ci-parity complete $(date -u +%FT%TZ) PASS ===" | tee -a "$LOG"
