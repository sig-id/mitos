package studio

// The handoff contract is shared by native tools, HTTP, journal replay and UI.
// Natural language output is evidence only; it is never parsed as a submission.
import (
	"encoding/json"
	"errors"
	"fmt"
	"mitos/internal/cooperation"
	"mitos/internal/harness"
	"reflect"
	"slices"
	"time"
)

const HandoffVersion = "mitos.handoff/v1"
const HandoffReadLimit = 32 << 10
const HandoffResponseLimit = 64 << 10
const HandoffRetrievalBudget = 2 << 20
const handoffArtifactLimit = 4096

// Proposal limits. Each review costs an orchestrator call, so one worker cannot
// starve planning with a stream of proposals or yield cycles.
const (
	HandoffProposalsPerTask = 4 // proposals across all attempts of one task
	HandoffTasksPerProposal = 8 // task specs in one proposal
	HandoffYieldsPerTask    = 2 // blocking yields (continuations) per task
	handoffProposalBytes    = 16 << 10
)

// ProposalCapability gates proposals on result_submit. Without it the field is
// absent from the advertised schema and a result carrying proposals is refused.
const ProposalCapability = "work.propose"

type ResultArtifact struct {
	ArtifactID string `json:"artifact_id"`
	Role       string `json:"role"`
}
type ResultCheck struct {
	Name     string   `json:"name"`
	Outcome  string   `json:"outcome"`
	Evidence []string `json:"evidence,omitempty"`
}
type ResultSubmission struct {
	Version            string           `json:"version"`
	Key                string           `json:"key"`
	Summary            string           `json:"summary"`
	Artifacts          []ResultArtifact `json:"artifacts,omitempty"`
	Checks             []ResultCheck    `json:"checks,omitempty"`
	Limitations        []string         `json:"limitations,omitempty"`
	UnresolvedRequests []string         `json:"unresolved_requests,omitempty"`
	Proposals          []ResultProposal `json:"proposals,omitempty"`
}

// ResultProposal asks the orchestrator for new work as part of ending an
// attempt. Workers never create tasks: the specs use the orchestrator's add
// shape, and the orchestrator accepts, reshapes or rejects them. A blocking
// proposal makes the attempt yield: it releases its slot and is continued in
// the same native conversation once the proposal settles.
type ResultProposal struct {
	ID        string      `json:"id"`
	Rationale string      `json:"rationale"`
	Blocking  bool        `json:"blocking"`
	Tasks     []GraphSpec `json:"tasks"`
	Artifacts []string    `json:"artifacts,omitempty"`
}
type ResultReceipt struct {
	ID         string           `json:"id"`
	ProjectID  string           `json:"project_id"`
	TaskID     string           `json:"task_id"`
	RunID      string           `json:"run_id"`
	Revision   int              `json:"revision"`
	Seq        int64            `json:"seq"`
	At         time.Time        `json:"at"`
	Digest     string           `json:"digest"`
	Submission ResultSubmission `json:"submission"`
	// ProposalIDs are kernel identities, one per submitted proposal in order.
	ProposalIDs []string `json:"proposal_ids,omitempty"`
}
type ArtifactDescriptor struct {
	ID           string `json:"id"`
	RunID        string `json:"run_id"`
	ResultID     string `json:"result_id,omitempty"`
	TaskID       string `json:"task_id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	MediaType    string `json:"media_type"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256"`
	Availability string `json:"availability"`
	Retention    string `json:"retention"`
}
type ArtifactRegister struct {
	Path string `json:"path"`
	Role string `json:"role"`
}
type ArtifactList struct {
	RunID  string `json:"run_id"`
	Cursor int    `json:"cursor"`
	Limit  int    `json:"limit"`
}

