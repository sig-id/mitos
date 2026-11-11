package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"mitos/internal/observation"
	"time"

	"mitos/internal/acceptance"
	"mitos/internal/autonomy"
	"mitos/internal/compute"
)

const BatchRuntimeVersion = "batch-segments/v1"

type ExecutionPlan struct {
	Runtime            string                 `json:"runtime,omitempty"`
	Horizon            *autonomy.HorizonGrant `json:"horizon,omitempty"`
	Launch             *compute.LaunchSpec    `json:"launch,omitempty"`
	Application        Application            `json:"application"`
	Directory          string                 `json:"application_directory"`
	InitialCommit      string                 `json:"initial_commit"`
	InitialFingerprint string                 `json:"initial_fingerprint"`
	OracleFingerprint  string                 `json:"oracle_fingerprint"`
	Deadline           time.Time              `json:"deadline"`
}

func (s *Store) applyRecovery(e Event, r *Run, c change) error {
	if c.ModelDecision != nil {
		d := *c.ModelDecision
		if d.Outcome == "pending" {
			r.ModelDecisions = append(r.ModelDecisions, d)
		} else {
			if len(r.ModelDecisions) == 0 || r.ModelDecisions[len(r.ModelDecisions)-1].Request.ID != d.Request.ID {
				return errors.New("unknown model decision")
			}
			r.ModelDecisions[len(r.ModelDecisions)-1] = d
		}
	}
	if c.Execution != nil {
		if c.Execution.Runtime != "" && c.Execution.Runtime != BatchRuntimeVersion {
			return errors.New("unsupported execution runtime")
		}
		if r.Execution != nil {
			return errors.New("execution plan changed")
		}
		r.Execution = c.Execution
		if c.Execution.Horizon != nil {
			until := c.Execution.Horizon.UnattendedUntil
			r.UnattendedUntil = &until
		}
	}
	if c.Segment != nil {
		if c.SegmentFinished {
			if len(r.Segments) == 0 || r.Segments[len(r.Segments)-1].ID != c.Segment.ID {
				return errors.New("unknown segment completion")
			}
			r.Segments[len(r.Segments)-1] = *c.Segment
		} else {
			if c.Segment.Number != len(r.Segments)+1 {
				return errors.New("invalid segment sequence")
			}
			r.Segments = append(r.Segments, *c.Segment)
			r.Acceptance = nil
			if r.Workspace != nil {
				r.Workspace.ResultCommit = ""
			}
			r.Verified = false
			r.ReadyRewind = nil
		}
	}
	if c.Checkpoint != nil {
		r.Checkpoints = append(r.Checkpoints, *c.Checkpoint)
	}
	if c.Rewind != nil {
		if c.RewindApplied {
			if r.PendingRewind == nil || r.PendingRewind.ID != c.Rewind.ID {
				return errors.New("unknown rewind completion")
			}
			for i := range r.Rewinds {
				if r.Rewinds[i].ID == c.Rewind.ID {
					r.Rewinds[i] = *c.Rewind
				}
			}
			var target *autonomy.Checkpoint
			for i := range r.Checkpoints {
				if r.Checkpoints[i].ID == c.Rewind.CheckpointID {
					target = &r.Checkpoints[i]
					break
				}
			}
			if target == nil {
				return errors.New("rewind checkpoint disappeared")
			}
			number := target.SegmentNumber
			for i := range r.Segments {
				if r.Segments[i].Number > number && (c.Rewind.Scope == "tail" || r.Segments[i].StepID == c.Rewind.StepID) {
					r.Segments[i].Invalidated = true
				}
			}
			for i := range r.Checkpoints {
				if r.Checkpoints[i].SegmentNumber > number && (c.Rewind.Scope == "tail" || r.Checkpoints[i].StepID == c.Rewind.StepID) {
					r.Checkpoints[i].Invalidated = true
				}
			}
			r.PendingRewind = nil
			r.ReadyRewind = c.Rewind
			r.Acceptance = nil
			r.Verified = false
		} else {
			r.PendingRewind = c.Rewind
			r.Rewinds = append(r.Rewinds, *c.Rewind)
		}
	}
	return nil
}

