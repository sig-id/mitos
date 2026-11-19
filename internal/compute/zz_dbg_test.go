package compute

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDebugArgv(t *testing.T) {
	if _, err := exec.LookPath(ConfinementBwrap); err != nil {
		t.Skip("bwrap required for confinement argv inspection")
	}
	c := Confinement{Tool: ConfinementBwrap, NetMode: NetUnrestricted, EgressSocket: "/tmp/egsock/model.sock", HelperExecutable: os.Args[0]}
	argv, err := confineArgv(c, []string{"/bin/sh", "-c", "true"}, "/tmp/egattempt", "/tmp/egattempt/workspace")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + strings.Join(argv, " \\\n"))
}
