package studio

// Judgment-shaped kernel rules. Deterministic invariants – digests and
// immutability, authentication and authority, schema validity, budgets,
// allowances, deadlines, CAS fencing – stay in code and keep failing closed:
// they are reproducible facts, and delegating them would be unsafe. Rules that
// instead encode a policy-of-thumb about work shape (for example "a review
// nature should not yield on follow-up work") raise exactly one bounded
// decision with two candidates, apply or refuse, each with the kernel's
// rationale:
//
//   - the graph's configured decision application decides when one is
//     registered (typed candidates, receipted, counted against the graph's
//     decision allowance);
//   - the operator decides otherwise, through the same decision cards as any
//     other judgment (`mitos decisions`, `mitos resolve`, the workflow view).
//
// The affected submission is held, not lost: the attempt parks with the
// decision pending, and nothing proceeds while it does. The conservative
// default is the old refusal semantics; only an explicit decision changes the
// outcome. On "apply" the original intent proceeds (the result is recorded and
// a blocking proposal yields as designed); on "refuse" the refusal applies,
// delivered to the proposer as feedback with the next continuation rather
// than as a dead end.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mitos/internal/decision"
	"mitos/internal/decisionmodel"
	"mitos/internal/fleet"
)

// kernelJudgmentContext bounds the state projection sent to a decision model.
const kernelJudgmentContext = 16 << 10

// judgmentRequired reports that a judgment-shaped rule would refuse a
// submission. Callers route it through a decision instead of failing closed.
type judgmentRequired struct {
	code   string
	detail string
}

func (e *judgmentRequired) Error() string { return e.code + ": " + e.detail }

// heldJudgment reports a submission already held by a pending kernel judgment.
type heldJudgment struct{ judgment KernelJudgment }

func (e *heldJudgment) Error() string {
	return "judgment pending on this attempt: " + e.judgment.Code
}

// KernelJudgment is the durable receipt of one judgment-gated rule trigger.
// While State is pending the record holds the affected submission; the settle
// event journals the outcome next to the work it decided.
type KernelJudgment struct {
	ResolverRevision *int                  `json:"resolver_revision,omitempty"`
	ID               string                `json:"id"`
	Code             string                `json:"code"`
	Detail           string                `json:"detail"`
	Question         string                `json:"question"`
	Digest           string                `json:"digest"`
	Proposals        int                   `json:"proposals"`
	Mode             string                `json:"mode"`  // decision_application or operator
	State            string                `json:"state"` // pending, applied, refused
	Reason           string                `json:"reason,omitempty"`
	Submission       *ResultSubmission     `json:"submission,omitempty"` // held while pending
	DecisionID       string                `json:"decision_id,omitempty"`
	GraphID          string                `json:"graph_id,omitempty"`
	GraphRevision    int64                 `json:"graph_revision,omitempty"`
	Request          decisionmodel.Request `json:"request,omitempty"`
	Config           *decisionmodel.Config `json:"config,omitempty"`
	Answer           *decisionmodel.Answer `json:"answer,omitempty"`
	Resolver         string                `json:"resolver,omitempty"`
	ResolvedAt       *time.Time            `json:"resolved_at,omitempty"`
	Deadline         time.Time             `json:"deadline,omitempty"`
	Seq              int64                 `json:"seq"`
	At               time.Time             `json:"at"`
}

// kernelJudgmentRule describes one judgment-shaped refusal: the operator
// question, the guidance a refusal returns to the proposer, and the two
// candidates. Deterministic refusals never appear here.
type kernelJudgmentRule struct {
	question string
	refusal  string
	apply    Option
	refuse   Option
}

var kernelJudgmentRules = map[string]kernelJudgmentRule{
	// The motivating case: review and context natures may propose follow-up
	// work but, by default, cannot yield on it. The ceiling is a heuristic
	// about work shape, not a safety invariant, so a bounded decision may
	// permit a deliberate yield.
	"proposal_blocking_not_permitted": {
		question: "Permit this attempt to yield on its blocking proposal?",
		refusal:  " Resubmit the result with blocking=false to record the follow-up work, or finish without proposals.",
		apply:    Option{ID: "apply", Label: "Permit the yield", Consequence: "Record the held submission; the attempt yields and resumes with the reviewed results."},
		refuse:   Option{ID: "refuse", Label: "Keep the ceiling", Consequence: "Refuse the held submission and return the reason to the worker as feedback.", Recommended: true},
	},
}

