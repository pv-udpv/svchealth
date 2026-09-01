package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Webhook implements Notifier and UptimeAlerter by POSTing JSON to a generic
// incoming-webhook URL. It is Slack- and Discord-compatible via the style
// setting, and also works with any HTTP endpoint that accepts a simple JSON body.
//
// Env:
//
//	SVCHEALTH_WEBHOOK_URL    Webhook URL (required)
//	SVCHEALTH_WEBHOOK_STYLE  "slack" (default), "discord", or "generic"
type Webhook struct {
	url    string
	style  string
	client *http.Client
}

// NewWebhookFromEnv returns a Webhook notifier, or (nil, nil) if not configured.
func NewWebhookFromEnv() (*Webhook, error) {
	u := os.Getenv("SVCHEALTH_WEBHOOK_URL")
	if u == "" {
		return nil, nil
	}
	style := os.Getenv("SVCHEALTH_WEBHOOK_STYLE")
	if style == "" {
		style = "slack"
	}
	switch style {
	case "slack", "discord", "generic":
	default:
		return nil, fmt.Errorf("unsupported SVCHEALTH_WEBHOOK_STYLE %q (want slack, discord, or generic)", style)
	}
	return &Webhook{url: u, style: style, client: &http.Client{Timeout: 12 * time.Second}}, nil
}

// OnSustainedDown posts a down alert.
func (w *Webhook) OnSustainedDown(ctx context.Context, endpoint string, streak int, last CheckSummary) error {
	msg := fmt.Sprintf("DOWN %s (%d failed checks) url=%s http=%d latency=%dms err=%s",
		endpoint, streak, last.TargetURL, last.HTTPStatus, last.LatencyMs, errOrNone(last.Err))
	return w.post(ctx, msg)
}

// OnRecovered posts a recovery notice.
func (w *Webhook) OnRecovered(ctx context.Context, endpoint string) error {
	return w.post(ctx, fmt.Sprintf("RECOVERED %s is healthy again", endpoint))
}

// OnUptimeAlert posts an uptime-threshold alert.
func (w *Webhook) OnUptimeAlert(ctx context.Context, endpoint string, uptimePct float64, window time.Duration, samples int) error {
	msg := fmt.Sprintf("UPTIME %s dropped to %.1f%% over %s (n=%d)", endpoint, uptimePct, window.String(), samples)
	return w.post(ctx, msg)
}

// OnUptimeRecovered posts an uptime-recovery notice.
func (w *Webhook) OnUptimeRecovered(ctx context.Context, endpoint string, uptimePct float64) error {
	return w.post(ctx, fmt.Sprintf("UPTIME RECOVERED %s back to %.1f%%", endpoint, uptimePct))
}

// post sends the message using the configured webhook style.
func (w *Webhook) post(ctx context.Context, msg string) error {
	payload := map[string]any{"text": msg}
	switch w.style {
	case "discord":
		payload = map[string]any{"content": msg}
	case "generic":
		payload = map[string]any{"text": msg, "type": "svchealth-alert"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: status %d", resp.StatusCode)
	}
	return nil
}
