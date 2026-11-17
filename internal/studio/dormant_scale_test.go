package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"mitos/agents"
	"mitos/internal/decision"
	"mitos/internal/structuredworker"
)

// This fixed workload measures the control plane, not model/process throughput.
// Run with -run '^$' -bench '^BenchmarkDormantResponsibilities$' -benchtime=1x.
func BenchmarkDormantResponsibilities(b *testing.B) {
	if b.N != 1 {
		b.Fatal("use -benchtime=1x to preserve the declared dataset")
	}
	b.StopTimer()
	const tenantCount, dormantCount, workerCount = 20, 2000, 8
	dir := b.TempDir()
	principals, tokens, identities, queued := seedDormantScale(b, dir, tenantCount, dormantCount, workerCount)
	debug.FreeOSMemory()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	s, err := OpenStore(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	replay := time.Since(started)
	debug.FreeOSMemory()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	statm, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		b.Fatal(err)
	}
	fields := strings.Fields(string(statm))
	if len(fields) < 2 {
		b.Fatal("invalid RSS sample")
	}
	rssPages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		b.Fatal(err)
	}
	rssBytes := rssPages * uint64(os.Getpagesize())
	if rssBytes > 768<<20 {
		b.Fatalf("declared RSS budget exceeded: %.1f MiB", float64(rssBytes)/(1<<20))
	}
	liveMiB := float64(after.HeapAlloc) / (1 << 20)
	deltaMiB := float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / (1 << 20)
	if replay > 15*time.Second || after.HeapAlloc > 512<<20 {
		b.Fatalf("declared replay budget exceeded: %s heap %.1f MiB", replay, liveMiB)
	}
	latencies := make([]time.Duration, 0, workerCount)
	b.StartTimer()
	for i := range workerCount {
		start := time.Now()
		receipt, err := s.WorkerClaim(tokens[i], structuredworker.Claim{Identity: identities[i], RequestID: fmt.Sprintf("scale-claim-%d", i), RunID: queued[i]})
		if err != nil || receipt.Binding == nil {
			b.Fatal("scale claim", receipt, err)
		}
		latencies = append(latencies, time.Since(start))
	}
	b.StopTimer()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := latencies[len(latencies)-1]
	total := time.Duration(0)
	for _, latency := range latencies {
		total += latency
	}
	if p95 > 250*time.Millisecond {
		b.Fatalf("declared claim budget exceeded: %s", p95)
	}
	s.mu.RLock()
	active := len(s.workerActiveLocked())
	dormant := 0
	for _, task := range s.tasks {
		if task.Status == "dormant" {
			dormant++
		}
	}
	s.mu.RUnlock()
	if active != workerCount || dormant != dormantCount || s.RuntimeCapacity() != workerCount {
		b.Fatal("dormant count confused with execution capacity", active, dormant, s.RuntimeCapacity())
	}
	page, err := s.As(principals[0]).RunPage(0, "", 50)
	if err != nil {
		b.Fatal(err)
	}
	raw, err := json.Marshal(page.Runs)
	if err != nil {
		b.Fatal(err)
	}
	if len(page.Runs) != 50 || len(raw) > ReadPageBytes {
		b.Fatal("declared read bound exceeded", len(page.Runs), len(raw))
	}
	events, err := s.As(principals[0]).EventPage(page.Runs[0].ID, 0, 200)
	if err != nil {
		b.Fatal(err)
	}
	eventBytes, _ := json.Marshal(events.Events)
	if len(events.Events) > 200 || len(eventBytes) > ReadPageBytes {
		b.Fatal("event read bound exceeded")
	}
	b.ReportMetric(float64(tenantCount), "tenants")
	b.ReportMetric(float64(dormant), "dormant_tasks")
	b.ReportMetric(float64(active), "active_workers")
	b.ReportMetric(float64(replay.Microseconds())/1000, "replay_ms")
	b.ReportMetric(liveMiB, "heap_live_MiB")
	b.ReportMetric(float64(rssBytes)/(1<<20), "rss_MiB")
	b.ReportMetric(deltaMiB, "replay_heap_MiB")
	b.ReportMetric(float64(total.Microseconds())/workerCount, "claim_mean_us")
	b.ReportMetric(float64(p95.Microseconds()), "claim_p95_us")
	b.ReportMetric(float64(len(page.Runs)), "read_items")
	b.ReportMetric(float64(len(raw)), "read_bytes")
	b.ReportMetric(float64(len(events.Events)), "event_items")
	b.ReportMetric(float64(len(eventBytes)), "event_bytes")
	b.Logf("control plane: %s %s/%s GOMAXPROCS=%d; budgets replay<=15s live Go heap<=512MiB RSS<=768MiB claim p95<=250ms; 20 tenants, 2000 dormant Tasks, 8 active worker identities", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0))
}

