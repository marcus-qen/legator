package signing

import (
	"crypto/rand"
	"testing"
)

type testPayload struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	RequestID string   `json:"request_id"`
}

func TestSignAndVerify(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	s := NewSigner(key)
	p := testPayload{Command: "echo", Args: []string{"hello"}, RequestID: "r1"}
	sig, err := s.Sign("r1", p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Verify("r1", p, sig); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestRejectsTampered(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	s := NewSigner(key)
	p := testPayload{Command: "echo", RequestID: "r2"}
	sig, _ := s.Sign("r2", p)
	tampered := testPayload{Command: "rm", RequestID: "r2"}
	if err := s.Verify("r2", tampered, sig); err == nil {
		t.Fatal("should reject tampered payload")
	}
}

func TestRejectsWrongRequestID(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	s := NewSigner(key)
	p := testPayload{Command: "ls", RequestID: "r3"}
	sig, _ := s.Sign("r3", p)
	if err := s.Verify("r999", p, sig); err == nil {
		t.Fatal("should reject wrong request ID")
	}
}

func TestRejectsWrongKey(t *testing.T) {
	k1 := make([]byte, 32)
	k2 := make([]byte, 32)
	rand.Read(k1)
	rand.Read(k2)
	s1, s2 := NewSigner(k1), NewSigner(k2)
	p := testPayload{Command: "hostname", RequestID: "r4"}
	sig, _ := s1.Sign("r4", p)
	if err := s2.Verify("r4", p, sig); err == nil {
		t.Fatal("should reject wrong key")
	}
}

func TestDeriveProbeKey(t *testing.T) {
	master := make([]byte, 32)
	rand.Read(master)
	k1 := DeriveProbeKey(master, "probe-001")
	k2 := DeriveProbeKey(master, "probe-002")
	k1a := DeriveProbeKey(master, "probe-001")
	if string(k1) == string(k2) {
		t.Fatal("different IDs should give different keys")
	}
	if string(k1) != string(k1a) {
		t.Fatal("same ID should give same key")
	}
	if len(k1) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(k1))
	}
}

func TestSignDeterministic(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	s := NewSigner(key)
	p := testPayload{Command: "uptime", RequestID: "r6"}
	s1, _ := s.Sign("r6", p)
	s2, _ := s.Sign("r6", p)
	if s1 != s2 {
		t.Fatal("same input should produce same signature")
	}
}

func TestNilPayload(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	s := NewSigner(key)
	sig, err := s.Sign("r7", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Verify("r7", nil, sig); err != nil {
		t.Fatalf("nil verify failed: %v", err)
	}
}
