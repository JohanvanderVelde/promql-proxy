package cache

import (
	"context"
	"sync"
	"time"
)

type entry struct {
	namespaces []string
	expiresAt  time.Time
}

// NamespaceCache provides a thread-safe TTL cache for user-to-namespace mappings.
type NamespaceCache struct {
	mu      sync.RWMutex
	entries map[string]entry
	ttl     time.Duration
}

// New creates a new NamespaceCache with the given TTL and starts a background
// goroutine that periodically removes expired entries. The goroutine stops
// when the provided context is cancelled.
func New(ctx context.Context, ttl time.Duration) *NamespaceCache {
	c := &NamespaceCache{
		entries: make(map[string]entry),
		ttl:     ttl,
	}
	go c.cleanup(ctx)
	return c
}

// Get returns the cached namespaces for a user. Returns nil, false if the entry
// is missing or expired.
func (c *NamespaceCache) Get(user string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[user]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	result := make([]string, len(e.namespaces))
	copy(result, e.namespaces)
	return result, true
}

// Set stores the namespace list for a user with the configured TTL.
func (c *NamespaceCache) Set(user string, namespaces []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stored := make([]string, len(namespaces))
	copy(stored, namespaces)
	c.entries[user] = entry{
		namespaces: stored,
		expiresAt:  time.Now().Add(c.ttl),
	}
}

// Invalidate removes a user's cached namespaces.
func (c *NamespaceCache) Invalidate(user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, user)
}

func (c *NamespaceCache) cleanup(ctx context.Context) {
	ticker := time.NewTicker(c.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			now := time.Now()
			for k, v := range c.entries {
				if now.After(v.expiresAt) {
					delete(c.entries, k)
				}
			}
			c.mu.Unlock()
		}
	}
}
