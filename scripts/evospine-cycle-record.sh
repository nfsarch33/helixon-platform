#!/usr/bin/env bash
# evospine-cycle-record.sh — record an evospine cycle for v14549.
set -euo pipefail

REPO_DIR="${EVOSPINE_REPO_DIR:-$HOME/Code/helixon-platform}"
# The cycle file is STATE, not source: it lives under the operator's logs, the
# same file `helix-dev-tools eval ab --cycle-record` appends to, never inside a
# repository checkout.
NDJSON="${EVOSPINE_CYCLES_FILE:-$HOME/logs/runx/evospine-cycles.ndjson}"
mkdir -p "$(dirname "$NDJSON")"
TS=$(date -u +%Y%m%dT%H%M%S)
CYCLE_ID="evospine-${TS}-$(openssl rand -hex 3 2>/dev/null || echo xxxxxx)"
STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.%6NZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)

# === OBS ===
SERVICES=()
for svc in engram.service sprintboard-api.service llm-router.service svcregistryd.service alertmanager.service; do
  state=$(XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user is-active "$svc" 2>/dev/null || echo "inactive")
  SERVICES+=("{\"name\":\"$svc\",\"state\":\"$state\"}")
done
OBS_JSON=$(printf '%s,' "${SERVICES[@]}")
OBS_JSON="[${OBS_JSON%,}]"
OBS_STATUS="ok"
OBS_RETURNCODE=0

# === HYPOTHESIZE ===
HYP_TEXT="v14549 self-check: all 5 fleet services active, AlertManager routing alerts, timers running, secrets via 1Password UUIDs."
HYP_STATUS="ok"

# === PATCH (informational only) ===
PATCH_STATUS="ok"
PATCH_APPLIED=false
PATCH_REASON="v14549 cycle is observational; v14556 will add weekly cron trigger"
PATCH_HYP="$HYP_TEXT"

# === EVAL ===
DOCTOR_LOG="${EVOSPINE_DOCTOR_LOG:-$HOME/logs/fleet-doctor.log}"
EVAL_TESTS=0
EVAL_FAIL=0
EVAL_SUMMARY=""

# 1) doctor verdict
if [[ -f "$DOCTOR_LOG" ]]; then
  EVAL_TESTS=$((EVAL_TESTS+1))
  if grep -q "VERDICT: GREEN" "$DOCTOR_LOG"; then
    EVAL_SUMMARY+="doctor:GREEN,"
  else
    EVAL_SUMMARY+="doctor:NON-GREEN,"
    EVAL_FAIL=$((EVAL_FAIL+1))
  fi
fi

# 2) llm-router healthz
HTTP=$(curl --max-time 3 -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8787/healthz 2>/dev/null || echo 000)
EVAL_TESTS=$((EVAL_TESTS+1))
if [[ "$HTTP" == "200" ]]; then
  EVAL_SUMMARY+="llm-router:200,"
else
  EVAL_SUMMARY+="llm-router:$HTTP,"
  EVAL_FAIL=$((EVAL_FAIL+1))
fi

# 3) engramd healthz
HTTP=$(curl --max-time 3 -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8280/healthz 2>/dev/null || echo 000)
EVAL_TESTS=$((EVAL_TESTS+1))
if [[ "$HTTP" == "200" ]]; then
  EVAL_SUMMARY+="engramd:200,"
else
  EVAL_SUMMARY+="engramd:$HTTP,"
  EVAL_FAIL=$((EVAL_FAIL+1))
fi