// GraphTaskRead pages bounded task detail from the caller's graph. The inline
// planning journal carries states only; specs, criteria and artifact lists are
// retrieved through this tool by orchestrator attempts.
type GraphTaskRead struct {
	Task   string `json:"task"`
	Cursor int    `json:"cursor"`
	Limit  int    `json:"limit"`
}
type ArtifactRead struct {
	RunID      string `json:"run_id"`
	ArtifactID string `json:"artifact_id"`
	Offset     int64  `json:"offset"`
	Limit      int    `json:"limit"`
}
type ArtifactSearch struct {
	RunID      string `json:"run_id"`
	ArtifactID string `json:"artifact_id"`
	Query      string `json:"query"`
	Cursor     int64  `json:"cursor"`
	Limit      int    `json:"limit"`
}
type ArtifactPage struct {
	RetrievalBytesUsed  int64                `json:"retrieval_bytes_used"`
	RetrievalBytesLimit int64                `json:"retrieval_bytes_limit"`
	Artifacts           []ArtifactDescriptor `json:"artifacts"`
	Next                *int                 `json:"next_cursor"`
}
type ArtifactRange struct {
	RetrievalBytesUsed  int64              `json:"retrieval_bytes_used"`
	RetrievalBytesLimit int64              `json:"retrieval_bytes_limit"`
	Artifact            ArtifactDescriptor `json:"artifact"`
	Offset              int64              `json:"offset"`
	Data                []byte             `json:"data"` // base64 encodes exact bytes, including split UTF-8 and binary files
	Next                *int64             `json:"next_offset"`
	Complete            bool               `json:"complete"`
}
type ArtifactMatches struct {
	RetrievalBytesUsed  int64              `json:"retrieval_bytes_used"`
	RetrievalBytesLimit int64              `json:"retrieval_bytes_limit"`
	Artifact            ArtifactDescriptor `json:"artifact"`
	Offsets             []int64            `json:"offsets"`
	Next                *int64             `json:"next_cursor"`
	Complete            bool               `json:"complete"`
}
type HandoffFailure struct {
	Code     string `json:"code"`
	Position int64  `json:"position"`
	Detail   string `json:"detail"`
}

func (e *HandoffFailure) Error() string { return e.Code + ": " + e.Detail }
func handoffError(code string, pos int64) error {
	return &HandoffFailure{Code: code, Position: pos, Detail: code}
}

func validArtifactRole(role string) bool {
	return slices.Contains([]string{"report", "log", "file", "patch", "test_evidence", "publication_receipt"}, role)
}

