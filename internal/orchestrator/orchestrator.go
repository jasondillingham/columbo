// Package orchestrator runs lanes concurrently. v0.4 is local goroutine
// fan-out; the k3s-job runner is v0.6. The contract that matters is
// determinism: results come back in the SAME order as the input thunks,
// regardless of which lane finishes first, so a parallel round is
// byte-identical to a sequential one.
package orchestrator

import "sync"

// RunParallel runs every thunk concurrently and returns their results in input
// order. Each goroutine writes only its own output slot, so there is no shared
// mutable state and no lock on the results.
func RunParallel[T any](thunks []func() T) []T {
	out := make([]T, len(thunks))
	var wg sync.WaitGroup
	for i, fn := range thunks {
		wg.Add(1)
		go func(i int, fn func() T) {
			defer wg.Done()
			out[i] = fn()
		}(i, fn)
	}
	wg.Wait()
	return out
}
