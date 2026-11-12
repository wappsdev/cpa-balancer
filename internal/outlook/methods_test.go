package outlook

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// threeWays is a pace measured three ways, peak the fastest and so the one used.
func threeWays() *Pace {
	return &Pace{PerHour: 2, Basis: "peak", Methods: []Method{{Name: "peak", PerHour: 2}, {Name: "trend", PerHour: 1}, {Name: "now", PerHour: 0.5}}}
}

// byMethod is the weekly outlook of two accounts with `measured` as the pace: A is active at 80%
// and resets in 100 h, B is at 90% and resets in 120 h.
func byMethod(t *testing.T, measured *Pace) Outlook {
	t.Helper()
	v, cfg := world(100, "a", seat{key: "a", util: 80, resets: 100, weight: 1}, seat{key: "b", util: 90, resets: 120, weight: 1})
	o, ok, err := weekly(v, cfg, measured, nil, now)
	if err != nil || !ok {
		t.Fatalf("two accounts or more combine: %v", err)
	}
	return o
}

func sameTime(a, b *time.Time) bool {
	return (a == nil) == (b == nil) && (a == nil || a.Equal(*b))
}

func TestEachMethodRunsTheWeekAtItsOwnPace(t *testing.T) {
	o := byMethod(t, threeWays())
	if len(o.ByMethod) != 3 {
		t.Fatalf("three methods, three weeks: %+v", o.ByMethod)
	}
	for i, want := range []string{"peak", "trend", "now"} {
		w := o.ByMethod[i]
		if w.Name != want || w.Used != (want == "peak") {
			t.Fatalf("%d: %+v", i, w)
		}
		// each is the week at that method's pace alone
		alone := byMethod(t, &Pace{PerHour: w.PerHour})
		if w.Verdict != alone.Verdict || w.Lasts != alone.Lasts || !sameTime(w.RunsOut, alone.RunsOut) {
			t.Fatalf("%s: %+v, alone %+v", w.Name, w, alone)
		}
		got, exhausts := handoffs(t, Outlook{Accounts: w.Accounts})
		want, wantExhausts := handoffs(t, alone)
		if exhausts != wantExhausts || got["a"] != want["a"] || got["b"] != want["b"] {
			t.Fatalf("%s: handoffs %v %q, alone %v %q", w.Name, got, exhausts, want, wantExhausts)
		}
	}
	// A's 20 points, then B's 10: at 2, 1 and 0.5 an hour A hands off at 10, 20 and 40 h, and the
	// room runs out on B 5, 10 and 20 h after that, each before a reset brings more
	for i, h := range []float64{15, 30, 60} {
		w := o.ByMethod[i]
		near(t, w.Name, w.RunsOut.Sub(now).Hours(), h, 1e-6)
		if w.Verdict != "out" || w.Dry == nil {
			t.Fatalf("%+v", w)
		}
	}
}

func TestTheUsedMethodIsTheOutlooksOwnNumbers(t *testing.T) {
	o := byMethod(t, threeWays())
	if len(o.ByMethod) == 0 {
		t.Fatal("no methods")
	}
	used := o.ByMethod[0]
	if !used.Used || used.PerHour != o.Pace.PerHour || used.Verdict != o.Verdict || used.Lasts != o.Lasts ||
		!sameTime(used.RunsOut, o.RunsOut) || used.Dry != o.Dry || len(used.Accounts) != len(o.Accounts) {
		t.Fatalf("used %+v, outlook %+v", used, o)
	}
	for i := range o.Accounts {
		if !sameTime(used.Accounts[i].At, o.Accounts[i].At) || !sameTime(used.Accounts[i].Capped, o.Accounts[i].Capped) || used.Accounts[i].Exhausts != o.Accounts[i].Exhausts || used.Accounts[i].Margin != o.Accounts[i].Margin {
			t.Fatalf("%d: %+v, outlook %+v", i, used.Accounts[i], o.Accounts[i])
		}
	}
}

func TestAnAbsentMethodIsListedWithItsReason(t *testing.T) {
	two := threeWays()
	two.Methods = []Method{two.Methods[0], two.Methods[2]}
	two.Absent = []Absent{{Name: "trend", Reason: "the last 24 h are not faster than the last 3 days"}}
	o := byMethod(t, two)
	if len(o.ByMethod) != 3 {
		t.Fatalf("every method has its place: %+v", o.ByMethod)
	}
	for i, want := range []string{"peak", "trend", "now"} {
		if o.ByMethod[i].Name != want {
			t.Fatalf("%d: %+v", i, o.ByMethod[i])
		}
	}
	if w := o.ByMethod[1]; w.Reason != "the last 24 h are not faster than the last 3 days" || w.Verdict != "" || w.PerHour != 0 || w.RunsOut != nil || w.Accounts != nil {
		t.Fatalf("an absent method has a reason and no week: %+v", w)
	}
	if o.ByMethod[2].Verdict == "" || o.ByMethod[2].Reason != "" {
		t.Fatalf("%+v", o.ByMethod[2])
	}
	// the week's own pace (7d): every method absent, each with its reason
	seven := &Pace{PerHour: 2, Basis: "7d", Absent: []Absent{{Name: "peak", Reason: "p"}, {Name: "trend", Reason: "t"}, {Name: "now", Reason: "n"}}}
	if o := byMethod(t, seven); len(o.ByMethod) != 3 || o.ByMethod[0].Reason != "p" || o.ByMethod[2].Reason != "n" {
		t.Fatalf("%+v", o.ByMethod)
	}
	// each window's average has no methods at all
	if o := byMethod(t, nil); o.ByMethod != nil {
		t.Fatalf("%+v", o.ByMethod)
	}
}