func HandoffTools() []harness.DynamicTool {
	specs := []struct {
		name, description string
		typ               reflect.Type
	}{
		{"result_get", "Read a typed result receipt, review state, artifact references, checks, limitations and unresolved requests in bounded pages. Use cursor 0 and limit 1..16. Review all pages before accepting.", reflect.TypeFor[ResultRead]()},
		{"graph_read", "Read bounded task detail (spec, criteria, brief, scheduling, current run, result, first artifacts) from your graph. Use task for one member, or cursor 0 and limit 1..16 to page. The inline planning journal carries immutable states only.", reflect.TypeFor[GraphTaskRead]()},
		{"cooperation_submit", "Record a mitos.cooperation/v2 request, answer or cancellation. Resolver IDs bind exact current attempts in this graph. Only acceptance blocking is supported. Approval messages reference canonical decisions and grant no permissions.", reflect.TypeFor[CooperationSubmission]()},
		{"cooperation_list", "List your requests and requests addressed to your exact attempt. Use cursor 0 and limit 1..16. Re-list from 0 to refresh request states.", reflect.TypeFor[CooperationList]()},
		{"artifact_register", "Retain a regular workspace file as immutable evidence. Returns its artifact ID. Maximum 64 MiB per file.", reflect.TypeFor[ArtifactRegister]()},
		{"artifact_list", "List retained artifacts for your run or an explicitly granted producer run. Use cursor 0 and limit 1..32. Large artifacts remain discoverable.", reflect.TypeFor[ArtifactList]()},
		{"artifact_read", "Read exact bytes (base64 data), at offset with limit 1..32768. next_offset is null only at EOF. A 2 MiB cumulative retrieval budget applies per attempt; this is not a token budget.", reflect.TypeFor[ArtifactRead]()},
		{"artifact_search", "Search a literal UTF-8 query (1..256 bytes), returning byte offsets. Use cursor 0 and limit 1..32; follow next_cursor even when no matches are returned.", reflect.TypeFor[ArtifactSearch]()},
		{"result_submit", "Submit one immutable typed result with version mitos.handoff/v1, an idempotency key, <=2 KiB summary, registered artifact IDs, checks (passed/failed/not_run), limitations and unresolved request IDs. Evidence lists (artifacts, checks, evidence IDs) are optional: omit them or send an empty list when none. Submission is not acceptance. This ends your attempt's work. Do not spawn subagents: to delegate, add proposals (unique id, rationale, 1-8 application task specs in the orchestrator add shape, optional registered artifact IDs as evidence; omit the artifacts field when none). Blocking is judgment-gated for task natures that normally may not yield (review, context): such a submission is either held for a decision (the response has held=kernel_judgment: stop after submitting; you resume in this conversation with the outcome) or refused with the reason (then resubmit with blocking=false to record the follow-up work). A blocking proposal means you yield: stop after submitting; you are resumed in this conversation with the accepted results or the rejection rationale. Non-blocking proposals are follow-up work you noticed; the result is otherwise final. Omit proposals or send an empty list when you need nothing.", reflect.TypeFor[ResultSubmission]()},
	}
	out := []harness.DynamicTool{}
	for _, spec := range specs {
		schema, _ := json.Marshal(orchestratorTypeSchema(spec.typ))
		out = append(out, harness.DynamicTool{Type: "function", Name: spec.name, Description: spec.description, InputSchema: schema})
	}
	return append(out, watchHandoffTools()...)
}
func decodeHandoff(raw json.RawMessage, v any) error {
	if len(raw) > 1<<20 {
		return handoffError("body_limit", 0)
	}
	return DecodeGraphJSON(raw, v)
}

