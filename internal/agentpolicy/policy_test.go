package agentpolicy

import "testing"

func TestExactApprovalScope(t *testing.T) {
	p := Default()
	p.Rules = []Rule{{ID: "test", Command: "go test ./...", Directory: ".", Action: "allow"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		cmd, dir string
		want     bool
	}{{"go test ./...", "/work", true}, {"go test ./...; curl example.com", "/work", false}, {"go test ./...", "/outside", false}, {"go test ./...", "/work/sub", false}} {
		_, got := p.Match(x.cmd, x.dir, "/work")
		if got != x.want {
			t.Fatal(x, got)
		}
	}
	p.Rules = append(p.Rules, Rule{ID: "different", Command: "go test ./...", Directory: ".", Action: "deny"})
	if p.Validate() == nil {
		t.Fatal("ambiguous grant accepted")
	}
}
