package quota

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time { t := now.Add(d); return &t }

func header(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func reset(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }

func TestFromHeadersSplitsBuckets(t *testing.T) {
	h := header(
		"Anthropic-Ratelimit-Unified-7d-Utilization", "0.35", "Anthropic-Ratelimit-Unified-7d-Reset", reset(100*time.Hour),
		"Anthropic-Ratelimit-Unified-5h-Utilization", "0.07", "Anthropic-Ratelimit-Unified-5h-Reset", reset(3*time.Hour),
	)
	main, fable := []string{"claude-opus-5-5"}, []string{"claude-fable-5-1"}
	out := FromHeaders(h, "claude-opus-5-5", main, fable, now)
	if int(out[Main].Util+0.5) != 35 || int(out[Session].Util+0.5) != 7 || !out[Main].ResetsAt.Equal(*at(100 * time.Hour)) {
		t.Fatalf("main model: %+v", out)
	}
	// The unified 7d header is the all-models weekly window on a Fable request too (Fable use
	// counts toward it); Fable's own weekly limit comes only from the usage endpoint.
	fableOut := FromHeaders(h, "claude-fable-5-1", main, fable, now)
	if _, ok := fableOut[Fable]; ok || int(fableOut[Main].Util+0.5) != 35 {
		t.Fatalf("fable model must fill the all-models window, not Fable's: %+v", fableOut)
	}
	other := FromHeaders(h, "claude-haiku-4-5-20251001", main, fable, now)
	if _, ok := other[Main]; ok || len(other) != 1 {
		t.Fatalf("unknown model must only update the session window: %+v", other)
	}
	if out[Main].Source != "header" {
		t.Fatal("source")
	}
}

const probeBody = `{"limits":[
 {"kind":"session","percent":8,"resets_at":"2026-10-03T17:59:59.747700+00:00"},
 {"kind":"weekly_all","percent":58,"resets_at":"2026-10-09T00:59:59.772845+00:00"},
 {"kind":"weekly_scoped","percent":1,"resets_at":"2026-10-09T00:59:59+00:00","scope":{"model":{"id":null,"display_name":"Fable"}}},
 {"kind":"weekly_scoped","percent":40,"resets_at":"2026-10-09T00:59:59+00:00","scope":{"model":{"display_name":"Other"}}}]}`

func TestFromProbe(t *testing.T) {
	out, fiveHour, err := FromProbe([]byte(probeBody), now)
	if err != nil || !fiveHour {
		t.Fatal(err, fiveHour)
	}
	if len(out) != 3 || out[Main].Util != 58 || out[Fable].Util != 1 || out[Session].Util != 8 || out[Main].Source != "probe" {
		t.Fatalf("%+v", out)
	}
	closed, fiveHour, _ := FromProbe([]byte(`{"limits":[{"kind":"session","percent":0,"resets_at":null}]}`), now)
	if !fiveHour {
		t.Fatal("a closed 5-hour window is still a 5-hour limit")
	}
	if r, ok := closed[Session]; !ok || r.ResetsAt != nil || r.Running(now) {
		t.Fatalf("closed session: %+v", closed)
	}
}

func TestMergeKeepsNewest(t *testing.T) {
	old := Readings{Main: {Util: 10, ResetsAt: at(time.Hour), ObservedAt: now.Add(-30 * time.Minute)}}
	newer := Readings{Main: {Util: 20, ResetsAt: at(time.Hour), ObservedAt: now.Add(-5 * time.Minute)}, Fable: {Util: 1, ObservedAt: now}}
	out := Merge(old, newer)
	if out[Main].Util != 20 || out[Fable].Util != 1 {
		t.Fatalf("%+v", out)
	}
	tie := Merge(Readings{Session: {Util: 0, ObservedAt: now, Source: "probe"}}, Readings{Session: {Util: 1, ObservedAt: now, Source: "anchor"}})
	if tie[Session].Source != "anchor" {
		t.Fatalf("later source must win a tie: %+v", tie)
	}
}

func TestRollForward(t *testing.T) {
	in := Readings{
		Main:    {Util: 80, ResetsAt: at(-time.Hour), ObservedAt: now.Add(-48 * time.Hour), Source: "probe"},
		Session: {Util: 40, ResetsAt: at(-time.Minute), ObservedAt: now.Add(-2 * time.Hour), Source: "header"},
		Fable:   {Util: 5, ResetsAt: at(time.Hour), ObservedAt: now, Source: "probe"},
	}
	out := RollForward(in, now)
	if out[Main].Util != 0 || out[Main].Source != "estimate" || !out[Main].ResetsAt.Equal(now.Add(-time.Hour).Add(week)) {
		t.Fatalf("weekly: %+v", out[Main])
	}
	if out[Session].ResetsAt != nil || out[Session].Util != 0 || out[Session].Source != "estimate" {
		t.Fatalf("session should be closed: %+v", out[Session])
	}
	if out[Fable].Util != 5 {
		t.Fatal("running window must be kept")
	}
	closed := Readings{Session: {ObservedAt: now.Add(-3 * time.Minute), Source: "probe"}}
	if RollForward(closed, now)[Session].Source != "probe" {
		t.Fatal("closed reading must be kept as is")
	}
}

func TestFromProbeWithoutAFiveHourLimit(t *testing.T) {
	// some plans have no 5-hour limit: the usage endpoint sends no session entry at all
	out, fiveHour, err := FromProbe([]byte(`{"limits":[{"kind":"weekly_all","percent":66,"resets_at":"2026-11-06T00:59:59Z"}]}`), now)
	if err != nil || fiveHour {
		t.Fatalf("fiveHour %v, err %v", fiveHour, err)
	}
	if _, ok := out[Session]; ok || out[Main].Util != 66 {
		t.Fatalf("%+v", out)
	}
}
