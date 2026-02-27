//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"strings"
)

func defaultConfigDir() string {
	return filepath.Join(programDataRoot(), "Legator", "probe-config")
}

func defaultDataDir() string {
	return filepath.Join(programDataRoot(), "Legator")
}

func defaultLogDir() string {
	return filepath.Join(programDataRoot(), "Legator")
}

func programDataRoot() string {
	if v := strings.TrimSpace(os.Getenv("ProgramData")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("PROGRAMDATA")); v != "" {
		return v
	}
	return `C:\ProgramData`
}
