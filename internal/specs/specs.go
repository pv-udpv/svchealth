// Package specs discovers API specifications (OpenAPI / Swagger / JSON Schema)
// by URI and derives concrete health-check targets from them.
package specs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Kind classifies a discovered spec document.
type Kind string

const (
	KindOpenAPI    Kind = "openapi"
	KindSwagger    Kind = "swagger"
	KindJSONSchema Kind = "jsonschema"
	KindUnknown    Kind = "unknown"
)

// Spec is the parsed result of discovery.
type Spec struct {
	Kind    Kind
	Title   string
	Version string
	BaseURL string
	// Targets are derived health-check URLs (absolute).
	Targets []Target
}

// Target is a single derived health-check endpoint.
type Target struct {
	Method  string
	Path    string
	URL     string
	Summary string
	// Priority: lower is checked first / preferred (health/ready paths win).
	Priority int
}

// commonHealthPaths are probed first when deriving targets.
var commonHealthPaths = []string{"/health", "/healthz", "/readyz", "/livez", "/ping", "/status"}

// Discover fetches the spec at uri and parses it into a Spec.
func Discover(ctx context.Context, uri string, headers map[string]string, timeout time.Duration) (*Spec, error) {
	body, err := fetch(ctx, uri, headers, timeout)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("spec %q is not valid JSON: %w", uri, err)
	}
	s := &Spec{Kind: detectKind(doc)}
	s.Title, s.Version = titleVersion(doc)
	s.BaseURL = baseURL(uri, doc)
	switch s.Kind {
	case KindOpenAPI, KindSwagger:
		s.Targets = deriveFromPaths(doc, s.BaseURL)
	case KindJSONSchema:
		// JSON Schema alone has no paths; treat the base host's common health
		// paths as candidate targets.
		s.Targets = deriveCommon(s.BaseURL)
	default:
		s.Targets = deriveCommon(s.BaseURL)
	}
	sort.SliceStable(s.Targets, func(i, j int) bool {
		if s.Targets[i].Priority != s.Targets[j].Priority {
			return s.Targets[i].Priority < s.Targets[j].Priority
		}
		return s.Targets[i].Path < s.Targets[j].Path
	})
	return s, nil
}

func fetch(ctx context.Context, uri string, headers map[string]string, timeout time.Duration) ([]byte, error) {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch spec %q: %w", uri, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch spec %q: status %d", uri, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
}

func detectKind(doc map[string]any) Kind {
	if _, ok := doc["openapi"]; ok {
		return KindOpenAPI
	}
	if _, ok := doc["swagger"]; ok {
		return KindSwagger
	}
	if v, ok := doc["$schema"]; ok {
		if s, _ := v.(string); strings.Contains(s, "json-schema") {
			return KindJSONSchema
		}
		return KindJSONSchema
	}
	if _, ok := doc["paths"]; ok {
		return KindOpenAPI
	}
	return KindUnknown
}

func titleVersion(doc map[string]any) (title, version string) {
	if info, ok := doc["info"].(map[string]any); ok {
		title, _ = info["title"].(string)
		version, _ = info["version"].(string)
	}
	if title == "" {
		title, _ = doc["title"].(string)
	}
	return title, version
}

// baseURL resolves the server base for OpenAPI/Swagger, falling back to the
// spec's own origin.
func baseURL(specURI string, doc map[string]any) string {
	// OpenAPI 3: servers[0].url
	if servers, ok := doc["servers"].([]any); ok && len(servers) > 0 {
		if sm, ok := servers[0].(map[string]any); ok {
			if u, _ := sm["url"].(string); u != "" {
				if abs := absolutize(specURI, u); abs != "" {
					return strings.TrimRight(abs, "/")
				}
			}
		}
	}
	// Swagger 2: host + basePath + schemes
	if host, _ := doc["host"].(string); host != "" {
		scheme := "https"
		if schemes, ok := doc["schemes"].([]any); ok && len(schemes) > 0 {
			if s, _ := schemes[0].(string); s != "" {
				scheme = s
			}
		}
		base := scheme + "://" + host
		if bp, _ := doc["basePath"].(string); bp != "" {
			base += bp
		}
		return strings.TrimRight(base, "/")
	}
	// Fallback: the spec's own origin.
	if u, err := url.Parse(specURI); err == nil {
		return strings.TrimRight(u.Scheme+"://"+u.Host, "/")
	}
	return ""
}

func absolutize(specURI, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	base, err := url.Parse(specURI)
	if err != nil {
		return ""
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return base.ResolveReference(rel).String()
}

// deriveFromPaths walks OpenAPI/Swagger paths and produces GET targets,
// prioritizing health-like routes and routes without path parameters.
func deriveFromPaths(doc map[string]any, base string) []Target {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		return deriveCommon(base)
	}
	var out []Target
	for p, item := range paths {
		im, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// Prefer GET for health probing.
		if _, ok := im["get"]; !ok {
			continue
		}
		summary := ""
		if g, ok := im["get"].(map[string]any); ok {
			summary, _ = g["summary"].(string)
		}
		out = append(out, Target{
			Method:   "GET",
			Path:     p,
			URL:      base + p,
			Summary:  summary,
			Priority: pathPriority(p),
		})
	}
	if len(out) == 0 {
		return deriveCommon(base)
	}
	return out
}

func deriveCommon(base string) []Target {
	var out []Target
	for _, p := range commonHealthPaths {
		out = append(out, Target{Method: "GET", Path: p, URL: base + p, Priority: 0})
	}
	return out
}

// pathPriority ranks health-like and parameter-free paths higher (lower value).
func pathPriority(p string) int {
	lp := strings.ToLower(p)
	for _, h := range commonHealthPaths {
		if lp == h || strings.HasSuffix(lp, h) {
			return 0
		}
	}
	if strings.Contains(p, "{") { // parameterized path, harder to probe
		return 20
	}
	return 10
}
