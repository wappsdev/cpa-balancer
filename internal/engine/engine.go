// Package engine keeps the live snapshot and decision, and answers CPA's per-request picks.
package engine

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/adnankurt/cpa-balancer/internal/anchor"
	"github.com/adnankurt/cpa-balancer/internal/config"
	"github.com/adnankurt/cpa-balancer/internal/history"
	"github.com/adnankurt/cpa-balancer/internal/policy"
	"github.com/adnankurt/cpa-balancer/internal/probe"
	"github.com/adnankurt/cpa-balancer/internal/quota"
	"github.com/adnankurt/cpa-balancer/internal/state"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Host is the subset of CPA host callbacks the engine uses.
type Host interface {
	AuthList() ([]pluginapi.HostAuthFileEntry, error)
	AuthGet(authIndex string) ([]byte, error) // raw credential JSON
	HTTPDo(pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error)
	ModelExecute(pluginapi.HostModelExecutionRequest) (pluginapi.HostModelExecutionResponse, error)
	Log(level, message string)
}

type credential struct {
	index       string
	unavailable bool
	disabled    bool // switched off by hand, as opposed to cooled down
}

type Engine struct {
	cfg  config.Config // immutable; reconfigure builds a new engine
	host Host
	now  func() time.Time
	rec  *history.Recorder // shared across engines; nil disables history

	tickMu      sync.Mutex // serializes Tick
	mu          sync.RWMutex
	st          *state.State
	credentials map[string]credential // auth file -> host view
	snaps       map[string]policy.Snapshot
	decision    policy.Decision
	switchedTo  map[string]bool // accounts that became active since the last tick, to probe if stale
	dirty       bool
}

var delegate = pluginapi.SchedulerPickResponse{DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst, Handled: true}

func New(cfg config.Config, host Host, now func() time.Time, rec *history.Recorder) *Engine {
	st, err := state.Load(cfg.StatePath)
	if err != nil {
		host.Log("warn", "cpa-balancer: state not loaded, starting empty: "+err.Error())
		st = state.New()
	}
	e := &Engine{cfg: cfg, host: host, now: now, rec: rec, st: st, credentials: map[string]credential{}, switchedTo: map[string]bool{}}
	e.mu.Lock()
	e.recompute(now())
	e.mu.Unlock()
	return e
}

func (e *Engine) bucketOf(model string) (quota.Bucket, bool) {
	switch {
	case slices.Contains(e.cfg.Buckets.Main, model):
		return quota.Main, true
	case slices.Contains(e.cfg.Buckets.Fable, model):
		return quota.Fable, true
	}
	return "", false
}

// recompute rebuilds snapshots and the decision; callers hold e.mu.
func (e *Engine) recompute(now time.Time) {
	snaps := map[string]policy.Snapshot{}
	for _, a := range e.cfg.Accounts {
		acc := e.st.Account(a.AuthFile)
		var anchored quota.Readings
		if acc.AnchorReading != nil {
			anchored = quota.Readings{quota.Session: *acc.AnchorReading}
		}
		readings := quota.RollForward(quota.Merge(acc.Headers, acc.Probe, anchored), now)
		if acc.NoFiveHour {
			// a reading kept from before the plan lost its 5-hour limit would read as a closed window
			delete(readings, quota.Session)
		}
		if acc.NoFable {
			delete(readings, quota.Fable)
		}
		snaps[a.AuthFile] = policy.Snapshot{Readings: readings, Unavailable: e.credentials[a.AuthFile].unavailable, NoFiveHour: acc.NoFiveHour,
			Weight: probe.WeightOf(acc.Plan.Name)}
	}
	for name, snap := range snaps {
		for b, r := range snap.Readings {
			if old, ok := e.snaps[name].Readings[b]; ok && r.Source == "estimate" && old.Source != "estimate" {
				e.rec.AddEvent(history.Event{At: now, Kind: "reset", Account: name, Bucket: string(b)})
			}
		}
	}
	d := policy.Decide(snaps, e.cfg, now, e.st.Active)
	for _, x := range []struct {
		b quota.Bucket
		c policy.Choice
	}{{quota.Main, d.Main}, {quota.Fable, d.Fable}} {
		if e.cfg.Paused {
			break // nothing is applied while paused, so nothing is recorded as a change
		}
		prev := e.st.Active[x.b]
		if prev.Account == x.c.Account {
			if prev.Provisional != x.c.Provisional {
				prev.Provisional = x.c.Provisional
				e.st.Active[x.b] = prev
				e.dirty = true
			}
			continue
		}
		e.st.Active[x.b] = policy.Active{Account: x.c.Account, Since: now, Provisional: x.c.Provisional}
		if x.c.Account != "" {
			e.switchedTo[x.c.Account] = true
		}
		e.st.AddChange(state.Change{At: now, Bucket: x.b, From: prev.Account, To: x.c.Account, Reason: x.c.Reason})
		e.rec.AddEvent(history.Event{At: now, Kind: "switch", Bucket: string(x.b), From: prev.Account, To: x.c.Account, Reason: x.c.Reason})
		e.host.Log("info", fmt.Sprintf("cpa-balancer: %s %q -> %q (%s)", x.b, prev.Account, x.c.Account, x.c.Reason))
		e.dirty = true
	}
	e.snaps, e.decision = snaps, d
}

