# Ansible Deployment — Legator Probe Fleet

This guide covers deploying and managing Legator probe agents at scale using
the `legator-probe` Ansible role.

---

## Prerequisites

| Requirement | Version |
|---|---|
| Ansible | 2.12+ |
| Python | 3.8+ (on control node) |
| Target OS | Linux (Ubuntu 20.04+, Debian 11+, RHEL/Rocky 8+) |
| Target arch | x86_64 (amd64) or aarch64 (arm64) |
| Init system | systemd |
| Connectivity | SSH + `become` (sudo) on targets |
| Legator CP | Running and reachable from probe hosts |

### Install Ansible

```bash
pip install ansible
# or
brew install ansible
```

### Optional — Molecule (for testing)

```bash
pip install molecule molecule-plugins[docker] ansible-lint yamllint
```

---

## Quick Start

### 1. Clone the repo and change directory

```bash
git clone https://github.com/marcus-qen/legator.git
cd legator/deploy/ansible
```

### 2. Create your inventory

```bash
cp inventory.example.yml inventory.yml
$EDITOR inventory.yml
```

At minimum, set:
- `legator_control_plane_url` — your control plane base URL
- `legator_probe_api_key` — your API key (see "Obtaining API keys" below)
- Your host list under `legator_probes`

### 3. Run the playbook

```bash
# Full deploy
ansible-playbook -i inventory.yml deploy-probes.yml

# Dry run (check mode)
ansible-playbook -i inventory.yml deploy-probes.yml --check --diff

# Target a specific group
ansible-playbook -i inventory.yml deploy-probes.yml --limit web_servers

# Override version at deploy time
ansible-playbook -i inventory.yml deploy-probes.yml \
  -e legator_probe_version=v0.3.0

# Run health checks only
ansible-playbook -i inventory.yml deploy-probes.yml --tags verify
```

---

## Obtaining API Keys

Each probe authenticates with an API key (`lgk_...`). You can either:

**Option A — Registration token (one-time, recommended for first deploy)**

```bash
# Generate a registration token on the control plane
curl -sf -X POST \
  -H "Authorization: Bearer <admin_key>" \
  https://legator.example.com/api/v1/tokens | jq .

# The response includes a token. Pass it as:
#   legator_probe_token: "lgk_..."
# The probe will self-register on first start and write its api_key to config.
```

**Option B — Pre-created API key**

Create an API key per host or per group via the Legator UI or API, then set
`legator_probe_api_key` in your inventory.

---

## Variable Reference

All variables have defaults in `roles/legator-probe/defaults/main.yml`.

### Required

| Variable | Description |
|---|---|
| `legator_control_plane_url` | Base URL of the Legator control plane, e.g. `https://legator.example.com` |
| `legator_probe_api_key` or `legator_probe_token` | Auth key or registration token |

### Binary Installation

| Variable | Default | Description |
|---|---|---|
| `legator_probe_version` | `latest` | Binary version to install, e.g. `v0.3.0` |
| `legator_probe_install_method` | `binary` | `binary`, `deb`, or `rpm` |
| `legator_probe_install_dir` | `/usr/local/bin` | Directory to install the probe binary |
| `legator_probe_binary_name` | `legator-probe` | Binary filename |
| `legator_probe_arch` | auto-detected | `amd64` or `arm64` |
| `legator_probe_download_base` | GitHub releases | Base URL for downloads |
| `legator_probe_download_url` | auto-computed | Full binary download URL (override to use CP-hosted binary) |

### Probe Identity

| Variable | Default | Description |
|---|---|---|
| `legator_probe_id` | `""` | Probe ID (leave blank to auto-assign) |
| `legator_probe_tags` | `[]` | List of string tags, e.g. `["web", "prod", "region-eu"]` |
| `legator_probe_policy_level` | `observe` | `observe` or `enforce` |

### System

| Variable | Default | Description |
|---|---|---|
| `legator_probe_user` | `legator` | System user that runs the probe |
| `legator_probe_group` | `legator` | System group |
| `legator_probe_config_dir` | `/etc/legator` | Config directory |
| `legator_probe_data_dir` | `/var/lib/legator` | Data directory |
| `legator_probe_log_dir` | `/var/log/legator` | Log directory |
| `legator_probe_config_file` | `/etc/legator/probe.yaml` | Config file path |

### Systemd Service

