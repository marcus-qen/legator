package networkdevices

// NETCONF client implementing RFC 6241 over SSH transport (RFC 6242).
// Pure Go, no CGO. SSH provided by golang.org/x/crypto/ssh.
//
// Protocol summary:
//  1. Dial TCP to host:port (default 830).
//  2. Open SSH session requesting "netconf" subsystem.
//  3. Both sides exchange <hello> messages ending with ]]>]]>.
//  4. Client sends <rpc> messages; server replies with <rpc-reply>.
//  5. Session ends with <close-session/> RPC.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	netconfEOM     = "]]>]]>"
	netconfPort    = 830
	netconfNS      = "urn:ietf:params:xml:ns:netconf:base:1.0"
	netconfCap10   = "urn:ietf:params:netconf:base:1.0"
	netconfTimeout = 15 * time.Second
)

// netconfHello is the hello message exchanged at session start.
type netconfHello struct {
	XMLName      xml.Name `xml:"hello"`
	Xmlns        string   `xml:"xmlns,attr"`
	Capabilities []string `xml:"capabilities>capability"`
	SessionID    string   `xml:"session-id,omitempty"`
}

// netconfRPC wraps an RPC operation.
type netconfRPC struct {
	XMLName   xml.Name    `xml:"rpc"`
	Xmlns     string      `xml:"xmlns,attr"`
	MessageID string      `xml:"message-id,attr"`
	Body      interface{} `xml:",omitempty"`
}

// netconfGetConfig is the get-config operation.
type netconfGetConfig struct {
	XMLName xml.Name       `xml:"get-config"`
	Source  netconfSource  `xml:"source"`
	Filter  *netconfFilter `xml:"filter,omitempty"`
}

// netconfGet is the get operation (operational state).
type netconfGet struct {
	XMLName xml.Name       `xml:"get"`
	Filter  *netconfFilter `xml:"filter,omitempty"`
}

type netconfSource struct {
	Running *struct{} `xml:"running,omitempty"`
}

type netconfFilter struct {
	Type    string `xml:"type,attr,omitempty"`
	Content string `xml:",innerxml"`
}

// netconfRPCReply is the decoded RPC reply envelope.
type netconfRPCReply struct {
	XMLName   xml.Name     `xml:"rpc-reply"`
	MessageID string       `xml:"message-id,attr"`
	Data      netconfData  `xml:"data"`
	Errors    []netconfErr `xml:"rpc-error"`
}

type netconfData struct {
	Content []byte `xml:",innerxml"`
}

type netconfErr struct {
	Tag      string `xml:"error-tag"`
	Severity string `xml:"error-severity"`
	Message  string `xml:"error-message"`
}

// NetconfClientInterface defines the NETCONF operations used by the enricher.
// Defined as an interface to allow mocking in tests.
type NetconfClientInterface interface {
	GetConfig(ctx context.Context, filter string) ([]byte, error)
	Get(ctx context.Context, filter string) ([]byte, error)
	Close() error
}

// NetconfClient is a live NETCONF client over SSH.
type NetconfClient struct {
	sshClient    *ssh.Client
	session      *ssh.Session
	stdin        io.WriteCloser
	stdout       io.Reader
	sessionID    string
	capabilities []string
	timeout      time.Duration
	msgID        int
}

// NewNetconfClient connects to a device and performs the hello exchange.
func NewNetconfClient(ctx context.Context, cfg NetconfConfig, creds CredentialInput) (*NetconfClient, error) {
	port := cfg.Port
	if port <= 0 {
		port = netconfPort
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = netconfTimeout
	}

	// Build SSH auth methods from config + inline creds
	authMethods := make([]ssh.AuthMethod, 0, 2)
	password := cfg.Password
	if creds.Password != "" {
		password = creds.Password
	}
	privateKey := cfg.PrivateKey
	if creds.PrivateKey != "" {
		privateKey = creds.PrivateKey
	}
	if password != "" {
		authMethods = append(authMethods, ssh.Password(password))
	}
	if privateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(privateKey))
		if err != nil {
			return nil, fmt.Errorf("netconf: parse private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("netconf: no credentials provided")
	}

	sshCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // inventory target, MVP
		Timeout:         timeout,
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", port))
	sshClient, err := sshDialContext(ctx, "tcp", addr, sshCfg, timeout)
	if err != nil {
		return nil, fmt.Errorf("netconf: ssh dial %s: %w", addr, err)
	}

	session, err := sshClient.NewSession()
	if err != nil {
		_ = sshClient.Close()
		return nil, fmt.Errorf("netconf: new ssh session: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		return nil, fmt.Errorf("netconf: stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		return nil, fmt.Errorf("netconf: stdout pipe: %w", err)
	}

	if err := session.RequestSubsystem("netconf"); err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		return nil, fmt.Errorf("netconf: request subsystem: %w", err)
	}

	c := &NetconfClient{
		sshClient: sshClient,
		session:   session,
		stdin:     stdin,
		stdout:    stdout,
		timeout:   timeout,
	}

	if err := c.exchangeHello(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("netconf: hello exchange: %w", err)
	}

	return c, nil
}

