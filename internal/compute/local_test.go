package compute

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspacefs "mitos/internal/workspace"
)

func prepared(t *testing.T) (*Local, LaunchSpec, Handle) {
	t.Helper()
	ctx := context.Background()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspacefs.CaptureSource(ctx, source, "", workspacefs.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "execution")
	history, err := workspacefs.Prepare(ctx, base, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	local := &Local{}
	spec, err := Compile(Request{}, map[string]Capabilities{"local": local.Capabilities()}, true)
	if err != nil {
		t.Fatal(err)
	}
	spec.RunID = "run"
	spec.CapabilityRef = "cap"
	spec.Base = base
	spec.Workspace.Directory = filepath.Join(base, "workspace")
	spec.Workspace.HistoryDirectory = history.Directory
	spec.Workspace.BaselineCommit = history.BaselineCommit
	spec.Workspace.BaselineFingerprint = snapshot.Fingerprint
	handle, err := local.Prepare(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	return local, spec, handle
}
func TestLocalLifecyclePreservesFailedChangesAndResumableStatuses(t *testing.T) {
	local, _, handle := prepared(t)
	ctx := context.Background()
	t.Setenv("MITOS_TEST_SECRET", "must-not-be-inherited")
	session, err := local.Start(ctx, handle, Command{Argv: []string{"/bin/sh", "-c", `test -z "$MITOS_TEST_SECRET" || exit 3; printf changed > input; printf output; exit 7`}})
	if err != nil {
		t.Fatal(err)
	}
	<-session.Done()
	output, err := session.Result()
	if err == nil || string(output) != "output" {
		t.Fatal("failure/output were lost")
	}
	stream, err := local.Stream(ctx, session, 0)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	for event := range stream {
		events = append(events, event)
	}
	if len(events) != 2 || events[0].Offset != 1 || events[1].Offset != 2 || events[1].ExitCode == nil || *events[1].ExitCode != 7 {
		t.Fatalf("bad stream: %+v", events)
	}
	resumed, _ := local.Stream(ctx, session, 1)
	count := 0
	for event := range resumed {
		count++
		if event.Offset != 2 {
			t.Fatal("resumed stream replayed old offset")
		}
	}
	if count != 1 {
		t.Fatal("missing resumed status")
	}
	result, err := local.Harvest(ctx, session)
	if err != nil || result.Change == nil || len(result.Change.Changes) != 1 || !strings.Contains(string(result.Change.Patch), "+changed") {
		t.Fatalf("failed harvest: %v %+v", err, result)
	}
	for range 2 {
		if err = local.Stop(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = local.Start(ctx, handle, Command{Argv: []string{"/bin/sh", "-c", "exit 0"}}); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatal("prepared handle started twice")
	}
	if _, err = local.Snapshot(ctx, session); !errors.Is(err, ErrUnsupported) {
		t.Fatal("local advertised a provider snapshot")
	}
	if err = local.Control(ctx, session, "approve"); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unverified steering accepted")
	}
}
func TestPreparePinsPostureAndRejectsForeignHandles(t *testing.T) {
	local, spec, handle := prepared(t)
	ctx := context.Background()
	again, err := local.Prepare(ctx, spec)
	if err != nil || again != handle {
		t.Fatal("prepare is not idempotent")
	}
	other := &Local{}
	if _, err = other.Start(ctx, handle, Command{Argv: []string{"/bin/sh"}}); err == nil {
		t.Fatal("foreign handle accepted")
	}
	changed := spec
	changed.TrustTier = ""
	if _, err = local.Prepare(ctx, changed); err == nil {
		t.Fatal("unbound trust tier accepted")
	}
	changed = spec
	changed.Posture.DataMount = "snapshot-ro"
	if _, err = local.Prepare(ctx, changed); err == nil {
		t.Fatal("forged data posture accepted")
	}
	changed = spec
	changed.CapabilityRef = "other"
	if _, err = other.Prepare(ctx, changed); err == nil {
		t.Fatal("saved launch identity changed")
	}
}
func TestProviderDeadlineStopsTreeBeforeHarvest(t *testing.T) {
	local, spec, _ := prepared(t)
	spec.Base = filepath.Join(t.TempDir(), "deadline")
	if err := os.MkdirAll(filepath.Join(spec.Base, "workspace"), 0700); err != nil {
		t.Fatal(err)
	}
	spec.Workspace.Directory = filepath.Join(spec.Base, "workspace")
	spec.Workspace.HistoryDirectory = ""
	spec.Posture.Deadline = time.Now().Add(200 * time.Millisecond)
	handle, err := local.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	session, err := local.Start(context.Background(), handle, Command{Argv: []string{"/bin/sh", "-c", "printf partial; sleep 30 & wait"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("provider deadline did not stop command")
	}
	output, err := session.Result()
	if err == nil || string(output) != "partial" || time.Since(started) > 3*time.Second {
		t.Fatal("deadline/output failure")
	}
	if _, err = local.Harvest(context.Background(), session); err != nil {
		t.Fatal(err)
	}
}
func TestPrepareMaterializesSourceWithoutChangingIt(t *testing.T) {
	local := &Local{}
	spec, _ := Compile(Request{}, map[string]Capabilities{"local": local.Capabilities()}, true)
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "tracked"), []byte("kept"), 0600)
	os.WriteFile(filepath.Join(source, ".gitignore"), []byte("ignored\n"), 0600)
	os.WriteFile(filepath.Join(source, "ignored"), []byte("skip"), 0600)
	spec.RunID = "run"
	spec.CapabilityRef = "cap"
	spec.Base = filepath.Join(t.TempDir(), "prepared")
	spec.Workspace.Source = source
	spec.Workspace.Directory = filepath.Join(spec.Base, "workspace")
	handle, err := local.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(handle.Workspace(), "tracked")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(handle.Workspace(), "ignored")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ignored input was copied")
	}
	if _, err = os.Stat(filepath.Join(source, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source repository was changed")
	}
	resumed := &Local{}
	handle, err = resumed.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	session, err := resumed.Start(context.Background(), handle, Command{Argv: []string{"/bin/sh", "-c", "printf result > result.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	<-session.Done()
	result, err := resumed.Harvest(context.Background(), session)
	if err != nil || result.Change == nil || len(result.Change.Changes) != 1 {
		t.Fatalf("provider-owned baseline was lost: %v", err)
	}
}
