package studio

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRetentionFollowsLogicalTaskAfterWaitAndActiveRetry(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	p, _ := serviceTestPrincipal(t, e.Store, "retention-app", "tenant", true)
	token, identity := testWorker(t, e, "retention-worker")
	brief := waitingBrief("retention-wait")
	r, err := e.Application(p).Admit(brief)
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "retention-claim")
	if _, err = e.Store.WorkerMutate(token, waitMutation(binding, "retention-wait", 0)); err != nil {
		t.Fatal(err)
	}
	woke, err := e.Application(p).AppendInput(r.TaskID, TaskInputRequest{Key: "answer", Data: json.RawMessage(`{"answer":42}`)})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := e.Store.ExpireStructuredKeys(time.Now().Add(time.Second)); err != nil || n != 0 {
		t.Fatal("live wake lost original receipt", n, err)
	}
	next := testWorkerClaim(t, e, token, identity, woke.RunID, "retention-resume")
	if _, err = e.Store.WorkerMutate(token, resultMutation(next, "retention-accepted", 1)); err != nil {
		t.Fatal(err)
	}
	if n, err := e.Store.ExpireStructuredKeys(time.Now().Add(time.Second)); err != nil || n != 1 {
		t.Fatal("completed dormant root never expired", n, err)
	}
	if _, err = e.Application(p).Admit(brief); !errors.Is(err, ErrRequestKeyExpired) {
		t.Fatal("expired logical request ran again", err)
	}
	retryBrief := structuredBrief("retention-retry", "agent:mitos.generalist")
	root, err := e.Application(p).Admit(retryBrief)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Store.Transition(root.ID, "test_failed", "failed", "Failed", "Retry pending"); err != nil {
		t.Fatal(err)
	}
	child, err := e.Retry(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := e.Store.ExpireStructuredKeys(time.Now().Add(time.Second)); err != nil || n != 0 {
		t.Fatal("active retry lost original receipt", n, err)
	}
	oldCutoff := time.Now()
	claim := testWorkerClaim(t, e, token, identity, child.ID, "retention-retry-claim")
	if _, err = e.Store.WorkerMutate(token, resultMutation(claim, "retention-retry-result", 0)); err != nil {
		t.Fatal(err)
	}
	if n, err := e.Store.ExpireStructuredKeys(oldCutoff); err != nil || n != 0 {
		t.Fatal("retention used old root completion time", n, err)
	}
	if n, err := e.Store.ExpireStructuredKeys(time.Now().Add(time.Second)); err != nil || n != 1 {
		t.Fatal("latest terminal attempt did not qualify", n, err)
	}
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
	if _, err = e.Application(p).Admit(retryBrief); !errors.Is(err, ErrRequestKeyExpired) {
		t.Fatal("logical tombstone lost on restart", err)
	}
}
