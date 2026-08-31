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
  "network_topology|192\\.168\\.|private address"
  "network_topology|10\\.0\\.|private address"
  "network_topology|172\\.(1[6-9]|2[0-9]|3[01])\\.|private address"
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

findings=0
suppressed=0
: >/tmp/public-repo-gate-findings.txt
: >/tmp/public-repo-gate-suppressed.txt

for rule in "${RULES[@]}"; do
  category="${rule%%|*}"
  rest="${rule#*|}"
  pattern="${rest%%|*}"
  desc="${rest##*|}"

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
  done < <(grep -rn "${INCLUDES[@]}" -E -- "$pattern" . 2>/dev/null \
           | grep -v '/\.git/' | grep -v 'node_modules/')
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
