#!/usr/bin/env bash
#
# Tests for .github/scripts/public-repo-gate.sh.
#
# The defect being fixed is a suppression mechanism that silently did nothing,
# so "the gate passes now" is worth nothing on its own -- a gate that always
# passes satisfies it too. Every claim below is paired with a control that fails
# if the behaviour is absent:
#
#   T1  POSITIVE control -- an unannotated violation still fails, per category
#   T2  an allow-file header suppresses its OWN category
#   T3  NEGATIVE control -- that header does NOT suppress a different category
#   T4  a `*` header suppresses everything in that file
#   T5  an annotation buried below the header window does not count
#   T6  the gate reports infrastructure failure (2) distinctly from findings (1)
#   T7  a file's own annotation line is never itself a finding
#
# Usage: .github/scripts/public-repo-gate-tests.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GATE="${REPO_ROOT}/.github/scripts/public-repo-gate.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0; FAIL=0
ok()   { PASS=$((PASS + 1)); printf 'ok   -- %s\n' "$*"; }
nope() { FAIL=$((FAIL + 1)); printf 'FAIL -- %s\n' "$*"; }

run_gate() { PUBLIC_REPO_GATE_ROOT="$1" bash "$GATE" >"$TMP/out.log" 2>&1; echo $?; }

fixture() { rm -rf "$TMP/r"; mkdir -p "$TMP/r"; }

# ---------------------------------------------------------------------------
# T1 POSITIVE CONTROL: an unannotated violation must fail, for each category.
# Without this the rest of the suite would pass against a gate that never fires.
# ---------------------------------------------------------------------------
t1() {
  local label="$1" content="$2"
  fixture
  printf '%s\n' "$content" >"$TMP/r/thing.md"
  local rc; rc="$(run_gate "$TMP/r")"
  [ "$rc" = "1" ] && ok "T1 unannotated ${label} fails the gate" \
                  || nope "T1 unannotated ${label} exited ${rc}, want 1"
}
t1 "operator home path" 'see /home/jason/runs for the worktree'
t1 "resolved secret ref" 'op read op://HelixonSafe/n2ecpwlnkpjsabcdefghijklmn/password'
t1 "private address"    'the node answers on 192.168.10.4'
t1 "private key"        '-----BEGIN OPENSSH PRIVATE KEY-----'

# ---------------------------------------------------------------------------
# T2: an allow-file header suppresses its own category.
# ---------------------------------------------------------------------------
fixture
printf '# runx-public-repo-gate: allow-file personal_path_id\nsee /home/jason/runs\n' >"$TMP/r/a.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "0" ] && ok "T2 allow-file suppresses its own category" \
                || nope "T2 exited ${rc}, want 0 -- annotation still does nothing"

# ---------------------------------------------------------------------------
# T3 NEGATIVE CONTROL: the same header must NOT suppress a different category.
# This is the property that makes per-category scoping worth having; without it
# one annotation blinds the file entirely, which is the old behaviour in a new
# costume.
# ---------------------------------------------------------------------------
fixture
printf '# runx-public-repo-gate: allow-file personal_path_id\nop://HelixonSafe/n2ecpwlnkpjsabcdefghijklmn/password\n' >"$TMP/r/b.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "1" ] && ok "T3 a personal_path_id annotation does NOT suppress secret_ref" \
                || nope "T3 exited ${rc}, want 1 -- annotation is over-broad"

# ---------------------------------------------------------------------------
# T4: an explicit wildcard suppresses everything in that file.
# ---------------------------------------------------------------------------
fixture
printf '# runx-public-repo-gate: allow-file *\n/home/jason and op:// and 192.168.1.1\n' >"$TMP/r/c.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "0" ] && ok "T4 allow-file * suppresses the whole file" \
                || nope "T4 exited ${rc}, want 0"

# ---------------------------------------------------------------------------
# T5: an annotation outside the header window must not count, or a suppression
# could be hidden far from where a reviewer looks.
# ---------------------------------------------------------------------------
fixture
{ printf 'filler\n%.0s' {1..12}; printf '# runx-public-repo-gate: allow-file personal_path_id\n/home/jason\n'; } >"$TMP/r/d.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "1" ] && ok "T5 an annotation below the header window does not suppress" \
                || nope "T5 exited ${rc}, want 1 -- a buried annotation was honoured"

# ---------------------------------------------------------------------------
# T6: infrastructure failure is distinct from findings. A gate that cannot run
# must never be readable as a clean scan.
# ---------------------------------------------------------------------------
rc="$(run_gate "$TMP/definitely-not-here")"
[ "$rc" = "2" ] && ok "T6 an unusable root exits 2, not 0 or 1" \
                || nope "T6 exited ${rc}, want 2"

# ---------------------------------------------------------------------------
# T7: the annotation line itself must never be reported. This is the inverse of
# the original defect, which reported everything EXCEPT that line.
# ---------------------------------------------------------------------------
fixture
printf '# runx-public-repo-gate: allow-file personal_path_id\nnothing else here\n' >"$TMP/r/e.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "0" ] && ok "T7 a file containing only an annotation is clean" \
                || nope "T7 exited ${rc}, want 0; output: $(head -3 "$TMP/out.log")"

# ---------------------------------------------------------------------------
# T8: the secret_ref narrowing, pinned in both directions. A resolved reference
# (vault + item id) must fail; the bare scheme and the tool's name must not.
# Without the negative half, someone could "fix" a false positive by widening
# the pattern back and no test would notice.
# ---------------------------------------------------------------------------
fixture
printf 'ref := fmt.Sprintf("op://%%s/%%s/%%s", vault, item, field)\n' >"$TMP/r/f.go"
printf 'per 1password-uuid-required.mdc an item is referenced by UUID\n' >"$TMP/r/g.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "0" ] && ok "T8a a format string and a tool name are not findings" \
                || nope "T8a exited ${rc}, want 0 -- the blunt pattern is back"

fixture
printf 'op://HelixonSafe/n2ecpwlnkpjsabcdefghijklmn/password\n' >"$TMP/r/h.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "1" ] && ok "T8b a resolved vault+item reference IS a finding" \
                || nope "T8b exited ${rc}, want 1 -- the narrowing went too far"

echo
echo "PASS: $PASS, FAIL: $FAIL"
[ "$FAIL" -eq 0 ] || exit 1