func judgmentRefusal(code, detail string) error {
	rule, ok := kernelJudgmentRules[code]
	if !ok {
		return &HandoffFailure{Code: code, Detail: detail}
	}
	return &HandoffFailure{Code: code, Detail: detail + rule.refusal}
}

var errJudgmentAllowance = errors.New("judgment allowance exhausted")

// judgeSubmission routes one judgment trigger to its decision maker. It never
// relaxes the rule by itself: without a usable decision the conservative
// refusal semantics apply.
func (e *Engine) judgeSubmission(run string, in ResultSubmission, req *judgmentRequired) (any, error) {
	rule, ok := kernelJudgmentRules[req.code]
	if !ok {
		return nil, handoffError(req.code, 0)
	}
	s := e.Store
	r, err := s.Get(run)
	if err != nil || r.GraphAttempt == nil {
		return nil, judgmentRefusal(req.code, req.detail)
	}
	g, err := s.Graph(r.GraphAttempt.GraphID)
	if err != nil {
		return nil, judgmentRefusal(req.code, req.detail)
	}
	if cfg, revision, _, err := e.workflowDecisionConfig(g); err == nil {
		j, err := s.beginApplicationJudgment(run, in, req, rule, cfg, revision)
		if err != nil {
			if errors.Is(err, errJudgmentAllowance) {
				// The graph's decision allowance is spent; failing closed keeps
				// a flailing proposer from flooding the operator.
				return nil, judgmentRefusal(req.code, req.detail)
			}
			return nil, err
		}
		ctx, cancel := context.WithDeadline(context.Background(), j.Deadline)
		answer, callErr := e.Decide(ctx, *j.Config, j.Request)
		cancel()
		j, outcome, _, err := s.finishApplicationJudgment(run, j.ID, answer, callErr)
		if err != nil {
			return nil, err
		}
		switch outcome {
		case "apply":
			receipt, err := s.submitJudgedResult(run, in, &j)
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": receipt.ID, "run_id": receipt.RunID, "revision": receipt.Revision, "seq": receipt.Seq, "at": receipt.At, "digest": receipt.Digest, "proposal_ids": receipt.ProposalIDs, "judgment": map[string]any{"id": j.ID, "state": "applied", "resolver": j.Resolver}}, nil
		case "refuse":
			return nil, judgmentRefusal(req.code, req.detail)
		}
		// The model could not decide; escalate with its receipt to the operator.
		held, err := s.holdForOperatorJudgment(run, in, req, rule, &j)
		if err != nil {
			return nil, err
		}
		return map[string]any{"held": "kernel_judgment", "judgment": held.ID, "code": held.Code, "detail": held.Detail, "note": "The submission is held for a judgment, not refused. Stop after submitting; this attempt continues with the judgment outcome."}, nil
	}
	j, err := s.holdForOperatorJudgment(run, in, req, rule, nil)
	if err != nil {
		if errors.Is(err, errJudgmentAllowance) {
			// The decision allowance is spent; failing closed keeps a
			// flailing proposer from flooding the operator with cards.
			return nil, judgmentRefusal(req.code, req.detail)
		}
		return nil, err
	}
	return map[string]any{"held": "kernel_judgment", "judgment": j.ID, "code": j.Code, "detail": j.Detail, "note": "The submission is held for a judgment, not refused. Stop after submitting; this attempt continues with the judgment outcome."}, nil
}

