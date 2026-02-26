#!/usr/bin/env bash
# Legator Probe Installer
# Usage: curl -sSL https://your-server/install.sh | sudo bash -s -- --server URL --token TOKEN
set -euo pipefail

VERSION="${LEGATOR_VERSION:-latest}"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/legator"
DATA_DIR="/var/lib/legator"
LOG_DIR="/var/log/legator"
SERVICE_USER="legator"

# Parse arguments
SERVER=""
TOKEN=""
ARCH=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server|-s) SERVER="$2"; shift 2 ;;
    --token|-t)  TOKEN="$2"; shift 2 ;;
    --arch)      ARCH="$2"; shift 2 ;;
    --version)   VERSION="$2"; shift 2 ;;
    --help|-h)
      echo "Usage: install.sh --server <url> --token <token> [--arch <arch>] [--version <ver>]"
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

if [[ -z "$SERVER" || -z "$TOKEN" ]]; then
  echo "Error: --server and --token are required"
  echo "Usage: install.sh --server <url> --token <token>"
  exit 1
fi

# Detect architecture
if [[ -z "$ARCH" ]]; then
  case "$(uname -m)" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    armv7l|armhf)  ARCH="arm" ;;
    *)
      echo "Unsupported architecture: $(uname -m)"
      exit 1
      ;;
  esac
fi

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
if [[ "$OS" != "linux" ]]; then
  echo "Unsupported OS: $OS (only Linux supported)"
  exit 1
fi

BINARY="legator-probe-${OS}-${ARCH}"
DOWNLOAD_URL="${SERVER}/download/${BINARY}"
CHECKSUM_URL="${SERVER}/download/${BINARY}.sha256"

echo "╔══════════════════════════════════════════╗"
echo "║        Legator Probe Installer           ║"
echo "╚══════════════════════════════════════════╝"
echo ""
echo "Server:   ${SERVER}"
echo "OS/Arch:  ${OS}/${ARCH}"
echo "Version:  ${VERSION}"
echo ""

# Check root
if [[ $EUID -ne 0 ]]; then
  echo "Error: This installer must be run as root (use sudo)"
  exit 1
fi

# Create service user
if ! id -u "${SERVICE_USER}" &>/dev/null; then
  echo "→ Creating service user: ${SERVICE_USER}"
  useradd --system --no-create-home --shell /usr/sbin/nologin "${SERVICE_USER}"
fi

# Create directories
echo "→ Creating directories"
mkdir -p "${CONFIG_DIR}" "${DATA_DIR}" "${LOG_DIR}"
chown "${SERVICE_USER}:${SERVICE_USER}" "${DATA_DIR}" "${LOG_DIR}"

# Download binary
echo "→ Downloading probe binary"
if command -v curl &>/dev/null; then
  curl -fsSL -o "/tmp/${BINARY}" "${DOWNLOAD_URL}"
  if curl -fsSL -o "/tmp/${BINARY}.sha256" "${CHECKSUM_URL}" 2>/dev/null; then
    echo "→ Verifying checksum"
    cd /tmp && sha256sum -c "${BINARY}.sha256"
  fi
elif command -v wget &>/dev/null; then
  wget -q -O "/tmp/${BINARY}" "${DOWNLOAD_URL}"
else
  echo "Error: curl or wget required"
  exit 1
fi

# Install binary
echo "→ Installing binary to ${INSTALL_DIR}"
install -m 755 "/tmp/${BINARY}" "${INSTALL_DIR}/legator-probe"
rm -f "/tmp/${BINARY}" "/tmp/${BINARY}.sha256"

# Register with control plane
echo "→ Registering with control plane"
"${INSTALL_DIR}/legator-probe" init --server "${SERVER}" --token "${TOKEN}"

# Install systemd service
echo "→ Installing systemd service"
cat > /etc/systemd/system/legator-probe.service << EOF
[Unit]
Description=Legator Probe Agent
After=network-online.target
Wants=network-online.target
Documentation=https://legator.io/docs

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
ExecStart=${INSTALL_DIR}/legator-probe run
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
EOF

systemctl daemon-reload
systemctl enable legator-probe
systemctl start legator-probe

echo ""
echo "✅ Legator probe installed and running!"
echo ""
echo "   Status:    systemctl status legator-probe"
echo "   Logs:      journalctl -u legator-probe -f"
echo "   Config:    ${CONFIG_DIR}/"
echo "   Uninstall: legator-probe uninstall"
