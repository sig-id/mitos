package studio

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// InterruptReport is a reader over recorded requests and resolutions. It never
// classifies from narration, terminal bytes, free-text reasons, or model claims.
type InterruptReport struct {
	Runs                     int               `json:"runs"`
	Events                   int               `json:"events"`
	SampleRunsExcluded       int               `json:"sample_runs_excluded"`
	LegacyEvents             int               `json:"legacy_events"`
	Requested                int               `json:"requested"`
	Defaults                 int               `json:"defaults"`
	HumanTouches             int               `json:"human_touches"`
	Unresolved               int               `json:"unresolved"`
	FirstAt                  *time.Time        `json:"first_at,omitempty"`
	LastAt                   *time.Time        `json:"last_at,omitempty"`
	ObservedDays             float64           `json:"observed_days"`
	Distribution             []InterruptBucket `json:"distribution"`
	SufficientForCalibration bool              `json:"sufficient_for_calibration"`
	Limitations              []string          `json:"limitations"`
}

type InterruptBucket struct {
	Class    string   `json:"class"`
	Requests int      `json:"requests"`
	Touches  int      `json:"touches"`
	Fraction *float64 `json:"fraction,omitempty"`
}

func ReportInterrupts(runs []Run) InterruptReport {
	report := InterruptReport{Distribution: []InterruptBucket{{Class: "approve_the_boring"}, {Class: "missing_information"}, {Class: "judgment"}, {Class: "rescue"}, {Class: "unclassified"}}, Limitations: []string{}}
	type card struct {
		class    int
		resolved bool
	}
	for _, run := range runs {
		if run.Sample {
			report.SampleRunsExcluded++
			continue
		}
		report.Runs++
		current := ""
		cards := map[string]*card{}
		for _, event := range run.Events {
			report.Events++
			if !event.At.IsZero() {
				if report.FirstAt == nil || event.At.Before(*report.FirstAt) {
					at := event.At
					report.FirstAt = &at
				}
				if report.LastAt == nil || event.At.After(*report.LastAt) {
					at := event.At
					report.LastAt = &at
				}
			}
			if event.Type != "effect" && !(event.Type == "decision" && (event.Kind == "rewind" || event.Kind == "request_approval")) {
				continue
			}
			var c change
			if json.Unmarshal(event.Data, &c) != nil {
				continue
			}
			candidate := c.Decision
			if event.Kind == "request_approval" {
				candidate = c.DecisionRequest
			}
			if candidate != nil && (event.Kind == "approval_requested" || event.Kind == "request_approval") {
				current = candidate.ID
				if _, exists := cards[current]; exists {
					continue
				}
				class := len(report.Distribution) - 1
				for i, bucket := range report.Distribution {
					if bucket.Class == candidate.InterruptClass {
						class = i
						break
					}
				}
				cards[current] = &card{class: class}
				report.Requested++
				report.Distribution[class].Requests++
			}
			if (event.Kind == "decision_resolved" || event.Kind == "approval_resolved" || event.Kind == "decision_defaulted" || event.Kind == "rewind") && (c.Choice != "" || c.Resolution != nil) {
				resolutionID := current
				if c.Resolution != nil {
					resolutionID = c.Resolution.DecisionID
				}
				if pending := cards[resolutionID]; pending != nil && !pending.resolved {
					pending.resolved = true
					if event.Kind == "decision_defaulted" || (c.Resolution != nil && (c.Resolution.Via == "default_expired" || c.Resolution.Via == ":default_expired")) {
						report.Defaults++
						continue
					}
					report.HumanTouches++
					report.Distribution[pending.class].Touches++
				}
			}
		}
		for _, card := range cards {
			if !card.resolved {
				report.Unresolved++
			}
		}
	}
	if report.FirstAt != nil && report.LastAt != nil {
		report.ObservedDays = report.LastAt.Sub(*report.FirstAt).Hours() / 24
	}
	if report.HumanTouches > 0 {
		for i := range report.Distribution {
			fraction := float64(report.Distribution[i].Touches) / float64(report.HumanTouches)
			report.Distribution[i].Fraction = &fraction
		}
	}
	if report.ObservedDays < 14 {
		report.Limitations = append(report.Limitations, "Less than two weeks of recorded sessions; coverage is insufficient for the requested baseline.")
	}
	if report.HumanTouches == 0 {
		report.Limitations = append(report.Limitations, "No recorded human resolutions; an interruption distribution cannot be estimated.")
	}
	if report.Distribution[len(report.Distribution)-1].Touches > 0 {
		report.Limitations = append(report.Limitations, "Some human touches lack explicit classifications; no class was inferred from free text.")
	}
	// Calendar coverage and labels are necessary, not evidence of a representative
	// sample. A corpus review must establish representativeness before calibration.
	report.Limitations = append(report.Limitations, "Corpus representativeness and measured harness think times require separate review.")
	return report
}