func seedDormantScale(b *testing.B, dir string, tenantCount, dormantCount, workerCount int) ([]decision.Principal, []string, []structuredworker.Identity, []string) {
	b.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	if err = s.ensureHome(); err != nil {
		b.Fatal(err)
	}
	policy := defaultRuntimePolicy()
	policy.Capacity = workerCount
	if err = s.ConfigureRuntimePolicy(policy); err != nil {
		b.Fatal(err)
	}
	principals := []decision.Principal{}
	for i := range tenantCount {
		_, token, err := s.CreateServicePrincipal(ServicePrincipalInput{ID: fmt.Sprintf("scale-principal-%d", i), Name: "Scale tenant", Tenant: decision.Tenant{Org: fmt.Sprintf("tenant-%d", i), Project: "scale"}, Read: true, Write: true, Agents: []string{"mitos.generalist"}, ExpiresAt: time.Now().Add(24 * time.Hour)}, decision.LocalOperator())
		if err != nil {
			b.Fatal(err)
		}
		p, err := s.AuthenticateServicePrincipal(token)
		if err != nil {
			b.Fatal(err)
		}
		principals = append(principals, p)
	}
	tokens := []string{}
	identities := []structuredworker.Identity{}
	for i := range workerCount {
		id := fmt.Sprintf("scale-worker-%d", i)
		_, token, err := s.CreateWorkerCredential(WorkerCredential{ID: id, Pools: []string{"interactive"}, Agents: []string{"mitos.generalist"}, ExpiresAt: time.Now().Add(24 * time.Hour)}, decision.LocalOperator())
		if err != nil {
			b.Fatal(err)
		}
		identity := structuredworker.Identity{ID: id, Incarnation: "scale-incarnation"}
		if _, err = s.RegisterWorker(token, structuredworker.Register{RequestID: "register-scale", Incarnation: identity.Incarnation, Capabilities: []string{"mitos.generalist"}, Features: structuredworker.Features}); err != nil {
			b.Fatal(err)
		}
		tokens = append(tokens, token)
		identities = append(identities, identity)
	}
	library, err := agents.Library()
	if err != nil {
		b.Fatal(err)
	}
	var definition agents.Definition
	for _, d := range library {
		if d.ID == "mitos.generalist" {
			definition = d
		}
	}
	if definition.ID == "" {
		b.Fatal("scale definition unavailable")
	}
	const schema = `{"type":"object","additionalProperties":false,"required":["reply"],"properties":{"reply":{"type":"string","maxLength":128}}}`
	queued := []string{}
	for i := range dormantCount + workerCount {
		brief := Brief{Title: "Dormant responsibility", Intent: "Wait for the next typed answer.", AppID: "agent:mitos.generalist", TimeoutMinutes: 1, Structured: &StructuredWork{Key: fmt.Sprintf("scale-task-%d", i), Input: json.RawMessage(`{"message":"wait"}`), ResultSchema: json.RawMessage(schema), CheckpointSchema: json.RawMessage(schema), Frozen: &definition, Definition: fmt.Sprintf("%s@%d", definition.ID, definition.Revision), Digest: definition.Digest, RequestHash: fmt.Sprintf("scale-input-%d", i)}}
		if err = brief.Validate(); err != nil {
			b.Fatal(err)
		}
		s.mu.Lock()
		r, err := s.createWithPrincipalLocked(brief, definition.Name, "", false, nil, nil, nil, principals[i%tenantCount])
		s.mu.Unlock()
		if err != nil {
			b.Fatal(err)
		}
		if i >= dormantCount {
			queued = append(queued, r.ID)
			continue
		}
		receipt, err := s.WorkerClaim(tokens[0], structuredworker.Claim{Identity: identities[0], RequestID: fmt.Sprintf("seed-claim-%d", i), RunID: r.ID})
		if err != nil || receipt.Binding == nil {
			b.Fatal("seed claim", i, receipt, err)
		}
		data := json.RawMessage(`{"cursor":0,"checkpoint":{"reply":"waiting"}}`)
		if _, err = s.WorkerMutate(tokens[0], structuredworker.Mutation{Binding: *receipt.Binding, RequestID: fmt.Sprintf("seed-wait-%d", i), Kind: "wait", Data: data}); err != nil {
			b.Fatal(err)
		}
	}
	return principals, tokens, identities, queued
}
