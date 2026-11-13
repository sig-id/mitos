package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStandaloneAgentDoesNotInheritDeskBinding(t *testing.T) {
	t.Setenv("MITOS_PROJECT_ID", "project")
	t.Setenv("MITOS_SESSION_ID", "native-session")
	for _, adapter := range []string{"codex", "claude"} {
		for _, bound := range []bool{false, true} {
			var received map[string]string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				w.WriteHeader(503)
			}))
			args := []string{adapter, "--url", srv.URL, "--workspace", t.TempDir()}
			if bound {
				args = append(args, "--bind-desk-session")
			}
			err := agentClient(args)
			srv.Close()
			if err == nil {
				t.Fatal("fixture must reject start")
			}
			wantProject, wantSession := "", ""
			if bound {
				wantProject, wantSession = "project", "native-session"
			}
			if received["project_id"] != wantProject || received["desk_session_id"] != wantSession {
				t.Fatal(adapter, bound, received)
			}
		}
	}
}
