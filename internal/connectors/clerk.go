package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Clerk implements AuthProvider: it mints a short-lived session token for a
// configured Clerk session and attaches it as a Bearer header to requests for
// protected endpoints. Tokens are cached and refreshed before expiry.
//
// Env:
//
//	SVCHEALTH_CLERK_SECRET_KEY  Clerk backend API secret key (required)
//	SVCHEALTH_CLERK_SESSION_ID  Clerk session id to mint tokens for (required)
//	SVCHEALTH_CLERK_TEMPLATE    Optional JWT template name
//	SVCHEALTH_CLERK_ENDPOINTS   Optional comma list of endpoint names to apply
//	                            auth to; empty means all endpoints.
type Clerk struct {
	secretKey string
	sessionID string
	template  string
	only      map[string]bool
	client    *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

const clerkAPI = "https://api.clerk.com/v1"

// NewClerkFromEnv returns a Clerk auth provider, or (nil, nil) if not configured.
func NewClerkFromEnv() (*Clerk, error) {
	key := os.Getenv("SVCHEALTH_CLERK_SECRET_KEY")
	sess := os.Getenv("SVCHEALTH_CLERK_SESSION_ID")
	if key == "" || sess == "" {
		return nil, nil
	}
	only := map[string]bool{}
	for _, e := range strings.Split(os.Getenv("SVCHEALTH_CLERK_ENDPOINTS"), ",") {
		if e = strings.TrimSpace(e); e != "" {
			only[e] = true
		}
	}
	return &Clerk{
		secretKey: key,
		sessionID: sess,
		template:  os.Getenv("SVCHEALTH_CLERK_TEMPLATE"),
		only:      only,
		client:    &http.Client{Timeout: 12 * time.Second},
	}, nil
}

// Authorize returns a Bearer header for endpoint, or nil if this endpoint is not
// in the configured allow-list (when one is set).
func (c *Clerk) Authorize(ctx context.Context, endpoint string) (map[string]string, error) {
	if len(c.only) > 0 && !c.only[endpoint] {
		return nil, nil
	}
	tok, err := c.token0(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "Bearer " + tok}, nil
}

// token0 returns a cached token or mints a new one when missing/near expiry.
func (c *Clerk) token0(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && time.Until(c.expires) > 10*time.Second {
		t := c.token
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()

	endpoint := fmt.Sprintf("%s/sessions/%s/tokens", clerkAPI, c.sessionID)
	if c.template != "" {
		endpoint += "/" + url.PathEscape(c.template)
	}
	var out struct {
		JWT    string `json:"jwt"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.post(ctx, endpoint, &out); err != nil {
		return "", err
	}
	if out.JWT == "" {
		if len(out.Errors) > 0 {
			return "", fmt.Errorf("clerk token: %s", out.Errors[0].Message)
		}
		return "", fmt.Errorf("clerk token: empty jwt")
	}
	c.mu.Lock()
	c.token = out.JWT
	// Clerk session tokens are short-lived (~60s); refresh conservatively.
	c.expires = time.Now().Add(50 * time.Second)
	c.mu.Unlock()
	return out.JWT, nil
}

func (c *Clerk) post(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("clerk api: status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