// judgmentCard builds the operator card for a pending judgment. External
// reversibility: no default, no timeout – silence never decides.
func judgmentCard(r *Run, rule kernelJudgmentRule, req *judgmentRequired, digest string, seq int64) *HumanDecision {
	evidence := []string{}
	for _, a := range r.Artifacts[max(0, len(r.Artifacts)-6):] {
		evidence = append(evidence, a.Name)
	}
	key, _ := json.Marshal(struct {
		Run, Code, Digest string
	}{r.ID, req.code, digest})
	hash := sha256.Sum256(key)
	blocked := "A judgment-shaped kernel rule holds this result: " + req.detail + " The submission is held, not refused; nothing proceeds until the judgment settles."
	return &HumanDecision{
		ID: newID("decision"), RunID: r.ID, CausedBy: seq, Tenant: runTenant(r), Kind: "choose",
		ReasonSignal: "kernel_judgment", InterruptClass: "judgment", Reversibility: "external",
		Question: rule.question,
		Context:  decision.Context{Attempted: strings.TrimSpace(r.Brief.Title), BlockedBy: blocked, EvidenceRefs: evidence},
		Options:  []Option{rule.apply, rule.refuse},
		CostOfWrong: decision.Cost{WastedWallMS: int64(r.Brief.TimeoutMinutes) * 60000,
			Detail: "Permitting the yield resumes a review-shaped worker with its proposed results; refusing returns the worker to a non-blocking resubmission."},
		BlastRadius: decision.BlastRadius{Paths: []string{r.Brief.Workspace}, Destinations: []string{}},
		RequestedAt: time.Now().UTC(), DedupeKey: hex.EncodeToString(hash[:]),
	}
}

// holdForOperatorJudgment holds the submission and parks the attempt with an
// operator card. A fresh hold charges the graph's decision allowance in the
// same commit, so judgment cards cannot outlive the operator-flood bound the
// spec places on them. It also serves as the fallback when a decision
// application cannot decide: the pending record switches to operator mode in
// place without charging again.
func (s *Store) holdForOperatorJudgment(id string, in ResultSubmission, req *judgmentRequired, rule kernelJudgmentRule, escalated *KernelJudgment) (KernelJudgment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return KernelJudgment{}, ErrNotFound
	}
	if r.Terminal() || r.Status == "stopping" {
		return KernelJudgment{}, judgmentRefusal(req.code, req.detail)
	}
	if r.Decision != nil && r.Decision.Choice == "" {
		return KernelJudgment{}, ErrConflict
	}
	digest := graphDigest(in)
	j := KernelJudgment{ID: newID("judgment"), Code: req.code, Detail: req.detail, Question: rule.question, Digest: digest, State: "pending", Seq: s.seq + 1, At: time.Now().UTC()}
	if r.GraphAttempt != nil {
		j.GraphID = r.GraphAttempt.GraphID
	}
	fresh := r.HeldJudgment == nil
	if held := r.HeldJudgment; held != nil {
		if held.State != "pending" || held.Digest != digest {
			return KernelJudgment{}, ErrConflict
		}
		j = *held // keep the existing receipt; only the decision maker changes
		if escalated != nil && escalated.ID == held.ID {
			j.Reason, j.Answer = escalated.Reason, escalated.Answer
		}
	}
	if r.Result != nil {
		return KernelJudgment{}, ErrConflict
	}
	j.Proposals = len(in.Proposals)
	var gc *graphChange
	if fresh {
		// A fresh operator judgment consumes the decision allowance; an
		// escalation already charged it as an application judgment.
		charged, err := s.chargeJudgmentGraphLocked(r, j.GraphID, "judgment_held")
		if err != nil {
			return KernelJudgment{}, err
		}
		gc = charged
	}
	card := judgmentCard(r, rule, req, digest, s.seq+1)
	j.Mode, j.DecisionID, j.State = "operator", card.ID, "pending"
	j.Submission = &in
	if err := card.Validate(false); err != nil {
		return KernelJudgment{}, err
	}
	if err := s.commit(id, "effect", "judgment_held", change{Judgment: &j, Decision: card, Status: "parked", Stage: "Waiting for a judgment", Summary: card.Context.BlockedBy, Graph: gc}); err != nil {
		return KernelJudgment{}, err
	}
	return j, nil
}

// chargeJudgmentGraphLocked consumes one decision allowance slot and returns
// the graph change that also carries the attempt's park projection (the change
// supplies its own Graph, so graphRunChange will not run for it). Caller holds
// the store lock.
func (s *Store) chargeJudgmentGraphLocked(r *Run, graphID, kind string) (*graphChange, error) {
	old := s.graphs[graphID]
	if old == nil || old.Status != "active" || old.OrchestratorCalls >= old.Limits.OrchestratorCalls {
		return nil, errJudgmentAllowance
	}
	now := time.Now().UTC()
	seq := s.seq + 1
	g := graphClone(*old)
	g.OrchestratorCalls++
	g.Revision++
	g.UpdatedAt = now
	if m := g.Members[r.TaskID]; m != nil && m.CurrentRunID == r.ID {
		m.FactRevision++
		m.State = "parked"
		if g.Policy.Mode != "defined" && m.OrchestratorFor == "" {
			g.Cursor = seq
			g.Triggers = append(g.Triggers, GraphTrigger{Seq: seq, TaskID: r.TaskID, RunID: r.ID, Kind: kind})
			if root := g.Members[g.RootTask]; root != nil && root.Sealed {
				root.Sealed = false
				g.Revision++
			}
		}
	}
	gc := &graphChange{Graph: g}
	s.projectGraph(gc, old)
	return gc, nil
}

