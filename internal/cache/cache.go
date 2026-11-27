// Package cache provides the storage layer behind mirava-core probes: a
// small interface implemented by a bounded in-memory LRU and an
// atomic-write disk layer, plus a singleflight guard so concurrent lookups
// share one upstream fetch.
package cache

import (
	"time"
)

// Entry is a stored value with its expiry. Zero ExpiresAt means no expiry.
type Entry struct {
	Value     []byte
	ExpiresAt time.Time
}

// Fresh reports whether the entry is usable at time now.
func (e Entry) Fresh(now time.Time) bool {
	return e.ExpiresAt.IsZero() || now.Before(e.ExpiresAt)
}

// Store is the cache contract. Implementations must be safe for concurrent
// use. mirava-core provides LRU (memory) and Disk; callers may supply their
// own (e.g. Redis-backed) via mirava.WithCache.
type Store interface {
	// Get returns the entry and whether it was present at all. Expiry is
	// checked by callers (so promotion never re-extends TTL).
	Get(key string) (Entry, bool)
	// Put stores the entry, replacing any previous value for key.
	Put(key string, e Entry)
	// Delete removes key if present.
	Delete(key string)
}

// compile-time guard for the default composite stores defined below.
var _ Store = (*LRU)(nil)
