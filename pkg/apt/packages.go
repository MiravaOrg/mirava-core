package apt

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	version "github.com/knqyf263/go-deb-version"

	"github.com/MiravaOrg/mirava-core/internal/constants"
)

// PackageLookupResult is returned by Mirror.LookupPackageVersion.
type PackageLookupResult struct {
	PackageName string
	Version     string
	Suite       string
	Component   string
	Arch        string
	IndexPath   string
	Filename    string
}

type PackageSearch struct {
	Suite     string
	Component string
	Arch      string
}

type aptIndexPath struct {
	Suite     string
	Component string
	Arch      string
	File      string
}

type aptPackageCandidate struct {
	Version   string
	Filename  string
	Suite     string
	Component string
	Arch      string
	IndexPath string
}

const (
	aptPreferredArch      = "amd64"
	aptPackagesFile       = "Packages.gz"
	aptIndexLookupWorkers = 10
)

var aptSearchComponentWaves = [][]string{
	{"main"},
	{"universe"},
	{"restricted", "multiverse"},
}

var aptFallbackSuites = []string{
	"noble", "noble-updates", "noble-security",
	"jammy", "jammy-updates", "jammy-security",
	"focal", "focal-updates", "focal-security",
	"bookworm", "bookworm-updates", "bookworm-security",
	"bullseye", "bullseye-updates", "bullseye-security",
	"trixie", "trixie-updates", "trixie-security",
}

var aptCodenamePriority = []string{
	"noble", "jammy", "focal",
	"bookworm", "bullseye", "trixie",
}

// LookupPackageVersion finds the latest package version on an APT mirror.
// Pass a non-nil search to limit which indexes are scanned (faster on live mirrors).
func (m *Mirror) LookupPackageVersion(
	repositoryURL,
	packageName string,
	search *PackageSearch,
) (*PackageLookupResult, error) {
	repositoryURL = strings.TrimSuffix(strings.TrimSpace(repositoryURL), "/")
	packageName = strings.TrimSpace(packageName)

	if repositoryURL == "" {
		return nil, &ValidationError{Field: "repositoryURL", Message: "repository URL is required"}
	}
	if packageName == "" {
		return nil, &ValidationError{Field: "packageName", Message: "package name is required"}
	}

	if cached, ok := m.mirrorCache().getPackageVersion(repositoryURL, packageName); ok && search == nil {
		return cached, nil
	}

	client := m.httpClient()

	var searchPaths []aptIndexPath
	if path, ok := exactAptIndexPath(search); ok {
		searchPaths = []aptIndexPath{path}
	} else {
		indexPaths, err := m.discoverAptIndexPaths(client, repositoryURL)
		if err != nil {
			return nil, err
		}

		if search == nil {
			searchPaths = filterIndexPathsToLatestCodename(indexPaths)
		} else {
			searchPaths = narrowAptSearchPaths(indexPaths, search)
		}
		if len(searchPaths) == 0 {
			return nil, &InvalidMirrorError{
				URL: repositoryURL,
				Err: fmt.Errorf("no matching package indexes for search filters"),
			}
		}
	}

	useComponentWaves := search == nil
	best := m.searchPackageIndexes(client, repositoryURL, searchPaths, packageName, useComponentWaves)
	if best == nil {
		notFound := &PackageNotFoundError{Package: packageName}
		if search != nil {
			notFound.Release = search.Suite
			notFound.Component = search.Component
			notFound.Arch = search.Arch
			if notFound.Arch == "" {
				notFound.Arch = aptPreferredArch
			}
		}
		return nil, notFound
	}

	result := &PackageLookupResult{
		PackageName: packageName,
		Version:     best.Version,
		Suite:       best.Suite,
		Component:   best.Component,
		Arch:        best.Arch,
		IndexPath:   best.IndexPath,
		Filename:    best.Filename,
	}
	if search == nil {
		m.mirrorCache().setPackageVersion(repositoryURL, packageName, result)
	}

	return result, nil
}

func (m *Mirror) discoverAptIndexPaths(client *http.Client, repositoryURL string) ([]aptIndexPath, error) {
	if cached, ok := m.mirrorCache().getIndexPaths(repositoryURL); ok {
		return cached, nil
	}

	paths, err := discoverAptIndexPathsFromReleases(client, repositoryURL)
	if err != nil {
		return nil, err
	}

	m.mirrorCache().setIndexPaths(repositoryURL, paths)
	return paths, nil
}