// beginApplicationJudgment records the trigger and charges the graph's
// decision allowance before any inference. The submission stays on the
// attempt; the run is not parked while the model call is in flight.
func (s *Store) beginApplicationJudgment(id string, in ResultSubmission, req *judgmentRequired, rule kernelJudgmentRule, cfg decisionmodel.Config, revision int) (KernelJudgment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return KernelJudgment{}, ErrNotFound
	}
	if s.agentPolicy(r.ProjectID).Revision != revision || r.Status != "running" || r.HeldJudgment != nil || r.Result != nil || r.Decision != nil && r.Decision.Choice == "" {
		return KernelJudgment{}, ErrConflict
	}
	now := time.Now().UTC()
	j := KernelJudgment{ID: newID("judgment"), Code: req.code, Detail: req.detail, Question: rule.question, Digest: graphDigest(in), Mode: "decision_application", State: "pending", Config: &cfg, Proposals: len(in.Proposals), At: now, Submission: &in}
	if r.GraphAttempt != nil {
		j.GraphID = r.GraphAttempt.GraphID
	}
	old := s.graphs[j.GraphID]
	if old == nil || old.Status != "active" || old.OrchestratorCalls >= old.Limits.OrchestratorCalls {
		return KernelJudgment{}, errJudgmentAllowance
	}
	if root := old.Members[old.RootTask]; root == nil || root.Sealed {
		return KernelJudgment{}, errJudgmentAllowance
	}
	// The accounting bump below lands in the same commit, so the revision this
	// judgment must still see is old.Revision + 1.
	j.GraphRevision = old.Revision + 1
	j.ResolverRevision = &revision
	j.Seq = s.seq + 1
	j.Deadline = minTime(now.Add(time.Duration(cfg.TimeoutMS)*time.Millisecond), old.Limits.Deadline)
	if r.Execution != nil {
		j.Deadline = minTime(j.Deadline, r.Execution.Deadline)
	}
	state, err := json.Marshal(map[string]any{
		"graph_id": old.ID, "task_id": r.TaskID, "run_id": r.ID, "code": req.code,
		"nature": judgmentNature(r, old), "summary": in.Summary, "rule_detail": req.detail,
		"proposals": judgmentProposalView(in), "deadline": j.Deadline,
	})
	if err != nil || len(state) > kernelJudgmentContext {
		return KernelJudgment{}, graphError("judgment projection exceeds its bound")
	}
	j.Request = decisionmodel.Request{ID: newID("kernel_judgment"), State: state, Choices: []decisionmodel.Choice{
		{Value: "apply", Description: "Permit the held intent: " + rule.apply.Consequence},
		{Value: "refuse", Description: "Keep the conservative default: " + rule.refuse.Consequence},
	}}
	g := graphClone(*old)
	g.OrchestratorCalls++
	g.Revision++
	g.UpdatedAt = now
	if err := s.commit(id, "decision", "kernel_judgment_requested", change{Judgment: &j, Graph: &graphChange{Graph: g}}); err != nil {
		return KernelJudgment{}, err
	}
	return j, nil
}

func judgmentNature(r *Run, g *WorkGraph) string {
	if m := g.Members[r.TaskID]; m != nil && m.Spec.Traits != nil {
		return m.Spec.Traits.Nature
	}
	return ""
}

// judgmentProposalView projects only bounded, decision-relevant proposal facts.
func judgmentProposalView(in ResultSubmission) []map[string]any {
	out := []map[string]any{}
	for _, p := range in.Proposals {
		titles := []string{}
		for _, t := range p.Tasks {
			titles = append(titles, t.Brief.Title)
		}
		rationale := p.Rationale
		if len(rationale) > 400 {
			rationale = rationale[:400]
		}
		out = append(out, map[string]any{"id": p.ID, "blocking": p.Blocking, "rationale": rationale, "tasks": titles})
	}
	return out
}

