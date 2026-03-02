package fleet

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marcus-qen/legator/internal/protocol"
	"golang.org/x/crypto/ssh"
)

const (
	defaultRemoteCommandTimeout   = 30 * time.Second
	defaultRemoteInventoryTimeout = 45 * time.Second
	defaultRemoteDialTimeout      = 10 * time.Second
	defaultRemoteOutputMaxBytes   = 128 * 1024
)

// RemoteExecutionTarget contains SSH connection details for a remote probe.
type RemoteExecutionTarget struct {
	Host       string
	Port       int
	Username   string
	Password   string
	PrivateKey string
}

// RemoteRunResult is the normalized command execution result from a runner.
type RemoteRunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// RemoteCommandRunner executes a command on a remote target.
type RemoteCommandRunner interface {
	Run(ctx context.Context, target RemoteExecutionTarget, command string, timeout time.Duration, onChunk func(stream, data string)) (*RemoteRunResult, error)
}

// RemoteProbeExecutor is the server-facing contract for remote probe execution.
type RemoteProbeExecutor interface {
	Execute(ctx context.Context, ps *ProbeState, cmd protocol.CommandPayload, onChunk func(protocol.OutputChunkPayload)) (*protocol.CommandResultPayload, error)
	CollectInventory(ctx context.Context, ps *ProbeState) (*protocol.InventoryPayload, error)
}

// RemoteExecutor runs SSH-backed commands and inventory collection for remote probes.
type RemoteExecutor struct {
	runner           RemoteCommandRunner
	defaultTimeout   time.Duration
	inventoryTimeout time.Duration
	maxOutputBytes   int
	now              func() time.Time
}

func NewRemoteExecutor() *RemoteExecutor {
	return &RemoteExecutor{
		runner:           &sshRemoteCommandRunner{dialTimeout: defaultRemoteDialTimeout},
		defaultTimeout:   defaultRemoteCommandTimeout,
		inventoryTimeout: defaultRemoteInventoryTimeout,
		maxOutputBytes:   defaultRemoteOutputMaxBytes,
		now:              func() time.Time { return time.Now().UTC() },
	}
}

// Execute runs a command on a remote probe and optionally emits output chunks.
func (e *RemoteExecutor) Execute(ctx context.Context, ps *ProbeState, cmd protocol.CommandPayload, onChunk func(protocol.OutputChunkPayload)) (*protocol.CommandResultPayload, error) {
	if e == nil {
		return nil, fmt.Errorf("remote executor is nil")
	}
	if cmd.RequestID == "" {
		cmd.RequestID = fmt.Sprintf("remote-%d", time.Now().UnixNano())
	}

	target, err := remoteTargetFromProbe(ps)
	if err != nil {
		return nil, err
	}

	command := buildRemoteCommand(cmd)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}

	timeout := cmd.Timeout
	if timeout <= 0 {
		timeout = e.defaultTimeout
	}

	seq := 0
	emit := func(stream, data string) {
		if onChunk == nil || data == "" {
			return
		}
		seq++
		onChunk(protocol.OutputChunkPayload{
			RequestID: cmd.RequestID,
			Stream:    stream,
			Data:      data,
			Seq:       seq,
			Final:     false,
		})
	}

	started := e.now()
	runResult, runErr := e.runner.Run(ctx, target, command, timeout, emit)
	if runErr != nil {
		return nil, runErr
	}
	if runResult == nil {
		return nil, fmt.Errorf("empty remote execution result")
	}

	stdout, stdoutTrunc := truncateOutput(runResult.Stdout, e.maxOutputBytes)
	stderr, stderrTrunc := truncateOutput(runResult.Stderr, e.maxOutputBytes)
	truncated := stdoutTrunc || stderrTrunc

	result := &protocol.CommandResultPayload{
		RequestID: cmd.RequestID,
		ExitCode:  runResult.ExitCode,
		Stdout:    stdout,
		Stderr:    stderr,
		Duration:  runResult.Duration.Milliseconds(),
		Truncated: truncated,
	}
	if result.Duration <= 0 {
		result.Duration = e.now().Sub(started).Milliseconds()
	}

	if onChunk != nil {
		seq++
		onChunk(protocol.OutputChunkPayload{
			RequestID: cmd.RequestID,
			Stream:    "stdout",
			Seq:       seq,
			Final:     true,
			ExitCode:  result.ExitCode,
		})
	}

	return result, nil
}

