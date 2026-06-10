package apt

import (
	"net/http"
	"strings"
	"sync"
)

const aptIndexLookupWorkers = 10

var aptSearchComponentWaves = [][]string{
	{"main"},
	{"universe"},
	{"restricted", "multiverse"},
}

func filterAptIndexPathsByComponents(paths []aptIndexPath, components []string) []aptIndexPath {
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

func splitAptIndexPathsBySuitePriority(paths []aptIndexPath) (priority, base []aptIndexPath) {
	for _, path := range paths {
		if strings.HasSuffix(path.Suite, "-security") || strings.HasSuffix(path.Suite, "-updates") {
			priority = append(priority, path)
			continue
		}
		base = append(base, path)
	}
	return priority, base
}

func (m *AptMirrorService) searchPackageInComponentWaves(
	client *http.Client,
	repositoryURL string,
	searchPaths []aptIndexPath,
	packageName string,
) *aptPackageCandidate {
	var best *aptPackageCandidate

	for _, components := range aptSearchComponentWaves {
		batch := filterAptIndexPathsByComponents(searchPaths, components)
		if len(batch) == 0 {
			continue
		}

		candidate := m.searchPackageAcrossIndexes(client, repositoryURL, batch, packageName)
		if candidate == nil {
			continue
		}

		if best == nil || debVersionGreaterThan(candidate.Version, best.Version) {
			copyCandidate := *candidate
			best = &copyCandidate
		}

		// Packages almost always live in a single component; stop after the first hit.
		return best
	}

	return best
}

func (m *AptMirrorService) searchPackageAcrossIndexes(
	client *http.Client,
	repositoryURL string,
	searchPaths []aptIndexPath,
	packageName string,
) *aptPackageCandidate {
	if len(searchPaths) == 0 {
		return nil
	}

	priority, base := splitAptIndexPathsBySuitePriority(searchPaths)

	var best *aptPackageCandidate
	if len(priority) > 0 {
		best = m.searchPackageAcrossIndexesParallel(client, repositoryURL, priority, packageName)
	}
	if len(base) == 0 {
		return best
	}

	baseBest := m.searchPackageAcrossIndexesParallel(client, repositoryURL, base, packageName)
	if baseBest == nil {
		return best
	}
	if best == nil || debVersionGreaterThan(baseBest.Version, best.Version) {
		return baseBest
	}
	return best
}

func (m *AptMirrorService) searchPackageAcrossIndexesParallel(
	client *http.Client,
	repositoryURL string,
	searchPaths []aptIndexPath,
	packageName string,
) *aptPackageCandidate {
	if len(searchPaths) == 0 {
		return nil
	}

	if len(searchPaths) == 1 {
		candidate, err := m.lookupPackageInIndex(client, repositoryURL, searchPaths[0], packageName)
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

	for _, indexPath := range searchPaths {
		wg.Add(1)
		go func(indexPath aptIndexPath) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			candidate, err := m.lookupPackageInIndex(client, repositoryURL, indexPath, packageName)
			if err != nil || candidate == nil {
				return
			}

			mu.Lock()
			if best == nil || debVersionGreaterThan(candidate.Version, best.Version) {
				copyCandidate := *candidate
				best = &copyCandidate
			}
			mu.Unlock()
		}(indexPath)
	}

	wg.Wait()
	return best
}
