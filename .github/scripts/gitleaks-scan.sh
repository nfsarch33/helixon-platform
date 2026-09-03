#!/usr/bin/env bash
#
# Install a pinned gitleaks release and run the full-history secret scan.
#
# Why this exists as a script rather than inline workflow YAML: the previous
# inline version hand-built the download URL as
#   gitleaks_<ver>_linux_amd64.tar.gz
# but gitleaks renamed its 64-bit Linux archive to "linux_x64". The URL 404'd,
# gitleaks never executed, and the repo carried a required check named
# "Gitleaks (full-history)" that had never scanned a single commit. The asset
# name is now resolved from the release API, and -- more importantly -- the run
# leaves behind a receipt so that "the scanner could not start" can never again
# be mistaken for "the scanner found nothing".
#
# Exit codes are deliberately distinct. Callers must treat 3 and 4 as
# infrastructure faults, not as scan verdicts:
#
#   0  scan completed, no findings
#   1  scan completed, findings present (see the SARIF report)
#   2  usage error
#   3  install failed  -- asset resolution, download, checksum or version check
#   4  scan did not complete, or completed without a usable report
#
# A receipt (JSON) is written to GITLEAKS_RECEIPT_PATH on 0 and 1 only. Its
# absence is proof the gate did not run.
#
# Environment:
#   GITLEAKS_VERSION       pinned version, without the leading "v" (default 8.30.1)
#   GITLEAKS_SOURCE        directory to scan (default ".")
#   GITLEAKS_REPORT_PATH   SARIF output path
#   GITLEAKS_RECEIPT_PATH  receipt output path
#   GITLEAKS_BIN_DIR       where to install the binary
#   GITLEAKS_CONFIG        ruleset path (default .gitleaks.toml)
#   GITLEAKS_IGNORE_PATH   allowlist path (default .gitleaksignore)
#   GH_TOKEN / GITHUB_TOKEN  optional, raises the release-API rate limit

set -euo pipefail

VERSION="${GITLEAKS_VERSION:-8.30.1}"
VERSION="${VERSION#v}"
SOURCE_DIR="${GITLEAKS_SOURCE:-.}"
REPORT_PATH="${GITLEAKS_REPORT_PATH:-/tmp/gitleaks.sarif}"
RECEIPT_PATH="${GITLEAKS_RECEIPT_PATH:-/tmp/gitleaks-receipt.json}"
BIN_DIR="${GITLEAKS_BIN_DIR:-/usr/local/bin}"
CONFIG_PATH="${GITLEAKS_CONFIG:-.gitleaks.toml}"
IGNORE_PATH="${GITLEAKS_IGNORE_PATH:-.gitleaksignore}"

REPO="gitleaks/gitleaks"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

EXIT_USAGE=2
EXIT_INSTALL=3
EXIT_SCAN=4

log()  { printf '[gitleaks-scan] %s\n' "$*" >&2; }

# ::error:: renders as an annotation on the job in the GitHub UI, so an
# infrastructure fault is legible without opening the raw log.
fail() {
  local code="$1"; shift
  printf '::error::%s\n' "$*" >&2
  log "FATAL ($code): $*"
  exit "$code"
}

summary() {
  [ -n "${GITHUB_STEP_SUMMARY:-}" ] || return 0
  printf '%s\n' "$*" >>"$GITHUB_STEP_SUMMARY"
}

for tool in curl tar jq sha256sum; do
  command -v "$tool" >/dev/null 2>&1 \
    || fail "$EXIT_USAGE" "required tool not on PATH: $tool"
done

[ -d "$SOURCE_DIR" ] || fail "$EXIT_USAGE" "source directory does not exist: $SOURCE_DIR"

api() {
  local url="$1" out="$2"
  local -a auth=()
  local token="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
  [ -n "$token" ] && auth=(-H "Authorization: Bearer ${token}")
  curl -sSfL --retry 3 --retry-delay 2 --max-time 60 \
    -H "Accept: application/vnd.github+json" \
    "${auth[@]}" -o "$out" "$url"
}

# ---------------------------------------------------------------------------
# 1. Resolve the release asset by name, rather than guessing the URL.
# ---------------------------------------------------------------------------
ASSET_NAME=""
ASSET_URL=""
ASSET_SOURCE=""

resolve_from_api() {
  local json="$WORK_DIR/release.json"
  api "https://api.github.com/repos/${REPO}/releases/tags/v${VERSION}" "$json" \
    || return 1

  # Accept whichever 64-bit Linux naming this release happens to use. The list
  # is ordered by preference, and every candidate is a real name that gitleaks
  # has shipped -- "x64" is current, "amd64" is what older releases used.
  local candidate
  for candidate in "linux_x64" "linux_amd64"; do
    ASSET_NAME="$(jq -r --arg want "gitleaks_${VERSION}_${candidate}.tar.gz" \
      '.assets[] | select(.name == $want) | .name' "$json" | head -n1)"
    if [ -n "$ASSET_NAME" ] && [ "$ASSET_NAME" != "null" ]; then
      ASSET_URL="$(jq -r --arg want "$ASSET_NAME" \
        '.assets[] | select(.name == $want) | .browser_download_url' "$json" | head -n1)"
      ASSET_SOURCE="release-api"
      return 0
    fi
  done

  log "release v${VERSION} has no 64-bit Linux tarball under any known name; assets are:"
  jq -r '.assets[].name' "$json" >&2 || true
  return 1
}

