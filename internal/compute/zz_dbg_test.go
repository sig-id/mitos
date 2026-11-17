package compute

import (
	"os"
	"strings"
	"testing"
)

func TestDebugArgv(t *testing.T) {
	c := Confinement{Tool: ConfinementBwrap, NetMode: NetUnrestricted, EgressSocket: "/tmp/egsock/model.sock", HelperExecutable: os.Args[0]}
	argv, err := confineArgv(c, []string{"/bin/sh", "-c", "true"}, "/tmp/egattempt", "/tmp/egattempt/workspace")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + strings.Join(argv, " \\\n"))
}
