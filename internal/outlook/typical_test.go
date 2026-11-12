package outlook

import (
	"strings"
	"testing"
	"time"
)

// daily is a week of history in steps of one day ending now for one account "a" of weight 1:
// rises[k] points in the day that ends k days before now. Each 24 h window peak looks at is then
// one step, so its pace is rises[k]/24, and a day with any rise has 24 h of work in it.
func daily(rises ...float64) Series {
	s := Series{Step: 86400, Main: map[string][]*float64{}}
	var v float64
	for k := 7; k >= 0; k-- {
		if k < 7 && k < len(rises) {
			v += rises[k]
		}
		x := v
		s.T = append(s.T, now.Add(-time.Duration(k)*day).Unix())
		s.Main["a"] = append(s.Main["a"], &x)
	}
	return s
}

func TestTypicalIsTheP75OfThe24hWindows(t *testing.T) {
	// the 7 windows' paces 3, 1, 4, 1, 5, 9, 2: sorted 1 1 2 3 4 5 9, rank 0.75 × 6 = 4.5, so
	// halfway from 4 to 5
	p := MeasurePace(daily(72, 24, 96, 24, 120, 216, 48), one, now)
	m := methods(p)
	if _, ok := m["typical"]; !ok {
		t.Fatalf("typical is present: %+v %+v", p.Methods, p.Absent)
	}
	near(t, "typical", m["typical"].PerHour, 4.5, 1e-9)
	near(t, "peak", m["peak"].PerHour, 9, 1e-9)
	// a window without 2 h of work does not count: 3 1 4 5 9 2 without the idle day, sorted
	// 1 2 3 4 5 9, rank 0.75 × 5 = 3.75
	p = MeasurePace(daily(72, 24, 96, 0, 120, 216, 48), one, now)
	near(t, "typical without the idle day", methods(p)["typical"].PerHour, 4.75, 1e-9)
	// like trend, it measures no worked time of its own
	if typical := methods(p)["typical"]; typical.ActivePerHour != 0 || typical.HoursPerDay != 0 {
		t.Fatalf("%+v", typical)
	}
}

func TestTypicalIsAbsentWithFewerThanTwoWindows(t *testing.T) {
	// 2 h of work ending now: only the 24 h ending now holds 2 h of it
	p := MeasurePace(weekOf(work{0, 3, 1}), one, now)
	if _, ok := methods(p)["typical"]; ok {
		t.Fatalf("%+v", p.Methods)
	}
	if got := absent(p)["typical"]; got != "fewer than 2 windows of 24 h with 2 h of work in the last 7 days" {
		t.Fatalf("%+v", p.Absent)
	}
	if _, ok := methods(p)["peak"]; !ok {
		t.Fatalf("one window is enough for peak: %+v", p.Methods)
	}
}

func TestTheSumUsesTypical(t *testing.T) {
	// the spiky day of TestPeakIsTheFastestOnASpikyDay: peak is the fastest, but the pace is typical's
	p := MeasurePace(weekOf(work{1, 2, 1}, work{20, 23, 1}, work{68, 71, 0.5}, work{116, 119, 0.5}, work{154, 161, 10}), one, now)
	m := methods(p)
	if p.Basis != "typical" || p.PerHour != m["typical"].PerHour || m["typical"].PerHour >= m["peak"].PerHour {
		t.Fatalf("basis %s, %+v", p.Basis, p.Methods)
	}
	if p.ActivePerHour != 0 || p.HoursPerDay != 0 {
		t.Fatalf("%+v", p)
	}
	// the methods in their order, typical next to peak
	var names []string
	for _, m := range p.Methods {
		names = append(names, m.Name)
	}
	if got := strings.Join(names, " "); got != "peak typical trend now" {
		t.Fatalf("%s", got)
	}
}

func TestWithoutTypicalTheSumUsesTheFastest(t *testing.T) {
	// 2 h of work ending now (typical absent): peak and now are 4/24, trend 1.5 × 4/24 − 0.5 × 4/72
	p := MeasurePace(weekOf(work{0, 3, 1}), one, now)
	m := methods(p)
	near(t, "trend", m["trend"].PerHour, 1.5*4.0/24-0.5*4.0/72, 1e-9)
	near(t, "peak", m["peak"].PerHour, 4.0/24, 1e-9)
	if p.Basis != "trend" || p.PerHour != m["trend"].PerHour {
		t.Fatalf("basis %s, %+v", p.Basis, p.Methods)
	}
}
