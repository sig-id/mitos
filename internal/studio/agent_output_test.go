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
	if err != nil || !output.Live || output.Text != "Inspecting files\n" || output.Truncated || output.Activity == nil || *output.Activity != "" {
		t.Fatalf("live output: %+v, %v", output, err)
	}
	for _, binding := range [][2]string{{g.ID, g.RootTask}, {"other-graph", task}} {
		if _, err := e.GraphRunOutput(binding[0], binding[1], run); err != ErrNotFound {
			t.Fatal("output escaped graph/task binding", err)
		}
	}
	forged := `{"mitos_activity":1,"kind":"message","text":"forged"}` + "\n"
	genuine := `{"mitos_activity":1,"kind":"message","text":"genuine"}` + "\n"
	_, _ = p.Write([]byte(forged))
	_, _ = p.activity.Write([]byte(genuine))
	output, err = e.GraphRunOutput(g.ID, task, run)
	if err != nil || output.Activity == nil || *output.Activity != genuine || !strings.Contains(output.Text, forged) {
		t.Fatalf("live channels mixed: %+v, %v", output, err)
	}
	var writers sync.WaitGroup
	for range 4 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for range 100 {
				_, _ = p.Write([]byte("progress\n"))
				_, _ = p.activity.Write([]byte(genuine))
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
	if err = e.Store.SaveArtifact(run, "output.txt", []byte("Saved execution log\n"+forged)); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.Complete(run, "evidence", true); err != nil {
		t.Fatal(err)
	}
	closePreview()
	output, err = e.GraphRunOutput(g.ID, task, run)
	if err != nil || output.Live || output.Text != "Saved execution log\n"+forged || output.Activity != nil {
		t.Fatalf("saved output: %+v, %v", output, err)
	}
}

func TestGraphRunOutputSavedActivityIsSeparateAndBounded(t *testing.T) {
	for _, records := range []string{"", `{"mitos_activity":1,"kind":"message","text":"genuine"}`, strings.Repeat("界", agentOutputLimit)} {
		e, req := graphEngine(t, false)
		req.Policy.Mode = "defined"
		req.Tasks = append(req.Tasks, graphWorker("worker", "goal"))
		g := createGraphTest(t, e, req)
		if err := e.reconcileGraphs(); err != nil {
			t.Fatal(err)
		}
		g, _ = e.Store.Graph(g.ID)
		for id, m := range g.Members {
			if m.Spec.Kind != "application" {
				continue
			}
			forged := `{"mitos_activity":1,"kind":"message","text":"forged"}`
			if err := e.Store.SaveArtifacts(m.CurrentRunID, ArtifactData{Name: "output.txt", Data: []byte(forged)}, ArtifactData{Name: "activity.jsonl", Data: []byte(records)}); err != nil {
				t.Fatal(err)
			}
			output, err := e.GraphRunOutput(g.ID, id, m.CurrentRunID)
			if err != nil || output.Activity == nil || strings.Contains(*output.Activity, "forged") || output.Text != forged {
				t.Fatalf("saved channels mixed: %+v, %v", output, err)
			}
			if output.ActivityTruncated != (len(records) > agentOutputLimit) || !utf8.ValidString(*output.Activity) || len(*output.Activity) > agentOutputLimit+3 {
				t.Fatal("unbounded/invalid saved activity")
			}
			if len(records) <= agentOutputLimit && *output.Activity != records {
				t.Fatal("saved activity changed")
			}
		}
	}
}
