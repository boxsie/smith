package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/boxsie/smith/internal/config"
)

var duckDuckGoHTMLSearchURL = "https://html.duckduckgo.com/html/"

type webLookupInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type webLookupResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type webLookupOutput struct {
	Results []webLookupResult `json:"results"`
}

var webLookupResultRE = regexp.MustCompile(`(?is)<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>([^<]+)</a>.*?<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)

func webLookupNative(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error) {
	var req webLookupInput
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("query must be non-empty")
	}

	results, err := performWebLookup(ctx, query, normalizeLookupMaxResults(req.MaxResults))
	if err != nil {
		return nil, err
	}

	return json.Marshal(webLookupOutput{Results: results})
}

func buildDuckDuckGoHTMLURL(baseURL, query string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse search url: %w", err)
	}
	values := parsed.Query()
	values.Set("q", query)
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func performWebLookup(ctx context.Context, query string, maxResults int) ([]webLookupResult, error) {
	cfg, _ := config.Load()
	if cfg.SearXNGEndpoint != "" {
		return performSearXNGLookup(ctx, cfg.SearXNGEndpoint, query, maxResults)
	}
	return performDuckDuckGoHTMLLookup(ctx, query, maxResults)
}

// searxngResponse is the top-level JSON response from SearXNG's /search endpoint.
type searxngResponse struct {
	Results []searxngResult `json:"results"`
}

type searxngResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

func performSearXNGLookup(ctx context.Context, endpoint, query string, maxResults int) ([]webLookupResult, error) {
	searchURL, err := buildSearXNGURL(endpoint, query)
	if err != nil {
		return nil, err
	}

	resp, err := doWebRequest(ctx, searchURL)
	if err != nil {
		return nil, fmt.Errorf("searxng request: %w", err)
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("searxng returned HTTP %d", resp.Status)
	}

	var sr searxngResponse
	if err := json.Unmarshal(resp.Body, &sr); err != nil {
		return nil, fmt.Errorf("parse searxng response: %w", err)
	}

	results := make([]webLookupResult, 0, min(len(sr.Results), maxResults))
	for _, r := range sr.Results {
		if len(results) >= maxResults {
			break
		}
		if r.URL == "" {
			continue
		}
		results = append(results, webLookupResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		})
	}
	return results, nil
}

func buildSearXNGURL(endpoint, query string) (string, error) {
	base := strings.TrimRight(endpoint, "/") + "/search"
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse searxng endpoint: %w", err)
	}
	values := parsed.Query()
	values.Set("q", query)
	values.Set("format", "json")
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func performDuckDuckGoHTMLLookup(ctx context.Context, query string, maxResults int) ([]webLookupResult, error) {
	searchURL, err := buildDuckDuckGoHTMLURL(duckDuckGoHTMLSearchURL, query)
	if err != nil {
		return nil, err
	}

	resp, err := doWebRequest(ctx, searchURL)
	if err != nil {
		return nil, err
	}
	if isDuckDuckGoChallengeStatus(resp.Status, resp.Body) {
		return nil, fmt.Errorf("search returned DuckDuckGo bot challenge (HTTP %d)", resp.Status)
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("search returned HTTP %d", resp.Status)
	}

	return parseDuckDuckGoHTMLResults(resp.Body, maxResults)
}


func parseDuckDuckGoHTMLResults(body []byte, maxResults int) ([]webLookupResult, error) {
	matches := webLookupResultRE.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		if isDuckDuckGoHTMLEmptyPage(body) {
			return []webLookupResult{}, nil
		}
		return nil, fmt.Errorf("failed to parse DuckDuckGo HTML results")
	}

	results := make([]webLookupResult, 0, min(len(matches), maxResults))
	for _, match := range matches {
		if len(results) >= maxResults {
			break
		}
		rawURL := strings.TrimSpace(string(match[1]))
		actualURL := unwrapDuckDuckGoRedirect(rawURL)
		if actualURL == "" {
			continue
		}
		results = append(results, webLookupResult{
			Title:   stripHTML(string(match[2])),
			URL:     actualURL,
			Snippet: stripHTML(string(match[3])),
		})
	}
	return results, nil
}

// unwrapDuckDuckGoRedirect extracts the real URL from a DDG redirect like
// //duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&rut=...
func unwrapDuckDuckGoRedirect(raw string) string {
	if strings.Contains(raw, "uddg=") {
		parsed, err := url.Parse(raw)
		if err == nil {
			if uddg := parsed.Query().Get("uddg"); uddg != "" {
				return uddg
			}
		}
	}
	if strings.HasPrefix(raw, "http") {
		return raw
	}
	return ""
}


func isDuckDuckGoHTMLEmptyPage(body []byte) bool {
	content := strings.ToLower(string(body))
	return strings.Contains(content, "no results found") || strings.Contains(content, "no-results") || strings.Contains(content, "no more results")
}

func isDuckDuckGoChallengeStatus(status int, body []byte) bool {
	if status != 202 && status != 403 {
		return false
	}
	return isDuckDuckGoChallengePage(body)
}

func isDuckDuckGoChallengePage(body []byte) bool {
	content := strings.ToLower(string(body))
	return strings.Contains(content, "bots use duckduckgo too") ||
		strings.Contains(content, "anomaly-modal") ||
		strings.Contains(content, "challenge-form") ||
		strings.Contains(content, "anomaly.js?")
}
