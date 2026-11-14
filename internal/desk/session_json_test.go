package desk

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStoredSessionLineageMigrationHasNoLegacyAPIField(t *testing.T) {
	var s Session
	if err := json.Unmarshal([]byte(`{"id":"worker","supervisor_id":"parent"}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.OrchestratorID != "parent" {
		t.Fatal("historical lineage lost")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "supervisor_id") || !strings.Contains(string(raw), `"orchestrator_id":"parent"`) {
		t.Fatal(string(raw))
	}
}

func TestStoredCoordinatorSessionMigratesToOrchestrator(t *testing.T) {
	var s Session
	if err := json.Unmarshal([]byte(`{"role":"coordinator","coordinator_id":"parent"}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Role != "orchestrator" || s.OrchestratorID != "parent" {
		t.Fatal(s)
	}
	var r Responsibility
	if err := json.Unmarshal([]byte(`{"coordinator_session_id":"parent"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.OrchestratorSessionID != "parent" {
		t.Fatal(r)
	}
}