// CollectInventory performs a best-effort SSH inventory scrape.
func (e *RemoteExecutor) CollectInventory(ctx context.Context, ps *ProbeState) (*protocol.InventoryPayload, error) {
	target, err := remoteTargetFromProbe(ps)
	if err != nil {
		return nil, err
	}

	timeout := e.inventoryTimeout
	if timeout <= 0 {
		timeout = defaultRemoteInventoryTimeout
	}

	run := func(command string) (string, error) {
		result, runErr := e.runner.Run(ctx, target, command, timeout, nil)
		if runErr != nil {
			return "", runErr
		}
		if result == nil {
			return "", fmt.Errorf("empty response for command %q", command)
		}
		if result.ExitCode != 0 {
			stderr := strings.TrimSpace(result.Stderr)
			if stderr == "" {
				stderr = "non-zero exit code"
			}
			return "", fmt.Errorf("%s", stderr)
		}
		return strings.TrimSpace(result.Stdout), nil
	}

	hostname, err := run("hostname")
	if err != nil {
		return nil, err
	}

	osName, _ := run("uname -s")
	arch, _ := run("uname -m")
	kernel, _ := run("uname -r")
	cpusRaw, _ := run("getconf _NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null || echo 1")
	memRaw, _ := run("awk '/MemTotal/ {print $2*1024}' /proc/meminfo 2>/dev/null || echo 0")
	diskRaw, _ := run("df -B1 --total 2>/dev/null | awk '/total/ {print $2}' | tail -n1")
	interfacesRaw, _ := run("ip -o addr show 2>/dev/null || ip addr 2>/dev/null")
	packagesRaw, _ := run("(dpkg-query -W -f='${Package}\t${Version}\n' 2>/dev/null || rpm -qa --qf '%{NAME}\t%{VERSION}-%{RELEASE}\n' 2>/dev/null || apk info -v 2>/dev/null || true) | head -n 100")
	servicesRaw, _ := run("(systemctl list-units --type=service --no-pager --no-legend 2>/dev/null || service --status-all 2>/dev/null || true) | head -n 100")
	usersRaw, _ := run("getent passwd 2>/dev/null || cat /etc/passwd 2>/dev/null || true")

	cpus, _ := strconv.Atoi(strings.TrimSpace(cpusRaw))
	memTotal, _ := strconv.ParseUint(strings.TrimSpace(memRaw), 10, 64)
	diskTotal, _ := strconv.ParseUint(strings.TrimSpace(diskRaw), 10, 64)

	inv := &protocol.InventoryPayload{
		ProbeID:     ps.ID,
		Hostname:    firstNonEmpty(hostname, ps.Hostname),
		OS:          strings.ToLower(firstNonEmpty(osName, ps.OS)),
		Arch:        firstNonEmpty(arch, ps.Arch),
		Kernel:      strings.TrimSpace(kernel),
		CPUs:        cpus,
		MemTotal:    memTotal,
		DiskTotal:   diskTotal,
		Interfaces:  parseRemoteInterfaces(interfacesRaw),
		Packages:    parseRemotePackages(packagesRaw),
		Services:    parseRemoteServices(servicesRaw),
		Users:       parseRemoteUsers(usersRaw),
		Metadata:    map[string]string{"probe_type": ProbeTypeRemote, "remote_host": target.Host},
		CollectedAt: e.now(),
	}

	if inv.CPUs <= 0 {
		inv.CPUs = 1
	}
	if inv.OS == "" {
		inv.OS = "linux"
	}

	return inv, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func buildRemoteCommand(cmd protocol.CommandPayload) string {
	base := strings.TrimSpace(cmd.Command)
	if base == "" {
		return ""
	}
	if len(cmd.Args) == 0 {
		return base
	}
	args := make([]string, 0, len(cmd.Args))
	for _, arg := range cmd.Args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		args = append(args, shellQuote(trimmed))
	}
	if len(args) == 0 {
		return base
	}
	return base + " " + strings.Join(args, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"$`\\") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func remoteTargetFromProbe(ps *ProbeState) (RemoteExecutionTarget, error) {
	if ps == nil {
		return RemoteExecutionTarget{}, fmt.Errorf("probe is required")
	}
	if normalizeProbeType(ps.Type) != ProbeTypeRemote {
		return RemoteExecutionTarget{}, fmt.Errorf("probe %s is not a remote probe", ps.ID)
	}
	if ps.Remote == nil {
		return RemoteExecutionTarget{}, fmt.Errorf("remote probe %s missing connection config", ps.ID)
	}
	if ps.RemoteCredentials == nil {
		return RemoteExecutionTarget{}, fmt.Errorf("remote probe %s missing credentials", ps.ID)
	}

	target := RemoteExecutionTarget{
		Host:       strings.TrimSpace(ps.Remote.Host),
		Port:       normalizeRemotePort(ps.Remote.Port),
		Username:   strings.TrimSpace(ps.Remote.Username),
		Password:   strings.TrimSpace(ps.RemoteCredentials.Password),
		PrivateKey: strings.TrimSpace(ps.RemoteCredentials.PrivateKey),
	}
	if target.Host == "" || target.Username == "" {
		return RemoteExecutionTarget{}, fmt.Errorf("remote probe %s has incomplete host/username config", ps.ID)
	}
	if target.Password == "" && target.PrivateKey == "" {
		return RemoteExecutionTarget{}, fmt.Errorf("remote probe %s has no SSH credentials", ps.ID)
	}
	return target, nil
}

func truncateOutput(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value, false
	}
	return value[:maxBytes], true
}