| Variable | Default | Description |
|---|---|---|
| `legator_probe_service_name` | `legator-probe` | systemd unit name |
| `legator_probe_service_enabled` | `true` | Enable service at boot |
| `legator_probe_service_state` | `started` | Desired service state |
| `legator_probe_restart_sec` | `5` | Seconds between restart attempts |

### Health Check

| Variable | Default | Description |
|---|---|---|
| `legator_probe_healthcheck_retries` | `10` | Number of status check retries |
| `legator_probe_healthcheck_delay` | `3` | Seconds between retries |
| `legator_probe_healthcheck_timeout` | `60` | Total timeout in seconds |

---

## Directory Layout

```
deploy/ansible/
├── deploy-probes.yml               # Main playbook
├── inventory.example.yml           # Example inventory (copy → inventory.yml)
└── roles/
    └── legator-probe/
        ├── defaults/
        │   └── main.yml            # All default variables
        ├── handlers/
        │   └── main.yml            # Restart/reload handlers
        ├── meta/
        │   └── main.yml            # Role metadata
        ├── molecule/
        │   └── default/
        │       ├── converge.yml    # Molecule provisioning playbook
        │       ├── molecule.yml    # Molecule driver config (Docker)
        │       ├── requirements.yml
        │       └── verify.yml      # Molecule verification playbook
        ├── tasks/
        │   ├── main.yml            # Entry point
        │   ├── install_binary.yml  # Download binary from GitHub/CP
        │   ├── install_deb.yml     # Install .deb package
        │   ├── install_rpm.yml     # Install .rpm package
        │   └── verify.yml          # Post-deploy health check
        └── templates/
            ├── legator-probe.service.j2   # systemd unit
            └── probe-config.yaml.j2       # /etc/legator/probe.yaml
```

---

## Idempotency

The role is safe to re-run. On subsequent runs:

- **Binary**: `get_url` with `force: false` — only re-downloads if the URL
  destination has changed (i.e. version bumped).
- **Config**: template renders and diffs against disk. If unchanged, no notify.
- **Service**: `systemd` module only restarts when notified by a change in
  binary or config.
- **User/group/dirs**: `state: present` — no-ops if already exist.

---

## Molecule Testing

Run the test suite with Docker:

```bash
cd deploy/ansible/roles/legator-probe
molecule test
```

Or individual steps:

```bash
molecule create      # spin up containers
molecule converge    # apply the role
molecule verify      # run assertions
molecule destroy     # tear down containers
```

Platforms tested: Ubuntu 22.04, Debian 12, Rocky Linux 9.

---

## Rolling Deployments

By default, the playbook uses `serial: 50%` — it deploys to half your fleet at
a time. Adjust per run:

```bash
# Deploy 10 hosts at a time
ansible-playbook -i inventory.yml deploy-probes.yml -e deploy_batch_size=10

# One host at a time (canary)
ansible-playbook -i inventory.yml deploy-probes.yml -e deploy_batch_size=1
```

`max_fail_percentage: 20` aborts the run if more than 20% of hosts fail.

---

## Troubleshooting

### Probe service fails to start

```bash
# Check journal
sudo journalctl -u legator-probe -n 50 --no-pager

# Check config
sudo cat /etc/legator/probe.yaml

# Test binary directly
/usr/local/bin/legator-probe --config /etc/legator/probe.yaml
```

### Connection refused to control plane

- Verify `legator_control_plane_url` is reachable from probe hosts.
- The config templates `https://` to `wss://` — ensure your CP listens on WebSocket.
- Check firewall: probe initiates outbound TCP to CP port (typically 443 or 8080).

### Binary download fails

- Set `legator_probe_download_url` to a URL reachable from the target host.
- Or pre-download the binary and use a local file path / internal HTTP mirror.

---

## Security Notes

- The probe runs as a dedicated `legator` system user (no login shell).
- systemd unit enforces `NoNewPrivileges`, `ProtectSystem=strict`, capability drops.
- Config file mode `0640` — only `legator` user and group can read it.
- API keys are write-only in Ansible — use Ansible Vault to encrypt them:

```bash
ansible-vault encrypt_string 'lgk_your_api_key_here' --name legator_probe_api_key
```

Then in inventory:

```yaml
legator_probe_api_key: !vault |
  $ANSIBLE_VAULT;1.1;AES256
  ...
```
