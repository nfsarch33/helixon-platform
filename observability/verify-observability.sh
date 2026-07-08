#!/usr/bin/env bash
#
# verify-observability.sh — v14512 cross-layer verifier.
#
# Checks the observability sidecar is wired correctly:
#   - 3 Grafana dashboards present + valid JSON
#   - Prometheus alert rules valid YAML, contain required P0/P1 alerts
#   - Prometheus scrape config valid YAML
#   - Alertmanager routing valid YAML
#   - docker-compose.observability.yml services resolve

set -o errexit
set -o nounset
set -o pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="${HELIXON_REPO_ROOT:-$(cd "$HERE/.." && pwd)}"
OBS="$HERE"

pass=0; fail=0
ok() { printf '[PASS] %s\n' "$*"; pass=$((pass+1)); }
ko() { printf '[FAIL] %s\n' "$*"; fail=$((fail+1)); }

# A. Grafana dashboards parse + have correct title + uid
for d in qwen36-fleet control-plane agentrace-traces; do
  f="$OBS/grafana/dashboards/$d.json"
  if [[ -f "$f" ]] && python3 -c "import sys,json; d=json.load(open('$f')); assert d['title']=='$d' and d['uid']=='$d' and len(d['panels'])>=3"; then
    ok "grafana: $d dashboard valid (title+uid+panels)"
  else
    ko "grafana: $d dashboard missing or malformed"
  fi
done

# B. dashboard contains at least one stat panel + one timeseries panel
for d in qwen36-fleet control-plane agentrace-traces; do
  f="$OBS/grafana/dashboards/$d.json"
  if python3 -c "import json; d=json.load(open('$f')); assert any(p['type']=='stat' for p in d['panels']) and any(p['type']=='timeseries' for p in d['panels'])"; then
    ok "grafana: $d has stat + timeseries panels"
  else
    ko "grafana: $d missing stat/timeseries panel"
  fi
done

# C. prometheus-helixon-alerts.yml valid + has P0 + P1 alerts
if python3 -c "
import yaml
d=yaml.safe_load(open('$OBS/alerts/prometheus-helixon-alerts.yml'))
rules=[r for g in d['groups'] for r in g['rules']]
assert any('page' in (r.get('labels',{}).get('severity') or '') for r in rules), 'no P0 (page) alerts'
assert any('notify' in (r.get('labels',{}).get('severity') or '') for r in rules), 'no P1 (notify) alerts'
assert any('log' in (r.get('labels',{}).get('severity') or '') for r in rules), 'no P2 (log) alerts'
"; then
  ok "alerts: P0 + P1 + P2 severities present"
else
  ko "alerts: missing P0/P1/P2 severity"
fi

# D. required alert names exist
for alert in Qwen36ZeroCellsReady Qwen36High5xx ControlPlaneDown FleetHeartbeatsStale AgentraceHighErrorRate; do
  if grep -qE "alert: $alert\b" "$OBS/alerts/prometheus-helixon-alerts.yml"; then
    ok "alerts: $alert defined"
  else
    ko "alerts: $alert missing"
  fi
done

# E. prometheus.yml valid + has control-plane job
if python3 -c "
import yaml
d=yaml.safe_load(open('$OBS/prometheus.yml'))
assert any(j['job_name']=='helixon-control-plane' for j in d['scrape_configs'])
assert any(j['job_name']=='qwen36-cells' for j in d['scrape_configs'])
assert any(j['job_name']=='agentrace-fleet' for j in d['scrape_configs'])
"; then
  ok "prometheus.yml: control-plane + qwen36 + agentrace jobs present"
else
  ko "prometheus.yml: required jobs missing"
fi

# F. alertmanager.yml valid + routes severity correctly
if python3 -c "
import yaml
d=yaml.safe_load(open('$OBS/alertmanager.yml'))
routes=d['route']['routes']
assert any('page' in str(m) for r in routes for m in r['matchers']), 'no page route'
"; then
  ok "alertmanager.yml: routes severity=page"
else
  ko "alertmanager.yml: page route missing"
fi

# G. docker-compose.observability.yml services
if python3 -c "
import yaml
d=yaml.safe_load(open('$OBS/docker-compose.observability.yml'))
assert 'prometheus' in d['services'] and 'grafana' in d['services'] and 'alertmanager' in d['services']
"; then
  ok "docker-compose: prometheus + grafana + alertmanager defined"
else
  ko "docker-compose: required services missing"
fi

# H. grafana provisioning datasources + dashboards
if [[ -f "$OBS/grafana-provisioning/datasources/datasource.yml" ]] && \
   grep -q 'uid: prometheus' "$OBS/grafana-provisioning/datasources/datasource.yml"; then
  ok "grafana-provisioning: datasources/datasource.yml has Prometheus uid"
else
  ko "grafana-provisioning: datasources file malformed"
fi
if [[ -f "$OBS/grafana-provisioning/dashboards/dashboards.yml" ]] && \
   grep -q '/var/lib/grafana/dashboards' "$OBS/grafana-provisioning/dashboards/dashboards.yml"; then
  ok "grafana-provisioning: dashboards provider points to dashboards dir"
else
  ko "grafana-provisioning: dashboards provider malformed"
fi

# I. P0 alerts actually use for+labels.severity=page
for alert in Qwen36ZeroCellsReady Qwen36High5xx ControlPlaneDown AgentraceHighErrorRate; do
  if python3 -c "
import yaml,re
src=open('$OBS/alerts/prometheus-helixon-alerts.yml').read()
docs=list(yaml.safe_load_all(src))
for d in docs:
  for g in d['groups']:
    for r in g['rules']:
      if r.get('alert')=='$alert':
        assert r['labels'].get('severity')=='page'
        assert 'for' in r
        exit()
exit(1)
"; then
    ok "alerts: $alert has for+severity=page"
  else
    ko "alerts: $alert missing for or severity=page"
  fi
done

# J. all alert expressions reference real metric names (no typos).
#    Allow optional `_bucket`, `_sum`, `_count` suffix for histograms.
for metric in qwen36_cell_ready qwen36_request_total qwen36_request_latency_seconds qwen36_gpu_mem_free_mib control_plane_up control_plane_ready control_plane_fleet_node_last_seen_seconds control_plane_http_request_duration_seconds agentrace_stage_total agentrace_self_eval_score agentrace_tokens_total; do
  if grep -qE "(^|[^A-Za-z0-9_])${metric}(_bucket|_sum|_count)?([^A-Za-z0-9_]|\\\$)" "$OBS/alerts/prometheus-helixon-alerts.yml"; then
    ok "alerts: metric $metric referenced"
  else
    ko "alerts: metric $metric never used (or typo)"
  fi
done

printf '\n=============================\n'
printf 'verifier: PASS=%d  FAIL=%d\n' "$pass" "$fail"
printf '=============================\n'
if [[ "$pass" -lt 18 ]]; then printf 'RESULT: below v14512 bar (need >= 18 PASS)\n'; exit 2; fi
printf 'RESULT: at/above v14512 bar (>= 18 PASS)\n'
exit 0