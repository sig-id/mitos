package studio

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const answerSchema = `{"type":"object","additionalProperties":false,"required":["answer","confidence","tags"],"properties":{"answer":{"type":"integer","minimum":0},"confidence":{"type":"string","enum":["low","high"]},"tags":{"type":"array","maxItems":3,"items":{"type":"string","maxLength":20}}}}`

func TestResultSchemaSubsetAndExactValidation(t *testing.T) {
	s, err := compileResultSchema(json.RawMessage(answerSchema))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = validateResult(s, []byte(`{"answer":42,"confidence":"high","tags":["math"]}`)); err != nil {
		t.Fatal(err)
	}
	for name, result := range map[string]string{
		"missing":    `{"answer":42,"confidence":"high"}`,
		"extra":      `{"answer":42,"confidence":"high","tags":[],"note":"x"}`,
		"enum":       `{"answer":42,"confidence":"medium","tags":[]}`,
		"type":       `{"answer":"42","confidence":"high","tags":[]}`,
		"fraction":   `{"answer":4.2,"confidence":"high","tags":[]}`,
		"bounds":     `{"answer":-1,"confidence":"high","tags":[]}`,
		"items":      `{"answer":1,"confidence":"high","tags":["a","b","c","d"]}`,
		"duplicate":  `{"answer":1,"answer":2,"confidence":"high","tags":[]}`,
		"trailing":   `{"answer":1,"confidence":"high","tags":[]} {}`,
		"not json":   `The answer is 42.`,
		"wrong root": `[1]`,
	} {
		if _, err := validateResult(s, []byte(result)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Bounds beyond float64's exact integer range are compared exactly.
	precise, err := compileResultSchema(json.RawMessage(`{"type":"object","additionalProperties":false,"required":["n","x"],"properties":{"n":{"type":"integer","maximum":9007199254740992},"x":{"type":"number","minimum":0.1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = validateResult(precise, []byte(`{"n":9007199254740992,"x":0.1}`)); err != nil {
		t.Fatal(err)
	}
	for _, result := range []string{`{"n":9007199254740993,"x":0.1}`, `{"n":1,"x":0.09999999999999999999}`, `{"n":1e999999999,"x":1}`, `{"n":1.5,"x":1}`} {
		if _, err = validateResult(precise, []byte(result)); err == nil {
			t.Errorf("imprecise comparison accepted %s", result)
		}
	}
	if _, err = validateResult(precise, []byte(`{"n":1e2,"x":1}`)); err != nil {
		t.Fatalf("integral exponent rejected: %v", err)
	}
	for name, schema := range map[string]string{
		"open object":    `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`,
		"optional field": `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"string"}},"required":[]}`,
		"keyword":        `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"string","pattern":"x"}},"required":["a"]}`,
		"non-object":     `{"type":"string"}`,
		"misapplied":     `{"type":"object","additionalProperties":false,"properties":{"a":{"type":"string","minimum":1}},"required":["a"]}`,
		"ref":            `{"$ref":"#/x"}`,
	} {
		if _, err := compileResultSchema(json.RawMessage(schema)); err == nil {
			t.Errorf("%s schema accepted", name)
		}
	}
}

func structuredBrief(key, app string) Brief {
	return Brief{Title: "Classify the answer", Intent: "Return the numeric answer.", AppID: app, TimeoutMinutes: 5, Structured: &StructuredWork{Key: key, Input: json.RawMessage(`{"question":"6*7"}`), ResultSchema: json.RawMessage(answerSchema)}}
}

func finishRun(t *testing.T, e *Engine, id string) Run {
	t.Helper()
	return waitRun(t, e.Store, id, func(r Run) bool { return r.Terminal() })
}

func TestStructuredWorkRunsHeadlessAndAcceptsExactResult(t *testing.T) {
	e, _ := automaticScriptEngine(t, `import json,pathlib,sys,os
prompt=sys.stdin.read()
out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
assert '--output-schema' in sys.argv
assert os.listdir('.')==[], os.listdir('.')
assert '"question":"6*7"' in section(prompt,'assignment').replace(' ','') or '6*7' in prompt
if 'Return garbage' in prompt:
 out.write_text('forty-two')
else:
 out.write_text(json.dumps({'tags':['math'],'answer':42,'confidence':'high'}))
