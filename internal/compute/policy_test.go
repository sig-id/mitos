package compute

import "testing"

func strong() Capabilities {
	return Capabilities{Isolation: "microvm", Remote: true, Egress: "broker", DataMount: "snapshot-ro", Snapshot: true, NetModes: []string{"deny-all-except-broker"}, WorkspaceConfined: true, Metered: true}
}
func TestT2AdmissionCannotDowngradeAnyBoundary(t *testing.T) {
	request := Request{TrustTier: "T2", BrokerEndpoint: "https://broker.example.invalid"}
	valid := strong()
	spec, err := Compile(request, map[string]Capabilities{"remote": valid}, false)
	if err != nil || spec.Provider != "remote" || spec.Development {
		t.Fatalf("eligible provider refused: %v", err)
	}
	for _, change := range []func(*Capabilities){func(c *Capabilities) { c.Remote = false }, func(c *Capabilities) { c.Isolation = "container" }, func(c *Capabilities) { c.Egress = "unrestricted" }, func(c *Capabilities) { c.DataMount = "bind-ro" }, func(c *Capabilities) { c.WorkspaceConfined = false }, func(c *Capabilities) { c.Metered = false }, func(c *Capabilities) { c.NetModes = []string{"host"} }} {
		caps := strong()
		change(&caps)
		if _, err := Compile(request, map[string]Capabilities{"remote": caps, "local": (&Local{}).Capabilities()}, false); err == nil {
			t.Fatalf("ineligible descriptor admitted: %+v", caps)
		}
	}
	request.WorkspaceMode = "in_place"
	if _, err := Compile(request, map[string]Capabilities{"remote": valid}, false); err == nil {
		t.Fatal("T2 in_place admitted")
	}
	request.WorkspaceMode = "copy"
	if _, err := Compile(request, map[string]Capabilities{"remote": valid}, true); err == nil {
		t.Fatal("T2 development downgrade admitted")
	}
	request.Provider = "missing"
	if _, err := Compile(request, map[string]Capabilities{"remote": valid}, false); err == nil {
		t.Fatal("explicit unavailable provider silently replaced")
	}
}
func TestLocalDevelopmentNeverClaimsBrokerOrConfinement(t *testing.T) {
	providers := map[string]Capabilities{"local": (&Local{}).Capabilities()}
	spec, err := Compile(Request{}, providers, true)
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Development || spec.Capabilities.WorkspaceConfined || spec.Capabilities.Egress != "unrestricted" {
		t.Fatal("development posture overstated")
	}
	for _, request := range []Request{{TrustTier: "T1"}, {TrustTier: "T0"}, {TrustTier: "T2"}, {TrustTier: "unknown"}, {TrustTier: "T1", NetMode: "deny-all-except-broker", BrokerEndpoint: "https://broker.example.invalid"}} {
		if _, err = Compile(request, providers, false); err == nil {
			t.Fatal("local execution satisfied unsupported enforcement")
		}
	}
}

func TestStrictLocalPosturesAndBrokerReferences(t *testing.T) {
	caps := Capabilities{Isolation: "gvisor", Egress: "broker", DataMount: "bind-ro", NetModes: []string{"deny-all-except-broker"}, WorkspaceConfined: true}
	request := Request{TrustTier: "T0", BrokerEndpoint: "unix:///run/mitos/broker.sock"}
	if _, err := Compile(request, map[string]Capabilities{"strict-local": caps}, false); err != nil {
		t.Fatal(err)
	}
	caps.Isolation = "container"
	if _, err := Compile(request, map[string]Capabilities{"container": caps}, false); err == nil {
		t.Fatal("T0 selected a strict container posture")
	}
	request.TrustTier = "T1"
	if _, err := Compile(request, map[string]Capabilities{"container": caps}, false); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://user:example@broker.example.invalid", "https://broker.example.invalid?credential=example", "https://broker.example.invalid#fragment", "file:///run/broker"} {
		request.BrokerEndpoint = endpoint
		if _, err := Compile(request, map[string]Capabilities{"container": caps}, false); err == nil {
			t.Fatal("invalid broker reference admitted")
		}
	}
}