// exchangeHello sends the client hello and reads the server hello.
func (c *NetconfClient) exchangeHello() error {
	// Read server hello first (server sends first per RFC 6241 §8.1)
	serverMsg, err := c.readMessage()
	if err != nil {
		return fmt.Errorf("read server hello: %w", err)
	}

	var hello netconfHello
	if err := xml.Unmarshal(serverMsg, &hello); err != nil {
		return fmt.Errorf("parse server hello: %w", err)
	}
	c.sessionID = hello.SessionID
	c.capabilities = hello.Capabilities

	// Send client hello
	clientHello := netconfHello{
		Xmlns:        netconfNS,
		Capabilities: []string{netconfCap10},
	}
	data, err := xml.Marshal(clientHello)
	if err != nil {
		return fmt.Errorf("marshal client hello: %w", err)
	}

	return c.writeMessage(data)
}

// GetConfig retrieves the running configuration via get-config RPC.
// filter is an optional XML subtree filter (empty = no filter = full config).
func (c *NetconfClient) GetConfig(ctx context.Context, filter string) ([]byte, error) {
	op := netconfGetConfig{
		Source: netconfSource{Running: &struct{}{}},
	}
	if filter != "" {
		op.Filter = &netconfFilter{Type: "subtree", Content: filter}
	}
	return c.doRPC(ctx, op)
}

// Get retrieves operational state data via get RPC.
func (c *NetconfClient) Get(ctx context.Context, filter string) ([]byte, error) {
	op := netconfGet{}
	if filter != "" {
		op.Filter = &netconfFilter{Type: "subtree", Content: filter}
	}
	return c.doRPC(ctx, op)
}

// Close sends close-session and tears down the SSH connection.
func (c *NetconfClient) Close() error {
	// Best-effort close-session RPC
	type closeSession struct {
		XMLName xml.Name `xml:"close-session"`
	}
	_ = c.sendRPC(closeSession{})

	var errs []string
	if err := c.stdin.Close(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := c.session.Close(); err != nil && !strings.Contains(err.Error(), "EOF") {
		errs = append(errs, err.Error())
	}
	if err := c.sshClient.Close(); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("netconf close: %s", strings.Join(errs, "; "))
	}
	return nil
}

// doRPC sends an RPC and returns the data content of the reply.
func (c *NetconfClient) doRPC(ctx context.Context, body interface{}) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		if err := c.sendRPC(body); err != nil {
			ch <- result{nil, err}
			return
		}
		raw, err := c.readMessage()
		if err != nil {
			ch <- result{nil, err}
			return
		}
		var reply netconfRPCReply
		if err := xml.Unmarshal(raw, &reply); err != nil {
			// Return raw bytes even if we can't fully parse the envelope
			ch <- result{raw, nil}
			return
		}
		if len(reply.Errors) > 0 {
			msgs := make([]string, 0, len(reply.Errors))
			for _, e := range reply.Errors {
				msgs = append(msgs, fmt.Sprintf("%s: %s", e.Tag, e.Message))
			}
			ch <- result{nil, fmt.Errorf("rpc-error: %s", strings.Join(msgs, "; "))}
			return
		}
		ch <- result{reply.Data.Content, nil}
	}()

	timeout := c.timeout
	if timeout <= 0 {
		timeout = netconfTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("netconf rpc timeout")
	case r := <-ch:
		return r.data, r.err
	}
}