// AuditInterruptLogs reads without opening a writer or repairing a partial tail.
// Current JSONL chains are verified. Historical BEAM records provide coverage
// counts only; they are never upgraded into authenticated Go Effects.
func AuditInterruptLogs(path string) (InterruptReport, error) {
	runs, legacyEvents, err := readAuditCorpus(path)
	if err != nil {
		return InterruptReport{}, err
	}
	report := ReportInterrupts(runs)
	report.LegacyEvents = legacyEvents
	if legacyEvents > 0 {
		report.Limitations = append(report.Limitations, "Historical records have no Go hash-chain verification or supported decision-card schema; their interruption coverage and timestamp semantics are unknown. Historical timestamps are excluded.")
	}
	return report, nil
}

func readAuditCorpus(path string) ([]Run, int, error) {
	var files []string
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	if !info.IsDir() {
		files = []string{path}
	} else {
		err = filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && d.Type()&os.ModeSymlink == 0 && d.Name() == "events.jsonl" {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, 0, err
		}
	}
	if len(files) == 0 {
		return nil, 0, errors.New("no events.jsonl files found")
	}
	sort.Strings(files)
	var runs []Run
	legacyEvents := 0
	for _, file := range files {
		loaded, legacy, err := readInterruptLog(file)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", file, err)
		}
		runs = append(runs, loaded...)
		legacyEvents += legacy
	}
	return runs, legacyEvents, nil
}

