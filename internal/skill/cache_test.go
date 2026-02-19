/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package skill

import (
	"testing"
	"time"
)

func TestCache_PutAndGet(t *testing.T) {
	cache := NewCache(0) // No TTL

	skill := &Skill{Name: "test-skill", Version: "1.0.0"}
	cache.Put("key1", skill)

	got, ok := cache.Get("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Name != "test-skill" {
		t.Errorf("Name = %q, want %q", got.Name, "test-skill")
	}
}

func TestCache_Miss(t *testing.T) {
	cache := NewCache(0)

	_, ok := cache.Get("nonexistent")
	if ok {
		t.Error("expected cache miss")
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	cache := NewCache(50 * time.Millisecond)

	skill := &Skill{Name: "expiring"}
	cache.Put("key1", skill)

	// Should hit immediately
	_, ok := cache.Get("key1")
	if !ok {
		t.Fatal("expected cache hit before TTL")
	}

	// Wait for expiry
	time.Sleep(60 * time.Millisecond)

	_, ok = cache.Get("key1")
	if ok {
		t.Error("expected cache miss after TTL")
	}
}

func TestCache_NoTTL(t *testing.T) {
	cache := NewCache(0) // 0 = never expires

	skill := &Skill{Name: "permanent"}
	cache.Put("key1", skill)

	// Should always hit
	_, ok := cache.Get("key1")
	if !ok {
		t.Fatal("expected cache hit with no TTL")
	}
}

func TestCache_Invalidate(t *testing.T) {
	cache := NewCache(0)
	cache.Put("key1", &Skill{Name: "a"})
	cache.Put("key2", &Skill{Name: "b"})

	cache.Invalidate("key1")

	_, ok := cache.Get("key1")
	if ok {
		t.Error("expected cache miss after invalidate")
	}

	_, ok = cache.Get("key2")
	if !ok {
		t.Error("key2 should still be cached")
	}
}

func TestCache_InvalidateAll(t *testing.T) {
	cache := NewCache(0)
	cache.Put("key1", &Skill{Name: "a"})
	cache.Put("key2", &Skill{Name: "b"})

	cache.InvalidateAll()

	if cache.Size() != 0 {
		t.Errorf("Size = %d, want 0", cache.Size())
	}
}

func TestCache_Size(t *testing.T) {
	cache := NewCache(0)
	if cache.Size() != 0 {
		t.Errorf("initial Size = %d, want 0", cache.Size())
	}

	cache.Put("key1", &Skill{Name: "a"})
	cache.Put("key2", &Skill{Name: "b"})

	if cache.Size() != 2 {
		t.Errorf("Size = %d, want 2", cache.Size())
	}
}

func TestCache_Keys(t *testing.T) {
	cache := NewCache(0)
	cache.Put("alpha", &Skill{Name: "a"})
	cache.Put("beta", &Skill{Name: "b"})

	keys := cache.Keys()
	if len(keys) != 2 {
		t.Errorf("Keys count = %d, want 2", len(keys))
	}

	// Check both keys present (order not guaranteed)
	found := map[string]bool{}
	for _, k := range keys {
		found[k] = true
	}
	if !found["alpha"] || !found["beta"] {
		t.Errorf("Keys = %v, want [alpha, beta]", keys)
	}
}

func TestCache_CleanExpired(t *testing.T) {
	cache := NewCache(50 * time.Millisecond)

	cache.Put("old", &Skill{Name: "old"})
	time.Sleep(60 * time.Millisecond)
	cache.Put("new", &Skill{Name: "new"})

	cleaned := cache.CleanExpired()
	if cleaned != 1 {
		t.Errorf("CleanExpired = %d, want 1", cleaned)
	}

	if cache.Size() != 1 {
		t.Errorf("Size after clean = %d, want 1", cache.Size())
	}

	_, ok := cache.Get("new")
	if !ok {
		t.Error("new entry should still exist")
	}
}

func TestCache_CleanExpiredNoTTL(t *testing.T) {
	cache := NewCache(0)
	cache.Put("key1", &Skill{Name: "a"})

	cleaned := cache.CleanExpired()
	if cleaned != 0 {
		t.Errorf("CleanExpired with no TTL = %d, want 0", cleaned)
	}
}

func TestCache_Overwrite(t *testing.T) {
	cache := NewCache(0)
	cache.Put("key1", &Skill{Name: "old"})
	cache.Put("key1", &Skill{Name: "new"})

	got, ok := cache.Get("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Name != "new" {
		t.Errorf("Name = %q, want %q (should be overwritten)", got.Name, "new")
	}
}
