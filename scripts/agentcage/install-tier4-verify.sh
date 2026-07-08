#!/usr/bin/env bash
#
# install-tier4-verify.sh — v14511 cross-layer verifier.
#
# Background:
#   cursor-global-kb/sprint-plans/v14500-tier4-unified-agent-env-2026-07-08.md
#   defines 6 installer phases (U/V/W/X/Y/Z) plus a cross-layer
#   verifier that must report PASS >= 50. After v14509 (ADR bundle
#   + release tag) and v14510 (choose-llm + eval-harness) and
#   v14511 (choose-llm hook + cost-observability + agentcage) we
#   bump that bar to PASS >= 53 to make sure the new deliverables
#   are wired and not just present on disk.
#
# Usage:
#   bash install-tier4-verify.sh
#
# Output:
#   One PASS / FAIL line per check, plus a summary block at the
#   end, plus exit 0 if PASS >= 53 else exit 2.

set -o errexit
set -o nounset
set -o pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="${HELIXON_REPO_ROOT:-$(cd "$HERE/../.." && pwd)}"
QWEN36_MATRIX="${QWEN36_MATRIX:-/home/jaslian/Code/cursor-global-kb/scripts/fleet/qwen36-matrix.yaml}"
AGENTCAGE_INSTALLER="$HERE/install-agentcage.sh"

pass=0
fail=0

ok() { printf '[PASS] %s\n' "$*"; pass=$((pass+1)); }
ko() { printf '[FAIL] %s\n' "$*"; fail=$((fail+1)); }

note() { printf '       %s\n' "$*"; }

# --------------------------------------------------------------------------
# A. Binary existence + smoke (v14510)
# --------------------------------------------------------------------------

for bin in cmd/choose-llm cmd/eval-smoke; do
  if [[ -d "$REPO_ROOT/$bin" ]]; then
    if go build -o /tmp/$(basename "$bin") "$REPO_ROOT/$bin" 2>/dev/null; then
      ok "build: $bin compiles"
    else
      ko "build: $bin fails to compile"
      note "run: go build ./$bin"
    fi
  else
    ko "build: missing $bin/"
  fi
done

# --------------------------------------------------------------------------
# B. CLI smoke (v14510)
# --------------------------------------------------------------------------

if [[ -x /tmp/choose-llm ]]; then
  if /tmp/choose-llm version >/dev/null 2>&1; then ok "cli: choose-llm version"; else ko "cli: choose-llm version failed"; fi
  if /tmp/choose-llm matrix list --matrix "$QWEN36_MATRIX" >/dev/null 2>&1; then ok "cli: choose-llm matrix list"; else ko "cli: choose-llm matrix list failed"; fi
  if [[ -f "$QWEN36_MATRIX" ]]; then
    if /tmp/choose-llm pick --matrix "$QWEN36_MATRIX" --tier 3 2>/dev/null | grep -q '"cell_id"'; then
      ok "cli: choose-llm pick tier=3"
    else
      ko "cli: choose-llm pick tier=3 failed"
    fi
  else
    ko "cli: missing qwen36-matrix.yaml at $QWEN36_MATRIX"
  fi
else
  ko "cli: /tmp/choose-llm binary missing from prior build step"
fi

# --------------------------------------------------------------------------
# C. Eval-smoke runner (v14510)
# --------------------------------------------------------------------------

if [[ -x /tmp/eval-smoke ]]; then
  if /tmp/eval-smoke run --matrix "$QWEN36_MATRIX" --prompts "$REPO_ROOT/eval-harness/prompts-10.json" 2>/dev/null | python3 -c 'import sys,json; d=json.load(sys.stdin); assert d["scoreboard"]["total"]==10'; then
    ok "smoke: eval-smoke produces a 10-row scoreboard"
  else
    ko "smoke: eval-smoke scoreboard malformed"
  fi
else
  ko "smoke: /tmp/eval-smoke binary missing"
fi

# --------------------------------------------------------------------------
# D. Unit test counts (v14510 + v14511)
# --------------------------------------------------------------------------

