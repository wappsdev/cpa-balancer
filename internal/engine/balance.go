package engine

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/policy"
	"github.com/wappsdev/cpa-balancer/internal/quota"
)

// loadsTTL is how long the per-project load stays before Tick reads the history again.
const loadsTTL = 5 * time.Minute

// tie is the difference below which two scores are the same: equal accounts must tie exactly.
const tie = 1e-9

// readLoads is each project's tokens over the last 7 days, summed from the history's root
// sessions and the project folder each one belongs to. It reads the disk, so callers hold no lock.
// ok is false when there is no history to read.
func (e *Engine) readLoads(now time.Time) (loads map[string]int64, ok bool) {
	if e.rec == nil {
		return nil, false
	}
	loads = map[string]int64{}
	for _, root := range e.rec.Sessions(now).Week.AllRoots {
		if folder, found := e.projects.Folder(root.ID); found {
			loads[folder] += root.Total.Tokens()
		}
	}
	return loads, true
}

// refreshLoads keeps the load Pick places projects by; it reads outside the lock.
func (e *Engine) refreshLoads(now time.Time) {
	loads, ok := e.readLoads(now)
	if !ok {
		return
	}
	e.mu.Lock()
	e.loads, e.loadsAt = loads, now
	e.mu.Unlock()
}

func (e *Engine) loadsDue(now time.Time) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return now.Sub(e.loadsAt) >= loadsTTL
}

// tenant is a project the balancer counts as load: where it lives and how much it weighs.
type tenant struct {
	project, account string
	weight           float64
	pinned           bool // its folder is pinned: the account is the pin's, and a rebalance leaves it
}

// tenants are the projects with a home and the folder-pinned projects with recent use (a pinned
// project gets no home while its pin has room), each on the account that carries it. A project's
// weight is its tokens over the last 7 days; one with none recorded weighs what the average used
// project does, or 1 when no project has used anything, so a project just placed is not free.
// Callers hold e.mu. skip names a project to leave out, the one being placed.
func (e *Engine) tenants(skip string) (out []tenant, mean float64) {
	a := &e.st.Affinity
	add := func(project, account string, pinned bool) {
		if project != skip {
			out = append(out, tenant{project: project, account: account, pinned: pinned})
		}
	}
	for project, h := range a.Homes {
		if pin := pinned(e.cfg.Affinity.FolderPins, a.FolderPins, project); pin != "" {
			add(project, pin, true)
		} else {
			add(project, h.Account, false)
		}
	}
	for project := range e.loads {
		if _, homed := a.Homes[project]; !homed {
			if pin := pinned(e.cfg.Affinity.FolderPins, a.FolderPins, project); pin != "" {
				add(project, pin, true)
			}
		}
	}
	var sum float64
	used := 0
	for _, t := range out {
		if tokens := e.loads[t.project]; tokens > 0 {
			sum += float64(tokens)
			used++
		}
	}
	mean = 1
	if used > 0 {
		mean = sum / float64(used)
	}
	for i := range out {
		out[i].weight = e.weightOf(out[i].project, mean)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].project < out[j].project })
	return out, mean
}

func (e *Engine) weightOf(project string, mean float64) float64 {
	if tokens := e.loads[project]; tokens > 0 {
		return float64(tokens)
	}
	return mean
}

// balancer scores accounts for a project about to be placed. An account's score is its share of the
// room left under the caps (headroom, in Pro plans' points, so a plan's size counts) minus its share
// of the load once the project is on it: the account furthest below its fair share wins. Both are
// shares of the candidates' total, so percent and tokens need no common unit.
type balancer struct {
	accounts []string
	share    map[string]float64
	load     map[string]float64
	rank     func(string) int
}

func (e *Engine) newBalancer(accounts []string, now time.Time) *balancer {
	b := &balancer{accounts: accounts, share: map[string]float64{}, load: map[string]float64{}, rank: e.ranker(quota.Main)}
	var room float64
	for _, a := range accounts {
		room += e.headroom(a, quota.Main, now)
	}
	for _, a := range accounts {
		if room > 0 {
			b.share[a] = e.headroom(a, quota.Main, now) / room
		} else {
			b.share[a] = 1 / float64(len(accounts))
		}
	}
	return b
}

