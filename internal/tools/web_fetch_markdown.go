package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	readability "codeberg.org/readeck/go-readability/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"golang.org/x/net/html"
)

const webMaxMarkdownBytes = 64 << 10

type webFetchMarkdownInput struct {
	URL string `json:"url"`
}

type webFetchMarkdownOutput struct {
	FinalURL string   `json:"final_url"`
	Status   int      `json:"status"`
	Title    *string  `json:"title"`
	Markdown string   `json:"markdown"`
	Warnings []string `json:"warnings"`
}

func webFetchMarkdownNative(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error) {
	var req webFetchMarkdownInput
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

	title := extractBestEffortHTMLTitle(resp.ContentType, resp.Body)
	if resp.Status < http.StatusOK || resp.Status >= http.StatusMultipleChoices {
		return json.Marshal(webFetchMarkdownOutput{
			FinalURL: resp.FinalURL,
			Status:   resp.Status,
			Title:    title,
			Markdown: "",
			Warnings: []string{fmt.Sprintf("upstream returned HTTP %d", resp.Status)},
		})
	}
	if !isHTMLContent(resp.ContentType, resp.Body) {
		return nil, fmt.Errorf("readable markdown requires an HTML response")
	}

	markdown, readableTitle, warnings, err := extractReadableMarkdown(resp.FinalURL, resp.Body)
	if err != nil {
		return nil, err
	}
	if readableTitle != nil {
		title = readableTitle
	}

	return json.Marshal(webFetchMarkdownOutput{
		FinalURL: resp.FinalURL,
		Status:   resp.Status,
		Title:    title,
		Markdown: markdown,
		Warnings: warnings,
	})
}

func extractReadableMarkdown(finalURL string, body []byte) (string, *string, []string, error) {
	pageURL, err := url.Parse(finalURL)
	if err != nil {
		return "", nil, nil, fmt.Errorf("parse final url: %w", err)
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", nil, nil, fmt.Errorf("parse html document: %w", err)
	}

	parser := readability.NewParser()
	isReadable := parser.CheckDocument(doc)

	article, err := parser.ParseDocument(doc, pageURL)
	if err != nil {
		return "", nil, nil, fmt.Errorf("extract readable content: %w", err)
	}
	if article.Node == nil {
		return "", nil, nil, fmt.Errorf("extract readable content: article body was empty")
	}

	var articleHTML bytes.Buffer
	if err := article.RenderHTML(&articleHTML); err != nil {
		return "", nil, nil, fmt.Errorf("render readable html: %w", err)
	}

	markdown, err := htmltomarkdown.ConvertString(articleHTML.String(), converter.WithDomain(finalURL))
	if err != nil {
		return "", nil, nil, fmt.Errorf("convert readable html to markdown: %w", err)
	}
	markdown = strings.TrimSpace(strings.ToValidUTF8(markdown, "\uFFFD"))
	if markdown == "" {
		return "", nil, nil, fmt.Errorf("readable markdown extraction returned empty output")
	}

	title := normalizeOptionalString(article.Title())
	if title == nil {
		title = extractHTMLTitle(body)
	}

	warnings := []string{}
	if !isReadable && hasBoilerplateSignals(body) {
		warnings = append(warnings, "page structure was weak; markdown may be incomplete")
	}

	truncated, wasTruncated := truncateUTF8String(markdown, webMaxMarkdownBytes)
	if wasTruncated {
		markdown = strings.TrimRightFunc(truncated, unicode.IsSpace)
		warnings = append(warnings, fmt.Sprintf("markdown truncated to %d bytes", webMaxMarkdownBytes))
	}

	return markdown, title, warnings, nil
}

func extractBestEffortHTMLTitle(contentType string, body []byte) *string {
	if !isHTMLContent(contentType, body) {
		return nil
	}
	return extractHTMLTitle(body)
}

func normalizeOptionalString(raw string) *string {
	value := normalizeText(raw)
	if value == "" {
		return nil
	}
	return &value
}

func truncateUTF8String(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}

	truncated := value[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated, true
}

func hasBoilerplateSignals(body []byte) bool {
	content := strings.ToLower(string(body))
	signals := 0
	for _, needle := range []string{"<nav", "<aside", "<header", "<footer", "sidebar"} {
		if strings.Contains(content, needle) {
			signals++
		}
	}
	return signals >= 2
}
