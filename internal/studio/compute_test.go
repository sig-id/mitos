package studio

import (
	"context"
	"sync/atomic"
	"testing"

	"mitos/internal/compute"
)

type recordingProvider struct {
	compute.Local
	starts   atomic.Int32
	harvests atomic.Int32
	stops    atomic.Int32
}

func (p *recordingProvider) Start(ctx context.Context, h compute.Handle, c compute.Command) (compute.Session, error) {
	p.starts.Add(1)
	return p.Local.Start(ctx, h, c)
}
func (p *recordingProvider) Harvest(ctx context.Context, s compute.Session) (compute.Harvest, error) {
	p.harvests.Add(1)
	return p.Local.Harvest(ctx, s)
}
func (p *recordingProvider) Stop(ctx context.Context, s compute.Session) error {
	p.stops.Add(1)
	return p.Local.Stop(ctx, s)
}
func TestWorkerLifecycleUsesProviderAndLogsActualPosture(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `printf result > result.txt`)
	e, s := testEngine(t, root, []Application{app})
	provider := &recordingProvider{}
	e.provider = provider
	r := launchApp(t, e, root, app)
	r = waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
	if r.Status != "done" || provider.starts.Load() != 1 || provider.harvests.Load() != 1 || provider.stops.Load() < 1 {
		t.Fatalf("provider lifecycle bypassed: %s starts=%d harvests=%d stops=%d", r.Status, provider.starts.Load(), provider.harvests.Load(), provider.stops.Load())
	}
	if r.Execution.Launch == nil || !r.Execution.Launch.Development || r.Execution.Launch.Capabilities.WorkspaceConfined || r.Execution.Launch.Posture.NetMode != "unrestricted" {
		t.Fatal("recorded posture does not match local execution")
	}
	statuses := 0
	compiled := 0
	for _, event := range r.Events {
		if event.Kind == "provider_status" {
			statuses++
		}
		if event.Kind == "provider_launch_compiled" {
			compiled++
		}
	}
	if statuses != 2 || compiled != 1 {
		t.Fatal("provider lifecycle evidence missing")
	}
}
func TestIneligibleComputeRequestCreatesNoRun(t *testing.T) {
	for _, request := range []compute.Request{{TrustTier: "T2"}, {TrustTier: "T1", NetMode: "deny-all-except-broker", BrokerEndpoint: "https://broker.example.invalid"}, {TrustTier: "T1", Provider: "missing"}, {TrustTier: "T2", WorkspaceMode: "in_place"}} {
		root := t.TempDir()
		app := executionApp(t, `printf result > result.txt`)
		app.Compute = &request
		e, s := testEngine(t, root, []Application{app})
		brief := testBrief(root)
		brief.AppID = app.ID
		if _, err := e.Launch(brief, ""); err == nil {
			t.Fatal("unsupported request launched")
		}
		if len(s.List()) != 0 {
			t.Fatal("refused provider created runnable work")
		}
	}
}