func (e *Engine) HandoffCall(session, token, method string, raw json.RawMessage) (any, error) {
	s := e.Store
	s.mu.RLock()
	a, err := s.agentSession(session, token)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if a.RunID == "" {
		return nil, handoffError("run_binding_required", 0)
	}
	return e.handoffCall(a.RunID, a.Workspace, method, raw)
}
func (e *Engine) handoffCall(run, workspace, method string, raw json.RawMessage) (any, error) {
	switch method {
	case "result_get":
		var in ResultRead
		if err := decodeHandoff(raw, &in); err != nil {
			return nil, err
		}
		return e.Store.readResult(run, in)
	case "cooperation_submit":
		return e.handoffCooperation(run, raw)
	case "cooperation_list":
		return e.handoffRequests(run, raw)
	case "graph_read":
		var in GraphTaskRead
		if err := decodeHandoff(raw, &in); err != nil {
			return nil, err
		}
		return e.Store.graphTaskRead(run, in)
	case "artifact_register":
		var in ArtifactRegister
		if err := decodeHandoff(raw, &in); err != nil {
			return nil, err
		}
		return e.Store.registerArtifact(run, workspace, in)
	case "result_submit":
		var in ResultSubmission
		if err := decodeHandoff(raw, &in); err != nil {
			return nil, err
		}
		if len(in.Proposals) > 0 && !e.Store.runGranted(run, ProposalCapability) {
			return nil, &HandoffFailure{Code: "capability_not_granted", Detail: "proposals require the " + ProposalCapability + " capability"}
		}
		if err := e.validateProposalExecution(run, in.Proposals); err != nil {
			return nil, err
		}
		receipt, err := e.Store.submitResult(run, in)
		if err != nil {
			var required *judgmentRequired
			if errors.As(err, &required) {
				return e.judgeSubmission(run, in, required)
			}
			var held *heldJudgment
			if errors.As(err, &held) {
				return map[string]any{"held": "kernel_judgment", "judgment": held.judgment.ID, "code": held.judgment.Code, "detail": held.judgment.Detail, "note": "The submission is held for a judgment, not refused. Stop after submitting; this attempt continues with the judgment outcome."}, nil
			}
			return nil, err
		}
		return map[string]any{"id": receipt.ID, "run_id": receipt.RunID, "revision": receipt.Revision, "seq": receipt.Seq, "at": receipt.At, "digest": receipt.Digest, "proposal_ids": receipt.ProposalIDs}, nil
	case "watch_create", "watch_renew", "watch_cancel", "watch_inbox", "delivery_outcome":
		return e.handoffWatch(run, method, raw)
	case "artifact_list":
		var in ArtifactList
		if err := decodeHandoff(raw, &in); err != nil {
			return nil, err
		}
		return e.Store.listArtifacts(run, in)
	case "artifact_read":
		var in ArtifactRead
		if err := decodeHandoff(raw, &in); err != nil {
			return nil, err
		}
		return e.Store.artifactRange(run, in, nil)
	case "artifact_search":
		var in ArtifactSearch
		if err := decodeHandoff(raw, &in); err != nil {
			return nil, err
		}
		if len(in.Query) == 0 || len(in.Query) > 256 || in.Limit < 1 || in.Limit > 32 {
			return nil, handoffError("invalid_search", in.Cursor)
		}
		return e.Store.artifactRange(run, ArtifactRead{RunID: in.RunID, ArtifactID: in.ArtifactID, Offset: in.Cursor, Limit: HandoffReadLimit}, &in)
	default:
		return nil, fmt.Errorf("unsupported handoff operation %q", method)
	}
}

// RunEvidence is the local operator's read surface, scoped to the selected run.
// Reads share integrity/range semantics, but do not consume an agent's allowance.
func (e *Engine) RunEvidence(project, run, method string, raw json.RawMessage) (any, error) {
	r, err := e.Store.Get(run)
	if err != nil || r.ProjectID != project {
		return nil, ErrNotFound
	}
	if method != "artifact_list" && method != "artifact_read" && method != "artifact_search" && method != "result_get" {
		return nil, handoffError("read_only", 0)
	}
	var target struct {
		RunID string `json:"run_id"`
	}
	if json.Unmarshal(raw, &target) != nil || target.RunID != run {
		return nil, handoffError("forbidden", 0)
	}
	// Operator reads are a separate consumer principal, not an agent budget debit.
	if method == "artifact_list" || method == "result_get" {
		return e.handoffCall(run, "", method, raw)
	}
	if method == "artifact_read" {
		var in ArtifactRead
		if err := decodeHandoff(raw, &in); err != nil {
			return nil, err
		}
		return e.Store.artifactRange("operator:"+run, in, nil)
	}
	var in ArtifactSearch
	if err := decodeHandoff(raw, &in); err != nil {
		return nil, err
	}
	if len(in.Query) == 0 || len(in.Query) > 256 || in.Limit < 1 || in.Limit > 32 {
		return nil, handoffError("invalid_search", in.Cursor)
	}
	return e.Store.artifactRange("operator:"+run, ArtifactRead{RunID: run, ArtifactID: in.ArtifactID, Offset: in.Cursor, Limit: HandoffReadLimit}, &in)
}

