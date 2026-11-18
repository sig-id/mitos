package studio

import (
	"encoding/json"
	"fmt"
	"sort"

	"mitos/internal/harness"
	"mitos/internal/modelcost"
)

// Agent cost statistics are estimates from harness-reported token usage against
// published list prices. They are evidence about spending, never a spend
// guarantee or an enforced budget: subscription plans, batch/flex/priority
// rates, and provider-side reroutes can change the real invoice either way.

// AgentUsageRecord is the journal evidence for one attempt's model spend.
// Tokens keep the cache hit/write split; CostUSD is present only when every
// reported token of the resolved model has a published list price.
type AgentUsageRecord struct {
	AgentID           string           `json:"agent_id"`
	AgentName         string           `json:"agent_name,omitempty"`
	Runtime           string           `json:"runtime"`
	Model             string           `json:"model"`
	InputTokens       int64            `json:"input_tokens"`
	CachedInputTokens int64            `json:"cached_input_tokens"`
	CacheWriteTokens  int64            `json:"cache_write_tokens"`
	OutputTokens      int64            `json:"output_tokens"`
	Priced            bool             `json:"priced"`
	CostUSD           *float64         `json:"cost_usd,omitempty"`
	UnitPrice         *modelcost.Price `json:"unit_price,omitempty"`
	PriceRetrieved    string           `json:"price_retrieved,omitempty"`
}

func newAgentUsageRecord(agentID, agentName, runtime string, t *harness.UsageTracker) (AgentUsageRecord, bool) {
	model, usage, ok := t.Snapshot()
	if !ok {
		return AgentUsageRecord{}, false
	}
	record := AgentUsageRecord{
		AgentID: agentID, AgentName: agentName, Runtime: runtime, Model: model,
		InputTokens: usage.InputTokens, CachedInputTokens: usage.CachedInputTokens,
		CacheWriteTokens: usage.CacheWriteTokens, OutputTokens: usage.OutputTokens,
		PriceRetrieved: modelcost.Retrieved,
	}
	if price, ok := modelcost.Lookup(model); ok {
		cost := price.CostUSD(modelcost.Usage{
			InputTokens: record.InputTokens, CachedInputTokens: record.CachedInputTokens,
			CacheWriteTokens: record.CacheWriteTokens, OutputTokens: record.OutputTokens,
		})
		record.Priced = true
		record.CostUSD = &cost
		record.UnitPrice = &price
	}
	return record, true
}

func (r AgentUsageRecord) summary() string {
	tokens := r.InputTokens + r.CachedInputTokens + r.CacheWriteTokens + r.OutputTokens
	detail := fmt.Sprintf("%s reported %d tokens (input %d, cache hits %d, cache writes %d, output %d) on %s", displayName(r.AgentName, r.AgentID), tokens, r.InputTokens, r.CachedInputTokens, r.CacheWriteTokens, r.OutputTokens, r.Model)
	if r.Priced {
		return detail + fmt.Sprintf("; estimated $%.4f at published list prices.", *r.CostUSD)
	}
	return detail + "; no published list price matched, so no estimate is recorded."
}

