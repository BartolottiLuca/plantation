package store

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWithLockSerialisesConcurrentCallers(t *testing.T) {
	repo := NewTadoTokenRepo(migratedPool(t))
	ctx := context.Background()

	var inCrit int32
	var maxIn int32
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	start := make(chan struct{})

	run := func(state string) {
		defer wg.Done()
		<-start
		err := repo.WithLock(ctx, func(ctx context.Context, current TadoToken, w TadoTokenWriter) error {
			n := atomic.AddInt32(&inCrit, 1)
			for {
				old := atomic.LoadInt32(&maxIn)
				if n <= old || atomic.CompareAndSwapInt32(&maxIn, old, n) {
					break
				}
			}
			time.Sleep(150 * time.Millisecond)
			atomic.AddInt32(&inCrit, -1)
			current.State = state
			return w.Write(ctx, current)
		})
		errCh <- err
	}

	wg.Add(2)
	go run(TadoLinked)
	go run(TadoNeedsReauth)
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("WithLock: %v", err)
		}
	}
	if maxIn != 1 {
		t.Fatalf("concurrent holders = %d, want 1", maxIn)
	}

	got, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != TadoLinked && got.State != TadoNeedsReauth {
		t.Fatalf("final state = %q", got.State)
	}
	if got.AccessToken != nil {
		t.Fatal("Load returned token material that tests must not invent; state-only write should leave tokens nil")
	}
}
