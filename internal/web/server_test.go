package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/studio"
)

func testServer(t *testing.T) (*Server, *studio.Engine) {
	t.Helper()
	s, err := studio.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0600)
	apps, _ := studio.LoadApps("")
	e, err := studio.NewEngine(s, apps, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(); s.Close() })
	server, err := New(e, false)
	if err != nil {
		t.Fatal(err)
	}
	return server, e
}
func request(s *Server, method, path, body string, hx bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if method == "POST" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if hx {
		r.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestScreensRenderWithLocalAssetsAndProgressiveForms(t *testing.T) {
	s, _ := testServer(t)
	for _, path := range []string{"/", "/work", "/decisions", "/apps", "/apps/workspace-inventory", "/new", "/static/htmx.min.js", "/static/studio.css"} {
		t.Run(path, func(t *testing.T) {
			w := request(s, "GET", path, "", false)
			if w.Code != 200 {
				t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
			}
		})
	}
	w := request(s, "GET", "/new", "", true)
	for _, want := range []string{`method="post"`, `action="/runs"`, `name="workspace"`, `name="title"`, `/static/htmx.min.js`, `hx-boost="true"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
}
func TestLaunchValidationAndReportFlow(t *testing.T) {
	s, e := testServer(t)
	values := url.Values{"title": {"<script>alert(1)</script>"}, "workspace": {e.Root}, "app_id": {"workspace-inventory"}, "timeout_minutes": {"1"}}
	bad := request(s, "POST", "/runs", "title=x", true)
	if bad.Code != 422 || !strings.Contains(bad.Body.String(), `name="title"`) {
		t.Fatal("validation did not preserve the form")
	}
	w := request(s, "POST", "/runs", values.Encode(), true)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("HX-Redirect"), "/runs/") {
		t.Fatalf("launch: %d %s", w.Code, w.Body.String())
	}
	path := w.Header().Get("HX-Redirect")
	id := strings.TrimPrefix(path, "/runs/")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := e.Store.Get(id)
		if run.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, suffix := range []string{"", "/report"} {
		page := request(s, "GET", path+suffix, "", false)
		if page.Code != 200 {
			t.Fatalf("page failed: %s", page.Body.String())
		}
		if strings.Contains(page.Body.String(), "<script>alert(1)</script>") {
			t.Fatal("unescaped title")
		}
		if !strings.Contains(page.Body.String(), "Acceptance") {
			t.Fatal("result missing acceptance")
		}
	}
	artifact := request(s, "GET", path+"/artifacts/inventory.json", "", false)
	if artifact.Code != 200 || !strings.Contains(artifact.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("artifact download failed")
	}
	missing := request(s, "GET", path+"/artifacts/absent.txt", "", false)
	if missing.Code != 404 {
		t.Fatal("unknown artifact not rejected")
	}
	filtered := request(s, "GET", "/work?q=does-not-match", "", true)
	if strings.Contains(filtered.Body.String(), "run-row") {
		t.Fatal("search filter ignored")
	}
	run, _ := e.Store.Get(id)
	if !run.Verified {
		t.Fatalf("run did not finish: %+v", run)
	}
}
func TestCrossOriginAndHostChecks(t *testing.T) {
	s, _ := testServer(t)
	for _, host := range []string{"attacker.example", "192.168.1.4:4646"} {
		r := httptest.NewRequest("GET", "http://"+host+"/", nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("host %s accepted", host)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/runs", strings.NewReader("title=test"))
	r.Header.Set("Origin", "https://attacker.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
}
func TestJSONAndFormShareLaunchOperations(t *testing.T) {
	s, e := testServer(t)
	b, _ := json.Marshal(studio.Brief{Title: "API launch", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1})
	r := httptest.NewRequest("POST", "http://localhost/api/runs", strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("api launch failed: %s", w.Body.String())
	}
	var run studio.Run
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	stored, err := e.Store.Get(run.ID)
	if err != nil || stored.Brief.Title != "API launch" {
		t.Fatal("API bypassed durable store")
	}
	events := request(s, "GET", "/api/runs/"+run.ID+"/events", "", false)
	if events.Code != 200 || !strings.Contains(events.Body.String(), "run_created") {
		t.Fatal("events API failed")
	}
}
func TestDecisionCardHasNoSilentDefault(t *testing.T) {
	s, e := testServer(t)
	if err := studio.SeedDemo(e.Store); err != nil {
		t.Fatal(err)
	}
	page := request(s, "GET", "/decisions", "", false)
	for _, want := range []string{"Restart this interrupted work?", "Nothing is approved by silence.", "What’s at stake", `name="choice"`} {
		if !strings.Contains(page.Body.String(), want) {
			t.Fatalf("missing decision context: %s", want)
		}
	}
	for _, r := range e.Store.List() {
		if r.Status == "parked" {
			w := request(s, "POST", "/runs/"+r.ID+"/decisions/"+r.Decision.ID, "choice=cancel", false)
			if w.Code != http.StatusSeeOther {
				t.Fatal("decision failed")
			}
			after, _ := e.Store.Get(r.ID)
			if after.Decision.Choice != "cancel" || after.Status != "cancelled" {
				t.Fatal("resolution was not recorded")
			}
		}
	}
}

func TestNonLiveScreensNeverPollWhileWorkIsQueued(t *testing.T) {
	s, e := testServer(t)
	if err := studio.SeedDemo(e.Store); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/apps", "/apps/workspace-inventory", "/new", "/work"} {
		w := request(s, "GET", path, "", false)
		if strings.Contains(w.Body.String(), `hx-trigger="every 3s"`) {
			t.Fatalf("%s would refresh away the current page or its form", path)
		}
	}
	if w := request(s, "GET", "/", "", false); !strings.Contains(w.Body.String(), `hx-trigger="every 3s"`) {
		t.Fatal("overview does not refresh queued work")
	}
}
