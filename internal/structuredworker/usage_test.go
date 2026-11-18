package structuredworker

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"mitos/agents"
	"mitos/internal/compute"
)

func lookPython() (string, error) { return exec.LookPath("python3") }

// A runtime that reports session token totals must surface them on the worker
// result, normalized to the uncached/cached/write split, even though the
// fixture model has no published price.
func TestExecuteReportsHarnessTokenUsage(t *testing.T) {
	python, err := lookPython()
	if err != nil {
		t.Skip("python3 required for the app-server fixture")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	fixture := `import json,sys
for line in sys.stdin:
	req=json.loads(line);method=req.get('method','')
	if method=='initialize':print(json.dumps({'id':req['id'],'result':{}}),flush=True)
	elif method=='thread/start':print(json.dumps({'id':req['id'],'result':{'thread':{'id':'th'},'model':'gpt-6.2-sol'}}),flush=True)
	elif method=='turn/start':
		print(json.dumps({'id':req['id'],'result':{'turn':{'id':'tu'}}}),flush=True)
		print(json.dumps({'method':'item/completed','params':{'item':{'id':'a','type':'agentMessage','text':'{"reply":"done"}'}}}),flush=True)
		print(json.dumps({'method':'thread/tokenUsage/updated','params':{'threadId':'th','turnId':'tu','tokenUsage':{'total':{'totalTokens':7000,'inputTokens':5000,'cachedInputTokens':4000,'cacheWriteInputTokens':600,'outputTokens':2000,'reasoningOutputTokens':0},'last':{'totalTokens':7000,'inputTokens':5000,'cachedInputTokens':4000,'cacheWriteInputTokens':600,'outputTokens':2000,'reasoningOutputTokens':0}}}}),flush=True)
		print(json.dumps({'method':'turn/completed','params':{'threadId':'th','turn':{'id':'tu','status':'completed'}}}),flush=True)
`
	if err = os.WriteFile(script, []byte("#!"+python+"\n"+fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	executor := Executor{Provider: &compute.Local{}, Executable: script, Base: dir, Preview: io.Discard, Activity: io.Discard}
	assignment := Context{Binding: Binding{RunID: "run_usage_test", BudgetSeconds: 30}, Definition: agents.Definition{ID: "mitos.generalist"}, Prompt: "Reply done.", ResultSchema: []byte(`{"type":"object","additionalProperties":false,"required":["reply"],"properties":{"reply":{"type":"string"}}}`), MaxTurns: 5}
	result, err := executor.Execute(context.Background(), assignment)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil {
		t.Fatal("usage totals missing from worker result")
	}
	if result.Usage.Model != "gpt-6.2-sol" || result.Usage.InputTokens != 1000 || result.Usage.CachedInputTokens != 4000 || result.Usage.CacheWriteTokens != 600 || result.Usage.OutputTokens != 2000 || result.Usage.Cumulative {
		t.Fatalf("unexpected usage %+v", result.Usage)
	}
	// The transport envelope keeps the usage field alongside the value.
	raw, err := json.Marshal(result)
	if err != nil || !json.Valid(raw) {
		t.Fatalf("result envelope broken: %v", err)
	}
	var round Result
	if json.Unmarshal(raw, &round) != nil || round.Usage == nil || round.Usage.CachedInputTokens != 4000 {
		t.Fatalf("usage lost across the envelope: %s", raw)
	}
}
