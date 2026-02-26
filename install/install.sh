#!/usr/bin/env bash
# Legator Probe Installer
# Usage: curl -sSL https://your-server/install.sh | sudo bash -s -- --server URL --token TOKEN
set -euo pipefail

VERSION="${LEGATOR_VERSION:-latest}"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/probe"
DATA_DIR="/var/lib/probe"
LOG_DIR="/var/log/probe"
SERVICE_USER="legator"

SERVER=""
TOKEN=""
ARCH=""
NO_START="false"
USE_GITHUB_RELEASE="false"

TMP_DIR="$(mktemp -d)"
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

usage() {
  cat <<USAGE
Usage: install.sh --server <url> --token <token> [options]

Required:
  --server, -s <url>        Control plane URL
  --token,  -t <token>      Single-use registration token

Options:
  --arch <arch>             Override arch (amd64|arm64)
  --version <version>       Binary version (default: latest)
  --config-dir <path>       Config directory (default: /etc/probe)
  --no-start                Install but do not start the service
  --github-release          Download from GitHub Releases instead of CP
  --help, -h                Show this help
USAGE
}

download_file() {
  local url="$1" out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --connect-timeout 10 --retry 2 -o "$out" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$out" "$url"
  else
    echo "Error: curl or wget required" >&2; exit 1
  fi
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac
}

verify_connectivity() {
  local url="${SERVER%/}/healthz"
  echo "→ Verifying connectivity to $url"
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --connect-timeout 8 --max-time 10 "$url" >/dev/null || {
      echo "Error: cannot reach $url" >&2; exit 1
    }
  else
    wget -q -T 10 -O /dev/null "$url" || {
      echo "Error: cannot reach $url" >&2; exit 1
    }
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server|-s)         SERVER="$2"; shift 2 ;;
    --token|-t)          TOKEN="$2"; shift 2 ;;
    --arch)              ARCH="$2"; shift 2 ;;
    --version)           VERSION="$2"; shift 2 ;;
    --config-dir)        CONFIG_DIR="$2"; shift 2 ;;
    --no-start)          NO_START="true"; shift ;;
    --github-release)    USE_GITHUB_RELEASE="true"; shift ;;
    --help|-h)           usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage; exit 1 ;;
  esac
done

if [[ -z "$SERVER" || -z "$TOKEN" ]]; then
  echo "Error: --server and --token are required" >&2
  usage; exit 1
fi

[[ -z "$ARCH" ]] && ARCH="$(detect_arch)"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
if [[ "$OS" != "linux" ]]; then
  echo "Unsupported OS: $OS (only Linux supported)" >&2; exit 1
fi

if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
  echo "Error: curl or wget required" >&2; exit 1
fi

if [[ "$USE_GITHUB_RELEASE" == "true" && "$VERSION" == "latest" ]]; then
  echo "Error: --github-release requires an explicit --version tag" >&2; exit 1
fi

verify_connectivity

echo "╔══════════════════════════════════════════╗"
echo "║        Legator Probe Installer           ║"
echo "╚══════════════════════════════════════════╝"
echo ""
echo "Server:      ${SERVER}"
echo "OS/Arch:     ${OS}/${ARCH}"
echo "Version:     ${VERSION}"
echo "Config dir:  ${CONFIG_DIR}"
echo ""

if [[ $EUID -ne 0 ]]; then
  echo "Error: must be run as root (use sudo)" >&2; exit 1
fi

# Service user
if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  echo "→ Creating service user: ${SERVICE_USER}"
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
fi

echo "→ Creating directories"
mkdir -p "$CONFIG_DIR" "$DATA_DIR" "$LOG_DIR"
chown "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR" "$LOG_DIR"

# Download binary
BINARY="probe-${OS}-${ARCH}"
BIN_PATH="$TMP_DIR/$BINARY"

if [[ "$USE_GITHUB_RELEASE" == "true" ]]; then
  RELEASE_BASE="https://github.com/marcus-qen/legator/releases/download/${VERSION}"
  DOWNLOAD_URL="${RELEASE_BASE}/${BINARY}"
  CHECKSUMS_URL="${RELEASE_BASE}/checksums.txt"
else
  DOWNLOAD_URL="${SERVER%/}/download/${BINARY}"
  CHECKSUMS_URL="${SERVER%/}/download/checksums.txt"
fi

echo "→ Downloading probe binary from $DOWNLOAD_URL"
download_file "$DOWNLOAD_URL" "$BIN_PATH"
if [[ ! -s "$BIN_PATH" ]]; then
  echo "Error: downloaded binary is empty or missing" >&2; exit 1
fi

# Verify checksum if available
echo "→ Verifying checksum"
CHECKSUMS_PATH="$TMP_DIR/checksums.txt"
if download_file "$CHECKSUMS_URL" "$CHECKSUMS_PATH" 2>/dev/null && [[ -s "$CHECKSUMS_PATH" ]]; then
  EXPECTED_SHA="$(grep "${BINARY}$" "$CHECKSUMS_PATH" | awk '{print $1}')"
  if [[ -n "$EXPECTED_SHA" ]]; then
    ACTUAL_SHA="$(sha256sum "$BIN_PATH" | awk '{print $1}')"
    if [[ "$ACTUAL_SHA" != "$EXPECTED_SHA" ]]; then
      echo "Error: checksum mismatch!" >&2
      echo "  expected: $EXPECTED_SHA" >&2
      echo "  actual:   $ACTUAL_SHA" >&2
      exit 1
    fi
    echo "  ✅ SHA256 verified"
  else
    echo "  ⚠ Binary not found in checksums file, skipping verification"
  fi
else
  echo "  ⚠ No checksums available, skipping verification"
fi

# Install binary
echo "→ Installing binary to ${INSTALL_DIR}/legator-probe"
install -m 755 "$BIN_PATH" "${INSTALL_DIR}/legator-probe"

# Register with control plane
echo "→ Registering with control plane"
"${INSTALL_DIR}/legator-probe" init --server "$SERVER" --token "$TOKEN" --config-dir "$CONFIG_DIR"

# Install systemd service
echo "→ Installing systemd service"
cat > /etc/systemd/system/legator-probe.service <<UNIT
[Unit]
Description=Legator Probe Agent
After=network-online.target
Wants=network-online.target
Documentation=https://legator.io/docs

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
ExecStart=${INSTALL_DIR}/legator-probe run --config-dir ${CONFIG_DIR}
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=legator-probe

# Hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=${DATA_DIR} ${LOG_DIR} ${CONFIG_DIR}
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload

if [[ "$NO_START" == "true" ]]; then
  echo ""
  echo "✅ Legator probe installed (not started — use systemctl enable --now legator-probe)"
else
  systemctl enable legator-probe
  systemctl start legator-probe
  echo ""
  echo "✅ Legator probe installed and running!"
fi

echo ""
echo "   Status:    systemctl status legator-probe"
echo "   Logs:      journalctl -u legator-probe -f"
echo "   Config:    ${CONFIG_DIR}/"
echo "   Uninstall: legator-probe uninstall"
