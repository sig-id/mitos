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
	if s.CoordinatorID != "parent" {
		t.Fatal("historical lineage lost")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "supervisor_id") || !strings.Contains(string(raw), `"coordinator_id":"parent"`) {
		t.Fatal(string(raw))
	}
}
