// Package agent — WinRM target configuration.
// WinRMTargetConfig extends the probe's config to define remote Windows hosts
// that can be managed via WinRM without requiring a probe binary on the target.
package agent

import "time"

// WinRMTargetConfig defines a remote Windows host managed via WinRM.
// It maps directly to the executor.WinRMConfig; the probe resolves targets
// by name when dispatching commands to Windows hosts.
type WinRMTargetConfig struct {
	// Name is a logical identifier for this target (e.g. "web-server-01").
	Name string `yaml:"name"`
	// Host is the IP address or DNS name of the Windows host.
	Host string `yaml:"host"`
	// Port overrides the default WinRM port (5985 HTTP / 5986 HTTPS).
	Port int `yaml:"port,omitempty"`
	// User is the Windows account used for authentication.
	User string `yaml:"user"`
	// Password is the account password. Use a secrets manager in production.
	Password string `yaml:"password"`
	// Auth selects the authentication mechanism: basic, ntlm (default), kerberos.
	Auth string `yaml:"auth,omitempty"`
	// HTTPS enables TLS (uses port 5986 by default when true).
	HTTPS bool `yaml:"https,omitempty"`
	// Insecure skips TLS certificate verification (dev/test only).
	Insecure bool `yaml:"insecure,omitempty"`
	// Timeout is the per-command connection and execution timeout.
	Timeout time.Duration `yaml:"timeout,omitempty"`
	// Labels are arbitrary key/value pairs added to this target's inventory.
	Labels map[string]string `yaml:"labels,omitempty"`

	// Kerberos-specific fields (required when Auth == "kerberos").

	// KrbRealm is the Kerberos realm, e.g. "CORP.LOCAL".
	KrbRealm string `yaml:"krb_realm,omitempty"`
	// KrbConfig is the path to the krb5.conf file on the probe host.
	KrbConfig string `yaml:"krb_config,omitempty"`
	// KrbCCache is the path to the Kerberos credential cache on the probe host.
	KrbCCache string `yaml:"krb_ccache,omitempty"`
	// KrbSPN is a service principal name override (leave empty for auto-detection).
	KrbSPN string `yaml:"krb_spn,omitempty"`
}