func (s *Store) ConfigureExecution(id string, plan ExecutionPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() || r.Status == "stopping" || r.Execution != nil {
		return ErrConflict
	}
	if r.Workspace == nil || r.Workspace.BaselineCommit != plan.InitialCommit || r.Workspace.Fingerprint != plan.InitialFingerprint || plan.Deadline.IsZero() {
		return errors.New("execution plan does not match the saved baseline")
	}
	if plan.Runtime != "" && plan.Runtime != BatchRuntimeVersion {
		return errors.New("unsupported execution runtime")
	}
	plan.Runtime = BatchRuntimeVersion
	if err := validateDecisionModel(plan.Application); err != nil {
		return err
	}
	// Current local observation is sampled and its verifier shares the operator's
	// UID. Neither an app declaration nor a historical metric can grant a horizon.
	structured := plan.Application.Observation != nil && plan.Application.Observation.Session != nil
	grant := autonomy.GrantHorizon(time.Now().UTC(), plan.Deadline, plan.Deadline, autonomy.Admission{StructuredObservation: structured, MachinePredicate: len(plan.Application.Acceptance.Cmd) > 0})
	plan.Horizon = &grant
	return s.commit(id, "effect", "execution_configured", change{Execution: &plan})
}

func (s *Store) StartSegment(id, inputCommit string) (autonomy.Segment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return autonomy.Segment{}, ErrNotFound
	}
	if !r.Active() || r.Status == "stopping" || r.PendingRewind != nil {
		return autonomy.Segment{}, ErrConflict
	}
	if len(r.Segments) > 0 && r.Segments[len(r.Segments)-1].FinishedAt == nil {
		return autonomy.Segment{}, ErrConflict
	}
	if len(r.Segments) > 0 && r.ReadyRewind == nil {
		return autonomy.Segment{}, errors.New("next segment requires an applied recovery decision")
	}
	if r.Execution == nil {
		return autonomy.Segment{}, errors.New("execution plan is missing")
	}
	expected := r.Execution.InitialCommit
	if r.ReadyRewind != nil {
		for _, checkpoint := range r.Checkpoints {
			if checkpoint.ID == r.ReadyRewind.CheckpointID {
				expected = checkpoint.WorkspaceCommit
			}
		}
	}
	if expected != inputCommit {
		return autonomy.Segment{}, errors.New("segment input does not match its recovery decision")
	}
	if !time.Now().Before(r.Execution.Deadline) {
		return autonomy.Segment{}, errors.New("execution deadline expired")
	}
	if cfg := r.Execution.Application.DecisionModel; cfg != nil && cfg.HasHook("before_segment") {
		d := findModelDecision(r, "before_segment", len(r.Segments)+1, inputCommit)
		if d == nil || d.Outcome != "accepted" || d.Answer == nil || d.Answer.Choice != "continue" {
			return autonomy.Segment{}, errors.New("segment requires an accepted model decision")
		}
	}
	segment := autonomy.Segment{ID: newID("segment"), StepID: "main", Number: len(r.Segments) + 1, InputCommit: inputCommit, StartedAt: time.Now().UTC()}
	if err := s.commit(id, "effect", "segment_started", change{Segment: &segment, Status: "running", Stage: "Working on the brief", Summary: fmt.Sprintf("Starting segment %d.", segment.Number)}); err != nil {
		return autonomy.Segment{}, err
	}
	return segment, nil
}
func (s *Store) FinishSegment(id, signal string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() || len(r.Segments) == 0 {
		return ErrConflict
	}
	segment := r.Segments[len(r.Segments)-1]
	if segment.FinishedAt != nil {
		return nil
	}
	at := time.Now().UTC()
	segment.FinishedAt = &at
	segment.Signal = signal
	return s.commit(id, "effect", "segment_finished", change{Segment: &segment, SegmentFinished: true})
}

