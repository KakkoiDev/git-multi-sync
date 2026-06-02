package main

import (
	"sort"
	"sync"
)

// fanOut runs work(path) for every path with bounded concurrency and returns the
// results sorted by path for deterministic output. Each goroutine writes a
// distinct index, so no locking is needed around the results slice.
func fanOut(paths []string, limit int, work func(string) Repo) []Repo {
	if limit < 1 {
		limit = 1
	}
	results := make([]Repo, len(paths))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for i, p := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = work(p)
		}(i, p)
	}
	wg.Wait()

	sort.Slice(results, func(a, b int) bool { return results[a].Path < results[b].Path })
	return results
}
