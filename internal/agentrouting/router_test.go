package agentrouting

import (
	"encoding/json"
	"mitos/internal/agentpolicy"
	"mitos/internal/harness"
	"testing"
)

func TestRulesPrecedeClassification(t *testing.T) {
	raw := json.RawMessage(`{"id":1,"method":"item/commandExecution/requestApproval","params":{"threadId":"t","turnId":"u","command":"go test ./...","cwd":"/work"}}`)
	r, err := harness.Normalize("codex", raw)
	if err != nil {
		t.Fatal(err)
	}
	p := agentpolicy.Default()
	p.Autonomy = "delegated"
	if got := Prepare(p, r, "/work"); got.Resolver != "human" {
		t.Fatal(got)
	}
	p.Rules = []agentpolicy.Rule{{ID: "test", Command: "go test ./...", Directory: ".", Action: "allow"}}
	if got := Prepare(p, r, "/work"); got.Resolver != "direct" || got.Choice != "approve" {
		t.Fatal(got)
	}
	p.Rules[0].Action = "review"
	if got := Prepare(p, r, "/work"); got.Resolver != "decision" {
		t.Fatal(got)
	}
	p.Autonomy = "manual"
	p.Rules[0].Action = "deny"
	if got := Prepare(p, r, "/work"); got.Choice != "deny" {
		t.Fatal(got)
	}
	r.Payload = json.RawMessage(`{"params":{"command":"go test ./...","cwd":"/work","additionalPermissions":{"network":true}}}`)
	p.Autonomy = "delegated"
	p.Rules[0].Action = "allow"
	if got := Prepare(p, r, "/work"); got.Resolver != "human" {
		t.Fatal("extra permission granted", got)
	}
}
