package studio

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"mitos/internal/observation"
)

var errBlockedOnHuman = errors.New("blocked_on_human")
var errObservationLost = errors.New("observation_contract_failed")

type commandObservation struct {
	store      *Store
	runID      string
	config     observation.Config
	thresholds observation.Thresholds
	activity   *observation.LocalActivity
	clock      *observation.Clock
	tail       *observation.Tail
}

func newCommandObservation(store *Store, id string, config observation.Config) (*commandObservation, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	thresholds, err := observation.FromThinkTime(time.Duration(config.ThinkTimeP95MS) * time.Millisecond)
	if err != nil {
		return nil, err
	}
	return &commandObservation{store: store, runID: id, config: config, thresholds: thresholds}, nil
}

func (m *commandObservation) prepare(base string) error {
	var err error
	m.activity, err = observation.NewLocalActivity(filepath.Join(base, "workspace"))
	if err != nil {
		return err
	}
	if m.config.Session != nil {
		m.tail, err = observation.NewTail(filepath.Join(base, "home"), m.config.Session.Path, observation.JSONLNormalizer{})
		if err != nil {
			m.activity.Close()
			return err
		}
	}
	m.clock, err = observation.NewClock(time.Now(), m.thresholds)
	if err != nil {
		m.close()
		return err
	}
	if err = m.store.RecordEvidence(m.runID, "observation_configured", "Watching workspace writes and sampled process activity. This local adapter has no unattended grant.", m.config.Capabilities()); err != nil {
		m.close()
		return err
	}
	return nil
}
func (m *commandObservation) close() {
	if m.tail != nil {
		_ = m.tail.Close()
	}
	if m.activity != nil {
		_ = m.activity.Close()
	}
}
func (m *commandObservation) consume(record observation.SessionRecord) error {
	return m.store.RecordEvidence(m.runID, "harness_session_recorded", "Recorded structured harness evidence.", record)
}
func (m *commandObservation) fail(err error) error {
	if errors.Is(err, ErrConflict) {
		return err
	}
	if recordErr := m.store.RecordEvidence(m.runID, "observation_failed", "The adapter's observation contract failed; execution must stop.", map[string]string{"reason": err.Error()}); recordErr != nil {
		return recordErr
	}
	if recordErr := m.store.RecordDecision(m.runID, "escalate", "Stop execution because the observation source cannot be trusted and ask the operator to repair it."); recordErr != nil {
		return recordErr
	}
	return fmt.Errorf("%w: %v", errObservationLost, err)
}
func (m *commandObservation) poll(outputOffset uint64) error {
	if m.tail != nil {
		if err := m.tail.Poll(m.consume); err != nil {
			return m.fail(err)
		}
	}
	sample, err := m.activity.Sample(outputOffset)
	if err != nil {
		return m.fail(err)
	}
	// A pending Wait alone is not evidence of a live, stalled process.
	if sample.TreeSize == 0 {
		return nil
	}
	edges, err := m.clock.Observe(time.Now(), sample)
	if err != nil {
		return m.fail(err)
	}
	for _, edge := range edges {
		if err = m.store.RecordEvidence(m.runID, "quiescent", fmt.Sprintf("No observed activity for %d ms (%s threshold).", edge.ForMS, edge.Threshold), edge); err != nil {
			return err
		}
		if edge.Threshold == "block" {
			if err = m.store.RecordEvidence(m.runID, "blocked_on_human", "The command is still open and quiet. Its modal state is unknown.", edge); err != nil {
				return err
			}
			if err = m.store.RecordDecision(m.runID, "escalate", "Stop the quiet command and ask the operator how to proceed."); err != nil {
				return err
			}
			return errBlockedOnHuman
		}
	}
	return nil
}
func (m *commandObservation) finish() error {
	if m.tail == nil {
		return nil
	}
	if err := m.tail.Finish(m.consume); err != nil {
		return m.fail(err)
	}
	return nil
}

func observationDecision(err error) *HumanDecision {
	d := recoveryDecision()
	d.Question = "Restart this work or keep it stopped?"
	d.InterruptClass = "judgment"
	if errors.Is(err, errBlockedOnHuman) {
		d.ReasonSignal = "blocked_on_human"
		d.Context.BlockedBy = "The command stayed quiet beyond its configured think-time threshold. Its modal state is unknown. The process was stopped and its output and workspace were preserved."
	} else {
		d.ReasonSignal = "observation_contract_failed"
		d.Context.BlockedBy = "The declared observation source could not be read reliably. Execution stopped. Review the observation failure in the execution record and repair the adapter before restarting."
	}
	return d
}