func (s *Store) TakeCheckpoint(id, unknownEvidence string) (autonomy.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return autonomy.Checkpoint{}, ErrNotFound
	}
	if r.Terminal() || r.Workspace == nil || r.Workspace.ResultCommit == "" {
		return autonomy.Checkpoint{}, ErrConflict
	}
	number := 0
	segmentID := ""
	var started time.Time
	if len(r.Segments) > 0 {
		segment := r.Segments[len(r.Segments)-1]
		if segment.FinishedAt == nil {
			return autonomy.Checkpoint{}, errors.New("cannot checkpoint an open segment")
		}
		number = segment.Number
		segmentID = segment.ID
		started = segment.StartedAt
	}
	for _, checkpoint := range r.Checkpoints {
		if checkpoint.SegmentNumber == number {
			return checkpoint, nil
		}
	}
	quietSeq := int64(0)
	for i := len(r.Events) - 1; i >= 0; i-- {
		event := r.Events[i]
		if event.Kind == "quiescent" && !event.At.Before(started) {
			var observed change
			if json.Unmarshal(event.Data, &observed) != nil {
				continue
			}
			var edge observation.Edge
			if json.Unmarshal(observed.Evidence, &edge) != nil || edge.ForMS < 200 || edge.TreeSize != 0 {
				continue
			}
			quietSeq = event.Seq
			break
		}
	}
	if quietSeq == 0 {
		return autonomy.Checkpoint{}, errors.New("checkpoint requires recorded quiescence")
	}
	predicate := acceptance.Verdict{Verdict: "unknown", WorkspaceCommit: r.Workspace.ResultCommit, EvidenceRef: unknownEvidence, OracleIsolation: "same_user_process"}
	if r.Acceptance != nil && r.Acceptance.WorkspaceCommit == r.Workspace.ResultCommit {
		predicate = *r.Acceptance
	}
	found := false
	for _, artifact := range r.Artifacts {
		if artifact.Name == predicate.EvidenceRef {
			found = true
			break
		}
	}
	if !found {
		return autonomy.Checkpoint{}, errors.New("checkpoint predicate evidence is missing")
	}
	checkpoint := autonomy.Checkpoint{ID: newID("checkpoint"), RunID: id, Seq: s.seq + 1, QuietSeq: quietSeq, StepID: "main", SegmentID: segmentID, SegmentNumber: number, WorkspaceCommit: r.Workspace.ResultCommit, Fingerprint: r.Workspace.Fingerprint, SandboxSnapshot: "unsupported", Predicate: predicate, Green: predicate.Verdict == "pass"}
	err := s.commit(id, "effect", "checkpoint_taken", change{Checkpoint: &checkpoint})
	return checkpoint, err
}