# 4) sprintboard-api
HTTP=$(curl --max-time 3 -s -o /dev/null -w "%{http_code}" http://127.0.0.1:9400/api/v1/health 2>/dev/null || echo 000)
EVAL_TESTS=$((EVAL_TESTS+1))
if [[ "$HTTP" == "200" ]]; then
  EVAL_SUMMARY+="sprintboard:200,"
else
  EVAL_SUMMARY+="sprintboard:$HTTP,"
  EVAL_FAIL=$((EVAL_FAIL+1))
fi

# 5) alertmanager
HTTP=$(curl --max-time 3 -s -o /dev/null -w "%{http_code}" http://127.0.0.1:9093/-/healthy 2>/dev/null || echo 000)
EVAL_TESTS=$((EVAL_TESTS+1))
if [[ "$HTTP" == "200" ]]; then
  EVAL_SUMMARY+="alertmanager:200,"
else
  EVAL_SUMMARY+="alertmanager:$HTTP,"
  EVAL_FAIL=$((EVAL_FAIL+1))
fi

# The eval status is DERIVED from the checks. It used to be the literal "ok"
# whatever failed, and the record said "complete" over a failing eval.
EVAL_STATUS="ok"
CYCLE_STATUS="complete"
if [[ "$EVAL_FAIL" -gt 0 ]]; then
  EVAL_STATUS="failed"
  CYCLE_STATUS="failed"
fi
EVAL_RETURNCODE=$EVAL_FAIL
EVAL_SUMMARY="${EVAL_SUMMARY%,} ($EVAL_TESTS tests, $EVAL_FAIL failed)"

# === COMMIT ===
COMMIT_EMPTY='{}'
# The repository slug is read from the checkout's remote, never written as a
# literal: this file lives in a public repository.
REPO_SLUG=$(git -C "$REPO_DIR" remote get-url origin 2>/dev/null | sed -E 's#.*[:/]([^/]+/[^/]+?)(\.git)?$#\1#' | sed 's/\.git$//')
REPO_SLUG=${REPO_SLUG:-unknown}
# Insertion sequence: the wall clock on this estate has stepped backwards, so
# order is the previous record's seq + 1, never a timestamp.
# A missing file is seq 0, not a failed pipeline (set -e -o pipefail is on).
LAST_SEQ=$({ grep -o '"seq":[0-9]*' "$NDJSON" 2>/dev/null || true; } | tail -1 | tr -dc '0-9')
SEQ=$(( ${LAST_SEQ:-0} + 1 ))
FINISHED_AT=$(date -u +%Y-%m-%dT%H:%M:%S.%6NZ 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)

RECORD="{\"seq\":$SEQ,\"cycle_id\":\"$CYCLE_ID\",\"repo\":\"$REPO_SLUG\",\"branch\":\"$(git -C "$REPO_DIR" rev-parse --abbrev-ref HEAD 2>/dev/null)\",\"started_at\":\"$STARTED_AT\",\"obs\":{\"stage\":\"obs\",\"status\":\"$OBS_STATUS\",\"returncode\":$OBS_RETURNCODE,\"items\":${#SERVICES[@]},\"data\":$OBS_JSON},\"hypothesis\":{\"stage\":\"hypothesize\",\"status\":\"$HYP_STATUS\",\"hypothesis\":\"$HYP_TEXT\"},\"patch\":{\"stage\":\"patch\",\"status\":\"$PATCH_STATUS\",\"applied\":$PATCH_APPLIED,\"reason\":\"$PATCH_REASON\",\"hypothesis\":\"$PATCH_HYP\"},\"eval\":{\"stage\":\"eval\",\"status\":\"$EVAL_STATUS\",\"returncode\":$EVAL_RETURNCODE,\"summary\":\"$EVAL_SUMMARY\",\"stderr_tail\":\"\"},\"commit\":$COMMIT_EMPTY,\"finished_at\":\"$FINISHED_AT\",\"status\":\"$CYCLE_STATUS\"}"

echo "$RECORD" >> "$NDJSON"

echo "=== evospine cycle $CYCLE_ID $CYCLE_STATUS ==="
echo "result: $EVAL_SUMMARY"
echo "record appended to: $NDJSON"
# A failed eval is a failed cycle: exit non-zero so a timer, a doctor or a
# human sees it, instead of exit 0 with "complete" in the file.
if [[ "$EVAL_FAIL" -gt 0 ]]; then
  exit 1
fi