// pick is the account for a project of the given weight when the load of every project counted
// comes to total (the project itself included). A tie goes to prefer, then to the existing order.
func (b *balancer) pick(weight, total float64, prefer string) string {
	best, bestScore := "", 0.0
	for _, a := range b.accounts {
		score := b.share[a] - (b.load[a]+weight)/total
		switch {
		case best == "" || score > bestScore+tie:
		case math.Abs(score-bestScore) <= tie && b.beats(a, best, prefer):
		default:
			continue
		}
		best, bestScore = a, score
	}
	return best
}

func (b *balancer) beats(a, than, prefer string) bool {
	if a == prefer || than == prefer {
		return a == prefer
	}
	return b.rank(a) < b.rank(than)
}

// balanced is the account for a project that is new or has to move: the best of the usable
// accounts by headroom against the load already homed there. Callers hold e.mu.
func (e *Engine) balanced(project string, usable []string, now time.Time) string {
	if len(usable) == 0 {
		return ""
	}
	b := e.newBalancer(usable, now)
	ts, mean := e.tenants(project)
	weight := e.weightOf(project, mean)
	total := weight
	for _, t := range ts {
		if _, ok := b.share[t.account]; ok {
			b.load[t.account] += t.weight
			total += t.weight
		}
	}
	return b.pick(weight, total, "")
}

// Move is a project's account before and after a rebalance; From == To is a project that stays.
type Move struct {
	Project string `json:"project"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// RebalanceResult is the plan to place every project without a folder pin again by the balancing
// rule, and, once applied, what was done. A pinned project stays where its pin says and counts as load.
type RebalanceResult struct {
	Applied bool   `json:"applied"`
	Plan    []Move `json:"plan"`   // every project without a folder pin, the heaviest first
	Pinned  int    `json:"pinned"` // projects left alone because their folder is pinned
}

// Rebalance plans, and with apply carries out, a fresh placement of every project that has no
// folder pin. It starts from nothing, so the plan does not depend on where the projects are now
// (running it twice changes nothing the second time), and a project whose account ties keeps it.
// Accounts that are not full are candidates: a 5-hour cap or a cooldown is a detour, never a
// reason to empty an account. A project with no candidate stays.
func (e *Engine) Rebalance(apply bool) RebalanceResult {
	now := e.now()
	loads, ok := e.readLoads(now) // reads the disk: before the lock
	e.mu.Lock()
	if ok {
		e.loads, e.loadsAt = loads, now
	}
	res := e.plan(now)
	moved := 0
	if apply {
		res.Applied = true
		for _, m := range res.Plan {
			if m.From != m.To {
				e.st.SetHome(m.Project, m.To, now)
				moved++
			}
		}
		if moved > 0 {
			e.dirty = true
			e.host.Log("info", fmt.Sprintf("cpa-balancer: rebalanced: %d of %d projects moved", moved, len(res.Plan)))
		}
	}
	e.mu.Unlock()
	if moved > 0 {
		e.save()
	}
	return res
}

// plan is the rebalance; callers hold e.mu.
func (e *Engine) plan(now time.Time) RebalanceResult {
	res := RebalanceResult{Plan: []Move{}}
	var candidates []string
	for _, a := range e.cfg.Accounts {
		if e.standing(a.AuthFile, quota.Main, now) != policy.Full {
			candidates = append(candidates, a.AuthFile)
		}
	}
	ts, _ := e.tenants("")
	var free []tenant
	for _, t := range ts {
		if t.pinned {
			res.Pinned++
		} else {
			free = append(free, t)
		}
	}
	if len(candidates) == 0 {
		for _, t := range free {
			res.Plan = append(res.Plan, Move{t.project, t.account, t.account})
		}
		return res
	}
	b := e.newBalancer(candidates, now)
	var total float64
	for _, t := range ts {
		if _, ok := b.share[t.account]; t.pinned && ok {
			b.load[t.account] += t.weight
			total += t.weight
		}
	}
	for _, t := range free {
		total += t.weight
	}
	sort.SliceStable(free, func(i, j int) bool { return free[i].weight > free[j].weight }) // ties stay by name
	for _, t := range free {
		to := b.pick(t.weight, total, t.account)
		b.load[to] += t.weight
		res.Plan = append(res.Plan, Move{t.project, t.account, to})
	}
	return res
}
