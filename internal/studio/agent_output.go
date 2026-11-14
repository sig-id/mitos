package studio

import (
	"errors"
	"strings"
	"sync"
	"time"
)

const agentOutputLimit = 64 << 10

// The live preview is bounded and ephemeral. output.txt remains the saved log.
type agentOutputPreview struct {
	mu        sync.Mutex
	text      []byte
	truncated bool
	updatedAt time.Time
	activity  *agentOutputPreview
}

func (p *agentOutputPreview) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(b)
	if n > 0 {
		p.updatedAt = time.Now().UTC()
	}
	if n >= agentOutputLimit {
		p.truncated = p.truncated || len(p.text)+n > agentOutputLimit
		p.text = append(p.text[:0], b[n-agentOutputLimit:]...)
	} else {
		if drop := len(p.text) + n - agentOutputLimit; drop > 0 {
			copy(p.text, p.text[drop:])
			p.text = p.text[:len(p.text)-drop]
			p.truncated = true
		}
		p.text = append(p.text, b...)
	}
	return n, nil
}

type GraphRunOutput struct {
	// Nil denotes historical output without a separate public activity channel.
	Activity          *string    `json:"activity,omitempty"`
	ActivityTruncated bool       `json:"activity_truncated,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
	Available         bool       `json:"available"`
	Text              string     `json:"text"`
	Truncated         bool       `json:"truncated"`
	Live              bool       `json:"live"`
}

func (e *Engine) beginAgentOutput(id string) (*agentOutputPreview, func()) {
	p := &agentOutputPreview{activity: &agentOutputPreview{}}
	e.mu.Lock()
	if e.liveOutput == nil {
		e.liveOutput = map[string]*agentOutputPreview{}
	}
	e.liveOutput[id] = p
	e.mu.Unlock()
	return p, func() {
		e.mu.Lock()
		delete(e.liveOutput, id)
		e.mu.Unlock()
	}
}

func (e *Engine) GraphRunOutput(graph, task, id string) (GraphRunOutput, error) {
	run, err := e.Store.GraphRun(graph, task, id)
	if err != nil {
		return GraphRunOutput{}, err
	}
	e.mu.Lock()
	p := e.liveOutput[id]
	e.mu.Unlock()
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		at := p.updatedAt
		p.activity.mu.Lock()
		defer p.activity.mu.Unlock()
		activity := strings.ToValidUTF8(string(p.activity.text), "�")
		if p.activity.updatedAt.After(at) {
			at = p.activity.updatedAt
		}
		return GraphRunOutput{Text: strings.ToValidUTF8(string(p.text), "�"), Truncated: p.truncated, Activity: &activity, ActivityTruncated: p.activity.truncated, Live: true, Available: true, UpdatedAt: &at}, nil
	}
	data, err := e.Store.Artifact(id, "output.txt")
	if err != nil && !errors.Is(err, ErrNotFound) {
		return GraphRunOutput{}, err
	}
	truncated := len(data) > agentOutputLimit
	if truncated {
		data = data[len(data)-agentOutputLimit:]
	}
	result := GraphRunOutput{Text: strings.ToValidUTF8(string(data), "�"), Truncated: truncated, Live: run.Status == "queued" || run.Status == "running" || run.Status == "stopping", Available: err == nil}
	records, activityErr := e.Store.Artifact(id, "activity.jsonl")
	if activityErr == nil {
		result.ActivityTruncated = len(records) > agentOutputLimit
		if result.ActivityTruncated {
			records = records[len(records)-agentOutputLimit:]
		}
		activity := strings.ToValidUTF8(string(records), "�")
		result.Activity = &activity
	} else if !errors.Is(activityErr, ErrNotFound) {
		return GraphRunOutput{}, activityErr
	}
	return result, nil
}
