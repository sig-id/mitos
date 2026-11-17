package studio

import (
	"bytes"
	"errors"
	"mitos/internal/decision"
	"mitos/internal/messaging"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSQLiteBackupRestoreVerifiesArtifactsLibraryAndFencesEpoch(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	if err := s.SaveArtifact(r.ID, "report.txt", []byte("retained evidence")); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(r.ID, "Verified", true); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(r.ID)
	e := &Engine{Store: s}
	definition, err := e.SaveAgent(AgentEdit{Key: "new", Name: "Backup agent", Description: "Retain the exact revision", Instructions: "Inspect the evidence."})
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err = s.Backup(backup); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{runtimeDatabase, "backup.version", "artifacts/" + r.ID + "/report.txt"} {
		info, err := os.Stat(filepath.Join(backup, name))
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("backup missing or not private", name, err)
		}
	}
	epochs := map[string]bool{s.AuthorityEpoch(): true}
	for range 2 {
		restored := filepath.Join(t.TempDir(), "restored")
		if err = RestoreStore(backup, restored); err != nil {
			t.Fatal(err)
		}
		store, err := OpenStore(restored)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := store.Get(r.ID)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("restore changed run")
		}
		if data, err := store.Artifact(r.ID, "report.txt"); err != nil || string(data) != "retained evidence" {
			t.Fatal("lost artifact", err)
		}
		library, err := (&Engine{Store: store}).readLibrary()
		if err != nil || library.Revisions[definition.ID][0].Digest != definition.Digest {
			t.Fatal("lost exact library revision", err)
		}
		epoch := store.AuthorityEpoch()
		if epochs[epoch] {
			t.Fatal("restore reused an authority epoch", epoch)
		}
		epochs[epoch] = true
		store.Close()
		if err = RestoreStore(backup, restored); err == nil {
			t.Fatal("restore overwrote existing data")
		}
	}
	if err = os.WriteFile(filepath.Join(backup, "artifacts", r.ID, "report.txt"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	rejected := filepath.Join(t.TempDir(), "rejected")
	if err = RestoreStore(backup, rejected); err == nil {
		t.Fatal("corrupt artifact backup restored")
	}
	if _, err = os.Stat(rejected); !os.IsNotExist(err) {
		t.Fatal("failed restore published a directory")
	}
}

func TestReceiptTombstonesSurviveReopenAndIndexesRebuild(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	b := structuredBrief("expires-once", "agent:mitos.generalist")
	r, err := e.LaunchStructured(b)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Store.Transition(r.ID, "run_failed", "failed", "Failed", "No external effects"); err != nil {
		t.Fatal(err)
	}
	if n, err := e.Store.ExpireStructuredKeys(time.Now().Add(time.Second)); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err = e.LaunchStructured(b); !errors.Is(err, ErrRequestKeyExpired) {
		t.Fatal("expired retry admitted", err)
	}
	s := e.Store
	j := s.journal.(*sqliteJournal)
	if _, err = j.db.Exec("DELETE FROM projections"); err != nil {
		t.Fatal(err)
	}
	before := s.hash
	if err = s.RebuildJournalIndexes(); err != nil {
		t.Fatal(err)
	}
	if s.hash != before {
		t.Fatal("index rebuild changed canonical history")
	}
	var value []byte
	if err = j.db.QueryRow("SELECT value FROM projections WHERE namespace='request_tombstone' AND key=?", b.Structured.Key).Scan(&value); err != nil || string(value) != "expired" {
		t.Fatal("tombstone index not rebuilt", err)
	}
	e.Close()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.requestKeyExpired(b.Structured.Key) {
		t.Fatal("expired key lost on replay")
	}
}

func TestRestoreFencesExistingDeviceAndAgentTokensAndKeepsMQTTIdentity(t *testing.T) {
	s := testStore(t)
	_, deviceToken, err := s.PairDevice(DevicePairInput{Name: "Before restore", Permissions: DevicePermissions{Read: true}, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	session, agentToken, err := s.CreateAgentSession("codex", "Before restore", t.TempDir(), decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	identity, err := messaging.LoadIdentity(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err = s.Backup(backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err = RestoreStore(backup, restored); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.AuthenticateDevice(deviceToken, "read"); err == nil {
		t.Fatal("pre-restore device token survived")
	}
	if err = store.AgentHeartbeat(session.ID, agentToken); err == nil {
		t.Fatal("pre-restore agent token survived")
	}
	gotIdentity, err := messaging.LoadIdentity(restored)
	if err != nil || !bytes.Equal(identity, gotIdentity) {
		t.Fatal("MQTT identity lost", err)
	}
	_, newToken, err := store.PairDevice(DevicePairInput{Name: "After restore", Permissions: DevicePermissions{Read: true}, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AuthenticateDevice(newToken, "read"); err != nil {
		t.Fatal("fresh token fenced", err)
	}
}
