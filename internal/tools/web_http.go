package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	webRequestTimeout  = 30 * time.Second
	webRedirectLimit   = 5
	webMaxResponseBody = 1 << 20
)

var webHTTPClientFactory = defaultWebHTTPClient

type webHTTPResponse struct {
	FinalURL    string
	Status      int
	ContentType string
	Body        []byte
}

func defaultWebHTTPClient() *http.Client {
	return &http.Client{
		Timeout: webRequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= webRedirectLimit {
				return fmt.Errorf("redirect limit exceeded")
			}
			return nil
		},
	}
}

func newWebHTTPClient() *http.Client {
	return webHTTPClientFactory()
}

// SetWebHTTPClientFactoryForTesting overrides the shared HTTP client factory
// used by native web tools and returns a restore function.
func SetWebHTTPClientFactoryForTesting(factory func() *http.Client) func() {
	oldFactory := webHTTPClientFactory
	webHTTPClientFactory = factory
	return func() {
		webHTTPClientFactory = oldFactory
	}
}

func doWebRequest(ctx context.Context, rawURL string) (*webHTTPResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := newWebHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readBoundedBody(resp.Body, webMaxResponseBody)
	if err != nil {
		return nil, err
	}

	return &webHTTPResponse{
		FinalURL:    resp.Request.URL.String(),
		Status:      resp.StatusCode,
		ContentType: strings.TrimSpace(resp.Header.Get("Content-Type")),
		Body:        body,
	}, nil
}

func readBoundedBody(r io.Reader, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(r, maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxBytes)
	}
	return body, nil
}

func normalizeURLString(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("url must be absolute")
	}
	return parsed.String(), nil
}

func normalizeLookupMaxResults(value int) int {
	if value <= 0 {
		return 5
	}
	if value > 10 {
		return 10
	}
	return value
}

func normalizeText(raw string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(html.UnescapeString(raw))), " ")
}

var htmlTagRE = regexp.MustCompile(`(?s)<[^>]+>`)
var titleRE = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func stripHTML(raw string) string {
	return normalizeText(htmlTagRE.ReplaceAllString(raw, " "))
}

func extractHTMLTitle(body []byte) *string {
	matches := titleRE.FindSubmatch(body)
	if len(matches) < 2 {
		return nil
	}
	title := normalizeText(string(matches[1]))
	if title == "" {
		return nil
	}
	return &title
}

func effectiveContentType(header string, body []byte) string {
	if header != "" {
		return header
	}
	if len(body) == 0 {
		return ""
	}
	sniffLen := min(len(body), 512)
	return http.DetectContentType(body[:sniffLen])
}

func isHTMLContent(contentType string, body []byte) bool {
	ct := effectiveContentType(contentType, body)
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		mediaType = ct
	}
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return true
	}
	trimmed := strings.TrimSpace(strings.ToLower(string(body)))
	return strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html")
}

func isClearlyBinary(contentType string, body []byte) bool {
	ct := effectiveContentType(contentType, body)
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		mediaType = ct
	}
	mediaType = strings.ToLower(mediaType)

	switch {
	case mediaType == "":
		return false
	case strings.HasPrefix(mediaType, "text/"):
		return false
	case strings.HasSuffix(mediaType, "+json"), strings.HasSuffix(mediaType, "+xml"):
		return false
	case mediaType == "application/json",
		mediaType == "application/xml",
		mediaType == "application/xhtml+xml",
		mediaType == "application/javascript",
		mediaType == "application/x-javascript",
		mediaType == "image/svg+xml":
		return false
	default:
		return true
	}
}
