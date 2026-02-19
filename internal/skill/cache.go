/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package skill

import (
	"sync"
	"time"
)

// SourceInfo records where a skill was loaded from.
type SourceInfo struct {
	// Type is the source type (bundled, configmap, git).
	Type string

	// URL is the source URL (for git sources).
	URL string

	// Ref is the git ref (tag, branch, commit).
	Ref string

	// Path is the subdirectory within the source.
	Path string

	// ConfigMap is the ConfigMap name (for configmap sources).
	ConfigMap string
}

// CacheEntry holds a cached skill and metadata.
type CacheEntry struct {
	Skill    *Skill
	LoadedAt time.Time
}

// Cache is a thread-safe in-memory skill cache.
// Skills are cached by a key (typically source string or git URL+ref).
// Cache entries have a configurable TTL after which they're considered stale.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*CacheEntry
	ttl     time.Duration
}

// NewCache creates a skill cache with the given TTL.
// A TTL of 0 means entries never expire (useful for git refs that are immutable like tags/SHAs).
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		entries: make(map[string]*CacheEntry),
		ttl:     ttl,
	}
}

// Get retrieves a skill from the cache.
// Returns the skill and true if found and not expired, nil and false otherwise.
func (c *Cache) Get(key string) (*Skill, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}

	// Check TTL (0 = never expires)
	if c.ttl > 0 && time.Since(entry.LoadedAt) > c.ttl {
		return nil, false
	}

	return entry.Skill, true
}

// Put stores a skill in the cache.
func (c *Cache) Put(key string, skill *Skill) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[key] = &CacheEntry{
		Skill:    skill,
		LoadedAt: time.Now(),
	}
}

// Invalidate removes a specific entry from the cache.
func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// InvalidateAll clears the entire cache.
func (c *Cache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*CacheEntry)
}

// Size returns the number of entries in the cache.
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Keys returns all cache keys.
func (c *Cache) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]string, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	return keys
}

// CleanExpired removes all expired entries.
// Returns the number of entries removed.
func (c *Cache) CleanExpired() int {
	if c.ttl == 0 {
		return 0 // Nothing expires
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cleaned := 0
	for key, entry := range c.entries {
		if time.Since(entry.LoadedAt) > c.ttl {
			delete(c.entries, key)
			cleaned++
		}
	}
	return cleaned
}
