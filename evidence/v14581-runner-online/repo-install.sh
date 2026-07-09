#!/bin/bash
set -e

# Installation script for runner/gitlab-runner APT repository
#
# Usage:
#   curl -fsSL https://packages.gitlab.com/install/repositories/runner/gitlab-runner/script.deb.sh | sudo bash
#
# With authentication:
#   username=myuser password=mypass ./script.deb.sh

username="${username:-}"
password="${password:-}"

fail_unsupported ()
{
  cat >&2 <<EOF
System configuration not recognized.
Set os= and dist= environment variables to override detection.
Reference: https://docs.gitlab.com/install/

Example: os=ubuntu dist=focal ./script.deb.sh

Contact support for assistance.
EOF
  exit 1
}

require_tool ()
{
  local tool=$1
  local pkg=$2

  if ! command -v "$tool" &> /dev/null; then
    apt-get install -y "$pkg" || {
      cat >&2 <<EOF
Failed to install $pkg. System package repositories may be misconfigured.
EOF
      exit 1
    }
  fi
}

setup_debian_keyring ()
{
  [ "${os,,}" != "debian" ] && return 0
  apt-get install -y debian-archive-keyring &> /dev/null
}

detect_system_info ()
{
  [ -n "$os" ] && [ -n "$dist" ] && return 0

  if [ -f /etc/os-release ]; then
    . /etc/os-release
    os=$ID
    if [ -n "$VERSION_CODENAME" ]; then
      dist=$VERSION_CODENAME
    elif [ -n "$VERSION_ID" ]; then
      dist=$VERSION_ID
    fi
  elif [ -f /etc/lsb-release ]; then
    . /etc/lsb-release
    if [ "$DISTRIB_ID" = "raspbian" ]; then
      os=$ID
      dist=$(cut -d. -f1 /etc/debian_version)
    else
      os=$DISTRIB_ID
      dist=$DISTRIB_CODENAME
      [ -z "$dist" ] && dist=$DISTRIB_RELEASE
    fi
  elif command -v lsb_release &> /dev/null; then
    dist=$(lsb_release -c | cut -f2)
    os=$(lsb_release -i | cut -f2 | awk '{print tolower($1)}')
  elif [ -f /etc/debian_version ]; then
    os=$(head -1 /etc/issue | awk '{print tolower($1)}')
    if grep -q '/' /etc/debian_version; then
      dist=$(cut -d/ -f1 /etc/debian_version)
    else
      dist=$(cut -d. -f1 /etc/debian_version)
    fi
  else
    fail_unsupported
  fi

  [ -z "$dist" ] && fail_unsupported
  os="${os// /}"
  os="${os,,}"
  dist="${dist// /}"
}

parse_apt_version ()
{
  local ver
  ver=$(apt-get -v 2>/dev/null | head -1 | awk '{print $2}') || {
    echo >&2 "Failed to detect apt version"
    exit 1
  }

  apt_version_major=$(echo "$ver" | cut -d. -f1)
  apt_version_minor=$(echo "$ver" | cut -d. -f2)
  apt_version_modified="${apt_version_major}${apt_version_minor}0"
}

fetch_repo_config ()
{
  local url=$1
  local dest=$2
  local curl_opts

  if [ -n "$username" ] && [ -n "$password" ]; then
    curl_opts=(-sSfL -u "${username}:${password}")
  else
    curl_opts=(-sSfL)
  fi

  curl "${curl_opts[@]}" "$url" > "$dest" || {
    local code=$?
    rm -f "$dest"

    case $code in
      22)
        cat >&2 <<EOF
Repository configuration unavailable at: $url
This OS/distribution may not be supported, or detection failed.
Override with: os=ubuntu dist=focal ./script.deb.sh
See: https://docs.gitlab.com/install/
EOF
        ;;
      35|60)
        cat >&2 <<EOF
TLS connection failed to https://packages.gitlab.com
Possible causes:
  - Missing ca-certificates package
  - Outdated libssl version
EOF
        ;;
      *)
        cat >&2 <<EOF
Failed to retrieve: $url
Verify curl is working correctly.
EOF
        ;;
    esac
    exit 1
  }
}

setup_gpg_key ()
{
  local key_url=$1
  local keyring_path=$2
  local keyring_dir=$3
  local old_path=$4
  local curl_opts

  if [ -n "$username" ] && [ -n "$password" ]; then
    curl_opts=(-fsSL -u "${username}:${password}")
  else
    curl_opts=(-fsSL)
  fi

  mkdir -p "$keyring_dir"
  curl "${curl_opts[@]}" "$key_url" | gpg --dearmor > "$keyring_path" || {
    local code=$?
    rm -f "$keyring_path"
    cat >&2 <<EOF
Failed to download or import GPG key from: $key_url
Exit code: $code
EOF
    exit 1
  }

  if [ ! -s "$keyring_path" ]; then
    rm -f "$keyring_path"
    cat >&2 <<EOF
GPG key download produced empty file from: $key_url
EOF
    exit 1
  fi

  chmod 0644 "$keyring_path"

  if [ "$apt_version_modified" -lt 110 ]; then
    mv "$keyring_path" "$old_path"
    chmod 0644 "$old_path"
    rmdir "$keyring_dir" 2>/dev/null || true
  fi
}

inject_credentials_to_source_file ()
{
  local source_file=$1

  if [ -n "$username" ] && [ -n "$password" ]; then
    sed -i "s|https://|https://${username}:${password}@|g" "$source_file"
  fi
}

main ()
{
  detect_system_info
  require_tool gpg gnupg
  parse_apt_version
  setup_debian_keyring
  apt-get install -y apt-transport-https &> /dev/null

  local key_url="https://packages.gitlab.com/gpgkey/gpg.key"
  local config_url="https://packages.gitlab.com/install/repositories/runner/gitlab-runner/${os}/${dist}/config_file.list"
  local source_file="/etc/apt/sources.list.d/runner_gitlab-runner.list"
  local keyring_dir="/etc/apt/keyrings"
  local keyring_file="${keyring_dir}/runner_gitlab-runner-archive-keyring.gpg"
  local old_keyring="/etc/apt/trusted.gpg.d/runner_gitlab-runner.gpg"

  fetch_repo_config "$config_url" "$source_file"
  inject_credentials_to_source_file "$source_file"
  setup_gpg_key "$key_url" "$keyring_file" "$keyring_dir" "$old_keyring"

  apt-get update || {
    cat >&2 <<EOF
Failed to update package lists. The repository may be misconfigured.
EOF
    exit 1
  }

  cat <<EOF
Repository configured successfully.
Ready to install packages.
EOF
}

main
