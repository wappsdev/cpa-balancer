package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/engine"
	"github.com/wappsdev/cpa-balancer/internal/viewcache"
)

func statusStamp(t *testing.T) time.Time {
	t.Helper()
	v, ok := pluginAPI{}.Status().(engine.View)
	if !ok {
		t.Fatalf("status is not a view: %+v", pluginAPI{}.Status())
	}
	return v.GeneratedAt
}

func TestStatusWithinTheTTLIsTheSameComputation(t *testing.T) {
	affinitySetup(t, "")
	first := statusStamp(t)
	time.Sleep(10 * time.Millisecond)
	if second := statusStamp(t); !second.Equal(first) {
		t.Fatalf("the second status was computed again: %v then %v", first, second)
	}
}

func TestEveryChangeTheStatusMustShowDropsTheCachedView(t *testing.T) {
	affinitySetup(t, "")
	changes := map[string]func(){
		"pin":       func() { _ = pluginAPI{}.Pin("session", affinitySession, "b.json") },
		"clear pin": func() { _ = pluginAPI{}.Pin("session", affinitySession, "") },
		"run":       func() { pluginAPI{}.Run() },
		"recheck":   func() { pluginAPI{}.Recheck("") },
		"rebalance": func() { _, _ = pluginAPI{}.Rebalance(true) },
		"configure": func() {
			configureYAML(t, "state_path: "+engineNow().Config().StatePath+"\naccounts: [{auth_file: a.json}]\nbuckets: {main: [m]}\n")
		},
	}
	for _, name := range []string{"pin", "clear pin", "run", "recheck", "rebalance", "configure"} {
		before := statusStamp(t)
		time.Sleep(10 * time.Millisecond)
		changes[name]()
		time.Sleep(10 * time.Millisecond)
		if after := statusStamp(t); !after.After(before) {
			t.Errorf("%s: the next status is the cached one (%v, then %v)", name, before, after)
		}
	}
}

func TestOutlookWithinTheTTLIsTheSameComputation(t *testing.T) {
	affinitySetup(t, "")
	var computed atomic.Int32
	previous := outlookView
	outlookView = viewcache.New(time.Minute, time.Hour, time.Now, func() (any, error) {
		computed.Add(1)
		return "outlook", nil
	})
	t.Cleanup(func() { outlookView = previous })

	for i := 0; i < 3; i++ {
		if _, err := (pluginAPI{}).Outlook(); err != nil {
			t.Fatal(err)
		}
	}
	if n := computed.Load(); n != 1 {
		t.Fatalf("%d outlook computations for 3 calls, want 1", n)
	}
	if err := (pluginAPI{}).Pin("session", affinitySession, "b.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := (pluginAPI{}).Outlook(); err != nil {
		t.Fatal(err)
	}
	if n := computed.Load(); n != 2 {
		t.Fatalf("%d outlook computations after a change, want 2", n)
	}
}
