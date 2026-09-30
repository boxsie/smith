package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupNativeTool(t *testing.T, version string, fn NativeFunc, extra map[string]string) (*NativeToolHandler, string) {
	t.Helper()
	root := t.TempDir()
	toolDir := filepath.Join(root, "tools", "web.lookup")
	files := map[string]string{
		"tool.yaml":         "description: native tool\ntype: native\ncache: auto\n",
		"input.schema.json": `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`,
	}
	for k, v := range extra {
		files[k] = v
	}
	writeToolDir(t, toolDir, files)

	def, err := ParseToolDir(toolDir)
	if err != nil {
		t.Fatalf("parse tool dir: %v", err)
	}
	def.ID = "web.lookup"

	handler, err := NewNativeToolHandler(def, root, version, fn)
	if err != nil {
		t.Fatalf("new native handler: %v", err)
	}
	return handler, root
}

func TestNativeToolHandler_ValidExecution(t *testing.T) {
	handler, root := setupNativeTool(t, "v1", func(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error) {
		return json.RawMessage(`{"result":"ok"}`), nil
	}, nil)

	scope := map[string]string{ScopeRoot: root}
	out, err := handler.Execute(context.Background(), json.RawMessage(`{"query":"smith"}`), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if result["result"] != "ok" {
		t.Fatalf("result = %v", result)
	}
}

func TestNativeToolHandler_EnvFiltering(t *testing.T) {
	handler, root := setupNativeTool(t, "v1", func(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error) {
		payload, err := json.Marshal(map[string]any{
			"keys": env,
		})
		if err != nil {
			return nil, err
		}
		return payload, nil
	}, map[string]string{
		"tool.yaml": "description: native tool\ntype: native\ncache: auto\nenv:\n  - API_TOKEN\n",
	})

	t.Setenv("API_TOKEN", "visible")
	t.Setenv("SECRET_KEY", "hidden")

	scope := map[string]string{ScopeRoot: root}
	out, err := handler.Execute(context.Background(), json.RawMessage(`{"query":"smith"}`), scope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result struct {
		Keys map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(result.Keys) != 1 || result.Keys["API_TOKEN"] != "visible" {
		t.Fatalf("env = %v, want only API_TOKEN", result.Keys)
	}
	if _, ok := result.Keys["SECRET_KEY"]; ok {
		t.Fatal("SECRET_KEY should not be visible to native handler")
	}
}

func TestNativeToolHandler_InputValidation(t *testing.T) {
	handler, root := setupNativeTool(t, "v1", func(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error) {
		return json.RawMessage(`{"result":"ok"}`), nil
	}, nil)

	scope := map[string]string{ScopeRoot: root}
	_, err := handler.Execute(context.Background(), json.RawMessage(`{"wrong":"field"}`), scope)
	if err == nil || !strings.Contains(err.Error(), "input validation") {
		t.Fatalf("expected input validation error, got: %v", err)
	}
}

func TestNativeToolHandler_CacheKeyIncludesVersion(t *testing.T) {
	handlerV1, _ := setupNativeTool(t, "v1", func(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error) {
		return json.RawMessage(`{"result":"ok"}`), nil
	}, nil)
	handlerV2, _ := setupNativeTool(t, "v2", func(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error) {
		return json.RawMessage(`{"result":"ok"}`), nil
	}, nil)

	canonical, err := canonicalizeJSON(json.RawMessage(`{"query":"smith"}`))
	if err != nil {
		t.Fatalf("canonicalize input: %v", err)
	}

	key1 := handlerV1.cacheKey(canonical, map[string]string{})
	key2 := handlerV2.cacheKey(canonical, map[string]string{})
	if key1 == key2 {
		t.Fatal("cache keys should differ when handler version changes")
	}
}

func TestNativeToolHandler_VersionBumpInvalidatesCache(t *testing.T) {
	root := t.TempDir()
	counterFile := filepath.Join(root, "counter")
	if err := os.WriteFile(counterFile, []byte("0"), 0o644); err != nil {
		t.Fatalf("write counter: %v", err)
	}

	makeHandler := func(version string) *NativeToolHandler {
		toolDir := filepath.Join(root, "tools", "web.lookup")
		writeToolDir(t, toolDir, map[string]string{
			"tool.yaml":         "description: native tool\ntype: native\ncache: auto\n",
			"input.schema.json": `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`,
		})

		def, err := ParseToolDir(toolDir)
		if err != nil {
			t.Fatalf("parse tool dir: %v", err)
		}
		def.ID = "web.lookup"

		handler, err := NewNativeToolHandler(def, root, version, func(ctx context.Context, input json.RawMessage, scope map[string]string, env map[string]string) (json.RawMessage, error) {
			data, err := os.ReadFile(counterFile)
			if err != nil {
				return nil, err
			}
			count := int(data[0]-'0') + 1
			if err := os.WriteFile(counterFile, []byte{byte('0' + count)}, 0o644); err != nil {
				return nil, err
			}
			return json.RawMessage(`{"count":` + string(byte('0'+count)) + `}`), nil
		})
		if err != nil {
			t.Fatalf("new native handler: %v", err)
		}
		return handler
	}

	scope := map[string]string{ScopeRoot: root}
	handlerV1 := makeHandler("v1")
	if _, err := handlerV1.Execute(context.Background(), json.RawMessage(`{"query":"smith"}`), scope); err != nil {
		t.Fatalf("first v1 call: %v", err)
	}
	out, err := handlerV1.Execute(context.Background(), json.RawMessage(`{"query":"smith"}`), scope)
	if err != nil {
		t.Fatalf("second v1 call: %v", err)
	}
	var cached map[string]int
	if err := json.Unmarshal(out, &cached); err != nil {
		t.Fatalf("unmarshal cached output: %v", err)
	}
	if cached["count"] != 1 {
		t.Fatalf("count = %d, want 1 from cache", cached["count"])
	}

	handlerV2 := makeHandler("v2")
	out, err = handlerV2.Execute(context.Background(), json.RawMessage(`{"query":"smith"}`), scope)
	if err != nil {
		t.Fatalf("v2 call: %v", err)
	}
	var uncached map[string]int
	if err := json.Unmarshal(out, &uncached); err != nil {
		t.Fatalf("unmarshal uncached output: %v", err)
	}
	if uncached["count"] != 2 {
		t.Fatalf("count = %d, want 2 after version bump", uncached["count"])
	}
}
