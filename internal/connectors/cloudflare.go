package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// Cloudflare implements EdgeStatus: it reports whether the Cloudflare zone
// fronting a host is active and whether any cloudflared tunnels are healthy.
// Results are cached briefly to avoid hammering the API on every check tick.
//
// Env:
//
//	SVCHEALTH_CF_API_TOKEN   Cloudflare API token (required to enable)
//	SVCHEALTH_CF_ZONE_ID     Zone ID whose status to report (optional)
//	SVCHEALTH_CF_ACCOUNT_ID  Account ID to enumerate tunnels under (optional)
type Cloudflare struct {
	token     string
	zoneID    string
	accountID string
	client    *http.Client

	mu    sync.Mutex
	cache map[string]cfEntry
	ttl   time.Duration
}

type cfEntry struct {
	healthy bool
	detail  string
	at      time.Time
}

const cfAPI = "https://api.cloudflare.com/client/v4"

// NewCloudflareFromEnv returns a Cloudflare edge reporter, or (nil, nil) if not
// configured. A token plus at least one of zone/account is required.
func NewCloudflareFromEnv() (*Cloudflare, error) {
	token := os.Getenv("SVCHEALTH_CF_API_TOKEN")
	zone := os.Getenv("SVCHEALTH_CF_ZONE_ID")
	account := os.Getenv("SVCHEALTH_CF_ACCOUNT_ID")
	if token == "" || (zone == "" && account == "") {
		return nil, nil
	}
	return &Cloudflare{
		token:     token,
		zoneID:    zone,
		accountID: account,
		client:    &http.Client{Timeout: 12 * time.Second},
		cache:     map[string]cfEntry{},
		ttl:       30 * time.Second,
	}, nil
}

// TunnelHealthy reports zone/tunnel health for host. The host is currently used
// only as a cache key and label; status is reported at the zone/account level.
func (c *Cloudflare) TunnelHealthy(ctx context.Context, host string) (bool, string, error) {
	c.mu.Lock()
	if e, ok := c.cache[host]; ok && time.Since(e.at) < c.ttl {
		c.mu.Unlock()
		return e.healthy, e.detail, nil
	}
	c.mu.Unlock()

	healthy := true
	detail := ""

	if c.zoneID != "" {
		status, err := c.zoneStatus(ctx)
		if err != nil {
			return false, "", err
		}
		if status != "active" {
			healthy = false
		}
		detail = "zone=" + status
	}

	if c.accountID != "" {
		up, total, err := c.tunnelCounts(ctx)
		if err != nil {
			return false, "", err
		}
		if total > 0 && up == 0 {
			healthy = false
		}
		if detail != "" {
			detail += " "
		}
		detail += fmt.Sprintf("tunnels=%d/%d", up, total)
	}

	if detail == "" {
		detail = "n/a"
	}

	c.mu.Lock()
	c.cache[host] = cfEntry{healthy: healthy, detail: detail, at: time.Now()}
	c.mu.Unlock()
	return healthy, detail, nil
}

func (c *Cloudflare) zoneStatus(ctx context.Context) (string, error) {
	var out struct {
		Success bool `json:"success"`
		Result  struct {
			Status string `json:"status"`
		} `json:"result"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.get(ctx, fmt.Sprintf("%s/zones/%s", cfAPI, c.zoneID), &out); err != nil {
		return "", err
	}
	if !out.Success {
		if len(out.Errors) > 0 {
			return "", fmt.Errorf("cloudflare zone: %s", out.Errors[0].Message)
		}
		return "", fmt.Errorf("cloudflare zone: request not successful")
	}
	return out.Result.Status, nil
}

// tunnelCounts returns (healthy, total) cloudflared tunnels for the account.
func (c *Cloudflare) tunnelCounts(ctx context.Context) (int, int, error) {
	var out struct {
		Success bool `json:"success"`
		Result  []struct {
			Status string `json:"status"`
		} `json:"result"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	// is_deleted=false keeps the list to live tunnels only.
	url := fmt.Sprintf("%s/accounts/%s/cfd_tunnel?is_deleted=false", cfAPI, c.accountID)
	if err := c.get(ctx, url, &out); err != nil {
		return 0, 0, err
	}
	if !out.Success {
		if len(out.Errors) > 0 {
			return 0, 0, fmt.Errorf("cloudflare tunnels: %s", out.Errors[0].Message)
		}
		return 0, 0, fmt.Errorf("cloudflare tunnels: request not successful")
	}
	up := 0
	for _, t := range out.Result {
		if t.Status == "healthy" {
			up++
		}
	}
	return up, len(out.Result), nil
}

func (c *Cloudflare) get(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cloudflare api: status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
