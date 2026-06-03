package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// Linear implements Notifier: it opens a Linear issue when an endpoint stays
// DOWN and posts a comment when it recovers. Idempotent per endpoint within a
// process via an in-memory issue-id cache.
//
// Env:
//
//	SVCHEALTH_LINEAR_API_KEY  Linear personal API key (required to enable)
//	SVCHEALTH_LINEAR_TEAM_ID  Linear team UUID to create issues under (required)
type Linear struct {
	apiKey string
	teamID string
	client *http.Client

	mu      sync.Mutex
	openIDs map[string]string // endpoint -> open issue id
}

const linearAPI = "https://api.linear.app/graphql"

// NewLinearFromEnv returns a Linear notifier, or (nil, nil) if not configured.
func NewLinearFromEnv() (*Linear, error) {
	key := os.Getenv("SVCHEALTH_LINEAR_API_KEY")
	team := os.Getenv("SVCHEALTH_LINEAR_TEAM_ID")
	if key == "" || team == "" {
		return nil, nil
	}
	return &Linear{
		apiKey:  key,
		teamID:  team,
		client:  &http.Client{Timeout: 12 * time.Second},
		openIDs: map[string]string{},
	}, nil
}

// OnSustainedDown creates a Linear issue (once per endpoint per outage).
func (l *Linear) OnSustainedDown(ctx context.Context, endpoint string, streak int, last CheckSummary) error {
	l.mu.Lock()
	if _, exists := l.openIDs[endpoint]; exists {
		l.mu.Unlock()
		return nil // already filed for this outage
	}
	l.mu.Unlock()

	title := fmt.Sprintf("[svchealth] %s is DOWN", endpoint)
	desc := fmt.Sprintf("Endpoint **%s** has been DOWN for %d consecutive checks.\n\n"+
		"- URL: %s\n- Last HTTP status: %d\n- Last latency: %dms\n- Error: %s\n\n_Filed automatically by svchealth._",
		endpoint, streak, last.TargetURL, last.HTTPStatus, last.LatencyMs, orNone(last.Err))

	const q = `mutation($title:String!,$desc:String!,$team:String!){
		issueCreate(input:{title:$title, description:$desc, teamId:$team}){
			success issue{ id identifier }
		}
	}`
	var out struct {
		Data struct {
			IssueCreate struct {
				Success bool `json:"success"`
				Issue   struct {
					ID         string `json:"id"`
					Identifier string `json:"identifier"`
				} `json:"issue"`
			} `json:"issueCreate"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := l.do(ctx, q, map[string]any{"title": title, "desc": desc, "team": l.teamID}, &out); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("linear issueCreate: %s", out.Errors[0].Message)
	}
	if !out.Data.IssueCreate.Success {
		return fmt.Errorf("linear issueCreate: not successful")
	}
	l.mu.Lock()
	l.openIDs[endpoint] = out.Data.IssueCreate.Issue.ID
	l.mu.Unlock()
	return nil
}

// OnRecovered posts a recovery comment and forgets the issue id.
func (l *Linear) OnRecovered(ctx context.Context, endpoint string) error {
	l.mu.Lock()
	id, ok := l.openIDs[endpoint]
	delete(l.openIDs, endpoint)
	l.mu.Unlock()
	if !ok {
		return nil
	}
	const q = `mutation($id:String!,$body:String!){
		commentCreate(input:{issueId:$id, body:$body}){ success }
	}`
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	body := fmt.Sprintf("svchealth: endpoint **%s** has RECOVERED at %s.", endpoint, time.Now().UTC().Format(time.RFC3339))
	if err := l.do(ctx, q, map[string]any{"id": id, "body": body}, &out); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("linear commentCreate: %s", out.Errors[0].Message)
	}
	return nil
}

func (l *Linear) do(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearAPI, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", l.apiKey) // Linear accepts the raw PAT
	resp, err := l.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("linear api: status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
