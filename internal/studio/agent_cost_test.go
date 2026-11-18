package studio

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/agents"
	"mitos/internal/harness"
	"mitos/internal/structuredworker"
)

func TestAgentUsageRecordPricesCacheSplits(t *testing.T) {
	tracker := harness.NewUsageTracker("claude-sonnet-4-5")
	// A Claude session: two per-call reports, cache reads grow between calls.
	tracker.Observe(harness.Usage{Model: "claude-sonnet-4-5-20250929", InputTokens: 500, CacheWriteTokens: 20_000, CachedInputTokens: 30_000, OutputTokens: 700})
	tracker.Observe(harness.Usage{Model: "claude-sonnet-4-5-20250929", InputTokens: 200, CacheWriteTokens: 0, CachedInputTokens: 52_000, OutputTokens: 300})
	record, ok := newAgentUsageRecord("mitos.engineer", "Engineer", "claude", tracker)
	if !ok {
		t.Fatal("reported usage must produce a record")
	}
	if record.Model != "claude-sonnet-4-5-20250929" || !record.Priced || record.CostUSD == nil {
		t.Fatalf("bad record: %+v", record)
	}
	// (500+200)*3 + 82000*0.30 + 20000*3.75 + 1000*15 = 101,510 / 1M tokens.
	want := 0.0007*3 + 0.082*0.30 + 0.02*3.75 + 0.001*15
	if diff := *record.CostUSD - want; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("cost %v, want %v", *record.CostUSD, want)
	}
	if !strings.Contains(record.summary(), "no published list price") == false {
		// priced records describe the estimate, not a missing price
		if !strings.Contains(record.summary(), "estimated $") {
			t.Fatalf("summary lost the estimate: %s", record.summary())
		}
	}
	if record.UnitPrice == nil || record.UnitPrice.Input != 3 {
		t.Fatal("unit price not attached")
	}
}

func TestAgentUsageRecordWithoutReportsOrPrice(t *testing.T) {
	if _, ok := newAgentUsageRecord("mitos.engineer", "", "pi", harness.NewUsageTracker("")); ok {
		t.Fatal("an attempt with no usage report must not record cost evidence")
	}
	tracker := harness.NewUsageTracker("aider/fixture")
	tracker.Observe(harness.Usage{InputTokens: 1, OutputTokens: 1})
	record, ok := newAgentUsageRecord("mitos.engineer", "", "pi", tracker)
	if !ok || record.Priced || record.CostUSD != nil {
		t.Fatalf("unpriced model must record tokens without a cost: %+v %v", record, ok)
	}
	if !strings.Contains(record.summary(), "no published list price") {
		t.Fatalf("summary must be honest about missing prices: %s", record.summary())
	}
}

func TestCodexCumulativeReportsDoNotDoubleCount(t *testing.T) {
	tracker := harness.NewUsageTracker("gpt-6.2-sol")
	first, ok := harness.CodexUsage("thread/tokenUsage/updated", json.RawMessage(`{"tokenUsage":{"total":{"inputTokens":5000,"cachedInputTokens":3000,"cacheWriteInputTokens":0,"outputTokens":900,"totalTokens":5900}}}`))
	if !ok {
		t.Fatal("usage notification must parse")
	}
	tracker.Observe(first)
	second, _ := harness.CodexUsage("thread/tokenUsage/updated", json.RawMessage(`{"tokenUsage":{"total":{"inputTokens":7000,"cachedInputTokens":5000,"cacheWriteInputTokens":0,"outputTokens":1400,"totalTokens":8400}}}`))
	tracker.Observe(second)
	model, usage, ok := tracker.Snapshot()
	if !ok || model != "gpt-6.2-sol" || usage.InputTokens != 2000 || usage.CachedInputTokens != 5000 || usage.OutputTokens != 1400 {
		t.Fatalf("cumulative totals replaced incorrectly: %s %+v", model, usage)
	}
}

