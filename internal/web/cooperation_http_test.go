package web

import (
	"encoding/json"

	"net/http/httptest"
	"strings"
	"testing"

	"mitos/internal/cooperation"
	"mitos/internal/studio"
)

func cooperationProject(t *testing.T, s *Server) string {
	t.Helper()
	directory := t.TempDir()
	w := deskRequest(s, "POST", "/api/projects", map[string]string{"name": "Cooperation", "directory": directory})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil || project.ID == "" {
		t.Fatal(err, w.Body.String())
	}
	return project.ID
}
func putCooperation(t *testing.T, s *Server, project, hash, text string) (*httptest.ResponseRecorder, cooperation.Document) {
	t.Helper()
	w := deskRequest(s, "PUT", "/api/projects/"+project+"/cooperation-document", map[string]string{"hash": hash, "text": text})
	var doc cooperation.Document
	_ = json.Unmarshal(w.Body.Bytes(), &doc)
	return w, doc
}

// The domain accepts documents up to 64 KiB; the HTTP editing boundary must carry
// every supported document, escaping included, while the semantic bound stays.
func TestCooperationDocumentHTTPCarriesSupportedSizes(t *testing.T) {
	s, e := testServer(t)
	project := cooperationProject(t, s)
	ordinary := strings.Repeat("section of ordinary cooperation prose\n", (cooperation.Limit-2048)/len("section of ordinary cooperation prose\n"))
	if len(ordinary) > cooperation.Limit {
		t.Fatal("fixture exceeds the supported bound")
	}
	w, doc := putCooperation(t, s, project, "missing", ordinary)
	if w.Code != 200 || doc.Hash == "" || doc.Hash == "missing" || !doc.Exists {
		t.Fatalf("ordinary near-maximum document rejected: %d %s", w.Code, w.Body.String())
	}
	if stored, err := cooperation.Read(projectDirectory(t, e, project)); err != nil || stored.Text != ordinary {
		t.Fatalf("saved document diverged: %v", err)
	}
	escaping := strings.Repeat("\"\\\n\t→", cooperation.Limit/20/5)
	if len(escaping) > cooperation.Limit {
		t.Fatal("fixture exceeds the supported bound")
	}
	w, doc = putCooperation(t, s, project, doc.Hash, escaping)
	if w.Code != 200 || doc.Text != escaping {
		t.Fatalf("escaping-heavy document rejected: %d %s", w.Code, w.Body.String())
	}

	// A one-character edit through the same boundary keeps hash-conflict checks.
	edited := strings.Replace(escaping, "→", "x", 1)
	w, next := putCooperation(t, s, project, doc.Hash, edited)
	if w.Code != 200 || next.Hash == doc.Hash {
		t.Fatalf("edit failed: %d %s", w.Code, w.Body.String())
	}
	w, _ = putCooperation(t, s, project, doc.Hash, edited)
	if w.Code == 200 || !strings.Contains(w.Body.String(), "changed; reload") {
		t.Fatalf("stale hash accepted: %d %s", w.Code, w.Body.String())
	}

	// Oversized content is refused with the explicit size error, not a wire error.
	w, _ = putCooperation(t, s, project, next.Hash, ordinary+strings.Repeat("x", 4096))
	if w.Code == 200 || !strings.Contains(w.Body.String(), "64 KiB") {
		t.Fatalf("oversized document not refused clearly: %d %s", w.Code, w.Body.String())
	}
}

// Project setup loads the same server-owned document, and a frozen graph setup
// keeps the snapshot it was created with even after later edits.
func TestCooperationDocumentSnapshotStaysFrozenAcrossEdits(t *testing.T) {
	s, e := testServer(t)
	project := cooperationProject(t, s)
	original := strings.Repeat("cooperation snapshot line\n", 1600)
	w, doc := putCooperation(t, s, project, "missing", original)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	setup := deskRequest(s, "GET", "/api/projects/"+project+"/setup", nil)
	if setup.Code != 200 || !strings.Contains(setup.Body.String(), "cooperation snapshot line") {
		t.Fatalf("project setup cannot load the saved document: %d", setup.Code)
	}
	graph, err := e.StartAutomaticGoal(studio.AutomaticGoal{Key: "frozen-cooperation", ProjectID: project, Goal: "Record the cooperation document"})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := e.Store.Graph(graph.ID)
	if err != nil || frozen.Setup == nil || frozen.Setup.Cooperation.Text != original {
		t.Fatalf("goal did not freeze the document: %v", err)
	}
	w, _ = putCooperation(t, s, project, doc.Hash, "rewritten after the goal started")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	frozen, err = e.Store.Graph(graph.ID)
	if err != nil || frozen.Setup.Cooperation.Text != original {
		t.Fatalf("frozen snapshot changed after an edit: %v", err)
	}
	current := deskRequest(s, "GET", "/api/projects/"+project+"/setup", nil)
	if current.Code != 200 || !strings.Contains(current.Body.String(), "rewritten after the goal started") {
		t.Fatalf("setup did not observe the edit: %d", current.Code)
	}
}

func projectDirectory(t *testing.T, e *studio.Engine, project string) string {
	t.Helper()
	p, err := e.Desk.Project(project)
	if err != nil {
		t.Fatal(err)
	}
	return p.Directory
}
