// Package connectors defines optional integration points (Cloudflare, Linear,
// Clerk) as interfaces with no-op default implementations. Real implementations
// can be wired in later without touching the TUI or check engine.
package connectors

import (
	"context"
)

// CheckSummary is a connector-facing view of a check result, kept independent
// of the checks package to avoid an import cycle.
type CheckSummary struct {
	Endpoint   string
	TargetURL  string
	HTTPStatus int
	LatencyMs  int64
	Err        string
}

// Notifier is invoked when an endpoint transitions to a sustained RED state.
// A Linear implementation would open/update an issue here.
type Notifier interface {
	// OnSustainedDown is called after an endpoint has been RED for `streak`
	// consecutive checks. Implementations must be idempotent.
	OnSustainedDown(ctx context.Context, endpoint string, streak int, last CheckSummary) error
	// OnRecovered is called when an endpoint returns to a healthy state.
	OnRecovered(ctx context.Context, endpoint string) error
}

// EdgeStatus reports edge/DNS/tunnel health for an endpoint's host.
// A Cloudflare implementation would query zone + tunnel status here.
type EdgeStatus interface {
	// TunnelHealthy reports whether the Cloudflare tunnel/zone fronting host is up.
	TunnelHealthy(ctx context.Context, host string) (bool, string, error)
}

// AuthProvider injects credentials for protected endpoints.
// A Clerk implementation would mint/refresh a session token here.
type AuthProvider interface {
	// Authorize returns headers to attach to a request for endpoint.
	Authorize(ctx context.Context, endpoint string) (map[string]string, error)
}

// Hooks bundles the optional integrations. Nil fields are treated as no-ops by
// the engine, so callers can populate only what they need.
type Hooks struct {
	Notifier Notifier
	Edge     EdgeStatus
	Auth     AuthProvider
}

// --- No-op default implementations (used until real connectors are wired) ---

// NoopNotifier satisfies Notifier and does nothing.
type NoopNotifier struct{}

func (NoopNotifier) OnSustainedDown(context.Context, string, int, CheckSummary) error { return nil }
func (NoopNotifier) OnRecovered(context.Context, string) error                        { return nil }

// NoopEdge satisfies EdgeStatus and always reports healthy/unknown.
type NoopEdge struct{}

func (NoopEdge) TunnelHealthy(context.Context, string) (bool, string, error) {
	return true, "n/a", nil
}

// NoopAuth satisfies AuthProvider and adds no headers.
type NoopAuth struct{}

func (NoopAuth) Authorize(context.Context, string) (map[string]string, error) { return nil, nil }

// DefaultHooks returns a Hooks with all no-op implementations.
func DefaultHooks() Hooks {
	return Hooks{Notifier: NoopNotifier{}, Edge: NoopEdge{}, Auth: NoopAuth{}}
}

// MultiNotifier fans a single event out to several Notifiers (e.g. Linear +
// Telegram). Errors from individual notifiers are collected but never abort the
// others; the first error is returned for visibility.
type MultiNotifier []Notifier

// OnSustainedDown delivers the down event to every wrapped notifier.
func (m MultiNotifier) OnSustainedDown(ctx context.Context, endpoint string, streak int, last CheckSummary) error {
	var firstErr error
	for _, n := range m {
		if n == nil {
			continue
		}
		if err := n.OnSustainedDown(ctx, endpoint, streak, last); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// OnRecovered delivers the recovery event to every wrapped notifier.
func (m MultiNotifier) OnRecovered(ctx context.Context, endpoint string) error {
	var firstErr error
	for _, n := range m {
		if n == nil {
			continue
		}
		if err := n.OnRecovered(ctx, endpoint); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
