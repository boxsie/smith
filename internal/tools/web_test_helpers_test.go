package tools

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func withTestWebHTTPClient(t *testing.T, transport http.RoundTripper) {
	t.Helper()

	oldFactory := webHTTPClientFactory
	webHTTPClientFactory = func() *http.Client {
		client := defaultWebHTTPClient()
		client.Transport = transport
		return client
	}

	t.Cleanup(func() {
		webHTTPClientFactory = oldFactory
	})
}

func newTestWebResponse(req *http.Request, status int, contentType string, body []byte, headers map[string]string) *http.Response {
	resp := &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}
	if contentType != "" {
		resp.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		resp.Header.Set(key, value)
	}
	return resp
}
