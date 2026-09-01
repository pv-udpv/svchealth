package checks

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/pv-udpv/svchealth/internal/config"
	"github.com/pv-udpv/svchealth/internal/connectors"
	"github.com/pv-udpv/svchealth/internal/specs"
)

// hostOf extracts the host (without port) from a URL string, "" on failure.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// UptimeSource reports rolling uptime (fraction 0..1) and sample count for an
// endpoint over a window. *store.Store satisfies this interface.
type UptimeSource interface {
	Uptime(endpoint string, window time.Duration) (float64, int, error)
}

// Engine coordinates checkers for all endpoints, tracks consecutive-failure
// streaks, and fires connector hooks on sustained-down / recovery transitions.
//
// Alerting uses debounce + hysteresis: a sustained-down alert fires only after
// AlertAfter consecutive RED checks, and clears only after AlertClearAfter
// consecutive healthy checks. Uptime-threshold alerting (when enabled) uses a
// separate latch keyed on rolling uptime crossing the configured threshold.
type Engine struct {
	cfg   *config.Config
	hooks connectors.Hooks

	alertAfter      int
	alertClearAfter int

	// Uptime alerts (optional). Set once before the check loop starts and
	// subsequently read-only, so they are safe to read without the mutex.
	uptime          UptimeSource
	uptimeThreshold float64 // percent; <= 0 disables
	uptimeWindow    time.Duration

	mu            sync.Mutex
	checkers      map[string]*Checker       // by endpoint name
	streak        map[string]int            // consecutive RED count
	upStreak      map[string]int            // consecutive healthy count (hysteresis)
	wasDown       map[string]bool           // alerted-down latch
	uptimeAlerted map[string]bool           // uptime alert latch
	specTargets   map[string][]specs.Target // discovered spec targets by endpoint
	specInfo      map[string]SpecInfo       // discovered spec metadata by endpoint
}

// SpecInfo is lightweight spec metadata surfaced to the UI detail view.
type SpecInfo struct {
	Kind    string
	Title   string
	Version string
	BaseURL string
}

// NewEngine builds an Engine, resolving spec-derived endpoints up front.
func NewEngine(ctx context.Context, cfg *config.Config, hooks connectors.Hooks) *Engine {
	e := &Engine{
		cfg:             cfg,
		hooks:           hooks,
		alertAfter:      norm(cfg.Settings.AlertAfter, 3),
		alertClearAfter: norm(cfg.Settings.AlertClearAfter, 1),
		streak:          map[string]int{},
		upStreak:        map[string]int{},
		wasDown:         map[string]bool{},
		uptimeAlerted:   map[string]bool{},
		specTargets:     map[string][]specs.Target{},
		specInfo:        map[string]SpecInfo{},
	}
	e.checkers, e.specTargets, e.specInfo = e.build(ctx, cfg)
	return e
}

// SetUptime enables uptime-threshold alerting. threshold is a percentage
// (0..100); a zero window defaults to 1h.
func (e *Engine) SetUptime(src UptimeSource, threshold float64, window time.Duration) {
	if window <= 0 {
		window = time.Hour
	}
	e.mu.Lock()
	e.uptime = src
	e.uptimeThreshold = threshold
	e.uptimeWindow = window
	e.mu.Unlock()
}

// Reload swaps in a new configuration, adding/removing/replacing checkers in
// place. State (streaks, latches) for removed endpoints is dropped; state for
// retained endpoints is preserved so an ongoing outage keeps its debounce.
func (e *Engine) Reload(ctx context.Context, cfg *config.Config) {
	checkers, targets, infos := e.build(ctx, cfg)

	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg
	e.alertAfter = norm(cfg.Settings.AlertAfter, 3)
	e.alertClearAfter = norm(cfg.Settings.AlertClearAfter, 1)
	e.checkers = checkers
	e.specTargets = targets
	e.specInfo = infos

	keep := map[string]bool{}
	for _, ep := range cfg.Endpoints {
		keep[ep.Name] = true
	}
	for k := range e.streak {
		if !keep[k] {
			delete(e.streak, k)
		}
	}
	for k := range e.upStreak {
		if !keep[k] {
			delete(e.upStreak, k)
		}
	}
	for k := range e.wasDown {
		if !keep[k] {
			delete(e.wasDown, k)
		}
	}
	for k := range e.uptimeAlerted {
		if !keep[k] {
			delete(e.uptimeAlerted, k)
		}
	}
}

// build resolves endpoints and constructs checkers + spec maps for a config.
func (e *Engine) build(ctx context.Context, cfg *config.Config) (map[string]*Checker, map[string][]specs.Target, map[string]SpecInfo) {
	checkers := map[string]*Checker{}
	targets := map[string][]specs.Target{}
	infos := map[string]SpecInfo{}
	for _, ep := range cfg.Endpoints {
		resolved, tr, info := resolve(ctx, ep)
		if len(tr) > 0 {
			targets[resolved.Name] = tr
		}
		if info.Kind != "" {
			infos[resolved.Name] = info
		}
		chk := New(resolved, cfg.Settings)
		if e.hooks.Auth != nil {
			chk.SetAuthorizer(e.hooks.Auth.Authorize)
		}
		checkers[resolved.Name] = chk
	}
	return checkers, targets, infos
}

