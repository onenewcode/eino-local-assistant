#!/usr/bin/env bash
# Build this checkout for the current host and install the public CLI command.
set -euo pipefail

readonly app_name="eino"
readonly package_path="./cmd/eino-assistant"

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: scripts/install.sh [--install-dir DIR]

Build the current checkout and install the eino command. macOS and Linux are
supported. The default install directory is /usr/local/bin.

Options:
  --install-dir DIR  Install into DIR instead of /usr/local/bin.
  -h, --help         Show this help text.

Environment:
  EINO_INSTALL_DIR   Default install directory when --install-dir is omitted.
EOF
}

install_dir="${EINO_INSTALL_DIR:-/usr/local/bin}"
while (($# > 0)); do
  case "$1" in
    --install-dir)
      (($# >= 2)) || fail "--install-dir requires a directory"
      install_dir="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

case "$(uname -s)" in
  Darwin|Linux) ;;
  *) fail "unsupported operating system: $(uname -s); only macOS and Linux are supported" ;;
esac

command -v go >/dev/null 2>&1 || fail "Go is required; install Go and try again"
command -v install >/dev/null 2>&1 || fail "the install command is required"
[[ -n "${HOME:-}" ]] || fail "HOME must be set to initialize the global configuration"

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
config_dir="$HOME/.eino-assistant"
config_path="$config_dir/config.toml"
build_dir="$(mktemp -d "${TMPDIR:-/tmp}/eino-install.XXXXXX")"
binary_path="$build_dir/$app_name"
target_path="$install_dir/$app_name"
trap 'rm -rf "$build_dir"' EXIT

if [[ ! -e "$config_dir" ]]; then
  mkdir -m 700 "$config_dir"
elif [[ ! -d "$config_dir" ]]; then
  fail "global configuration path is not a directory: $config_dir"
fi

if [[ -e "$config_path" || -L "$config_path" ]]; then
  log "==> Keeping existing global configuration: $config_path"
  log "    Ensure it contains [model]; existing [projects] entries remain supported"
else
  log "==> Creating global configuration template: $config_path"
  install -m 0600 "$repo_root/config.example.toml" "$config_path"
  log "    Edit this file and set [model] before running $app_name"
fi

log "==> Building $app_name for $(go env GOOS)/$(go env GOARCH)"
(
  cd "$repo_root"
  go build -trimpath -o "$binary_path" "$package_path"
)

if [[ ! -d "$install_dir" ]]; then
  if [[ -w "$(dirname "$install_dir")" ]]; then
    mkdir -p "$install_dir"
  elif command -v sudo >/dev/null 2>&1; then
    log "==> Creating $install_dir with sudo"
    sudo mkdir -p "$install_dir"
  else
    fail "cannot create $install_dir; rerun with --install-dir in a writable directory"
  fi
fi

if [[ -w "$install_dir" ]]; then
  install -m 0755 "$binary_path" "$target_path"
else
  command -v sudo >/dev/null 2>&1 || fail "cannot write to $install_dir; rerun with --install-dir in a writable directory"
  log "==> Installing $target_path with sudo"
  sudo install -m 0755 "$binary_path" "$target_path"
fi

log "==> Verifying $target_path"
"$target_path" version

case ":${PATH}:" in
  *":${install_dir}:"*)
    log "Installed. Run: $app_name"
    ;;
  *)
    log "Installed at $target_path, but $install_dir is not in PATH."
    log "Add this to your shell profile, then start a new shell:"
    log "  export PATH=\"$install_dir:\$PATH\""
    ;;
esac
