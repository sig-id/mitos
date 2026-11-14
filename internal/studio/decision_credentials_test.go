package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mitos/internal/decisionmodel"
)

func TestSavedDecisionCredentialLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		if key != "Bearer first-test-key" && key != "Bearer second-test-key" {
			t.Error("saved key not resolved")
		}
		modelReply(w, "continue", .9)
	}))
	defer server.Close()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s}
	cfg := modelConfig(server.URL, "before_segment")
	cfg.APIKey = "first-test-key"
	saved, err := e.SaveSystemConfiguration(SystemConfiguration{DecisionProvider: cfg})
	if err != nil {
		t.Fatal(err)
	}
	first := *saved.DecisionProvider
	if first.APIKey != "" || first.APIKeyRef == "" {
		t.Fatal("key not replaced by reference")
	}
	file := filepath.Join(dir, "credentials", first.APIKeyRef)
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key permissions", err)
	}
	info, err = os.Stat(filepath.Dir(file))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("directory permissions", err)
	}
	saved, err = e.SaveSystemConfiguration(saved)
	if err != nil || saved.DecisionProvider.APIKeyRef != first.APIKeyRef {
		t.Fatal("unchanged save lost key", err)
	}
	replacement := *saved.DecisionProvider
	replacement.APIKeyRef = ""
	replacement.APIKey = "second-test-key"
	saved.DecisionProvider = &replacement
	saved, err = e.SaveSystemConfiguration(saved)
	if err != nil {
		t.Fatal(err)
	}
	second := *saved.DecisionProvider
	if first.APIKeyRef == second.APIKeyRef {
		t.Fatal("rotation changed existing plan credential")
	}
	journal, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"first-test-key", "second-test-key"} {
		if strings.Contains(string(journal), key) {
			t.Fatal("key journaled")
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	r := decisionmodel.Request{ID: "credential-test", State: json.RawMessage(`{}`), Choices: []decisionmodel.Choice{{Value: "continue"}, {Value: "escalate"}}}
	for _, c := range []decisionmodel.Config{first, *s.SystemConfiguration().DecisionProvider} {
		if _, err = e.Decide(context.Background(), c, r); err != nil {
			t.Fatal("key did not survive restart", err)
		}
	}
	changed := first
	changed.Endpoint = server.URL + "/other"
	if _, err = e.Decide(context.Background(), changed, r); err == nil {
		t.Fatal("saved key forwarded to changed endpoint")
	}
	changed = first
	changed.APIKeyRef = "../events.jsonl"
	if _, err = e.Decide(context.Background(), changed, r); err == nil {
		t.Fatal("unsafe reference accepted")
	}
	saved = s.SystemConfiguration()
	saved.DecisionProvider.APIKeyRef = ""
	if _, err = e.SaveSystemConfiguration(saved); err != nil {
		t.Fatal(err)
	}
	if s.SystemConfiguration().DecisionProvider.APIKeyRef != "" {
		t.Fatal("key not removed from settings")
	}
}
