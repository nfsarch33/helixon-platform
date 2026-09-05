#!/usr/bin/env bash
#
# Tests for .github/scripts/gitleaks-scan.sh.
#
# The defect these guard against is not "the scanner missed a secret" -- it is
# "the scanner never ran, and nobody could tell". So a clean run proves nothing
# on its own: a scanner that always exits 0 passes that test. Every claim below
# is therefore paired with a control that fails when the behaviour is absent.
#
#   T1  the pinned release really does publish the asset name we resolve
#   T2  negative control -- a clean repo yields exit 0, receipt, verdict clean
#   T3  POSITIVE control -- a planted secret yields exit 1 and is reported
#   T4  an install failure exits 3 and writes NO receipt (the original defect)
#   T5  the workflow and this script agree on paths, and the dead URL is gone
#
# Usage: .github/scripts/gitleaks-scan-tests.sh
# Requires network access to the GitHub release API.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCAN_SCRIPT="${REPO_ROOT}/.github/scripts/gitleaks-scan.sh"
WORKFLOW="${REPO_ROOT}/.github/workflows/secrets-scan.yml"
VERSION="${GITLEAKS_VERSION:-8.30.1}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0

ok()   { PASS=$((PASS + 1)); printf 'ok   -- %s\n' "$*"; }
nope() { FAIL=$((FAIL + 1)); printf 'FAIL -- %s\n' "$*"; }

# Build a throwaway git repo. $2, if given, is planted in a committed file.
#
# --no-verify, and a conventional-commit subject, because a developer's global
# hooks apply to fixture repos too: a commit-msg hook rejecting "fixture" leaves
# an empty repo, and a scan of an empty repo passes for the wrong reason.
# The commit is asserted rather than assumed, so that failure is never silent.
make_fixture() {
  local dir="$1" secret="${2:-}"
  mkdir -p "$dir"
  git -C "$dir" init -q
  git -C "$dir" config user.email "ci@example.invalid"
  git -C "$dir" config user.name "ci"
  git -C "$dir" config commit.gpgsign false
  printf 'package main\n\nfunc main() {}\n' >"$dir/main.go"
  if [ -n "$secret" ]; then
    printf 'const key = "%s"\n' "$secret" >"$dir/config.go"
  fi
  git -C "$dir" add -A
  git -C "$dir" commit -q --no-verify -m "test(fixture): seed scan fixture" \
    || { nope "fixture commit failed in $dir"; return 1; }
  local n
  n="$(git -C "$dir" rev-list --count HEAD 2>/dev/null || echo 0)"
  [ "$n" -ge 1 ] || { nope "fixture $dir has no commits; tests against it are vacuous"; return 1; }
}

run_scan() {
  local src="$1" report="$2" receipt="$3" version="${4:-$VERSION}"
  GITLEAKS_VERSION="$version" \
  GITLEAKS_SOURCE="$src" \
  GITLEAKS_REPORT_PATH="$report" \
  GITLEAKS_RECEIPT_PATH="$receipt" \
  GITLEAKS_BIN_DIR="$TMP/bin" \
  GITLEAKS_CONFIG="${REPO_ROOT}/.gitleaks.toml" \
  GITLEAKS_IGNORE_PATH="${REPO_ROOT}/.gitleaksignore" \
    bash "$SCAN_SCRIPT" >"$TMP/scan.log" 2>&1
  echo $?
}

# ---------------------------------------------------------------------------
# T1: the release asset name. This is the exact fact the old workflow got wrong.
# ---------------------------------------------------------------------------
assets="$TMP/assets.json"
if curl -sSfL --max-time 60 \
     -H "Accept: application/vnd.github+json" \
     ${GH_TOKEN:+-H "Authorization: Bearer ${GH_TOKEN}"} \
     -o "$assets" \
     "https://api.github.com/repos/gitleaks/gitleaks/releases/tags/v${VERSION}"; then
  names="$(jq -r '.assets[].name' "$assets")"
  if printf '%s\n' "$names" | grep -qx "gitleaks_${VERSION}_linux_DELIBERATELY_WRONG.tar.gz"; then
    ok "T1a release v${VERSION} publishes gitleaks_${VERSION}_linux_x64.tar.gz"
  else
    nope "T1a release v${VERSION} has no linux_x64 tarball"
  fi
  if printf '%s\n' "$names" | grep -qx "gitleaks_${VERSION}_linux_amd64.tar.gz"; then
    nope "T1b linux_amd64 exists after all -- the URL fix needs rechecking"
  else
    ok "T1b linux_amd64 is absent, confirming the old pinned URL could only 404"
  fi
else
  nope "T1 could not reach the gitleaks release API"
fi

# ---------------------------------------------------------------------------
# T2: negative control -- clean repo.
# ---------------------------------------------------------------------------
make_fixture "$TMP/clean"
rc="$(run_scan "$TMP/clean" "$TMP/clean.sarif" "$TMP/clean.receipt.json")"
if [ "$rc" = "0" ]; then
  ok "T2a clean repo exits 0"
else
  nope "T2a clean repo exited ${rc}, expected 0 (see $TMP/scan.log)"
  sed -n '1,20p' "$TMP/scan.log"
fi
if [ -s "$TMP/clean.receipt.json" ] \
   && [ "$(jq -r .verdict "$TMP/clean.receipt.json" 2>/dev/null)" = "clean" ]; then
  ok "T2b clean run writes a receipt with verdict=clean"
