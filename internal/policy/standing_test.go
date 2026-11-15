package policy

import (
	"testing"

	"github.com/wappsdev/cpa-balancer/internal/quota"
)

func TestStandingSeparatesAWeeklyCapFromAnythingThatOnlyHoldsAnAccountBack(t *testing.T) {
	cfg := testConfig()
	th := cfg.Thresholds
	a := cfg.Accounts[0] // no reserve: weekly cap 99
	withSession := func(weekly, session quota.Reading) Snapshot {
		return Snapshot{Readings: quota.Readings{quota.Main: weekly, quota.Session: session}}
	}
	cases := []struct {
		name string
		snap Snapshot
		want Standing
	}{
		{"room left", snap(quota.Readings{quota.Main: rd(50, 100)}), Usable},
		{"fresh reading at the weekly cap", snap(quota.Readings{quota.Main: rd(99, 100)}), Full},
		{"a 5-hour cap is a detour, not a full week", withSession(rd(50, 100), rd(98, 2)), Detour},
		{"a stale weekly reading is a detour", snap(quota.Readings{quota.Main: rd(10, 100, 7*60)}), Detour},
		{"a stale reading at the cap does not prove a full week", snap(quota.Readings{quota.Main: rd(99, 100, 7*60)}), Detour},
		{"no reading at all is a detour", snap(quota.Readings{}), Detour},
		{"a cooled-down account with room is a detour", Snapshot{Readings: quota.Readings{quota.Main: rd(50, 100)}, Unavailable: true}, Detour},
	}
	for _, c := range cases {
		if got := Stand(a, c.snap, true, quota.Main, now, th); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if got := Stand(a, snap(quota.Readings{quota.Main: rd(50, 100)}), false, quota.Main, now, th); got != Detour {
		t.Errorf("an account CPA does not list is a detour, got %v", got)
	}
	// a reserve account is full at its own cap: 85% far from the reset
	if got := Stand(cfg.Accounts[1], snap(quota.Readings{quota.Main: rd(85, 100)}), true, quota.Main, now, th); got != Full {
		t.Errorf("reserve cap: got %v", got)
	}
	// Fable use counts toward the all-models week, so a full all-models week is a full Fable week
	if got := Stand(a, snap(quota.Readings{quota.Main: rd(99, 100), quota.Fable: rd(10, 100)}), true, quota.Fable, now, th); got != Full {
		t.Errorf("fable with a full all-models week: got %v", got)
	}
}

func TestHeadroomIsWeeklyRoomUnderTheCapInProPlans(t *testing.T) {
	cfg := testConfig()
	th := cfg.Thresholds
	a := cfg.Accounts[0]
	room := func(s Snapshot) float64 { h, _ := Headroom(a, s, quota.Main, now, th); return h }
	if got := room(Snapshot{Readings: quota.Readings{quota.Main: rd(49, 100)}, Weight: 5}); got != 250 {
		t.Errorf("(99-49) points x weight 5 = 250, got %v", got)
	}
	// an unknown plan (weight 0) counts as one Pro plan, so it neither wins nor loses by default
	if got := room(Snapshot{Readings: quota.Readings{quota.Main: rd(49, 100)}}); got != 50 {
		t.Errorf("unknown weight counts as 1, got %v", got)
	}
	if got := room(Snapshot{Readings: quota.Readings{quota.Main: rd(99.5, 100)}, Weight: 1}); got != 0 {
		t.Errorf("past the cap there is no room, got %v", got)
	}
	if _, ok := Headroom(a, snap(quota.Readings{quota.Main: rd(10, 100, 7*60)}), quota.Main, now, th); ok {
		t.Error("a stale reading gives no headroom")
	}
	if _, ok := Headroom(a, snap(quota.Readings{}), quota.Main, now, th); ok {
		t.Error("no reading gives no headroom")
	}
}
