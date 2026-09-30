package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/input"
	"github.com/boxsie/smith/internal/lib"
	"github.com/boxsie/smith/internal/runtime"
	"github.com/boxsie/smith/internal/task"
	"github.com/boxsie/smith/internal/tools"
	"github.com/boxsie/smith/internal/validate"
)

func TestWebToolsE2E_AuthorFlow(t *testing.T) {
	lookupFixture := readWebFixture(t, "web.lookup", "duckduckgo-html-results.html")
	articleFixture := readWebFixture(t, "web.fetch", "page-200.html")
	notFoundFixture := readWebFixture(t, "web.fetch", "page-404.html")

	restore := tools.SetWebHTTPClientFactoryForTesting(func() *http.Client {
		return &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch {
				case strings.Contains(req.URL.Host, "duckduckgo.com"):
					return newTestWebResponse(req, http.StatusOK, "text/html; charset=utf-8", lookupFixture, nil), nil
				case req.URL.Host == "example.com" && req.URL.Path == "/article":
					return newTestWebResponse(req, http.StatusOK, "text/html; charset=utf-8", articleFixture, nil), nil
				default:
					return newTestWebResponse(req, http.StatusNotFound, "text/html; charset=utf-8", notFoundFixture, nil), nil
				}
			}),
		}
	})
	defer restore()

	t.Run("web.lookup", func(t *testing.T) {
		mock := &runtime.MockProvider{
			Respond: func(req *runtime.Request) *runtime.Response {
				if tr := lastToolResult(req.Messages); tr != nil {
					var out struct {
						Results []struct {
							Title string `json:"title"`
						} `json:"results"`
					}
					if err := json.Unmarshal(tr.Output, &out); err != nil {
						return &runtime.Response{Content: fmt.Sprintf("bad lookup output: %v", err)}
					}
					return &runtime.Response{Content: "Lookup top result: " + out.Results[0].Title}
				}
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{{
						ID:     "lookup-call",
						ToolID: "web.lookup",
						Input:  json.RawMessage(`{"query":"alpha result","max_results":1}`),
					}},
				}
			},
		}

		output, _ := runWebE2EApp(t, mock, map[string]string{
			"task.md":  "Use the available web tool to find a result and report it back.",
			"agent.md": "model: mock/static\n",
			"tools.md": "- web.lookup\n",
		}, nil)

		if !strings.Contains(output, "Alpha Result") {
			t.Fatalf("output = %q, want lookup result", output)
		}
	})

	t.Run("web.fetch", func(t *testing.T) {
		mock := &runtime.MockProvider{
			Respond: func(req *runtime.Request) *runtime.Response {
				if tr := lastToolResult(req.Messages); tr != nil {
					var out struct {
						Title *string `json:"title"`
						Body  string  `json:"body"`
					}
					if err := json.Unmarshal(tr.Output, &out); err != nil {
						return &runtime.Response{Content: fmt.Sprintf("bad fetch output: %v", err)}
					}
					title := ""
					if out.Title != nil {
						title = *out.Title
					}
					return &runtime.Response{Content: title + "\n" + out.Body}
				}
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{{
						ID:     "fetch-call",
						ToolID: "web.fetch",
						Input:  json.RawMessage(`{"url":"https://example.com/article"}`),
					}},
				}
			},
		}

		output, _ := runWebE2EApp(t, mock, map[string]string{
			"task.md":  "Fetch the page and surface the raw body.",
			"agent.md": "model: mock/static\n",
			"tools.md": "- web.fetch\n",
		}, nil)

		if !strings.Contains(output, "Article Page") || !strings.Contains(output, "canonical 200 response fixture") {
			t.Fatalf("output = %q, want raw fetch content", output)
		}
	})

	t.Run("web.fetch_markdown", func(t *testing.T) {
		mock := &runtime.MockProvider{
			Respond: func(req *runtime.Request) *runtime.Response {
				if tr := lastToolResult(req.Messages); tr != nil {
					if errMsg := toolErrorMessage(tr.Output); errMsg != "" {
						return &runtime.Response{Content: "tool error: " + errMsg}
					}
					var out struct {
						Markdown string `json:"markdown"`
					}
					if err := json.Unmarshal(tr.Output, &out); err != nil {
						return &runtime.Response{Content: fmt.Sprintf("bad fetch_markdown output: %v", err)}
					}
					return &runtime.Response{Content: out.Markdown}
				}
				return &runtime.Response{
					ToolCalls: []runtime.ToolCall{{
						ID:     "fetch-markdown-call",
						ToolID: "web.fetch_markdown",
						Input:  json.RawMessage(`{"url":"https://example.com/article"}`),
					}},
				}
			},
		}

		output, _ := runWebE2EApp(t, mock, map[string]string{
			"task.md":  "Fetch the page as readable markdown and return it.",
			"agent.md": "model: mock/static\n",
			"tools.md": "- web.lookup\n- web.fetch_markdown\n",
		}, nil)

		if !strings.Contains(output, "Example Article") || !strings.Contains(output, "canonical 200 response fixture") {
			t.Fatalf("output = %q, want markdown content", output)
		}
	})

	t.Run("web.summarize", func(t *testing.T) {
		mock := &runtime.MockProvider{
			Respond: func(req *runtime.Request) *runtime.Response {
				switch {
				case requestHasTool(req, "web.summarize"):
					if tr := lastToolResult(req.Messages); tr != nil {
						if errMsg := toolErrorMessage(tr.Output); errMsg != "" {
							return &runtime.Response{Content: "tool error: " + errMsg}
						}
						var out struct {
							Summary string `json:"summary"`
						}
						if err := json.Unmarshal(tr.Output, &out); err != nil {
							return &runtime.Response{Content: fmt.Sprintf("bad summarize output: %v", err)}
						}
						return &runtime.Response{Content: out.Summary}
					}
					return &runtime.Response{
						ToolCalls: []runtime.ToolCall{{
							ID:     "summarize-call",
							ToolID: "web.summarize",
							Input:  json.RawMessage(`{"url":"https://example.com/article","focus":"main claim"}`),
						}},
					}
				case requestHasTool(req, "web.fetch_markdown"):
					if tr := lastToolResult(req.Messages); tr != nil {
						if errMsg := toolErrorMessage(tr.Output); errMsg != "" {
							return &runtime.Response{Content: fmt.Sprintf(`{"final_url":"https://example.com/article","status":500,"title":null,"summary":"tool error: %s","key_points":[],"warnings":["tool error"]}`, errMsg)}
						}
						var out struct {
							FinalURL string   `json:"final_url"`
							Status   int      `json:"status"`
							Title    *string  `json:"title"`
							Warnings []string `json:"warnings"`
						}
						if err := json.Unmarshal(tr.Output, &out); err != nil {
							return &runtime.Response{Content: `{"final_url":"https://example.com/article","status":500,"title":null,"summary":"","key_points":[],"warnings":["bad markdown output"]}`}
						}
						return &runtime.Response{
							Content: fmt.Sprintf(`{"final_url":%q,"status":%d,"title":%s,"summary":"Focused on the main claim of %s.","key_points":["Example Article","Canonical fixture"],"warnings":%s}`,
								out.FinalURL,
								out.Status,
								jsonStringOrNull(out.Title),
								derefString(out.Title),
								mustMarshalJSON(t, out.Warnings),
							),
						}
					}
					return &runtime.Response{
						ToolCalls: []runtime.ToolCall{{
							ID:     "fetch-markdown-call",
							ToolID: "web.fetch_markdown",
							Input:  json.RawMessage(`{"url":"https://example.com/article"}`),
						}},
					}
				default:
					return &runtime.Response{Content: "unexpected provider branch"}
				}
			},
		}

		output, calls := runWebE2EApp(t, mock, map[string]string{
			"task.md":  "Use the summary tool and report the result.",
			"agent.md": "model: mock/static\n",
			"tools.md": "- web.summarize\n",
		}, nil)

		if !strings.Contains(output, "Focused on the main claim") {
			t.Fatalf("output = %q, want summarized content", output)
		}
		if !callsContainUserText(calls, "main claim") {
			t.Fatal("expected focused summary input to appear in provider prompts")
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func runWebE2EApp(t *testing.T, provider *runtime.MockProvider, files map[string]string, runInput []input.Entry) (string, []runtime.Request) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := lib.Init(filepath.Join(home, ".smith", "lib"), false); err != nil {
		t.Fatalf("init lib: %v", err)
	}

	appDir := setupTree(t, files)
	vr := validate.Validate(appDir)
	if len(vr.Errs) > 0 {
		for _, err := range vr.Errs {
			t.Logf("validate error: %v", err)
		}
		t.Fatal("validation failed")
	}

	registry := tools.NewRegistry()
	registry.Register("project.list", &tools.ProjectList{})
	registry.Register("project.read", &tools.ProjectRead{})
	registry.Register("project.find", &tools.ProjectFind{})
	registry.Register("proposal.write", &tools.ProposalWrite{})

	scope := map[string]string{tools.ScopeRoot: appDir}
	factory := mockFactory(provider)

	resolvedDefs, err := tools.RegisterResolvedTools(registry, vr.ResolvedTools, tools.RegisterResolvedToolsConfig{
		ProjectRoot: appDir,
		Factory:     factory,
		Scope:       scope,
		SubExecute: func(ctx context.Context, root *task.Task, graph *task.Graph, subCfg tools.SubExecConfig) error {
			_, err := Execute(ctx, root, graph, Config{
				Factory:      subCfg.Factory,
				Adapter:      subCfg.Adapter,
				NoCache:      true,
				RunInput:     subCfg.RunInput,
				Scope:        subCfg.Scope,
				ResolvedDefs: subCfg.ResolvedDefs,
			})
			return err
		},
	})
	if err != nil {
		t.Fatalf("register resolved tools: %v", err)
	}

	result, err := Execute(context.Background(), vr.Root, vr.Graph, Config{
		Factory:      factory,
		Adapter:      registry,
		NoCache:      true,
		RunInput:     runInput,
		Scope:        scope,
		ResolvedDefs: resolvedDefs,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Success {
		for _, tr := range result.Tasks {
			if tr.Err != nil {
				t.Logf("task %q failed: %v", tr.TaskID, tr.Err)
			}
		}
		t.Fatal("execution failed")
	}

	data, err := os.ReadFile(filepath.Join(appDir, "output", "result.md"))
	if err != nil {
		t.Fatalf("read result.md: %v", err)
	}
	return string(data), provider.Calls
}

func lastToolResult(messages []runtime.Message) *runtime.ToolResult {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].ToolResult != nil {
			return messages[i].ToolResult
		}
	}
	return nil
}

func requestHasTool(req *runtime.Request, toolID string) bool {
	for _, def := range req.Tools {
		if def.ID == toolID {
			return true
		}
	}
	return false
}

func callsContainUserText(calls []runtime.Request, needle string) bool {
	for _, call := range calls {
		for _, msg := range call.Messages {
			if msg.Role == "user" && strings.Contains(msg.Text, needle) {
				return true
			}
		}
	}
	return false
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

func readWebFixture(t *testing.T, parts ...string) []byte {
	t.Helper()
	_, file, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	base := filepath.Join(filepath.Dir(file), "..", "tools", "testdata")
	path := filepath.Join(append([]string{base}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return data
}

func jsonStringOrNull(value *string) string {
	if value == nil {
		return "null"
	}
	data, _ := json.Marshal(*value)
	return string(data)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(data)
}

func toolErrorMessage(output json.RawMessage) string {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		return ""
	}
	return payload.Error
}
