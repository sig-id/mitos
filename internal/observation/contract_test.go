package observation

import "testing"

func TestLocalContractRefusesUnsupportedDeclarations(t *testing.T) {
	cases := []Config{
		{},
		{ThinkTimeP95MS: -1},
		{ThinkTimeP95MS: 86400001},
		{ThinkTimeP95MS: 1, Session: &SessionSpec{Path: "../session", Format: "mitos-jsonl", SchemaVersion: "1", Tail: true}},
		{ThinkTimeP95MS: 1, Session: &SessionSpec{Path: "/tmp/session", Format: "mitos-jsonl", SchemaVersion: "1", Tail: true}},
		{ThinkTimeP95MS: 1, Session: &SessionSpec{Path: "session", Format: "native-unknown", SchemaVersion: "1", Tail: true}},
		{ThinkTimeP95MS: 1, Session: &SessionSpec{Path: "session", Format: "mitos-jsonl", SchemaVersion: "2", Tail: true}},
		{ThinkTimeP95MS: 1, Session: &SessionSpec{Path: "session", Format: "mitos-jsonl", SchemaVersion: "1"}},
	}
	for _, config := range cases {
		if err := config.Validate(); err == nil {
			t.Fatalf("unsupported declaration accepted: %+v", config)
		}
	}
	for _, config := range []Config{{ThinkTimeP95MS: 1}, {ThinkTimeP95MS: 60000, Session: &SessionSpec{Path: "native/session.jsonl", Format: "mitos-jsonl", SchemaVersion: "1", Tail: true}}} {
		if err := config.Validate(); err != nil {
			t.Fatal(err)
		}
		capabilities := config.Capabilities()
		if capabilities.UnattendedEligible || capabilities.PTYControl || capabilities.Interactive || capabilities.Resume != "none" || capabilities.ExecCoverage != "sampled_procfs" {
			t.Fatalf("local adapter overstated capability: %+v", capabilities)
		}
	}
}
