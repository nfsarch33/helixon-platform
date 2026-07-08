#!/usr/bin/env bash
#
# test-install-agentcage.sh — replaces the .bats harness because
# bats is not installed on win1/wsl1; the script asserts the
# same 8 contracts as the .bats file.
#
# Run with: bash scripts/agentcage/test-install-agentcage.sh
# Exits 0 on green, 1 on any failure.

set -o errexit
set -o nounset
set -o pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
INSTALLER="$HERE/install-agentcage.sh"

fail=0
pass=0

ok() { printf '\033[32mPASS\033[0m %s\n' "$*"; pass=$((pass+1)); }
ko() { printf '\033[31mFAIL\033[0m %s\n' "$*"; fail=$((fail+1)); }

[ -x "$INSTALLER" ] || [ -f "$INSTALLER" ] || { ko "install-agentcage.sh missing"; exit 1; }
chmod +x "$INSTALLER" 2>/dev/null || true

# 1. shell syntax clean
if bash -n "$INSTALLER"; then ok "bash -n syntax-clean"; else ko "bash -n found syntax errors"; fi

# 2. pins agentcage==0.29.0
if grep -qE 'agentcage==0\.29\.0' "$INSTALLER"; then ok "pins agentcage==0.29.0"; else ko "missing pinned version 0.29.0"; fi

# 3. no raw curl|bash for github content (the dangerous pattern
# is `curl https://raw.githubusercontent.com/... | bash` which
# pulls unverified upstream code). pip download from PyPI is OK.
if ! grep -qE 'curl[^|]*raw\.githubusercontent\.com[^|]*\|[^|]*(bash|sh)' "$INSTALLER" &&
   ! grep -qE 'curl[^|]*github\.com[^|]*\|[^|]*(bash|sh)' "$INSTALLER"; then
  ok "no raw curl|bash from github"
else
  ko "found raw curl|bash from github"
fi

# 4. uses pip3 install
if grep -qE 'pip3.*install.*agentcage|pip3 download.*agentcage' "$INSTALLER"; then ok "uses pip3 + canonical package name"; else ko "missing pip3 install line"; fi

# 5. sha256 verification
if grep -qE 'sha256sum|openssl.*sha256|hashlib\.sha256' "$INSTALLER"; then ok "verifies wheel SHA256"; else ko "missing sha256 verify"; fi

# 6. --dry-run flag
if grep -qE -- '--dry-run' "$INSTALLER"; then ok "supports --dry-run"; else ko "missing --dry-run"; fi

# 7. podman verify
if grep -qE 'podman info|podman --version' "$INSTALLER"; then ok "verifies podman"; else ko "missing podman verify"; fi

# 8. experimental marker in comment
if grep -qiE 'experimental|use at your own risk' "$INSTALLER"; then ok "marks project as experimental"; else ko "missing experimental disclaimer"; fi

# 9. dry-run happy path
if AGENTCAGE_WHEEL_SHA256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef bash "$INSTALLER" --dry-run >/dev/null 2>&1; then
  ok "--dry-run exits 0"
else
  ko "--dry-run exited non-zero (expected 0)"
fi

# 10. missing sha256 in non-dry-run mode refuses
# The installer uses bash's -u + ${EXPECTED_SHA256:?...} syntax;
# unset SHA256 must cause the script to exit non-zero before any
# destructive pip install runs.
if AGENTCAGE_WHEEL_SHA256="" bash "$INSTALLER" 2>/dev/null; then
  ko "non-dry-run with empty SHA256 silently succeeded (DANGEROUS)"
else
  ok "non-dry-run with empty SHA256 refuses"
fi

echo "------"
echo "passed: $pass / failed: $fail"
exit $((fail > 0 ? 1 : 0))
