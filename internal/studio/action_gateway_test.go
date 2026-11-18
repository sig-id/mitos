package studio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/structuredworker"
)

func TestActionGatewayLostResponseHoldSurvivesReplacementAndRestart(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	var mu sync.Mutex
	posts, queries := 0, 0
	original := ""
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer target-secret" {
			t.Error("target credential absent")
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "POST" {
			posts++
			original = r.Header.Get("Idempotency-Key")
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		queries++
		if r.URL.Path != "/operations/"+original || r.Header.Get("Idempotency-Key") != original {
			t.Error("reconciliation changed identity")
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"idempotency_key": original, "state": "applied", "result": map[string]any{"answer": 42, "confidence": "high", "tags": []string{}}})
	}))
	defer external.Close()
	principal, _ := serviceTestPrincipal(t, e.Store, "actions", "tenant", true)
	target, err := e.Store.ConfigureActionTarget(ActionTargetInput{ID: "ledger", Tenant: principal.Tenant, Agents: []string{"mitos.generalist"}, Endpoint: external.URL, Bearer: "target-secret", InputSchema: json.RawMessage(answerSchema), ResultSchema: json.RawMessage(answerSchema)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	brief := structuredBrief("external-action", "agent:mitos.generalist")
	brief.Structured.ActionTargets = []string{target.ID}
	r, err := e.Application(principal).Admit(brief)
	if err != nil {
		t.Fatal(err)
	}
	token, identity := testWorker(t, e, "action-worker")
	binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-action")
	assignment, err := e.WorkerContext(token, binding)
	if err != nil {
		t.Fatal(err)
	}
	contextRaw, _ := json.Marshal(assignment)
	if len(assignment.Actions) != 1 || strings.Contains(string(contextRaw), "target-secret") || strings.Contains(string(contextRaw), external.URL) {
		t.Fatal("action credential or endpoint exposed to worker")
	}
	body := json.RawMessage(`{"target":"ledger","key":"charge-one","payload":{"answer":42,"confidence":"high","tags":[]}}`)
	request := structuredworker.Mutation{Binding: binding, RequestID: "action-one", Kind: "action", Data: body}
	receipt, err := e.WorkerAction(context.Background(), token, request)
	if err != nil || receipt.State != "prepared" {
		t.Fatal("dispatch", receipt, err)
	}
	if e.Store.externalOperations[receipt.OperationID].State != "unknown" || e.Store.actionHolds[actionHoldKey(r.TaskID, target.ID)] != receipt.OperationID {
		t.Fatal("uncertain effect not held on Task")
	}
	again, err := e.WorkerAction(context.Background(), token, request)
	if err != nil || graphDigest(again) != graphDigest(receipt) {
		t.Fatal("lost action receipt not replayed", again, err)
	}
	request.RequestID = "different-key"
	request.Data = json.RawMessage(`{"target":"ledger","key":"charge-two","payload":{"answer":42,"confidence":"high","tags":[]}}`)
	if _, err = e.WorkerAction(context.Background(), token, request); !errors.Is(err, ErrReconciliationRequired) {
		t.Fatal("different key bypassed target hold", err)
	}
	result := structuredworker.Mutation{Binding: binding, RequestID: "premature-result", Kind: "result", Data: json.RawMessage(`{"value":{"answer":42,"confidence":"high","tags":[]}}`)}
	if _, err = e.Store.WorkerMutate(token, result); !errors.Is(err, ErrReconciliationRequired) {
		t.Fatal("unresolved effect accepted as complete", err)
	}
	// Recovery reads the same canonical Task-level hold and immutable target.
	dir := e.Store.Dir()
	if err = e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = s
	defer s.Close()
	if e.Store.actionHolds[actionHoldKey(r.TaskID, target.ID)] != receipt.OperationID {
		t.Fatal("hold lost on restart")
	}
	prior, _ := e.Store.Get(r.ID)
	if err = e.recoverStructured(prior); err != nil {
		t.Fatal(err)
	}
	task, _ := e.Store.Task(r.TaskID)
	next := testWorkerClaim(t, e, token, identity, task.CurrentRunID, "replacement-action")
	// The replacement context names the held operation and its original key.
	replacement, err := e.WorkerContext(token, next)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pending := range replacement.PendingActions {
		if pending.OperationID != receipt.OperationID || pending.Target != target.ID || pending.Key != "charge-one" {
			continue
		}
		found = true
	}
	if !found {
		t.Fatalf("held operation not disclosed to the replacement: %+v", replacement.PendingActions)
	}
	replacementRaw, _ := json.Marshal(replacement)
	if strings.Contains(string(replacementRaw), "target-secret") || strings.Contains(string(replacementRaw), external.URL) {
		t.Fatal("pending action disclosure leaked credentials or endpoint")
	}
	request.Binding = next
	request.RequestID = "replacement-different-key"
	if _, err = e.WorkerAction(context.Background(), token, request); !errors.Is(err, ErrReconciliationRequired) {
		t.Fatal("replacement bypassed Task hold", err)
	}
	reconciled, err := e.WorkerAction(context.Background(), token, structuredworker.Mutation{Binding: next, RequestID: "reconcile-one", Kind: "reconcile", Data: json.RawMessage(`{"target":"ledger","key":"charge-one"}`)})
	if err != nil || reconciled.State != "applied" || reconciled.OperationID != receipt.OperationID {
		t.Fatal("original identity not reconciled", reconciled, err)
	}
	if e.Store.actionHolds[actionHoldKey(r.TaskID, target.ID)] != "" {
		t.Fatal("resolved hold retained")
	}
	mu.Lock()
	if posts != 1 || queries != 1 {
		t.Error("external effect repeated", posts, queries)
	}
	mu.Unlock()
	result.Binding = next
	result.RequestID = "resolved-result"
	if _, err = e.Store.WorkerMutate(token, result); err != nil {
		t.Fatal("resolved result refused", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, runtimeDatabase))
	if strings.Contains(string(raw), "target-secret") {
		t.Fatal("target secret journaled")
	}
}

func TestActionGatewayPreparationRefusalAndCurrentTargetPolicy(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	token, identity := testWorker(t, e, "policy-worker")
	input := ActionTargetInput{ID: "target", Tenant: decision.LocalTenant, Agents: []string{"mitos.generalist"}, Endpoint: "http://127.0.0.1:1", InputSchema: json.RawMessage(answerSchema), ResultSchema: json.RawMessage(answerSchema)}
	if _, err := e.Store.ConfigureActionTarget(input, decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	b := structuredBrief("policy-action", "agent:mitos.generalist")
	b.Structured.ActionTargets = []string{"target"}
	r, err := e.LaunchStructured(b)
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-policy")
	bad := structuredworker.Mutation{Binding: binding, RequestID: "bad-payload", Kind: "action", Data: json.RawMessage(`{"target":"target","key":"op","payload":{"wrong":true}}`)}
	if _, err = e.WorkerAction(context.Background(), token, bad); err == nil || len(e.Store.externalOperations) != 0 {
		t.Fatal("invalid payload persisted or dispatched", err)
	}
	input.Disabled = true
	if _, err = e.Store.ConfigureActionTarget(input, decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	bad.Data = json.RawMessage(`{"target":"target","key":"op","payload":{"answer":42,"confidence":"high","tags":[]}}`)
	if _, err = e.WorkerAction(context.Background(), token, bad); !errors.Is(err, ErrForbidden) {
		t.Fatal("current policy not checked", err)
	}
	e.Store.mu.Lock()
	lease := e.Store.workerLeases[r.ID]
	lease.ExpiresAt = time.Now().Add(-time.Second)
	_ = e.Store.commit(r.ID, "effect", "expired", change{Worker: &workerChange{Lease: &lease}})
	e.Store.mu.Unlock()
	if _, err = e.WorkerAction(context.Background(), token, bad); !errors.Is(err, ErrStaleBinding) {
		t.Fatal("stale worker reached gateway", err)
	}
}
