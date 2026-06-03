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

// Telegram implements Notifier by sending a message to a chat via the Bot API
// on sustained-down and recovery transitions.
//
// Env:
//
//	SVCHEALTH_TELEGRAM_BOT_TOKEN  Bot token from @BotFather (required)
//	SVCHEALTH_TELEGRAM_CHAT_ID    Target chat id (required)
type Telegram struct {
	token  string
	chatID string
	client *http.Client
}

// NewTelegramFromEnv returns a Telegram notifier, or (nil, nil) if not configured.
func NewTelegramFromEnv() (*Telegram, error) {
	token := os.Getenv("SVCHEALTH_TELEGRAM_BOT_TOKEN")
	chat := os.Getenv("SVCHEALTH_TELEGRAM_CHAT_ID")
	if token == "" || chat == "" {
		return nil, nil
	}
	return &Telegram{
		token:  token,
		chatID: chat,
		client: &http.Client{Timeout: 12 * time.Second},
	}, nil
}

// OnSustainedDown posts a down alert to the configured chat.
func (t *Telegram) OnSustainedDown(ctx context.Context, endpoint string, streak int, last CheckSummary) error {
	text := fmt.Sprintf(
		"\U0001F534 *DOWN* `%s`\nstreak: %d failed checks\nurl: %s\nhttp: %d  latency: %dms\nerr: %s",
		endpoint, streak, last.TargetURL, last.HTTPStatus, last.LatencyMs, errOrNone(last.Err),
	)
	return t.send(ctx, text)
}

// OnRecovered posts a recovery notice to the configured chat.
func (t *Telegram) OnRecovered(ctx context.Context, endpoint string) error {
	return t.send(ctx, fmt.Sprintf("\U0001F7E2 *RECOVERED* `%s` is healthy again", endpoint))
}

func errOrNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func (t *Telegram) send(ctx context.Context, text string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.token)
	body, err := json.Marshal(map[string]any{
		"chat_id":    t.chatID,
		"text":       text,
		"parse_mode": "Markdown",
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram api: status %d", resp.StatusCode)
	}
	return nil
}
