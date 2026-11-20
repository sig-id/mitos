//go:build linux

package desk

import (
	"os/exec"
	"strings"
	"testing"
)

// A managed PTY session's master must not leak into unrelated children:
// exec.Command relies on FD_CLOEXEC for that, so the poller duplicate must
// carry the flag. A plain dup clears it, and children started while the
// session is open would inherit read and write access to another session's
// terminal and keep its master alive past teardown.
func TestTerminalMasterNotInheritedByChildren(t *testing.T) {
	m, p := testManager(t)
	s, err := startTestCommand(m, p.ID, "Holder", "agent", "sleep 60", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Stop(s.ID)
	out, err := exec.Command("sh", "-c", "ls -l /proc/self/fd").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "ptmx") {
			t.Fatal("child inherited a session master descriptor:", line)
		}
	}
}
