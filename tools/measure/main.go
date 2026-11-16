// Command measure reads a history directory (read-only) and prints, as numbers only with the accounts named a0, a1, ..., how stable the factor of
// the meter is for several bases of use, whether models weigh differently, and rises of the meter outside the proxy. One-off: it was used
// to choose the basis in internal/history/meter.go; run it as `go run ./tools/measure <history dir>`.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type traffic struct {
	Requests int64 `json:"requests"`
	In       int64 `json:"input_tokens"`
	Out      int64 `json:"output_tokens"`
	CR       int64 `json:"cache_read_tokens"`
	CW       int64 `json:"cache_creation_tokens"`
}
type acct struct {
	Main        *float64           `json:"main"`
	Session     *float64           `json:"session"`
	SessionOpen bool               `json:"session_open"`
	Models      map[string]traffic `json:"models"`
}
type slot struct {
	T        time.Time        `json:"t"`
	Accounts map[string]*acct `json:"accounts"`
	Events   []struct {
		Kind, Account, Bucket string
	} `json:"events"`
}

func class(m string) string {
	for _, k := range []string{"opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(m, k) {
			return k
		}
	}
	return "other"
}

func main() {
	dir := os.Args[1]
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(files)
	var slots []*slot
	for _, f := range files {
		fh, _ := os.Open(f)
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		for sc.Scan() {
			var s slot
			if json.Unmarshal(sc.Bytes(), &s) == nil {
				slots = append(slots, &s)
			}
		}
		fh.Close()
	}
	sort.SliceStable(slots, func(i, j int) bool { return slots[i].T.Before(slots[j].T) })
	fmt.Println("slots", len(slots), "from", slots[0].T.Format("01-02"), "to", slots[len(slots)-1].T.Format("01-02"))
	names := map[string]bool{}
	has5h := map[string]bool{}
	for _, s := range slots {
		for n, a := range s.Accounts {
			names[n] = true
			if a.SessionOpen {
				has5h[n] = true
			}
		}
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	label := map[string]string{}
	for i, n := range sorted {
		label[n] = fmt.Sprintf("a%d", i)
		fmt.Printf("%s has5h=%v\n", label[n], has5h[n])
	}
	evCount := map[string]int{}
	for _, s := range slots {
		for _, e := range s.Events {
			evCount[e.Kind+"/"+e.Bucket]++
		}
	}
	fmt.Println("events", evCount)
	for _, n := range sorted {
		analyse(label[n], n, slots)
	}
	fit(slots, sorted, label)
	fit2(slots, sorted, label)
}

type interval struct {
	acct string
	win  int
	du   float64
	use  map[string]float64 // basis units (M) per class
	end  time.Time
}

func b(t traffic) float64 { return float64(t.In+t.Out+t.CW) + 0.015*float64(t.CR) }

// fit2: per-model weights over intervals between readings, never across a reset.
func fit2(slots []*slot, names []string, label map[string]string) {
	fmt.Println("===== fit2 per-model")
	cw0 := -1
	for i, s := range slots {
		for _, a := range s.Accounts {
			for _, t := range a.Models {
				if t.CW > 0 && cw0 < 0 {
					cw0 = i
				}
			}
		}
	}
	var ivs []interval
	for _, name := range names {
		if label[name] == "a0" {
			continue
		}
		win := 0
		var prev *float64
		cur := interval{acct: label[name], use: map[string]float64{}}
		for i := cw0; i < len(slots); i++ {
			a := slots[i].Accounts[name]
			if a == nil {
				continue
			}
			if a.Main != nil && prev != nil && *a.Main < *prev-10 {
				win++
				cur = interval{acct: label[name], win: win, use: map[string]float64{}}
				p := *a.Main
				prev = &p
				for m, t := range a.Models {
					cur.use[class(m)] += b(t) / 1e6
				}
				continue
			}
			for m, t := range a.Models {
				cur.use[class(m)] += b(t) / 1e6
			}
			if a.Main != nil {
				if prev == nil {
					p := *a.Main
					prev = &p
					cur.use = map[string]float64{}
					continue
				}
				if *a.Main-*prev >= 4 { // close an interval once the meter has moved 4 points
					cur.du = *a.Main - *prev
					cur.end = slots[i].T
					ivs = append(ivs, cur)
					p := *a.Main
					prev = &p
					cur = interval{acct: label[name], win: win, use: map[string]float64{}}
				}
			}
		}
	}
	fmt.Println("intervals", len(ivs))
	classes := []string{"opus", "sonnet", "fable", "haiku"}
	// mix variation
	var minS, maxS = 1.0, 0.0
	var fableIv int
	for _, iv := range ivs {
		tot := 0.0
		for _, v := range iv.use {
			tot += v
		}
		if tot > 0 {
			sh := iv.use["sonnet"] / tot
			minS, maxS = math.Min(minS, sh), math.Max(maxS, sh)
		}
		if iv.use["fable"] > 0.01 {
			fableIv++
		}
	}
	fmt.Printf("sonnet share of use per interval: min %.2f max %.2f; intervals with fable use: %d\n", minS, maxS, fableIv)
	// single factor vs per-model, leave-one-window-out
	keys := map[string]bool{}
	for _, iv := range ivs {
		keys[fmt.Sprintf("%s/%d", iv.acct, iv.win)] = true
	}
	var ks []string
	for k := range keys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	fmt.Println("windows", ks)
	for _, set := range [][]string{{"opus", "sonnet"}, {"opus", "sonnet", "fable"}} {
		var errSingle, errModel float64
		var n int
		for _, held := range ks {
			var train, test []interval
			for _, iv := range ivs {
				if fmt.Sprintf("%s/%d", iv.acct, iv.win) == held {
					test = append(test, iv)
				} else {
					train = append(train, iv)
				}
			}
			tot := func(iv interval) float64 {
				var t float64
				for _, v := range iv.use {
					t += v
				}
				return t
			}
			one := solve(train, []string{"all"}, func(s interval, _ string) float64 { return tot(s) }, func(s interval) float64 { return s.du })
			multi := solve(train, set, func(s interval, c string) float64 { return s.use[c] }, func(s interval) float64 { return s.du })
			for _, iv := range test {
				p1 := one["all"] * tot(iv)
				var p2 float64
				for _, c := range set {
					p2 += multi[c] * iv.use[c]
				}
				// the other classes (haiku etc.) priced at the mean of the set
				errSingle += (p1 - iv.du) * (p1 - iv.du)
				errModel += (p2 - iv.du) * (p2 - iv.du)
				n++
			}
		}
		fmt.Printf("held-out RMSE (points per interval) classes %v: single %.3f, per-model %.3f over %d intervals\n", set, math.Sqrt(errSingle/float64(n)), math.Sqrt(errModel/float64(n)), n)
	}
	all := func(s interval, c string) float64 { return s.use[c] }
	w := solve(ivs, []string{"opus", "sonnet"}, all, func(s interval) float64 { return s.du })
	w2 := solve(ivs, []string{"opus", "sonnet", "fable"}, all, func(s interval) float64 { return s.du })
	fmt.Println("all-data weights (pts/M units) opus,sonnet:", w, " with fable:", w2)
	_ = classes
	// per account
	for _, acc := range []string{"a1", "a2"} {
		var sub []interval
		for _, iv := range ivs {
			if iv.acct == acc {
				sub = append(sub, iv)
			}
		}
		fmt.Println(acc, "opus,sonnet:", solve(sub, []string{"opus", "sonnet"}, all, func(s interval) float64 { return s.du }), "n", len(sub))
	}
}

type span struct {
	acct         string
	util         float64
	in, out      float64
	cw, cr       float64
	byClass      map[string]float64 // in+out per class
	startT, endT time.Time
}

// fit builds cumulative spans from the start of each window that opened after cache writes were
// recorded, on the two accounts with no 5-hour limit, and looks for the basis with the tightest spread.
func fit(slots []*slot, names []string, label map[string]string) {
	cw0 := -1
	for i, s := range slots {
		for _, a := range s.Accounts {
			for _, t := range a.Models {
				if t.CW > 0 && cw0 < 0 {
					cw0 = i
				}
			}
		}
	}
	fmt.Println("===== fit; first cache write slot", slots[cw0].T.Format("01-02 15:04"))
	var spans []span
	for _, name := range names {
		if label[name] == "a0" {
			continue
		}
		var prev *float64
		start := -1
		for i, s := range slots {
			a := s.Accounts[name]
			if a == nil || a.Main == nil {
				continue
			}
			if prev != nil && *a.Main < *prev-10 {
				start = i
			}
			prev = a.Main
		}
		_ = start
		// walk windows
		var starts []int
		prev = nil
		for i, s := range slots {
			a := s.Accounts[name]
			if a == nil || a.Main == nil {
				continue
			}
			if prev != nil && *a.Main < *prev-10 {
				starts = append(starts, i)
			}
			p := *a.Main
			prev = &p
		}
		for k, st := range starts {
			end := len(slots)
			if k+1 < len(starts) {
				end = starts[k+1]
			}
			if st < cw0 {
				continue
			}
			sp := span{acct: label[name], byClass: map[string]float64{}}
			for i := st; i < end; i++ {
				a := slots[i].Accounts[name]
				if a == nil {
					continue
				}
				for m, t := range a.Models {
					sp.in += float64(t.In)
					sp.out += float64(t.Out)
					sp.cw += float64(t.CW)
					sp.cr += float64(t.CR)
					sp.byClass[class(m)] += float64(t.In + t.Out)
				}
				if a.Main != nil && (i-st)%72 == 71 { // every 6 hours
					c := sp
					c.util = *a.Main
					c.byClass = map[string]float64{}
					for k, v := range sp.byClass {
						c.byClass[k] = v
					}
					c.endT = slots[i].T
					if c.util >= 10 {
						spans = append(spans, c)
					}
				}
			}
		}
	}
	fmt.Println("spans", len(spans))
	counts := map[string]int{}
	for _, sp := range spans {
		counts[sp.acct]++
	}
	fmt.Println(counts)
	cov := func(f func(span) float64, filter func(span) bool) (float64, float64, int) {
		var xs []float64
		for _, sp := range spans {
			if filter(sp) {
				xs = append(xs, sp.util/(f(sp)/1e6))
			}
		}
		var m float64
		for _, v := range xs {
			m += v
		}
		m /= float64(len(xs))
		var ss float64
		for _, v := range xs {
			ss += (v - m) * (v - m)
		}
		return m, math.Sqrt(ss/float64(len(xs)-1)) / m, len(xs)
	}
	all := func(span) bool { return true }
	for _, acc := range []string{"a1", "a2", ""} {
		filter := func(sp span) bool { return acc == "" || sp.acct == acc }
		for _, c := range []struct {
			name string
			f    func(span) float64
		}{
			{"in+out", func(s span) float64 { return s.in + s.out }},
			{"in+out+cw", func(s span) float64 { return s.in + s.out + s.cw }},
			{"in+out+0.25cw", func(s span) float64 { return s.in + s.out + 0.25*s.cw }},
			{"in+out+0.0625cw", func(s span) float64 { return s.in + s.out + 0.0625*s.cw }},
		} {
			m, cv, n := cov(c.f, filter)
			fmt.Printf("%-3s %-16s n=%d mean=%.3f cov=%.3f\n", acc, c.name, n, m, cv)
		}
	}
	_ = all
	// grid fit for alpha (cw) and beta (cr), both accounts
	bestA, bestB, best := 0.0, 0.0, 9.9
	for a := 0.0; a <= 1.5; a += 0.05 {
		for b := 0.0; b <= 0.3; b += 0.005 {
			_, cv, _ := cov(func(s span) float64 { return s.in + s.out + a*s.cw + b*s.cr }, func(span) bool { return true })
			if cv < best {
				bestA, bestB, best = a, b, cv
			}
		}
	}
	fmt.Printf("grid best: cw weight %.2f cr weight %.3f cov=%.3f\n", bestA, bestB, best)
	for _, acc := range []string{"a1", "a2"} {
		_, cv, n := cov(func(s span) float64 { return s.in + s.out + bestA*s.cw + bestB*s.cr }, func(sp span) bool { return sp.acct == acc })
		fmt.Printf("  %s with grid best: n=%d cov=%.3f\n", acc, n, cv)
	}
	// only cr weight, cw = 0
	bestB2, best2 := 0.0, 9.9
	for b := 0.0; b <= 0.3; b += 0.0025 {
		_, cv, _ := cov(func(s span) float64 { return s.in + s.out + b*s.cr }, func(span) bool { return true })
		if cv < best2 {
			bestB2, best2 = b, cv
		}
	}
	fmt.Printf("cw=0, best cr weight %.4f cov=%.3f\n", bestB2, best2)
	for f := 0.008; f <= 0.0301; f += 0.002 {
		_, cv, _ := cov(func(s span) float64 { return s.in + s.out + s.cw + f*s.cr }, all)
		fmt.Printf("cw=1 cr=%.3f cov=%.3f\n", f, cv)
	}
	for _, name := range names {
		if label[name] == "a0" {
			continue
		}
		// outside the proxy: rises with no proxy tokens within 6 slots either side of the step
		tok := func(i int) float64 {
			if i < 0 || i >= len(slots) {
				return 0
			}
			var u float64
			if a := slots[i].Accounts[name]; a != nil {
				for _, t := range a.Models {
					u += float64(t.In + t.Out + t.CW)
				}
			}
			return u
		}
		var prevI = -1
		var prevV float64
		var pts, steps, tot float64
		var days = map[string]float64{}
		for i, s := range slots {
			a := s.Accounts[name]
			if a == nil || a.Main == nil {
				continue
			}
			if prevI >= 0 && *a.Main > prevV && *a.Main-prevV < 50 {
				d := *a.Main - prevV
				tot += d
				var near float64
				for j := prevI - 12; j <= i+12; j++ {
					near += tok(j)
				}
				if near == 0 {
					pts += d
					steps++
					days[s.T.Format("01-02")] += d
					fmt.Printf("  step %s %s +%.0f (prev %s)\n", label[name], s.T.Format("01-02 15:04"), d, slots[prevI].T.Format("01-02 15:04"))
				}
			}
			prevI, prevV = i, *a.Main
		}
		fmt.Printf("%s outside-the-proxy (no proxy tokens within 1h either side): %.0f of %.0f rise points, %.0f steps; days %v\n", label[name], pts, tot, steps, days)
	}
	for _, acc := range []string{"a1", "a2", ""} {
		filter := func(sp span) bool { return acc == "" || sp.acct == acc }
		m, cv, n := cov(func(s span) float64 { return s.in + 5*s.out + 1.25*s.cw + 0.1*s.cr }, filter)
		fmt.Printf("%-3s price-weighted(in+5out+1.25cw+0.1cr) n=%d mean=%.4f cov=%.3f\n", acc, n, m, cv)
		m, cv, n = cov(func(s span) float64 { return s.in + s.out + s.cw + 0.005*s.cr }, filter)
		fmt.Printf("%-3s (c) in+out+cw+0.005cr n=%d mean=%.4f cov=%.3f\n", acc, n, m, cv)
		m, cv, n = cov(func(s span) float64 { return s.in + s.out + s.cw + 0.01*s.cr }, filter)
		fmt.Printf("%-3s (c) in+out+cw+0.01cr n=%d mean=%.4f cov=%.3f\n", acc, n, m, cv)
		m, cv, n = cov(func(s span) float64 { return s.in + s.out + s.cw + 0.001*s.cr }, filter)
		fmt.Printf("%-3s (c) in+out+cw+0.001cr n=%d mean=%.4f cov=%.3f\n", acc, n, m, cv)
	}
	bo, ba, bb, bc := 0.0, 0.0, 0.0, 9.9
	for o := 0.0; o <= 8; o += 0.5 {
		for a := 0.0; a <= 2; a += 0.05 {
			for b := 0.0; b <= 0.2; b += 0.005 {
				_, cv, _ := cov(func(s span) float64 { return s.in + o*s.out + a*s.cw + b*s.cr }, all)
				if cv < bc {
					bo, ba, bb, bc = o, a, b, cv
				}
			}
		}
	}
	fmt.Printf("free fit: out %.1f cw %.2f cr %.3f cov=%.3f\n", bo, ba, bb, bc)
	// per class: spans where one class is >= 80% of in+out
	for _, cl := range []string{"opus", "sonnet", "fable"} {
		var xs []float64
		for _, sp := range spans {
			tot := sp.in + sp.out
			if tot > 0 && sp.byClass[cl]/tot >= 0.8 {
				xs = append(xs, sp.util/(tot/1e6))
			}
		}
		var m float64
		for _, v := range xs {
			m += v
		}
		if len(xs) > 0 {
			m /= float64(len(xs))
		}
		fmt.Printf("class %s dominant: n=%d mean factor (in+out)=%.3f\n", cl, len(xs), m)
	}
}

type win struct {
	start, end int // slot indexes (start = slot of the drop, inclusive)
	util       float64
}

func analyse(label, name string, slots []*slot) {
	fmt.Println("=====", label)
	type pt struct {
		i int
		v float64
	}
	var pts []pt
	for i, s := range slots {
		if a := s.Accounts[name]; a != nil && a.Main != nil {
			pts = append(pts, pt{i, *a.Main})
		}
	}
	fmt.Println("readings", len(pts))
	open7, all7 := 0, 0
	for _, s := range slots[len(slots)-2016:] {
		if a := s.Accounts[name]; a != nil {
			all7++
			if a.SessionOpen {
				open7++
			}
		}
	}
	fmt.Println("session open slots in last 7d:", open7, "of", all7)
	// drops
	var drops []int // slot index of first reading after the drop
	for k := 1; k < len(pts); k++ {
		if pts[k].v < pts[k-1].v-3 {
			drops = append(drops, pts[k].i)
		}
	}
	fmt.Println("drops", len(drops))
	var gaps []float64
	for k := 1; k < len(drops); k++ {
		gaps = append(gaps, slots[drops[k]].T.Sub(slots[drops[k-1]].T).Hours())
	}
	sort.Float64s(gaps)
	fmt.Printf("gap hours between drops: %.1f\n", gaps)
	// drop sizes
	for k := 1; k < len(pts); k++ {
		if pts[k].v < pts[k-1].v-3 {
			fmt.Printf("drop %.1f -> %.1f at %s\n", pts[k-1].v, pts[k].v, slots[pts[k].i].T.Format("01-02 15:04"))
		}
	}
	// windows between drops (also the first partial one and last, current)
	bounds := append([]int{}, drops...)
	var wins []win
	for k := 0; k < len(bounds); k++ {
		end := len(slots)
		if k+1 < len(bounds) {
			end = bounds[k+1]
		}
		wins = append(wins, win{start: bounds[k], end: end})
	}
	type basisFn func(t traffic) float64
	bases := []struct {
		name string
		fn   basisFn
	}{
		{"in+out", func(t traffic) float64 { return float64(t.In + t.Out) }},
		{"in+out+cw", func(t traffic) float64 { return float64(t.In + t.Out + t.CW) }},
	}
	usage := func(from, to int, fn basisFn, onlyClass string) float64 {
		var u float64
		for i := from; i < to; i++ {
			if a := slots[i].Accounts[name]; a != nil {
				for m, t := range a.Models {
					if onlyClass == "" || class(m) == onlyClass {
						u += fn(t)
					}
				}
			}
		}
		return u
	}
	lastUtil := func(from, to int) (float64, bool) {
		for i := to - 1; i >= from; i-- {
			if a := slots[i].Accounts[name]; a != nil && a.Main != nil {
				return *a.Main, true
			}
		}
		return 0, false
	}
	// per window factor: util_end / usage (start slot included or not)
	report := func(label string, fn basisFn) []float64 {
		var fs []float64
		for _, w := range wins {
			u, ok := lastUtil(w.start, w.end)
			use := usage(w.start, w.end, fn, "")
			if !ok || use == 0 || u < 5 {
				continue
			}
			fs = append(fs, u/(use/1e6))
		}
		return fs
	}
	stat := func(x []float64) (mean, cov float64) {
		if len(x) == 0 {
			return 0, 0
		}
		for _, v := range x {
			mean += v
		}
		mean /= float64(len(x))
		var ss float64
		for _, v := range x {
			ss += (v - mean) * (v - mean)
		}
		if len(x) > 1 {
			cov = math.Sqrt(ss/float64(len(x)-1)) / mean
		}
		return
	}
	for _, b := range bases {
		fs := report(b.name, b.fn)
		m, c := stat(fs)
		fmt.Printf("basis %-10s windows=%d mean=%.3f pts/M cov=%.3f vals=%.2f\n", b.name, len(fs), m, c, fs)
	}
	// (c) cache reads fraction fit
	bestF, bestCov := 0.0, 9.9
	for f := 0.0; f <= 0.5001; f += 0.005 {
		fn := func(t traffic) float64 { return float64(t.In+t.Out+t.CW) + f*float64(t.CR) }
		_, c := stat(report("c", fn))
		if c < bestCov {
			bestF, bestCov = f, c
		}
	}
	fn := func(t traffic) float64 { return float64(t.In+t.Out+t.CW) + bestF*float64(t.CR) }
	fs := report("c", fn)
	m, c := stat(fs)
	fmt.Printf("basis c: in+out+cw+%.3f*cr windows=%d mean=%.3f cov=%.3f vals=%.2f\n", bestF, len(fs), m, c, fs)

	// sub-spans of windows: within each window, the factor at 1/3 and 2/3 of its length vs the end
	for _, b := range append(bases, struct {
		name string
		fn   basisFn
	}{"c", fn}) {
		var all []float64
		for _, w := range wins {
			if w.end-w.start < 300 {
				continue
			}
			for _, frac := range []float64{1.0 / 3, 2.0 / 3, 1} {
				e := w.start + int(float64(w.end-w.start)*frac)
				u, ok := lastUtil(w.start, e)
				use := usage(w.start, e, b.fn, "")
				if ok && use > 0 && u >= 5 {
					all = append(all, u/(use/1e6))
				}
			}
		}
		mm, cc := stat(all)
		fmt.Printf("subspan %-10s n=%d mean=%.3f cov=%.3f\n", b.name, len(all), mm, cc)
	}
	// per class: share of tokens (in+out) by class over the history
	tot := map[string]float64{}
	for i := range slots {
		if a := slots[i].Accounts[name]; a != nil {
			for m, t := range a.Models {
				tot[class(m)] += float64(t.In + t.Out)
			}
		}
	}
	for _, cl := range []string{"opus", "sonnet", "haiku", "fable", "other"} {
		fmt.Printf("  in+out %s=%.0f\n", cl, tot[cl])
	}
	firstCW := -1
	for i := range slots {
		if a := slots[i].Accounts[name]; a != nil {
			for _, t := range a.Models {
				if t.CW > 0 || t.CR > 0 {
					firstCW = i
				}
			}
		}
		if firstCW >= 0 {
			break
		}
	}
	if firstCW >= 0 {
		fmt.Println("cache fields first recorded", slots[firstCW].T.Format("01-02 15:04"))
	}
	// regression per class over daily increments of util: dutil = sum_c k_c * tokens_c(day)
	classes := []string{"opus", "sonnet", "haiku", "fable", "other"}
	// build per-day samples inside windows (no drop day)
	type samp struct {
		du  float64
		use map[string]float64
	}
	var samples []samp
	day := 288 // slots per day
	for _, w := range wins {
		for s0 := w.start; s0+day <= w.end; s0 += day {
			u0, ok0 := lastUtil(w.start, s0+1)
			u1, ok1 := lastUtil(w.start, s0+day)
			if !ok0 || !ok1 {
				continue
			}
			sp := samp{du: u1 - u0, use: map[string]float64{}}
			for _, cl := range classes {
				sp.use[cl] = usage(s0+1, s0+day+1, bases[1].fn, cl) / 1e6
			}
			samples = append(samples, sp)
		}
	}
	fmt.Println("daily samples", len(samples))
	// least squares for opus and sonnet (+ fable) only if present
	var use []string
	for _, cl := range classes {
		if tot[cl] > 2e5 {
			use = append(use, cl)
		}
	}
	fmt.Println("classes in fit", use)
	if len(use) > 0 && len(samples) > len(use) {
		coef := solve(samples, use, func(s samp, cl string) float64 { return s.use[cl] }, func(s samp) float64 { return s.du })
		fmt.Printf("per-class pts/M (in+out+cw): %v\n", coef)
	}
	// outside the proxy: rises in slots with zero proxy tokens in this slot and the previous one
	var outside, total float64
	var outsideSlots int
	for k := 1; k < len(pts); k++ {
		d := pts[k].v - pts[k-1].v
		if d <= 0 {
			continue
		}
		total += d
		i := pts[k].i
		use := usage(pts[k-1].i+1, i+1, bases[1].fn, "")
		if use == 0 {
			outside += d
			outsideSlots++
		}
	}
	fmt.Printf("rises total=%.1f pts; with zero proxy tokens between the two readings=%.1f pts in %d steps\n", total, outside, outsideSlots)
	// current window
	if len(wins) > 0 {
		w := wins[len(wins)-1]
		u, _ := lastUtil(w.start, w.end)
		fmt.Printf("current window start %s util %.1f in+out=%.0f in+out+cw=%.0f cr=%.0f\n", slots[w.start].T.Format("01-02 15:04"), u,
			usage(w.start, w.end, bases[0].fn, ""), usage(w.start, w.end, bases[1].fn, ""), usage(w.start, w.end, func(t traffic) float64 { return float64(t.CR) }, ""))
	}
}

// solve is ordinary least squares without an intercept by normal equations.
func solve[S any](samples []S, cls []string, x func(S, string) float64, y func(S) float64) map[string]float64 {
	n := len(cls)
	a := make([][]float64, n)
	for i := range a {
		a[i] = make([]float64, n+1)
	}
	for _, s := range samples {
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				a[i][j] += x(s, cls[i]) * x(s, cls[j])
			}
			a[i][n] += x(s, cls[i]) * y(s)
		}
	}
	for i := 0; i < n; i++ {
		p := i
		for r := i + 1; r < n; r++ {
			if math.Abs(a[r][i]) > math.Abs(a[p][i]) {
				p = r
			}
		}
		a[i], a[p] = a[p], a[i]
		if math.Abs(a[i][i]) < 1e-12 {
			continue
		}
		for r := 0; r < n; r++ {
			if r != i {
				f := a[r][i] / a[i][i]
				for c := i; c <= n; c++ {
					a[r][c] -= f * a[i][c]
				}
			}
		}
	}
	out := map[string]float64{}
	for i := 0; i < n; i++ {
		if math.Abs(a[i][i]) > 1e-12 {
			out[cls[i]] = math.Round(a[i][n]/a[i][i]*1000) / 1000
		}
	}
	return out
}
