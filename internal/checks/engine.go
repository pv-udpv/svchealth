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

// Engine coordinates checkers for all endpoints, tracks consecutive-failure
// streaks, and fires connector hooks on sustained-down / recovery transitions.
//
// Alerting uses debounce + hysteresis: a sustained-down alert fires only after
// AlertAfter consecutive RED checks, and clears only after AlertClearAfter
// consecutive healthy checks. This avoids flapping notifications.
type Engine struct {
	cfg   *config.Config
	hooks connectors.Hooks

	alertAfter      int
	alertClearAfter int

	mu          sync.Mutex
	checkers    map[string]*Checker       // by endpoint name
	streak      map[string]int            // consecutive RED count
	upStreak    map[string]int            // consecutive healthy count (hysteresis)
	wasDown     map[string]bool           // alerted-down latch
	specTargets map[string][]specs.Target // discovered spec targets by endpoint
	specInfo    map[string]SpecInfo       // discovered spec metadata by endpoint
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
	alertAfter := cfg.Settings.AlertAfter
	if alertAfter <= 0 {
		alertAfter = 3
	}
	alertClear := cfg.Settings.AlertClearAfter
	if alertClear <= 0 {
		alertClear = 1
	}
	e := &Engine{
		cfg:             cfg,
		hooks:           hooks,
		alertAfter:      alertAfter,
		alertClearAfter: alertClear,
		checkers:        map[string]*Checker{},
		streak:          map[string]int{},
		upStreak:        map[string]int{},
		wasDown:         map[string]bool{},
		specTargets:     map[string][]specs.Target{},
		specInfo:        map[string]SpecInfo{},
	}
	for _, ep := range cfg.Endpoints {
		resolved := e.resolveEndpoint(ctx, ep)
		chk := New(resolved, cfg.Settings)
		if hooks.Auth != nil {
			chk.SetAuthorizer(hooks.Auth.Authorize)
		}
		e.checkers[resolved.Name] = chk
	}
	return e
}

// resolveEndpoint fills in a concrete URL when only a spec_uri was provided by
// discovering the spec and picking the highest-priority derived target.
func (e *Engine) resolveEndpoint(ctx context.Context, ep config.Endpoint) config.Endpoint {
	if ep.URL != "" || ep.SpecURI == "" {
		return ep
	}
	sp, err := specs.Discover(ctx, ep.SpecURI, ep.Headers, ep.Timeout())
	if err != nil || len(sp.Targets) == 0 {
		// Leave URL empty; the check will fail RED with a clear error, which is
		// the correct signal that discovery did not yield a target.
		ep.URL = ep.SpecURI
		return ep
	}
	e.specTargets[ep.Name] = sp.Targets
	e.specInfo[ep.Name] = SpecInfo{Kind: string(sp.Kind), Title: sp.Title, Version: sp.Version, BaseURL: sp.BaseURL}
	ep.URL = sp.Targets[0].URL
	return ep
}

// Endpoints returns endpoint names in config order.
func (e *Engine) Endpoints() []string {
	names := make([]string, 0, len(e.cfg.Endpoints))
	for _, ep := range e.cfg.Endpoints {
		names = append(names, ep.Name)
	}
	return names
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
// debounce (AlertAfter) and hysteresis (AlertClearAfter).
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

	if e.hooks.Notifier == nil {
		return
	}
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
