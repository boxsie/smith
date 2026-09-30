// Package renderer adapts the memory renderer's versioned read API to Smith's transport-neutral
// memorysource.Reader contract.
package renderer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/boxsie/smith/internal/memorysource"
)

type Config struct {
	Endpoint    string
	BearerToken string
	HTTPClient  *http.Client
}

type Reader struct {
	endpoint string
	token    string
	client   *http.Client
}

func New(config Config) *Reader {
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Reader{endpoint: strings.TrimRight(strings.TrimSpace(config.Endpoint), "/"), token: config.BearerToken, client: client}
}

type UpstreamError struct {
	Status  int
	Code    string
	Message string
}

func (e *UpstreamError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("memory renderer returned HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("memory renderer %s: %s", e.Code, e.Message)
}

func (e *UpstreamError) Unwrap() error { return memorysource.ErrUpstream }

func (r *Reader) Snapshot(ctx context.Context) (memorysource.Snapshot, error) {
	var result memorysource.Snapshot
	err := r.get(ctx, "/api/v1/snapshot", nil, &result)
	return result, err
}

func (r *Reader) List(ctx context.Context, request memorysource.ListRequest) (memorysource.MemoryPage, error) {
	query := url.Values{}
	optional(query, "q", request.Query)
	optional(query, "type", request.Type)
	optional(query, "cursor", request.Cursor)
	positive(query, "limit", request.Limit)
	var result memorysource.MemoryPage
	err := r.get(ctx, "/api/v1/memories", query, &result)
	return result, err
}

func (r *Reader) Get(ctx context.Context, slug string) (memorysource.Memory, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" || strings.ContainsAny(slug, `/\\`) {
		return memorysource.Memory{}, fmt.Errorf("memory slug is required and must not contain a path")
	}
	var result memorysource.Memory
	err := r.get(ctx, "/api/v1/memories/"+url.PathEscape(slug), nil, &result)
	return result, err
}

func (r *Reader) Review(ctx context.Context, request memorysource.ReviewRequest) (memorysource.ReviewPage, error) {
	query := url.Values{}
	optional(query, "shelf", request.Shelf)
	optional(query, "cursor", request.Cursor)
	positive(query, "limit", request.Limit)
	var result memorysource.ReviewPage
	err := r.get(ctx, "/api/v1/review", query, &result)
	return result, err
}

func (r *Reader) Audit(ctx context.Context) (memorysource.AuditState, error) {
	var result memorysource.AuditState
	err := r.get(ctx, "/api/v1/audit", nil, &result)
	return result, err
}

func (r *Reader) Render(ctx context.Context, request memorysource.RenderRequest) (memorysource.RenderArtifact, error) {
	body := strings.TrimSpace(request.Body)
	if body == "" {
		return memorysource.RenderArtifact{}, fmt.Errorf("render body is required")
	}
	query := url.Values{"body": []string{body}}
	optional(query, "context", request.Context)
	var result memorysource.RenderArtifact
	err := r.get(ctx, "/api/v1/render", query, &result)
	return result, err
}

func (r *Reader) get(ctx context.Context, path string, query url.Values, target any) error {
	if r == nil || r.endpoint == "" {
		return memorysource.ErrUnavailable
	}
	endpoint := r.endpoint + path
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build memory renderer request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if r.token != "" {
		request.Header.Set("Authorization", "Bearer "+r.token)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: read memory renderer: %v", memorysource.ErrUpstream, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&envelope)
		if envelope.Error.Message == "" {
			envelope.Error.Message = response.Status
		}
		return &UpstreamError{Status: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("%w: decode memory renderer response: %v", memorysource.ErrUpstream, err)
	}
	return nil
}

func optional(values url.Values, name, value string) {
	if value = strings.TrimSpace(value); value != "" {
		values.Set(name, value)
	}
}

func positive(values url.Values, name string, value int) {
	if value > 0 {
		values.Set(name, strconv.Itoa(value))
	}
}