func (s *Store) BeginRewind(id string, request autonomy.Rewind) (autonomy.Rewind, error) {
	if err := request.Validate(); err != nil {
		return autonomy.Rewind{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beginRewind(id, request, true)
}

func (s *Store) beginRewind(id string, request autonomy.Rewind, commit bool) (autonomy.Rewind, error) {
	r := s.runs[id]
	if r == nil {
		return autonomy.Rewind{}, ErrNotFound
	}
	if r.Terminal() || r.Status == "stopping" {
		return autonomy.Rewind{}, ErrConflict
	}
	if r.PendingRewind != nil {
		pending := r.PendingRewind
		if (request.CheckpointID == "" || pending.CheckpointID == request.CheckpointID) && pending.FromSegment == request.FromSegment && pending.Scope == request.Scope && pending.Memo == request.Memo && pending.ReasonSignal == request.ReasonSignal && pending.AllowNonGreen == request.AllowNonGreen {
			if commit && r.Status == "parked" {
				if err := s.commit(id, "effect", "rewind_resumed", change{Status: "queued", Stage: "Restoring checkpoint", Summary: "Resume the previously recorded checkpoint restoration."}); err != nil {
					return autonomy.Rewind{}, err
				}
			}
			return *pending, nil
		}
		return autonomy.Rewind{}, ErrConflict
	}
	if r.ReadyRewind != nil {
		ready := r.ReadyRewind
		if (request.CheckpointID == "" || ready.CheckpointID == request.CheckpointID) && ready.FromSegment == request.FromSegment && ready.Scope == request.Scope && ready.Memo == request.Memo && ready.ReasonSignal == request.ReasonSignal && ready.AllowNonGreen == request.AllowNonGreen {
			if commit && r.Status == "parked" {
				if err := s.commit(id, "effect", "rewind_resumed", change{Status: "queued", Stage: "Ready to retry", Summary: "Resume the previously restored checkpoint."}); err != nil {
					return autonomy.Rewind{}, err
				}
			}
			return *ready, nil
		}
		return autonomy.Rewind{}, ErrConflict
	}
	if request.FromSegment != len(r.Segments) {
		return autonomy.Rewind{}, ErrConflict
	}
	if len(r.Segments) > 0 && r.Segments[len(r.Segments)-1].FinishedAt == nil {
		return autonomy.Rewind{}, errors.New("cannot rewind a live segment")
	}
	var target *autonomy.Checkpoint
	for i := len(r.Checkpoints) - 1; i >= 0; i-- {
		checkpoint := &r.Checkpoints[i]
		if checkpoint.Invalidated {
			continue
		}
		if request.CheckpointID == "" {
			if checkpoint.Green {
				target = checkpoint
				break
			}
		} else if checkpoint.ID == request.CheckpointID {
			target = checkpoint
			break
		}
	}
	if target == nil {
		return autonomy.Rewind{}, errors.New("no matching green checkpoint is available")
	}
	if !target.Green && !request.AllowNonGreen {
		return autonomy.Rewind{}, errors.New("non-green rewind requires allow_non_green")
	}
	if r.Execution == nil {
		return autonomy.Rewind{}, errors.New("execution plan is missing")
	}
	policy := autonomy.Supervision{Strategy: "rewind_to_green"}
	if r.Execution.Application.Supervision != nil {
		policy = *r.Execution.Application.Supervision
	}
	if _, _, exhausted := autonomy.Intensity(policy, r.Rewinds, target.StepID, time.Now()); exhausted {
		return autonomy.Rewind{}, errors.New("restart intensity exhausted")
	}
	request.AppliedAt = nil
	request.ID = newID("rewind")
	request.CheckpointID = target.ID
	request.StepID = target.StepID
	request.NextSegment = len(r.Segments) + 1
	request.CausedBy = s.seq + 1
	request.RequestedAt = time.Now().UTC()
	choice := ""
	if r.Decision != nil && r.Decision.Choice == "" {
		for _, option := range r.Decision.Options {
			if option.ID == "rewind" {
				choice = "rewind"
			}
		}
	}
	if !commit {
		return request, nil
	}
	if err := s.commit(id, "decision", "rewind", change{Rewind: &request, Choice: choice, Status: "queued", Stage: "Restoring checkpoint", Summary: request.Memo}); err != nil {
		return autonomy.Rewind{}, err
	}
	return request, nil
}
func (s *Store) CompleteRewind(id, rewindID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() || r.Status == "stopping" {
		return ErrConflict
	}
	if r.ReadyRewind != nil && r.ReadyRewind.ID == rewindID {
		return nil
	}
	if r.PendingRewind == nil || r.PendingRewind.ID != rewindID {
		return ErrConflict
	}
	rewind := *r.PendingRewind
	at := time.Now().UTC()
	rewind.AppliedAt = &at
	return s.commit(id, "effect", "checkpoint_restored", change{Rewind: &rewind, RewindApplied: true, Status: "queued", Stage: "Ready to retry", Summary: "The checkpoint was restored. The abandoned segment is preserved."})
}
