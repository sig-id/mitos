package studio

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/structuredworker"
)

func testWorker(t *testing.T, e *Engine, id string) (string, structuredworker.Identity) {
	t.Helper()
	_, token, err := e.Store.CreateWorkerCredential(WorkerCredential{ID: id, Pools: []string{"interactive"}, Agents: []string{"mitos.generalist"}, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	identity := structuredworker.Identity{ID: id, Incarnation: "incarnation-one"}
	request := structuredworker.Register{RequestID: "register-one", Incarnation: identity.Incarnation, Capabilities: []string{"mitos.generalist"}}
	first, err := e.Store.RegisterWorker(token, request)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := e.Store.RegisterWorker(token, request); err != nil || again != first {
		t.Fatal("registration replay", again, err)
	}
	return token, identity
}
func testWorkerClaim(t *testing.T, e *Engine, token string, identity structuredworker.Identity, id, key string) structuredworker.Binding {
	t.Helper()
	request := structuredworker.Claim{Identity: identity, RequestID: key, RunID: id}
	receipt, err := e.Store.WorkerClaim(token, request)
	if err != nil || receipt.Binding == nil {
		t.Fatal("claim", receipt, err)
	}
	again, err := e.Store.WorkerClaim(token, request)
	if err != nil || graphDigest(again) != graphDigest(receipt) {
		t.Fatal("claim receipt lost", again, err)
	}
	return *receipt.Binding
}
func TestWorkerClaimAcceptanceReplayAndEpoch(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	token, identity := testWorker(t, e, "worker-one")
	r, err := e.LaunchStructured(structuredBrief("worker-input", "agent:mitos.generalist"))
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-one")
	running, _ := e.Store.Get(r.ID)
	if running.Status != "running" || binding.TaskID != r.TaskID || binding.InputDigest != digestText(string(r.Brief.Structured.Input)) || binding.Generation != 1 {
		t.Fatal("claim is not atomically bound", binding, running.Status)
	}
	assignment, err := e.WorkerContext(token, binding)
	if err != nil || !strings.Contains(assignment.Prompt, "6*7") {
		t.Fatal("context", assignment, err)
	}
	body, _ := json.Marshal(structuredworker.Result{Value: json.RawMessage(`{"answer":42,"confidence":"high","tags":[]}`)})
	mutation := structuredworker.Mutation{Binding: binding, RequestID: "result-one", Kind: "result", Data: body}
	receipt, err := e.Store.WorkerMutate(token, mutation)
	if err != nil || receipt.State != "accepted" {
		t.Fatal("accept", receipt, err)
	}
	before := e.Store.seq
	again, err := e.Store.WorkerMutate(token, mutation)
	if err != nil || again != receipt || e.Store.seq != before {
		t.Fatal("lost acceptance reply duplicated", again, err)
	}
	mutation.Data = json.RawMessage(`{"value":{"answer":43,"confidence":"high","tags":[]}}`)
	if _, err = e.Store.WorkerMutate(token, mutation); !errors.Is(err, ErrConflict) {
		t.Fatal("request ID payload changed", err)
	}
	done, _ := e.Store.Get(r.ID)
	if done.Status != "done" || !done.Verified || len(e.Store.workerOutbox) != 1 {
		t.Fatal("result/receipt/outbox not co-committed")
	}
	dir := e.Store.Dir()
	backup := filepath.Join(t.TempDir(), "backup")
	if err = e.Store.Backup(backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restore")
	if err = RestoreStore(backup, restored); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.RegisterWorker(token, structuredworker.Register{RequestID: "restored", Incarnation: identity.Incarnation, Capabilities: []string{"mitos.generalist"}}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("restore did not fence worker token", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, runtimeDatabase))
	if strings.Contains(string(data), token) {
		t.Fatal("raw worker token stored")
	}
}

func TestWorkerExpiredBindingReplacementAndStaleEvidence(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	token, identity := testWorker(t, e, "worker-two")
	r, err := e.LaunchStructured(structuredBrief("worker-expiry", "agent:mitos.generalist"))
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-old")
	e.Store.mu.Lock()
	lease := e.Store.workerLeases[r.ID]
	lease.ExpiresAt = time.Now().Add(-time.Second)
	err = e.Store.commit(r.ID, "effect", "test_expiry", change{Worker: &workerChange{Lease: &lease}})
	e.Store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileWorkerLeases(); err != nil {
		t.Fatal(err)
	}
	task, _ := e.Store.Task(r.TaskID)
	if task.CurrentRunID == r.ID {
		t.Fatal("expired attempt not replaced")
	}
	next := testWorkerClaim(t, e, token, identity, task.CurrentRunID, "claim-new")
	if next.Generation <= binding.Generation || next.TaskID != binding.TaskID {
		t.Fatal("replacement reused authority", next)
	}
	mutation := structuredworker.Mutation{Binding: binding, RequestID: "stale-result", Kind: "result", Data: json.RawMessage(`{"value":{"answer":1,"confidence":"low","tags":[]}}`)}
	receipt, err := e.Store.WorkerMutate(token, mutation)
	if !errors.Is(err, ErrStaleBinding) || receipt.Seq == 0 {
		t.Fatal("stale evidence not saved", receipt, err)
	}
	seq := e.Store.seq
	if again, err := e.Store.WorkerMutate(token, mutation); !errors.Is(err, ErrStaleBinding) || again != receipt || seq != e.Store.seq {
		t.Fatal("stale result not deduplicated", again, err)
	}
	old, _ := e.Store.Get(r.ID)
	if old.Status == "done" || old.Events[len(old.Events)-1].Kind != "stale_worker_result" {
		t.Fatal("stale evidence changed outcome")
	}
	if _, err = e.WorkerContext(token, binding); !errors.Is(err, ErrStaleBinding) {
		t.Fatal("stale context readable", err)
	}
}

func TestWorkerCurrentPermissionAndIncarnationFencing(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	principal, _ := serviceTestPrincipal(t, e.Store, "workerapp", "tenant", true)
	token, identity := testWorker(t, e, "worker-three")
	r, err := e.Application(principal).Admit(structuredBrief("scoped-worker", "agent:mitos.generalist"))
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-scoped")
	if _, _, err = e.Store.ChangeServicePrincipal(principal.ID, false, time.Time{}, decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	if _, err = e.WorkerContext(token, binding); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked context", err)
	}
	result := structuredworker.Mutation{Binding: binding, RequestID: "revoked-result", Kind: "result", Data: json.RawMessage(`{"value":{"answer":1,"confidence":"low","tags":[]}}`)}
	if _, err = e.Store.WorkerMutate(token, result); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked acceptance", err)
	}
	_, err = e.Store.RegisterWorker(token, structuredworker.Register{RequestID: "register-next", Incarnation: "incarnation-two", Capabilities: []string{"mitos.generalist"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store.WorkerMutate(token, structuredworker.Mutation{Binding: binding, RequestID: "renew-old", Kind: "renew"}); !errors.Is(err, ErrStaleBinding) {
		t.Fatal("old incarnation renewed", err)
	}
	if err = e.Store.RevokeWorker(identity.ID, decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store.WorkerClaim(token, structuredworker.Claim{Identity: identity, RequestID: "revoked-claim"}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("revoked worker claimed", err)
	}
}

func TestWorkerCheckpointSchemaProvenanceAndReplay(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	token, identity := testWorker(t, e, "worker-checkpoint")
	brief := structuredBrief("checkpoint", "agent:mitos.generalist")
	brief.Structured.CheckpointSchema = json.RawMessage(answerSchema)
	r, err := e.LaunchStructured(brief)
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-checkpoint")
	mutation := structuredworker.Mutation{Binding: binding, RequestID: "checkpoint-one", Kind: "checkpoint", Data: json.RawMessage(`{"answer":1,"confidence":"low","tags":[]}`)}
	receipt, err := e.Store.WorkerMutate(token, mutation)
	if err != nil {
		t.Fatal(err)
	}
	cp := e.Store.structuredCheckpoints[r.TaskID]
	if cp.Generation != binding.Generation || cp.RunID != r.ID || cp.InputDigest != binding.InputDigest || cp.SchemaDigest != digestText(answerSchema) {
		t.Fatal("checkpoint provenance", cp)
	}
	seq := e.Store.seq
	if again, err := e.Store.WorkerMutate(token, mutation); err != nil || again != receipt || seq != e.Store.seq {
		t.Fatal("checkpoint replay", again, err)
	}
	mutation.RequestID = "checkpoint-bad"
	mutation.Data = json.RawMessage(`{"answer":"bad"}`)
	if _, err = e.Store.WorkerMutate(token, mutation); err == nil {
		t.Fatal("undeclared checkpoint accepted")
	}
	dir := e.Store.Dir()
	if err = e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if graphDigest(s.structuredCheckpoints[r.TaskID]) != graphDigest(cp) {
		t.Fatal("checkpoint not durable")
	}
}

// Pre-upgrade structured runs recorded no frozen definition body. They must be
// hydrated to the exact admitted revision at startup, become claimable again,
// and produce hydrated replacements on retry.
func TestLegacyStructuredRunsAreHydratedAndClaimable(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	r, err := e.LaunchStructured(structuredBrief("legacy-frozen", "agent:mitos.generalist"))
	if err != nil {
		t.Fatal(err)
	}
	s := e.Store
	token, identity := testWorker(t, e, "worker-legacy")
	s.mu.Lock()
	s.runs[r.ID].Brief.Structured.Frozen = nil
	s.mu.Unlock()
	// Without the frozen body the run is not claimable and has no context.
	if receipt, claimErr := e.Store.WorkerClaim(token, structuredworker.Claim{Identity: identity, RequestID: "legacy-claim", RunID: r.ID}); claimErr != nil || receipt.Binding != nil {
		t.Fatal("unhydrated legacy run was claimed", receipt, claimErr)
	}
	if err = e.hydrateStructuredDefinitions(); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d := fresh.Brief.Structured.Frozen; d == nil || d.ID != "mitos.generalist" || d.Digest != fresh.Brief.Structured.Digest {
		t.Fatalf("definition not hydrated: %+v", fresh.Brief.Structured.Frozen)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "legacy-claim-2")
	assignment, err := e.WorkerContext(token, binding)
	if err != nil || !strings.Contains(assignment.Prompt, "6*7") {
		t.Fatal("hydrated run has no executable context", err)
	}
	// A retry of a pre-upgrade attempt freezes the same revision in the child.
	brief := fresh.Brief
	brief.Structured.Frozen = nil
	child, err := e.retryStructured(brief, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	childRun, err := s.Get(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if childRun.Brief.Structured.Frozen == nil || childRun.Brief.Structured.Frozen.Digest != fresh.Brief.Structured.Digest {
		t.Fatal("retry preserved the missing definition body")
	}
}

// The lease budget is the narrowest of every admitted limit, never the raw
// brief timeout: the frozen definition's own timeout and an explicit
// scheduling deadline both cap execution.
func TestWorkerClaimBudgetIntersectsAdmittedLimits(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	token, identity := testWorker(t, e, "worker-budget")
	deadline := time.Now().Add(30 * time.Second)
	brief := structuredBrief("budget-deadline", "agent:mitos.generalist")
	brief.TimeoutMinutes = 60
	brief.Scheduling.Deadline = &deadline
	r, err := e.LaunchStructured(brief)
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "budget-claim")
	if binding.BudgetSeconds <= 0 || binding.BudgetSeconds > 30 {
		t.Fatalf("scheduling deadline did not cap the budget: %d", binding.BudgetSeconds)
	}
	plain, err := e.LaunchStructured(structuredBrief("budget-manifest", "agent:mitos.generalist"))
	if err != nil {
		t.Fatal(err)
	}
	brief60, _ := e.Store.Get(plain.ID)
	brief60.Brief.TimeoutMinutes = 60
	// Re-admit through the claim path by launching with the longer brief.
	longer, err := e.LaunchStructured(func() Brief {
		b := structuredBrief("budget-manifest-60", "agent:mitos.generalist")
		b.TimeoutMinutes = 60
		return b
	}())
	if err != nil {
		t.Fatal(err)
	}
	binding = testWorkerClaim(t, e, token, identity, longer.ID, "budget-claim-60")
	if binding.BudgetSeconds <= 0 || binding.BudgetSeconds > 1800 {
		t.Fatalf("definition timeout did not cap the 60-minute budget: %d", binding.BudgetSeconds)
	}
}
