package cache

import (
	"golang.org/x/sync/singleflight"
)

// flight deduplicates concurrent loads: the first caller performs the work,
// the rest receive the same result. Use it around any upstream fetch that
// populates the cache.
type flight struct {
	g singleflight.Group
}

type flightResult struct {
	value []byte
	err   error
}

// Do executes fn once for all concurrent callers with the same key.
func (f *flight) Do(key string, fn func() ([]byte, error)) ([]byte, error) {
	v, _, _ := f.g.Do(key, func() (any, error) {
		value, err := fn()
		return flightResult{value: value, err: err}, nil
	})
	res := v.(flightResult)
	return res.value, res.err
}

// Layered composes a fast store (memory) over a slower one (disk). Gets
// promote disk hits into the fast layer WITHOUT extending their original
// expiry — the disk entry's ExpiresAt is preserved exactly.
type Layered struct {
	fast Store
	slow Store
}

// NewLayered builds a two-tier store.
func NewLayered(fast, slow Store) *Layered {
	return &Layered{fast: fast, slow: slow}
}

func (l *Layered) Get(key string) (Entry, bool) {
	if e, ok := l.fast.Get(key); ok {
		return e, true
	}
	e, ok := l.slow.Get(key)
	if !ok {
		return Entry{}, false
	}
	// Promote without extending: keep the slow layer's exact expiry.
	l.fast.Put(key, Entry{Value: e.Value, ExpiresAt: e.ExpiresAt})
	return e, true
}

func (l *Layered) Put(key string, e Entry) {
	l.fast.Put(key, e)
	l.slow.Put(key, e)
}

func (l *Layered) Delete(key string) {
	l.fast.Delete(key)
	l.slow.Delete(key)
}