else
  nope "T2b clean run produced no receipt, or the verdict was not 'clean'"
fi

# ---------------------------------------------------------------------------
# T3: positive control -- a planted secret must be found. Without this, T2
#     would also pass against a scanner that does nothing at all.
#
#     The literal never appears in this file: it is assembled at runtime, so
#     the fixture cannot itself trip the scan of this repository.
# ---------------------------------------------------------------------------
planted="$(printf 'AKIA%s' 'QQZZ7WBLTESTONLY')"
make_fixture "$TMP/dirty" "$planted"
rc="$(run_scan "$TMP/dirty" "$TMP/dirty.sarif" "$TMP/dirty.receipt.json")"
if [ "$rc" = "1" ]; then
  ok "T3a planted secret makes the scan exit 1"
else
  nope "T3a planted secret produced exit ${rc}, expected 1 -- THE SCAN IS NOT DETECTING"
  sed -n '1,20p' "$TMP/scan.log"
fi
found="$(jq '[.runs[].results[]] | length' "$TMP/dirty.sarif" 2>/dev/null || echo 0)"
if [ "${found:-0}" -ge 1 ]; then
  ok "T3b the SARIF records the planted secret (${found} finding(s))"
else
  nope "T3b the SARIF recorded no findings for a repo with a planted secret"
fi
if [ "$(jq -r .verdict "$TMP/dirty.receipt.json" 2>/dev/null)" = "findings" ]; then
  ok "T3c receipt records verdict=findings"
else
  nope "T3c receipt did not record verdict=findings"
fi
if grep -q "$planted" "$TMP/dirty.sarif" 2>/dev/null; then
  nope "T3d the SARIF contains the raw secret -- --redact is not in effect"
else
  ok "T3d the SARIF is redacted; no raw secret value in the report"
fi

# ---------------------------------------------------------------------------
# T4: the original defect. An install that cannot complete must exit 3 and
#     leave no receipt, so it can never be read as a scan verdict.
# ---------------------------------------------------------------------------
rm -f "$TMP/ghost.receipt.json"
rc="$(run_scan "$TMP/clean" "$TMP/ghost.sarif" "$TMP/ghost.receipt.json" "0.0.0-does-not-exist")"
if [ "$rc" = "3" ]; then
  ok "T4a an unresolvable release exits 3 (install fault), not 0 or 1"
else
  nope "T4a an unresolvable release exited ${rc}, expected 3"
fi
if [ -e "$TMP/ghost.receipt.json" ]; then
  nope "T4b a failed install still wrote a receipt -- infra fault looks like a verdict"
else
  ok "T4b a failed install writes no receipt"
fi
if grep -q '::error::' "$TMP/scan.log"; then
  ok "T4c the install fault is annotated with ::error:: so it surfaces in the UI"
else
  nope "T4c the install fault produced no ::error:: annotation"
fi

# ---------------------------------------------------------------------------
# T5: workflow parity. The YAML and this script must not drift apart.
# ---------------------------------------------------------------------------
# Matches a download URL being assembled, not the word "linux_amd64" appearing
# in the comment that explains why this fix exists.
if grep -qE 'releases/download/[^[:space:]]*gitleaks' "$WORKFLOW"; then
  nope "T5a the workflow still hand-builds a gitleaks download URL"
else
  ok "T5a the workflow no longer hand-builds a gitleaks download URL"
fi
if grep -q 'gitleaks-scan.sh' "$WORKFLOW"; then
  ok "T5b the workflow invokes the scan script"
else
  nope "T5b the workflow does not invoke the scan script"
fi
if grep -q 'gitleaks-receipt.json' "$WORKFLOW"; then
  ok "T5c the workflow checks for the receipt"
else
  nope "T5c the workflow never checks the receipt, so a missing scan is invisible"
fi
if grep -q "steps.receipt.outputs.sarif == 'true'" "$WORKFLOW"; then
  ok "T5d upload-sarif is guarded on the report existing"
else
  nope "T5d upload-sarif is not guarded and will error when the scan step failed"
fi
# hashFiles() only matches paths under GITHUB_WORKSPACE; against /tmp it returns
# an empty string, so a guard written that way would silently never fire.
if grep -qE "hashFiles\('/tmp" "$WORKFLOW"; then
  nope "T5e a guard uses hashFiles() on /tmp, which always evaluates to absent"
else
  ok "T5e no guard relies on hashFiles() outside the workspace"
fi

# ---------------------------------------------------------------------------
# T6: a full-history scan that examined no history must not report "clean".
#     A shallow checkout would otherwise produce a confident, empty pass.
# ---------------------------------------------------------------------------
mkdir -p "$TMP/empty"
git -C "$TMP/empty" init -q
rm -f "$TMP/empty.receipt.json"
rc="$(run_scan "$TMP/empty" "$TMP/empty.sarif" "$TMP/empty.receipt.json")"
if [ "$rc" = "4" ]; then
  ok "T6a a repo with no commits exits 4, not a vacuous 0"
else
  nope "T6a a repo with no commits exited ${rc}, expected 4"
fi
if [ -e "$TMP/empty.receipt.json" ]; then
  nope "T6b a zero-commit scan wrote a receipt, legitimising a vacuous pass"
else
  ok "T6b a zero-commit scan writes no receipt"
fi

printf '\nPASS: %d, FAIL: %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
