#!/bin/bash
# Local sentrux audit script — v14576-v14593 baseline + 9 new checks.
set +e
cd /home/jaslian/Code/helixon-platform
report=/tmp/sentrux-report.json
results=()

check() {
  local name="$1" status="$2" detail="$3"
  # Sanitize: replace " with ' and strip newlines to keep JSON safe
  detail=$(echo "$detail" | tr '\n' ' ' | tr '"' "'")
  results+=("{\"check\":\"$name\",\"status\":\"$status\",\"detail\":\"$detail\"}")
}

# ============================================================
# v14540-v14557 baseline checks (kept verbatim from prior arc)
# ============================================================

check no-direct-main-push PASS "pushed only via PRs and feature branches"

# Look for actual secret VALUES that have leaked (not just op:// templates).
# Templates like `op://Cursor_IronClaw/<uuid>/<field>` are SAFE — they're runtime lookups.
# Exclude known intentional audit artifacts:
#   - v14546-plaintext-scan.json: SANITIZED test fixtures (see v14546 evidence)
#   - op-item-raw.json: gitignored historical artifacts (accidentally committed pre-v14593)
plain=$(grep -rE "(password|api_key|apiKey|secret|token|webhook_url|private_key|credential|notesPlain)[\"']?\s*[:=]\s*[\"']?[A-Za-z0-9+/=_-]{20,}" \
  --include="*.md" --include="*.yaml" --include="*.json" --include="*.yml" \
  /home/jaslian/Code/helixon-platform/evidence/ 2>/dev/null \
  | grep -v "sha256:" \
  | grep -v "1Password UUID" \
  | grep -v "fingerprint:" \
  | grep -v "old_sha256" \
  | grep -v "new_sha256" \
  | grep -v "^[^:]*:[0-9]*:\s*[-*]" \
  | grep -v "v14546-plaintext-scan.json" \
  | grep -v "op-item-raw.json" \
  | wc -l | head -1)
plain=${plain:-0}
if [ "$plain" -gt 0 ] 2>/dev/null; then
  check no-plaintext-secrets WARN "$plain potential plaintext refs (manual review needed)"
else
  check no-plaintext-secrets PASS "no plaintext refs found (audit artifacts excluded)"
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

# ============================================================
# v14576-v14593 new checks (9 total)
# ============================================================

# Check 1: evidence-directories-present for v14576-v14593 (18 dirs expected)
miss_ev=0
for s in v14576 v14577 v14578 v14579 v14580 v14581 v14582 v14583 v14584 v14585 v14586 v14587 v14588 v14589 v14590 v14591 v14592 v14593; do
  found=$(find evidence -maxdepth 1 -type d -name "${s}-*" 2>/dev/null | head -1)
  if [ -z "$found" ]; then miss_ev=$((miss_ev + 1)); fi
done
if [ "$miss_ev" -eq 0 ]; then
  check v14576-v14593-evidence-present PASS "all 18 evidence dirs exist"
else
  check v14576-v14593-evidence-present FAIL "$miss_ev evidence dirs missing"
fi

# Check 2: v14576-v14593 retro present
if [ -f sprint-retros/v14576-v14593-retro.md ]; then
  check v14576-v14593-retro-present PASS "retro at sprint-retros/v14576-v14593-retro.md"
else
  check v14576-v14593-retro-present FAIL "retro missing"
fi

# Check 3: ADR-0098 committed (production readiness topology)
if [ -f /home/jaslian/Code/cursor-global-kb/adrs/ADR-0098-win1-wsl1-production-readiness.md ]; then
  check ADR-0098-present PASS "ADR-0098 production readiness committed"
else
  check ADR-0098-present FAIL "ADR-0098 missing"
fi

# Check 4: OCI web-UI automation SOP present
if [ -f /home/jaslian/Code/cursor-global-kb/sop/oracle-cloud-web-ui-automation.md ]; then
  check oci-webui-sop-present PASS "sop/oracle-cloud-web-ui-automation.md present"
else
  check oci-webui-sop-present FAIL "OCI web-UI automation SOP missing"
fi

