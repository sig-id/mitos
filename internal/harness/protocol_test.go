package harness

import (
	"encoding/json"
	"mitos/internal/decision"
	"testing"
)

func TestNativeProtocolsPreserveSourceAndDenyPersistentGrants(t *testing.T) {
	raw := json.RawMessage(`{"id":1,"method":"item/commandExecution/requestApproval","params":{"threadId":"t","turnId":"u","command":"dangerous script","availableDecisions":["acceptForSession","decline"]}}`)
	if _, err := Normalize("codex", raw); err == nil {
		t.Fatal("invented one-shot grant not offered by source")
	}
	for _, question := range []string{`{"id":"q","question":"Password?"}`, `{"id":"q","question":"Name?","isSecret":true}`, `{"id":"q","question":"Select?","multiSelect":true}`, `{"id":"q","question":"Name?","schema":{"type":"string","format":"password"}}`} {
		raw := json.RawMessage(`{"id":1,"method":"item/tool/requestUserInput","params":{"threadId":"t","turnId":"u","questions":[` + question + `]}}`)
		if _, err := Normalize("codex", raw); err == nil {
			t.Fatal("unsupported/secret input accepted", question)
		}
	}
	request, err := Normalize("claude", json.RawMessage(`{"session_id":"t","tool_use_id":"tool","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"echo reviewed"}}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := Response("claude", request, []decision.Resolution{{Choice: "approve"}})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	_ = json.Unmarshal(response, &envelope)
	output := envelope["hookSpecificOutput"].(map[string]any)
	if output["permissionDecision"] != "allow" || output["updatedPermissions"] != nil {
		t.Fatal("native policy weakened", output)
	}
}