# Fallback for the case where the API itself is unreachable or rate limited --
# probe the candidate URLs directly. This still never invents a name silently:
# whichever path was used is recorded in the receipt.
resolve_by_probe() {
  local candidate url
  for candidate in "linux_x64" "linux_amd64"; do
    url="https://github.com/${REPO}/releases/download/v${VERSION}/gitleaks_${VERSION}_${candidate}.tar.gz"
    if curl -sSfL -I --max-time 30 -o /dev/null "$url" 2>/dev/null; then
      ASSET_NAME="gitleaks_${VERSION}_${candidate}.tar.gz"
      ASSET_URL="$url"
      ASSET_SOURCE="url-probe"
      return 0
    fi
  done
  return 1
}

log "resolving gitleaks v${VERSION} release asset"
if ! resolve_from_api; then
  log "release API did not yield an asset; falling back to URL probing"
  resolve_by_probe \
    || fail "$EXIT_INSTALL" \
         "cannot resolve a 64-bit Linux asset for gitleaks v${VERSION} via the release API or URL probing"
fi
log "resolved ${ASSET_NAME} via ${ASSET_SOURCE}"

# ---------------------------------------------------------------------------
# 2. Download and verify the archive before trusting it.
# ---------------------------------------------------------------------------
TARBALL="$WORK_DIR/${ASSET_NAME}"
curl -sSfL --retry 3 --retry-delay 2 --max-time 180 -o "$TARBALL" "$ASSET_URL" \
  || fail "$EXIT_INSTALL" "download failed: $ASSET_URL"

[ -s "$TARBALL" ] || fail "$EXIT_INSTALL" "downloaded archive is empty: $ASSET_NAME"

CHECKSUMS="$WORK_DIR/checksums.txt"
curl -sSfL --retry 3 --retry-delay 2 --max-time 60 \
  -o "$CHECKSUMS" \
  "https://github.com/${REPO}/releases/download/v${VERSION}/gitleaks_${VERSION}_checksums.txt" \
  || fail "$EXIT_INSTALL" "cannot fetch checksums for gitleaks v${VERSION}"

EXPECTED_SHA="$(awk -v n="$ASSET_NAME" '$2 == n || $2 == "*"n {print $1}' "$CHECKSUMS" | head -n1)"
[ -n "$EXPECTED_SHA" ] \
  || fail "$EXIT_INSTALL" "no checksum listed for ${ASSET_NAME}"

ACTUAL_SHA="$(sha256sum "$TARBALL" | awk '{print $1}')"
[ "$EXPECTED_SHA" = "$ACTUAL_SHA" ] \
  || fail "$EXIT_INSTALL" "checksum mismatch for ${ASSET_NAME}: expected ${EXPECTED_SHA}, got ${ACTUAL_SHA}"
log "checksum verified: ${ACTUAL_SHA}"

tar -xzf "$TARBALL" -C "$WORK_DIR" gitleaks \
  || fail "$EXIT_INSTALL" "archive ${ASSET_NAME} does not contain a 'gitleaks' binary"

mkdir -p "$BIN_DIR" 2>/dev/null || true
if [ -w "$BIN_DIR" ]; then
  install -m 0755 "$WORK_DIR/gitleaks" "$BIN_DIR/gitleaks"
elif command -v sudo >/dev/null 2>&1; then
  sudo install -m 0755 "$WORK_DIR/gitleaks" "$BIN_DIR/gitleaks"
else
  fail "$EXIT_INSTALL" "cannot install into ${BIN_DIR}: not writable and sudo unavailable"
fi
GITLEAKS_BIN="$BIN_DIR/gitleaks"

# The binary must be the version we pinned. A cached or pre-existing gitleaks of
# a different vintage would scan with a different rule engine than we reviewed.
INSTALLED_VERSION="$("$GITLEAKS_BIN" version 2>/dev/null | tr -d 'v \r\n')" \
  || fail "$EXIT_INSTALL" "installed gitleaks binary is not executable"
[ "$INSTALLED_VERSION" = "$VERSION" ] \
  || fail "$EXIT_INSTALL" "version mismatch: pinned v${VERSION}, installed v${INSTALLED_VERSION:-unknown}"
log "installed gitleaks v${INSTALLED_VERSION} at ${GITLEAKS_BIN}"

# ---------------------------------------------------------------------------
# 3. Scan. From here on the scanner exists, so any non-verdict outcome is a
#    scan integrity fault (4), never a silent pass.
# ---------------------------------------------------------------------------
rm -f "$REPORT_PATH" "$RECEIPT_PATH"

declare -a scan_args=(
  detect
  --no-banner
  --redact
  --source "$SOURCE_DIR"
  --exit-code 1
  --report-format sarif
  --report-path "$REPORT_PATH"
)
[ -f "$CONFIG_PATH" ] && scan_args+=(--config "$CONFIG_PATH")
[ -f "$IGNORE_PATH" ] && scan_args+=(--gitleaks-ignore-path "$IGNORE_PATH")

