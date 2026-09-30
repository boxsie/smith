package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestWebFetchMarkdownNative_ArticlePage(t *testing.T) {
	fixture := readFetchFixture(t, "page-200.html")
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return newTestWebResponse(req, http.StatusOK, "text/html; charset=utf-8", fixture, nil), nil
	}))

	out, err := webFetchMarkdownNative(context.Background(), json.RawMessage(`{"url":"https://example.com/article"}`), nil, nil)
	if err != nil {
		t.Fatalf("webFetchMarkdownNative: %v", err)
	}

	var result webFetchMarkdownOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.Status)
	}
	if result.FinalURL != "https://example.com/article" {
		t.Fatalf("final_url = %q", result.FinalURL)
	}
	if result.Title == nil || *result.Title != "Article Page" {
		t.Fatalf("title = %v, want Article Page", result.Title)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", result.Warnings)
	}
	if !strings.Contains(result.Markdown, "Example Article") {
		t.Fatalf("markdown missing heading: %q", result.Markdown)
	}
	if !strings.Contains(result.Markdown, "canonical 200 response fixture") {
		t.Fatalf("markdown missing body text: %q", result.Markdown)
	}
}

func TestWebFetchMarkdownNative_BoilerplateHeavyPageWarns(t *testing.T) {
	fixture := readFetchFixture(t, "boilerplate-heavy.html")
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return newTestWebResponse(req, http.StatusOK, "text/html; charset=utf-8", fixture, nil), nil
	}))

	out, err := webFetchMarkdownNative(context.Background(), json.RawMessage(`{"url":"https://example.com/noisy"}`), nil, nil)
	if err != nil {
		t.Fatalf("webFetchMarkdownNative: %v", err)
	}

	var result webFetchMarkdownOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if result.Markdown == "" {
		t.Fatalf("markdown = empty, want extracted markdown")
	}
	if !strings.Contains(result.Markdown, "Useful Content Hidden In Noise") {
		t.Fatalf("markdown missing article content: %q", result.Markdown)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("warnings = empty, want weak extraction warning")
	}
}

func TestWebFetchMarkdownNative_404ReturnsWarning(t *testing.T) {
	fixture := readFetchFixture(t, "page-404.html")
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return newTestWebResponse(req, http.StatusNotFound, "text/html; charset=utf-8", fixture, nil), nil
	}))

	out, err := webFetchMarkdownNative(context.Background(), json.RawMessage(`{"url":"https://example.com/missing"}`), nil, nil)
	if err != nil {
		t.Fatalf("webFetchMarkdownNative: %v", err)
	}

	var result webFetchMarkdownOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if result.Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", result.Status)
	}
	if result.Title == nil || *result.Title != "Missing Page" {
		t.Fatalf("title = %v, want Missing Page", result.Title)
	}
	if result.Markdown != "" {
		t.Fatalf("markdown = %q, want empty", result.Markdown)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "404") {
		t.Fatalf("warnings = %v, want 404 warning", result.Warnings)
	}
}

func TestWebFetchMarkdownNative_TruncatesLargeMarkdown(t *testing.T) {
	body := "<!doctype html><html><head><title>Very Large Article</title></head><body><main><article><h1>Very Large Article</h1><p>" +
		strings.Repeat("long paragraph text ", webMaxMarkdownBytes/4) +
		"</p></article></main></body></html>"
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return newTestWebResponse(req, http.StatusOK, "text/html; charset=utf-8", []byte(body), nil), nil
	}))

	out, err := webFetchMarkdownNative(context.Background(), json.RawMessage(`{"url":"https://example.com/large"}`), nil, nil)
	if err != nil {
		t.Fatalf("webFetchMarkdownNative: %v", err)
	}

	var result webFetchMarkdownOutput
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(result.Markdown) > webMaxMarkdownBytes {
		t.Fatalf("markdown length = %d, want <= %d", len(result.Markdown), webMaxMarkdownBytes)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("warnings = empty, want truncation warning")
	}
	if !strings.Contains(strings.Join(result.Warnings, " "), "truncated") {
		t.Fatalf("warnings = %v, want truncation warning", result.Warnings)
	}
}

func TestWebFetchMarkdownNative_UnsupportedResponseRejected(t *testing.T) {
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return newTestWebResponse(req, http.StatusOK, "text/plain; charset=utf-8", []byte("plain text"), nil), nil
	}))

	_, err := webFetchMarkdownNative(context.Background(), json.RawMessage(`{"url":"https://example.com/plain.txt"}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "HTML") {
		t.Fatalf("expected unsupported response error, got %v", err)
	}
}

func TestWebFetchMarkdownNative_BinaryRejected(t *testing.T) {
	withTestWebHTTPClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return newTestWebResponse(req, http.StatusOK, "image/png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, nil), nil
	}))

	_, err := webFetchMarkdownNative(context.Background(), json.RawMessage(`{"url":"https://example.com/image.png"}`), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary rejection, got %v", err)
	}
}
