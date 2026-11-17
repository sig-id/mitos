package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func resultArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func resultStore(t *testing.T) (*Store, *WorkGraph) {
	t.Helper()
	s := testStore(t)
	producer := createRun(t, s)
	answerSum := sha256.Sum256([]byte("final answer"))
	manifest, err := json.Marshal(map[string]any{
		"baseline_commit": "1",
		"result_commit":   "2",
		"fingerprint":     "f",
		"patch":           "--- a\n+++ b\n",
		"changes":         []map[string]string{{"path": "docs/answer.md", "kind": "added", "after_sha256": hex.EncodeToString(answerSum[:])}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveArtifacts(producer.ID,
		ArtifactData{Name: "report.md", Data: []byte("# Reviewed report")},
		ArtifactData{Name: "changes.patch", Data: []byte("--- a\n+++ b\n")},
		ArtifactData{Name: "changes.json", Data: manifest},
		ArtifactData{Name: "workspace.tar.gz", Data: resultArchive(t, map[string]string{"docs/answer.md": "final answer", "notes/kept.txt": "unchanged source"})},
	); err != nil {
		t.Fatal(err)
	}
	g := &WorkGraph{Members: map[string]*GraphMember{
		"worker": {Spec: GraphSpec{Kind: "application"}, CurrentRunID: producer.ID, Accepted: &GraphAcceptance{RunID: producer.ID}},
	}}
	return s, g
}

func TestFinalAnswerRequiresAcceptedSources(t *testing.T) {
	s, g := resultStore(t)
	g.Members["unreviewed"] = &GraphMember{Spec: GraphSpec{Kind: "application"}}
	g.Members["replaced"] = &GraphMember{Spec: GraphSpec{Kind: "application"}, Replacement: "worker", Accepted: &GraphAcceptance{RunID: "old"}}
	g.Members["orchestrator"] = &GraphMember{Spec: GraphSpec{Kind: "application"}, OrchestratorFor: "initial", Accepted: &GraphAcceptance{RunID: "control"}}
	answer := FinalAnswer{Title: "Gap report", Markdown: "- [ ] A demonstrated gap.", SourceTasks: []string{"worker"}}
	result, err := s.bindGraphResult(g, answer, "orchestrator:test")
	if err != nil || result.Sources[0].Run == "" {
		t.Fatalf("must freeze reviewed run: %+v %v", result, err)
	}
	for _, id := range []string{"missing", "unreviewed", "replaced", "orchestrator"} {
		bad := answer
		bad.SourceTasks = []string{id}
		if _, err := s.bindGraphResult(g, bad, "orchestrator:test"); err == nil {
			t.Fatalf("accepted invalid source %s", id)
		}
	}
	for _, bad := range []FinalAnswer{{}, {Title: "Review accepted", SourceTasks: []string{"worker"}}, {Title: "Gap report", Markdown: "Findings", SourceTasks: []string{"worker", "worker"}}, {Title: "Gap report", Markdown: strings.Repeat("x", (128<<10)+1), SourceTasks: []string{"worker"}}} {
		if _, err := s.bindGraphResult(g, bad, "orchestrator:test"); err == nil {
			t.Fatal("accepted missing or unbounded final answer")
		}
	}
}

func TestFinalAnswerPresentationItems(t *testing.T) {
	s, g := resultStore(t)
	base := FinalAnswer{Title: "Implementation", Markdown: "Updated the implementation.", SourceTasks: []string{"worker"}}
	for _, item := range []ResultItem{
		{Kind: "file", Title: "Report", Task: "worker", Path: "report.md"},
		{Kind: "changes", Title: "Changes", Task: "worker", Path: "changes.patch"},
		{Kind: "file", Title: "Workspace file", Task: "worker", Path: "notes/kept.txt"},
		{Kind: "changes", Title: "Answer", Task: "worker", Path: "docs/answer.md"},
		{Kind: "link", Title: "PR 1", URL: "https://example.com/pull/1"},
	} {
		base.Items = []ResultItem{item}
		result, err := s.bindGraphResult(g, base, "orchestrator:test")
		if err != nil {
			t.Fatal(err)
		}
		if item.Kind != "link" && (result.Items[0].Digest == "" || result.Items[0].Bytes == 0) {
			t.Fatalf("item not bound to saved evidence: %+v", result.Items[0])
		}
	}
	for _, item := range []ResultItem{
		{Kind: "html", Title: "Unsafe"},
		{Kind: "link", Title: "Unsafe", URL: "javascript:alert(1)"},
		{Kind: "file", Title: "Outside", Task: "worker", Path: "../secret"},
		{Kind: "changes", Title: "Wrong source", Task: "missing", Path: "changes.patch"},
		{Kind: "link", Title: "Credentials", URL: "https://user:secret@example.com"},
		// Absent, wrong-run and unsafe paths must be refused before publication.
		{Kind: "file", Title: "Missing", Task: "worker", Path: "never-produced.md"},
		{Kind: "file", Title: "Escaping", Task: "worker", Path: "a/../report.md"},
		{Kind: "changes", Title: "Cross run", Task: "worker", Path: "inputs/task/report.md"},
	} {
		base.Items = []ResultItem{item}
		if _, err := s.bindGraphResult(g, base, "orchestrator:test"); err == nil {
			t.Fatalf("accepted invalid item %+v", item)
		}
	}
}

// Validated links must survive publication: the result reader renders external
// links exclusively from the stored result items.
func TestFinalAnswerPreservesLinkItems(t *testing.T) {
	s, g := resultStore(t)
	answer := FinalAnswer{Title: "Implementation", Markdown: "Updated the implementation.", SourceTasks: []string{"worker"}, Items: []ResultItem{
		{Kind: "link", Title: "Pull request", URL: "https://example.com/pull/7"},
		{Kind: "file", Title: "Workspace file", Task: "worker", Path: "notes/kept.txt"},
	}}
	result, err := s.bindGraphResult(g, answer, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].Kind != "link" || result.Items[0].URL != "https://example.com/pull/7" || result.Items[0].Title != "Pull request" {
		t.Fatalf("validated link item missing from published result: %+v", result.Items)
	}
}

// Publication must bind workspace files to the registered archive: a replaced
// or unregistered tar.gz on disk cannot back a deliverable, matching the
// checksum-verified artifact reads used by result readers.
func TestFinalAnswerRejectsTamperedWorkspaceArchive(t *testing.T) {
	s, g := resultStore(t)
	run := g.Members["worker"].Accepted.RunID
	replaced := resultArchive(t, map[string]string{"docs/answer.md": "final answer", "notes/kept.txt": "unchanged source", "extra.txt": "extra"})
	if err := os.WriteFile(filepath.Join(s.dir, "artifacts", run, "workspace.tar.gz"), replaced, 0600); err != nil {
		t.Fatal(err)
	}
	tampered := FinalAnswer{Title: "Implementation", Markdown: "Updated.", SourceTasks: []string{"worker"}, Items: []ResultItem{{Kind: "file", Title: "Kept", Task: "worker", Path: "notes/kept.txt"}}}
	if _, err := s.bindGraphResult(g, tampered, "operator"); err == nil {
		t.Fatal("replaced workspace archive accepted")
	}
	stray := createRun(t, s)
	if err := os.MkdirAll(filepath.Join(s.dir, "artifacts", stray.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "artifacts", stray.ID, "workspace.tar.gz"), resultArchive(t, map[string]string{"notes/kept.txt": "unchanged source"}), 0600); err != nil {
		t.Fatal(err)
	}
	unregistered := FinalAnswer{Title: "Implementation", Markdown: "Updated.", SourceTasks: []string{"stray"}, Items: []ResultItem{{Kind: "file", Title: "Kept", Task: "stray", Path: "notes/kept.txt"}}}
	strayGraph := &WorkGraph{Members: map[string]*GraphMember{"stray": {Spec: GraphSpec{Kind: "application"}, Accepted: &GraphAcceptance{RunID: stray.ID}}}}
	if _, err := s.bindGraphResult(strayGraph, unregistered, "operator"); err == nil {
		t.Fatal("unregistered workspace archive accepted")
	}
}

// Deleted deliverables and corrupt archives refuse publication while the prior
// result stays untouched.
func TestFinalAnswerRefusesDeletedAndCorruptFiles(t *testing.T) {
	s, _ := resultStore(t)
	producer := createRun(t, s)
	if err := s.SaveArtifacts(producer.ID,
		ArtifactData{Name: "changes.json", Data: []byte(`{"changes":[{"path":"removed.txt","kind":"deleted","after_sha256":""}]}`)},
		ArtifactData{Name: "workspace.tar.gz", Data: resultArchive(t, map[string]string{"kept.txt": "kept"})},
	); err != nil {
		t.Fatal(err)
	}
	g := &WorkGraph{Members: map[string]*GraphMember{"worker": {Spec: GraphSpec{Kind: "application"}, Accepted: &GraphAcceptance{RunID: producer.ID}}}}
	deleted := FinalAnswer{Title: "Implementation", Markdown: "Updated.", SourceTasks: []string{"worker"}, Items: []ResultItem{{Kind: "file", Title: "Removed", Task: "worker", Path: "removed.txt"}}}
	if _, err := s.bindGraphResult(g, deleted, "operator"); err == nil {
		t.Fatal("deleted deliverable accepted")
	}
	corrupt := deleted
	corrupt.Items = []ResultItem{{Kind: "file", Title: "Corrupt", Task: "worker", Path: "report.md"}}
	if _, err := s.bindGraphResult(g, corrupt, "operator"); err == nil {
		t.Fatal("unregistered artifact without archive accepted")
	}
}
