package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mitos/internal/agentcli"
	"mitos/internal/decision"
	"mitos/internal/harness"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type InvestigationAnswer struct {
	Choice string  `json:"choice"`
	Value  *string `json:"value"`
}
type InvestigationResult struct {
	NeedsUser bool                  `json:"needs_user"`
	Reason    string                `json:"reason"`
	Answers   []InvestigationAnswer `json:"answers"`
}

func (e *Engine) investigateAgentRequest(ctx context.Context, session AgentSession, request AgentRequest, route AgentRoute) error {
	s := e.Store
	s.mu.Lock()
	p := s.agentPolicy(session.ProjectID)
	id := graphDigest([]any{route.Key, "investigate", route.PolicyRevision})
	evaluation, exists := s.modelEvaluations[id]
	if p.Revision != route.PolicyRevision {
		s.mu.Unlock()
		return ErrConflict
	}
	if !exists {
		used := 0
		for _, v := range s.modelEvaluations {
			if v.Scope == route.Key {
				used++
			}
		}
		if used >= p.MaxCalls {
			s.mu.Unlock()
			return e.agentResolutionFallback(route, request, errors.New("investigation allowance exhausted"))
		}
		evaluation = ModelEvaluation{ID: id, Scope: route.Key, ProjectID: session.ProjectID, Purpose: "investigate", PolicyRevision: p.Revision, Status: "pending", At: time.Now().UTC(), Deadline: time.Now().UTC().Add(2 * time.Minute)}
		if d, ok := ctx.Deadline(); ok && d.Before(evaluation.Deadline) {
			evaluation.Deadline = d
		}
		if err := s.commit("", "effect", "agent_investigation_requested", change{ModelEvaluation: &evaluation}); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.mu.Unlock()
	if exists && evaluation.Status == "pending" {
		return ErrEvaluationPending
	}
	if !exists {
		native, err := e.Desk.Agents.Prepare(agentcli.Request{AgentID: "codex"})
		var result InvestigationResult
		if err == nil {
			child, cancel := context.WithDeadline(ctx, evaluation.Deadline)
			encoded, _ := json.Marshal(request.Request.Cards)
			prompt := "Investigate this pending request using read-only inspection of the workspace. Do not change files, execute effects, delegate, or assume missing user intent. Project guidance is not permission. Return needs_user=true if the answer requires a user preference or facts you cannot establish. Otherwise return one answer per supplied card, in order, using its exact option ID and a string value only for text input. Explain the evidence in reason.\nGuidance:\n" + p.Guidance + "\nCards:\n" + string(encoded)
			schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["needs_user","reason","answers"],"properties":{"needs_user":{"type":"boolean"},"reason":{"type":"string"},"answers":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["choice","value"],"properties":{"choice":{"type":"string"},"value":{"type":["string","null"]}}}}}}`)
			err = runReadOnlyInvestigator(child, native, session.Workspace, prompt, schema, &result)
			cancel()
		}
		evaluation.Status = "failed"
		evaluation.Reason = "investigation_failed"
		if err == nil && len(result.Reason) <= 10000 {
			evaluation.Investigation = &result
			evaluation.Status = "selected"
			evaluation.Reason = ""
			if result.NeedsUser {
				evaluation.Status = "uncertain"
				evaluation.Reason = "missing_user_intent_or_evidence"
			}
		}
		s.mu.Lock()
		saveErr := s.commit("", "effect", "agent_investigation_completed", change{ModelEvaluation: &evaluation})
		s.mu.Unlock()
		if saveErr != nil {
			return saveErr
		}
	}
	result := evaluation.Investigation
	if evaluation.Status != "selected" || result == nil || result.NeedsUser || len(result.Answers) != len(request.Request.Cards) {
		return e.agentResolutionFallback(route, request, errors.New("investigation requires operator review"))
	}
	for i, a := range result.Answers {
		var value json.RawMessage
		if a.Value != nil {
			value, _ = json.Marshal(*a.Value)
		}
		if !request.Request.Cards[i].HasOption(a.Choice) || request.Request.Cards[i].ValidateValue(value) != nil {
			return e.agentResolutionFallback(route, request, errors.New("investigation returned an unsupported answer"))
		}
	}
	route.Route.Reason = result.Reason
	for i, a := range result.Answers {
		var value json.RawMessage
		if a.Value != nil {
			value, _ = json.Marshal(*a.Value)
		}
		if err := e.applyAgentResolution(session, request, route, i, a.Choice, value); err != nil {
			return err
		}
	}
	return nil
}

// denyInvestigator prevents recursive questions and denies all requested escalations.
type denyInvestigator struct {
	mu      sync.Mutex
	answers map[string]harness.Delivery
}

func (b *denyInvestigator) Submit(ctx context.Context, raw json.RawMessage) (harness.Delivery, error) {
	r, err := harness.Normalize("codex", raw)
	if err != nil {
		return harness.Delivery{}, err
	}
	if len(r.Questions) > 0 {
		return harness.Delivery{}, errors.New("investigator cannot ask recursive questions")
	}
	answers := []decision.Resolution{}
	for range r.Cards {
		answers = append(answers, decision.Resolution{Choice: "deny"})
	}
	body, err := harness.Response("codex", r, answers)
	d := harness.Delivery{RequestID: r.Key, State: "accepted", Body: body}
	b.mu.Lock()
	b.answers[r.Key] = d
	b.mu.Unlock()
	return d, err
}
func (b *denyInvestigator) Get(_ context.Context, key string) (harness.Delivery, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.answers[key], nil
}
func (b *denyInvestigator) Deliver(context.Context, string, string, string) error { return nil }
func (b *denyInvestigator) Invalidate(context.Context, string) error              { return nil }
func runReadOnlyInvestigator(ctx context.Context, native agentcli.Launch, workspace, prompt string, schema json.RawMessage, result *InvestigationResult) error {
	cmd := exec.CommandContext(ctx, native.Executable, "app-server")
	cmd.Dir = workspace
	cmd.Env = native.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	stream := &investigationStream{Reader: output, Writer: input, close: func() error { return errors.Join(input.Close(), output.Close()) }}
	options := harness.CodexOptions{Headless: true, Sandbox: "read-only", OutputSchema: schema, Result: func(text string) error {
		if len(text) > 24<<10 {
			return errors.New("investigation result too large")
		}
		decoder := json.NewDecoder(bytes.NewBufferString(text))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(result); err != nil {
			return err
		}
		if decoder.Decode(new(any)) != io.EOF {
			return errors.New("invalid trailing investigation result")
		}
		return nil
	}}
	return harness.RunCodex(ctx, &denyInvestigator{answers: map[string]harness.Delivery{}}, stream, strings.NewReader(""), io.Discard, workspace, prompt, options)
}

type investigationStream struct {
	io.Reader
	io.Writer
	close func() error
}

func (s *investigationStream) Close() error { return s.close() }
