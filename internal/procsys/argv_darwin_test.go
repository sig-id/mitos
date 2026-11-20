//go:build darwin

package procsys

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestArgvReadsOwnAndChildCommandLines(t *testing.T) {
	self, err := Argv(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if len(self) == 0 || !strings.HasSuffix(self[0], ".test") {
		t.Fatal("own argv missing:", self)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "child.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	attr := &syscall.ProcAttr{Files: []uintptr{0, 1, 2}}
	pid, _, err := syscall.StartProcess("/bin/sh", []string{"/bin/sh", script, "--flag", "value"}, attr)
	if err != nil {
		t.Fatal(err)
	}
	proc, _ := os.FindProcess(pid)
	defer proc.Signal(syscall.SIGKILL)
	argv, err := Argv(pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 4 || argv[0] != "/bin/sh" || argv[2] != "--flag" || argv[3] != "value" {
		t.Fatal("child argv mismatch:", argv)
	}
}
