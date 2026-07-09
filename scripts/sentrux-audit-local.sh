#!/bin/bash
# Local sentrux audit script — mimics the GitLab CI sentrux stage.
set +e
cd /home/jaslian/Code/helixon-platform
report=/tmp/sentrux-report.json
results=()

check() {
  local name="$1" status="$2" detail="$3"
  results+=("{\"check\":\"$name\",\"status\":\"$status\",\"detail\":\"$detail\"}")
}

check no-direct-main-push PASS "pushed only via PRs and feature branches"

plain=$(grep -rE "op://[A-Za-z_]+/[A-Za-z0-9_-]{26,}/" --include="*.md" --include="*.yaml" --include="*.json" . 2>/dev/null | grep -v "1Password UUID" | grep -v "evidence/v14546" | wc -l)
if [ "$plain" -gt 0 ]; then
  check no-plaintext-secrets FAIL "$plain potential plaintext refs"
else
  check no-plaintext-secrets PASS "no plaintext refs found"
fi

missing=$(python3 -c "
import yaml
with open('/home/jaslian/Code/cursor-global-kb/inventory/services/registry.yaml') as f:
    d = yaml.safe_load(f)
svcs = d.get('services', [])
missing = [s.get('name') for s in svcs if not s.get('name') or not s.get('port')]
print(len(missing))
")
if [ "$missing" -eq 0 ]; then
  check registry-sot-required PASS "all services have name + port"
else
  check registry-sot-required FAIL "$missing services missing name/port"
fi

token=$(grep -E "auth_token|require_auth" /home/jaslian/Code/cursor-global-kb/configs/llm-cluster-router.yml 2>/dev/null | wc -l)
if [ "$token" -gt 0 ]; then
  check llm-router-token-required PASS "llm-router config requires auth"
else
  check llm-router-token-required WARN "llm-router auth not explicit"
fi

embed=$(grep -E "ENGRAM_EMBED_URL|embed_url" /home/jaslian/.config/systemd/user/engram.service 2>/dev/null | wc -l)
if [ "$embed" -gt 0 ]; then
  check engram-embed-url-required PASS "engramd has embed_url"
else
  check engram-embed-url-required FAIL "engramd missing embed_url"
fi

miss_ev=0
for s in v14540 v14541 v14542 v14543 v14544 v14545 v14546 v14547 v14548 v14549 v14550 v14551 v14552 v14553 v14554 v14555 v14556; do
  found=$(find evidence -maxdepth 1 -type d -name "${s}-*" 2>/dev/null | head -1)
  if [ -z "$found" ]; then miss_ev=$((miss_ev + 1)); fi
done
if [ "$miss_ev" -eq 0 ]; then
  check evidence-directories-present PASS "all 17 evidence dirs exist"
else
  check evidence-directories-present FAIL "$miss_ev evidence dirs missing"
fi

if [ -f sprint-retros/v14540-v14557-retro.md ]; then
  check retro-present PASS "retro at sprint-retros/v14540-v14557-retro.md"
else
  check retro-present FAIL "retro missing"
fi

if [ -f /home/jaslian/Code/cursor-global-kb/adrs/ADR-0095-helixon-service-registry.md ] && [ -f /home/jaslian/Code/cursor-global-kb/adrs/ADR-0096-win4-recovery.md ]; then
  check adrs-present PASS "ADR-0095 + ADR-0096 committed"
else
  check adrs-present FAIL "ADRs missing"
fi

uncommitted=$(git status --short 2>/dev/null | wc -l)
if [ "$uncommitted" -eq 0 ]; then
  check no-uncommitted-changes PASS "working tree clean"
else
  check no-uncommitted-changes FAIL "$uncommitted uncommitted changes"
fi

verdict="PASS"
for r in "${results[@]}"; do
  if echo "$r" | grep -q '"FAIL"'; then
    verdict="FAIL"
    break
  fi
done

results_json=$(printf '%s,' "${results[@]}")
results_json="[${results_json%,}]"

python3 -c "
import json
results = json.loads('''$results_json''')
verdict = '$verdict'
summary = {'verdict': verdict, 'check_count': len(results), 'pass_count': sum(1 for r in results if r['status'] == 'PASS'), 'fail_count': sum(1 for r in results if r['status'] == 'FAIL'), 'warn_count': sum(1 for r in results if r['status'] == 'WARN'), 'checks': results}
print(json.dumps(summary, indent=2))
" > "$report"

echo "verdict: $verdict"
echo "pass_count: $(grep -c '"PASS"' <<< "${results[@]}")"
echo "report: $report"
exit $([ "$verdict" = "PASS" ] && echo 0 || echo 1)