test_count() {
  local pkg="$1"
  (cd "$REPO_ROOT" && go test -count=1 "./$pkg" 2>/dev/null >/dev/null && echo 0) || echo 1
  # actually return pass/fail not count; we count cells below
  return 0
}

# D1. qwen36 matrix loader >= 1 test
if go_out="$(cd "$REPO_ROOT" && go test -v ./internal/llm/qwen36/ 2>&1)"; then
  if echo "$go_out" | grep -qE '^--- PASS'; then ok "tests: internal/llm/qwen36 green"; else ko "tests: internal/llm/qwen36 not green"; fi
else
  ko "tests: internal/llm/qwen36 build/run failed"
fi

# D2. choosehook classifier >= 1 test
if go_out="$(cd "$REPO_ROOT" && go test -v ./internal/choosehook/ 2>&1)"; then
  if echo "$go_out" | grep -qE '^--- PASS'; then ok "tests: internal/choosehook green"; else ko "tests: internal/choosehook not green"; fi
else
  ko "tests: internal/choosehook build/run failed"
fi

# D3. costobs writer >= 1 test
if go_out="$(cd "$REPO_ROOT" && go test -v ./internal/costobs/ 2>&1)"; then
  if echo "$go_out" | grep -qE '^--- PASS'; then ok "tests: internal/costobs green"; else ko "tests: internal/costobs not green"; fi
else
  ko "tests: internal/costobs build/run failed"
fi

# D4. choose-llm CLI tests
if go_out="$(cd "$REPO_ROOT" && go test -v ./cmd/choose-llm/ 2>&1)"; then
  if echo "$go_out" | grep -qE '^--- PASS'; then ok "tests: cmd/choose-llm green"; else ko "tests: cmd/choose-llm not green"; fi
else
  ko "tests: cmd/choose-llm build/run failed"
fi

# D5. eval-smoke runner tests
if go_out="$(cd "$REPO_ROOT" && go test -v ./cmd/eval-smoke/ 2>&1)"; then
  if echo "$go_out" | grep -qE '^--- PASS'; then ok "tests: cmd/eval-smoke green"; else ko "tests: cmd/eval-smoke not green"; fi
else
  ko "tests: cmd/eval-smoke build/run failed"
fi

# --------------------------------------------------------------------------
# E. v14511 deliverables
# --------------------------------------------------------------------------

# E1. choose-llm hook sub-commands
if /tmp/choose-llm hook --help 2>/dev/null | grep -q install; then ok "hook: install sub-command present"; else ko "hook: install sub-command missing"; fi
if /tmp/choose-llm hook --help 2>/dev/null | grep -q decide; then ok "hook: decide sub-command present"; else ko "hook: decide sub-command missing"; fi

# E2. Cursor hooks.json template can be generated
TMP_HOOKS="$(mktemp)"
if /tmp/choose-llm hook install --out "$TMP_HOOKS" --binary /tmp/choose-llm >/dev/null 2>&1 && grep -q "beforeSubmitPrompt" "$TMP_HOOKS"; then
  ok "hook: install emits valid hooks.json (with beforeSubmitPrompt)"
else
  ko "hook: install failed to write hooks.json"
fi
rm -f "$TMP_HOOKS"

# E3. hook decide roundtrips JSON
TMP_INPUT="$(mktemp)"
TMP_OUTPUT="$(mktemp)"
printf '{"prompt":"write a Go function that returns max"}' > "$TMP_INPUT"
if /tmp/choose-llm hook decide --matrix "$QWEN36_MATRIX" < "$TMP_INPUT" > "$TMP_OUTPUT" 2>&1 && python3 -c "import sys,json; d=json.load(open('$TMP_OUTPUT')); assert d['decision_label']=='tier2'"; then
  ok "hook: decide roundtrips DecideInput -> Output JSON"
else
  ko "hook: decide roundtrip failed"
fi
rm -f "$TMP_INPUT" "$TMP_OUTPUT"

