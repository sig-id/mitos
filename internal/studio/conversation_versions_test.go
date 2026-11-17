package studio

import (
	"context"
	"encoding/json"
	"errors"
	"mitos/internal/decision"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mitos/internal/structuredworker"
)

func resultMutation(b structuredworker.Binding, id string, cursor int64) structuredworker.Mutation {
	raw, _ := json.Marshal(structuredworker.Result{ConsumedCursor: cursor, Value: json.RawMessage(`{"answer":42,"confidence":"high","tags":[]}`)})
	return structuredworker.Mutation{Binding: b, RequestID: id, Kind: "result", Data: raw}
}

func TestSupersededTaskHoldAllowsReconciliationWithoutNewEffects(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	var posts atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts.Add(1)
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"idempotency_key": r.Header.Get("Idempotency-Key"), "state": "applied", "result": map[string]any{"answer": 42, "confidence": "high", "tags": []string{}}})
	}))
	defer target.Close()
	_, err := e.Store.ConfigureActionTarget(ActionTargetInput{ID: "effect", Tenant: decision.LocalTenant, Agents: []string{"mitos.generalist"}, Endpoint: target.URL, InputSchema: json.RawMessage(answerSchema), ResultSchema: json.RawMessage(answerSchema)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	b := structuredBrief("held-preferences", "agent:mitos.generalist")
	b.Structured.ConsumedVersions = map[string]uint64{"preferences": 0}
	b.Structured.ActionTargets = []string{"effect"}
	r, err := e.LaunchStructured(b)
	if err != nil {
		t.Fatal(err)
	}
	token, identity := testWorker(t, e, "held-version-worker")
	binding := testWorkerClaim(t, e, token, identity, r.ID, "held-version-claim")
	request := structuredworker.Mutation{Binding: binding, RequestID: "prepare-effect", Kind: "action", Data: json.RawMessage(`{"target":"effect","key":"original","payload":{"answer":42,"confidence":"high","tags":[]}}`)}
	if _, err = e.WorkerAction(context.Background(), token, request); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store.AppendTaskInput("", TaskInputRequest{Key: "advance-held", Versions: []VersionAdvance{{Name: "preferences", Expected: 0, Value: 1}}}, decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "stale-effect"
	if _, err = e.WorkerAction(context.Background(), token, request); !errors.Is(err, ErrSuperseded) {
		t.Fatal("stale context dispatched effect", err)
	}
	if _, err = e.Store.WorkerMutate(token, structuredworker.Mutation{Binding: binding, RequestID: "release-held", Kind: "release"}); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileWorkerLeases(); err != nil {
		t.Fatal(err)
	}
	task, _ := e.Store.Task(r.TaskID)
	next := testWorkerClaim(t, e, token, identity, task.CurrentRunID, "held-reconcile-claim")
	if next.Acceptance != "reconcile-only/current-permissions" {
		t.Fatal("superseded replacement not restricted", next.Acceptance)
	}
	_, err = e.WorkerAction(context.Background(), token, structuredworker.Mutation{Binding: next, RequestID: "settle-original", Kind: "reconcile", Data: json.RawMessage(`{"target":"effect","key":"original"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store.WorkerMutate(token, resultMutation(next, "held-stale-result", 0)); !errors.Is(err, ErrSuperseded) {
		t.Fatal("reconciliation accepted stale result", err)
	}
	if posts.Load() != 1 || e.Store.taskHasActionHoldLocked(r.TaskID) {
		t.Fatal("original operation not settled exactly once", posts.Load())
	}
}

func TestWorkerFeatureMatchingKeepsBaseWorkersOffWaitingTasks(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	r, err := e.LaunchStructured(waitingBrief("feature-wait"))
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := e.Store.CreateWorkerCredential(WorkerCredential{ID: "base-worker", Pools: []string{"interactive"}, Agents: []string{"mitos.generalist"}, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Store.RegisterWorker(token, structuredworker.Register{RequestID: "register-base", Incarnation: "base", Capabilities: []string{"mitos.generalist"}})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := e.Store.WorkerClaim(token, structuredworker.Claim{Identity: structuredworker.Identity{ID: "base-worker", Incarnation: "base"}, RequestID: "claim-base", RunID: r.ID})
	if err != nil || receipt.Binding != nil {
		t.Fatal("base worker received unsupported wait", receipt, err)
	}
	capable, identity := testWorker(t, e, "feature-worker")
	_ = testWorkerClaim(t, e, capable, identity, r.ID, "claim-feature")
}

func TestConversationKeySerializesOnlyActiveAttemptsWithinTenant(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	policy := defaultRuntimePolicy()
	policy.Capacity = 3
	if err := e.Store.ConfigureRuntimePolicy(policy); err != nil {
		t.Fatal(err)
	}
	a, _ := serviceTestPrincipal(t, e.Store, "serial-a", "a", true)
	b, _ := serviceTestPrincipal(t, e.Store, "serial-b", "b", true)
	token, identity := testWorker(t, e, "serial-worker")
	first := waitingBrief("serial-first")
	first.Structured.SerializationKey = "personal-conversation"
	second := waitingBrief("serial-second")
	second.Structured.SerializationKey = first.Structured.SerializationKey
	one, err := e.Application(a).Admit(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := e.Application(a).Admit(second)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := e.Application(b).Admit(first)
	if err != nil {
		t.Fatal(err)
	}
	claimOne := testWorkerClaim(t, e, token, identity, one.ID, "serial-claim-one")
	blocked, err := e.Store.WorkerClaim(token, structuredworker.Claim{Identity: identity, RequestID: "serial-blocked", RunID: two.ID})
	if err != nil || blocked.Binding != nil {
		t.Fatal("same conversation concurrently admitted", blocked, err)
	}
	_ = testWorkerClaim(t, e, token, identity, foreign.ID, "serial-claim-foreign")
	if _, err = e.Store.WorkerMutate(token, waitMutation(claimOne, "serial-wait", 0)); err != nil {
		t.Fatal(err)
	}
	claimTwo := testWorkerClaim(t, e, token, identity, two.ID, "serial-claim-two")
	woke, err := e.Application(a).AppendInput(one.TaskID, TaskInputRequest{Key: "answer", Data: json.RawMessage(`{"answer":42}`)})
	if err != nil || woke.State != "woke" {
		t.Fatal(woke, err)
	}
	blocked, err = e.Store.WorkerClaim(token, structuredworker.Claim{Identity: identity, RequestID: "serial-wake-blocked", RunID: woke.RunID})
	if err != nil || blocked.Binding != nil {
		t.Fatal("wake bypassed active conversation holder", blocked, err)
	}
	if _, err = e.Store.WorkerMutate(token, resultMutation(claimTwo, "serial-finish", 0)); err != nil {
		t.Fatal(err)
	}
	next := testWorkerClaim(t, e, token, identity, woke.RunID, "serial-claim-resumed")
	if next.Generation <= claimOne.Generation {
		t.Fatal("serialized wake revived old grant")
	}
}

func TestTenantVersionCASAdmissionAndSupersededResultEvidence(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	a, _ := serviceTestPrincipal(t, e.Store, "versions-a", "a", true)
	b, _ := serviceTestPrincipal(t, e.Store, "versions-b", "b", true)
	token, identity := testWorker(t, e, "versions-worker")
	brief := structuredBrief("versions-root", "agent:mitos.generalist")
	brief.Structured.ConsumedVersions = map[string]uint64{"preferences": 0}
	brief.Structured.SerializationKey = "personal"
	r, err := e.Application(a).Admit(brief)
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "versions-claim")
	input := TaskInputRequest{Key: "preferences-one", Data: json.RawMessage(`{"marker":"alpha-only-context"}`), Versions: []VersionAdvance{{Name: "preferences", Expected: 0, Value: 1}}}
	receipt, err := e.Application(a).AppendInput("", input)
	if err != nil || receipt.Versions["preferences"] != 1 {
		t.Fatal("version advance", receipt, err)
	}
	seq := e.Store.seq
	if again, err := e.Application(a).AppendInput("", input); err != nil || graphDigest(again) != graphDigest(receipt) || seq != e.Store.seq {
		t.Fatal("version advance replay", again, err)
	}
	changed := input
	changed.Key = "wrong-cas"
	changed.Versions = []VersionAdvance{{Name: "preferences", Expected: 0, Value: 2}}
	if _, err = e.Application(a).AppendInput("", changed); !errors.Is(err, ErrVersionConflict) {
		t.Fatal("stale CAS advanced register", err)
	}
	if _, err = e.Application(b).Admit(brief); err != nil {
		t.Fatal("tenant version leaked across boundary", err)
	}
	page, err := e.Application(b).Feed(0, 200)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "alpha-only-context") {
		t.Fatal("global version input escaped tenant feed")
	}
	mutation := resultMutation(binding, "superseded-result", 0)
	rejected, err := e.Store.WorkerMutate(token, mutation)
	if !errors.Is(err, ErrSuperseded) || rejected.State != "superseded_context" {
		t.Fatal("stale preferences accepted", rejected, err)
	}
	seq = e.Store.seq
	if again, err := e.Store.WorkerMutate(token, mutation); !errors.Is(err, ErrSuperseded) || graphDigest(again) != graphDigest(rejected) || seq != e.Store.seq {
		t.Fatal("superseded evidence duplicated", again, err)
	}
	failed, _ := e.Store.Get(r.ID)
	if failed.Status != "failed" || failed.Verified || failed.Events[len(failed.Events)-1].Kind != "superseded_worker_result" {
		t.Fatal("superseded result not retained and rejected")
	}
	if again, err := e.Application(a).Admit(brief); err != nil || again.ID != r.ID {
		t.Fatal("preference change broke original admission receipt", again.ID, err)
	}
	stale := graphClone(brief)
	stale.Structured.Key = "new-stale"
	if _, err = e.Application(a).Admit(stale); !errors.Is(err, ErrSuperseded) {
		t.Fatal("outdated new admission accepted", err)
	}
	fresh := graphClone(stale)
	fresh.Structured.Key = "new-fresh"
	fresh.Structured.ConsumedVersions["preferences"] = 1
	fresh.Structured.Input = json.RawMessage(`{"preferences":"fresh"}`)
	if _, err = e.Application(a).Admit(fresh); err != nil {
		t.Fatal("current snapshot refused", err)
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
	if s.tenantVersions[scopedRequestKey(a.Tenant, "preferences")] != 1 {
		t.Fatal("version register not durable")
	}
}

func TestWakeNeedsExplicitCurrentSnapshotAndConsumesItsInputPage(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	p, _ := serviceTestPrincipal(t, e.Store, "snapshot-app", "tenant", true)
	token, identity := testWorker(t, e, "snapshot-worker")
	brief := waitingBrief("snapshot-root")
	brief.Structured.ConsumedVersions = map[string]uint64{"preferences": 0}
	r, err := e.Application(p).Admit(brief)
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "snapshot-claim")
	if _, err = e.Store.WorkerMutate(token, waitMutation(binding, "snapshot-wait", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Application(p).AppendInput("", TaskInputRequest{Key: "advance", Versions: []VersionAdvance{{Name: "preferences", Expected: 0, Value: 1}}}); err != nil {
		t.Fatal(err)
	}
	withheld, err := e.Application(p).AppendInput(r.TaskID, TaskInputRequest{Key: "answer", Data: json.RawMessage(`{"answer":42}`)})
	if err != nil || withheld.State != "wake_withheld" {
		t.Fatal("old data relabeled as a new version", withheld, err)
	}
	woke, err := e.Application(p).AppendInput(r.TaskID, TaskInputRequest{Key: "fresh-profile", Data: json.RawMessage(`{"preferences":"fresh"}`), ConsumedVersions: map[string]uint64{"preferences": 1}})
	if err != nil || woke.State != "woke" {
		t.Fatal("fresh snapshot did not wake", woke, err)
	}
	next := testWorkerClaim(t, e, token, identity, woke.RunID, "snapshot-resume")
	if e.Store.runs[next.RunID].Brief.Structured.ConsumedVersions["preferences"] != 1 {
		t.Fatal("new grant missing explicit snapshot version")
	}
	rejected, err := e.Store.WorkerMutate(token, resultMutation(next, "snapshot-unread", 1))
	if !errors.Is(err, ErrInputsNotConsumed) || rejected.State != "inputs_not_consumed" {
		t.Fatal("unread snapshot accepted", rejected, err)
	}
	if err = e.reconcileWorkerLeases(); err != nil {
		t.Fatal(err)
	}
	task, _ := e.Store.Task(r.TaskID)
	fresh := testWorkerClaim(t, e, token, identity, task.CurrentRunID, "snapshot-reconsider")
	assignment, err := e.WorkerContext(token, fresh)
	if err != nil || assignment.Inputs.Next != 2 || assignment.ConsumedVersions["preferences"] != 1 {
		t.Fatal("fresh snapshot not in context", assignment, err)
	}
	if _, err = e.Store.WorkerMutate(token, resultMutation(fresh, "snapshot-accepted", 2)); err != nil {
		t.Fatal("consumed current snapshot rejected", err)
	}
}
