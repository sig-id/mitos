package studio

import (
	"errors"
	"mitos/internal/cooperation"
	"testing"
	"time"
)

func TestCooperationSnapshotAndRequestLifecycle(t *testing.T) {
	e, req := graphEngine(t, false)
	doc, err := e.SaveCooperation(req.ProjectID, "missing", "Publish evidence before handoff.")
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := e.freezeSetup(req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.SaveCooperation(req.ProjectID, doc.Hash, "Updated guidance."); err != nil {
		t.Fatal(err)
	}
	if frozen.Cooperation.Text != doc.Text {
		t.Fatal("snapshot changed")
	}
	s := e.Store
	s.mu.Lock()
	s.tasks["task"] = &Task{ID: "task", ProjectID: req.ProjectID, Revision: 1}
	s.runs["run"] = &Run{ID: "run", ProjectID: req.ProjectID, TaskID: "task", Status: "running"}
	s.mu.Unlock()
	expiry := time.Now().Add(time.Hour)
	m := cooperation.Message{Version: cooperation.Version, ID: "q", TaskID: "task", RunID: "run", Revision: 1, Kind: "question", Body: "Choose", ExpiresAt: &expiry}
	a, err := e.SubmitCooperation(req.ProjectID, m)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.SubmitCooperation(req.ProjectID, m)
	if err != nil || a.Digest != b.Digest {
		t.Fatal(b, err)
	}
	m.Body = "Different"
	if _, err = e.SubmitCooperation(req.ProjectID, m); !errors.Is(err, ErrConflict) {
		t.Fatal("conflict accepted", err)
	}
	answer := cooperation.Message{Version: cooperation.Version, ID: "a", TaskID: "task", RunID: "run", Revision: 1, Kind: "answer", InReplyTo: "q", Body: "First"}
	if _, err = e.SubmitCooperation(req.ProjectID, answer); err != nil {
		t.Fatal(err)
	}
	answer.ID = "a2"
	if _, err = e.SubmitCooperation(req.ProjectID, answer); !errors.Is(err, ErrConflict) {
		t.Fatal("second answer accepted", err)
	}
	answer.ID = "a3"
	answer.Revision = 2
	if _, err = e.SubmitCooperation(req.ProjectID, answer); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong revision accepted", err)
	}
	if len(s.Cooperation(req.ProjectID)) != 2 {
		t.Fatal("duplicate effects")
	}
}
