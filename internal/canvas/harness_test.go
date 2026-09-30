package canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boxsie/smith/internal/service"
)

type harnessCanvasService struct {
	*service.Service
	previews    int
	starts      int
	selection   service.HarnessSelection
	previewBase string
}

func (s *harnessCanvasService) PreviewHarness(_ context.Context, base string, config service.HarnessConfig, selection service.HarnessSelection) (*service.HarnessPreview, error) {
	s.previews++
	s.previewBase = base
	s.selection = selection
	return &service.HarnessPreview{Digest: "bound-digest", Selection: selection, Workspace: config.Workspace}, nil
}
func (s *harnessCanvasService) StartHarness(_ context.Context, _ string, _ service.HarnessConfig, digest string) (*service.HarnessStartResult, error) {
	s.starts++
	return nil, service.ErrHarnessStale
}

func TestCanvasHarnessUsesOnlyOperatorAuthority(t *testing.T) {
	root, svc := testPatchService(t)
	spy := &harnessCanvasService{Service: svc}
	config := &service.HarnessConfig{ProjectSlug: "smith", Workspace: root}
	server, err := New(Config{Service: spy, Root: root, WritableRoots: []string{root}, Harness: config})
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, body string, token bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		if token {
			r.Header.Set("X-Smith-Canvas", server.token)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	for _, body := range []string{
		`{"project_slug":"smith","ticket_id":"ticket-1","workspace":"/"}`,
		`{"project_slug":"smith","ticket_id":"ticket-1","memories":["private"]}`,
		`{"project_slug":"smith","ticket_id":"ticket-1","grants":[{"access":"mutate"}]}`,
	} {
		if w := post("/api/harness/preview", body, true); w.Code != http.StatusBadRequest {
			t.Fatalf("untrusted authority accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if w := post("/api/harness/preview", `{"project_slug":"smith","ticket_id":"ticket-1"}`, false); w.Code != http.StatusForbidden {
		t.Fatal("missing session accepted")
	}
	if spy.previews != 0 || spy.starts != 0 {
		t.Fatal("denied request reached service")
	}
	w := post("/api/harness/preview", `{"project_slug":"smith","ticket_id":"ticket-1"}`, true)
	if w.Code != 200 || spy.previews != 1 || spy.starts != 0 {
		t.Fatalf("preview launched work: %d %s", w.Code, w.Body.String())
	}
	var preview service.HarnessPreview
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Workspace != root {
		t.Fatal("preview lost configured workspace")
	}
	if w := post("/api/harness/start", `{"digest":"bound-digest","workspace":"/"}`, true); w.Code != 400 || spy.starts != 0 {
		t.Fatal("start accepted a replacement request")
	}
	if w := post("/api/harness/start", `{"digest":"stale"}`, true); w.Code != 409 || !strings.Contains(w.Body.String(), "harness_stale") {
		t.Fatalf("stale response: %d %s", w.Code, w.Body.String())
	}
	if _, err := New(Config{Service: svc, Root: root, Harness: config}); err == nil {
		t.Fatal("harness acquired an ungranted workspace")
	}
	r := httptest.NewRequest(http.MethodGet, "http://localhost/api/session?launch=../outside", nil)
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatal("launch traversal accepted")
	}
}
