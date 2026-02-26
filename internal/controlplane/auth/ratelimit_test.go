package auth

import (
	"testing"
	"time"
)

func TestRateLimiterAllow(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)

	if !rl.Allow("key1") {
		t.Fatal("first request should be allowed")
	}
	if !rl.Allow("key1") {
		t.Fatal("second request should be allowed")
	}
	if !rl.Allow("key1") {
		t.Fatal("third request should be allowed")
	}
	if rl.Allow("key1") {
		t.Fatal("fourth request should be denied")
	}
}

func TestRateLimiterDifferentKeys(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)

	rl.Allow("key1")
	rl.Allow("key1")

	// key1 exhausted
	if rl.Allow("key1") {
		t.Fatal("key1 should be exhausted")
	}

	// key2 should still work
	if !rl.Allow("key2") {
		t.Fatal("key2 should be allowed")
	}
}

func TestRateLimiterWindowReset(t *testing.T) {
	rl := NewRateLimiter(1, 50*time.Millisecond)

	if !rl.Allow("key1") {
		t.Fatal("first should be allowed")
	}
	if rl.Allow("key1") {
		t.Fatal("second should be denied")
	}

	time.Sleep(60 * time.Millisecond) // wait for window reset

	if !rl.Allow("key1") {
		t.Fatal("should be allowed after window reset")
	}
}

func TestRateLimiterRemaining(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)

	if rl.Remaining("key1") != 5 {
		t.Fatalf("expected 5, got %d", rl.Remaining("key1"))
	}

	rl.Allow("key1")
	rl.Allow("key1")

	if rl.Remaining("key1") != 3 {
		t.Fatalf("expected 3, got %d", rl.Remaining("key1"))
	}
}
