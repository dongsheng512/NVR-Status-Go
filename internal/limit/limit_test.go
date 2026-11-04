package limit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiterCapsConcurrency(t *testing.T) {
	l := New(2)
	var current, max int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Acquire(nil); err != nil {
				t.Error(err)
				return
			}
			defer l.Release()
			n := atomic.AddInt32(&current, 1)
			for {
				old := atomic.LoadInt32(&max)
				if n <= old || atomic.CompareAndSwapInt32(&max, old, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt32(&current, -1)
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&max); got > 2 {
		t.Fatalf("max concurrency = %d, want <= 2", got)
	}
}

func TestLimiterAcquireHonorsCancel(t *testing.T) {
	l := New(1)
	if err := l.Acquire(nil); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := l.Acquire(func() error { return errSentinel })
	if err != errSentinel {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cancel check too slow: %v", time.Since(start))
	}
	l.Release()
}

var errSentinel = &sentinelError{}

type sentinelError struct{}

func (*sentinelError) Error() string { return "sentinel" }