func discoverAptIndexPathsFromReleases(client *http.Client, repositoryURL string) ([]aptIndexPath, error) {
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		metas = make(map[string]aptReleaseMeta)
	)

	for _, suite := range aptFallbackSuites {
		wg.Add(1)
		go func(suite string) {
			defer wg.Done()

			releaseURL := fmt.Sprintf("%s/dists/%s/Release", repositoryURL, suite)
			releaseBody, err := fetchTextIfOK(client, releaseURL)
			if err != nil || releaseBody == "" {
				return
			}

			meta := parseAptRelease(releaseBody)
			if len(meta.Components) == 0 {
				return
			}

			mu.Lock()
			metas[suite] = meta
			mu.Unlock()
		}(suite)
	}
	wg.Wait()

	if len(metas) == 0 {
		return nil, &InvalidMirrorError{
			URL: repositoryURL,
			Err: fmt.Errorf("unable to discover apt package indexes"),
		}
	}

	seen := make(map[string]struct{})
	var paths []aptIndexPath

	for suite, meta := range metas {
		for _, arch := range archsFromReleaseMeta(meta) {
			if !releaseHasArch(meta, arch) {
				continue
			}
			for _, component := range meta.Components {
				addAptIndexPath(&paths, seen, suite, component, arch, aptPackagesFile)
			}
		}
	}

	if len(paths) == 0 {
		return nil, &InvalidMirrorError{
			URL: repositoryURL,
			Err: fmt.Errorf("unable to discover apt package indexes"),
		}
	}

	return paths, nil
}

func (m *Mirror) searchPackageIndexes(
	client *http.Client,
	repositoryURL string,
	paths []aptIndexPath,
	packageName string,
	byComponentWave bool,
) *aptPackageCandidate {
	if !byComponentWave {
		return m.lookupPackageParallel(client, repositoryURL, paths, packageName)
	}

	for _, components := range aptSearchComponentWaves {
		batch := filterIndexPathsByComponents(paths, components)
		if len(batch) == 0 {
			continue
		}
		if best := m.lookupPackageParallel(client, repositoryURL, batch, packageName); best != nil {
			return best
		}
	}
	return nil
}

func (m *Mirror) lookupPackageParallel(
	client *http.Client,
	repositoryURL string,
	paths []aptIndexPath,
	packageName string,
) *aptPackageCandidate {
	if len(paths) == 0 {
		return nil
	}
	if len(paths) == 1 {
		candidate, err := m.lookupPackageInIndex(client, repositoryURL, paths[0], packageName)
		if err != nil || candidate == nil {
			return nil
		}
		return candidate
	}

	sem := make(chan struct{}, aptIndexLookupWorkers)
	var (
		mu   sync.Mutex
		best *aptPackageCandidate
		wg   sync.WaitGroup
	)

	for _, path := range paths {
		wg.Add(1)
		go func(path aptIndexPath) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			candidate, err := m.lookupPackageInIndex(client, repositoryURL, path, packageName)
			if err != nil || candidate == nil {
				return
			}

			mu.Lock()
			if best == nil || debVersionGreaterThan(candidate.Version, best.Version) {
				copyCandidate := *candidate
				best = &copyCandidate
			}
			mu.Unlock()
		}(path)
	}

	wg.Wait()
	return best
}

func (m *Mirror) lookupPackageInIndex(
	client *http.Client,
	repositoryURL string,
	indexPath aptIndexPath,
	packageName string,
) (*aptPackageCandidate, error) {
	indexURL := fmt.Sprintf(
		"%s/dists/%s/%s/binary-%s/%s",
		repositoryURL,
		indexPath.Suite,
		indexPath.Component,
		indexPath.Arch,
		indexPath.File,
	)

	body, err := m.FetchMirrorFile(client, indexURL)
	if err != nil {
		return nil, err
	}

	reader, err := decompressAptIndex(body, indexPath.File)
	if err != nil {
		return nil, err
	}

	candidate, err := findLatestPackageInIndex(reader, packageName)
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, nil
	}

	candidate.Suite = indexPath.Suite
	candidate.Component = indexPath.Component
	candidate.Arch = indexPath.Arch
	candidate.IndexPath = indexURL

	return candidate, nil
}

