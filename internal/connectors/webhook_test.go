package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookFromEnv(t *testing.T) {
	t.Setenv("SVCHEALTH_WEBHOOK_URL", "http://example.com/hook")
	t.Setenv("SVCHEALTH_WEBHOOK_STYLE", "discord")
	w, err := NewWebhookFromEnv()
	if err != nil {
		t.Fatalf("NewWebhookFromEnv: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil webhook when URL is set")
	}
	if w.style != "discord" {
		t.Errorf("style = %q, want discord", w.style)
	}
}

func TestWebhookUnset(t *testing.T) {
	t.Setenv("SVCHEALTH_WEBHOOK_URL", "")
	w, err := NewWebhookFromEnv()
	if err != nil || w != nil {
		t.Fatalf("expected (nil, nil) when unset, got (%v, %v)", w, err)
	}
}

func TestWebhookInvalidStyle(t *testing.T) {
	t.Setenv("SVCHEALTH_WEBHOOK_URL", "http://example.com")
	t.Setenv("SVCHEALTH_WEBHOOK_STYLE", "carrier-pigeon")
	if _, err := NewWebhookFromEnv(); err == nil {
		t.Error("expected error for unsupported style")
	}
}

func TestWebhookPost(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	wh := &Webhook{url: srv.URL, style: "slack", client: srv.Client()}
	err := wh.OnSustainedDown(context.Background(), "api", 3, CheckSummary{
		Endpoint:   "api",
		TargetURL:  "https://api.example",
		HTTPStatus: 500,
		LatencyMs:  42,
		Err:        "HTTP 500",
	})
	if err != nil {
		t.Fatalf("OnSustainedDown: %v", err)
	}
	text, _ := got["text"].(string)
	if text == "" || !strings.Contains(text, "DOWN api") {
		t.Errorf("unexpected slack text: %q", text)
	}
}

func TestWebhookDiscordPayload(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(204)
	}))
	defer srv.Close()

	wh := &Webhook{url: srv.URL, style: "discord", client: srv.Client()}
	if err := wh.OnRecovered(context.Background(), "api"); err != nil {
		t.Fatalf("OnRecovered: %v", err)
	}
	if _, ok := got["content"]; !ok {
		t.Errorf("discord payload missing 'content': %v", got)
	}
	if _, ok := got["text"]; ok {
		t.Errorf("discord payload should not set 'text': %v", got)
	}
}

func TestWebhookNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	wh := &Webhook{url: srv.URL, style: "slack", client: srv.Client()}
	if err := wh.OnRecovered(context.Background(), "api"); err == nil {
		t.Error("expected error on non-2xx response")
	}
}