log "scanning ${SOURCE_DIR} (full history)"
set +e
"$GITLEAKS_BIN" "${scan_args[@]}"
SCAN_RC=$?
set -e
log "gitleaks exited ${SCAN_RC}"

# gitleaks uses 0 for clean and --exit-code (1) for findings. Anything else is
# the scanner itself failing -- a config parse error, an unreadable repo, an
# internal panic -- and must not be reported as a verdict.
if [ "$SCAN_RC" -ne 0 ] && [ "$SCAN_RC" -ne 1 ]; then
  fail "$EXIT_SCAN" "gitleaks terminated abnormally with exit ${SCAN_RC}; no verdict was produced"
fi

[ -s "$REPORT_PATH" ] \
  || fail "$EXIT_SCAN" "gitleaks exited ${SCAN_RC} but wrote no SARIF report to ${REPORT_PATH}"

jq -e '.runs[0].tool.driver.name == "gitleaks"' "$REPORT_PATH" >/dev/null 2>&1 \
  || fail "$EXIT_SCAN" "SARIF at ${REPORT_PATH} is not a gitleaks report"

FINDINGS="$(jq '[.runs[].results // [] | length] | add // 0' "$REPORT_PATH")"

# Cross-check the exit code against the report. If these disagree, one of the
# two is lying and we cannot say what the scan found.
if [ "$SCAN_RC" -eq 0 ] && [ "$FINDINGS" -ne 0 ]; then
  fail "$EXIT_SCAN" "gitleaks reported success but the SARIF lists ${FINDINGS} findings"
fi
if [ "$SCAN_RC" -eq 1 ] && [ "$FINDINGS" -eq 0 ]; then
  fail "$EXIT_SCAN" "gitleaks signalled findings but the SARIF lists none"
fi

# ---------------------------------------------------------------------------
# 4. Receipt. Written only once a verdict is established.
# ---------------------------------------------------------------------------
COMMIT="$(git -C "$SOURCE_DIR" rev-parse HEAD 2>/dev/null || echo unknown)"
COMMITS_SCANNED="$(git -C "$SOURCE_DIR" rev-list --count HEAD 2>/dev/null || echo 0)"

# A full-history gate that scanned no history is not clean, it is vacuous.
# This catches a shallow checkout (fetch-depth regressed away from 0), a
# detached or empty repository, and a source directory that is not a git repo
# at all -- each of which otherwise yields a confident, meaningless "0 findings".
if [ "${COMMITS_SCANNED:-0}" -eq 0 ]; then
  fail "$EXIT_SCAN" \
    "gitleaks scanned 0 commits in ${SOURCE_DIR}: nothing was examined, so '${FINDINGS} findings' is not a result. Check that the checkout is a full-history git clone (fetch-depth: 0)."
fi

jq -n \
  --arg version "$INSTALLED_VERSION" \
  --arg asset "$ASSET_NAME" \
  --arg asset_source "$ASSET_SOURCE" \
  --arg sha256 "$ACTUAL_SHA" \
  --arg report "$REPORT_PATH" \
  --arg commit "$COMMIT" \
  --argjson commits_scanned "$COMMITS_SCANNED" \
  --argjson findings "$FINDINGS" \
  --argjson exit_code "$SCAN_RC" \
  '{scanner:"gitleaks", version:$version, asset:$asset, asset_source:$asset_source,
    sha256:$sha256, report:$report, commit:$commit,
    commits_scanned:$commits_scanned, findings:$findings, exit_code:$exit_code,
    verdict:(if $findings == 0 then "clean" else "findings" end)}' \
  >"$RECEIPT_PATH" \
  || fail "$EXIT_SCAN" "could not write receipt to ${RECEIPT_PATH}"

if [ "$FINDINGS" -eq 0 ]; then
  log "PASS: gitleaks v${INSTALLED_VERSION} scanned ${COMMITS_SCANNED} commits, 0 findings"
  summary "### Gitleaks (full-history): clean"
  summary ""
  summary "\`gitleaks v${INSTALLED_VERSION}\` scanned **${COMMITS_SCANNED}** commits and found **0** secrets."
  exit 0
fi

log "FAIL: gitleaks v${INSTALLED_VERSION} found ${FINDINGS} secret(s)"
summary "### Gitleaks (full-history): ${FINDINGS} finding(s)"
summary ""
summary "\`gitleaks v${INSTALLED_VERSION}\` scanned **${COMMITS_SCANNED}** commits."
summary "Values are redacted here and in the SARIF; see the Security tab for detail."
summary ""
summary "| rule | file | count |"
summary "| --- | --- | ---: |"
jq -r '.runs[0].results[] |
       "\(.ruleId)\t\(.locations[0].physicalLocation.artifactLocation.uri)"' \
  "$REPORT_PATH" | sort | uniq -c | sort -rn | head -25 \
  | while read -r count rule file; do summary "| ${rule} | ${file} | ${count} |"; done
exit 1
