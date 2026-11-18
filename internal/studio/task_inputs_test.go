package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/structuredworker"
)

func waitingBrief(key string) Brief {
	b := structuredBrief(key, "agent:mitos.generalist")
	b.Structured.CheckpointSchema = json.RawMessage(answerSchema)
	return b
}

func TestTaskWaitAndInputWakeCrashAtomicity(t *testing.T) {
	for _, mode := range []string{"wait", "input"} {
		for _, point := range []string{"sqlite.before_commit", "sqlite.after_commit"} {
			t.Run(mode+"/"+point, func(t *testing.T) {
				e, _ := automaticScriptEngine(t, `print('unused')`)
				e.stop()
				e.wg.Wait()
				p, appToken := serviceTestPrincipal(t, e.Store, "crash-input-app", "tenant", true)
				token, identity := testWorker(t, e, "crash-wait-worker")
				r, err := e.Application(p).Admit(waitingBrief("crash-wait"))
				if err != nil {
					t.Fatal(err)
				}
				binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-crash-wait")
				if mode == "input" {
					if _, err = e.Store.WorkerMutate(token, waitMutation(binding, "wait-before-input", 0)); err != nil {
						t.Fatal(err)
					}
				}
				dir := e.Store.Dir()
				if err = e.Store.Close(); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(binding)
				cmd := exec.Command(os.Args[0], "-test.run=^TestTaskInputCrashHelper$")
				cmd.Env = append(os.Environ(), "MITOS_TASK_INPUT_CRASH_HELPER=1", "MITOS_TASK_INPUT_CRASH_DIR="+dir, "MITOS_TASK_INPUT_CRASH_MODE="+mode, "MITOS_TASK_INPUT_CRASH_BINDING="+string(raw), "MITOS_TASK_INPUT_CRASH_WORKER_TOKEN="+token, "MITOS_TASK_INPUT_CRASH_APP_TOKEN="+appToken, "MITOS_ENABLE_FAILPOINTS=1", "MITOS_FAILPOINT="+point)
				output, err := cmd.CombinedOutput()
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 86 {
					t.Fatalf("crash not injected: %v %s", err, output)
				}
				s, err := OpenStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				landed := point == "sqlite.after_commit"
				if mode == "wait" {
					waiting := s.runs[r.ID].Status == "waiting"
					if waiting != landed || s.workerLeases[r.ID].Closed != landed || s.taskWaits[r.TaskID].Dormant != landed {
						t.Fatal("torn checkpoint/wait/grant close", point)
					}
				} else {
					task := s.tasks[r.TaskID]
					if (len(s.taskInputs[r.TaskID]) == 1) != landed || (task.CurrentRunID != r.ID) != landed {
						t.Fatal("torn input/wake", point)
					}
					if landed && s.runs[task.CurrentRunID].AuthorityGeneration <= binding.Generation {
						t.Fatal("input wake missing new generation")
					}
				}
			})
		}
	}
}
func TestTaskInputCrashHelper(t *testing.T) {
	if os.Getenv("MITOS_TASK_INPUT_CRASH_HELPER") != "1" {
		return
	}
	s, err := OpenStore(os.Getenv("MITOS_TASK_INPUT_CRASH_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var b structuredworker.Binding
	if err = json.Unmarshal([]byte(os.Getenv("MITOS_TASK_INPUT_CRASH_BINDING")), &b); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MITOS_TASK_INPUT_CRASH_MODE") == "wait" {
		_, err = s.WorkerMutate(os.Getenv("MITOS_TASK_INPUT_CRASH_WORKER_TOKEN"), waitMutation(b, "crash-wait", 0))
	} else {
		var p decision.Principal
		p, err = s.AuthenticateServicePrincipal(os.Getenv("MITOS_TASK_INPUT_CRASH_APP_TOKEN"))
		if err == nil {
			_, err = s.AppendTaskInput(b.TaskID, TaskInputRequest{Key: "answer", Data: json.RawMessage(`{"resume":true}`)}, p)
		}
	}
	t.Fatalf("crash boundary not reached: %v", err)
}

func TestDurableInputDoesNotBypassCurrentWakeAuthority(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	owner, _ := serviceTestPrincipal(t, e.Store, "wait-owner", "tenant", true)
	writer, _ := serviceTestPrincipal(t, e.Store, "wait-writer", "tenant", true)
	token, identity := testWorker(t, e, "policy-wait-worker")
	r, err := e.Application(owner).Admit(waitingBrief("policy-wait"))
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-policy-wait")
	if _, err = e.Store.WorkerMutate(token, waitMutation(binding, "policy-wait", 0)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.Store.ChangeServicePrincipal(owner.ID, false, time.Time{}, decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	receipt, err := e.Application(writer).AppendInput(r.TaskID, TaskInputRequest{Key: "answer", Data: json.RawMessage(`{"answer":42}`)})
	if err != nil || receipt.State != "wake_withheld" || receipt.RunID != r.ID {
		t.Fatal("revoked authority revived", receipt, err)
	}
	if len(e.Store.taskInputs[r.TaskID]) != 1 || len(e.Store.runs) != 1 || !e.Store.taskWaits[r.TaskID].Dormant {
		t.Fatal("withheld input was lost or dispatched")
	}
}

func TestWithheldInputMakesProgressAfterQueueSpaceReturns(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	policy := defaultRuntimePolicy()
	policy.Default.Queue = 1
	if err := e.Store.ConfigureRuntimePolicy(policy); err != nil {
		t.Fatal(err)
	}
	p, _ := serviceTestPrincipal(t, e.Store, "queue-wait-app", "tenant", true)
	token, identity := testWorker(t, e, "queue-wait-worker")
	r, err := e.Application(p).Admit(waitingBrief("queue-wait"))
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-queue-wait")
	if _, err = e.Store.WorkerMutate(token, waitMutation(binding, "wait-queue", 0)); err != nil {
		t.Fatal(err)
	}
	blocker, err := e.Application(p).Admit(waitingBrief("queue-blocker"))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := e.Application(p).AppendInput(r.TaskID, TaskInputRequest{Key: "answer", Data: json.RawMessage(`{"answer":42}`)})
	if err != nil || receipt.State != "wake_withheld" {
		t.Fatal("queue policy bypassed", receipt, err)
	}
	if err = e.Application(p).Cancel(blocker.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileDormantInputs(); err != nil {
		t.Fatal(err)
	}
	task, _ := e.Store.Task(r.TaskID)
	if task.CurrentRunID == r.ID || task.Status != "queued" || len(e.Store.pendingInputWakes) != 0 {
		t.Fatal("stored answer stranded after queue released", task.CurrentRunID, task.Status)
	}
}
func waitMutation(b structuredworker.Binding, key string, cursor int64) structuredworker.Mutation {
	raw, _ := json.Marshal(structuredworker.WaitRequest{Cursor: cursor, Checkpoint: json.RawMessage(`{"answer":1,"confidence":"low","tags":[]}`)})
	return structuredworker.Mutation{Binding: b, RequestID: key, Kind: "wait", Data: raw}
}

func TestTaskInputWaitContinuationDurableWakeAndFencing(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	p, _ := serviceTestPrincipal(t, e.Store, "wait-app", "tenant", true)
	token, identity := testWorker(t, e, "wait-worker")
	r, err := e.Application(p).Admit(waitingBrief("input-wait"))
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-wait")
	first := TaskInputRequest{Key: "first", Data: json.RawMessage(`{"message":"first"}`)}
	stored, err := e.Application(p).AppendInput(r.TaskID, first)
	if err != nil || stored.Seq != 1 || stored.State != "stored" {
		t.Fatal("input before wait", stored, err)
	}
	request := waitMutation(binding, "wait-first", 0)
	receipt, err := e.Store.WorkerMutate(token, request)
	if err != nil || receipt.State != "continue" {
		t.Fatal("existing input lost", receipt, err)
	}
	var inputs structuredworker.InputPage
	if json.Unmarshal(receipt.Result, &inputs) != nil || len(inputs.Inputs) != 1 || inputs.Next != 1 {
		t.Fatal("continuation page", string(receipt.Result))
	}
	if again, err := e.Store.WorkerMutate(token, request); err != nil || graphDigest(again) != graphDigest(receipt) {
		t.Fatal("wait replay", again, err)
	}
	request = waitMutation(binding, "wait-dormant", 1)
	receipt, err = e.Store.WorkerMutate(token, request)
	if err != nil || receipt.State != "dormant" {
		t.Fatal("wait did not release grant", receipt, err)
	}
	task, _ := e.Store.Task(r.TaskID)
	if task.Status != "dormant" {
		t.Fatal("responsibility not dormant", task.Status)
	}
	e.Store.mu.RLock()
	active := e.Store.workerActiveLocked()
	e.Store.mu.RUnlock()
	if len(active) != 0 {
		t.Fatal("dormant responsibility holds execution capacity", active)
	}
	// A restart must retain both the consumed cursor and the portable checkpoint.
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
	second := TaskInputRequest{Key: "second", Data: json.RawMessage(`{"message":"second"}`)}
	woke, err := e.Application(p).AppendInput(r.TaskID, second)
	if err != nil || woke.State != "woke" || woke.Seq != 2 || woke.RunID == r.ID {
		t.Fatal("durable input failed to wake", woke, err)
	}
	seq := s.seq
	if again, err := e.Application(p).AppendInput(r.TaskID, second); err != nil || again != woke || seq != s.seq {
		t.Fatal("input retry duplicated wake", again, err)
	}
	task, _ = s.Task(r.TaskID)
	if task.CurrentRunID != woke.RunID {
		t.Fatal("input/wake not co-committed")
	}
	next := testWorkerClaim(t, e, token, identity, woke.RunID, "claim-resumed")
	if next.Generation <= binding.Generation || next.Generation != s.runs[woke.RunID].AuthorityGeneration {
		t.Fatal("wake resurrected authority", next, binding)
	}
	assignment, err := e.WorkerContext(token, next)
	if err != nil || len(assignment.Checkpoint) == 0 || len(assignment.Inputs.Inputs) != 1 || assignment.Inputs.Inputs[0].Seq != 2 {
		t.Fatal("checkpoint/input context missing", assignment, err)
	}
	if _, err = s.WorkerMutate(token, structuredworker.Mutation{Binding: next, RequestID: "resume-checkpoint", Kind: "checkpoint", Data: json.RawMessage(`{"answer":2,"confidence":"high","tags":[]}`)}); err != nil {
		t.Fatal(err)
	}
	if s.structuredCheckpoints[r.TaskID].Cursor != 1 {
		t.Fatal("checkpoint rewound consumed input cursor")
	}

	if _, err = e.WorkerContext(token, binding); !errors.Is(err, ErrStaleBinding) {
		t.Fatal("old grant can read after wake", err)
	}
	if _, err = s.WorkerMutate(token, structuredworker.Mutation{Binding: binding, RequestID: "old-renew", Kind: "renew"}); !errors.Is(err, ErrStaleBinding) {
		t.Fatal("old grant renewed", err)
	}
	page, err := e.Application(p).Inputs(r.TaskID, 0, 1)
	if err != nil || len(page.Inputs) != 1 || !page.HasMore || page.Next != 1 {
		t.Fatal("bounded input log", page, err)
	}
	other, _ := serviceTestPrincipal(t, s, "other-input-app", "other", true)
	if _, err = e.Application(other).AppendInput(r.TaskID, first); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign input write", err)
	}
	if _, err = e.Application(other).Inputs(r.TaskID, 0, 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign input read", err)
	}
	changed := second
	changed.Data = json.RawMessage(`{"message":"changed"}`)
	if _, err = e.Application(p).AppendInput(r.TaskID, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("input identity changed", err)
	}
}

func TestTaskInputRacesWaitRegistrationWithoutLostWake(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	p, _ := serviceTestPrincipal(t, e.Store, "race-input-app", "tenant", true)
	token, identity := testWorker(t, e, "race-wait-worker")
	for i := range 12 {
		r, err := e.Application(p).Admit(waitingBrief(fmt.Sprintf("race-wait-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		binding := testWorkerClaim(t, e, token, identity, r.ID, fmt.Sprintf("claim-race-%d", i))
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() {
			<-start
			_, err := e.Store.WorkerMutate(token, waitMutation(binding, fmt.Sprintf("race-wait-%d", i), 0))
			done <- err
		}()
		go func() {
			<-start
			_, err := e.Application(p).AppendInput(r.TaskID, TaskInputRequest{Key: "answer", Data: json.RawMessage(`{"answer":42}`)})
			done <- err
		}()
		close(start)
		for range 2 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		e.Store.mu.RLock()
		wait := e.Store.taskWaits[r.TaskID]
		count := len(e.Store.taskInputs[r.TaskID])
		task := cloneTask(e.Store.tasks[r.TaskID])
		e.Store.mu.RUnlock()
		if count != 1 || wait.Dormant {
			t.Fatal("answer stranded after wait registration", count, wait)
		}
		if task.CurrentRunID == r.ID {
			current, _ := e.Store.Get(r.ID)
			if current.Status != "running" {
				t.Fatal("continuation lost active grant", current.Status)
			}
		} else {
			current, _ := e.Store.Get(task.CurrentRunID)
			if current.Status != "queued" || current.ParentID != r.ID || current.AuthorityGeneration <= binding.Generation {
				t.Fatal("wake did not create fresh authorized attempt", current.ID)
			}
		}
		if err = e.Application(p).Cancel(task.CurrentRunID); err != nil {
			t.Fatal(err)
		}
	}
}

// A resumed attempt whose Task accumulated many valid inputs must still fit the
// agent's context budget: the page is trimmed from the tail with HasMore kept,
// so assembly succeeds and the worker consumes the backlog incrementally.
func TestWorkerContextSizesInputPageToDefinitionBudget(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	p, _ := serviceTestPrincipal(t, e.Store, "backlog-app", "tenant", true)
	token, identity := testWorker(t, e, "backlog-wait-worker")
	r, err := e.Application(p).Admit(waitingBrief("backlog"))
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 2048)
	for i := range 48 {
		if _, err = e.Application(p).AppendInput(r.TaskID, TaskInputRequest{Key: fmt.Sprintf("input-%02d", i), Data: json.RawMessage(`{"note":"` + big + `"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "claim-backlog")
	assignment, err := e.WorkerContext(token, binding)
	if err != nil {
		t.Fatalf("large input backlog failed context assembly: %v", err)
	}
	if len(assignment.Inputs.Inputs) == 0 || len(assignment.Inputs.Inputs) >= 48 {
		t.Fatalf("input page was not sized to the budget: %d inputs", len(assignment.Inputs.Inputs))
	}
	if !assignment.Inputs.HasMore {
		t.Fatal("has_more lost: the worker cannot consume the backlog incrementally")
	}
	if assignment.Inputs.Next <= 0 || assignment.Inputs.Next >= int64(len(assignment.Inputs.Inputs))+1 {
		t.Fatalf("cursor regressed incorrectly: next=%d inputs=%d", assignment.Inputs.Next, len(assignment.Inputs.Inputs))
	}
	// The rendered prompt stays inside the definition's declared budget.
	var receipt ContextReceipt
	if err = json.Unmarshal(assignment.Artifacts["context.json"], &receipt); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, section := range receipt.Sections {
		if section.Included {
			total += section.Characters
		}
	}
	if total > receipt.Budget {
		t.Fatalf("assembled context %d exceeds budget %d", total, receipt.Budget)
	}
}