func TestEachMethodsWeekIsInTheJSON(t *testing.T) {
	b, err := json.Marshal(byMethod(t, threeWays()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"by_method":[{"name":"peak","per_hour":2,"used":true,"verdict":"out",`,
		`{"name":"trend","per_hour":1,"verdict":"out",`,
		`"runs_out":"2026-11-05T18:00:00Z"`,
		`"accounts":[{"key":"a","handoff":"2026-11-05T08:00:00Z","capped":"2026-11-05T08:00:00Z","margin":1},{"key":"b","handoff":"2026-11-05T18:00:00Z","capped":"2026-11-05T18:00:00Z","exhausts":true,"margin":1}]`,
	} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("%s not in %s", want, b)
		}
	}
	// an absent method is its name and its reason
	two := threeWays()
	two.Methods = two.Methods[:2]
	two.Absent = []Absent{{Name: "now", Reason: "less than 30 min of work in the last 5 h"}}
	b2, err := json.Marshal(byMethod(t, two))
	if err != nil {
		t.Fatal(err)
	}
	_, ways, _ := strings.Cut(string(b2), `"by_method":`)
	if want := `{"name":"now","reason":"less than 30 min of work in the last 5 h"}]`; !strings.Contains(ways, want) {
		t.Fatalf("%s not in %s", want, b2)
	}
	// the methods' weeks carry no pace of their own
	if strings.Count(string(b), `"pace"`) != 1 {
		t.Fatalf("%s", b)
	}
}

// fourWays is a pace measured four ways: typical is used, peak is the fastest.
func fourWays() *Pace {
	return &Pace{PerHour: 1.5, Basis: "typical", Methods: []Method{{Name: "peak", PerHour: 2}, {Name: "typical", PerHour: 1.5},
		{Name: "trend", PerHour: 1}, {Name: "now", PerHour: 0.5}}}
}

func TestTypicalSitsNextToPeakAndIsTheSumsOwnNumbers(t *testing.T) {
	o := byMethod(t, fourWays())
	var names []string
	for _, w := range o.ByMethod {
		names = append(names, w.Name)
	}
	if got := strings.Join(names, " "); got != "peak typical trend now" {
		t.Fatalf("%s", got)
	}
	typical := o.ByMethod[1]
	if !typical.Used || o.ByMethod[0].Used || typical.PerHour != o.Pace.PerHour || typical.Verdict != o.Verdict ||
		typical.Lasts != o.Lasts || !sameTime(typical.RunsOut, o.RunsOut) || typical.Dry != o.Dry {
		t.Fatalf("typical %+v, outlook %+v", typical, o)
	}
	for i := range o.Accounts {
		if !sameTime(typical.Accounts[i].At, o.Accounts[i].At) || typical.Accounts[i].Exhausts != o.Accounts[i].Exhausts {
			t.Fatalf("%d: %+v, outlook %+v", i, typical.Accounts[i], o.Accounts[i])
		}
	}
	// peak is the week at its own, faster pace
	alone := byMethod(t, &Pace{PerHour: 2})
	if peak := o.ByMethod[0]; peak.Verdict != alone.Verdict || !sameTime(peak.RunsOut, alone.RunsOut) {
		t.Fatalf("peak %+v, alone %+v", peak, alone)
	}
	// an absent typical keeps its place with its reason
	why := "fewer than 2 days with 2 h of work in the last 7 days"
	o = byMethod(t, &Pace{PerHour: 2, Basis: "peak", Methods: []Method{{Name: "peak", PerHour: 2}, {Name: "now", PerHour: 0.5}},
		Absent: []Absent{{Name: "typical", Reason: why}, {Name: "trend", Reason: "t"}}})
	if len(o.ByMethod) != 4 || o.ByMethod[1].Name != "typical" || o.ByMethod[1].Reason != why || !o.ByMethod[0].Used {
		t.Fatalf("%+v", o.ByMethod)
	}
}

func TestEachMethodsAccountsHaveTheirCappedAtThatPace(t *testing.T) {
	o := byMethod(t, fourWays())
	for _, w := range o.ByMethod {
		alone := byMethod(t, &Pace{PerHour: w.PerHour})
		for i, a := range w.Accounts {
			if a.Capped == nil || !sameTime(a.Capped, alone.Accounts[i].Capped) {
				t.Fatalf("%s %s: capped %v, alone %v", w.Name, a.Key, a.Capped, alone.Accounts[i].Capped)
			}
		}
	}
	// A's 20 points go in 13.3 h at 1.5 an hour (typical) and in 40 h at 0.5 (now)
	near(t, "typical a", cappedOf(Outlook{Accounts: o.ByMethod[1].Accounts})["a"], 20/1.5, 1e-6)
	near(t, "now a", cappedOf(Outlook{Accounts: o.ByMethod[3].Accounts})["a"], 40, 1e-6)
}
