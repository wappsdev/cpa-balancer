// Package viewcache keeps one expensive, read-only view cheap to ask for. Callers that arrive
// together share one computation (single flight). A result younger than the TTL is served as it
// is. An older one is served at once while one computation refreshes it in the background (stale
// while revalidate), but never past the hard cap: then the caller waits for the refresh.
// Invalidate drops the result, for a change the next answer must show.
package viewcache

import (
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

// PanicError is what a computation that panicked becomes: its goroutine is not the caller's, so
// the panic must not leave it.
type PanicError struct {
	Value any
	Stack []byte
}

func (p *PanicError) Error() string { return fmt.Sprintf("panic: %v", p.Value) }

// flight is one run of the computation.
type flight[T any] struct {
	gen     uint64 // the invalidation count it started under
	started time.Time
	done    chan struct{}
	val     T
	err     error
}

// Cache holds the last result of compute. The computation runs without the cache's lock, so a slow
// one blocks no caller that can be answered from the cache.
type Cache[T any] struct {
	ttl, maxAge time.Duration
	now         func() time.Time
	compute     func() (T, error)

	mu  sync.Mutex
	gen uint64 // bumped by Invalidate
	has bool
	val T
	at  time.Time // when the computation that made val started: its state is no newer
	fl  *flight[T]
}

// New makes a cache. ttl is how long a result counts as fresh; maxAge is how long a stale one may
// still be served.
func New[T any](ttl, maxAge time.Duration, now func() time.Time, compute func() (T, error)) *Cache[T] {
	return &Cache[T]{ttl: ttl, maxAge: maxAge, now: now, compute: compute}
}

// Get answers from the cache, or from the computation it has to wait for.
func (c *Cache[T]) Get() (T, error) {
	c.mu.Lock()
	if c.has {
		age := c.now().Sub(c.at)
		if age < c.ttl {
			v := c.val
			c.mu.Unlock()
			return v, nil
		}
		if age < c.maxAge {
			c.join() // the refresh; this caller does not wait for it
			v := c.val
			c.mu.Unlock()
			return v, nil
		}
	}
	f := c.join()
	c.mu.Unlock()
	<-f.done
	return f.val, f.err
}

// Invalidate drops the result: the next Get waits for a computation that starts now or later. One
// still running began before the change, so its result answers only the callers already waiting.
func (c *Cache[T]) Invalidate() {
	c.mu.Lock()
	c.gen++
	c.has = false
	var zero T
	c.val = zero
	c.mu.Unlock()
}

// join returns the running computation of the current generation, and starts one if there is none.
// Callers hold c.mu.
func (c *Cache[T]) join() *flight[T] {
	if c.fl != nil && c.fl.gen == c.gen {
		return c.fl
	}
	f := &flight[T]{gen: c.gen, started: c.now(), done: make(chan struct{})}
	c.fl = f
	go c.run(f)
	return f
}

func (c *Cache[T]) run(f *flight[T]) {
	defer func() {
		if r := recover(); r != nil {
			f.err = &PanicError{Value: r, Stack: debug.Stack()}
		}
		c.mu.Lock()
		if c.fl == f {
			c.fl = nil
		}
		if f.err == nil && f.gen == c.gen {
			c.val, c.at, c.has = f.val, f.started, true
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.val, f.err = c.compute()
}