// finishApplicationJudgment records the model receipt and returns the outcome:
// apply, refuse, or escalate when the application could not decide.
func (s *Store) finishApplicationJudgment(id, judgmentID string, answer decisionmodel.Answer, callErr error) (KernelJudgment, string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return KernelJudgment{}, "", "", ErrNotFound
	}
	held := r.HeldJudgment
	if held == nil || held.ID != judgmentID || held.State != "pending" || held.Mode != "decision_application" {
		return KernelJudgment{}, "", "", ErrConflict
	}
	j := *held
	g := s.graphs[j.GraphID]
	outcome, reason := "escalate", ""
	switch {
	case j.ResolverRevision != nil && *j.ResolverRevision != s.agentPolicy(r.ProjectID).Revision || r.Status != "running" || g == nil || g.Status != "active" || g.Revision != j.GraphRevision:
		outcome, reason = "refuse", "state_changed_before_decision"
	case !time.Now().Before(j.Deadline):
		outcome, reason = "refuse", "deadline_expired"
	case callErr != nil:
		reason = "inference_failed"
	case answer.Validate(j.Request) != nil:
		reason = "invalid_answer"
	case !answer.Meets(*j.Config):
		reason = "confidence_threshold"
		j.Answer = &answer
	default:
		j.Answer = &answer
		outcome = answer.Choice
	}
	if outcome == "escalate" {
		// The receipt stays pending with its reason and any validated answer;
		// the caller parks it for the operator.
		j.Reason = reason
		return j, outcome, reason, nil
	}
	now := time.Now().UTC()
	j.State, j.Reason, j.Resolver, j.ResolvedAt, j.Submission = outcomeToState(outcome), reason, "decision_application", &now, nil
	if err := s.commit(id, "effect", "kernel_judgment_"+outcome, change{Judgment: &j}); err != nil {
		return KernelJudgment{}, "", "", err
	}
	return j, outcome, reason, nil
}

func outcomeToState(outcome string) string {
	if outcome == "apply" {
		return "applied"
	}
	return "refused"
}

// settleJudgmentChoice applies an operator resolution to a held judgment.
// The caller validated the card through the canonical decision path and holds
// the store lock.
func (s *Store) settleJudgmentChoice(id, decisionID, choice string, resolution decision.Resolution, command *DeviceCommandRecord) error {
	r := s.runs[id]
	j := r.HeldJudgment
	if j == nil || j.State != "pending" || j.DecisionID != decisionID || j.Submission == nil {
		return ErrConflict
	}
	if resolution.Note != "" {
		j.Reason = resolution.Note
	}
	now := time.Now().UTC()
	j.Resolver, j.ResolvedAt = resolution.Resolver, &now
	if choice == "apply" {
		return s.applyHeldJudgmentLocked(r, *j, resolution, command, now)
	}
	if choice != "refuse" {
		return ErrConflict
	}
	return s.refuseHeldJudgmentLocked(r, *j, resolution, command, now)
}