// OnUsage records the rate-limit headers of a completed request and re-decides at once.
func (e *Engine) OnUsage(rec pluginapi.UsageRecord) {
	if rec.Provider != "claude" || !e.cfg.Managed(rec.AuthID) {
		return
	}
	now := e.now()
	traffic := history.Traffic{Requests: 1, InputTokens: rec.Detail.InputTokens, OutputTokens: rec.Detail.OutputTokens,
		CacheReadTokens: rec.Detail.CacheReadTokens, CacheCreationTokens: rec.Detail.CacheCreationTokens}
	if rec.Failed {
		traffic.Failed = 1
	}
	e.rec.AddTraffic(now, rec.AuthID, rec.Model, traffic)
	readings := quota.FromHeaders(rec.ResponseHeaders, rec.Model, e.cfg.Buckets.Main, e.cfg.Buckets.Fable, now)
	if len(readings) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	acc := e.st.Account(rec.AuthID)
	acc.Headers = quota.Merge(acc.Headers, readings)
	e.dirty = true
	e.recompute(now)
}

func isClaude(req pluginapi.SchedulerPickRequest) bool {
	return req.Provider == "claude" || (len(req.Providers) == 1 && req.Providers[0] == "claude")
}

// Pick answers scheduler.pick without I/O.
func (e *Engine) Pick(req pluginapi.SchedulerPickRequest) pluginapi.SchedulerPickResponse {
	candidates := map[string]bool{}
	for _, c := range req.Candidates {
		candidates[c.ID] = true
	}
	if pinned, _ := req.Options.Metadata["pinned_auth_id"].(string); pinned != "" {
		if candidates[pinned] {
			return pluginapi.SchedulerPickResponse{AuthID: pinned, Handled: true}
		}
		return delegate
	}
	b, ok := e.bucketOf(req.Model)
	if e.cfg.Mode != "apply" || e.cfg.Paused || !isClaude(req) || !ok {
		return delegate
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	order := e.decision.MainOrder
	if b == quota.Fable {
		order = e.decision.FableOrder
	}
	for _, name := range order {
		if candidates[name] {
			return pluginapi.SchedulerPickResponse{AuthID: name, Handled: true}
		}
	}
	return delegate
}

// Tick runs the periodic work: refresh credentials, probe, re-decide, anchor, persist.
func (e *Engine) Tick() {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()
	now := e.now()
	e.refreshCredentials()
	defer e.record(now)
	if e.cfg.Paused {
		e.mu.Lock()
		e.recompute(now)
		e.mu.Unlock()
		e.save()
		return
	}
	e.probeDue(now, nil)
	e.mu.Lock()
	e.recompute(now)
	// Accounts switched to here or by OnUsage since the last tick, if they are still chosen.
	switched := map[string]bool{}
	for account := range e.switchedTo {
		if account == e.st.Active[quota.Main].Account || account == e.st.Active[quota.Fable].Account {
			switched[account] = true
		}
	}
	e.switchedTo = map[string]bool{}
	e.mu.Unlock()
	if len(switched) > 0 && e.probeDue(now, switched) {
		e.mu.Lock()
		e.recompute(now)
		e.mu.Unlock()
	}
	e.anchorIdle(now)
	e.checkPlans(now)
	e.save()
}

func (e *Engine) refreshCredentials() {
	files, err := e.host.AuthList()
	if err != nil {
		e.host.Log("warn", "cpa-balancer: auth list failed: "+err.Error())
		return
	}
	creds := map[string]credential{}
	for _, f := range files {
		if e.cfg.Managed(f.ID) {
			creds[f.ID] = credential{index: f.AuthIndex, unavailable: f.Disabled || f.Unavailable, disabled: f.Disabled}
		}
	}
	e.mu.Lock()
	e.credentials = creds
	e.mu.Unlock()
}

// probeDue probes every due account (only those in onlySwitched when given); reports any success.
func (e *Engine) probeDue(now time.Time, onlySwitched map[string]bool) bool {
	type job struct{ name, index string }
	var jobs []job
	weekly := []quota.Bucket{quota.Main}
	if len(e.cfg.Buckets.Fable) > 0 {
		weekly = append(weekly, quota.Fable)
	}
	e.mu.RLock()
	for _, a := range e.cfg.Accounts {
		cred, ok := e.credentials[a.AuthFile]
		if !ok || (onlySwitched != nil && !onlySwitched[a.AuthFile]) {
			continue
		}
		snap, acc := e.snaps[a.AuthFile], e.st.Accounts[a.AuthFile]
		var lastProbe, retryAfter time.Time
		if acc != nil {
			lastProbe, retryAfter = acc.LastProbe, acc.RetryAfter
		}
		// Data is only as fresh as the oldest weekly bucket; a missing one counts as stale.
		var reference time.Time
		for i, b := range weekly {
			r, ok := snap.Readings[b]
			if !ok {
				reference = time.Time{}
				break
			}
			if i == 0 || r.ObservedAt.Before(reference) {
				reference = r.ObservedAt
			}
		}
		estimated := false
		for _, r := range snap.Readings {
			estimated = estimated || r.Source == "estimate"
		}
		if probe.Due(now, lastProbe, retryAfter, reference, estimated, onlySwitched != nil) {
			jobs = append(jobs, job{a.AuthFile, cred.index})
		}
	}
	e.mu.RUnlock()

	anySuccess := false
	for _, j := range jobs {
		res := e.runProbe(j.index, now)
		e.mu.Lock()
		acc := e.st.Account(j.name)
		acc.LastProbe, acc.RetryAfter = now, res.RetryAfter
		if res.Err != nil {
			e.host.Log("warn", fmt.Sprintf("cpa-balancer: probe %s failed: %v", j.name, res.Err))
		} else {
			acc.Probe = res.Readings
			acc.NoFiveHour, acc.NoFable = !res.Limits[quota.Session], !res.Limits[quota.Fable]
			session, has := res.Readings[quota.Session]
			if !acc.AnchorMainModel && policy.AnchorDidNotStartWindow(session, has, acc.AnchorReading, acc.LastAnchor) {
				acc.AnchorMainModel = true
				e.host.Log("warn", fmt.Sprintf("cpa-balancer: anchor %s: Haiku did not start the 5-hour window; using %s from now on",
					j.name, e.cfg.Anchor.FallbackModel))
			}
			anySuccess = true
		}
		e.dirty = true
		e.mu.Unlock()
	}
	return anySuccess
}

func (e *Engine) runProbe(index string, now time.Time) probe.Result {
	raw, err := e.host.AuthGet(index)
	if err != nil {
		return probe.Result{Err: fmt.Errorf("auth get: %w", err)}
	}
	token, err := probe.AccessToken(raw)
	if err != nil {
		return probe.Result{Err: err}
	}
	resp, err := e.host.HTTPDo(probe.Request(token))
	if err != nil {
		return probe.Result{Err: fmt.Errorf("http: %w", err)}
	}
	return probe.Parse(resp, now)
}

func (e *Engine) anchorIdle(now time.Time) {
	e.mu.RLock()
	last, retry, useMain := map[string]time.Time{}, map[string]time.Time{}, map[string]bool{}
	for name, acc := range e.st.Accounts {
		last[name], retry[name], useMain[name] = acc.LastAnchor, acc.AnchorRetryAfter, acc.AnchorMainModel
	}
	targets := policy.AnchorTargets(e.decision, e.snaps, e.cfg, now, last, retry)
	e.mu.RUnlock()

	anchored := false
	for _, name := range targets {
		if e.cfg.Mode != "apply" {
			e.host.Log("info", "cpa-balancer: would anchor "+name)
			continue
		}
		req := anchor.Request(name, useMain[name], e.cfg.Anchor.Model, e.cfg.Anchor.FallbackModel)
		resp, err := e.host.ModelExecute(req)
		e.mu.Lock()
		acc := e.st.Account(name)
		acc.LastAnchor, acc.AnchorRetryAfter = now, time.Time{}
		switch {
		case err != nil:
			e.host.Log("warn", fmt.Sprintf("cpa-balancer: anchor %s failed: %v", name, err))
		case resp.StatusCode != http.StatusOK:
			if seconds, errAtoi := strconv.Atoi(resp.Headers.Get("Retry-After")); errAtoi == nil {
				acc.AnchorRetryAfter = now.Add(time.Duration(seconds) * time.Second)
			}
			e.host.Log("warn", fmt.Sprintf("cpa-balancer: anchor %s failed: HTTP %d", name, resp.StatusCode))
		default:
			if r, ok := anchor.WindowReading(resp, now); ok {
				acc.AnchorReading = &r
			}
			e.host.Log("info", fmt.Sprintf("cpa-balancer: anchored %s with %s", name, req.Model))
			e.rec.AddEvent(history.Event{At: now, Kind: "anchor", Account: name, Model: req.Model})
			anchored = true
		}
		e.dirty = true
		e.mu.Unlock()
	}
	if anchored {
		e.mu.Lock()
		e.recompute(now)
		e.mu.Unlock()
	}
}

// Save persists state if anything changed.
func (e *Engine) save() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.dirty {
		return
	}
	if err := e.st.Save(e.cfg.StatePath); err != nil {
		e.host.Log("warn", "cpa-balancer: state not saved: "+err.Error())
		return
	}
	e.dirty = false
}