func displayName(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

// RecordAgentUsage journals one attempt's harness-reported usage as cost
// evidence. It is the public seam for runtimes outside the graph adapters;
// attempts whose harness reported nothing record nothing.
func (s *Store) RecordAgentUsage(id, agentID, agentName, runtime string, t *harness.UsageTracker) error {
	record, ok := newAgentUsageRecord(agentID, agentName, runtime, t)
	if !ok {
		return nil
	}
	return s.RecordEvidence(id, "agent_usage_recorded", record.summary(), record)
}

// recordStructuredUsage journals worker-reported usage as cost evidence for a
// structured attempt. Best effort by design: telemetry must not change the
// outcome of the attempt that produced it, so only a store failure is
// surfaced and missing usage records nothing.
func recordStructuredUsage(s *Store, r Run, usage *harness.Usage) error {
	if usage == nil {
		return nil
	}
	frozen := r.Brief.Structured.Frozen
	if frozen == nil {
		return nil
	}
	tracker := harness.NewUsageTracker("")
	tracker.Observe(*usage)
	record, ok := newAgentUsageRecord(frozen.ID, frozen.Name, "codex", tracker)
	if !ok {
		return nil
	}
	return s.RecordEvidence(r.ID, "agent_usage_recorded", record.summary(), record)
}

// structuredUsageChange builds the cost-evidence change for a worker result
// batch so the usage record commits atomically with the result it belongs to.
func structuredUsageChange(r *Run, usage *harness.Usage) (*change, bool) {
	if usage == nil || r.Brief.Structured == nil || r.Brief.Structured.Frozen == nil {
		return nil, false
	}
	frozen := r.Brief.Structured.Frozen
	tracker := harness.NewUsageTracker("")
	tracker.Observe(*usage)
	record, ok := newAgentUsageRecord(frozen.ID, frozen.Name, "codex", tracker)
	if !ok {
		return nil, false
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, false
	}
	return &change{Summary: record.summary(), Evidence: data}, true
}

// ModelCost aggregates one agent's spend on one model.
type ModelCost struct {
	Model             string   `json:"model"`
	Runs              int      `json:"runs"`
	InputTokens       int64    `json:"input_tokens"`
	CachedInputTokens int64    `json:"cached_input_tokens"`
	CacheWriteTokens  int64    `json:"cache_write_tokens"`
	OutputTokens      int64    `json:"output_tokens"`
	CostUSD           *float64 `json:"cost_usd,omitempty"`
	PricedRuns        int      `json:"priced_runs"`
	UnpricedRuns      int      `json:"unpriced_runs"`
}

// AgentCost aggregates one agent's estimated spend across its attempts.
type AgentCost struct {
	AgentID           string      `json:"agent_id"`
	AgentName         string      `json:"agent_name,omitempty"`
	Runs              int         `json:"runs"`
	InputTokens       int64       `json:"input_tokens"`
	CachedInputTokens int64       `json:"cached_input_tokens"`
	CacheWriteTokens  int64       `json:"cache_write_tokens"`
	OutputTokens      int64       `json:"output_tokens"`
	Models            []ModelCost `json:"models"`
	CostUSD           float64     `json:"cost_usd"`
	PricedRuns        int         `json:"priced_runs"`
	UnpricedRuns      int         `json:"unpriced_runs"`
}

// AgentCostReport folds journal evidence into per-agent cost statistics.
type AgentCostReport struct {
	Runs            int         `json:"runs"`
	Agents          []AgentCost `json:"agents"`
	TotalCostUSD    float64     `json:"total_cost_usd"`
	PricedRuns      int         `json:"priced_runs"`
	UnpricedRuns    int         `json:"unpriced_runs"`
	UnpricedModels  []string    `json:"unpriced_models,omitempty"`
	PricesRetrieved string      `json:"prices_retrieved"`
	PriceSources    []string    `json:"price_sources"`
	Limitations     []string    `json:"limitations"`
}

// AuditAgentCostLogs produces the same report from an exported audit corpus
// instead of the live store, so archived journals reprice identically.
func AuditAgentCostLogs(path string) (AgentCostReport, error) {
	runs, _, err := readAuditCorpus(path)
	if err != nil {
		return AgentCostReport{}, err
	}
	return ReportAgentCosts(runs), nil
}

// WorkCost is one work item's cost statistics: the sum over priced usage
// records and the count of records without a matching list price. A nil
// CostUSD with zero runs means no usage was reported at all; a nil CostUSD
// with UnpricedRuns means usage exists but nothing could be priced.
type WorkCost struct {
	CostUSD      *float64 `json:"cost_usd,omitempty"`
	Runs         int      `json:"runs"`
	UnpricedRuns int      `json:"unpriced_runs"`
}

// WorkCostIndex aggregates journaled usage cost by desk Task and by graph,
// plus the project total, for work views. It reads the same evidence as
// ReportAgentCosts, never a mutable projection.
type WorkCostIndex struct {
	Tasks  map[string]WorkCost `json:"tasks"`
	Graphs map[string]WorkCost `json:"graphs"`
	Total  WorkCost            `json:"total"`
}

// usageRecords decodes a run's journaled agent_usage_recorded evidence in
// journal order. Records are per-attempt deltas, including resumed native
// conversations whose evidence covers only newly consumed tokens.
func usageRecords(run *Run) []AgentUsageRecord {
	var records []AgentUsageRecord
	for _, event := range run.Events {
		if event.Kind != "agent_usage_recorded" || event.Type != "effect" {
			continue
		}
		var payload struct {
			Evidence AgentUsageRecord `json:"evidence"`
		}
		if json.Unmarshal(event.Data, &payload) != nil {
			continue
		}
		records = append(records, payload.Evidence)
	}
	return records
}

// foldUsage sums a run's agent_usage_recorded evidence.
func foldUsage(run *Run) (priced float64, pricedRuns, unpricedRuns int) {
	for _, record := range usageRecords(run) {
		if record.Priced && record.CostUSD != nil {
			priced += *record.CostUSD
			pricedRuns++
		} else {
			unpricedRuns++
		}
	}
	return priced, pricedRuns, unpricedRuns
}

func accumulate(cost *WorkCost, priced float64, pricedRuns, unpricedRuns int) {
	if pricedRuns > 0 {
		if cost.CostUSD == nil {
			cost.CostUSD = &priced
		} else {
			*cost.CostUSD += priced
		}
	}
	cost.Runs += pricedRuns + unpricedRuns
	cost.UnpricedRuns += unpricedRuns
}

// NativeThreadUsage sums the usage already journaled by attempts on one native
// conversation. A resumed Codex thread keeps reporting lifetime totals, so a
// continuation seeds its tracker with this baseline and records only the
// delta instead of re-billing earlier attempts.
func (s *Store) NativeThreadUsage(threadID string) harness.Usage {
	if threadID == "" {
		return harness.Usage{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var total harness.Usage
	for _, run := range s.runs {
		if run.NativeThreadID != threadID {
			continue
		}
		for _, record := range usageRecords(run) {
			total.InputTokens += record.InputTokens
			total.CachedInputTokens += record.CachedInputTokens
			total.CacheWriteTokens += record.CacheWriteTokens
			total.OutputTokens += record.OutputTokens
		}
	}
	return total
}

// WorkCosts aggregates cost statistics for one project's runs. Graph rows and
// the total share the same run set, so a graph's spend is never counted twice
// in the total even when its runs also carry a Task ID.
func (s *Store) WorkCosts(project string) WorkCostIndex {
	s.mu.RLock()
	defer s.mu.RUnlock()
	index := WorkCostIndex{Tasks: map[string]WorkCost{}, Graphs: map[string]WorkCost{}}
	for _, run := range s.runs {
		// Runs created through the plain brief path carry the project on the
		// brief rather than the projected run field.
		runProject := run.ProjectID
		if runProject == "" {
			runProject = run.Brief.ProjectID
		}
		if runProject != project {
			continue
		}
		priced, pricedRuns, unpricedRuns := foldUsage(run)
		if pricedRuns+unpricedRuns == 0 {
			continue
		}
		accumulate(&index.Total, priced, pricedRuns, unpricedRuns)
		task := index.Tasks[run.TaskID]
		accumulate(&task, priced, pricedRuns, unpricedRuns)
		index.Tasks[run.TaskID] = task
		if run.GraphAttempt != nil {
			graph := index.Graphs[run.GraphAttempt.GraphID]
			accumulate(&graph, priced, pricedRuns, unpricedRuns)
			index.Graphs[run.GraphAttempt.GraphID] = graph
		}
	}
	return index
}

// ReportAgentCosts folds recorded agent_usage_recorded evidence. It consumes
// journal payloads, never live projections, so replayed or exported histories
// produce the same report.
func ReportAgentCosts(runs []Run) AgentCostReport {
	report := AgentCostReport{PricesRetrieved: modelcost.Retrieved, PriceSources: modelcost.Sources()}
	type key struct{ agent, model string }
	models := map[key]*ModelCost{}
	agents := map[string]*AgentCost{}
	unpriced := map[string]bool{}
	for _, source := range runs {
		for _, event := range source.Events {
			if event.Kind != "agent_usage_recorded" || event.Type != "effect" {
				continue
			}
			var payload struct {
				Evidence AgentUsageRecord `json:"evidence"`
			}
			if json.Unmarshal(event.Data, &payload) != nil || payload.Evidence.AgentID == "" || payload.Evidence.Model == "" {
				continue
			}
			record := payload.Evidence
			report.Runs++
			mk := key{record.AgentID, record.Model}
			if models[mk] == nil {
				models[mk] = &ModelCost{Model: record.Model}
			}
			if agents[record.AgentID] == nil {
				agents[record.AgentID] = &AgentCost{AgentID: record.AgentID, AgentName: record.AgentName}
			}
			m, a := models[mk], agents[record.AgentID]
			if a.AgentName == "" {
				a.AgentName = record.AgentName
			}
			a.Runs++
			m.Runs++
			a.InputTokens += record.InputTokens
			a.CachedInputTokens += record.CachedInputTokens
			a.CacheWriteTokens += record.CacheWriteTokens
			a.OutputTokens += record.OutputTokens
			m.InputTokens += record.InputTokens
			m.CachedInputTokens += record.CachedInputTokens
			m.CacheWriteTokens += record.CacheWriteTokens
			m.OutputTokens += record.OutputTokens
			if record.Priced && record.CostUSD != nil {
				cost := *record.CostUSD
				a.CostUSD += cost
				report.TotalCostUSD += cost
				if m.CostUSD == nil {
					m.CostUSD = &cost
				} else {
					*m.CostUSD += cost
				}
				a.PricedRuns++
				m.PricedRuns++
				report.PricedRuns++
			} else {
				a.UnpricedRuns++
				m.UnpricedRuns++
				report.UnpricedRuns++
				unpriced[record.Model] = true
			}
		}
	}
	for k, m := range models {
		a := agents[k.agent]
		if a == nil {
			continue
		}
		a.Models = append(a.Models, *m)
	}
	for _, a := range agents {
		sort.Slice(a.Models, func(i, j int) bool { return a.Models[i].Model < a.Models[j].Model })
		report.Agents = append(report.Agents, *a)
	}
	sort.Slice(report.Agents, func(i, j int) bool { return report.Agents[i].AgentID < report.Agents[j].AgentID })
	for model := range unpriced {
		report.UnpricedModels = append(report.UnpricedModels, model)
	}
	sort.Strings(report.UnpricedModels)
	report.Limitations = []string{
		"Costs are estimates from harness-reported token counts multiplied by provider list prices retrieved " + modelcost.Retrieved + "; they are not invoices, spend limits, or guarantees.",
		"Cache hits use the published cached-input rate, cache writes the provider's write premium where one exists, and remaining input the base rate; long-context tiers, batch, flex, priority, and regional rates are not modeled.",
		"Runs whose harness reported no token usage record no cost evidence; absence is unknown spend, not zero spend.",
		"Unpriced models record token counts without a cost estimate rather than an assumed price.",
	}
	return report
}