func parseRemoteInterfaces(raw string) []protocol.NetInterface {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	lines := strings.Split(raw, "\n")
	interfaces := make([]protocol.NetInterface, 0, len(lines))
	seen := map[string]*protocol.NetInterface{}

	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		name := fields[1]
		entry, ok := seen[name]
		if !ok {
			interfaces = append(interfaces, protocol.NetInterface{Name: name, State: "up"})
			entry = &interfaces[len(interfaces)-1]
			seen[name] = entry
		}
		for _, field := range fields {
			if strings.Contains(field, "/") {
				entry.Addrs = append(entry.Addrs, field)
			}
		}
		if len(interfaces) >= 32 {
			break
		}
	}

	if len(interfaces) == 0 {
		return nil
	}
	return interfaces
}

func parseRemotePackages(raw string) []protocol.Package {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	packages := make([]protocol.Package, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pkg := protocol.Package{Name: fields[0], Manager: "system"}
		if len(fields) > 1 {
			pkg.Version = fields[1]
		}
		packages = append(packages, pkg)
		if len(packages) >= 100 {
			break
		}
	}
	if len(packages) == 0 {
		return nil
	}
	return packages
}

func parseRemoteServices(raw string) []protocol.Service {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	services := make([]protocol.Service, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		state := "unknown"
		enabled := false
		if strings.Contains(line, "running") || strings.Contains(line, "active") {
			state = "running"
		}
		if strings.Contains(line, "enabled") {
			enabled = true
		}
		services = append(services, protocol.Service{Name: fields[0], State: state, Enabled: enabled})
		if len(services) >= 100 {
			break
		}
	}
	if len(services) == 0 {
		return nil
	}
	return services
}

func parseRemoteUsers(raw string) []protocol.User {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	users := make([]protocol.User, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, ":")
		if len(parts) < 7 {
			continue
		}
		uid, _ := strconv.Atoi(parts[2])
		users = append(users, protocol.User{Name: parts[0], UID: uid, Shell: parts[6]})
		if len(users) >= 100 {
			break
		}
	}
	if len(users) == 0 {
		return nil
	}
	return users
}

type sshRemoteCommandRunner struct {
	dialTimeout time.Duration
}

func (r *sshRemoteCommandRunner) Run(ctx context.Context, target RemoteExecutionTarget, command string, timeout time.Duration, onChunk func(stream, data string)) (*RemoteRunResult, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command is required")
	}

	clientConfig, err := remoteSSHClientConfig(target, r.dialTimeout)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(target.Host, strconv.Itoa(normalizeRemotePort(target.Port)))
	client, err := sshDialContext(ctx, "tcp", address, clientConfig, r.dialTimeout)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return nil, err
	}

	started := time.Now()
	if err := session.Start(command); err != nil {
		return nil, err
	}

	stdoutBuf := bytes.Buffer{}
	stderrBuf := bytes.Buffer{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		streamRemotePipe(stdoutPipe, &stdoutBuf, "stdout", onChunk)
	}()
	go func() {
		defer wg.Done()
		streamRemotePipe(stderrPipe, &stderrBuf, "stderr", onChunk)
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- session.Wait()
	}()

	if timeout <= 0 {
		timeout = defaultRemoteCommandTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var waitErr error
	select {
	case <-ctx.Done():
		_ = session.Close()
		wg.Wait()
		return nil, ctx.Err()
	case <-timer.C:
		_ = session.Close()
		wg.Wait()
		return nil, fmt.Errorf("remote command timeout")
	case waitErr = <-waitCh:
	}

	wg.Wait()

	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			return nil, waitErr
		}
	}

	return &RemoteRunResult{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode,
		Duration: time.Since(started),
	}, nil
}

func streamRemotePipe(reader io.Reader, dst *bytes.Buffer, stream string, onChunk func(stream, data string)) {
	bufReader := bufio.NewReader(reader)
	for {
		chunk := make([]byte, 2048)
		n, err := bufReader.Read(chunk)
		if n > 0 {
			_, _ = dst.Write(chunk[:n])
			if onChunk != nil {
				onChunk(stream, string(chunk[:n]))
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
	}
}

func remoteSSHClientConfig(target RemoteExecutionTarget, timeout time.Duration) (*ssh.ClientConfig, error) {
	authMethods := make([]ssh.AuthMethod, 0, 2)
	if strings.TrimSpace(target.Password) != "" {
		authMethods = append(authMethods, ssh.Password(target.Password))
	}
	if strings.TrimSpace(target.PrivateKey) != "" {
		signer, err := ssh.ParsePrivateKey([]byte(target.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("invalid private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("missing ssh credentials")
	}

	if timeout <= 0 {
		timeout = defaultRemoteDialTimeout
	}

	return &ssh.ClientConfig{
		User:            target.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // agentless probe bootstrap
		Timeout:         timeout,
	}, nil
}

func sshDialContext(ctx context.Context, network, addr string, config *ssh.ClientConfig, timeout time.Duration) (*ssh.Client, error) {
	type dialResult struct {
		client *ssh.Client
		err    error
	}

	ch := make(chan dialResult, 1)
	go func() {
		client, err := ssh.Dial(network, addr, config)
		ch <- dialResult{client: client, err: err}
	}()

	if timeout <= 0 {
		timeout = defaultRemoteDialTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("ssh dial timeout")
	case res := <-ch:
		return res.client, res.err
	}
}
