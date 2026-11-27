package cache

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLRUEviction(t *testing.T) {
	c := NewLRU(3, 0)
	for i := 0; i < 5; i++ {
		c.Put(string(rune('a'+i)), Entry{Value: []byte{byte(i)}})
	}
	// a, b evicted by count.
	if _, ok := c.Get("a"); ok {
		t.Fatal("a should be evicted")
	}
	if _, ok := c.Get("e"); !ok {
		t.Fatal("e should be present")
	}
}

func TestLRUByteBudget(t *testing.T) {
	c := NewLRU(100, 10)
	big := bytes.Repeat([]byte{0}, 8)
	for i := 0; i < 5; i++ {
		c.Put(string(rune('a'+i)), Entry{Value: big})
	}
	if c.size > 10 {
		t.Fatalf("size %d exceeds budget 10", c.size)
	}
}

func TestDiskAtomicWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	d, err := OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	d.Put("k", Entry{Value: []byte("hello"), ExpiresAt: time.Now().Add(time.Minute)})
	e, ok := d.Get("k")
	if !ok || string(e.Value) != "hello" {
		t.Fatalf("got %v %v", e, ok)
	}
	// No temp leftovers.
	entries, _ := os.ReadDir(dir)
	for _, f := range entries {
		if f.IsDir() {
			continue
		}
		if bytes.HasPrefix([]byte(f.Name()), []byte(".tmp-")) {
			t.Fatalf("temp file left behind: %s", f.Name())
		}
	}
}

func TestDiskCorruptRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	d, _ := OpenDisk(dir)
	d.Put("k", Entry{Value: []byte("v")})
	// Corrupt every file.
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			os.WriteFile(p, []byte("{corrupt"), 0o644)
		}
		return nil
	})
	if _, ok := d.Get("k"); ok {
		t.Fatal("corrupt record should read as absent")
	}
}

func TestLayeredNoTTLExtension(t *testing.T) {
	fast := NewLRU(0, 0)
	slow := NewLRU(0, 0)
	l := NewLayered(fast, slow)

	exp := time.Now().Add(-time.Second) // already expired
	slow.Put("k", Entry{Value: []byte("v"), ExpiresAt: exp})

	e, ok := l.Get("k")
	if !ok {
		t.Fatal("expected present")
	}
	if !e.ExpiresAt.Equal(exp) {
		t.Fatalf("expiry changed on promotion: %v -> %v", exp, e.ExpiresAt)
	}
	// And the fast layer must hold the ORIGINAL expiry, not an extended one.
	fe, _ := fast.Get("k")
	if !fe.ExpiresAt.Equal(exp) {
		t.Fatalf("fast layer expiry extended: %v", fe.ExpiresAt)
	}
}

func TestSingleflight(t *testing.T) {
	f := &flight{}
	calls := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.Do("key", func() ([]byte, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				return make([]byte, 16), nil
			})
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestPrune(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	d, _ := OpenDisk(dir)
	d.Put("old", Entry{Value: []byte("v"), ExpiresAt: time.Now().Add(-time.Minute)})
	d.Put("new", Entry{Value: []byte("v"), ExpiresAt: time.Now().Add(time.Minute)})
	if removed := d.Prune(time.Now()); removed != 1 {
		t.Fatalf("prune removed %d, want 1", removed)
	}
	if _, ok := d.Get("new"); !ok {
		t.Fatal("new entry should survive prune")
	}
}

func TestRandomDataRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	d, _ := OpenDisk(dir)
	data := make([]byte, 4096)
	_, _ = rand.Read(data)
	d.Put("blob", Entry{Value: data})
	e, ok := d.Get("blob")
	if !ok || !bytes.Equal(e.Value, data) {
		t.Fatal("round trip failed")
	}
}
