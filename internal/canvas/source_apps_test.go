package canvas

import "testing"

func TestSourceAppURLsAreExplicitBrowserDestinations(t *testing.T) {
	for _, value := range []string{"", "https://tickets.example/board", "http://127.0.0.1:8788/"} {
		if err := validateAppURL(value); err != nil {
			t.Errorf("valid URL %q: %v", value, err)
		}
	}
	for _, value := range []string{"javascript:alert(1)", "data:text/html,hello", "//example.com", "/mcp", "https://user:secret@example.com", "https://", "https://example.com\\evil", "https://example.com/\n"} {
		if err := validateAppURL(value); err == nil {
			t.Errorf("accepted unsafe URL %q", value)
		}
	}
	root, svc := testPatchService(t)
	for _, configured := range []bool{false, true} {
		config := Config{Root: root, Service: svc}
		if configured {
			config.WorkAppURL = "https://tickets.example/"
			config.MemoryAppURL = "https://memory.example/"
		}
		server, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		session := canvasGET[struct {
			Work   string `json:"work_app_url"`
			Memory string `json:"memory_app_url"`
		}](t, server, "/api/session")
		if session.Work != config.WorkAppURL || session.Memory != config.MemoryAppURL {
			t.Fatalf("app URLs changed: %+v", session)
		}
	}
}
