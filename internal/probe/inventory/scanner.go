// Package inventory collects system information from the probe's host.
package inventory

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
)

// Scan collects a full system inventory.
func Scan(probeID string) (*protocol.InventoryPayload, error) {
	inv := &protocol.InventoryPayload{
		ProbeID:     probeID,
		Hostname:    hostname(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Kernel:      kernel(),
		CPUs:        runtime.NumCPU(),
		CollectedAt: time.Now().UTC(),
		Labels:      map[string]string{},
		Metadata:    map[string]string{},
	}

	inv.MemTotal = memTotal()
	inv.DiskTotal = diskTotal()
	inv.Interfaces = interfaces()
	inv.Services = services()
	inv.Users = users()
	inv.Packages = packages()

	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		inv.Metadata["k8s_node"] = os.Getenv("NODE_NAME")
		inv.Metadata["k8s_pod"] = os.Getenv("POD_NAME")
		inv.Metadata["k8s_namespace"] = os.Getenv("POD_NAMESPACE")
		inv.Metadata["k8s_cluster"] = "true"
	}

	return inv, nil
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func kernel() string {
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func memTotal() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseUint(fields[1], 10, 64)
				return kb * 1024 // convert kB to bytes
			}
		}
	}
	return 0
}

func diskTotal() uint64 {
	out, err := exec.Command("df", "--output=size", "--total", "-B1").Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		n, _ := strconv.ParseUint(last, 10, 64)
		return n
	}
	return 0
}

func interfaces() []protocol.NetInterface {
	out, err := exec.Command("ip", "-j", "addr", "show").Output()
	if err != nil {
		// Fallback: basic info from /sys
		return interfacesFallback()
	}
	// Parse JSON output from iproute2
	_ = out // TODO: parse ip -j output
	return interfacesFallback()
}

func interfacesFallback() []protocol.NetInterface {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	var result []protocol.NetInterface
	for _, e := range entries {
		name := e.Name()
		if name == "lo" {
			continue
		}
		state := "unknown"
		if data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/operstate", name)); err == nil {
			state = strings.TrimSpace(string(data))
		}
		mac := ""
		if data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/address", name)); err == nil {
			mac = strings.TrimSpace(string(data))
		}
		result = append(result, protocol.NetInterface{
			Name:  name,
			MAC:   mac,
			State: state,
		})
	}
	return result
}

func services() []protocol.Service {
	out, err := exec.Command("systemctl", "list-units", "--type=service", "--all",
		"--no-pager", "--no-legend", "--plain").Output()
	if err != nil {
		return nil
	}
	var result []protocol.Service
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ".service")
		state := fields[3] // active sub-state: running, exited, dead, failed
		result = append(result, protocol.Service{
			Name:    name,
			State:   state,
			Enabled: false, // would need a separate systemctl is-enabled call
		})
	}
	return result
}

func users() []protocol.User {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return nil
	}
	defer f.Close()

	var result []protocol.User
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) < 7 {
			continue
		}
		uid, _ := strconv.Atoi(fields[2])
		// Skip system users (uid < 1000) except root
		if uid > 0 && uid < 1000 {
			continue
		}
		shell := fields[6]
		if shell == "/usr/sbin/nologin" || shell == "/bin/false" {
			continue
		}
		u := protocol.User{
			Name:  fields[0],
			UID:   uid,
			Shell: shell,
		}
		// Get groups
		if lu, err := user.Lookup(fields[0]); err == nil {
			if gids, err := lu.GroupIds(); err == nil {
				for _, gid := range gids {
					if g, err := user.LookupGroupId(gid); err == nil {
						u.Groups = append(u.Groups, g.Name)
					}
				}
			}
		}
		result = append(result, u)
	}
	return result
}

func packages() []protocol.Package {
	// Try dpkg first (Debian/Ubuntu)
	if out, err := exec.Command("dpkg-query", "-W", "-f=${Package}\t${Version}\n").Output(); err == nil {
		return parsePkgList(string(out), "apt")
	}
	// Try rpm
	if out, err := exec.Command("rpm", "-qa", "--queryformat", "%{NAME}\t%{VERSION}\n").Output(); err == nil {
		return parsePkgList(string(out), "rpm")
	}
	return nil
}

func parsePkgList(output, manager string) []protocol.Package {
	var result []protocol.Package
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "\t", 2)
		if len(parts) != 2 {
			continue
		}
		result = append(result, protocol.Package{
			Name:    parts[0],
			Version: parts[1],
			Manager: manager,
		})
	}
	return result
}
