package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Disk is a persistent store under a root directory. Files are written
// atomically (temp file + rename) so a crash — or a second process sharing
// the cache — never observes a torn value.
type Disk struct {
	root string
}

// diskRecord is the on-disk envelope. Storing ExpiresAt alongside the value
// keeps Get cheap and lets expiry be checked without a second read.
type diskRecord struct {
	Value     []byte    `json:"v"`
	ExpiresAt time.Time `json:"e"`
}

// OpenDisk opens (creating if needed) a disk store rooted at dir.
func OpenDisk(dir string) (*Disk, error) {
	if dir == "" {
		return nil, errors.New("cache: empty directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cache: create %s: %w", dir, err)
	}
	return &Disk{root: dir}, nil
}

// DefaultDir returns the OS cache location for mirava-core, e.g.
// ~/Library/Caches/mirava-core on macOS or ~/.cache/mirava-core on Linux.
func DefaultDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "mirava-core")
}

func fileKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (d *Disk) path(key string) string {
	k := fileKey(key)
	return filepath.Join(d.root, k[:2], k[2:4], k+".json")
}

func (d *Disk) Get(key string) (Entry, bool) {
	data, err := os.ReadFile(d.path(key))
	if err != nil {
		return Entry{}, false
	}
	var rec diskRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		// Corrupt record: treat as absent and remove it.
		_ = os.Remove(d.path(key))
		return Entry{}, false
	}
	return Entry{Value: rec.Value, ExpiresAt: rec.ExpiresAt}, true
}

func (d *Disk) Put(key string, e Entry) {
	data, err := json.Marshal(diskRecord{Value: e.Value, ExpiresAt: e.ExpiresAt})
	if err != nil {
		return
	}
	p := d.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	// Atomic write: temp file in the same directory, fsync-free rename.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return
	}
	_ = os.Rename(tmpName, p)
}

func (d *Disk) Delete(key string) {
	_ = os.Remove(d.path(key))
}

// Prune walks the store and removes expired records. It is best-effort and
// intended to run occasionally (e.g. on process start).
func (d *Disk) Prune(now time.Time) (removed int) {
	entries, err := os.ReadDir(d.root)
	if err != nil {
		return 0
	}
	var walk func(dir string)
	walk = func(dir string) {
		list, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range list {
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				walk(p)
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var rec diskRecord
			if json.Unmarshal(data, &rec) != nil || (!rec.ExpiresAt.IsZero() && now.After(rec.ExpiresAt)) {
				_ = os.Remove(p)
				removed++
			}
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			walk(filepath.Join(d.root, e.Name()))
		}
	}
	return removed
}
