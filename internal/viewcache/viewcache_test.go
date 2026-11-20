package viewcache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testTTL    = 5 * time.Second
	testMaxAge = 60 * time.Second
)

// clock is a hand-set time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// rig is a cache over a computation the test releases by hand: each call to compute reports itself
// on started, waits for a token on release, and returns its own call number.
type rig struct {
	c       *Cache[int]
	clock   *clock
	calls   atomic.Int32
	started chan int
	release chan struct{}
}

func newRig() *rig {
	r := &rig{clock: &clock{t: time.Unix(1_000_000, 0)}, started: make(chan int, 64), release: make(chan struct{}, 64)}
	r.c = New(testTTL, testMaxAge, r.clock.now, func() (int, error) {
		n := int(r.calls.Add(1))
		r.started <- n
		<-r.release
		return n, nil
	})
	return r
}

// get calls Get on its own goroutine and returns what it answers on a channel.
func (r *rig) get() <-chan int {
	out := make(chan int, 1)
	go func() {
		v, err := r.c.Get()
		if err != nil {
			v = -1
		}
		out <- v
	}()
	return out
}

func within(t *testing.T, ch <-chan int, what string) int {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no answer", what)
		return 0
	}
}

func (r *rig) waitStarted(t *testing.T, n int) {
	t.Helper()
	select {
	case got := <-r.started:
		if got != n {
			t.Fatalf("computation %d started, want %d", got, n)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("computation %d did not start", n)
	}
}

func TestConcurrentCallersShareOneComputation(t *testing.T) {
	r := newRig()
	var answers []<-chan int
	for i := 0; i < 20; i++ {
		answers = append(answers, r.get())
	}
	r.waitStarted(t, 1)
	time.Sleep(50 * time.Millisecond) // let every caller reach the wait
	r.release <- struct{}{}
	for i, a := range answers {
		if v := within(t, a, "caller"); v != 1 {
			t.Fatalf("caller %d got %d, want the one computation's answer", i, v)
		}
	}
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("%d computations for 20 callers, want 1", n)
	}
}

func TestFreshResultIsServedFromCache(t *testing.T) {
	r := newRig()
	r.release <- struct{}{}
	within(t, r.get(), "first")
	r.waitStarted(t, 1)
	r.clock.advance(testTTL - time.Millisecond)
	if v := within(t, r.get(), "second"); v != 1 {
		t.Fatalf("got %d, want the cached 1", v)
	}
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("%d computations, want 1", n)
	}
}

func TestStaleResultIsServedWhileOneRefreshRuns(t *testing.T) {
	r := newRig()
	r.release <- struct{}{}
	within(t, r.get(), "first")
	r.waitStarted(t, 1)

	r.clock.advance(testTTL + time.Second)
	if v := within(t, r.get(), "stale"); v != 1 {
		t.Fatalf("got %d, want the stale 1 at once", v)
	}
	r.waitStarted(t, 2) // the refresh runs in the background, still unreleased
	if v := within(t, r.get(), "stale again"); v != 1 {
		t.Fatalf("got %d, want the stale 1 again", v)
	}
	if n := r.calls.Load(); n != 2 {
		t.Fatalf("%d computations, want the first and one refresh", n)
	}

	r.release <- struct{}{}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if v := within(t, r.get(), "refreshed"); v == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the refresh never replaced the stale result")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := r.calls.Load(); n != 2 {
		t.Fatalf("%d computations after the refresh, want 2", n)
	}
}

func TestResultPastTheHardCapIsNotServed(t *testing.T) {
	r := newRig()
	r.release <- struct{}{}
	within(t, r.get(), "first")
	r.waitStarted(t, 1)

	r.clock.advance(testMaxAge + time.Second)
	answer := r.get()
	r.waitStarted(t, 2)
	select {
	case v := <-answer:
		t.Fatalf("got %d before the refresh finished, want the caller to wait", v)
	case <-time.After(100 * time.Millisecond):
	}
	r.release <- struct{}{}
	if v := within(t, answer, "waiter"); v != 2 {
		t.Fatalf("got %d, want the refreshed 2", v)
	}
}

func TestInvalidateMakesTheNextCallRecompute(t *testing.T) {
	r := newRig()
	r.release <- struct{}{}
	within(t, r.get(), "first")
	r.waitStarted(t, 1)

	r.c.Invalidate() // well inside the TTL
	answer := r.get()
	r.waitStarted(t, 2)
	select {
	case v := <-answer:
		t.Fatalf("got %d after an invalidation, want the caller to wait for a new computation", v)
	case <-time.After(100 * time.Millisecond):
	}
	r.release <- struct{}{}
	if v := within(t, answer, "after invalidate"); v != 2 {
		t.Fatalf("got %d, want 2", v)
	}
}

func TestComputationStartedBeforeInvalidateDoesNotAnswerLaterCallers(t *testing.T) {
	r := newRig()
	early := r.get()
	r.waitStarted(t, 1)

	r.c.Invalidate() // the state changed while computation 1 was reading the old one
	late := r.get()
	r.waitStarted(t, 2)

	r.release <- struct{}{} // finishes computation 1, whose state is older than the change
	r.release <- struct{}{}
	within(t, early, "early")
	if v := within(t, late, "late"); v != 2 {
		t.Fatalf("a caller after the invalidation got %d, want the newer computation's 2", v)
	}
	if v := within(t, r.get(), "later"); v != 2 {
		t.Fatalf("the cache holds %d, want 2: the old computation must not be stored", v)
	}
}

func TestFailedComputationIsNotCached(t *testing.T) {
	fail := atomic.Bool{}
	fail.Store(true)
	c := New(testTTL, testMaxAge, time.Now, func() (int, error) {
		if fail.Load() {
			return 0, errors.New("boom")
		}
		return 7, nil
	})
	if _, err := c.Get(); err == nil {
		t.Fatal("want the computation's error")
	}
	fail.Store(false)
	if v, err := c.Get(); err != nil || v != 7 {
		t.Fatalf("got %d, %v; want the next call to compute again", v, err)
	}
}

func TestFailedRefreshKeepsServingTheStaleResultUntilTheHardCap(t *testing.T) {
	clk := &clock{t: time.Unix(1_000_000, 0)}
	var calls atomic.Int32
	failing := make(chan struct{}, 8)
	c := New(testTTL, testMaxAge, clk.now, func() (int, error) {
		n := int(calls.Add(1))
		if n > 1 {
			failing <- struct{}{}
			return 0, errors.New("boom")
		}
		return n, nil
	})
	if v, _ := c.Get(); v != 1 {
		t.Fatalf("got %d, want 1", v)
	}
	clk.advance(testTTL + time.Second)
	if v, err := c.Get(); err != nil || v != 1 {
		t.Fatalf("got %d, %v; want the stale 1", v, err)
	}
	<-failing
	time.Sleep(50 * time.Millisecond) // the failed refresh has ended
	if v, err := c.Get(); err != nil || v != 1 {
		t.Fatalf("got %d, %v; want the stale 1 after a failed refresh", v, err)
	}
	clk.advance(testMaxAge)
	if _, err := c.Get(); err == nil {
		t.Fatal("past the hard cap the caller must see the error, not a result older than the cap")
	}
}

func TestPanickingComputationBecomesAnError(t *testing.T) {
	c := New(testTTL, testMaxAge, time.Now, func() (int, error) { panic("bad") })
	_, err := c.Get()
	var p *PanicError
	if !errors.As(err, &p) || p.Value != "bad" || len(p.Stack) == 0 {
		t.Fatalf("got %v, want a PanicError with the value and the stack", err)
	}
}
