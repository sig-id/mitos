package desk

import (
	"errors"
	"strings"
	"testing"
)

func TestStopEndedSessionPreservesEvidence(t *testing.T) {
	for _, tc := range []struct{ status, command string }{{"failed", "printf retained; exit 7"}, {"exited", "printf retained"}} {
		t.Run(tc.status, func(t *testing.T) {
			m, p := testManager(t)
			s, err := startTestCommand(m, p.ID, "Ended", "shell", tc.command, "http://localhost")
			if err != nil {
				t.Fatal(err)
			}
			await(t, func() bool { v, _ := m.Session(s.ID); return v.Status == tc.status })
			before, _ := m.Session(s.ID)
			originalOutput, err := m.ReadOutput(s.ID, 0)
			if err != nil || !strings.Contains(string(originalOutput.Data), "retained") {
				t.Fatalf("missing process output: %q, %v", originalOutput.Data, err)
			}
			for range 2 {
				if err := m.Stop(s.ID); err != nil {
					t.Fatal(err)
				}
			}
			after, _ := m.Session(s.ID)
			if after.Status != "stopped" || after.PID != 0 || *after.ExitCode != *before.ExitCode || !after.EndedAt.Equal(*before.EndedAt) || after.Detail != before.Detail {
				t.Fatalf("close lost exit evidence: before=%+v after=%+v", before, after)
			}
			m.Close()
			reopened, err := Open(m.dir, m.root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			saved, _ := reopened.Session(s.ID)
			output, err := reopened.ReadOutput(s.ID, 0)
			if saved.Status != "stopped" || err != nil || string(output.Data) != string(originalOutput.Data) {
				t.Fatalf("close was not durable: %+v, %q, %v", saved, output.Data, err)
			}
		})
	}
}

func TestStopInterruptedSessionRequiresSettlement(t *testing.T) {
	m, p := testManager(t)
	m.state.Sessions = append(m.state.Sessions, Session{ID: "interrupted", ProjectID: p.ID, Status: "interrupted"})
	if err := m.Stop("interrupted"); !errors.Is(err, ErrConflict) {
		t.Fatalf("interrupted session falsely reported stopped: %v", err)
	}
	if s, _ := m.Session("interrupted"); s.Status != "interrupted" {
		t.Fatal("stop cleared the unverified process state")
	}
	if err := m.Settle("interrupted", true); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop("interrupted"); err != nil {
		t.Fatal(err)
	}
}
