# Legator Deployment Guide

## Control Plane

### Systemd Service

```ini
[Unit]
Description=Legator Control Plane
After=network.target

[Service]
Type=simple
ExecStart=/opt/legator/bin/control-plane
EnvironmentFile=/home/marcus/.config/legator/llm.env
Environment=LEGATOR_DATA_DIR=/var/lib/legator
Environment=LEGATOR_LISTEN_ADDR=:9080
Environment=LEGATOR_AUTH=true
WorkingDirectory=/opt/legator
Restart=on-failure
RestartSec=5s

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/legator
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

### Configuration

- **Config file**: `/opt/legator/legator.json`
- **LLM credentials**: `/home/marcus/.config/legator/llm.env`
- **Templates**: `/opt/legator/web/templates/`
- **Data stores**: `/var/lib/legator/` (fleet.db, audit.db, chat.db, webhook.db, policy.db)

### Reverse Proxy (Caddy)

```caddyfile
legator.lab.k-dev.uk {
    reverse_proxy http://localhost:9080
}
```

No Pomerium — Legator handles its own API key auth. Probes need raw WebSocket without OIDC.

## Probe

### Installation

```bash
# One-liner install
curl -sSL https://legator.lab.k-dev.uk/install.sh | sudo bash -s -- \
  --server https://legator.lab.k-dev.uk \
  --token <registration-token>

# Manual
./probe run --server https://legator.lab.k-dev.uk --token <token>
```

### Configuration

Probe config at `<config-dir>/config.yaml`:

```yaml
server_url: "https://legator.lab.k-dev.uk"
probe_id: "prb-XXXXXXXX"
token: "<auth-token>"
```

## Upgrade

1. Build new binary: `make build VERSION=vX.Y.Z`
2. Stop service: `sudo systemctl stop legator-control-plane`
3. Copy binary: `sudo cp bin/control-plane /opt/legator/bin/control-plane`
4. Copy templates: `sudo cp -r web/templates/ /opt/legator/web/templates/`
5. Start service: `sudo systemctl start legator-control-plane`
6. Verify: `curl -s https://legator.lab.k-dev.uk/version`

Probes reconnect automatically after control plane restart (exponential backoff with jitter).
