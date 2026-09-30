package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readLookupFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", "web.lookup", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lookup fixture %s: %v", name, err)
	}
	return data
}

func setupLookupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func writeLookupConfig(t *testing.T, home, contents string) {
	t.Helper()
	configDir := filepath.Join(home, ".smith")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestBuildDuckDuckGoHTMLURL_EncodesQuery(t *testing.T) {
	raw, err := buildDuckDuckGoHTMLURL("https://html.duckduckgo.com/html/", "C&C cafe unicode cafe")
	if err != nil {
		t.Fatalf("buildDuckDuckGoHTMLURL: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse result url: %v", err)
	}
	if got := parsed.Query().Get("q"); got != "C&C cafe unicode cafe" {
		t.Fatalf("q = %q", got)
	}
	if !strings.Contains(parsed.RawQuery, "C%26C+cafe+unicode+cafe") {
		t.Fatalf("raw query = %q, expected encoded ampersand/spaces", parsed.RawQuery)
	}
}

func TestNormalizeLookupMaxResults(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{0, 5},
		{-1, 5},
		{1, 1},
		{10, 10},
		{99, 10},
	}
	for _, tt := range tests {
		if got := normalizeLookupMaxResults(tt.in); got != tt.want {
			t.Fatalf("normalizeLookupMaxResults(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseDuckDuckGoHTMLResults_Success(t *testing.T) {
	results, err := parseDuckDuckGoHTMLResults(readLookupFixture(t, "duckduckgo-html-results.html"), 5)
	if err != nil {
		t.Fatalf("parseDuckDuckGoHTMLResults: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if results[0].Title != "Alpha Result" || results[0].URL != "https://example.com/alpha" {
		t.Fatalf("unexpected first result: %+v", results[0])
	}
	if results[1].URL != "https://example.com/beta" {
		t.Fatalf("unexpected second result URL: %+v", results[1])
	}
	if !strings.Contains(results[1].Snippet, "second result") {
		t.Fatalf("unexpected second result snippet: %+v", results[1])
	}
}

func TestParseDuckDuckGoHTMLResults_Empty(t *testing.T) {
	results, err := parseDuckDuckGoHTMLResults(readLookupFixture(t, "duckduckgo-html-empty.html"), 5)
	if err != nil {
		t.Fatalf("parse empty results: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0", len(results))
	}
}

func TestParseDuckDuckGoHTMLResults_ParseFailure(t *testing.T) {
	_, err := parseDuckDuckGoHTMLResults(readLookupFixture(t, "duckduckgo-html-parse-failure.html"), 5)
	if err == nil || !strings.Contains(err.Error(), "failed to parse") {
		t.Fatalf("expected parse failure, got %v", err)
	}
}

func TestUnwrapDuckDuckGoRedirect(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"uddg redirect", "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&rut=abc", "https://example.com/page"},
		{"direct http URL", "https://example.com/direct", "https://example.com/direct"},
		{"no uddg no http", "/some/relative/path", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unwrapDuckDuckGoRedirect(tt.raw)
			if got != tt.want {
				t.Fatalf("unwrapDuckDuckGoRedirect(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestIsDuckDuckGoChallengePage(t *testing.T) {
	if !isDuckDuckGoChallengePage(readLookupFixture(t, "duckduckgo-challenge.html")) {
		t.Fatal("expected challenge page to be detected")
	}
	if isDuckDuckGoChallengePage(readLookupFixture(t, "duckduckgo-html-results.html")) {
		t.Fatal("did not expect results page to be detected as challenge")
	}
}

func TestWebLookupNative_UsesHTTPAndTrimsResults(t *testing.T) {
	setupLookupHome(t)
	fixture := readLookupFixture(t, "duckduckgo-html-results.html")
	var gotRawQuery string
	var gotUserAgent string
	withTestWebHTTPClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotRawQuery = r.URL.RawQuery
		gotUserAgent = r.Header.Get("User-Agent")
		return newTestWebResponse(r, http.StatusOK, "text/html; charset=utf-8", fixture, nil), nil
	}))

	oldURL := duckDuckGoHTMLSearchURL
	duckDuckGoHTMLSearchURL = "https://duckduckgo.test/html/"
	defer func() { duckDuckGoHTMLSearchURL = oldURL }()

	out, err := webLookupNative(context.Background(), json.RawMessage(`{"query":"alpha & beta","max_results":1}`), nil, nil)
	if err != nil {
		t.Fatalf("webLookupNative: %v", err)
	}
	if !strings.Contains(gotRawQuery, "alpha+%26+beta") {
		t.Fatalf("raw query = %q, expected encoded query", gotRawQuery)
	}
	if !strings.Contains(gotUserAgent, "Mozilla") {
		t.Fatalf("User-Agent = %q, expected browser-like UA", gotUserAgent)
	}

	var result webLookupOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(result.Results) = %d, want 1", len(result.Results))
	}
	if result.Results[0].Title != "Alpha Result" {
		t.Fatalf("unexpected result: %+v", result.Results[0])
	}
	if result.Results[0].URL != "https://example.com/alpha" {
		t.Fatalf("expected unwrapped URL, got: %s", result.Results[0].URL)
	}
}

func TestWebLookupNative_ReturnsErrorOnDuckDuckGoChallenge(t *testing.T) {
	setupLookupHome(t)
	ddgFixture := readLookupFixture(t, "duckduckgo-challenge.html")

	withTestWebHTTPClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return newTestWebResponse(r, http.StatusAccepted, "text/html; charset=utf-8", ddgFixture, nil), nil
	}))

	oldDDGURL := duckDuckGoHTMLSearchURL
	duckDuckGoHTMLSearchURL = "https://duckduckgo.test/html/"
	defer func() { duckDuckGoHTMLSearchURL = oldDDGURL }()

	_, err := webLookupNative(context.Background(), json.RawMessage(`{"query":"alpha","max_results":2}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "bot challenge") {
		t.Fatalf("expected bot challenge error, got: %v", err)
	}
}

func TestWebLookupNative_UsesSearXNGWhenConfigured(t *testing.T) {
	home := setupLookupHome(t)
	writeLookupConfig(t, home, `{"searxng_endpoint":"https://search.example/searxng"}`)

	var gotPath string
	var gotRawQuery string
	withTestWebHTTPClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		body := []byte(`{"results":[{"url":"https://example.com/alpha","title":"Alpha Result","content":"First result"},{"url":"","title":"Skip Me","content":"Missing URL"}]}`)
		return newTestWebResponse(r, http.StatusOK, "application/json", body, nil), nil
	}))

	out, err := webLookupNative(context.Background(), json.RawMessage(`{"query":"alpha & beta","max_results":5}`), nil, nil)
	if err != nil {
		t.Fatalf("webLookupNative: %v", err)
	}
	if gotPath != "/searxng/search" {
		t.Fatalf("path = %q, want /searxng/search", gotPath)
	}
	if !strings.Contains(gotRawQuery, "q=alpha+%26+beta") {
		t.Fatalf("raw query = %q, expected encoded query", gotRawQuery)
	}
	if !strings.Contains(gotRawQuery, "format=json") {
		t.Fatalf("raw query = %q, expected format=json", gotRawQuery)
	}

	var result webLookupOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(result.Results) = %d, want 1", len(result.Results))
	}
	if result.Results[0].Title != "Alpha Result" || result.Results[0].URL != "https://example.com/alpha" {
		t.Fatalf("unexpected result: %+v", result.Results[0])
	}
}
