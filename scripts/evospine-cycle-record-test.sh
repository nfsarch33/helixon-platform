#!/usr/bin/env bash
# evospine-cycle-record-test.sh - stub test for scripts/evospine-cycle-record.sh:
# the record's status and the exit code must follow the checks. Runs the real
# script with curl/systemctl replaced by stubs on PATH; touches no service.
# Prints "PASS: n, FAIL: m" and exits non-zero on any FAIL.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/evospine-cycle-record.sh"
pass=0; fail=0
ok()  { pass=$((pass+1)); echo "PASS: $1"; }
bad() { fail=$((fail+1)); echo "FAIL: $1"; }
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
cat > "$tmp/bin/curl" <<'EOF'
#!/usr/bin/env bash
# stub: print the HTTP code the test asked for, whatever the URL
printf '%s' "${STUB_HTTP:-000}"
EOF
cat > "$tmp/bin/systemctl" <<'EOF'
#!/usr/bin/env bash
echo "${STUB_UNIT_STATE:-active}"
EOF
chmod +x "$tmp/bin/curl" "$tmp/bin/systemctl"
export PATH="$tmp/bin:$PATH"
export EVOSPINE_CYCLES_FILE="$tmp/cycles.ndjson" EVOSPINE_DOCTOR_LOG="$tmp/doctor.log" EVOSPINE_REPO_DIR="$HERE/.."

last() { tail -1 "$EVOSPINE_CYCLES_FILE"; }
field() { python3 -c 'import json,sys; r=json.loads(sys.stdin.readline()); print(eval(sys.argv[1], {}, {"r": r}))' "$1"; }

# Case 1: everything green -> exit 0, status complete, eval ok, seq 1
echo "VERDICT: GREEN" > "$EVOSPINE_DOCTOR_LOG"
STUB_HTTP=200 bash "$SCRIPT" >/dev/null 2>&1; rc=$?
if [ "$rc" -eq 0 ]; then ok "all checks green: exit 0"; else bad "all checks green: exit $rc, want 0"; fi
st=$(last | field 'r["status"]'); es=$(last | field 'r["eval"]["status"]'); sq=$(last | field 'r["seq"]')
if [ "$st" = "complete" ] && [ "$es" = "ok" ] && [ "$sq" = "1" ]; then ok "green record: status=complete eval=ok seq=1"; else bad "green record: status=$st eval=$es seq=$sq"; fi

# Case 2: services down -> exit 1, status failed, eval failed, seq 2
STUB_HTTP=000 bash "$SCRIPT" >/dev/null 2>&1; rc=$?
if [ "$rc" -ne 0 ]; then ok "checks failing: exit non-zero ($rc)"; else bad "checks failing: exit 0 - the script cannot fail"; fi
st=$(last | field 'r["status"]'); es=$(last | field 'r["eval"]["status"]'); sq=$(last | field 'r["seq"]'); rcode=$(last | field 'r["eval"]["returncode"]')
if [ "$st" = "failed" ] && [ "$es" = "failed" ] && [ "$sq" = "2" ] && [ "$rcode" -gt 0 ]; then ok "failing record: status=failed eval=failed returncode=$rcode seq=2"; else bad "failing record: status=$st eval=$es returncode=$rcode seq=$sq"; fi

# Case 3: the file must be state, never a repo path
if [ "$(wc -l < "$EVOSPINE_CYCLES_FILE")" -eq 2 ] && [ ! -e "$EVOSPINE_REPO_DIR/evospine-cycles.ndjson.new" ]; then ok "records go to the cycles file (2 lines), not the checkout"; else bad "record placement wrong"; fi
if grep -q '/home/' "$SCRIPT"; then bad "script hard-codes a home path (public repo)"; else ok "no hard-coded home path in the script"; fi

echo "PASS: $pass, FAIL: $fail"
[ "$fail" -eq 0 ]
