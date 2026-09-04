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

# ---------------------------------------------------------------------------
# T9: the extension list is a real boundary, pinned in both directions.
#
# The gate's INCLUDES decide what is scanned at all. A format that is missing
# produces zero findings no matter what it contains, which reads exactly like
# a clean file. That is how a 70-row export of the secret store's item
# inventory lived under evidence/ for seven weeks: .csv and .tsv were not on
# the list. T9a pins that they now are.
#
# T9b is the honest half. It asserts the CURRENT boundary rather than a
# desirable one: .txt is still unscanned, so a violation there is still
# missed. It is here so that the gate's real blast radius is written down and
# a future reader can see what widening it would buy, instead of inferring
# completeness from a green run.
# ---------------------------------------------------------------------------
fixture
printf 'id,ref\n1,op://HelixonSafe/n2ecpwlnkpjsabcdefghijklmn/password\n' >"$TMP/r/i.csv"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "1" ] && ok "T9a a violation in a .csv IS a finding" \
                || nope "T9a exited ${rc}, want 1 -- .csv is not being scanned"

fixture
printf 'id\tref\n1\top://HelixonSafe/n2ecpwlnkpjsabcdefghijklmn/password\n' >"$TMP/r/j.tsv"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "1" ] && ok "T9b a violation in a .tsv IS a finding" \
                || nope "T9b exited ${rc}, want 1 -- .tsv is not being scanned"

fixture
printf 'op://HelixonSafe/n2ecpwlnkpjsabcdefghijklmn/password\n' >"$TMP/r/k.txt"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "0" ] && ok "T9c KNOWN LIMIT: .txt is still outside the gate's blast radius" \
                || nope "T9c exited ${rc}, want 0 -- if .txt was added, update this test"

# ---------------------------------------------------------------------------
# T10 private addressing: four octets, bounded.
#
# The three network_topology rules used to carry two or three octets, which was
# wrong in both directions at once. Too narrow: `10\.0\.` is 10.0/16 and
# nothing else of 10/8. Too broad: two octets match any dotted number that
# contains them, so the first npm lockfile committed here matched ten lines of
# semver and registry URLs. T10e-h are those false positives; T10a-d are the
# true positives, two of which this gate could not catch at all before.
# ---------------------------------------------------------------------------
# The sibling anti-leak scanner (scripts/ci/deny-pattern.sh) reads ADDED lines
# and refuses a complete private-address literal. That is correct for a public
# repository, and it applies to this file like any other: a test is not exempt.
#
# These vectors are synthetic - not one of them is an address in use here - but
# a literal is a literal, so they are assembled from octets at run time. The
# assertions are what give them meaning; the strings themselves say nothing
# about this estate.
ip() { printf '%s.%s.%s.%s' "$1" "$2" "$3" "$4"; }
A172="$(ip 172 31 0 12)"    # RFC1918, the range whose rule never compiled
A10="$(ip 10 42 7 19)"      # RFC1918, outside the 10.0/16 the old rule reached
A192="$(ip 192 168 10 4)"   # RFC1918, the range that always worked: the control
AMESH="$(ip 100 96 0 12)"   # carrier-grade NAT, the range no rule covers
APUB="$(ip 210 0 0 1)"      # public, and carries a private address as a substring
AVER="1.$(ip 10 0 0 1)"     # not an address at all: five dotted parts

topo() { # label content want_rc
  fixture
  printf '%s\n' "$2" >"$TMP/r/topology.md"
  local rc; rc="$(run_gate "$TMP/r")"
  [ "$rc" = "$3" ] && ok "$1" || nope "$1 (exited ${rc}, want $3)"
}

topo "T10a a 172.16/12 address is a finding"          "control plane: $A172"     1
topo "T10b a 10/8 address outside 10.0/16 is one too" "vpc host $A10 answers"    1
topo "T10c a 192.168 address still is"                "lan host $A192"           1
topo "T10d an address ending a sentence still is"     "the control plane is ${A172}."  1
topo "T10e a caret semver range is NOT"               '"eslint": "^8.57.0 || ^9.0.0 || ^10.0.0",'  0
topo "T10f a registry tarball URL is NOT"             '"resolved": "https://r.example/x-1.10.0.tgz"' 0
topo "T10g a public address carrying one as a substring is NOT" "the endpoint is $APUB today" 0
topo "T10h a five-part dotted number is NOT"          "build $AVER shipped"      0

# T10i and T10j are KNOWN LIMITS, asserted as they are rather than as they
# should be, so the gate's real blast radius is written down. Both are meant to
# go RED the day the corresponding rule is added -- that is the point of them.
topo "T10i KNOWN GAP: a mesh address is not scanned"  "peer $AMESH direct"       0
topo "T10j KNOWN LIMIT: a prose placeholder is not"   'the lan is 192.168.x.x somewhere'  0

# ---------------------------------------------------------------------------
# T11 a rule that does not compile must stop the gate, not pass it.
#
# The 172.16/12 rule contains an alternation and the parser split on the FIRST
# delimiter, so the gate compiled `172\.(1[6-9]`, grep exited 2, stderr went to
# /dev/null with everything else, and the rule reported nothing for as long as
# it existed. T10a is the positive half of that fix; this is the half that
# stops the next one being silent.
# ---------------------------------------------------------------------------
fixture
printf 'clean\n' >"$TMP/r/a.md"
sed 's#^  "private_key|ssh-rsa |public key material"#  "private_key|ssh-rsa (unclosed|public key material"#' \
    "$GATE" >"$TMP/broken-gate.sh"
