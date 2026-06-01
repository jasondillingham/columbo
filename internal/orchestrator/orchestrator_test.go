package orchestrator

import (
	"sync/atomic"
	"testing"
	"time"
)

// All thunks must run concurrently (proved by a barrier: the test does not
// release them until all N are simultaneously running — sequential execution
// would never reach N and the test would hang), and results must come back in
// input order regardless of completion order.
func TestRunParallelOrderAndConcurrency(t *testing.T) {
	const n = 6
	var running, maxRunning int32
	start := make(chan struct{})

	thunks := make([]func() int, n)
	for i := 0; i < n; i++ {
		i := i
		thunks[i] = func() int {
			c := atomic.AddInt32(&running, 1)
			for {
				m := atomic.LoadInt32(&maxRunning)
				if c <= m || atomic.CompareAndSwapInt32(&maxRunning, m, c) {
					break
				}
			}
			<-start // block until all have started
			return i * 10
		}
	}

	done := make(chan []int, 1)
	go func() { done <- RunParallel(thunks) }()

	deadline := time.After(5 * time.Second)
	for atomic.LoadInt32(&running) < n {
		select {
		case <-deadline:
			t.Fatalf("only %d of %d thunks running; not concurrent", atomic.LoadInt32(&running), n)
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(start)
	out := <-done

	if maxRunning != n {
		t.Errorf("max concurrent = %d, want %d", maxRunning, n)
	}
	for i := 0; i < n; i++ {
		if out[i] != i*10 {
			t.Errorf("out[%d] = %d, want %d (order not preserved)", i, out[i], i*10)
		}
	}
}

func TestRunParallelEmpty(t *testing.T) {
	if out := RunParallel[int](nil); len(out) != 0 {
		t.Errorf("empty input should give empty output, got %v", out)
	}
}
