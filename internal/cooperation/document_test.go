package cooperation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDocumentCASAndBoundary(t *testing.T) {
	dir := t.TempDir()
	d, err := Read(dir)
	if err != nil || d.Exists || d.Hash != "missing" {
		t.Fatal(d, err)
	}
	d, err = Save(dir, d.Hash, "Keep handoffs explicit.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Save(dir, "missing", "overwrite"); err == nil {
		t.Fatal("stale write accepted")
	}
	if err = os.Remove(filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(t.TempDir(), "outside.md"), filepath.Join(dir, FileName)); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(dir); err == nil {
		t.Fatal("symlink followed")
	}
	if _, err = Save(dir, d.Hash, strings.Repeat("a", Limit+1)); err == nil {
		t.Fatal("oversized document accepted")
	}
}
func TestMessageRequestSemantics(t *testing.T) {
	now := time.Now()
	m := Message{Version: Version, ID: "q1", TaskID: "task", RunID: "run", Revision: 1, Kind: "question", Body: "Which option?", Blocking: true}
	if err := m.Validate(now); err != nil {
		t.Fatal(err)
	}
	m.Kind = "result"
	if m.Validate(now) == nil {
		t.Fatal("result blocks work")
	}
	m.Blocking = false
	m.Kind = "answer"
	if m.Validate(now) == nil {
		t.Fatal("uncorrelated answer")
	}
	m.InReplyTo = "q0"
	if err := m.Validate(now); err != nil {
		t.Fatal(err)
	}
}
