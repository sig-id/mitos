package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"mitos/internal/autonomy"
	"mitos/internal/observation"
)

type AutonomyReport struct {
	HorizonUnknown         int          `json:"horizon_unknown"`
	HorizonObservedP50MS   *float64     `json:"observed_decision_horizon_p50_ms"`
	HorizonLowerMS         *float64     `json:"unattended_horizon_p50_lower_ms"`
	HorizonUpperMS         *float64     `json:"unattended_horizon_p50_upper_ms"`
	TimingRunsExcluded     int          `json:"timing_runs_excluded"`
	Runs                   int          `json:"runs"`
	SampleRunsExcluded     int          `json:"sample_runs_excluded"`
	UnsupportedRuns        int          `json:"unsupported_runs"`
	Events                 int          `json:"events"`
	AcceptedChanges        int          `json:"accepted_changes"`
	ProductiveWallMS       int64        `json:"productive_wall_ms"`
	HumanDecisionsRaised   int          `json:"human_decisions_raised"`
	AutonomyRatioMS        *float64     `json:"autonomy_ratio_ms_per_decision"`
	RequiredDecisions      int          `json:"required_decisions"`
	HorizonSamples         int          `json:"horizon_samples"`
	HorizonCensored        int          `json:"horizon_censored"`
	HorizonUnattendedP50MS *float64     `json:"unattended_horizon_p50_ms"`
	DecisionResolved       int          `json:"decisions_resolved"`
	DecisionUnresolved     int          `json:"decisions_unresolved"`
	DecisionDefaulted      int          `json:"decisions_defaulted"`
	NonDefaultChoices      int          `json:"non_default_choices"`
	DecisionPrecision      *float64     `json:"decision_precision"`
	PrecisionLower         *float64     `json:"decision_precision_lower"`
	PrecisionUpper         *float64     `json:"decision_precision_upper"`
	DiscardedSegments      int          `json:"discarded_segments"`
	RewindDepth            *float64     `json:"rewind_depth_per_accepted_change"`
	Checkpoints            int          `json:"checkpoints"`
	GreenCheckpoints       int          `json:"green_checkpoints"`
	GreenRate              *float64     `json:"green_rate"`
	RunsWithoutGrant       int          `json:"runs_without_recorded_grant"`
	RunsWithZeroGrant      int          `json:"runs_with_zero_grant"`
	InvalidEvidence        int          `json:"invalid_evidence"`
	Limitations            []string     `json:"limitations"`
	PerRun                 []RunMetrics `json:"per_run"`
}
type RunMetrics struct {
	RunID             string                 `json:"run_id"`
	AcceptedChange    bool                   `json:"accepted_change"`
	ProductiveWallMS  int64                  `json:"productive_wall_ms"`
	Raised            int                    `json:"decisions_raised"`
	DiscardedSegments int                    `json:"discarded_segments"`
	FirstRequiredMS   *int64                 `json:"first_required_decision_ms"`
	ObservedThroughMS *int64                 `json:"observed_through_ms"`
	Censored          bool                   `json:"horizon_censored"`
	HorizonGrant      *autonomy.HorizonGrant `json:"horizon_grant"`
}
type metricCard struct {
	card      *HumanDecision
	at        time.Time
	choice    string
	resolved  bool
	defaulted bool
}
type interval struct{ start, end time.Time }

func ratio(n float64, d int) *float64 {
	if d == 0 {
		return nil
	}
	v := n / float64(d)
	return &v
}