func (m *Mirror) FetchMirrorFile(client *http.Client, rawURL string) (io.ReadCloser, error) {
	cache := m.mirrorCache()
	if data, ok := cache.getListFile(rawURL); ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}

	if meta := cache.getListFileMeta(rawURL); meta != nil {
		if meta.ETag != "" {
			req.Header.Set("If-None-Match", meta.ETag)
		}
		if meta.LastModified != "" {
			req.Header.Set("If-Modified-Since", meta.LastModified)
		}
	}

	req.Header.Set("User-Agent", constants.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, &HttpRequestError{URL: rawURL, Err: err}
	}

	if resp.StatusCode == http.StatusNotModified {
		if data, ok := cache.readListFileData(rawURL); ok {
			resp.Body.Close()
			cache.touchListFileMeta(rawURL)
			return io.NopCloser(bytes.NewReader(data)), nil
		}
		resp.Body.Close()
		return nil, &HttpRequestError{URL: rawURL, StatusCode: resp.StatusCode}
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &HttpRequestError{URL: rawURL, StatusCode: resp.StatusCode}
	}

	data, err := readHTTPBody(resp)
	if err != nil {
		return nil, &HttpRequestError{URL: rawURL, Err: err}
	}

	cache.setListFile(rawURL, data, resp.Header)
	return io.NopCloser(bytes.NewReader(data)), nil
}

func fetchTextIfOK(client *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", constants.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := readHTTPBody(resp)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

func exactAptIndexPath(search *PackageSearch) (aptIndexPath, bool) {
	if search == nil || search.Suite == "" || search.Component == "" || search.Arch == "" {
		return aptIndexPath{}, false
	}

	return aptIndexPath{
		Suite:     search.Suite,
		Component: search.Component,
		Arch:      search.Arch,
		File:      aptPackagesFile,
	}, true
}

func archsFromReleaseMeta(meta aptReleaseMeta) []string {
	if len(meta.Architectures) == 0 {
		return []string{aptPreferredArch}
	}
	return meta.Architectures
}

func filterIndexPathsToLatestCodename(paths []aptIndexPath) []aptIndexPath {
	found := make(map[string]struct{})
	for _, path := range paths {
		found[suiteCodename(path.Suite)] = struct{}{}
	}
	latest := ""
	for _, name := range aptCodenamePriority {
		if _, ok := found[name]; ok {
			latest = name
			break
		}
	}
	if latest == "" {
		for name := range found {
			latest = name
			break
		}
	}
	if latest == "" {
		return paths
	}

	filtered := make([]aptIndexPath, 0, len(paths))
	for _, path := range paths {
		if suiteCodename(path.Suite) == latest {
			filtered = append(filtered, path)
		}
	}
	return filtered
}

func narrowAptSearchPaths(paths []aptIndexPath, search *PackageSearch) []aptIndexPath {
	if search == nil {
		return paths
	}

	exactSuite := search.Suite != "" && search.Component != ""

	filtered := make([]aptIndexPath, 0, len(paths))
	for _, path := range paths {
		if path.File != aptPackagesFile {
			continue
		}
		if search.Arch != "" && path.Arch != search.Arch {
			continue
		}
		if search.Suite != "" {
			if exactSuite {
				if path.Suite != search.Suite {
					continue
				}
			} else if path.Suite != search.Suite && suiteCodename(path.Suite) != search.Suite {
				continue
			}
		}
		if search.Component != "" && path.Component != search.Component {
			continue
		}
		filtered = append(filtered, path)
	}

	if search.Suite != "" && !exactSuite && !strings.Contains(search.Suite, "-") {
		codename := search.Suite
		next := make([]aptIndexPath, 0, len(filtered))
		for _, path := range filtered {
			if suiteCodename(path.Suite) == codename {
				next = append(next, path)
			}
		}
		return next
	}

	return filtered
}

func filterIndexPathsByComponents(paths []aptIndexPath, components []string) []aptIndexPath {
	allowed := make(map[string]struct{}, len(components))
	for _, component := range components {
		allowed[component] = struct{}{}
	}

	filtered := make([]aptIndexPath, 0, len(paths))
	for _, path := range paths {
		if _, ok := allowed[path.Component]; ok {
			filtered = append(filtered, path)
		}
	}
	return filtered
}

func addAptIndexPath(paths *[]aptIndexPath, seen map[string]struct{}, suite, component, arch, file string) {
	key := suite + "/" + component + "/" + arch + "/" + file
	if _, ok := seen[key]; ok {
		return
	}

	seen[key] = struct{}{}
	*paths = append(*paths, aptIndexPath{
		Suite:     suite,
		Component: component,
		Arch:      arch,
		File:      file,
	})
}

func suiteCodename(suite string) string {
	for _, suffix := range []string{"-updates", "-security", "-backports"} {
		if strings.HasSuffix(suite, suffix) {
			return strings.TrimSuffix(suite, suffix)
		}
	}
	return suite
}

func latestAptCodename(metas map[string]aptReleaseMeta) string {
	found := make(map[string]struct{}, len(metas))
	for suite := range metas {
		found[suiteCodename(suite)] = struct{}{}
	}
	for _, name := range aptCodenamePriority {
		if _, ok := found[name]; ok {
			return name
		}
	}
	for name := range found {
		return name
	}
	return ""
}

type aptReleaseMeta struct {
	Components    []string
	Architectures []string
}

func parseAptRelease(body string) aptReleaseMeta {
	meta := aptReleaseMeta{}
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "Components:"):
			meta.Components = strings.Fields(strings.TrimPrefix(line, "Components:"))
		case strings.HasPrefix(line, "Architectures:"):
			meta.Architectures = strings.Fields(strings.TrimPrefix(line, "Architectures:"))
		}
	}
	return meta
}