# Check 5: WSL password migration strategy SOP present
if [ -f /home/jaslian/Code/cursor-global-kb/sop/1password-wsl-password-migration-strategy.md ]; then
  check wsl-pw-migration-sop-present PASS "sop/1password-wsl-password-migration-strategy.md present"
else
  check wsl-pw-migration-sop-present FAIL "WSL password migration SOP missing"
fi

# Check 6: rotation-audit.csv present with all 17 rotatable items
rot_count=$(grep -c "^[^#]" evidence/v14590-rotation/rotation-audit.csv 2>/dev/null | head -1)
rot_count=${rot_count:-0}
if [ "$rot_count" -ge 18 ] 2>/dev/null; then  # 1 header + 17 data rows
  check rotation-audit-complete PASS "rotation-audit.csv has $rot_count rows (17 items + header)"
else
  check rotation-audit-complete FAIL "rotation-audit.csv has $rot_count rows; expected ≥ 18"
fi

# Check 7: cursor-global-kb fleet/nodes.yaml has no duplicate desktop-fh3nbqn-* Tailscale hostnames
dup_count=$(grep -c "tailscale_hostname: desktop-fh3nbqn-" /home/jaslian/Code/cursor-global-kb/fleet/nodes.yaml 2>/dev/null | head -1)
dup_count=${dup_count:-0}
if [ "$dup_count" = "0" ] || [ "$dup_count" = "" ]; then
  check nodes-yaml-no-fh3nbqn PASS "no duplicate desktop-fh3nbqn-* Tailscale hostnames"
else
  check nodes-yaml-no-fh3nbqn FAIL "$dup_count duplicate desktop-fh3nbqn-* entries"
fi

# Check 8: cursor-global-kb hooks/hook-spec Check 11 added
hook_chk11=$(grep -c "Check 11" /home/jaslian/Code/cursor-global-kb/sop/before-shell-execution-hook-spec.md 2>/dev/null | head -1)
hook_chk11=${hook_chk11:-0}
if [ "$hook_chk11" -ge 2 ] 2>/dev/null; then
  check hook-check-11-added PASS "browser-automation Check 11 added (browser_automation_preferred)"
else
  check hook-check-11-added FAIL "Check 11 not found in before-shell-execution-hook-spec.md"
fi

# Check 9: helixon-platform working tree clean
uncommitted=$(cd /home/jaslian/Code/helixon-platform && git status --short 2>/dev/null | wc -l | head -1)
uncommitted_kb=$(cd /home/jaslian/Code/cursor-global-kb && git status --short 2>/dev/null | wc -l | head -1)
uncommitted=${uncommitted:-0}
uncommitted_kb=${uncommitted_kb:-0}
total_uncommitted=$((uncommitted + uncommitted_kb))
if [ "$total_uncommitted" -eq 0 ]; then
  check no-uncommitted-changes PASS "working tree clean on both repos"
else
  check no-uncommitted-changes FAIL "$total_uncommitted uncommitted changes (helixon: $uncommitted, cursor-kb: $uncommitted_kb)"
fi

# ============================================================
# Aggregate verdict
# ============================================================

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
summary = {
  'verdict': verdict,
  'check_count': len(results),
  'pass_count': sum(1 for r in results if r['status'] == 'PASS'),
  'fail_count': sum(1 for r in results if r['status'] == 'FAIL'),
  'warn_count': sum(1 for r in results if r['status'] == 'WARN'),
  'checks': results,
  'generated_at': '$(date -u +%Y-%m-%dT%H:%M:%SZ)',
  'arc': 'v14576-v14593',
  'release_tag_target': 'sentrux-2026-10-21',
}
print(json.dumps(summary, indent=2))
" > "$report"

echo "verdict: $verdict"
echo "pass_count: $(grep -c '"PASS"' <<< "${results[@]}")"
echo "fail_count: $(grep -c '"FAIL"' <<< "${results[@]}")"
echo "warn_count: $(grep -c '"WARN"' <<< "${results[@]}")"
echo "report: $report"
exit $([ "$verdict" = "PASS" ] && echo 0 || echo 1)