func readInterruptLog(path string) ([]Run, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	runs := map[string]*Run{}
	var seq int64
	hash := ""
	format := ""
	legacy := 0
	for {
		line, err := readAuditLine(reader)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		var envelope struct {
			Seq           int64  `json:"seq"`
			Sequence      *int64 `json:"sequence"`
			TimestampMS   int64  `json:"timestamp_ms"`
			SchemaVersion int    `json:"schema_version"`
			RunID         string `json:"run_id"`
			Type          string `json:"type"`
		}
		if err = json.Unmarshal(line, &envelope); err != nil {
			return nil, 0, err
		}
		if envelope.Seq > 0 {
			if format == "legacy" {
				return nil, 0, errors.New("mixed log formats")
			}
			format = "go"
			var event Event
			if err = json.Unmarshal(line, &event); err != nil {
				return nil, 0, err
			}
			if event.Seq != seq+1 || event.Previous != hash || event.Hash != eventHash(event) {
				return nil, 0, errors.New("event chain damaged")
			}
			seq, hash = event.Seq, event.Hash
			var c change
			if err = json.Unmarshal(event.Data, &c); err != nil {
				return nil, 0, err
			}
			if c.Home != nil {
				if event.RunID != "" || event.Kind != "home_server_created" || c.Home.ID == "" {
					return nil, 0, errors.New("invalid home server record")
				}
				continue
			}
			if c.DeskState != nil {
				projection := &Store{runs: runs}
				if err = projection.applyDesk(event, c.DeskState); err != nil {
					return nil, 0, err
				}
				continue
			}
			if c.Graph != nil {
				projection := &Store{runs: runs}
				if err = projection.applyGraph(event, c.Graph); err != nil {
					return nil, 0, err
				}
				if event.RunID == "" {
					continue
				}
			}
			if c.ServiceLifecycle != nil {
				if err = validateServiceLifecycle(event, c); err != nil {
					return nil, 0, err
				}
				continue
			}
			if c.CustomEvent != nil {
				if err = validateCustomEvent(event, c); err != nil {
					return nil, 0, err
				}
				continue
			}
			if c.Services != nil {
				if err = validateServiceEvent(event, c); err != nil {
					return nil, 0, err
				}
				continue
			}
			if err = validateServiceCondition(event, c); err != nil {
				return nil, 0, err
			}
			if err = validateServiceDependencies(event, c); err != nil {
				return nil, 0, err
			}
			if err = validateServiceIngress(event, c); err != nil {
				return nil, 0, err
			}
			if err = validateServiceHealth(event, c); err != nil {
				return nil, 0, err
			}
			if err = validateContinuous(event, c); err != nil {
				return nil, 0, err
			}
			if c.ServiceContinuous != nil && event.RunID == "" {
				continue
			}
			if err = validateServiceEmission(event, c); err != nil {
				return nil, 0, err
			}
			if err = validateServiceDelivery(event, c); err != nil {
				return nil, 0, err
			}
			if c.ServiceDelivery != nil && event.RunID == "" {
				continue
			}
			if c.ServiceTick != nil {
				if c.Run != nil || c.Task != nil || c.Pool != nil || c.ServiceCall != nil {
					return nil, 0, errors.New("mixed service tick event")
				}
				if err = validateServiceTick(event, c.ServiceTick, false); err != nil {
					return nil, 0, err
				}
				continue
			}
			if err = validateServiceRetryEvent(event, c); err != nil {
				return nil, 0, err
			}
			if c.ServiceBreaker != nil {
				if err = validateServiceBreakerEffect(event, c); err != nil {
					return nil, 0, err
				}
			}
			if c.ServiceCall != nil {
				if err = validateServiceCall(event, c); err != nil {
					return nil, 0, err
				}
			}
			if c.Pool != nil {
				if err = validatePoolEvent(event, c); err != nil {
					return nil, 0, err
				}
				continue
			}
			if c.Run != nil {
				if runs[event.RunID] != nil || c.Run.ID != event.RunID {
					return nil, 0, errors.New("invalid run creation")
				}
				run := *c.Run
				run.Events = nil
				runs[event.RunID] = &run
			}
			run := runs[event.RunID]
			if run == nil {
				return nil, 0, errors.New("event refers to missing run")
			}
			run.Events = append(run.Events, event)
		} else if envelope.SchemaVersion == 1 && envelope.Sequence != nil && *envelope.Sequence >= 0 && envelope.RunID != "" && envelope.Type != "" {
			if format == "go" {
				return nil, 0, errors.New("mixed log formats")
			}
			format = "legacy"
			legacy++
			run := runs[envelope.RunID]
			if run == nil {
				run = &Run{ID: envelope.RunID}
				runs[run.ID] = run
			}
			run.Events = append(run.Events, Event{Type: "legacy", Kind: envelope.Type})
		} else {
			return nil, 0, errors.New("unsupported event schema")
		}
	}
	result := make([]Run, 0, len(runs))
	for _, run := range runs {
		result = append(result, *run)
	}
	return result, legacy, nil
}

func readAuditLine(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > 16<<20 {
			return nil, errors.New("event exceeds 16 MiB audit limit")
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err == io.EOF && len(line) > 0 {
			return nil, errors.New("incomplete final event; audit did not modify the log")
		}
		return line, err
	}
}