// ReportAutonomy consumes event payloads rather than trusting mutable projections
// supplied by a reader. Current writers have already verified their own log;
// AuditAutonomyLogs additionally verifies every chain before calling this fold.
func ReportAutonomy(runs []Run) (AutonomyReport, error) {
	report := AutonomyReport{Limitations: []string{}, PerRun: []RunMetrics{}}
	seen := map[string]string{}
	var horizons, horizonLower, horizonUpper []float64
	var productiveIntervals []interval
	for _, source := range runs {
		if len(source.Events) == 0 || source.Events[0].Type == "legacy" {
			report.UnsupportedRuns++
			continue
		}
		lastHash := source.Events[len(source.Events)-1].Hash
		if prior, ok := seen[source.ID]; ok {
			if prior == lastHash && lastHash != "" {
				continue
			}
			return report, fmt.Errorf("divergent or incomplete duplicate run %s", source.ID)
		}
		seen[source.ID] = lastHash
		fold := &Store{runs: map[string]*Run{}}
		cards := map[string]*metricCard{}
		currentCard := ""
		var started, last time.Time
		hasStart := false
		invalid := 0
		checkpointEvidence := map[string]bool{}
		quiet := map[int64]bool{}
		segmentStarts := map[string]int64{}
		for _, event := range source.Events {
			if event.RunID != source.ID {
				return report, errors.New("mixed run events in metric input")
			}
			if err := fold.apply(event); err != nil {
				return report, err
			}
			var c change
			if err := json.Unmarshal(event.Data, &c); err != nil {
				return report, err
			}
			if event.Kind == "quiescent" && event.Type == "effect" {
				var edge observation.Edge
				if json.Unmarshal(c.Evidence, &edge) == nil && edge.ForMS >= 200 && edge.TreeSize == 0 {
					quiet[event.Seq] = true
				}
			}
			if c.Segment != nil && !c.SegmentFinished {
				segmentStarts[c.Segment.ID] = event.Seq
			}
			if cp := c.Checkpoint; cp != nil {
				registered := false
				for _, artifact := range fold.runs[source.ID].Artifacts {
					if artifact.Name == cp.Predicate.EvidenceRef {
						registered = true
					}
				}
				segmentBound := cp.SegmentNumber == 0 && cp.SegmentID == ""
				if cp.SegmentNumber > 0 {
					for _, segment := range fold.runs[source.ID].Segments {
						if segment.ID == cp.SegmentID && segment.Number == cp.SegmentNumber && segment.StepID == cp.StepID && segment.FinishedAt != nil && segmentStarts[segment.ID] > 0 && cp.QuietSeq > segmentStarts[segment.ID] {
							segmentBound = true
						}
					}
				}
				checkpointEvidence[cp.ID] = segmentBound && cp.WorkspaceCommit != "" && event.Kind == "checkpoint_taken" && event.Type == "effect" && cp.Seq == event.Seq && cp.RunID == source.ID && quiet[cp.QuietSeq] && registered && cp.Predicate.WorkspaceCommit == cp.WorkspaceCommit
			}
			if event.At.IsZero() || (!last.IsZero() && event.At.Before(last)) {
				invalid++
			}
			if event.At.After(last) {
				last = event.At
			}
			if started.IsZero() && event.Kind == "run_started" && event.Type == "effect" {
				started = event.At
				hasStart = true
			}
			candidate := c.Decision
			if event.Kind == "request_approval" && event.Type == "decision" {
				candidate = c.DecisionRequest
			}
			if candidate != nil && (event.Kind == "approval_requested" || event.Kind == "request_approval") {
				currentCard = candidate.ID
				if cards[currentCard] == nil {
					copy := *candidate
					cards[currentCard] = &metricCard{card: &copy, at: event.At}
				}
			}
			if event.Kind == "approval_resolved" || event.Kind == "decision_resolved" || event.Kind == "decision_defaulted" || (event.Kind == "rewind" && event.Type == "decision") {
				id := currentCard
				choice := c.Choice
				defaulted := event.Kind == "decision_defaulted"
				if c.Resolution != nil {
					id = c.Resolution.DecisionID
					choice = c.Resolution.Choice
					defaulted = defaulted || c.Resolution.Via == "default_expired" || c.Resolution.Via == ":default_expired"
				}
				if card := cards[id]; card != nil && !card.resolved && choice != "" {
					if !card.card.HasOption(choice) || (defaulted && (card.card.Default == nil || *card.card.Default != choice || card.card.Reversibility == "external")) {
						invalid++
						continue
					}
					card.resolved = true
					card.choice = choice
					card.defaulted = defaulted
				}
			}
		}
		run := fold.runs[source.ID]
		if run == nil {
			return report, ErrNotFound
		}
		if run.Sample {
			report.SampleRunsExcluded++
			continue
		}
		report.Runs++
		report.Events += len(source.Events)
		report.InvalidEvidence += invalid

		result := RunMetrics{RunID: run.ID}
		if run.Execution != nil {
			result.HorizonGrant = run.Execution.Horizon
		}
		if result.HorizonGrant == nil {
			report.RunsWithoutGrant++
		} else if result.HorizonGrant.DurationMS == 0 {
			report.RunsWithZeroGrant++
		}
		validGreen := map[string]bool{}
		validCheckpoint := map[string]bool{}
		for _, checkpoint := range run.Checkpoints {
			report.Checkpoints++
			valid := checkpointEvidence[checkpoint.ID] && checkpoint.Green && checkpoint.Predicate.Verdict == "pass" && checkpoint.Predicate.WorkspaceCommit == checkpoint.WorkspaceCommit && checkpoint.Predicate.EvidenceRef != ""
			if valid {
				report.GreenCheckpoints++
			} else if checkpoint.Green {
				report.InvalidEvidence++
			}
			if valid && !checkpoint.Invalidated {
				validGreen[checkpoint.SegmentID] = true
				validCheckpoint[checkpoint.ID] = true
			}
		}
		if run.Status == "done" && run.Verified && run.Acceptance != nil && run.Acceptance.Verdict == "pass" && run.Workspace != nil && run.Workspace.ChangedFiles > 0 && run.Acceptance.WorkspaceCommit == run.Workspace.ResultCommit {
			for _, checkpoint := range run.Checkpoints {
				if !checkpoint.Invalidated && validCheckpoint[checkpoint.ID] && checkpoint.WorkspaceCommit == run.Workspace.ResultCommit {
					result.AcceptedChange = true
					break
				}
			}
		}
		var productive []interval
		for _, segment := range run.Segments {
			if segment.Invalidated {
				result.DiscardedSegments++
			}
			if result.AcceptedChange && !segment.Invalidated && validGreen[segment.ID] && segment.FinishedAt != nil {
				if segment.StartedAt.IsZero() || segment.FinishedAt.Before(segment.StartedAt) {
					report.InvalidEvidence++
					invalid++
					continue
				}
				productive = append(productive, interval{segment.StartedAt, *segment.FinishedAt})
			}
		}
		if invalid > 0 {
			report.TimingRunsExcluded++
		}
		if hasStart && invalid > 0 {
			report.HorizonUnknown++
			horizonLower = append(horizonLower, 0)
			horizonUpper = append(horizonUpper, math.Inf(1))
		}
		if invalid == 0 {
			result.ProductiveWallMS = unionMS(productive)
			productiveIntervals = append(productiveIntervals, productive...)
		}
		if result.AcceptedChange {
			report.AcceptedChanges++
		}
		report.DiscardedSegments += result.DiscardedSegments

		var firstRequired time.Time
		for _, card := range cards {
			result.Raised++
			report.HumanDecisionsRaised++
			if card.card.Default == nil {
				report.RequiredDecisions++
				if firstRequired.IsZero() || card.at.Before(firstRequired) {
					firstRequired = card.at
				}
			}
			if !card.resolved {
				report.DecisionUnresolved++
				continue
			}
			report.DecisionResolved++
			if card.defaulted {
				report.DecisionDefaulted++
			}
			if card.card.Default == nil || card.choice != *card.card.Default {
				report.NonDefaultChoices++
			}
		}
		if !started.IsZero() && invalid == 0 {
			observedUntil := last
			if !run.FinishedAt.IsZero() && run.FinishedAt.Before(observedUntil) {
				observedUntil = run.FinishedAt
			}
			observed := observedUntil.Sub(started).Milliseconds()
			if observed >= 0 {
				result.ObservedThroughMS = &observed
			}
			if !firstRequired.IsZero() && !firstRequired.Before(started) {
				duration := firstRequired.Sub(started).Milliseconds()
				result.FirstRequiredMS = &duration
				horizons = append(horizons, float64(duration))
				horizonLower = append(horizonLower, float64(duration))
				horizonUpper = append(horizonUpper, float64(duration))
				report.HorizonSamples++
			} else if firstRequired.IsZero() {
				result.Censored = true
				report.HorizonCensored++
				horizonLower = append(horizonLower, float64(observed))
				horizonUpper = append(horizonUpper, math.Inf(1))
			}
		}
		report.PerRun = append(report.PerRun, result)
	}
	sort.Slice(report.PerRun, func(i, j int) bool { return report.PerRun[i].RunID < report.PerRun[j].RunID })
	report.ProductiveWallMS = unionMS(productiveIntervals)
	if report.TimingRunsExcluded == 0 {
		report.AutonomyRatioMS = ratio(float64(report.ProductiveWallMS), report.HumanDecisionsRaised)
	}
	report.RewindDepth = ratio(float64(report.DiscardedSegments), report.AcceptedChanges)
	report.GreenRate = ratio(float64(report.GreenCheckpoints), report.Checkpoints)
	report.PrecisionLower = ratio(float64(report.NonDefaultChoices), report.HumanDecisionsRaised)
	report.PrecisionUpper = ratio(float64(report.NonDefaultChoices+report.DecisionUnresolved), report.HumanDecisionsRaised)
	if report.DecisionUnresolved == 0 {
		report.DecisionPrecision = report.PrecisionLower
	}
	report.HorizonObservedP50MS = median(horizons)
	report.HorizonLowerMS = median(horizonLower)
	report.HorizonUpperMS = median(horizonUpper)
	if report.HorizonLowerMS != nil && report.HorizonUpperMS != nil && *report.HorizonLowerMS == *report.HorizonUpperMS {
		report.HorizonUnattendedP50MS = report.HorizonLowerMS
	}

	report.Limitations = append(report.Limitations, "Productive wall time is the union of completed retained green segments on runs with accepted nonempty changes; it is a verifier-based proxy, not business-value evidence.", "Horizon median bounds include decision-free runs as right-censored intervals. A point median is reported only when the bounds coincide; the observed-decision median is separate.", "Metrics never grant unattended execution. Recorded provider, observation, and oracle admission determine each run's grant.", "Local same-user predicate passes do not establish inaccessible-oracle verification or either product launch gate.")
	if report.InvalidEvidence > 0 {
		report.Limitations = append(report.Limitations, "Some timestamps or predicate/resolution evidence are inconsistent; affected wall-time measurements are excluded.")
	}
	if report.UnsupportedRuns > 0 {
		report.Limitations = append(report.Limitations, "Unsupported historical records are excluded; missing metrics are not zero measurements.")
	}
	return report, nil
}
func median(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sort.Float64s(values)
	middle := len(values) / 2
	value := values[middle]
	if len(values)%2 == 0 {
		value = (values[middle-1] + value) / 2
	}
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return nil
	}
	return &value
}
func unionMS(values []interval) int64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i].start.Before(values[j].start) })
	start, end := values[0].start, values[0].end
	var total time.Duration
	for _, value := range values[1:] {
		if value.start.After(end) {
			total += end.Sub(start)
			start, end = value.start, value.end
		} else if value.end.After(end) {
			end = value.end
		}
	}
	return (total + end.Sub(start)).Milliseconds()
}
func AuditAutonomyLogs(path string) (AutonomyReport, error) {
	runs, _, err := readAuditCorpus(path)
	if err != nil {
		return AutonomyReport{}, err
	}
	return ReportAutonomy(runs)
}
