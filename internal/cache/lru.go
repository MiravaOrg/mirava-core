package cache

import (
	"container/list"
	"sync"
)

// LRU is a bounded least-recently-used in-memory store. Entries are evicted
// when either the entry count or total byte size exceeds the limits.
type LRU struct {
	maxEntries int
	maxBytes   int64

	mu    sync.Mutex
	ll    *list.List // of *lruItem, front = most recent
	items map[string]*list.Element
	size  int64
}

type lruItem struct {
	key   string
	entry Entry
}

// NewLRU builds an in-memory store. Non-positive maxEntries defaults to
// 4096; non-positive maxBytes defaults to 256 MiB.
func NewLRU(maxEntries int, maxBytes int64) *LRU {
	if maxEntries <= 0 {
		maxEntries = 4096
	}
	if maxBytes <= 0 {
		maxBytes = 256 << 20
	}
	return &LRU{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		ll:         list.New(),
		items:      make(map[string]*list.Element, maxEntries),
	}
}

func (c *LRU) Get(key string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return Entry{}, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*lruItem).entry, true
}

func (c *LRU) Put(key string, e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		it := el.Value.(*lruItem)
		c.size += int64(len(e.Value)) - int64(len(it.entry.Value))
		it.entry = e
		c.ll.MoveToFront(el)
	} else {
		c.items[key] = c.ll.PushFront(&lruItem{key: key, entry: e})
		c.size += int64(len(e.Value))
	}
	c.evict()
}

func (c *LRU) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.removeElement(el)
	}
}

func (c *LRU) removeElement(el *list.Element) {
	it := el.Value.(*lruItem)
	c.size -= int64(len(it.entry.Value))
	c.ll.Remove(el)
	delete(c.items, it.key)
}

func (c *LRU) evict() {
	for c.ll.Len() > c.maxEntries || (c.size > c.maxBytes && c.ll.Len() > 1) {
		c.removeElement(c.ll.Back())
	}
}
