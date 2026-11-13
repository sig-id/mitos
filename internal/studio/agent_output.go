package studio

import (
	"errors"
	"strings"
	"sync"
)

const agentOutputLimit = 64 << 10

// The live preview is bounded and ephemeral. output.txt remains the saved log.
type agentOutputPreview struct {
	mu        sync.Mutex
	text      []byte
	truncated bool
}

func (p *agentOutputPreview) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(b)
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
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	Live      bool   `json:"live"`
}

func (e *Engine) beginAgentOutput(id string) (*agentOutputPreview, func()) {
	p := &agentOutputPreview{}
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
		return GraphRunOutput{Text: strings.ToValidUTF8(string(p.text), "�"), Truncated: p.truncated, Live: true}, nil
	}
	data, err := e.Store.Artifact(id, "output.txt")
	if err != nil && !errors.Is(err, ErrNotFound) {
		return GraphRunOutput{}, err
	}
	truncated := len(data) > agentOutputLimit
	if truncated {
		data = data[len(data)-agentOutputLimit:]
	}
	return GraphRunOutput{Text: strings.ToValidUTF8(string(data), "�"), Truncated: truncated, Live: run.Status == "queued" || run.Status == "running" || run.Status == "stopping"}, nil
}