// Shutdown persists state before the plugin is unloaded or replaced.
func (e *Engine) Shutdown() { e.save() }

// record stores the end-of-tick levels and writes closed history slots.
func (e *Engine) record(now time.Time) {
	if e.rec == nil {
		return
	}
	e.mu.RLock()
	active := map[string]string{
		string(quota.Main): e.st.Active[quota.Main].Account, string(quota.Fable): e.st.Active[quota.Fable].Account,
	}
	levels := map[string]history.Levels{}
	for _, a := range e.cfg.Accounts {
		snap := e.snaps[a.AuthFile]
		var l history.Levels
		weekly := func(b quota.Bucket) (*float64, *float64) {
			r, ok := snap.Readings[b]
			if !ok || r.ResetsAt == nil {
				return nil, nil
			}
			util, limit := r.Util, policy.Cap(a, *r.ResetsAt, now, e.cfg.Thresholds)
			return &util, &limit
		}
		l.Main, l.MainCap = weekly(quota.Main)
		l.Fable, l.FableCap = weekly(quota.Fable)
		if s, ok := snap.Readings[quota.Session]; ok && s.Running(now) {
			util := s.Util
			l.Session, l.SessionOpen = &util, true
		}
		levels[a.AuthFile] = l
	}
	e.mu.RUnlock()
	e.rec.Snapshot(now, active, levels)
	if err := e.rec.Flush(now); err != nil {
		e.host.Log("warn", "cpa-balancer: history not written: "+err.Error())
	}
}
