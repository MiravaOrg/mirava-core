package apt

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"crypto/sha256"
	"encoding/hex"
	"github.com/MiravaOrg/mirava-core/internal/cache"
	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/httpx"
	"golang.org/x/sync/singleflight"
)

// packageCandidate is a found package inside one index.
type packageCandidate struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Filename  string `json:"filename,omitempty"`
	Size      int64  `json:"size,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Suite     string `json:"suite"`
	Codename  string `json:"codename,omitempty"`
	Component string `json:"component"`
	Arch      string `json:"arch"`
	IndexPath string `json:"index_path"`
}

// indexDB manages suite discovery, blob fetching and parsed-index caching.
type indexDB struct {
	store cache.Store
	fg    singleflight.Group
}

func newIndexDB(store cache.Store) *indexDB {
	if store == nil {
		store = cache.NewLRU(0, 0)
	}
	return &indexDB{store: store}
}

// lookup resolves a package. Order:
//  1. cached final result (per mirror+name, TTL-bounded)
//  2. discover suites (per-mirror cached Release metas)
//  3. scan candidate indexes in waves, in parallel
//  4. cache the final result
func (s *Service) lookup(ctx context.Context, mirror, name, version string, opts *QueryOptions) (*packageCandidate, error) {
	db := s.idx
	f := &fetcher{db: db, client: s.client(), mirror: mirror}

	// 1. Cached final result — only for unfiltered lookups.
	if opts != nil && opts.Suite == "" && opts.Component == "" && opts.Arch == "" && version == "" {
		if e, ok := db.store.Get(resultKey(mirror, name)); ok && e.Fresh(time.Now()) {
			var c packageCandidate
			if json.Unmarshal(e.Value, &c) == nil && c.Version != "" {
				return &c, nil
			}
		}
	}

	// 2. Discover suites.
	metas, err := db.suitesCached(ctx, s, mirror)
	if err != nil {
		return nil, err
	}

	// 3. Build candidate index list.
	type idxPlan struct {
		entry indexEntry
		wave  int
	}
	var plans []idxPlan
	for wave, meta := range metas { // metas are sorted best-codename-first
		entries := indexesFromMeta(meta, opts.arch())
		for _, e := range entries {
			if opts.Component != "" && e.component != opts.Component {
				continue
			}
			if opts.Suite != "" && e.suite != opts.Suite && e.codename != opts.Suite {
				continue
			}
			w := wave
			if opts.Component == "" {
				// Component waves within each suite: main before universe.
				w = wave*10 + componentWave(e.component)
			}
			plans = append(plans, idxPlan{entry: e, wave: w})
		}
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].wave < plans[j].wave })

	if len(plans) == 0 {
		return nil, nil
	}

	// 4. Scan in parallel; first hit wins.
	entries := make([]indexEntry, len(plans))
	for i, p := range plans {
		entries[i] = p.entry
	}
	found := scanIndexes(ctx, f, entries, name)
	if found == nil {
		return nil, nil
	}

	cand := &packageCandidate{
		Name:      name,
		Version:   found.Version,
		Filename:  found.Filename,
		Size:      found.Size,
		SHA256:    found.SHA256,
		Suite:     found.Suite,
		Component: found.Component,
		Arch:      found.Arch,
		IndexPath: found.IndexPath,
	}

	// 5. Cache final result for unfiltered lookups.
	if opts.Suite == "" && opts.Component == "" && opts.Arch == "" && version == "" {
		if data, jerr := json.Marshal(cand); jerr == nil {
			db.store.Put(resultKey(mirror, name), cache.Entry{
				Value:     data,
				ExpiresAt: time.Now().Add(30 * time.Minute),
			})
		}
	}
	return cand, nil
}

func (o *QueryOptions) arch() string {
	if o == nil || o.Arch == "" {
		return "amd64"
	}
	return o.Arch
}

func componentWave(component string) int {
	switch component {
	case "main":
		return 0
	case "contrib", "universe":
		return 1
	default:
		return 2
	}
}

// suitesCached discovers (and caches per-mirror) release metadata.
func (db *indexDB) suitesCached(ctx context.Context, s *Service, mirror string) ([]*releaseMeta, error) {
	key := "apt:suites:" + string(s.distro) + ":" + mirror
	if e, ok := db.store.Get(key); ok && e.Fresh(time.Now()) {
		var metas []*releaseMeta
		if json.Unmarshal(e.Value, &metas) == nil && len(metas) > 0 {
			return metas, nil
		}
	}

	type sfResult struct {
		metas []*releaseMeta
		err   error
	}
	v, _, _ := db.fg.Do(key, func() (any, error) {
		metas, err := s.discoverSuites(ctx, mirror)
		return sfResult{metas: metas, err: err}, nil //nolint:nilerr // error carried in result
	})
	res := v.(sfResult)
	if res.err != nil {
		return nil, res.err
	}

	// Cache the discovery for a short window.
	if data, err := json.Marshal(res.metas); err == nil {
		db.store.Put(key, cache.Entry{Value: data, ExpiresAt: time.Now().Add(10 * time.Minute)})
	}
	return res.metas, nil
}

// fetcher owns HTTP access for one lookup: blob cache + singleflight.
type fetcher struct {
	db     *indexDB
	client *http.Client
	mirror string
}

// fetchBlob returns the (decompressed) bytes of an index file, from the
// content-addressed cache or downloaded and SHA256-verified.
func (f *fetcher) fetchBlob(ctx context.Context, mirror string, e indexEntry) ([]byte, bool, error) {
	key := hashBlob(e.sha256)
	if ent, ok := f.db.store.Get(key); ok && ent.Fresh(time.Now()) {
		return ent.Value, false, nil
	}

	data, err, _ := f.db.fg.Do(key, func() (any, error) {
		// Re-check inside the flight.
		if ent, ok := f.db.store.Get(key); ok && ent.Fresh(time.Now()) {
			return ent.Value, nil
		}
		u, err := httpx.JoinURL(mirror, "dists", e.suite, e.relPath)
		if err != nil {
			return nil, err
		}
		req, err := httpx.NewRequest(ctx, http.MethodGet, u)
		if err != nil {
			return nil, err
		}
		resp, err := f.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, &core.StatusError{URL: u, StatusCode: resp.StatusCode}
		}
		body, err := httpx.ReadAll(resp)
		if err != nil {
			return nil, err
		}
		// Verify SHA256 — a mismatch means the mirror is mid-sync or broken.
		sum := sha256Hex(body)
		if sum != e.sha256 {
			return nil, fmt.Errorf("sha256 mismatch for %s: got %s want %s", u, sum, e.sha256)
		}
		f.db.store.Put(key, cache.Entry{Value: body, ExpiresAt: time.Now().Add(6 * time.Hour)})
		return body, nil
	})
	if err != nil {
		return nil, false, err
	}
	return data.([]byte), true, nil
}

// fetchParsed returns the parsed package list for an index entry, parsing
// the blob once per blob hash and caching the result.
func (f *fetcher) fetchParsed(ctx context.Context, e indexEntry) (map[string]*packageCandidate, error) {
	pk := parsedKey(e.sha256)
	if ent, ok := f.db.store.Get(pk); ok && ent.Fresh(time.Now()) {
		return decodeParsed(ent.Value)
	}

	data, err, _ := f.db.fg.Do(pk, func() (any, error) {
		if ent, ok := f.db.store.Get(pk); ok && ent.Fresh(time.Now()) {
			return ent.Value, nil
		}
		blob, _, err := f.fetchBlob(ctx, f.mirror, e)
		if err != nil {
			return nil, err
		}
		raw := blob
		if strings.HasSuffix(e.relPath, ".gz") {
			zr, gerr := gzip.NewReader(bytes.NewReader(blob))
			if gerr != nil {
				return nil, &core.ParseError{URL: e.relPath, Kind: "gzip", Err: gerr}
			}
			defer func() { _ = zr.Close() }()
			raw, err = io.ReadAll(zr)
			if err != nil {
				return nil, &core.ParseError{URL: e.relPath, Kind: "gzip", Err: err}
			}
		} else if strings.HasSuffix(e.relPath, ".xz") {
			raw, err = xzDecompress(blob)
			if err != nil {
				return nil, &core.ParseError{URL: e.relPath, Kind: "xz", Err: err}
			}
		}
		cands := parsePackagesIndex(raw, e)
		out, jerr := json.Marshal(cands)
		if jerr != nil {
			return nil, jerr
		}
		f.db.store.Put(pk, cache.Entry{Value: out, ExpiresAt: time.Now().Add(24 * time.Hour)})
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return decodeParsed(data.([]byte))
}

func decodeParsed(data []byte) (map[string]*packageCandidate, error) {
	var cands []*packageCandidate
	if err := json.Unmarshal(data, &cands); err != nil {
		return nil, err
	}
	m := make(map[string]*packageCandidate, len(cands))
	for _, c := range cands {
		if prev, ok := m[c.Name]; !ok || debVersionGreater(c.Version, prev.Version) {
			m[c.Name] = c
		}
	}
	return m, nil
}

// parsePackagesIndex scans a Packages file into candidates. Only the fields
// mirava needs are kept.
func parsePackagesIndex(raw []byte, e indexEntry) []*packageCandidate {
	var (
		out                                         []*packageCandidate
		curName, curVersion, curFilename, curSHA256 string
		curSize                                     int64
	)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	flush := func() {
		if curName == "" || curVersion == "" {
			return
		}
		out = append(out, &packageCandidate{
			Name: curName, Version: curVersion, Filename: curFilename,
			Size: curSize, SHA256: curSHA256,
			Suite: e.suite, Codename: e.codename, Component: e.component, Arch: e.arch,
			IndexPath: e.relPath,
		})
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			curName, curVersion, curFilename, curSHA256, curSize = "", "", "", "", 0
			continue
		}
		key, val, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch key {
		case "Package":
			flush()
			curName, curVersion, curFilename, curSHA256, curSize = val, "", "", "", 0
		case "Version":
			curVersion = val
		case "Filename":
			curFilename = val
		case "Size":
			fmt.Sscan(val, &curSize)
		case "SHA256":
			curSHA256 = val
		}
	}
	flush()
	return out
}

// scanIndexes looks up name across index entries in parallel; the first
// candidate found (in entry order priority) wins. Entries earlier in the
// list (better wave) are attempted first via a semaphore-ordered pool.
func scanIndexes(ctx context.Context, f *fetcher, entries []indexEntry, name string) *packageCandidate {
	if len(entries) == 0 {
		return nil
	}
	if len(entries) == 1 {
		m, err := f.fetchParsed(ctx, entries[0])
		if err != nil || m == nil {
			return nil
		}
		return m[name]
	}

	type result struct {
		idx  int
		cand *packageCandidate
	}
	resultCh := make(chan result, len(entries))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup

	for i, e := range entries {
		wg.Add(1)
		go func(i int, e indexEntry) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			m, err := f.fetchParsed(ctx, e)
			if err != nil || m == nil {
				return
			}
			if c, ok := m[name]; ok {
				resultCh <- result{idx: i, cand: c}
			}
		}(i, e)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Prefer the earliest (best-wave) entry that found the package.
	var best *result
	for r := range resultCh {
		if best == nil || r.idx < best.idx {
			b := r
			best = &b
		}
	}
	if best == nil {
		return nil
	}
	return best.cand
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
