package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mitos/internal/desk"
)

func TestAttentionHookPauseAcknowledgmentAndRelease(t *testing.T) {
	now := time.Now()
	n := desk.Notice{ID: "notice_test", Body: "Hold Git", Pause: true, Targets: []desk.NoticeTarget{{SessionID: "session_test"}}}
	var releaseReceipt bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			json.NewEncoder(w).Encode([]desk.Notice{n})
			return
		}
		var payload struct {
			Release bool `json:"release"`
		}
		json.NewDecoder(r.Body).Decode(&payload)
		releaseReceipt = payload.Release
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	t.Setenv("MITOS_URL", server.URL)
	t.Setenv("MITOS_SESSION_ID", "session_test")
	call := func(command string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]string{"command": command}})
		var out bytes.Buffer
		if err := attentionHookIO(bytes.NewReader(body), &out); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Output map[string]any `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Output
	}
	if result := call("git commit -am changed"); result["permissionDecision"] != "deny" || !strings.Contains(result["additionalContext"].(string), "Hold Git") {
		t.Fatal(result)
	}
	if result := call(`"$MITOS_BIN" desk acknowledge notice_test`); result["permissionDecision"] != nil {
		t.Fatal("acknowledgment blocked", result)
	}
	if result := call(`"$MITOS_BIN" desk inbox; git reset --hard`); result["permissionDecision"] != "deny" {
		t.Fatal("compound command escaped pause", result)
	}
	n.Targets[0].AcknowledgedAt = &now
	if result := call("git status"); result["permissionDecision"] != "deny" {
		t.Fatal("acknowledgment released breakpoint", result)
	}
	n.ReleasedAt = &now
	if result := call("git status"); result["permissionDecision"] != nil || !strings.Contains(result["additionalContext"].(string), "RELEASED") {
		t.Fatal(result)
	}
	if !releaseReceipt {
		t.Fatal("release receipt missing")
	}
	server.Close()
	if err := attentionHookIO(strings.NewReader(`{"hook_event_name":"PreToolUse"}`), &bytes.Buffer{}); err == nil {
		t.Fatal("hook silently allowed offline operation")
	}
}