`)
	r, err := e.Launch(structuredBrief("s1-answer", "agent:mitos.generalist"), "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Brief.Workspace != "" || r.Brief.Structured.Definition != "mitos.generalist@3" || r.Brief.Structured.Digest == "" {
		t.Fatalf("admission did not freeze a headless binding: %+v", r.Brief)
	}
	replay, err := e.Launch(structuredBrief("s1-answer", "agent:mitos.generalist"), "")
	if err != nil || replay.ID != r.ID {
		t.Fatalf("idempotent replay: %v %s", err, replay.ID)
	}
	changed := structuredBrief("s1-answer", "agent:mitos.generalist")
	changed.Structured.Input = json.RawMessage(`{"question":"6*9"}`)
	if _, err = e.Launch(changed, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request under reused key: %v", err)
	}
	done := finishRun(t, e, r.ID)
	if done.Status != "done" || !done.Verified {
		t.Fatalf("structured run: %s %s", done.Status, done.Summary)
	}
	result, err := e.StructuredResult(r.ID)
	if err != nil || string(result) != `{"answer":42,"confidence":"high","tags":["math"]}` {
		t.Fatalf("canonical result: %s %v", result, err)
	}
	var authority AuthorityReceipt
	raw, _ := e.Store.Artifact(r.ID, "authority.json")
	if json.Unmarshal(raw, &authority) != nil || authority.Runtime != "codex-headless" || authority.Granted("workspace.write") || authority.Granted("artifact.read") {
		t.Fatalf("headless authority: %s", raw)
	}
	if _, err = os.Stat(filepath.Join(e.Store.Dir(), "workspaces", r.ID, "workspace", ".git")); !os.IsNotExist(err) {
		t.Fatal("headless work created a Git history")
	}

	bad := structuredBrief("s1-garbage", "agent:mitos.generalist")
	bad.Intent = "Return garbage."
	r, err = e.Launch(bad, "")
	if err != nil {
		t.Fatal(err)
	}
	if done = finishRun(t, e, r.ID); done.Status == "done" || !strings.Contains(done.Summary, "result_schema_mismatch") {
		t.Fatalf("schema mismatch accepted: %s %s", done.Status, done.Summary)
	}
	if _, err = e.StructuredResult(r.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("rejected result readable as accepted")
	}
	// A retry reuses the frozen request and binding as a child attempt; replay
	// of the admission key still returns the original run.
	retry, err := e.Retry(r.ID)
	if err != nil || retry.ParentID != r.ID || retry.Brief.Structured.Digest != r.Brief.Structured.Digest || retry.Brief.Structured.RequestHash != r.Brief.Structured.RequestHash {
		t.Fatalf("structured retry: %v %+v", err, retry.Brief.Structured)
	}
	if again, err := e.Retry(r.ID); err != nil || again.ID != retry.ID {
		t.Fatal("repeated retry created duplicate work")
	}
	if replay, err := e.Launch(bad, ""); err != nil || replay.ID != r.ID {
		t.Fatalf("key replay after retry: %v %s", err, replay.ID)
	}
	finishRun(t, e, retry.ID)
}

func TestStructuredWorkRefusesUnsupportedShapes(t *testing.T) {
	e, req := automaticScriptEngine(t, "")
	cases := map[string]func(*Brief){
		"workspace":    func(b *Brief) { b.Workspace = "." },
		"project":      func(b *Brief) { b.ProjectID = req.ProjectID },
		"no key":       func(b *Brief) { b.Structured.Key = "" },
		"array input":  func(b *Brief) { b.Structured.Input = json.RawMessage(`[1]`) },
		"open schema":  func(b *Brief) { b.Structured.ResultSchema = json.RawMessage(`{"type":"object","properties":{}}`) },
		"command app":  func(b *Brief) { b.AppID = "workspace-inventory" },
		"orchestrator": func(b *Brief) { b.AppID = "agent:mitos.orchestrator" },
		// The engineer requires workspace access, which headless work cannot provide.
		"needs workspace": func(b *Brief) { b.AppID = "agent:mitos.engineer" },
	}
	for name, change := range cases {
		b := structuredBrief("refuse-"+strings.ReplaceAll(name, " ", "-"), "agent:mitos.generalist")
		change(&b)
		if _, err := e.Launch(b, ""); err == nil {
			t.Errorf("%s admitted", name)
		}
	}
}
