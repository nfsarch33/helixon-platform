#!/usr/bin/env bash
#
# Public-repo leak gate for nfsarch33/helixon-platform.
#
# This repository is public, so the risk it guards is not only credentials but
# the internal map: operator home paths, host aliases, private addressing.
#
# The defect this replaces: the gate was a series of `grep -rn ... | grep -v
# 'public-repo-gate'`. That final filter drops any LINE containing the string
# "public-repo-gate" -- which is the annotation line itself, and nothing else.
# So the `# runx-public-repo-gate: allow-file ...` headers the repository
# already carries suppressed only themselves, never the file they annotate.
# The gate therefore could not pass while the evidence tree existed, and it had
# been failing identically on main and on every branch. A gate that always
# fails carries the same amount of information as one that always passes.
#
# Suppression is per FILE and per CATEGORY, declared in the file's own header:
#
#   # runx-public-repo-gate: allow-file personal_path_id,secret_ref
#   # runx-public-repo-gate: allow-file *
#
# and it must be justified in review, not added to make a red go away. The
# categories are deliberately narrow so that annotating a file for one reason
# does not blind the gate to every other.
#
# Exit codes:
#   0  clean
#   1  findings
#   2  the gate could not run (never confuse this with clean)

set -uo pipefail

ROOT="${PUBLIC_REPO_GATE_ROOT:-.}"
SELF_NAME="runx-public-repo-gate"
ANNOTATION_SCAN_LINES="${PUBLIC_REPO_GATE_HEADER_LINES:-5}"

cd "$ROOT" 2>/dev/null || { echo "::error::gate cannot enter ${ROOT}"; exit 2; }
command -v grep >/dev/null 2>&1 || { echo "::error::grep unavailable"; exit 2; }

# This list is the gate's blast radius. A file whose extension is absent is
# not scanned at all, and therefore can never produce a finding -- which is a
# different and quieter failure than a pattern that does not match.
#
# .csv/.tsv were missing until v18792, and a 70-row export of the secret
# store's item inventory sat under evidence/ for seven weeks without ever
# reaching this gate. The sibling .json export from the same sprint HAD been
# sanitised, because .json was on this list: the scrub tracked the scanner
# rather than the risk. Adding a data format here is cheap; leaving one out is
# silent, so prefer adding.
#
# Tabular exports are exactly the shape an inventory takes, which is why they
# are worth scanning even though today's RULES are unlikely to match one --
# see the note on secret_ref below.
INCLUDES=(--include='*.go' --include='*.yaml' --include='*.yml' --include='*.md'
          --include='*.json' --include='*.toml' --include='*.sh'
          --include='*.csv' --include='*.tsv')