func releaseHasArch(meta aptReleaseMeta, arch string) bool {
	if len(meta.Architectures) == 0 {
		return true
	}
	for _, candidate := range meta.Architectures {
		if candidate == arch {
			return true
		}
	}
	return false
}

func decompressAptIndex(body io.ReadCloser, fileName string) (io.Reader, error) {
	switch {
	case strings.HasSuffix(fileName, ".gz") || fileName == "ls-lR.gz":
		gzReader, err := gzip.NewReader(body)
		if err != nil {
			body.Close()
			return nil, err
		}
		return &gzipReadCloser{Reader: gzReader, closers: []io.Closer{gzReader, body}}, nil
	default:
		return &readCloserWrapper{Reader: body, closer: body}, nil
	}
}

type readCloserWrapper struct {
	io.Reader
	closer io.Closer
}

func (r *readCloserWrapper) Close() error {
	return r.closer.Close()
}

type gzipReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (g *gzipReadCloser) Close() error {
	var firstErr error
	for _, closer := range g.closers {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func findLatestPackageInIndex(body io.Reader, packageName string) (*aptPackageCandidate, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		currentPackage  string
		currentVersion  string
		currentFilename string
		best            *aptPackageCandidate
	)

	flush := func() {
		if currentPackage != packageName || currentVersion == "" {
			return
		}
		candidate := aptPackageCandidate{
			Version:  currentVersion,
			Filename: currentFilename,
		}
		if best == nil || debVersionGreaterThan(candidate.Version, best.Version) {
			copyCandidate := candidate
			best = &copyCandidate
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "Package: "):
			flush()
			currentPackage = strings.TrimSpace(strings.TrimPrefix(line, "Package: "))
			currentVersion = ""
			currentFilename = ""
		case strings.HasPrefix(line, "Version: "):
			currentVersion = strings.TrimSpace(strings.TrimPrefix(line, "Version: "))
		case strings.HasPrefix(line, "Filename: "):
			currentFilename = strings.TrimSpace(strings.TrimPrefix(line, "Filename: "))
		case line == "":
			flush()
			currentPackage = ""
			currentVersion = ""
			currentFilename = ""
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	flush()
	return best, nil
}

func debVersionGreaterThan(left, right string) bool {
	if right == "" {
		return left != ""
	}
	if left == "" {
		return false
	}

	lv, err := version.NewVersion(left)
	if err != nil {
		return strings.Compare(left, right) > 0
	}

	rv, err := version.NewVersion(right)
	if err != nil {
		return strings.Compare(left, right) > 0
	}

	return lv.GreaterThan(rv)
}
