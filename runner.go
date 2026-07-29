package main

import "sync"

// fanOut runs work(item) for every item with bounded concurrency, returning the
// results index-aligned with items. Each goroutine writes a distinct index, so no
// locking is needed around the results slice.
//
// Ordering is the caller's job: sort the input, not the output. Sorting results
// here would need to know a field of T, and callers already have a sorted list.
func fanOut[T any](items []string, limit int, work func(string) T) []T {
	if limit < 1 {
		limit = 1
	}
	results := make([]T, len(items))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for i, p := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = work(p)
		}(i, p)
	}
	wg.Wait()
	return results
}
