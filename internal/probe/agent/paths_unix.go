//go:build !windows

package agent

func defaultConfigDir() string {
	return "/etc/legator"
}

func defaultDataDir() string {
	return "/var/lib/legator"
}

func defaultLogDir() string {
	return "/var/log/legator"
}
