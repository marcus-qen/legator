# Windows Remote Probe via WinRM

Legator can manage Windows hosts remotely **without installing a probe binary** on the target. Commands and inventory are collected via [WinRM](https://learn.microsoft.com/en-us/windows/win32/winrm/portal) (Windows Remote Management), Microsoft's SOAP-over-HTTP management protocol.

---

## Prerequisites on the Windows target

### 1. Enable WinRM

Run the following in an elevated PowerShell session on the target:

```powershell
# Enable WinRM with default settings
Enable-PSRemoting -Force

# Verify the service is running
Get-Service WinRM

# Check the configured listeners
winrm enumerate winrm/config/listener
```

### 2. Configure authentication

Legator supports three authentication methods:

| Method    | Use case                                  | Default port |
|-----------|-------------------------------------------|-------------|
| `ntlm`    | Workgroup or domain environments (default)| 5985 (HTTP) |
| `kerberos`| Active Directory domain environments      | 5985 (HTTP) |
| `basic`   | Simple setups (requires HTTPS)            | 5986 (HTTPS)|

#### NTLM (recommended default)

No additional configuration is needed if the user account has WinRM access.
NTLM is negotiated automatically.

```powershell
# Allow NTLM (usually already enabled)
winrm set winrm/config/service/auth '@{NTLM="true"}'

# Ensure the Legator user is in the Remote Management Users group
Add-LocalGroupMember -Group "Remote Management Users" -Member "legator-user"
```

#### Kerberos (Active Directory)

Kerberos requires a valid `krb5.conf` on the probe host and the probe host to be domain-joined (or to have a Kerberos credential cache).

```powershell
# Kerberos is enabled by default in AD environments
winrm set winrm/config/service/auth '@{Kerberos="true"}'
```

Probe configuration additionally requires `krb_realm`, `krb_config`, and optionally `krb_ccache`.

#### Basic (dev/test only)

Basic auth **must** be used with HTTPS (`https: true`) to avoid credentials in plaintext.

```powershell
# Enable basic auth on the service (not recommended for production)
winrm set winrm/config/service/auth '@{Basic="true"}'
winrm set winrm/config/service '@{AllowUnencrypted="false"}'
```

### 3. Firewall rules

```powershell
# HTTP (5985)
New-NetFirewallRule -Name "WinRM-HTTP"  -DisplayName "WinRM HTTP"  -Protocol TCP -LocalPort 5985 -Direction Inbound -Action Allow
# HTTPS (5986, if using TLS)
New-NetFirewallRule -Name "WinRM-HTTPS" -DisplayName "WinRM HTTPS" -Protocol TCP -LocalPort 5986 -Direction Inbound -Action Allow
```

### 4. Verify connectivity from the probe host

```bash
# HTTP
curl -v http://WINDOWS_HOST:5985/wsman

# HTTPS (skip TLS verify for self-signed certs)
curl -vk https://WINDOWS_HOST:5986/wsman
```

---

## Probe configuration

Add one or more `winrm_targets` entries to the probe's `config.yaml`:

```yaml
server_url: https://legator.example.com
probe_id: probe-xyz
api_key: lgk_probe_...

winrm_targets:
  # NTLM example (most common)
  - name: web-server-01
    host: 192.168.1.50
    user: Administrator
    password: "s3cret!"
    auth: ntlm

  # Kerberos example (Active Directory)
  - name: dc01
    host: dc01.corp.local
    user: legator@CORP.LOCAL
    password: "kerbpass"
    auth: kerberos
    krb_realm: CORP.LOCAL
    krb_config: /etc/krb5.conf

  # Basic auth over HTTPS
  - name: test-vm
    host: 192.168.1.99
    port: 5986
    user: Administrator
    password: "testpass"
    auth: basic
    https: true
    insecure: true   # allow self-signed cert

  # Custom port with timeout
  - name: edge-device
    host: 10.0.0.200
    port: 5985
    user: winrm-user
    password: "edgepass"
    timeout: 60s
    labels:
      environment: production
      role: edge
```

### Configuration reference

| Field       | Type     | Default       | Description                                         |
|-------------|----------|---------------|-----------------------------------------------------|
| `name`      | string   | **required**  | Logical name for this target                        |
| `host`      | string   | **required**  | IP address or hostname                              |
| `user`      | string   | **required**  | Windows user account                                |
| `password`  | string   | **required**  | Password (consider a secrets manager for production)|
| `auth`      | string   | `ntlm`        | Authentication: `basic`, `ntlm`, `kerberos`         |
| `port`      | int      | 5985 / 5986   | WinRM port (auto-selected based on `https`)         |
| `https`     | bool     | `false`       | Use HTTPS (port 5986 by default)                    |
| `insecure`  | bool     | `false`       | Skip TLS certificate verification                   |
| `timeout`   | duration | `30s`         | Connection and command timeout                      |
| `krb_realm` | string   | —             | Kerberos realm (required for `kerberos` auth)       |
| `krb_config`| string   | —             | Path to `krb5.conf`                                 |
| `krb_ccache`| string   | —             | Path to Kerberos credential cache                   |
| `krb_spn`   | string   | —             | Service Principal Name override                     |
| `labels`    | map      | —             | Arbitrary key/value labels for the inventory        |

---

## Inventory collection

When WinRM is configured, the probe collects the following via PowerShell cmdlets:

| Data              | PowerShell cmdlet                    |
|-------------------|--------------------------------------|
| OS version/arch   | `Get-ComputerInfo` (Windows 10+)     |
| Hostname          | `$env:COMPUTERNAME` (fallback)       |
| CPU count         | `Get-ComputerInfo` / `Win32_ComputerSystem` |
| Memory total      | `Win32_ComputerSystem.TotalPhysicalMemory` |
| Disk total        | `Win32_LogicalDisk` (fixed drives)   |
| Running services  | `Get-Service`                        |
| Installed packages| `Get-Package`                        |
| Network adapters  | `Get-NetIPConfiguration`             |

Output is serialised as JSON with `ConvertTo-Json -Compress` to minimise bandwidth.

### Fallback behaviour

- If `Get-ComputerInfo` is unavailable (Windows Server 2012 R2, older systems), individual WMI/CIM calls are used.
- Partial failures (e.g., `Get-Package` access denied) are silently skipped — the inventory is returned with whatever data was successfully collected.

---

## Remote command execution

WinRM targets support the same `CommandPayload` protocol as locally-installed probes. Commands are dispatched through the `WinRMAdapter` which applies standard policy enforcement (capability level, blocklist, allowlist) before executing.

```json
{
  "type": "command",
  "payload": {
    "request_id": "req-abc",
    "command": "powershell",
    "args": ["Get-Process | Sort-Object CPU -Descending | Select-Object -First 10"],
    "level": "remediate",
    "timeout": "30s"
  }
}
```

All PowerShell commands run via `powershell.exe -EncodedCommand` on the target. Non-PowerShell executables are wrapped in `& 'path'` call syntax.

---

## Security considerations

1. **Use NTLM or Kerberos, not Basic auth** — Basic auth sends credentials in near-plaintext over HTTP.
2. **Principle of least privilege** — create a dedicated `legator` user and add it only to `Remote Management Users`. Avoid using `Administrator` in production.
3. **Network segmentation** — restrict WinRM port (5985/5986) access to probe hosts only via firewall rules.
4. **Encrypt passwords at rest** — use the probe's secrets manager or vault integration; never commit passwords to git.
5. **TLS for HTTPS** — use certificates from a trusted CA, not self-signed certs, in production (`insecure: false`).
6. **Rotate credentials** — use the platform's credential rotation workflow to cycle WinRM passwords.

---

## Troubleshooting

### Access denied

```
winrm: execution error: The WS-Management service cannot process the request...
```

- Verify the user is in `Remote Management Users`.
- Check `winrm get winrm/config/service/auth` shows the authentication type enabled.
- For NTLM: ensure the account is not locked out.

### Connection refused

```
winrm: build client: dial tcp <host>:5985: connect: connection refused
```

- Verify WinRM is running: `Get-Service WinRM`.
- Check the firewall rule exists: `Get-NetFirewallRule -Name "WinRM-HTTP"`.
- Confirm the correct port (5985 vs 5986).

### Kerberos failures

- Ensure `krb5.conf` points to the correct KDC.
- Verify system time on probe and target are in sync (Kerberos requires < 5 min skew).
- Test with `kinit <user>@REALM` on the probe host.