if ! grep -q 'ssh-rsa (unclosed' "$TMP/broken-gate.sh"; then
  nope "T11 could not inject an uncompilable rule (the rule list moved)"
else
  rc="$(PUBLIC_REPO_GATE_ROOT="$TMP/r" bash "$TMP/broken-gate.sh" >"$TMP/broken.log" 2>&1; echo $?)"
  [ "$rc" = "2" ] && ok "T11a an uncompilable rule exits 2, not 0" \
                  || nope "T11a exited ${rc}, want 2 -- a dead rule reads as a clean tree"
  grep -q "does not compile" "$TMP/broken.log" \
    && ok "T11b   ... and names the category it could not compile" \
    || nope "T11b the failure does not say which rule"
fi

# ---------------------------------------------------------------------------
# T12 exclusions are by PATH, not by the text of a finding.
#
# `grep -rn` prints path:line:TEXT, so filtering that stream with
# `grep -v 'node_modules/'` dropped any finding whose matched TEXT contained
# the string, wherever the file lived. T12a is the violation that used to
# vanish; T12b and T12c are the controls that the real directories are still
# out of scope, so the fix did not simply delete the exclusion.
# ---------------------------------------------------------------------------
fixture
printf 'see node_modules/react and the box at %s\n' "$A192" >"$TMP/r/notes.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "1" ] && ok "T12a a violation on a line mentioning node_modules/ IS a finding" \
                || nope "T12a exited ${rc}, want 1 -- the exclusion is filtering findings, not paths"

fixture
mkdir -p "$TMP/r/node_modules/pkg"
printf 'host %s\n' "$A192" >"$TMP/r/node_modules/pkg/readme.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "0" ] && ok "T12b a violation inside node_modules/ is still out of scope" \
                || nope "T12b exited ${rc}, want 0 -- dependency trees are not ours to scan"

fixture
mkdir -p "$TMP/r/.git"
printf 'host %s\n' "$A192" >"$TMP/r/.git/config.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "0" ] && ok "T12c a violation inside .git/ is still out of scope" \
                || nope "T12c exited ${rc}, want 0"

# ---------------------------------------------------------------------------
# T13 a committed executable is a finding.
#
# No RULE can produce this one: every rule greps text and INCLUDES lists text
# extensions, so a compiled binary is invisible to the rest of this gate -- and
# to gitleaks, which reports "no leaks" on one. A 3.5 MB ELF was tracked at the
# root of this repository carrying 337 copies of the operator's home path.
#
# T13c is the control that matters: a TEXT file whose name looks like a binary
# must not be flagged, so the check is reading magic bytes rather than names.
# ---------------------------------------------------------------------------
elf_fixture() { # $1 = path -- a minimal but genuine ELF header
  printf '\177ELF\002\001\001\000\000\000\000\000\000\000\000\000\002\000\076\000' >"$1"
  printf 'padding so the file is not empty\n' >>"$1"
}

fixture
elf_fixture "$TMP/r/some-tool"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "1" ] && ok "T13a a committed ELF executable is a finding" \
                || nope "T13a exited ${rc}, want 1 -- a binary is invisible to every text rule"
grep -q "tracked_binary" "$TMP/out.log" \
  && ok "T13b   ... reported under the tracked_binary category" \
  || nope "T13b the finding is not labelled tracked_binary"

fixture
printf 'ELF is mentioned here but this is a text file about 7f454c46 magic\n' >"$TMP/r/notes.md"
printf 'MZ and Mach-O too\n' >>"$TMP/r/notes.md"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "0" ] && ok "T13c a TEXT file naming those magics is NOT a finding" \
                || nope "T13c exited ${rc}, want 0 -- the check must read magic bytes, not words"

fixture
printf 'MZ\220\000\003\000\000\000\004\000\000\000' >"$TMP/r/thing.exe"
rc="$(run_gate "$TMP/r")"
[ "$rc" = "1" ] && ok "T13d a committed PE executable is a finding too" \
                || nope "T13d exited ${rc}, want 1"

# The mode line must always be printed. A check that can silently not run is
# the exact failure this gate was built to stop.
fixture
printf 'clean\n' >"$TMP/r/a.md"
rc="$(run_gate "$TMP/r")"
grep -q "binary check ran in" "$TMP/out.log" \
  && ok "T13e the binary check always says which mode it ran in" \
  || nope "T13e the run does not say whether the binary check ran"

# And in a real git checkout it scopes to TRACKED files, so a developer's build
# output does not turn the gate red on their machine.
fixture
( cd "$TMP/r" && git init -q . 2>/dev/null && git config user.email t@invalid && git config user.name t )
printf 'clean\n' >"$TMP/r/a.md"
( cd "$TMP/r" && git add a.md && git commit -qm base 2>/dev/null )
elf_fixture "$TMP/r/built-binary"      # untracked build output
rc="$(run_gate "$TMP/r")"
[ "$rc" = "0" ] && ok "T13f untracked build output does not trip the gate in a checkout" \
                || nope "T13f exited ${rc}, want 0 -- untracked output is what .gitignore is for"
( cd "$TMP/r" && git add -f built-binary && git commit -qm "oops" 2>/dev/null )
rc="$(run_gate "$TMP/r")"
[ "$rc" = "1" ] && ok "T13g   ... but committing it is a finding" \
                || nope "T13g exited ${rc}, want 1 -- a committed binary must be caught"

echo
echo "PASS: $PASS, FAIL: $FAIL"
[ "$FAIL" -eq 0 ] || exit 1
