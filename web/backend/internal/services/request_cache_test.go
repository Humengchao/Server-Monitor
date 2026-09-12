package services

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestCacheCoalescesAndExpires(testContext *testing.T) {
	cache := NewRequestCache[string](2)
	var calls atomic.Int32
	var group sync.WaitGroup
	loader := func(context.Context) (string, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return "value", nil
	}
	for index := 0; index < 20; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			value, err := cache.Get(context.Background(), "same", time.Minute, loader)
			if err != nil || value != "value" {
				testContext.Errorf("result %q %v", value, err)
			}
		}()
	}
	group.Wait()
	if calls.Load() != 1 {
		testContext.Fatalf("duplicate calls %d", calls.Load())
	}
	cache.Forget("same")
	if _, err := cache.Get(context.Background(), "same", time.Millisecond, loader); err != nil {
		testContext.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := cache.Get(context.Background(), "same", time.Minute, loader); err != nil {
		testContext.Fatal(err)
	}
	if calls.Load() != 3 {
		testContext.Fatal(calls.Load())
	}
}
func TestRequestCacheCanceledWaiterAndInvalidation(testContext *testing.T) {
	cache := NewRequestCache[string](2)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(finished)
		_, err := cache.Get(ctx, "same", time.Minute, func(context.Context) (string, error) { close(started); <-release; return "old", nil })
		if err != context.Canceled {
			testContext.Errorf("cancel: %v", err)
		}
	}()
	<-started
	cancel()
	<-finished
	cache.Forget("same")
	value, err := cache.Get(context.Background(), "same", time.Minute, func(context.Context) (string, error) { return "new", nil })
	if err != nil || value != "new" {
		testContext.Fatal(value, err)
	}
	close(release)
	time.Sleep(20 * time.Millisecond)
	value, err = cache.Get(context.Background(), "same", time.Minute, func(context.Context) (string, error) { testContext.Error("invalidation lost"); return "bad", nil })
	if err != nil || value != "new" {
		testContext.Fatal(value, err)
	}
}
