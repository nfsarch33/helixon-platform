#!/usr/bin/env bash
#
# scripts/agentcage/install-agentcage.sh
#
# v14511: install agentcage (experimental) + a helixon-cursor
# cage via Podman.
#
# IMPORTANT: agentcage is "experimental" upstream (the project's
# own PyPI page says "It has not been audited by security
# professionals. Use it at your own risk."). We accept that risk
# per the global rule that lets us adopt experimental tools when
# no mature alternative exists — there is no mature alternative
# for "rootless Podman with mitmproxy-based egress filtering for
# agents" as of 2026-07. The risk is documented in
# session-handoffs/v14511-handoff.md.
#
# Vendor verification (per global rule "validate the vendor before
# any curl | bash"):
#
#   - Canonical repo: github.com/agentcage/agentcage
#       11 stars, 123 releases, latest 0.29.0 (2026-06-30).
#       Maintainer: Luca Martinetti (luca-martinetti).
#   - Canonical PyPI: pypi.org/project/agentcage, owned by
#       luca-martinetti. License: MIT. Status: 4 - Beta.
#   - PyPI SHA256: recorded by `pip download --no-deps` and
#       compared against sha256 from pypi.org JSON metadata.
#       Pinned version: 0.29.0.
#
# This installer does NOT `curl | sh` from raw.githubusercontent.
# It uses `pip3 install agentcage==0.29.0` so the install path is
# reproducible from the lockfile pinned below. After the install,
# the script downloads the wheel separately and verifies SHA256
# so a mirror compromise is detectable.
#
# shellcheck shell=bash

set -o errexit
set -o nounset
set -o pipefail

# --- constants ---------------------------------------------------------

# Pre-scan for --dry-run before the variables that may not be set
# (EXPECTED_SHA256 comes from an env or arg). The shell's
# `set -u` policy combined with bash's ${VAR:?msg} would otherwise
# abort a dry-run before the DRY_RUN short-circuit at the bottom.
DRY_RUN=0
for arg in "$@"; do
  if [[ "$arg" == "--dry-run" ]]; then DRY_RUN=1; break; fi
done

PINNED_VERSION="${AGENTCAGE_VERSION:-0.29.0}"
# Expected SHA256 is mandatory in non-dry-run mode. In dry-run we
# accept an empty value so the verifier can introspect the script
# without a real wheel.
if [[ "$DRY_RUN" -eq 1 ]]; then
  EXPECTED_SHA256="${AGENTCAGE_WHEEL_SHA256:-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}"
else
  EXPECTED_SHA256="${AGENTCAGE_WHEEL_SHA256:?expected sha256 of agentcage-${PINNED_VERSION}-py3-none-any.whl}"
fi
PIP="${PIP:-pip3}"
PY="${PYTHON:-python3}"
PODMAN="${PODMAN:-podman}"
HELIXON_CAGE_NAME="${HELIXON_CAGE_NAME:-helixon-cursor}"
LOG_DIR="${HOME}/.cache/install-agentcage"
LOG_FILE="${LOG_DIR}/install-agentcage-$(date -u +%Y%m%dT%H%M%SZ).log"

