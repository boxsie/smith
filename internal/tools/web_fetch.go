package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type webFetchInput struct {
	URL string `json:"url"`
}

type webFetchOutput struct {
	FinalURL    string  `json:"final_url"`
	Status      int     `json:"status"`
	ContentType *string `json:"content_type"`
	Title       *string `json:"title"`
	Body        string  `json:"body"`
}

func webFetchNative(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error) {
	var req webFetchInput
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	normalizedURL, err := normalizeURLString(strings.TrimSpace(req.URL))
	if err != nil {
		return nil, err
	}

	resp, err := doWebRequest(ctx, normalizedURL)
	if err != nil {
		return nil, err
	}
	if isClearlyBinary(resp.ContentType, resp.Body) {
		return nil, fmt.Errorf("binary responses are not supported")
	}

	var contentType *string
	if ct := strings.TrimSpace(resp.ContentType); ct != "" {
		contentType = &ct
	}

	var title *string
	if isHTMLContent(resp.ContentType, resp.Body) {
		title = extractHTMLTitle(resp.Body)
	}

	body := strings.ToValidUTF8(string(resp.Body), "\uFFFD")
	return json.Marshal(webFetchOutput{
		FinalURL:    resp.FinalURL,
		Status:      resp.Status,
		ContentType: contentType,
		Title:       title,
		Body:        body,
	})
}
