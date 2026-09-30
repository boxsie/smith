package canvas

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/boxsie/smith/internal/service"
)

func TestLibraryHTTPAuthoringWithoutExecution(t *testing.T) {
	root, svc := testPatchService(t)
	server, err := New(Config{Service: svc, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		if auth {
			r.Header.Set("X-Smith-Canvas", server.token)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "/api/library/projects", `{"name":"demo"}`, false); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("POST", "/api/library/projects", `{"name":"demo","root":"/tmp"}`, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	w := request("POST", "/api/library/projects", `{"name":"demo"}`, true)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var project service.CanvasProject
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	w = request("POST", "/api/library/patches", `{"project_id":"`+project.ID+`","name":"first"}`, true)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var item service.CanvasPatch
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	description, err := svc.InspectPatch(item.Root)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"expected_revision":"` + description.Revision + `","operations":[{"type":"add_node","node":{"id":"pass","kind":"builtin","builtin":{"type":"passthrough"}}}]}`
	w = request("POST", "/api/author?patch="+item.ID, body, true)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = request("POST", "/api/author?patch="+item.ID, body, true); w.Code != http.StatusConflict {
		t.Fatalf("stale edit: %d %s", w.Code, w.Body.String())
	}
	runs, err := svc.ListPatchRuns(item.Root)
	if err != nil || len(runs) != 0 {
		t.Fatalf("unexpected execution: %v %v", runs, err)
	}
	w = request("GET", "/api/session?patch="+item.ID, "", false)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"name":"first"`)) {
		t.Fatal(w.Body.String())
	}
	w = request("GET", "/api/session?patch=../outside", "", false)
	if w.Code == 200 {
		t.Fatal("unmounted browser path accepted")
	}
}

func TestProjectTicketPreparationKeepsSelectedLocalProject(t *testing.T) {
	root, svc := testPatchService(t)
	spy := &harnessCanvasService{Service: svc}
	server, err := New(Config{Service: spy, Root: root, WritableRoots: []string{root}, Harness: &service.HarnessConfig{ProjectSlug: "smith", Workspace: root}})
	if err != nil {
		t.Fatal(err)
	}
	project, err := svc.CreateCanvasProject(server.library, "new-project")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://localhost/api/harness/preview?project="+project.ID, bytes.NewBufferString(`{"project_slug":"smith","ticket_id":"ticket-1"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Smith-Canvas", server.token)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 200 || spy.previewBase != project.Root {
		t.Fatalf("project preparation escaped selection: %d %q %s", w.Code, spy.previewBase, w.Body.String())
	}
	if runs, err := svc.ListPatchRuns(project.Root); err != nil || len(runs) != 0 {
		t.Fatalf("preview started work: %v %v", runs, err)
	}
}
