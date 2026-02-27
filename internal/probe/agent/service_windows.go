//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName        = "probe-agent"
	serviceDisplayName = "Legator Probe Agent"
)

func ServiceInstall(configDir string) error {
	probeBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate probe executable: %w", err)
	}
	probeBin, _ = filepath.Abs(probeBin)

	if configDir == "" {
		configDir = DefaultConfigDir
	}

	if _, err := os.Stat(ConfigPath(configDir)); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("config not found at %s — run 'probe init' first", ConfigPath(configDir))
	} else if err != nil {
		return fmt.Errorf("check config: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	if existing, err := m.OpenService(serviceName); err == nil {
		existing.Close()
		return fmt.Errorf("service %s already installed", serviceName)
	}

	svcConfig := mgr.Config{
		DisplayName: serviceDisplayName,
		Description: "Legator Probe Agent",
		StartType:   mgr.StartAutomatic,
	}

	s, err := m.CreateService(serviceName, probeBin, svcConfig, "run", "--config-dir", configDir)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		_ = s.Delete()
		return fmt.Errorf("start service: %w", err)
	}

	fmt.Printf("✅ Service %s installed and started\n", serviceName)
	fmt.Printf("   Binary: %s\n", probeBin)
	fmt.Printf("   Status: probe service status\n")
	return nil
}

func ServiceStart() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service: %w", err)
	}
	if status.State == svc.Running {
		return nil
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return waitForServiceState(s, svc.Running, 20*time.Second)
}

func ServiceStop() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service: %w", err)
	}
	if status.State == svc.Stopped {
		return nil
	}

	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	return waitForServiceState(s, svc.Stopped, 20*time.Second)
}

func ServiceRemove() error {
	_ = ServiceStop()

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return nil
	}
	defer s.Close()

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}

	fmt.Printf("✅ Service %s removed\n", serviceName)
	return nil
}

func ServiceStatus() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		fmt.Printf("Service %s: not installed\n", serviceName)
		return nil
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service: %w", err)
	}

	fmt.Printf("Service %s: %s\n", serviceName, serviceStateString(status.State))
	return nil
}

func waitForServiceState(s *mgr.Service, state svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == state {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for service state %s", serviceStateString(state))
}

func serviceStateString(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start-pending"
	case svc.StopPending:
		return "stop-pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue-pending"
	case svc.PausePending:
		return "pause-pending"
	case svc.Paused:
		return "paused"
	default:
		return "unknown"
	}
}
