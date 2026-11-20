package engine

import (
	"slices"
	"testing"

	"github.com/wappsdev/cpa-balancer/internal/config"
	"github.com/wappsdev/cpa-balancer/internal/history"
	"github.com/wappsdev/cpa-balancer/internal/state"
)

// bucketedWorld lists Extra in the Fable bucket and pins it to the given accounts. The readings make
// W the account the Fable bucket's balancing would choose (the earliest Fable reset, the most room).
func bucketedWorld(t *testing.T, pinned ...string) *world {
	w := newWorld(t, W, G, X)
	w.cfg.Buckets.Fable = []string{Fable, Extra}
	w.cfg.ExtraModels = []config.ExtraModel{{ID: Extra, Accounts: pinned, ContextLength: 1000, MaxCompletionTokens: 100}}
	w.folder("-work-alpha", 1)
	w.start()
	w.use(W, 10, 140, 0)
	w.use(G, 20, 100, 0)
	w.use(X, 30, 120, 0)
	fableProbe(w.e, W, 1, 50, w.clock.t)
	fableProbe(w.e, G, 5, 90, w.clock.t)
	fableProbe(w.e, X, 30, 120, w.clock.t)
	return w
}

// Pinning wins over the bucket: the model's request is picked among its accounts only, by any name
// CPA routes it by, with or without a session, and a session pin to another account changes
// nothing.
func TestAnExtraModelInTheFableBucketNeverLeavesItsPinnedAccounts(t *testing.T) {
	w := bucketedWorld(t, X)
	if got := w.e.Pick(w.req(Fable, 0, "", W, G, X)).AuthID; got != W {
		t.Fatalf("setup: the Fable bucket's own balancing chooses %s, got %q", W, got)
	}
	if err := w.e.SetPin(state.PinSession, sidN(1), W); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{Extra, Extra + "(high)", "p/" + Extra, " " + Extra + " ", "Claude-Mythos-9-1 (max)"} {
		for _, session := range []int{0, 1} {
			if got := w.e.Pick(w.req(model, session, "", W, G, X)); got.AuthID != X || !got.Handled || got.Reject {
				t.Fatalf("%s session %d: pick = %+v, want %s", model, session, got, X)
			}
			if got := w.e.Pick(w.req(model, session, "", W, G)); !refused(got) {
				t.Fatalf("%s session %d: pinned account not offered: pick = %+v, want a refusal, never W or G", model, session, got)
			}
		}
	}
}

// When no pinned account can take the request the model is refused; the accounts the bucket would
// balance over are never tried.
func TestAnExtraModelInTheFableBucketIsRefusedWhenItsAccountIsFull(t *testing.T) {
	w := bucketedWorld(t, X)
	fableProbe(w.e, X, 99.5, 120, w.clock.t)
	if got := w.e.Pick(w.req(Extra, 0, "", W, G, X)); !refused(got) {
		t.Fatalf("pick = %+v, want a refusal", got)
	}
	reason, ok := w.e.EarlyRefusal(Extra, nil, false, nil)
	if !ok || reason == "" {
		t.Fatalf("EarlyRefusal = %q, %v", reason, ok)
	}
}

// The bucket names the quota the model spends: with the model in the Fable bucket a pinned account
// whose Fable share is full is skipped, under any name of the model.
func TestAnExtraModelInTheFableBucketSpendsTheFableShare(t *testing.T) {
	w := bucketedWorld(t, X, G)
	w.use(X, 30, 40, 0) // X resets first, in the all-models week and in the Fable share, so it would be chosen
	fableProbe(w.e, X, 30, 40, w.clock.t)
	if got := w.e.Pick(w.req(Extra, 0, "", W, G, X)).AuthID; got != X {
		t.Fatalf("setup: got %q, want %s", got, X)
	}
	fableProbe(w.e, X, 99.5, 40, w.clock.t)
	for _, model := range []string{Extra, Extra + "(high)", "p/" + Extra} {
		if got := w.e.Pick(w.req(model, 0, "", W, G, X)).AuthID; got != G {
			t.Fatalf("%s: the pinned account with a full Fable share is skipped, got %q", model, got)
		}
	}
}

func TestTheMeterListsAnExtraModelOfTheFableBucketOnce(t *testing.T) {
	cfg := mythosConfig(t)
	cfg.Buckets.Fable = []string{Fable, Extra}
	e := New(cfg, newHost(), (&clock{t0}).now, nil)
	v := e.Status()
	e.AddSessions(&v, history.Sessions{})
	if !slices.Equal(v.Meter.FableModels, []string{Fable, Extra}) {
		t.Fatalf("fable models = %v, want each once", v.Meter.FableModels)
	}
}