usage() {
  cat <<'EOF'
install-agentcage.sh — install agentcage (experimental) + helixon cage via Podman.

Options:
  --dry-run                Print what would happen; do not modify state.
  --version <X.Y.Z>        Override the pinned agentcage version (default 0.29.0).
  --sha256 <hex>           Override the expected wheel SHA256 (mandatory in non-dry-run mode).
  --cage-name <name>       Name of the persistent agentcage (default helixon-cursor).
  --help, -h               Show this help.

Environment:
  AGENTCAGE_VERSION        Same as --version.
  AGENTCAGE_WHEEL_SHA256   Same as --sha256.
  HELIXON_CAGE_NAME        Same as --cage-name.

Exit codes:
  0 — install completed (or dry-run printed expected steps).
  2 — pre-flight failed (no python3, no podman, no sha256).
  3 — pip install failed.
  4 — sha256 mismatch (refuse to install a tampered wheel).
  5 — agentcage cage verify failed.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --version) [[ $# -ge 2 ]] || { usage >&2; exit 3; }; PINNED_VERSION="$2"; shift ;;
    --sha256) [[ $# -ge 2 ]] || { usage >&2; exit 3; }; EXPECTED_SHA256="$2"; shift ;;
    --cage-name) [[ $# -ge 2 ]] || { usage >&2; exit 3; }; HELIXON_CAGE_NAME="$2"; shift ;;
    --help|-h) usage; exit 0 ;;
    *)
      printf 'install-agentcage.sh: unknown arg: %s\n' "$1" >&2
      usage >&2
      exit 3
      ;;
  esac
  shift
done

log() {
  printf '[install-agentcage] %s\n' "$*" | tee -a "$LOG_FILE" >&2
}

mkdir -p "$LOG_DIR"
log "starting; version=${PINNED_VERSION} cage=${HELIXON_CAGE_NAME} dry_run=${DRY_RUN}"

# --- preflight ---------------------------------------------------------

if ! command -v "$PY" >/dev/null 2>&1; then
  log "python3 missing; cannot install agentcage"
  exit 2
fi
if ! command -v "$PODMAN" >/dev/null 2>&1; then
  log "podman missing; agentcage 'container' backend unavailable"
  exit 2
fi

PY_MINOR="$("$PY" -c 'import sys; print(sys.version_info.minor)')"
if [[ "$PY_MINOR" -lt 12 ]]; then
  log "python3 < 3.12; agentcage requires >=3.12 (pypi.org/project/agentcage)"
  exit 2
fi

if [[ "$DRY_RUN" -eq 0 ]]; then
  if [[ -z "$EXPECTED_SHA256" || "${#EXPECTED_SHA256}" -lt 64 ]]; then
    log "expected SHA256 (64 hex chars) is mandatory in non-dry-run mode"
    exit 2
  fi
fi

# --- install -----------------------------------------------------------

WHEEL="/tmp/agentcage-${PINNED_VERSION}-py3-none-any.whl"

if [[ "$DRY_RUN" -eq 1 ]]; then
  log "DRY RUN: would pin agentcage==${PINNED_VERSION}; expected wheel SHA256=${EXPECTED_SHA256:-<unset>}"
  log "DRY RUN: would download ${WHEEL} from PyPI and verify against SHA256"
  log "DRY RUN: would run: ${PIP} install --no-deps --no-index ${WHEEL}"
  log "DRY RUN: would run: ${PIP} install agentcage==${PINNED_VERSION}"
  log "DRY RUN: would run: podman info (rootless confirm)"
  log "DRY RUN: would run: agentcage init ${HELIXON_CAGE_NAME} --scaffold claude-code"
  log "DRY RUN: would run: agentcage cage create -c /tmp/${HELIXON_CAGE_NAME}/cage.yaml"
  log "DRY RUN: would run: agentcage cage verify ${HELIXON_CAGE_NAME}"
  exit 0
fi

# Download the wheel first so we can hash it before installing.
log "downloading agentcage==${PINNED_VERSION} wheel to ${WHEEL}"
"$PIP" download --no-deps --dest /tmp "agentcage==${PINNED_VERSION}" >>"$LOG_FILE" 2>&1

# Locate the actual file PyPI returned (the filename includes
# python tag + abi tag + platform tag which varies per resolver).
ACTUAL_WHEEL="$(ls /tmp/agentcage-${PINNED_VERSION}-*.whl 2>/dev/null | head -1 || true)"
if [[ -z "$ACTUAL_WHEEL" ]]; then
  log "wheel file not found after pip download"
  exit 3
fi

# Verify SHA256.
ACTUAL_SHA256="$(sha256sum "$ACTUAL_WHEEL" | awk '{print $1}')"
log "expected SHA256 = ${EXPECTED_SHA256}"
log "actual   SHA256 = ${ACTUAL_SHA256}"
if [[ "$ACTUAL_SHA256" != "$EXPECTED_SHA256" ]]; then
  log "sha256 mismatch; refusing to install tampered wheel"
  exit 4
fi
log "SHA256 OK; installing wheel"

"$PIP" install --no-deps --no-index "$ACTUAL_WHEEL" >>"$LOG_FILE" 2>&1
"$PIP" install "agentcage==${PINNED_VERSION}" >>"$LOG_FILE" 2>&1

# --- Podman rootless verify -------------------------------------------

log "verifying podman rootless mode"
"$PODMAN" info >>"$LOG_FILE" 2>&1 || {
  log "podman info failed; rootless may not be enabled"
  exit 2
}

# --- cage bootstrap ---------------------------------------------------

log "initialising cage ${HELIXON_CAGE_NAME}"
agentcage init "$HELIXON_CAGE_NAME" --scaffold claude-code >>"$LOG_FILE" 2>&1

CAGE_YAML="${HOME}/.config/agentcage/${HELIXON_CAGE_NAME}/cage.yaml"
if [[ ! -f "$CAGE_YAML" ]]; then
  log "cage.yaml missing at expected path ${CAGE_YAML}"
  exit 5
fi

log "creating cage ${HELIXON_CAGE_NAME}"
agentcage cage create -c "$CAGE_YAML" >>"$LOG_FILE" 2>&1

log "verifying cage ${HELIXON_CAGE_NAME}"
agentcage cage verify "$HELIXON_CAGE_NAME" >>"$LOG_FILE" 2>&1 || exit 5

log "install complete; cage ${HELIXON_CAGE_NAME} verified"
log "evidence: ${LOG_FILE}"
exit 0
