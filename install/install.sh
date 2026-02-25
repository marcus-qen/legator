#!/usr/bin/env bash
# Legator Probe — One-liner installer
#
# Usage:
#   curl -sSL https://cp.example.com/install.sh | sudo bash -s -- \
#     --server https://cp.example.com \
#     --token prb_a8f3d1e9c2b4_1772060000_hmac_7f2a
#
# What this does:
#   1. Detects OS and architecture
#   2. Downloads the probe binary
#   3. Verifies SHA256 checksum
#   4. Installs to /usr/local/bin/probe
#   5. Runs `probe init` to register with the control plane
#   6. Installs and starts the systemd service

set -euo pipefail

PROBE_BIN="/usr/local/bin/probe"
PROBE_CONFIG_DIR="/etc/probe"
PROBE_DATA_DIR="/var/lib/probe"
PROBE_LOG_DIR="/var/log/probe"

# --- Parse arguments ---
SERVER=""
TOKEN=""
while [[ $# -gt 0 ]]; do
  case $1 in
    --server|-s) SERVER="$2"; shift 2 ;;
    --token|-t)  TOKEN="$2"; shift 2 ;;
    --help|-h)   usage; exit 0 ;;
    *)           echo "Unknown argument: $1"; exit 1 ;;
  esac
done

if [[ -z "$SERVER" || -z "$TOKEN" ]]; then
  echo "Error: --server and --token are required"
  echo ""
  echo "Usage:"
  echo "  curl -sSL https://cp.example.com/install.sh | sudo bash -s -- \\"
  echo "    --server https://cp.example.com \\"
  echo "    --token prb_xxx"
  exit 1
fi

# --- Detect OS and architecture ---
detect_platform() {
  OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
  ARCH="$(uname -m)"

  case "$OS" in
    linux)  OS="linux" ;;
    darwin) OS="darwin" ;;
    *)      echo "Unsupported OS: $OS"; exit 1 ;;
  esac

  case "$ARCH" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    armv7l)        ARCH="armv7" ;;
    *)             echo "Unsupported architecture: $ARCH"; exit 1 ;;
  esac

  echo "Detected platform: ${OS}/${ARCH}"
}

# --- Check prerequisites ---
check_prereqs() {
  if [[ "$(id -u)" -ne 0 ]]; then
    echo "Error: this script must be run as root (use sudo)"
    exit 1
  fi

  for cmd in curl sha256sum; do
    if ! command -v "$cmd" &>/dev/null; then
      echo "Error: $cmd is required but not found"
      exit 1
    fi
  done
}

# --- Download and verify ---
download_probe() {
  local url="${SERVER}/download/probe-${OS}-${ARCH}"
  local checksum_url="${SERVER}/download/probe-${OS}-${ARCH}.sha256"

  echo "Downloading probe binary..."
  curl -sSL -o "${PROBE_BIN}.tmp" "$url"

  echo "Downloading checksum..."
  local expected
  expected=$(curl -sSL "$checksum_url" | awk '{print $1}')

  echo "Verifying SHA256..."
  local actual
  actual=$(sha256sum "${PROBE_BIN}.tmp" | awk '{print $1}')

  if [[ "$expected" != "$actual" ]]; then
    echo "CHECKSUM MISMATCH!"
    echo "  Expected: $expected"
    echo "  Got:      $actual"
    rm -f "${PROBE_BIN}.tmp"
    exit 1
  fi

  mv "${PROBE_BIN}.tmp" "$PROBE_BIN"
  chmod 0755 "$PROBE_BIN"
  echo "Binary installed to ${PROBE_BIN}"
}

# --- Create directories ---
setup_dirs() {
  mkdir -p "$PROBE_CONFIG_DIR" "$PROBE_DATA_DIR" "$PROBE_LOG_DIR"
  chmod 0700 "$PROBE_CONFIG_DIR"
}

# --- Register with control plane ---
register_probe() {
  echo "Registering with control plane..."
  "$PROBE_BIN" init --server "$SERVER" --token "$TOKEN"
}

# --- Install systemd service ---
install_service() {
  "$PROBE_BIN" service install
  echo "Service installed and started."
}

# --- Main ---
main() {
  echo "=== Legator Probe Installer ==="
  echo ""
  check_prereqs
  detect_platform
  setup_dirs
  download_probe
  register_probe
  install_service

  echo ""
  echo "✅ Probe installed and registered."
  echo "   Config:  ${PROBE_CONFIG_DIR}/config.yaml"
  echo "   Binary:  ${PROBE_BIN}"
  echo "   Service: systemctl status probe-agent"
  echo ""
  echo "The probe is now reporting to ${SERVER}."
}

main