# category|pattern|description
#
# Categories exist so a file that legitimately documents op:// syntax is not
# thereby allowed to carry an operator home path.
RULES=(
  "personal_path_id|/home/jason|operator home path"
  "personal_path_id|/Users/jason|operator home path"
  "fleet_host_alias|wsl1-travel|operator host alias"
  "vendor_ref|zendesk|vendor name"
  "vendor_ref|zd-gateway|retired vendor gateway"
  "vendor_ref|jlianzendesk|vendor account"
  # Narrowed deliberately, and this is a policy change worth arguing rather than
  # assuming. The previous patterns were the bare scheme `op://` and the literal
  # word `1password`. Between them they produced 46 of 61 findings, and not one
  # was a leak: they fire on `fmt.Sprintf("op://%s/%s/%s", ...)`, on rule names
  # like `1password-uuid-required.mdc` quoted in a comment, on a `tags = [...]`
  # entry, and on this repository's own secret-handling package. Blocking the
  # name of a password manager protects nothing; the cost was 28 files needing
  # an annotation, recurring for every file added afterwards.
  #
  # What is actually sensitive is a RESOLVED reference -- vault name plus item
  # id -- because that discloses vault structure and addresses a real item. That
  # is also how the repository's own scripts/sentrux-audit-local.sh defines the
  # hazard, so the gate and the audit now agree.
  "secret_ref|op://[A-Za-z0-9_-]+/[A-Za-z0-9_-]{20,}|resolved secret reference (vault + item id)"
  "token_name|HOMEBREW_GITHUB_API_TOKEN|token variable"
  "token_name|VENDIR_GITHUB_API_TOKEN|token variable"
  "private_key|ssh-rsa |public key material"
  "private_key|BEGIN RSA PRIVATE|private key"
  "private_key|BEGIN OPENSSH PRIVATE|private key"
  # v18809 -- a private address has four octets, and these had two or three.
  # That was wrong in both directions at once, and the narrow direction is the
  # one that matters.
  #
  # Too narrow: `10\\.0\\.` covers 10.0/16 and nothing else of 10/8, so a real
  # address elsewhere in that range walked past it. `172\\.(1[6-9]|...)` was worse -- see the
  # parser note above: it never compiled, so 172.16/12 was not scanned at all.
  #
  # Too broad: two octets match any dotted number containing them. The first npm
  # lockfile committed here matched ten lines of semver (`^10.0.0`) and registry
  # URLs (`runtime-1.10.0.tgz`), and every JavaScript dependency added afterwards
  # would have matched the same way.
  #
  # Requiring four octets, bounded so a longer number cannot supply them
  # (a public address that merely carries one as a substring is not one, and a
  # five-part dotted number is not an address at all), is strictly
  # stronger: measured over this tree it keeps every existing true positive,
  # adds the whole of 10/8 and 172.16/12 that were being missed, and drops all
  # ten lockfile false positives. The right-hand bound admits a trailing dot so
  # an address ending a sentence still matches.
  #
  # Deliberate limit, pinned in the tests: a prose placeholder such as
  # 192.168.x.x no longer matches. A placeholder is not a disclosure.
  "network_topology|(^|[^0-9.])192\\.168\\.[0-9]{1,3}\\.[0-9]{1,3}([^0-9]|$)|private address"
  "network_topology|(^|[^0-9.])10\\.[0-9]{1,3}\\.[0-9]{1,3}\\.[0-9]{1,3}([^0-9]|$)|private address"
  "network_topology|(^|[^0-9.])172\\.(1[6-9]|2[0-9]|3[01])\\.[0-9]{1,3}\\.[0-9]{1,3}([^0-9]|$)|private address"
  # KNOWN GAP, measured and deliberately not closed in this change: the mesh
  # range. 100.64/10 is carrier-grade NAT space, which is what the overlay
  # network hands out, and one of those addresses identifies a host here
  # exactly as an RFC1918 address does. No rule covers it, so the gate is blind
  # to every one of them.
  #
  #   "network_topology|(^|[^0-9.])100\\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\\.[0-9]{1,3}\\.[0-9]{1,3}([^0-9]|$)|carrier-grade NAT address"
  #
  # Two numbers, because they answer different questions and the smaller one
  # was quoted alone here at first, which framed the decision too narrowly.
  #
  # THE EXPOSURE, measured over this gate's own file types with a strict
  # dotted-quad match: 10 distinct addresses in that range, appearing 82 times
  # across 31 files. That is what is already published.
  #
  # THE FINDING COUNT if the rule below were enabled: 15 findings across 9
  # files, with 77 occurrences suppressed. The gap is not noise -- most of
  # those files already carry an allow-file header naming network_topology, so
  # enabling the rule surfaces only the unannotated remainder: the scrape
  # config, a fleet health check, a cluster join script, and six files under
  # evidence/ whose headers name fleet_host_alias or internal_service_id
  # instead, and a header only suppresses the categories it names.
  #
  # So the decision is about ten addresses, not fifteen findings. Enabling the
  # rule does not reduce the exposure; it stops the next one being added
  # silently. (Paths are described rather than quoted here: one of them carries
  # a host name, and the sibling scanner refuses a diff that adds one -- which
  # is that gate working.)
  #
  # So enabling it is not a gate change, it is a decision about fifteen mesh
  # addresses already published in a public repo: annotate them, or scrub them.
  # That decision is the operator's, and it is not this PR's to make quietly.
  # T10i below pins the gap so it stays visible and goes red the day the rule
  # is added -- at which point this comment is what needs re-arguing.
)

# Does $1 carry an allow-file header covering category $2?
#
# Only the first few lines are consulted: an annotation must be a visible header,
# not a line buried where review will not see it.
allows() {
  local file="$1" category="$2" header
  header="$(head -n "$ANNOTATION_SCAN_LINES" "$file" 2>/dev/null \
            | grep -o "${SELF_NAME}: allow-file [A-Za-z0-9_,*]*" | head -1)" || true
  [ -n "$header" ] || return 1
  local list="${header##*allow-file }"
  case ",${list}," in
    *",*,"*)            return 0 ;;
    *",${category},"*)  return 0 ;;
  esac
  return 1
}

