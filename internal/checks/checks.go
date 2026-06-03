// Package checks performs HTTP health checks and classifies results into a
// three-state status model (Green / Yellow / Red).
package checks

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pv-udpv/svchealth/internal/config"
)

// Status is the three-state health classification.
type Status int

const (
	StatusUnknown Status = iota // not yet checked
	StatusGreen                 // healthy
	StatusYellow                // degraded (slow, or non-ideal but reachable)
	StatusRed                   // down / failing
)

func (s Status) String() string {
	switch s {
	case StatusGreen:
		return "UP"
	case StatusYellow:
		return "DEGRADED"
	case StatusRed:
		return "DOWN"
	default:
		return "UNKNOWN"
	}
}

// Symbol returns a compact glyph for the status.
func (s Status) Symbol() string {
	switch s {
	case StatusGreen:
		return "●"
	case StatusYellow:
		return "◐"
	case StatusRed:
		return "○"
	default:
		return "·"
	}
}

// Result is the outcome of a single health check.
type Result struct {
	Endpoint   string
	TargetURL  string
	Status     Status
	HTTPStatus int
	Latency    time.Duration
	At         time.Time
	Err        string
}

// Healthy reports whether the result counts as a success for uptime math.
func (r Result) Healthy() bool { return r.Status == StatusGreen || r.Status == StatusYellow }

// Authorizer returns extra request headers (e.g. a bearer token) for an
// endpoint, or nil to add none. It mirrors connectors.AuthProvider.Authorize
// without importing the connectors package (avoids an import cycle).
type Authorizer func(ctx context.Context, endpoint string) (map[string]string, error)

// Checker executes checks against a single endpoint definition.
type Checker struct {
	ep         config.Endpoint
	degradedMs int
	criticalMs int
	client     *http.Client
	authorize  Authorizer
}

// SetAuthorizer attaches an optional per-request header provider.
func (c *Checker) SetAuthorizer(a Authorizer) { c.authorize = a }

// New builds a Checker for an endpoint. Latency thresholds come from the
// endpoint (already defaulted to the global values by config.applyDefaults).
func New(ep config.Endpoint, settings config.Settings) *Checker {
	degraded := ep.DegradedLatencyMs
	if degraded <= 0 {
		degraded = settings.DegradedLatencyMs
	}
	critical := ep.CriticalLatencyMs
	if critical <= 0 {
		critical = settings.CriticalLatencyMs
	}
	return &Checker{
		ep:         ep,
		degradedMs: degraded,
		criticalMs: critical,
		client: &http.Client{
			// Per-request timeout enforced via context; keep transport sane.
			Timeout: ep.Timeout() + 2*time.Second,
		},
	}
}

// TargetURL returns the URL this checker pings. When the endpoint was derived
// from a spec, the caller sets ep.URL to the chosen target beforehand.
func (c *Checker) TargetURL() string { return c.ep.URL }

// Check performs one health check and classifies the result.
func (c *Checker) Check(ctx context.Context) Result {
	res := Result{
		Endpoint:  c.ep.Name,
		TargetURL: c.ep.URL,
		At:        time.Now(),
	}
	cctx, cancel := context.WithTimeout(ctx, c.ep.Timeout())
	defer cancel()

	method := c.ep.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(cctx, method, c.ep.URL, nil)
	if err != nil {
		res.Status = StatusRed
		res.Err = err.Error()
		return res
	}
	for k, v := range c.ep.Headers {
		req.Header.Set(k, v)
	}
	if c.authorize != nil {
		if hdrs, aerr := c.authorize(cctx, c.ep.Name); aerr != nil {
			res.Status = StatusRed
			res.Err = "auth: " + trimErr(aerr)
			return res
		} else {
			for k, v := range hdrs {
				req.Header.Set(k, v)
			}
		}
	}

	start := time.Now()
	resp, err := c.client.Do(req)
	res.Latency = time.Since(start)
	if err != nil {
		res.Status = StatusRed
		res.Err = trimErr(err)
		return res
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // drain to reuse conn
	res.HTTPStatus = resp.StatusCode

	res.Status = c.classify(resp.StatusCode, res.Latency)
	if res.Status == StatusRed {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Healthy code but over the critical latency budget.
			res.Err = fmt.Sprintf("slow %dms > %dms", res.Latency.Milliseconds(), c.criticalMs)
		} else {
			res.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
	}
	return res
}

func (c *Checker) classify(code int, latency time.Duration) Status {
	expected := c.ep.ExpectStatus
	ok := false
	if expected > 0 {
		ok = code == expected
	} else {
		ok = code >= 200 && code < 300
	}
	if !ok {
		// 3xx/4xx/5xx that don't match -> red, but treat reachable 4xx auth as red too.
		return StatusRed
	}
	// Healthy code but pathologically slow -> treat as down (red).
	if c.criticalMs > 0 && latency > time.Duration(c.criticalMs)*time.Millisecond {
		return StatusRed
	}
	// Healthy code, but slow -> degraded (yellow).
	if c.degradedMs > 0 && latency > time.Duration(c.degradedMs)*time.Millisecond {
		return StatusYellow
	}
	return StatusGreen
}

func trimErr(err error) string {
	s := err.Error()
	const max = 80
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
