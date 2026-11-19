package studio

import (
	"context"
	"errors"
	"sync"
	"testing"

	"mitos/internal/compute"
	"mitos/internal/resourcepolicy"
)

// enforcingStub stands in for the Nucleus provider: it advertises the probed
// confined capabilities and records the launch it is asked to prepare.
type enforcingStub struct {
	compute.Local
	mu       sync.Mutex
	prepared []compute.LaunchSpec
}

func (p *enforcingStub) Capabilities() compute.Capabilities {
	return compute.Capabilities{CPUQuota: true, MemoryLimit: true, Isolation: "container", Egress: compute.NetOff, DataMount: "bind-ro", NetModes: []string{compute.NetOff}, WorkspaceConfined: true}
}

func (p *enforcingStub) Prepare(_ context.Context, spec compute.LaunchSpec) (compute.Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepared = append(p.prepared, spec)
	return nil, errors.New("stub provider does not execute")
}

func confinedApp(t *testing.T) Application {
	app := executionApp(t, `printf result > result.txt`)
	app.Compute = &compute.Request{TrustTier: "T1", NetMode: compute.NetOff, Resources: resourcepolicy.Agent{MemoryMiB: resourcepolicy.Capped(256)}}
	return app
}

func TestDeclaredConfinedPostureRunsOnlyUnderNucleus(t *testing.T) {
	root := t.TempDir()
	app := confinedApp(t)
	e, s := testEngine(t, root, []Application{app})
	brief := testBrief(root)
	brief.AppID = app.ID
	// Without an enforcing provider the posture is refused, never downgraded.
	if _, err := e.Launch(brief, ""); err == nil {
		t.Fatal("confined posture launched without an enforcing provider")
	}
	if len(s.List()) != 0 {
		t.Fatal("refused posture created runnable work")
	}
	stub := &enforcingStub{}
	e.nucleus = stub
	r := launchApp(t, e, root, app)
	waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.prepared) == 0 {
		t.Fatal("confined launch bypassed the nucleus provider")
	}
	launch := stub.prepared[0]
	if launch.Provider != compute.ConfinementNucleus || launch.Development || launch.Posture.NetMode != compute.NetOff || launch.Posture.Resources.MemoryMiB.Amount != 256 {
		t.Fatalf("admitted launch %+v", launch)
	}
}

func TestDevelopmentApplicationsKeepTheLocalProvider(t *testing.T) {
	e := &Engine{provider: &compute.Local{}, nucleus: &enforcingStub{}}
	launch, err := e.compileApplication(executionApp(t, "true"))
	if err != nil || launch.Provider != "local" || !launch.Development {
		t.Fatalf("development app admitted as %+v: %v", launch, err)
	}
	provider, err := e.providerFor(launch)
	if err != nil || provider != e.provider {
		t.Fatalf("development launch routed to %T: %v", provider, err)
	}
}

func TestFrozenNucleusLaunchRefusedWhenNucleusIsGone(t *testing.T) {
	e := &Engine{provider: &compute.Local{}}
	if _, err := e.providerFor(compute.LaunchSpec{Provider: compute.ConfinementNucleus}); err == nil {
		t.Fatal("nucleus-admitted launch routed to another provider")
	}
	if _, err := e.providerFor(compute.LaunchSpec{Provider: "unknown"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
}
