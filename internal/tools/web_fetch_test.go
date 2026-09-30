package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFetchFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", "web.fetch", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fetch fixture %s: %v", name, err)
	}
	return data
}

func TestWebFetchNative_HTMLPage(t *testing.T) {
	fixture := readFetchFixture(t, "page-200.html")
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return newTestWebResponse(req, http.StatusOK, "text/html; charset=utf-8", fixture, nil), nil
	}))

	out, err := webFetchNative(context.Background(), json.RawMessage(`{"url":"https://example.com/article"}`), nil, nil)
	if err != nil {
		t.Fatalf("webFetchNative: %v", err)
	}

	var result webFetchOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if result.Status != 200 {
		t.Fatalf("status = %d, want 200", result.Status)
	}
	if result.FinalURL != "https://example.com/article" {
		t.Fatalf("final_url = %q, want %q", result.FinalURL, "https://example.com/article")
	}
	if result.Title == nil || *result.Title != "Article Page" {
		t.Fatalf("title = %v, want Article Page", result.Title)
	}
	if result.ContentType == nil || !strings.Contains(*result.ContentType, "text/html") {
		t.Fatalf("content_type = %v, want text/html", result.ContentType)
	}
	if !strings.Contains(result.Body, "Example Article") {
		t.Fatalf("body missing expected content: %q", result.Body)
	}
}

func TestWebFetchNative_Redirect(t *testing.T) {
	targetFixture := readFetchFixture(t, "redirect-target.html")
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/redirect":
			return newTestWebResponse(req, http.StatusFound, "text/html; charset=utf-8", nil, map[string]string{
				"Location": "/target",
			}), nil
		case "/target":
			return newTestWebResponse(req, http.StatusOK, "text/html; charset=utf-8", targetFixture, nil), nil
		default:
			return newTestWebResponse(req, http.StatusNotFound, "text/plain; charset=utf-8", []byte("not found"), nil), nil
		}
	}))

	out, err := webFetchNative(context.Background(), json.RawMessage(`{"url":"https://example.com/redirect"}`), nil, nil)
	if err != nil {
		t.Fatalf("webFetchNative: %v", err)
	}

	var result webFetchOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if result.FinalURL != "https://example.com/target" {
		t.Fatalf("final_url = %q, want %q", result.FinalURL, "https://example.com/target")
	}
	if result.Status != 200 {
		t.Fatalf("status = %d, want 200", result.Status)
	}
}

func TestWebFetchNative_404IsSuccessful(t *testing.T) {
	fixture := readFetchFixture(t, "page-404.html")
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return newTestWebResponse(req, http.StatusNotFound, "text/html; charset=utf-8", fixture, nil), nil
	}))

	out, err := webFetchNative(context.Background(), json.RawMessage(`{"url":"https://example.com/missing"}`), nil, nil)
	if err != nil {
		t.Fatalf("webFetchNative: %v", err)
	}

	var result webFetchOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if result.Status != 404 {
		t.Fatalf("status = %d, want 404", result.Status)
	}
	if result.Title == nil || *result.Title != "Missing Page" {
		t.Fatalf("title = %v, want Missing Page", result.Title)
	}
}

func TestWebFetchNative_LargeBodyRejected(t *testing.T) {
	largeBody := strings.Repeat("a", webMaxResponseBody+1)
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return newTestWebResponse(req, http.StatusOK, "text/plain", []byte(largeBody), nil), nil
	}))

	_, err := webFetchNative(context.Background(), json.RawMessage(`{"url":"https://example.com/large"}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected body cap error, got %v", err)
	}
}

func TestWebFetchNative_BinaryRejected(t *testing.T) {
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return newTestWebResponse(req, http.StatusOK, "image/png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, nil), nil
	}))

	_, err := webFetchNative(context.Background(), json.RawMessage(`{"url":"https://example.com/image.png"}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary rejection, got %v", err)
	}
}