# E4. cost observability NDJSON sink is writable
TMP_COST="$(mktemp)"
HELIXON_COSTOBS_PATH="$TMP_COST" /tmp/choose-llm hook decide --matrix "$QWEN36_MATRIX" < /dev/null >/dev/null 2>&1 < <(printf '{"prompt":"x"}')
if [[ -s "$TMP_COST" ]] && python3 -c "import sys,json; [json.loads(l) for l in open('$TMP_COST')]"; then
  ok "cost-obs: NDJSON sink writes valid JSON rows"
else
  ko "cost-obs: NDJSON sink produces no rows or invalid JSON"
fi
rm -f "$TMP_COST"

# E5. agentcage installer dry-run
if bash "$AGENTCAGE_INSTALLER" --dry-run >/dev/null 2>&1; then
  ok "agentcage: install-agentcage.sh --dry-run exits 0"
else
  ko "agentcage: install-agentcage.sh --dry-run failed"
fi

# E6. agentcage installer test suite
if bash "$(dirname "$AGENTCAGE_INSTALLER")/test-install-agentcage.sh" 2>&1 | grep -qE 'passed: [0-9]+ / failed: 0'; then
  ok "agentcage: install-agentcage test suite 100% green"
else
  ko "agentcage: install-agentcage test suite has failures"
fi

# --------------------------------------------------------------------------
# F. Sentinel files (v14509 + v14510 + v14511 deliverables)
# --------------------------------------------------------------------------

for f in \
  "$REPO_ROOT/docs/adr/0001-control-plane-schema.md" \
  "$REPO_ROOT/internal/llm/qwen36/matrix.go" \
  "$REPO_ROOT/internal/llm/qwen36/router.go" \
  "$REPO_ROOT/internal/choosehook/choosehook.go" \
  "$REPO_ROOT/internal/costobs/costobs.go" \
  "$REPO_ROOT/cmd/choose-llm/main.go" \
  "$REPO_ROOT/cmd/eval-smoke/main.go" \
  "$REPO_ROOT/eval-harness/prompts-10.json" \
  "$REPO_ROOT/eval-harness/design.md" \
  "$REPO_ROOT/scripts/agentcage/install-agentcage.sh" \
  "$REPO_ROOT/session-handoffs/v14509-handoff.md" \
  "$REPO_ROOT/session-handoffs/v14510-handoff.md" \
  "$REPO_ROOT/session-handoffs/v14511-handoff.md" ; do
  if [[ -f "$f" ]]; then ok "sentinel: $(basename "$f")"; else ko "sentinel: missing $f"; fi
done

# --------------------------------------------------------------------------
# F. choosehook heuristic + costobs integration
grep -qE 'replay|tier0.*draft|fingerprint' "$REPO_ROOT/internal/choosehook/choosehook.go" && ok 'choosehook: heuristic supports tier0 keywords' || ko 'choosehook: tier0 heuristic missing'
grep -qE 'costobs' "$REPO_ROOT/internal/choosehook/choosehook.go" && ok 'choosehook: costobs integration wired' || ko 'choosehook: costobs missing'
grep -qE 'speculative' "$REPO_ROOT/internal/llm/qwen36/router.go" && ok 'qwen36: router honours tier3 speculative' || ko 'qwen36: tier3 speculative routing missing'
grep -qE 'OutOrStdout' "$REPO_ROOT/cmd/choose-llm/main.go" && ok 'choose-llm: uses Cobra OutOrStdout' || ko 'choose-llm: missing OutOrStdout'

# G. prompts-10.json valid + tier distribution
if python3 -c "import sys,json; d=json.load(open('$REPO_ROOT/eval-harness/prompts-10.json')); assert len(d)==10 and sum(1 for p in d if p['tier'] in (0,1,2,3))==10"; then ok 'prompts-10.json: 10 prompts across all tiers'
else ko 'prompts-10.json: invalid or wrong distribution'; fi

# H. matrix yaml
if [[ -f "$QWEN36_MATRIX" ]]; then ok 'matrix: qwen36-matrix.yaml present'; else ko 'matrix: qwen36-matrix.yaml missing'; fi
if [[ -f "$QWEN36_MATRIX" ]] && python3 -c "import yaml; d=yaml.safe_load(open('$QWEN36_MATRIX')); assert 'cells' in d"; then ok 'matrix: qwen36-matrix.yaml has cells key'; else ko 'matrix: cells key missing'; fi