// resolve fills in a concrete URL when only a spec_uri was provided by
// discovering the spec and picking the highest-priority derived target.
func resolve(ctx context.Context, ep config.Endpoint) (config.Endpoint, []specs.Target, SpecInfo) {
	if ep.URL != "" || ep.SpecURI == "" {
		return ep, nil, SpecInfo{}
	}
	sp, err := specs.Discover(ctx, ep.SpecURI, ep.Headers, ep.Timeout())
	if err != nil || len(sp.Targets) == 0 {
		// Leave URL = spec_uri; the check fails RED with a clear error, which is
		// the correct signal that discovery did not yield a target.
		ep.URL = ep.SpecURI
		return ep, nil, SpecInfo{}
	}
	info := SpecInfo{Kind: string(sp.Kind), Title: sp.Title, Version: sp.Version, BaseURL: sp.BaseURL}
	ep.URL = sp.Targets[0].URL
	return ep, sp.Targets, info
}

func norm(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// Endpoints returns endpoint names in config order.
func (e *Engine) Endpoints() []string {
	names := make([]string, 0, len(e.cfg.Endpoints))
	for _, ep := range e.cfg.Endpoints {
		names = append(names, ep.Name)
	}
	return names
}

// Has reports whether an endpoint is currently known to the engine.
func (e *Engine) Has(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.checkers[name]
	return ok
}

// TargetURL returns the resolved URL for an endpoint.
func (e *Engine) TargetURL(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.checkers[name]; ok {
		return c.TargetURL()
	}
	return ""
}

// SpecPaths returns up to `max` discovered spec target paths for an endpoint
// (empty if it was not spec-derived).
func (e *Engine) SpecPaths(name string, max int) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	targets := e.specTargets[name]
	out := make([]string, 0, len(targets))
	for i, t := range targets {
		if max > 0 && i >= max {
			break
		}
		out = append(out, t.Method+" "+t.Path)
	}
	return out
}

// SpecMeta returns discovered spec metadata for an endpoint, ok=false if none.
func (e *Engine) SpecMeta(name string) (SpecInfo, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	info, ok := e.specInfo[name]
	return info, ok
}

// EdgeStatus returns the Cloudflare (or other) edge/tunnel status for an
// endpoint's host, ok=false when no Edge connector is configured. The result is
// (healthy, detail). Errors are folded into ok=false with a detail message.
func (e *Engine) EdgeStatus(ctx context.Context, name string) (healthy bool, detail string, ok bool) {
	if e.hooks.Edge == nil {
		return false, "", false
	}
	host := hostOf(e.TargetURL(name))
	if host == "" {
		return false, "", false
	}
	h, d, err := e.hooks.Edge.TunnelHealthy(ctx, host)
	if err != nil {
		return false, "err: " + err.Error(), true
	}
	return h, d, true
}

// IntervalOf returns the polling interval for an endpoint.
func (e *Engine) IntervalOf(name string) time.Duration {
	for _, ep := range e.cfg.Endpoints {
		if ep.Name == name {
			return ep.Interval()
		}
	}
	return time.Duration(e.cfg.Settings.IntervalSeconds) * time.Second
}

// CheckOne runs a single check for an endpoint and processes hooks.
func (e *Engine) CheckOne(ctx context.Context, name string) (Result, bool) {
	e.mu.Lock()
	c, ok := e.checkers[name]
	e.mu.Unlock()
	if !ok {
		return Result{}, false
	}
	res := c.Check(ctx)
	e.processHooks(ctx, res)
	return res, true
}

// processHooks updates streak state and invokes connector callbacks using
// debounce (AlertAfter) and hysteresis (AlertClearAfter), plus optional
// uptime-threshold alerting.
func (e *Engine) processHooks(ctx context.Context, res Result) {
	ep := res.Endpoint
	e.mu.Lock()

	var fireDown, fireRecover bool
	var downStreak int

	if res.Status == StatusRed {
		e.streak[ep]++
		e.upStreak[ep] = 0
		downStreak = e.streak[ep]
		if downStreak >= e.alertAfter && !e.wasDown[ep] {
			e.wasDown[ep] = true
			fireDown = true
		}
	} else {
		e.upStreak[ep]++
		e.streak[ep] = 0
		if e.wasDown[ep] && e.upStreak[ep] >= e.alertClearAfter {
			e.wasDown[ep] = false
			fireRecover = true
		}
	}
	e.mu.Unlock()

	if e.hooks.Notifier != nil {
		if fireDown {
			_ = e.hooks.Notifier.OnSustainedDown(ctx, ep, downStreak, connectors.CheckSummary{
				Endpoint:   res.Endpoint,
				TargetURL:  res.TargetURL,
				HTTPStatus: res.HTTPStatus,
				LatencyMs:  res.Latency.Milliseconds(),
				Err:        res.Err,
			})
		}
		if fireRecover {
			_ = e.hooks.Notifier.OnRecovered(ctx, ep)
		}
	}

	// Uptime-threshold alerting, independent of the sustained-down notifier.
	if e.uptime != nil && e.uptimeThreshold > 0 && e.hooks.Uptime != nil {
		up, n, err := e.uptime.Uptime(ep, e.uptimeWindow)
		if err == nil && n >= e.alertAfter {
			pct := up * 100
			e.mu.Lock()
			alerted := e.uptimeAlerted[ep]
			fireAlert := !alerted && pct < e.uptimeThreshold
			fireRecover := alerted && pct >= e.uptimeThreshold
			if fireAlert {
				e.uptimeAlerted[ep] = true
			}
			if fireRecover {
				e.uptimeAlerted[ep] = false
			}
			e.mu.Unlock()

			if fireAlert {
				_ = e.hooks.Uptime.OnUptimeAlert(ctx, ep, pct, e.uptimeWindow, n)
			}
			if fireRecover {
				_ = e.hooks.Uptime.OnUptimeRecovered(ctx, ep, pct)
			}
		}
	}
}