func TestReportAgentCostsFoldsJournalEvidence(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	tracker := harness.NewUsageTracker("gpt-6.2-sol")
	tracker.Observe(harness.Usage{InputTokens: 1_000_000, CachedInputTokens: 2_000_000, OutputTokens: 500_000})
	record, ok := newAgentUsageRecord("mitos.engineer", "Engineer", "codex", tracker)
	if !ok {
		t.Fatal("record expected")
	}
	if err := s.RecordEvidence(r.ID, "agent_usage_recorded", record.summary(), record); err != nil {
		t.Fatal(err)
	}
	// A second agent runs an unpriced model; its tokens count, its cost does not.
	r2 := createRun(t, s)
	other := harness.NewUsageTracker("qwen3-max")
	other.Observe(harness.Usage{InputTokens: 10, OutputTokens: 10})
	unpriced, _ := newAgentUsageRecord("mitos.researcher", "Researcher", "codex", other)
	if unpriced.Priced {
		t.Fatal("fixture must be unpriced")
	}
	if err := s.RecordEvidence(r2.ID, "agent_usage_recorded", unpriced.summary(), unpriced); err != nil {
		t.Fatal(err)
	}
	report := ReportAgentCosts(s.List())
	if report.Runs != 2 || len(report.Agents) != 2 {
		t.Fatalf("bad fold: %+v", report)
	}
	if report.Agents[0].AgentID != "mitos.engineer" || report.Agents[0].Runs != 1 || len(report.Agents[0].Models) != 1 {
		t.Fatalf("bad agent aggregate: %+v", report.Agents[0])
	}
	// 1M*2 + 2M*0.10 + 0.5M*10 = 7.20 USD.
	if diff := report.TotalCostUSD - 7.20; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("total %v, want 7.20", report.TotalCostUSD)
	}
	if report.PricedRuns != 1 || report.UnpricedRuns != 1 || len(report.UnpricedModels) != 1 || report.UnpricedModels[0] != "qwen3-max" {
		t.Fatalf("pricing split wrong: %+v", report)
	}
	if report.PricesRetrieved == "" || len(report.PriceSources) != 3 || len(report.Limitations) == 0 {
		t.Fatal("report must carry its price provenance and limitations")
	}
	// The structured worker result path journals the same evidence shape.
	frozen := agents.Definition{ID: "mitos.tester", Name: "Tester"}
	usage := &harness.Usage{Model: "claude-haiku-4-5", InputTokens: 100_000, CachedInputTokens: 900_000, OutputTokens: 50_000}
	change, ok := structuredUsageChange(&Run{Brief: Brief{Structured: &StructuredWork{Frozen: &frozen}}}, usage)
	if !ok || change.Evidence == nil {
		t.Fatal("worker usage change expected")
	}
	if !strings.Contains(change.Summary, "Tester") || !strings.Contains(change.Summary, "estimated $") {
		t.Fatalf("worker usage summary lost detail: %s", change.Summary)
	}
	if _, ok := structuredUsageChange(&Run{}, nil); ok {
		t.Fatal("missing usage records nothing")
	}
}

func TestStructuredWorkerResultJournalsUsageCost(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	token, identity := testWorker(t, e, "cost-worker")
	r, err := e.LaunchStructured(structuredBrief("cost-input", "agent:mitos.generalist"))
	if err != nil {
		t.Fatal(err)
	}
	binding := testWorkerClaim(t, e, token, identity, r.ID, "cost-claim")
	body, _ := json.Marshal(structuredworker.Result{
		Value: json.RawMessage(`{"answer":42,"confidence":"high","tags":[]}`),
		Usage: &harness.Usage{Model: "gpt-6.1-sol", InputTokens: 40_000, CachedInputTokens: 160_000, OutputTokens: 3_000},
	})
	receipt, err := e.Store.WorkerMutate(token, structuredworker.Mutation{Binding: binding, RequestID: "cost-result", Kind: "result", Data: body})
	if err != nil || receipt.State != "accepted" {
		t.Fatal("accept", receipt, err)
	}
	finished, err := e.Store.Get(r.ID)
	if err != nil || finished.Status != "done" {
		t.Fatal("result not accepted", finished.Status, err)
	}
	var sawUsage bool
	for _, event := range finished.Events {
		if event.Kind != "agent_usage_recorded" || event.Type != "effect" {
			continue
		}
		sawUsage = true
		var payload struct {
			Evidence AgentUsageRecord `json:"evidence"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatal(err)
		}
		record := payload.Evidence
		if record.AgentID != "mitos.generalist" || record.Runtime != "codex" || record.Model != "gpt-6.1-sol" || !record.Priced || record.CostUSD == nil {
			t.Fatalf("bad cost evidence: %+v", record)
		}
		// 40000*2 + 160000*0.10 + 3000*10 per 1M tokens = 0.126 USD.
		if diff := *record.CostUSD - 0.126; diff < -1e-9 || diff > 1e-9 {
			t.Fatalf("cost %v, want 0.126", *record.CostUSD)
		}
	}
	if !sawUsage {
		t.Fatal("accepted result with usage journaled no cost evidence")
	}
	report := ReportAgentCosts(e.Store.List())
	if report.Runs != 1 || len(report.Agents) != 1 || report.Agents[0].AgentID != "mitos.generalist" {
		t.Fatalf("report fold missing worker usage: %+v", report)
	}
	if diff := report.TotalCostUSD - 0.126; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("report total %v, want 0.126", report.TotalCostUSD)
	}
}
