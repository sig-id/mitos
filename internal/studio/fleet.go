package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"sort"

	"mitos/internal/acceptance"
	"mitos/internal/fleet"
)

const fleetWindow = 128

var fleetArtifactName = regexp.MustCompile(`^(initial-|segment-[0-9]{3,}-)?(changes\.(patch|json)|dependency-inputs\.json|acceptance\.txt|measure-(baseline|result)\.txt)$`)

func fleetSummary(r *Run) fleet.Summary {
	status := r.Status
	switch status {
	case "queued", "running", "stopping", "parked", "done", "failed", "cancelled":
	default:
		status = "unknown"
	}
	s := fleet.Summary{RunID: r.ID, TaskID: r.TaskID, Pool: r.Brief.Scheduling.Pool, CapabilityRef: r.Brief.AppID, Status: status, Sample: r.Sample, Verified: r.Verified}
	if len(r.Events) > 0 {
		e := r.Events[len(r.Events)-1]
		s.Head = fleet.Head{Seq: e.Seq, Hash: e.Hash}
	}
	if r.Claim != nil {
		s.ClaimID = r.Claim.ID
		s.ClaimHeld = r.Claim.ReleasedAt == nil
	}
	if r.Status == "queued" {
		for _, c := range r.ClaimConflicts {
			s.BlockedBy = append(s.BlockedBy, c.RunID)
		}
	}
	sort.Strings(s.BlockedBy)
	s.BlockedBy = slices.Compact(s.BlockedBy)
	s.BlockerWindow = fleet.Window{Total: len(s.BlockedBy), Omitted: max(0, len(s.BlockedBy)-fleetWindow)}
	s.BlockedBy = s.BlockedBy[:min(len(s.BlockedBy), fleetWindow)]
	return s
}

func (s *Store) FleetIndex() fleet.Index {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := make([]*Run, 0, len(s.runs))
	for _, r := range s.runs {
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].ID < runs[j].ID
		}
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})
	index := fleet.Index{Schema: "mitos.fleet.index.v1", Runs: []fleet.Summary{}, Window: fleet.Window{Total: len(runs), Omitted: max(0, len(runs)-fleetWindow)}}
	for _, r := range runs[:min(len(runs), fleetWindow)] {
		index.Runs = append(index.Runs, fleetSummary(r))
	}
	return index
}

// FleetEvidence selects structured fields from one coherent replayed prefix.
// It never decodes arbitrary event Data or reads artifact bodies.
func (s *Store) FleetEvidence(id string) (fleet.Evidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return fleet.Evidence{}, ErrNotFound
	}
	e := fleet.Evidence{Schema: "mitos.fleet.evidence.v1", Run: fleetSummary(r), Segments: []fleet.Segment{}, Checkpoints: []fleet.Checkpoint{}, Controls: []fleet.Control{}, Artifacts: []fleet.ArtifactRef{}, Windows: map[string]fleet.Window{}}
	refs := map[string]fleet.ArtifactRef{}
	for _, a := range r.Artifacts {
		if fleetArtifactName.MatchString(a.Name) {
			ref := fleet.ArtifactRef{Name: a.Name, SHA256: a.SHA256, Bytes: a.Bytes}
			refs[a.Name] = ref
			e.Artifacts = append(e.Artifacts, ref)
		}
	}
	predicate := func(v acceptance.Verdict) fleet.Predicate {
		verdict := "unknown"
		if v.Verdict == "pass" || v.Verdict == "fail" {
			verdict = v.Verdict
		}
		isolation := "unknown"
		if v.OracleIsolation == "same_user_process" {
			isolation = v.OracleIsolation
		}
		p := fleet.Predicate{Verdict: verdict, WorkspaceCommit: v.WorkspaceCommit, OracleIsolation: isolation, BaselineMeasure: slices.Clone(v.BaselineMeasure), Measure: slices.Clone(v.Measure), Delta: slices.Clone(v.Delta)}
		if ref, ok := refs[v.EvidenceRef]; ok {
			p.Evidence = &ref
		}
		return p
	}
	if r.Workspace != nil {
		w := r.Workspace
		e.Workspace = &fleet.Workspace{BaselineCommit: w.BaselineCommit, ResultCommit: w.ResultCommit, Fingerprint: w.Fingerprint, ChangedFiles: w.ChangedFiles}
	}
	if r.Acceptance != nil {
		p := predicate(*r.Acceptance)
		e.Predicate = &p
	}
	for _, segment := range r.Segments[max(0, len(r.Segments)-fleetWindow):] {
		v := fleet.Segment{ID: segment.ID, Number: segment.Number, InputCommit: segment.InputCommit, StartedAt: segment.StartedAt, Invalidated: segment.Invalidated}
		if segment.FinishedAt != nil {
			at := *segment.FinishedAt
			v.FinishedAt = &at
		}
		e.Segments = append(e.Segments, v)
	}
	for _, c := range r.Checkpoints[max(0, len(r.Checkpoints)-fleetWindow):] {
		e.Checkpoints = append(e.Checkpoints, fleet.Checkpoint{ID: c.ID, Seq: c.Seq, QuietSeq: c.QuietSeq, SegmentNumber: c.SegmentNumber, WorkspaceCommit: c.WorkspaceCommit, Fingerprint: c.Fingerprint, Predicate: predicate(c.Predicate), Green: c.Green, Invalidated: c.Invalidated})
	}
	for _, event := range r.Events {
		switch event.Kind {
		case "dependency_inputs_prepared", "dependency_inputs_changed", "dependency_input_unavailable", "advance", "abort", "escalate", "cancellation_requested", "run_started", "run_finished", "run_failed", "run_cancelled", "approval_requested", "approval_resolved", "request_approval", "decision_applied", "decision_rejected", "decision_application_blocked", "segment_started", "segment_finished", "checkpoint_taken", "rewind", "checkpoint_restored", "rewind_resumed", "workspace_harvested", "predicate_evaluated", "ownership_violation", "dispatch_conflict", "claims_acquired", "claims_released", "claims_release_confirmed":
			if event.Type == "decision" || event.Type == "effect" {
				e.Controls = append(e.Controls, fleet.Control{Seq: event.Seq, Type: event.Type, Kind: event.Kind, At: event.At})
			}
		}
	}
	e.Windows["segments"] = fleet.Window{Total: len(r.Segments), Omitted: max(0, len(r.Segments)-fleetWindow)}
	e.Windows["checkpoints"] = fleet.Window{Total: len(r.Checkpoints), Omitted: max(0, len(r.Checkpoints)-fleetWindow)}
	e.Windows["controls"] = fleet.Window{Total: len(e.Controls), Omitted: max(0, len(e.Controls)-fleetWindow)}
	e.Windows["artifacts"] = fleet.Window{Total: len(e.Artifacts), Omitted: max(0, len(e.Artifacts)-fleetWindow)}
	e.Controls = e.Controls[max(0, len(e.Controls)-fleetWindow):]
	e.Artifacts = e.Artifacts[max(0, len(e.Artifacts)-fleetWindow):]
	data, err := json.Marshal(e)
	if err != nil {
		return fleet.Evidence{}, err
	}
	digest := sha256.Sum256(data)
	e.Digest = hex.EncodeToString(digest[:])
	return e, nil
}
