package studio

import (
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestGraphRunOutputLiveAndSaved(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Policy.Mode = "defined"
	req.Tasks = append(req.Tasks, graphWorker("worker", "goal"))
	g := createGraphTest(t, e, req)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var task, run string
	for id, m := range g.Members {
		if m.Spec.Kind == "application" {
			task, run = id, m.CurrentRunID
		}
	}
	p, closePreview := e.beginAgentOutput(run)
	defer closePreview()
	if n, err := p.Write([]byte("Inspecting files\n")); n != 17 || err != nil {
		t.Fatal(n, err)
	}
	output, err := e.GraphRunOutput(g.ID, task, run)
	if err != nil || !output.Live || output.Text != "Inspecting files\n" || output.Truncated {
		t.Fatalf("live output: %+v, %v", output, err)
	}
	for _, binding := range [][2]string{{g.ID, g.RootTask}, {"other-graph", task}} {
		if _, err := e.GraphRunOutput(binding[0], binding[1], run); err != ErrNotFound {
			t.Fatal("output escaped graph/task binding", err)
		}
	}
	var writers sync.WaitGroup
	for range 4 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for range 100 {
				_, _ = p.Write([]byte("progress\n"))
				_, _ = e.GraphRunOutput(g.ID, task, run)
			}
		}()
	}
	writers.Wait()
	_, _ = p.Write([]byte(strings.Repeat("界", agentOutputLimit)))
	output, err = e.GraphRunOutput(g.ID, task, run)
	if err != nil || !output.Truncated || !utf8.ValidString(output.Text) || len(p.text) > agentOutputLimit {
		t.Fatal("unbounded/invalid preview", err)
	}
	if err = e.Store.SaveArtifact(run, "output.txt", []byte("Saved execution log")); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.Complete(run, "evidence", true); err != nil {
		t.Fatal(err)
	}
	closePreview()
	output, err = e.GraphRunOutput(g.ID, task, run)
	if err != nil || output.Live || output.Text != "Saved execution log" {
		t.Fatalf("saved output: %+v, %v", output, err)
	}
}