// applyHeldJudgmentLocked records the held submission as the attempt's result.
// The run stays parked when its execution is still open; Complete finishes it,
// which records the yield exactly as an ungated blocking submission would.
func (s *Store) applyHeldJudgmentLocked(r *Run, j KernelJudgment, resolution decision.Resolution, command *DeviceCommandRecord, now time.Time) error {
	old := s.graphs[j.GraphID]
	if old == nil || old.Status != "active" {
		return s.refuseHeldJudgmentLocked(r, withRefusalReason(j, "work is no longer active"), resolution, command, now)
	}
	m := old.Members[r.TaskID]
	if m == nil || m.CurrentRunID != r.ID || m.CancelRequested || m.Replacement != "" {
		return s.refuseHeldJudgmentLocked(r, withRefusalReason(j, "task changed while the judgment was pending"), resolution, command, now)
	}
	if s.cooperationBlocks(r.ID) {
		return errors.New("cooperation_blocked: answer the unresolved request before applying this judgment")
	}
	in := *j.Submission
	refs := map[string]bool{}
	for _, ref := range in.Artifacts {
		if !validArtifactRole(ref.Role) || refs[ref.ArtifactID] {
			return s.refuseHeldJudgmentLocked(r, withRefusalReason(j, "held submission no longer validates"), resolution, command, now)
		}
		refs[ref.ArtifactID] = true
	}
	// Re-verify against current facts: prevalidation at hold time is not a lock.
	if err := s.verifySubmissionEvidence(r, in, refs); err != nil {
		return s.refuseHeldJudgmentLocked(r, withRefusalReason(j, "held submission no longer validates"), resolution, command, now)
	}
	if err := s.validateProposals(r, in.Proposals, true); err != nil {
		return s.refuseHeldJudgmentLocked(r, withRefusalReason(j, "graph state changed while the judgment was pending"), resolution, command, now)
	}
	receipt, err := s.buildResultReceipt(r, in, now)
	if err != nil {
		return err
	}
	applied := j
	applied.State, applied.Submission = "applied", nil
	if err := s.commit(r.ID, "effect", "judgment_applied", change{Judgment: &applied, Result: &receipt, Resolution: &resolution, Choice: resolution.Choice, DecisionApplied: true, ResultRunID: r.ID, DeviceCommand: command, Stage: "Judgment applied", Summary: "The judgment permitted the held submission; recording the result."}); err != nil {
		return err
	}
	if r.Claim != nil && r.Claim.ReleasedAt == nil {
		return nil // the open execution completes the run through Complete
	}
	return s.finishHeldRunLocked(r.ID, in.Summary)
}

// refuseHeldJudgmentLocked journals the refused judgment and its card
// resolution. The refusal semantics - a cancelled attempt and a continuation
// that returns the reason to the proposer - are finalized by
// finalizeRefusedJudgmentLocked once the worker has finished harvesting: an
// open execution must stay nonterminal so its output and workspace can be
// saved, and the replacement attempt needs a harvested, claim-released
// predecessor. The card records the operator's choice; an apply that could no
// longer proceed still refuses, with the reason on the judgment receipt.
func (s *Store) refuseHeldJudgmentLocked(r *Run, j KernelJudgment, resolution decision.Resolution, command *DeviceCommandRecord, now time.Time) error {
	refused := j
	refused.State, refused.Submission = "refused", nil
	if refused.Reason == "" && resolution.Choice != "apply" {
		refused.Reason = "operator_refused"
	}
	if err := s.commit(r.ID, "effect", "judgment_refused", change{Judgment: &refused, Resolution: &resolution, Choice: resolution.Choice, DecisionApplied: true, ResultRunID: r.ID, DeviceCommand: command, Stage: "Judgment refused", Summary: j.Detail}); err != nil {
		return err
	}
	if r.Claim != nil && r.Claim.ReleasedAt == nil {
		return nil // execution still open; Complete or reconcile finalizes
	}
	return s.finalizeRefusedJudgmentLocked(r.ID, now)
}

// refusedJudgmentPending reports a refusal whose decision is journaled but
// whose attempt has not yet taken the cancelled-and-requeued outcome.
func refusedJudgmentPending(r *Run) *KernelJudgment {
	if r.Result != nil || r.Decision == nil || r.Decision.Resolution == nil || r.Status != "parked" {
		return nil
	}
	for i := len(r.KernelJudgments) - 1; i >= 0; i-- {
		if r.KernelJudgments[i].DecisionID == r.Decision.ID && r.KernelJudgments[i].State == "refused" {
			return &r.KernelJudgments[i]
		}
	}
	return nil
}

// refusalGraphChangeLocked requeues the member for a continuation that
// returns the refusal to the proposer, and spends the refused proposals
// against the task budget. Caller holds the store lock.
func (s *Store) refusalGraphChangeLocked(r *Run, j *KernelJudgment, now time.Time) *graphChange {
	old := s.graphs[j.GraphID]
	if old == nil {
		return nil
	}
	g := graphClone(*old)
	m := g.Members[r.TaskID]
	if m == nil || m.CurrentRunID != r.ID {
		return nil // the task moved on; nothing to requeue
	}
	m.Proposed += j.Proposals // the refused proposals are spent
	m.ContinueFrom = r.ID
	m.Continuation = &GraphContinuation{Reason: "judgment_refused", Refusal: &JudgmentRefusal{Code: j.Code, Detail: j.Detail}}
	m.CurrentRunID = ""
	m.State = "queued"
	m.FactRevision++
	if root := g.Members[g.RootTask]; root != nil && root.Sealed {
		root.Sealed = false
	}
	g.Triggers = append(g.Triggers, GraphTrigger{Seq: s.seq + 1, TaskID: r.TaskID, RunID: r.ID, Kind: "judgment_refused"})
	g.Cursor = s.seq + 1
	g.Revision++
	g.UpdatedAt = now
	gc := &graphChange{Graph: g}
	s.projectGraph(gc, old)
	return gc
}

