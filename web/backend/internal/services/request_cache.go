package services

import (
	"context"
	"errors"
	"sync"
	"time"
)

type cachedRequest[Value any] struct {
	done    chan struct{}
	value   Value
	err     error
	expires time.Time
}
type RequestCache[Value any] struct {
	mutex   sync.Mutex
	entries map[string]*cachedRequest[Value]
	slots   chan struct{}
}

func NewRequestCache[Value any](concurrency int) *RequestCache[Value] {
	return &RequestCache[Value]{entries: make(map[string]*cachedRequest[Value]), slots: make(chan struct{}, concurrency)}
}
func (cache *RequestCache[Value]) Forget(key string) {
	cache.mutex.Lock()
	delete(cache.entries, key)
	cache.mutex.Unlock()
}
func (cache *RequestCache[Value]) Get(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (Value, error)) (Value, error) {
	cache.mutex.Lock()
	now := time.Now()
	for name, entry := range cache.entries {
		if !entry.expires.IsZero() && now.After(entry.expires) {
			delete(cache.entries, name)
		}
	}
	entry := cache.entries[key]
	if entry == nil {
		if len(cache.entries) >= 512 {
			cache.mutex.Unlock()
			var empty Value
			return empty, errors.New("too many pending requests")
		}
		entry = &cachedRequest[Value]{done: make(chan struct{})}
		cache.entries[key] = entry
		go func() {
			task, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			select {
			case cache.slots <- struct{}{}:
				entry.value, entry.err = load(task)
				<-cache.slots
			case <-task.Done():
				entry.err = task.Err()
			}
			cache.mutex.Lock()
			if cache.entries[key] == entry {
				if entry.err != nil {
					delete(cache.entries, key)
				} else {
					entry.expires = time.Now().Add(ttl)
				}
			}
			close(entry.done)
			cache.mutex.Unlock()
		}()
	}
	cache.mutex.Unlock()
	select {
	case <-ctx.Done():
		var empty Value
		return empty, ctx.Err()
	case <-entry.done:
		return entry.value, entry.err
	}
}
