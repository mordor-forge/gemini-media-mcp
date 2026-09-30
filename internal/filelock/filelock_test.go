package filelock

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireExcludesOtherHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	var inside, maxInside atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := Acquire(path) // a separate handle each time, like another process
			if err != nil {
				t.Error(err)
				return
			}
			n := inside.Add(1)
			for {
				m := maxInside.Load()
				if n <= m || maxInside.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			inside.Add(-1)
			if err := l.Unlock(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := maxInside.Load(); got != 1 {
		t.Fatalf("%d holders at once, want 1", got)
	}
}
