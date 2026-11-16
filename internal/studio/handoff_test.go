package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mitos/internal/cooperation"
	"mitos/internal/decision"
)

func handoffFixture(t *testing.T) (*Engine, Run, AgentSession, string) {
	t.Helper()
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	spec := graphWorker("worker", g.RootTask)
	spec.Acceptance = "operator"
	g, _ = amendGraphTest(t, e, g, "worker", GraphOperation{Kind: "add", Spec: &spec}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "fixture"})
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var r Run
	for _, m := range g.Members {
		if m.Spec.Kind == "application" {
			r, _ = e.Store.Get(m.CurrentRunID)
		}
	}
	if r.ID == "" {
		t.Fatal("no run")
	}
	if err := e.Store.Transition(r.ID, "fixture_started", "running", "Running", "fixture"); err != nil {
		t.Fatal(err)
	}
	a, token, err := e.Store.CreateBoundAgentSession("codex", "Handoff", e.Root, req.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	return e, r, a, token
}
func callHandoff(t *testing.T, e *Engine, a AgentSession, token, method string, in any) any {
	t.Helper()
	raw, _ := json.Marshal(in)
	out, err := e.HandoffCall(a.ID, token, method, raw)
	if err != nil {
		t.Fatal(method, err)
	}
	return out
}
func saveEvidence(t *testing.T, e *Engine, r Run, name string, data []byte) ArtifactDescriptor {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.Root, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := e.Store.registerArtifact(r.ID, e.Root, ArtifactRegister{Path: name, Role: "report"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestHandoffResultIdentityReplayAndIntegrity(t *testing.T) {
	e, r, a, token := handoffFixture(t)
	ref := saveEvidence(t, e, r, "report.md", []byte("workspace report"))
	if err := e.Store.SaveArtifact(r.ID, "report.md", []byte("native summary")); err != nil {
		t.Fatal(err)
	}
	page := callHandoff(t, e, a, token, "artifact_list", ArtifactList{RunID: r.ID, Limit: 32}).(ArtifactPage)
	if len(page.Artifacts) != 2 || page.Artifacts[0].ID == page.Artifacts[1].ID || page.Artifacts[0].Name != page.Artifacts[1].Name {
		t.Fatal(page)
	}
	in := ResultSubmission{Version: HandoffVersion, Key: "done", Summary: "Verified findings", Artifacts: []ResultArtifact{{ArtifactID: ref.ID, Role: "report"}}, Checks: []ResultCheck{{Name: "manual check", Outcome: "passed", Evidence: []string{ref.ID}}}}
	first := callHandoff(t, e, a, token, "result_submit", in)
	again := callHandoff(t, e, a, token, "result_submit", in)
	if graphDigest(first) != graphDigest(again) {
		t.Fatal("receipt changed")
	}
	in.Summary = "different"
	raw, _ := json.Marshal(in)
	if _, err := e.HandoffCall(a.ID, token, "result_submit", raw); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	run, _ := e.Store.Get(r.ID)
	if run.Result == nil || run.Result.RunID != r.ID || run.Result.TaskID != r.TaskID || run.Result.Seq == 0 {
		t.Fatal(run.Result)
	}
	if err := e.Store.Complete(r.ID, "done", false); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(e.Store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e.Store = reopened
	got, _ := reopened.Get(r.ID)
	if graphDigest(got.Result) != graphDigest(run.Result) {
		t.Fatal("receipt not replayed")
	}
	replay, err := reopened.submitResult(r.ID, run.Result.Submission)
	if err != nil || replay.ID != run.Result.ID {
		t.Fatal(replay, err)
	}
	// Known integrity failures cannot be accepted, even by the operator path.
	if err = os.WriteFile(filepath.Join(e.Store.dir, "artifacts", r.ID, run.Artifacts[0].Name), []byte("tampered report"), 0600); err != nil {
		t.Fatal(err)
	}
	e.Store.mu.Lock()
	g := graphClone(*e.Store.graphs[r.GraphAttempt.GraphID])
	err = e.Store.acceptGraphMember(&g, r.TaskID, "review", "reviewed", time.Now())
	e.Store.mu.Unlock()
	if err == nil {
		t.Fatal("accepted changed evidence")
	}
	if _, err = e.Store.artifactRange(r.ID, ArtifactRead{RunID: r.ID, ArtifactID: ref.ID, Limit: 10}, nil); err == nil {
		t.Fatal("read changed evidence")
	}
}
func TestHandoffRangesSearchPaginationAndBudget(t *testing.T) {
	e, r, _, _ := handoffFixture(t)
	// Include split UTF-8, escaping-heavy text, long lines and binary bytes.
	data := bytes.Repeat([]byte("\x00<\"\\é🙂needle"), 5000)
	ref := saveEvidence(t, e, r, "unicode.md", data)
	var restored []byte
	for offset := int64(0); ; {
		value, err := e.Store.artifactRange(r.ID, ArtifactRead{RunID: r.ID, ArtifactID: ref.ID, Offset: offset, Limit: 32767}, nil)
		if err != nil {
			t.Fatal(err)
		}
		out := value.(ArtifactRange)
		encoded, _ := json.Marshal(out)
		if len(encoded) > HandoffResponseLimit {
			t.Fatal("unbounded response")
		}
		restored = append(restored, out.Data...)
		if out.Complete {
			if out.Next != nil {
				t.Fatal(out)
			}
			break
		}
		offset = *out.Next
	}
	if !bytes.Equal(restored, data) {
		t.Fatal("range corrupts bytes")
	}
	query := "needle"
	cursor := int64(0)
	var positions []int64
	for {
		out, err := e.Store.artifactRange("operator:"+r.ID, ArtifactRead{RunID: r.ID, ArtifactID: ref.ID, Offset: cursor, Limit: HandoffReadLimit}, &ArtifactSearch{Query: query, Limit: 32})
		if err != nil {
			t.Fatal(err)
		}
		matches := out.(ArtifactMatches)
		positions = append(positions, matches.Offsets...)
		if matches.Complete {
			break
		}
		if *matches.Next <= cursor {
			t.Fatal("no progress")
		}
		cursor = *matches.Next
	}
	if len(positions) != 5000 {
		t.Fatal(len(positions))
	}
	for _, pos := range positions {
		if string(data[pos:pos+6]) != query {
			t.Fatal(pos)
		}
	}
	large := saveEvidence(t, e, r, "large.md", bytes.Repeat([]byte("x"), 20<<20))
	out, err := e.Store.artifactRange(r.ID, ArtifactRead{RunID: r.ID, ArtifactID: large.ID, Offset: 19 << 20, Limit: HandoffReadLimit}, nil)
	if err != nil || out.(ArtifactRange).Complete {
		t.Fatal(out, err)
	}
	// Thousands of files are discoverable without an unbounded metadata response.
	batch := []ArtifactData{}
	for i := 0; i < 2000; i++ {
		batch = append(batch, ArtifactData{Name: fmt.Sprintf("file-%04d", i), Data: []byte("x")})
	}
	if err = e.Store.SaveArtifacts(r.ID, batch...); err != nil {
		t.Fatal(err)
	}
	count := 0
	for cursor := 0; ; {
		page, err := e.Store.listArtifacts(r.ID, ArtifactList{RunID: r.ID, Cursor: cursor, Limit: 32})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(page)
		if len(encoded) > HandoffResponseLimit {
			t.Fatal(len(encoded))
		}
		count += len(page.Artifacts)
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	if count != 2002 {
		t.Fatal(count)
	}
	for i := 0; i < 70; i++ {
		_, err = e.Store.artifactRange(r.ID, ArtifactRead{RunID: r.ID, ArtifactID: large.ID, Limit: HandoffReadLimit}, nil)
		if err != nil {
			break
		}
	}
	var failure *HandoffFailure
	if !errors.As(err, &failure) || failure.Code != "retrieval_budget_exhausted" || failure.Position != 0 {
		t.Fatal(err)
	}
	if _, err = e.Store.listArtifacts(r.ID, ArtifactList{RunID: r.ID, Limit: 32}); err != nil {
		t.Fatal("budget hides metadata", err)
	}
}
func TestHandoffAuthorizationAndUnsafePaths(t *testing.T) {
	e, r, a, _ := handoffFixture(t)
	ref := saveEvidence(t, e, r, "safe", []byte("evidence"))
	raw, _ := json.Marshal(ArtifactRead{RunID: r.ID, ArtifactID: ref.ID, Limit: 10})
	if _, err := e.HandoffCall(a.ID, "forged", "artifact_read", raw); err == nil {
		t.Fatal("forged session")
	}
	if _, err := e.Store.artifactRange("unknown", ArtifactRead{RunID: r.ID, ArtifactID: ref.ID, Limit: 10}, nil); err == nil {
		t.Fatal("ungranted lookup")
	}
	if err := os.Symlink(filepath.Join(e.Root, "safe"), filepath.Join(e.Root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../safe", "/safe", "link"} {
		if _, err := e.Store.registerArtifact(r.ID, e.Root, ArtifactRegister{Path: name, Role: "file"}); err == nil {
			t.Fatal(name)
		}
	}
	run, _ := e.Store.Get(r.ID)
	path := filepath.Join(e.Store.dir, "artifacts", r.ID, run.Artifacts[0].Name)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.Root, "safe"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.artifactRange(r.ID, ArtifactRead{RunID: r.ID, ArtifactID: ref.ID, Limit: 10}, nil); err == nil {
		t.Fatal("retained symlink accepted")
	}
}
func TestCooperationV2SettlementRaceAndLifetimes(t *testing.T) {
	e, r, _, _ := handoffFixture(t)
	task, _ := e.Store.Task(r.TaskID)
	request := cooperation.Message{Version: cooperation.Version, ID: "request", TaskID: r.TaskID, RunID: r.ID, Revision: task.Revision, Kind: "question", Body: "Choose", PermittedResolvers: []cooperation.Resolver{{TaskID: r.TaskID, RunID: r.ID, Revision: task.Revision}}, BlockedActions: []cooperation.BlockedAction{{TaskID: r.TaskID, RunID: r.ID, Revision: task.Revision, Action: "acceptance"}}}
	if _, err := e.SubmitCooperation(r.ProjectID, request); err != nil {
		t.Fatal(err)
	}
	if notices := e.Store.CooperationNotices(); len(notices) != 1 || !notices[0].Blocking {
		t.Fatal("pending request not visible", notices)
	}
	if err := e.Store.Complete(r.ID, "premature", false); err == nil {
		t.Fatal("ignored blocking request")
	}
	answer := cooperation.Message{Version: cooperation.Version, ID: "reply", TaskID: r.TaskID, RunID: r.ID, Revision: task.Revision, Kind: "answer", InReplyTo: "request", Body: "selected"}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			copy := answer
			copy.ID = fmt.Sprintf("reply-%d", i)
			_, err := e.SubmitCooperation(r.ProjectID, copy)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal(wins)
	}
	if notices := e.Store.CooperationNotices(); len(notices) != 0 {
		t.Fatal("answered request still pending", notices)
	}
	request.ID = "cancel-me"
	request.BlockedActions = nil
	if _, err := e.SubmitCooperation(r.ProjectID, request); err != nil {
		t.Fatal(err)
	}
	cancel := answer
	cancel.ID = "cancel"
	cancel.Kind = "cancel"
	cancel.InReplyTo = request.ID
	first, err := e.SubmitCooperation(r.ProjectID, cancel)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.SubmitCooperation(r.ProjectID, cancel)
	if err != nil || graphDigest(first) != graphDigest(again) {
		t.Fatal(err)
	}
	answer.ID = "late"
	answer.InReplyTo = request.ID
	if _, err = e.SubmitCooperation(r.ProjectID, answer); err == nil {
		t.Fatal("late answer")
	}
	request.ID = "expire"
	expiry := time.Now().Add(5 * time.Millisecond)
	request.ExpiresAt = &expiry
	if _, err = e.SubmitCooperation(r.ProjectID, request); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	answer.ID = "expired-answer"
	answer.InReplyTo = request.ID
	if _, err = e.SubmitCooperation(r.ProjectID, answer); err == nil {
		t.Fatal("expired answer")
	}
	request.ID = "replace"
	request.ExpiresAt = nil
	if _, err = e.SubmitCooperation(r.ProjectID, request); err != nil {
		t.Fatal(err)
	}
	e.Store.mu.Lock()
	e.Store.tasks[r.TaskID].Revision++
	e.Store.mu.Unlock()
	answer.Revision++
	answer.ID = "replacement-answer"
	answer.InReplyTo = request.ID
	if _, err = e.SubmitCooperation(r.ProjectID, answer); err == nil {
		t.Fatal("replacement adopted request")
	}
	states := map[string]string{}
	for _, rec := range e.Store.Cooperation(r.ProjectID) {
		states[rec.Message.ID] = rec.State
	}
	if states["request"] != "answered" || states["cancel-me"] != "cancelled" || states["expire"] != "expired" || states["replace"] != "superseded" {
		t.Fatal(states)
	}
}

func TestCooperationExplicitCrossTaskResolverAndFrozenArtifactGrant(t *testing.T) {
	e, origin, _, _ := handoffFixture(t)
	g, _ := e.Store.Graph(origin.GraphAttempt.GraphID)
	spec := graphWorker("resolver", g.RootTask)
	spec.Acceptance = "operator"
	g, _ = amendGraphTest(t, e, g, "resolver", GraphOperation{Kind: "add", Spec: &spec})
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var resolver Run
	for _, m := range g.Members {
		if m.Spec.Kind == "application" && m.Spec.ID != origin.TaskID {
			resolver, _ = e.Store.Get(m.CurrentRunID)
		}
	}
	if err := e.Store.Transition(resolver.ID, "started", "running", "Running", "resolver"); err != nil {
		t.Fatal(err)
	}
	task, _ := e.Store.Task(origin.TaskID)
	other, _ := e.Store.Task(resolver.TaskID)
	msg := cooperation.Message{Version: cooperation.Version, ID: "help", TaskID: origin.TaskID, RunID: origin.ID, Revision: task.Revision, Kind: "help", Body: "Review", PermittedResolvers: []cooperation.Resolver{{TaskID: resolver.TaskID, RunID: resolver.ID, Revision: other.Revision}}}
	if _, err := e.SubmitCooperation(origin.ProjectID, msg); err != nil {
		t.Fatal(err)
	}
	answer := cooperation.Message{Version: cooperation.Version, ID: "answer", TaskID: origin.TaskID, RunID: origin.ID, Revision: task.Revision, Kind: "answer", InReplyTo: msg.ID, Body: "Reviewed"}
	if _, err := e.SubmitCooperation(origin.ProjectID, answer); err == nil {
		t.Fatal("unlisted origin resolved own request")
	}
	answer.TaskID = resolver.TaskID
	answer.RunID = resolver.ID
	answer.Revision = other.Revision
	if _, err := e.SubmitCooperation(origin.ProjectID, answer); err != nil {
		t.Fatal(err)
	}

	first, err := e.Store.CooperationPage(origin.ProjectID, origin.ID, 0, 1)
	if err != nil || len(first.Messages) != 1 || first.Messages[0].Message.ID != msg.ID || first.Next == nil {
		t.Fatal("request page", first, err)
	}
	second, err := e.Store.CooperationPage(origin.ProjectID, origin.ID, *first.Next, 1)
	if err != nil || len(second.Messages) != 1 || second.Messages[0].Message.ID != answer.ID || second.Messages[0].Message.Body != answer.Body || second.Next != nil {
		t.Fatal("cross-task answer hidden", second, err)
	}
	raw, _ := json.Marshal(CooperationList{Limit: 16})
	native, err := e.handoffRequests(origin.ID, raw)
	if err != nil {
		t.Fatal(err)
	}
	messages := native.(map[string]any)["messages"].([]CooperationRecord)
	if len(messages) != 2 || messages[1].Message.ID != answer.ID {
		t.Fatal("native history diverges", messages)
	}
	unrelated, err := e.Store.CooperationPage(origin.ProjectID, "unrelated-run", 0, 16)
	if err != nil || len(unrelated.Messages) != 0 {
		t.Fatal("unrelated run received conversation", unrelated, err)
	}
	// Merely belonging to the same graph or knowing an ID grants no artifact access.
	ref := saveEvidence(t, e, origin, "report.txt", []byte("evidence"))
	if _, err := e.Store.artifactRange(resolver.ID, ArtifactRead{RunID: origin.ID, ArtifactID: ref.ID, Limit: 10}, nil); err == nil {
		t.Fatal("same graph grants all files")
	}
	msg.ID = "help-with-evidence"
	msg.Artifacts = []string{ref.ID}
	if _, err := e.SubmitCooperation(origin.ProjectID, msg); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.artifactRange(resolver.ID, ArtifactRead{RunID: origin.ID, ArtifactID: ref.ID, Limit: 10}, nil); err != nil {
		t.Fatal("named resolver cannot read cited evidence", err)
	}
	if _, err := e.Store.readResult(resolver.ID, ResultRead{RunID: origin.ID, Limit: 1}); err == nil {
		t.Fatal("artifact grant exposes entire result")
	}
	e.Store.mu.Lock()
	copy := graphClone(*e.Store.runs[resolver.ID])
	copy.ReviewRuns = []string{origin.ID}
	e.Store.runs[resolver.ID] = &copy
	e.Store.mu.Unlock()
	if _, err := e.Store.artifactRange(resolver.ID, ArtifactRead{RunID: origin.ID, ArtifactID: ref.ID, Limit: 10}, nil); err != nil {
		t.Fatal(err)
	}
	// A later run on the same task is not included in the frozen review grant.
	e.Store.mu.Lock()
	next := graphClone(*e.Store.runs[origin.ID])
	next.ID = "replacement"
	e.Store.runs[next.ID] = &next
	e.Store.mu.Unlock()
	if _, err := e.Store.listArtifacts(resolver.ID, ArtifactList{RunID: next.ID, Limit: 1}); err == nil {
		t.Fatal("grant moved to replacement")
	}
}

func TestHandoffMetadataEnvelopeAndMissingSubmission(t *testing.T) {
	e, r, _, _ := handoffFixture(t)
	batch := []ArtifactData{}
	for i := 0; i < 40; i++ {
		batch = append(batch, ArtifactData{Name: fmt.Sprintf("encoded-%d", i), DisplayName: strings.Repeat("<", 512), Role: "file", Data: []byte("x")})
	}
	if err := e.Store.SaveArtifacts(r.ID, batch...); err != nil {
		t.Fatal(err)
	}
	page, err := e.Store.listArtifacts(r.ID, ArtifactList{RunID: r.ID, Limit: 32})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(page)
	if len(body) > HandoffResponseLimit || page.Next == nil || len(page.Artifacts) >= 32 {
		t.Fatal(len(body), page.Next, len(page.Artifacts))
	}
	e.Store.mu.Lock()
	copy := graphClone(*e.Store.runs[r.ID])
	copy.GraphAttempt.Executor.Application.Agent = &AgentBinding{}
	copy.HandoffProtocol = HandoffVersion
	e.Store.runs[r.ID] = &copy
	e.Store.mu.Unlock()
	if err := e.Store.Complete(r.ID, "prose only", true); err == nil {
		t.Fatal("missing submission silently completed")
	}
}
