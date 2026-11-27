package apt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/MiravaOrg/mirava-core/internal/core"
	"github.com/MiravaOrg/mirava-core/internal/httpx"
)

// releaseMeta is the parsed subset of a dists/<suite>/InRelease (or Release)
// file that the backend needs.
type releaseMeta struct {
	Suite         string
	Codename      string
	Components    []string
	Architectures []string
	Origin        string
	// Checksums maps index paths (relative to dists/<suite>/) to their
	// SHA256, e.g. "main/binary-amd64/Packages.gz" -> "abc...".
	Checksums map[string]string
}

// fetchRelease fetches and parses InRelease, falling back to Release.
func (s *Service) fetchRelease(ctx context.Context, mirror, suite string) (*releaseMeta, error) {
	base, err := httpx.JoinURL(mirror, "dists", suite)
	if err != nil {
		return nil, err
	}
	for _, file := range []string{"InRelease", "Release"} {
		u := base + "/" + file
		req, rerr := httpx.NewRequest(ctx, "GET", u)
		if rerr != nil {
			continue
		}
		resp, derr := s.client().Do(req)
		if derr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode != 200 {
			_ = resp.Body.Close()
			continue
		}
		body, rerr2 := httpx.ReadAll(resp)
		if rerr2 != nil {
			continue
		}
		meta := parseRelease(string(body))
		if len(meta.Components) == 0 || len(meta.Checksums) == 0 {
			continue
		}
		meta.Suite = suite
		return &meta, nil
	}
	return nil, fmt.Errorf("no usable InRelease/Release for suite %q", suite)
}

// parseRelease extracts the fields mirava needs from a Release/InRelease
// document. It reads the plain-text header and the SHA256 checksum section
// (InRelease is clearsigned; checksums live in the signed payload, so
// parsing them straight from the file is correct).
func parseRelease(body string) releaseMeta {
	meta := releaseMeta{Checksums: map[string]string{}}
	inSHA := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "SHA256:"):
			inSHA = true
		case inSHA && strings.HasPrefix(line, " "):
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) == 3 {
				meta.Checksums[fields[2]] = fields[0]
			}
		case inSHA && line == "":
			inSHA = false
		case strings.HasPrefix(line, "Codename:"):
			meta.Codename = strings.TrimSpace(strings.TrimPrefix(line, "Codename:"))
		case strings.HasPrefix(line, "Suite:"):
			meta.Suite = strings.TrimSpace(strings.TrimPrefix(line, "Suite:"))
		case strings.HasPrefix(line, "Origin:"):
			meta.Origin = strings.TrimSpace(strings.TrimPrefix(line, "Origin:"))
		case strings.HasPrefix(line, "Components:"):
			meta.Components = strings.Fields(strings.TrimPrefix(line, "Components:"))
		case strings.HasPrefix(line, "Architectures:"):
			meta.Architectures = strings.Fields(strings.TrimPrefix(line, "Architectures:"))
		}
	}
	return meta
}

// suiteHints are bootstrap suite names probed when discovering what a
// mirror actually hosts. Discovery reads Codename/Suite from the Release
// files themselves, so these are only candidate URLs, not a source of truth.
var suiteHints = map[Distro][]string{
	DistroDebian: {"trixie", "bookworm", "stable", "testing", "bullseye"},
	DistroUbuntu: {"noble", "jammy", "questing", "plucky"},
	DistroAny:    {"noble", "trixie", "stable", "jammy", "bookworm", "questing"},
}

// codenameRank prefers newer codenames. Rank is decoded from the parsed
// Release data when possible; this list only breaks ties among hints.
var codenameRank = map[string]int{
	// Debian
	"trixie": 60, "bookworm": 50, "bullseye": 40, "buster": 30,
	// Ubuntu
	"questing": 60, "plucky": 55, "noble": 50, "jammy": 40, "focal": 30, "bionic": 20,
	"testing": 58, "sid": 59, "unstable": 59,
}

// discoverSuites probes candidate suites in parallel and returns parsed
// metadata for the ones the mirror actually hosts, best codename first.
func (s *Service) discoverSuites(ctx context.Context, mirror string) ([]*releaseMeta, error) {
	hints := suiteHints[s.distro]
	if hints == nil {
		hints = suiteHints[DistroAny]
	}

	type result struct {
		idx  int
		meta *releaseMeta
	}
	results := make([]result, 0, len(hints))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)

	for i, suite := range hints {
		wg.Add(1)
		go func(i int, suite string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			meta, err := s.fetchRelease(ctx, mirror, suite)
			if err != nil {
				return
			}
			mu.Lock()
			results = append(results, result{idx: i, meta: meta})
			mu.Unlock()
		}(i, suite)
	}
	wg.Wait()

	if len(results) == 0 {
		return nil, &core.RequestError{URL: mirror, Err: fmt.Errorf("no apt suite found (tried %v)", hints)}
	}

	sort.Slice(results, func(a, b int) bool {
		ra, rb := results[a], results[b]
		rka, rkb := codenameRank[ra.meta.Codename], codenameRank[rb.meta.Codename]
		if rka != rkb {
			return rka > rkb
		}
		return ra.idx < rb.idx
	})
	metas := make([]*releaseMeta, len(results))
	for i, r := range results {
		metas[i] = r.meta
	}
	return metas, nil
}

// indexEntry describes one Packages index found in a release.
type indexEntry struct {
	suite     string
	codename  string
	component string
	arch      string
	relPath   string // path relative to dists/<suite>/
	sha256    string
	size      int64
}

// indexesFromMeta lists binary Packages indexes for a release, preferring
// the smallest format available (.gz over raw).
func indexesFromMeta(meta *releaseMeta, wantArch string) []indexEntry {
	var entries []indexEntry
	archs := meta.Architectures
	if wantArch != "" {
		for _, a := range archs {
			if a == wantArch {
				archs = []string{a}
				break
			}
		}
	}
	if len(archs) == 0 {
		archs = []string{"amd64"}
	}
	for _, arch := range archs {
		for _, comp := range meta.Components {
			rel := comp + "/binary-" + arch + "/"
			sha, ok := lookupChecksum(meta, rel+"Packages.xz", rel+"Packages.gz")
			if !ok {
				continue
			}
			file := "Packages.gz"
			if _, hasXZ := meta.Checksums[rel+"Packages.xz"]; hasXZ && !hasGZ(meta, rel) {
				file = "Packages.xz"
			}
			entries = append(entries, indexEntry{
				suite: meta.Suite, codename: meta.Codename,
				component: comp, arch: arch,
				relPath: rel + file, sha256: sha,
			})
		}
	}
	return entries
}

func hasGZ(meta *releaseMeta, rel string) bool {
	_, ok := meta.Checksums[rel+"Packages.gz"]
	return ok
}

// lookupChecksum returns the SHA256 for the first existing variant.
func lookupChecksum(meta *releaseMeta, variants ...string) (string, bool) {
	for _, v := range variants {
		if sha, ok := meta.Checksums[v]; ok {
			return sha, true
		}
	}
	return "", false
}

// hashBlob is the content-addressed cache key for an index file.
func hashBlob(sha256hex string) string {
	return "apt:blob:" + sha256hex
}

// parsedKey is the cache key for the parsed index map of a blob.
func parsedKey(sha256hex string) string {
	return "apt:parsed:" + sha256hex
}

// resultKey caches a final package lookup result per mirror+name.
func resultKey(mirror, name string) string {
	sum := sha256.Sum256([]byte(mirror + "\x00" + name))
	return "apt:res:" + hex.EncodeToString(sum[:8])
}
