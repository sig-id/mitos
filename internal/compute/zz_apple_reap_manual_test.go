package compute

// Manual smoke test for ADR 0011 container reaping on apple/container: boots
// a real detached VM, records its cidfile, and proves reapAppleContainer
// kills and deletes it through the real CLI. Skips without the CLI.
//
//	go test ./internal/compute -run TestManualAppleReapSmoke -v -count=1

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func appleContainerIDs(t *testing.T, all bool) map[string]bool {
	t.Helper()
	args := []string{ConfinementContainerCLI, "ls", "--format", "json"}
	if all {
		args = append(args, "--all")
	}
	raw, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("container ls failed: %v: %s", err, raw)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("container ls output not JSON: %v: %s", err, raw)
	}
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids[row.ID] = true
	}
	return ids
}

func TestManualAppleReapSmoke(t *testing.T) {
	if _, err := exec.LookPath(ConfinementContainerCLI); err != nil {
		t.Skip("container CLI required")
	}
	base, err := os.MkdirTemp("", "ms-reap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	cidfile := filepath.Join(base, appleContainerCidFn)
	argv := []string{
		ConfinementContainerCLI, "run", "-d", "--rm", "--progress", "none",
		"--cidfile", cidfile,
		"docker.io/library/alpine:latest", "sleep", "60",
	}
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("detached launch failed: %v: %s", err, out)
	}
	var id string
	deadline := time.Now().Add(90 * time.Second)
	for id == "" {
		raw, _ := os.ReadFile(cidfile)
		id = string(raw)
		if id == "" {
			if time.Now().After(deadline) {
				t.Fatal("cidfile never appeared")
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	t.Logf("booted container %s", id)
	for !appleContainerIDs(t, false)[id] {
		if time.Now().After(deadline) {
			t.Fatal("container never reported running")
		}
		time.Sleep(500 * time.Millisecond)
	}
	c := Confinement{Tool: ConfinementAppleContainer, NetMode: NetOff, Image: "unused"}
	reapAppleContainer(c, base)
	if _, statErr := os.Stat(cidfile); !os.IsNotExist(statErr) {
		t.Fatalf("cidfile not cleaned: %v", statErr)
	}
	if appleContainerIDs(t, true)[id] {
		t.Fatalf("container %s survived the reap", id)
	}
}