# Every rule must compile before any of them runs. grep exits 2 on a regex it
# cannot parse, and the scan below sends stderr to /dev/null, so an uncompilable
# rule is indistinguishable from a rule that found nothing -- which is exactly
# how the 172.16/12 rule stayed dead. An unusable rule is an infrastructure
# failure (exit 2), never a clean tree (exit 0).
for rule in "${RULES[@]}"; do
  probe="${rule#*|}"
  probe="${probe%|*}"
  grep -qE -- "$probe" /dev/null
  if [ "$?" -ge 2 ]; then
    echo "::error::gate rule does not compile: ${rule%%|*}"
    echo "A rule that grep cannot parse reports nothing, which reads as clean."
    exit 2
  fi
done

findings=0
suppressed=0
: >/tmp/public-repo-gate-findings.txt
: >/tmp/public-repo-gate-suppressed.txt

# A rule is category|pattern|description, and the PATTERN is everything between
# the first delimiter and the last -- not everything up to the second.
#
# v18809: it used to be `${rest%%|*}`, which truncates at the first `|` INSIDE
# the pattern. The 172.16/12 rule contains an alternation, so the gate compiled
# `172\.(1[6-9]` , grep exited 2 on the unmatched parenthesis, the error went to
# /dev/null with everything else, and the rule reported nothing for as long as
# it existed. A dead rule and a clean tree print the same line, which is the
# whole reason the compile check below is not optional.
for rule in "${RULES[@]}"; do
  category="${rule%%|*}"
  desc="${rule##*|}"
  pattern="${rule#*|}"
  pattern="${pattern%|*}"

  while IFS= read -r hit; do
    [ -n "$hit" ] || continue
    file="${hit%%:*}"

    # The gate's own sources describe every pattern by construction.
    case "$file" in
      ./.github/scripts/public-repo-gate.sh|./.github/scripts/public-repo-gate-tests.sh) continue ;;
    esac
    # Never let a file's own annotation line count as a finding.
    case "$hit" in *"$SELF_NAME"*) continue ;; esac

    # Preserved from the gate this replaces: private addressing is blocked in
    # production code and allowed in test fixtures, which legitimately need
    # concrete addresses. Scoped to that one category, not a blanket test-file
    # exemption -- a test file must not become a place to park a real secret.
    if [ "$category" = "network_topology" ]; then
      case "$file" in *_test.go) continue ;; esac
    fi

    if allows "$file" "$category"; then
      suppressed=$((suppressed + 1))
      echo "$category|$file" >>/tmp/public-repo-gate-suppressed.txt
    else
      findings=$((findings + 1))
      echo "[$category] $hit" >>/tmp/public-repo-gate-findings.txt
      echo "::error file=${file#./}::${desc} (${category}): ${pattern}"
    fi
  # v18809: --exclude-dir, not a `grep -v` on the output.
  #
  # The output of `grep -rn` is path:line:TEXT, so filtering it with
  # `grep -v 'node_modules/'` drops every finding whose matched TEXT happens to
  # contain that string, wherever the file actually lives. A dependency
  # manifest quoting a path, a script that greps its own tree, a note about a
  # build directory -- a real violation sharing a line with any of those was
  # silently discarded. The filter was meant to scope the scan and was instead
  # scoping the findings, which is the shape of an exclusion that fails open.
  #
  # --exclude-dir sits before the path operand deliberately: grep ignores it
  # after one, without a word.
  done < <(grep -rn "${INCLUDES[@]}" --exclude-dir=.git --exclude-dir=node_modules \
           -E -- "$pattern" . 2>/dev/null)
done

echo "public-repo-gate: ${findings} finding(s), ${suppressed} suppressed by allow-file annotation"

if [ "$findings" -gt 0 ]; then
  echo
  echo "Findings:"
  sed -E 's/(.{200}).*/\1.../' /tmp/public-repo-gate-findings.txt | head -60
  echo
  echo "Each must be removed, or the file annotated with a header naming the"
  echo "specific category and a reason that survives review:"
  echo "  # ${SELF_NAME}: allow-file <category>"
  exit 1
fi

exit 0
