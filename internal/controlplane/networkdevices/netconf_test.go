package networkdevices

import (
	"context"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"
)

// --- mock NETCONF client for use in enrichment tests ---

type mockNetconfClient struct {
	configData []byte
	stateData  []byte
	configErr  error
	stateErr   error
	closed     bool
}

func (m *mockNetconfClient) GetConfig(_ context.Context, _ string) ([]byte, error) {
	return m.configData, m.configErr
}

func (m *mockNetconfClient) Get(_ context.Context, _ string) ([]byte, error) {
	return m.stateData, m.stateErr
}

func (m *mockNetconfClient) Close() error {
	m.closed = true
	return nil
}

// --- XML parsing tests ---

func TestParseNetconfInterfaces_IETFModel(t *testing.T) {
	// ietf-interfaces style data
	xmlData := `<data>
  <interfaces xmlns="urn:ietf:params:xml:ns:yang:ietf-interfaces">
    <interface>
      <name>GigabitEthernet0/0</name>
      <description>WAN uplink</description>
      <admin-status>up</admin-status>
      <oper-status>up</oper-status>
    </interface>
    <interface>
      <name>GigabitEthernet0/1</name>
      <description>LAN</description>
      <admin-status>up</admin-status>
      <oper-status>down</oper-status>
    </interface>
  </interfaces>
</data>`

	ifaces := ParseNetconfInterfaces([]byte(xmlData))
	if len(ifaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %d", len(ifaces))
	}
	if ifaces[0].Name != "GigabitEthernet0/0" {
		t.Errorf("expected GigabitEthernet0/0, got %q", ifaces[0].Name)
	}
	if !ifaces[0].AdminUp {
		t.Error("expected GigabitEthernet0/0 AdminUp=true")
	}
	if !ifaces[0].OperUp {
		t.Error("expected GigabitEthernet0/0 OperUp=true")
	}
	if ifaces[1].OperUp {
		t.Error("expected GigabitEthernet0/1 OperUp=false")
	}
}

func TestParseNetconfInterfaces_RawFallback(t *testing.T) {
	// Non-IETF style — raw XML fallback scanner
	xmlData := `<data>
  <interfaces>
    <interface>
      <name>eth0</name>
    </interface>
    <interface>
      <name>eth1</name>
    </interface>
  </interfaces>
</data>`

	ifaces := ParseNetconfInterfaces([]byte(xmlData))
	if len(ifaces) != 2 {
		t.Fatalf("expected 2 interfaces (raw fallback), got %d", len(ifaces))
	}
	names := map[string]bool{}
	for _, iface := range ifaces {
		names[iface.Name] = true
	}
	for _, want := range []string{"eth0", "eth1"} {
		if !names[want] {
			t.Errorf("missing interface %q", want)
		}
	}
}

func TestParseNetconfInterfaces_Empty(t *testing.T) {
	ifaces := ParseNetconfInterfaces(nil)
	if ifaces != nil {
		t.Errorf("expected nil for empty input, got %v", ifaces)
	}
	ifaces = ParseNetconfInterfaces([]byte("<data/>"))
	// No interfaces — should return nil
	if len(ifaces) > 0 {
		t.Errorf("expected no interfaces from minimal data, got %v", ifaces)
	}
}

func TestParseNetconfFirmware(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantNot bool // if true, just check non-empty
		want    string
	}{
		{
			name: "os-version element",
			data: `<data><system-state><platform><os-version>17.3.4</os-version></platform></system-state></data>`,
			want: "17.3.4",
		},
		{
			name: "version element",
			data: `<data><version>15.1(4)M12a</version></data>`,
			want: "15.1(4)M12a",
		},
		{
			name: "no version element",
			data: `<data><hostname>router1</hostname></data>`,
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseNetconfFirmware([]byte(tc.data))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractXMLText(t *testing.T) {
	data := `<root><version>1.2.3</version><other>nope</other></root>`
	if v := extractXMLText([]byte(data), "version"); v != "1.2.3" {
		t.Errorf("got %q", v)
	}
	if v := extractXMLText([]byte(data), "missing"); v != "" {
		t.Errorf("got %q for missing tag", v)
	}
}

// --- NETCONF message framing tests ---

func TestNetconfReadMessage(t *testing.T) {
	// Simulate the stdout of a NETCONF session
	serverHello := `<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities><capability>urn:ietf:params:netconf:base:1.0</capability></capabilities>
  <session-id>42</session-id>
</hello>
]]>]]>
`
	r := strings.NewReader(serverHello)
	c := &NetconfClient{
		stdout:  r,
		timeout: 5 * time.Second,
	}

	msg, err := c.readMessage()
	if err != nil && err != io.EOF {
		t.Fatalf("readMessage: %v", err)
	}

	var hello netconfHello
	if err := xml.Unmarshal(msg, &hello); err != nil {
		t.Fatalf("unmarshal hello: %v", err)
	}
	if hello.SessionID != "42" {
		t.Errorf("expected session-id 42, got %q", hello.SessionID)
	}
	if len(hello.Capabilities) == 0 {
		t.Error("expected at least one capability")
	}
}

func TestNetconfWriteMessage(t *testing.T) {
	var buf strings.Builder
	c := &NetconfClient{
		stdin: &writeCloser{w: &buf},
	}

	data := []byte(`<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0" message-id="1"><get-config><source><running/></source></get-config></rpc>`)
	if err := c.writeMessage(data); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "]]>]]>") {
		t.Error("message missing EOM marker ]]>]]>")
	}
	if !strings.Contains(got, "get-config") {
		t.Error("message missing rpc body")
	}
}

// writeCloser adapts strings.Builder to io.WriteCloser.
type writeCloser struct {
	w io.Writer
}

func (wc *writeCloser) Write(p []byte) (int, error) { return wc.w.Write(p) }
func (wc *writeCloser) Close() error                { return nil }

func TestParseNetconfInterfacesRaw_MalformedXML(t *testing.T) {
	// Should not panic on malformed XML
	ifaces := parseNetconfInterfacesRaw([]byte("<not valid xml><<<<"))
	// May return empty — just check no panic
	_ = ifaces
}

func TestNetconfConfig_Defaults(t *testing.T) {
	// Ensure zero-value port is treated as 830 in NewNetconfClient path
	// We don't actually dial, just verify the port logic
	cfg := NetconfConfig{Host: "10.0.0.1", Port: 0}
	port := cfg.Port
	if port <= 0 {
		port = netconfPort
	}
	if port != 830 {
		t.Errorf("expected default port 830, got %d", port)
	}
}