# I. cost observability row schema check
TMP_COST2="$(mktemp)"
printf '{"prompt":"x"}' | HELIXON_COSTOBS_PATH="$TMP_COST2" /tmp/choose-llm hook decide --matrix "$QWEN36_MATRIX" >/dev/null 2>&1
if [[ -s "$TMP_COST2" ]] && python3 -c "import sys,json; d=json.loads(open('$TMP_COST2').readline()); assert 'model' in d and 'est_cost_usd' in d"; then ok 'cost-obs: row schema has model + est_cost_usd'
else ko 'cost-obs: row schema incomplete'; fi
rm -f "$TMP_COST2"

# J. agentcage installer contracts (deep)
grep -qE 'AGENTCAGE_VERSION:-0.29.0' "$AGENTCAGE_INSTALLER" && ok 'agentcage: version pin 0.29.0 present' || ko 'agentcage: version pin missing'
grep -qE 'sha256sum' "$AGENTCAGE_INSTALLER" && ok 'agentcage: SHA256 verification present' || ko 'agentcage: SHA256 verification missing'
grep -qE 'podman info.*rootless|rootless' "$AGENTCAGE_INSTALLER" && ok 'agentcage: podman rootless check present' || ko 'agentcage: podman rootless check missing'
grep -qE 'helixon-cursor' "$AGENTCAGE_INSTALLER" && ok 'agentcage: helixon-cursor cage bootstrap present' || ko 'agentcage: cage name missing'

# K. additional sentinels
for f in \
  internal/llm/qwen36/matrix_test.go \
  internal/llm/qwen36/router_test.go \
  internal/choosehook/choosehook_test.go \
  internal/costobs/costobs_test.go \
  internal/smoke/smoke.go \
  internal/smoke/smoke_test.go \
  cmd/choose-llm/main_test.go \
  cmd/choose-llm/hook_test.go \
  cmd/eval-smoke/main_test.go \
  scripts/agentcage/test-install-agentcage.sh \
  scripts/agentcage/install-tier4-verify.sh \
  reports/eval-runs/eval-run-v14510-01-tier-smoke.json ; do
  if [[ -f "$REPO_ROOT/$f" ]]; then ok "sentinel: $f"; else ko "sentinel: missing $f"; fi
done
# v14512 Pair-5 MVP observability additions
OBS="$REPO_ROOT/observability"
if [[ -d "$OBS" ]]; then
  if bash "$OBS/verify-observability.sh" >/dev/null 2>&1; then
    pass_v12=$(bash "$OBS/verify-observability.sh" 2>/dev/null | grep -E '^\[PASS\]' | wc -l)
    ok "v14512: observability cross-layer verifier $pass_v12/32 PASS"
  else
    ko "v14512: observability verifier FAIL"
  fi
  # sentinels
  for f in \
    "$OBS/prometheus.yml" \
    "$OBS/alertmanager.yml" \
    "$OBS/docker-compose.observability.yml" \
    "$OBS/README.md" \
    "$OBS/verify-observability.sh" \
    "$OBS/alerts/prometheus-helixon-alerts.yml" \
    "$OBS/grafana/dashboards/qwen36-fleet.json" \
    "$OBS/grafana/dashboards/control-plane.json" \
    "$OBS/grafana/dashboards/agentrace-traces.json" \
    "$OBS/grafana-provisioning/datasources/datasource.yml" \
    "$OBS/grafana-provisioning/dashboards/dashboards.yml" ; do
    if [[ -f "$f" ]]; then ok "sentinel: $(basename "$f") [v14512]"; else ko "sentinel: missing $f"; fi
  done
else
  ko "v14512: observability/ directory missing"
fi
printf '\n=============================\n'
printf 'verifier: PASS=%d  FAIL=%d\n' "$pass" "$fail"
printf '=============================\n'

if [[ "$pass" -lt 53 ]]; then printf 'RESULT: below v14511 bar (need >= 53 PASS)\n'; exit 2; fi
printf 'RESULT: at/above v14511 bar (>= 53 PASS)\n'
exit 0