// sendRPC serialises an RPC body and writes it to the session.
func (c *NetconfClient) sendRPC(body interface{}) error {
	c.msgID++
	rpc := netconfRPC{
		Xmlns:     netconfNS,
		MessageID: fmt.Sprintf("%d", c.msgID),
		Body:      body,
	}
	data, err := xml.Marshal(rpc)
	if err != nil {
		return fmt.Errorf("marshal rpc: %w", err)
	}
	return c.writeMessage(data)
}

// writeMessage writes an XML message followed by the EOM marker.
func (c *NetconfClient) writeMessage(data []byte) error {
	_, err := fmt.Fprintf(c.stdin, "%s\n%s\n", data, netconfEOM)
	return err
}

// readMessage reads bytes from stdout until the EOM marker is found.
func (c *NetconfClient) readMessage() ([]byte, error) {
	var buf bytes.Buffer
	marker := []byte(netconfEOM)
	tmp := make([]byte, 4096)

	for {
		n, err := c.stdout.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			if bytes.Contains(buf.Bytes(), marker) {
				break
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}

	// Strip the EOM marker and any surrounding whitespace
	raw := bytes.TrimSpace(bytes.Replace(buf.Bytes(), marker, nil, 1))
	return raw, nil
}

// ParseNetconfInterfaces extracts interface names from a NETCONF data blob.
// It handles common YANG models: ietf-interfaces and Cisco/Junos variants.
func ParseNetconfInterfaces(data []byte) []InterfaceDetail {
	if len(data) == 0 {
		return nil
	}

	type ifEntry struct {
		Name        string `xml:"name"`
		Description string `xml:"description"`
		AdminStatus string `xml:"admin-status"`
		OperStatus  string `xml:"oper-status"`
	}
	type interfaces struct {
		Entries []ifEntry `xml:"interface"`
	}

	// Try ietf-interfaces model
	type dataWrapper struct {
		Interfaces interfaces `xml:"interfaces"`
	}
	var wrapper dataWrapper
	if err := xml.Unmarshal(data, &wrapper); err == nil && len(wrapper.Interfaces.Entries) > 0 {
		out := make([]InterfaceDetail, 0, len(wrapper.Interfaces.Entries))
		for _, e := range wrapper.Interfaces.Entries {
			detail := InterfaceDetail{
				Name:        e.Name,
				Description: e.Description,
				AdminUp:     strings.EqualFold(e.AdminStatus, "up"),
				OperUp:      strings.EqualFold(e.OperStatus, "up"),
			}
			out = append(out, detail)
		}
		return out
	}

	// Fallback: scan for <name> tags within <interface> elements
	out := parseNetconfInterfacesRaw(data)
	return out
}

// parseNetconfInterfacesRaw is a best-effort raw XML scan for interface names.
func parseNetconfInterfacesRaw(data []byte) []InterfaceDetail {
	type xmlIface struct {
		Name string `xml:"name"`
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []InterfaceDetail
	var inInterface bool
	var current xmlIface

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if name == "interface" {
				inInterface = true
				current = xmlIface{}
			} else if inInterface && name == "name" {
				var val string
				if err2 := dec.DecodeElement(&val, &t); err2 == nil {
					current.Name = val
				}
			}
		case xml.EndElement:
			if strings.ToLower(t.Name.Local) == "interface" && inInterface {
				if current.Name != "" {
					out = append(out, InterfaceDetail{Name: current.Name})
				}
				inInterface = false
			}
		}
	}
	return out
}

// ParseNetconfFirmware extracts the firmware/version string from NETCONF data.
func ParseNetconfFirmware(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	// Look for common version elements
	for _, tag := range []string{"os-version", "version", "software-version", "firmware-version"} {
		if v := extractXMLText(data, tag); v != "" {
			return v
		}
	}
	return ""
}

// extractXMLText finds the first text content of a named element.
func extractXMLText(data []byte, tagName string) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && strings.EqualFold(se.Name.Local, tagName) {
			var val string
			if err2 := dec.DecodeElement(&val, &se); err2 == nil && val != "" {
				return strings.TrimSpace(val)
			}
		}
	}
	return ""
}
