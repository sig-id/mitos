package studio

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mitos/internal/decision"
)

func pairTestDevice(t *testing.T, s *Store, p decision.Principal) (Device, string) {
	t.Helper()
	d, token, err := s.PairDevice(DevicePairInput{Name: "Handheld", Permissions: DevicePermissions{Read: true, Resolve: true, Voice: true}, ExpiresAt: time.Now().Add(time.Hour), STTDisclosure: "Local test transcriber; no retention"}, p)
	if err != nil {
		t.Fatal(err)
	}
	return d, token
}
func deviceCommand(t *testing.T, s *Store, token string) (DeviceCommand, []byte) {
	t.Helper()
	snap, err := s.DeviceSnapshot(token)
	if err != nil || len(snap.Cards) == 0 {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
	c := snap.Cards[0]
	cmd := DeviceCommand{1, "command_0123456789abcdef0123456789abcdef", c.DecisionID, c.RunID, c.Revision, "cancel", json.RawMessage("null")}
	body, _ := json.Marshal(cmd)
	return cmd, body
}
func TestDeviceLifecycleAndTenantIsolation(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	if err := s.Park(r.ID, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	d, token := pairTestDevice(t, s, decision.LocalOperator())
	decoded, decodeErr := hex.DecodeString(token)
	if decodeErr != nil || len(decoded) != 32 {
		t.Fatal("credential must contain 256 random bits")
	}
	if d.TokenHash != "" {
		t.Fatal("pair returned token hash")
	}
	if _, err := s.AuthenticateDevice("wrong", "read"); err == nil {
		t.Fatal("wrong token accepted")
	}
	_, other := pairTestDevice(t, s, decision.Principal{ID: "operator", Tenant: decision.Tenant{Org: "other", Project: "project"}, Authenticated: true})
	snap, err := s.DeviceSnapshot(other)
	if err != nil || len(snap.Cards) != 0 || len(snap.Runs) != 0 {
		t.Fatalf("tenant leak: %+v %v", snap, err)
	}
	cmd, body := deviceCommand(t, s, token)
	if _, _, err = s.acceptDeviceCommand(other, cmd, body, false); err == nil {
		t.Fatal("cross tenant answer accepted")
	}
	rotated, newToken, err := s.ChangeDevice(d.ID, true, time.Now().Add(time.Hour), decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Revision == d.Revision {
		t.Fatal("rotation retained authorization revision")
	}
	if _, err = s.AuthenticateDevice(token, "read"); err == nil {
		t.Fatal("old token accepted")
	}
	if _, _, err = s.acceptDeviceCommand(newToken, cmd, body, false); err == nil {
		t.Fatal("old review authorized after rotation")
	}
	if _, _, err = s.ChangeDevice(d.ID, false, time.Time{}, decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateDevice(newToken, "read"); err == nil {
		t.Fatal("revoked token accepted")
	}
	log, err := os.ReadFile(filepath.Join(s.Dir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(log, []byte(token)) || bytes.Contains(log, []byte(newToken)) {
		t.Fatal("plaintext token persisted")
	}
	for _, d := range s.Devices(decision.LocalOperator()) {
		if d.TokenHash != "" {
			t.Fatal("list leaked token hash")
		}
	}
}
func TestDeviceReceiptAtomicReplayAndSingleRetry(t *testing.T) {
	for _, childCreated := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_application", true: "after_child"}[childCreated], func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			r, err := s.Create(testBrief(root), "Inventory", "", false)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Park(r.ID, recoveryDecision()); err != nil {
				t.Fatal(err)
			}
			_, token := pairTestDevice(t, s, decision.LocalOperator())
			cmd, _ := deviceCommand(t, s, token)
			cmd.Choice = "retry"
			body, _ := json.Marshal(cmd)
			receipt, fresh, err := s.acceptDeviceCommand(token, cmd, body, false)
			if err != nil || !fresh {
				t.Fatal(receipt, fresh, err)
			}
			accepted, _ := s.Get(r.ID)
			last := accepted.Events[len(accepted.Events)-1]
			var c change
			_ = json.Unmarshal(last.Data, &c)
			if c.Resolution == nil || c.DeviceCommand == nil || c.Resolution.DeviceID == "" || c.Resolution.Via != "esp-notify" {
				t.Fatal("resolution and receipt not in one durable record")
			}
			if childCreated {
				if _, err = s.Create(r.Brief, r.AppName, r.ID, false); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			again, fresh, err := s.acceptDeviceCommand(token, cmd, body, false)
			if err != nil || fresh || again != receipt {
				t.Fatal("restart receipt changed", again, err)
			}
			changed := append(append([]byte{}, body...), ' ')
			if _, _, err = s.acceptDeviceCommand(token, cmd, changed, false); err == nil {
				t.Fatal("same ID with changed body accepted")
			}
			apps, _ := LoadApps("")
			e, err := NewEngine(s, apps, root)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			applied := waitRun(t, s, r.ID, func(r Run) bool { return r.Decision.AppliedAt != nil })
			replay, err := e.ResolveDevice(token, cmd, body)
			if err != nil || replay != receipt {
				t.Fatal("applied receipt changed", replay, err)
			}
			children := 0
			for _, run := range s.List() {
				if run.ParentID == r.ID {
					children++
				}
			}
			if children != 1 || applied.Decision.ResultRunID == "" {
				t.Fatalf("retry created %d children", children)
			}
		})
	}
}
func TestConsoleDeviceRaceHasOneWinner(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	if err := s.Park(r.ID, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	_, token := pairTestDevice(t, s, decision.LocalOperator())
	cmd, body := deviceCommand(t, s, token)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _, _ = s.acceptDeviceCommand(token, cmd, body, false) }()
	go func() {
		defer wg.Done()
		_ = s.ResolveChoice(r.ID, decision.Resolution{DecisionID: cmd.DecisionID, Choice: "retry", Via: "console"}, decision.LocalOperator(), nil)
	}()
	wg.Wait()
	got, _ := s.Get(r.ID)
	count := 0
	for _, e := range got.Events {
		if e.Kind == "approval_resolved" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d authorizations", count)
	}
	if got.Decision.Resolution.Via == "esp-notify" && got.Decision.Resolution.Choice != "cancel" {
		t.Fatal("wrong device choice")
	}
}
func TestDeviceAcceptanceFailureDoesNotPublish(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	if err := s.Park(r.ID, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	_, token := pairTestDevice(t, s, decision.LocalOperator())
	cmd, body := deviceCommand(t, s, token)
	s.file = &controlledLog{eventLog: s.file, write: true, failure: errors.New("disk failure")}
	if _, _, err := s.acceptDeviceCommand(token, cmd, body, false); err == nil {
		t.Fatal("acknowledged failed commit")
	}
	got, _ := s.Get(r.ID)
	if got.Decision.Resolution != nil || len(s.deviceCommands) != 0 {
		t.Fatal("unsaved acceptance published")
	}
}
func TestDeviceNewCommandRejectsStaleAndUnsupportedWithoutMutation(t *testing.T) {
	for _, which := range []string{"revision", "choice", "value", "expired", "display"} {
		t.Run(which, func(t *testing.T) {
			s := testStore(t)
			r := createRun(t, s)
			if err := s.Park(r.ID, recoveryDecision()); err != nil {
				t.Fatal(err)
			}
			_, token := pairTestDevice(t, s, decision.LocalOperator())
			cmd, body := deviceCommand(t, s, token)
			switch which {
			case "revision":
				cmd.Revision = "stale"
			case "choice":
				cmd.Choice = "publish"
			case "value":
				cmd.Value = json.RawMessage(`"secret"`)
			case "expired":
				ttl := int64(1)
				choice := "cancel"
				s.runs[r.ID].Decision.TTLMS = &ttl
				s.runs[r.ID].Decision.Default = &choice
				s.runs[r.ID].Decision.RequestedAt = time.Now().Add(-time.Hour)
			case "display":
				s.runs[r.ID].Decision.Question = "Approve ✓?"
			}
			seq := s.seq
			if _, _, err := s.acceptDeviceCommand(token, cmd, body, false); err == nil {
				t.Fatal("invalid acceptance")
			}
			if s.seq != seq || s.runs[r.ID].Decision.Resolution != nil {
				t.Fatal("rejected command mutated log")
			}
		})
	}
}

func TestDeviceBlockedApplicationAndHistoricalOutcomePersist(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := createRun(t, s)
	if err = s.Park(r.ID, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	_, token := pairTestDevice(t, s, decision.LocalOperator())
	cmd, _ := deviceCommand(t, s, token)
	cmd.Choice = "retry"
	body, _ := json.Marshal(cmd)
	receipt, _, err := s.acceptDeviceCommand(token, cmd, body, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.failDecisionApplication(r.ID, "Application needs attention.", true); err != nil {
		t.Fatal(err)
	}
	// A replacement request must not erase the recent blocked outcome.
	next := recoveryDecision()
	next.Question = "Start another attempt?"
	if err = s.Park(r.ID, next); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snapshot, err := s.DeviceSnapshot(token)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PendingTotal != 1 || len(snapshot.Cards) != 2 || snapshot.Cards[0].State != "pending" || snapshot.Cards[1].State != "application_blocked" || snapshot.Cards[1].ApplicationError != "Application needs attention." {
		t.Fatalf("lost history: %+v", snapshot)
	}
	replay, fresh, err := s.acceptDeviceCommand(token, cmd, body, false)
	if err != nil || fresh || replay != receipt {
		t.Fatal("superseded card lost receipt", replay, err)
	}
}