// Tool-facing messages exclude producer fields; the authenticated native session
// supplies those. The local operator HTTP endpoint deliberately carries full IDs.
type CooperationSubmission struct {
	Version            string                      `json:"version"`
	ID                 string                      `json:"id"`
	Kind               string                      `json:"kind"`
	InReplyTo          string                      `json:"in_reply_to"`
	Body               string                      `json:"body"`
	Artifacts          []string                    `json:"artifacts"`
	PermittedResolvers []cooperation.Resolver      `json:"permitted_resolvers"`
	BlockedActions     []cooperation.BlockedAction `json:"blocked_actions"`
	ExpiresAt          *time.Time                  `json:"expires_at"`
	DecisionID         string                      `json:"decision_id"`
}
type CooperationList struct {
	Cursor int64 `json:"cursor"`
	Limit  int   `json:"limit"`
}

func (e *Engine) handoffCooperation(run string, raw json.RawMessage) (any, error) {
	var in CooperationSubmission
	if err := decodeHandoff(raw, &in); err != nil {
		return nil, err
	}
	r, err := e.Store.Get(run)
	if err != nil {
		return nil, err
	}
	t, err := e.Store.Task(r.TaskID)
	if err != nil {
		return nil, err
	}
	return e.SubmitCooperation(r.ProjectID, cooperation.Message{Version: in.Version, ID: in.ID, TaskID: r.TaskID, RunID: r.ID, Revision: t.Revision, Kind: in.Kind, InReplyTo: in.InReplyTo, Body: in.Body, Artifacts: in.Artifacts, PermittedResolvers: in.PermittedResolvers, BlockedActions: in.BlockedActions, ExpiresAt: in.ExpiresAt, DecisionID: in.DecisionID})
}
func (e *Engine) handoffRequests(run string, raw json.RawMessage) (any, error) {
	var in CooperationList
	if err := decodeHandoff(raw, &in); err != nil {
		return nil, err
	}
	if in.Cursor < 0 || in.Limit < 1 || in.Limit > 16 {
		return nil, handoffError("invalid_page", in.Cursor)
	}
	r, err := e.Store.Get(run)
	if err != nil {
		return nil, err
	}
	records := cooperationVisibleRecords(e.Store.Cooperation(r.ProjectID), run)
	slices.SortFunc(records, func(a, b CooperationRecord) int {
		if a.Seq < b.Seq {
			return -1
		}
		if a.Seq > b.Seq {
			return 1
		}
		return 0
	})
	selected := []CooperationRecord{}
	next := in.Cursor
	for _, rec := range records {
		if rec.Seq <= in.Cursor {
			continue
		}

		candidate := append(append([]CooperationRecord{}, selected...), rec)
		body, _ := json.Marshal(candidate)
		if len(body) > 48<<10 || len(candidate) > in.Limit {
			break
		}
		selected = candidate
		next = rec.Seq
	}
	return map[string]any{"messages": selected, "cursor": next, "poll_from": 0, "note": "Re-list from zero to refresh expiry and supersession states; cursor advances immutable events only."}, nil
}

type ResultRead struct {
	RunID  string `json:"run_id"`
	Cursor int    `json:"cursor"`
	Limit  int    `json:"limit"`
}
type ResultEntry struct {
	Kind     string          `json:"kind"`
	Artifact *ResultArtifact `json:"artifact,omitempty"`
	Check    *ResultCheck    `json:"check,omitempty"`
	Text     string          `json:"text,omitempty"`
	Proposal *ProposalEntry  `json:"proposal,omitempty"`
}

// ProposalEntry is a submitted proposal with its kernel identity and review.
type ProposalEntry struct {
	ID      string         `json:"id"`
	Outcome string         `json:"outcome"`
	Detail  ResultProposal `json:"detail"`
}
type ResultPage struct {
	ID       string        `json:"id"`
	RunID    string        `json:"run_id"`
	TaskID   string        `json:"task_id"`
	Revision int           `json:"revision"`
	Seq      int64         `json:"seq"`
	At       time.Time     `json:"at"`
	Summary  string        `json:"summary"`
	State    string        `json:"state"`
	Entries  []ResultEntry `json:"entries"`
	Next     *int          `json:"next_cursor"`
}