// finalizeRefusedJudgmentLocked applies the refusal outcome: the attempt ends
// cancelled with its evidence preserved, its claim is released, and the member
// is requeued for the feedback continuation. Caller holds the store lock.
func (s *Store) finalizeRefusedJudgmentLocked(id string, now time.Time) error {
	r := s.runs[id]
	j := refusedJudgmentPending(r)
	if j == nil {
		return nil
	}
	var claim *fleet.Claim
	if r.Claim != nil && r.Claim.ReleasedAt == nil {
		copyClaim := *r.Claim
		copyClaim.ReleasedAt = &now
		copyClaim.ReleaseReason = "execution_stopped_and_harvest_finished"
		claim = &copyClaim
	}
	return s.commit(id, "effect", "judgment_refusal_finalized", change{Graph: s.refusalGraphChangeLocked(r, j, now), Claim: claim, Status: "cancelled", Stage: "Judgment refused", Summary: j.Detail})
}

func withRefusalReason(j KernelJudgment, reason string) KernelJudgment {
	j.Reason = reason
	return j
}

// finishHeldRunLocked completes a parked run whose judgment applied after its
// execution ended. The normal run_finished projection records the yield.
func (s *Store) finishHeldRunLocked(id, summary string) error {
	r := s.runs[id]
	if r == nil || r.Status != "parked" || r.Result == nil {
		return nil
	}
	var claim *fleet.Claim
	if r.Claim != nil && r.Claim.ReleasedAt == nil {
		copyClaim := *r.Claim
		at := time.Now().UTC()
		copyClaim.ReleasedAt = &at
		copyClaim.ReleaseReason = "execution_stopped_and_harvest_finished"
		claim = &copyClaim
	}
	if len(summary) > 2000 {
		summary = summary[:2000]
	}
	return s.commit(id, "effect", "run_finished", change{Claim: claim, Status: "done", Stage: "Complete", Summary: summary})
}

// recoverKernelJudgments settles judgments interrupted mid-flight. A pending
// decision-application judgment whose attempt is no longer running was
// interrupted with unknown consumption, and a pending operator judgment whose
// attempt already ended can never be applied: the conservative default
// applies to both.
func (s *Store) recoverKernelJudgments() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		j := r.HeldJudgment
		if j == nil || j.State != "pending" {
			continue
		}
		interrupted := j.Mode == "decision_application" && r.Status != "running"
		ended := j.Mode == "operator" && r.Terminal()
		if !interrupted && !ended {
			continue
		}
		refused := withRefusalReason(*j, "interrupted_unknown_consumption")
		if ended {
			refused = withRefusalReason(*j, "attempt_ended_before_decision")
		}
		refused.State, refused.Submission = "refused", nil
		if err := s.commit(r.ID, "effect", "kernel_judgment_interrupted", change{Judgment: &refused}); err != nil {
			return fmt.Errorf("recover kernel judgment: %w", err)
		}
	}
	return nil
}

// reconcileHeldJudgments finishes parked runs whose judgment settled after
// their execution had already stopped: an applied judgment completes the run
// so an accepted yield cannot wedge, and a refused judgment whose execution
// claim is released (for example after a crash or claim release) finally
// takes its cancelled-and-requeued outcome.
func (s *Store) reconcileHeldJudgments() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if r.Status != "parked" {
			continue
		}
		if r.Result != nil && r.Decision != nil && r.Decision.Choice == "apply" {
			if r.Claim == nil || r.Claim.ReleasedAt != nil {
				_ = s.finishHeldRunLocked(r.ID, r.Result.Submission.Summary)
			}
			continue
		}
		if refusedJudgmentPending(r) != nil && (r.Claim == nil || r.Claim.ReleasedAt != nil) {
			_ = s.finalizeRefusedJudgmentLocked(r.ID, time.Now().UTC())
		}
	}
}
