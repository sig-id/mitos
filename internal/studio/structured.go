package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"mitos/agents"
	"mitos/internal/agentcli"
	"mitos/internal/compute"
	"mitos/internal/decision"
	"mitos/internal/structuredworker"
)

// StructuredWork is headless work (delivery gate S1): structured JSON input, no
// repository, workspace, Git or PTY, and an exact typed result validated against
// a declared schema. The definition revision and digest are frozen at admission.
type StructuredWork struct {
	SerializationKey string             `json:"serialization_key,omitempty"`
	ConsumedVersions map[string]uint64  `json:"consumed_versions,omitempty"`
	ActionTargets    []string           `json:"action_targets,omitempty"`
	CheckpointSchema json.RawMessage    `json:"checkpoint_schema,omitempty"`
	Frozen           *agents.Definition `json:"frozen_definition,omitempty"`
	Key              string             `json:"key"`
	Input            json.RawMessage    `json:"input"`
	ResultSchema     json.RawMessage    `json:"result_schema"`
	Definition       string             `json:"definition,omitempty"` // id@revision, frozen at admission
	Digest           string             `json:"definition_digest,omitempty"`
	RequestHash      string             `json:"request_hash,omitempty"`
}

const structuredResultArtifact = "result.json"

func (w *StructuredWork) validate() error {
	if (w.SerializationKey != "" && !validGraphKey(w.SerializationKey)) || !validConsumedVersions(w.ConsumedVersions) {
		return errors.New("invalid conversation key or consumed context versions")
	}
	if !validGraphKey(w.Key) {
		return errors.New("structured work needs a request key")
	}
	if len(w.Input) == 0 || len(w.Input) > maxStructuredInputBytes {
		return errors.New("structured input is required and limited to 256 KiB")
	}
	if err := rejectDuplicateJSONKeys(w.Input); err != nil {
		return fmt.Errorf("structured input: %w", err)
	}
	var input map[string]any
	dec := json.NewDecoder(bytes.NewReader(w.Input))
	if err := dec.Decode(&input); err != nil || input == nil {
		return errors.New("structured input must be a JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("structured input has trailing data")
	}
	if len(w.ActionTargets) > 16 {
		return errors.New("too many action targets")
	}
	for _, id := range w.ActionTargets {
		if !ValidDeviceID(id) {
			return errors.New("invalid action target")
		}
	}
	if len(w.CheckpointSchema) > 0 {
		if _, err := compileResultSchema(w.CheckpointSchema); err != nil {
			return fmt.Errorf("checkpoint schema: %w", err)
		}
	}
	_, err := compileResultSchema(w.ResultSchema)
	return err
}

// prepareStructured admits headless work. Workspace, project, ownership and
// dependencies are refused rather than ignored: S1 work has none of them.
func (e *Engine) prepareStructured(b Brief) (Brief, string, error) {
	w := b.Structured
	if b.Workspace != "" || b.ProjectID != "" || b.ProjectLimits || len(b.Ownership.Paths) > 0 || len(b.Ownership.Leases) > 0 || len(b.Scheduling.Deps) > 0 {
		return Brief{}, "", errors.New("structured work has no workspace, project, ownership or dependencies")
	}
	if err := w.validate(); err != nil {
		return Brief{}, "", err
	}
	if err := b.Validate(); err != nil {
		return Brief{}, "", err
	}
	app, ok := e.structuredApplication(b.AppID)
	if !ok || app.Agent == nil || app.Agent.Executable == "" {
		return Brief{}, "", errors.New("structured work runs an agent definition (agent:<id>) on an installed runtime")
	}
	d := app.Agent.Definition
	if isOrchestratorKind(d.Kind) {
		return Brief{}, "", errors.New("structured work runs worker definitions only")
	}
	if _, err := resolveAuthority(d, headlessRuntime(app.Agent.Runtime), EnforcementAdvisory, b.TimeoutMinutes*60, "", (&compute.Local{}).Capabilities()); err != nil {
		return Brief{}, "", err
	}
	frozen := *w
	frozen.Definition = fmt.Sprintf("%s@%d", d.ID, d.Revision)
	frozen.Digest = d.Digest
	copy := graphClone(d)
	frozen.Frozen = &copy
	frozen.RequestHash = ""
	b.Structured = &frozen
	b.Structured.RequestHash = graphDigest(struct {
		Title, Intent, AppID string
		Timeout              int
		Input, Schema        json.RawMessage
	}{b.Title, b.Intent, b.AppID, b.TimeoutMinutes, w.Input, w.ResultSchema})
	if len(w.CheckpointSchema) > 0 {
		b.Structured.RequestHash = graphDigest(struct {
			Request          string
			CheckpointSchema json.RawMessage
		}{b.Structured.RequestHash, w.CheckpointSchema})
	}
	if len(w.ActionTargets) > 0 {
		b.Structured.RequestHash = graphDigest(struct {
			Request string
			Targets []string
		}{b.Structured.RequestHash, w.ActionTargets})
	}
	if w.SerializationKey != "" || len(w.ConsumedVersions) > 0 {
		b.Structured.RequestHash = graphDigest(struct {
			Request  string
			Key      string
			Versions map[string]uint64
		}{b.Structured.RequestHash, w.SerializationKey, w.ConsumedVersions})
	}
	return b, app.Name, nil
}

// LaunchStructured admits structured work idempotently: the same key and request
// returns the original run; the same key with a different request conflicts.
func (e *Engine) LaunchStructured(b Brief) (Run, error) {
	return e.launchStructuredAs(decision.LocalOperator(), b)
}

func (e *Engine) launchStructuredAs(p decision.Principal, b Brief) (Run, error) {
	e.structuredMu.Lock()
	defer e.structuredMu.Unlock()
	e.Store.mu.RLock()
	authErr := e.Store.authorizeServiceLocked(p, true)
	e.Store.mu.RUnlock()
	if authErr != nil {
		return Run{}, authErr
	}
	b, name, err := e.prepareStructured(b)
	if err != nil {
		return Run{}, err
	}
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.authorizeServiceLocked(p, true); err != nil {
		return Run{}, err
	}
	if !strings.HasPrefix(b.AppID, agentAppPrefix) || !s.principalAgentAllowedLocked(p, b.AppID) {
		return Run{}, ErrForbidden
	}
	candidate := Run{Brief: b, Tenant: p.Tenant, PrincipalID: p.ID}
	for _, id := range b.Structured.ActionTargets {
		if _, err := s.actionTargetAllowedLocked(&candidate, id); err != nil {
			return Run{}, err
		}
	}
	key := scopedRequestKey(p.Tenant, b.Structured.Key)
	if s.expiredRequestKeys[key] {
		return Run{}, ErrRequestKeyExpired
	}
	if id := s.structuredKeys[key]; id != "" {
		prior := s.runs[id]
		owner := prior.PrincipalID
		if owner == "" {
			owner = decision.LocalOperator().ID
		}
		if owner != p.ID || prior.Brief.Structured.RequestHash != b.Structured.RequestHash {
			return Run{}, ErrConflict
		}
		return cloneRun(prior), nil
	}
	return s.createWithPrincipalLocked(b, name, "", false, nil, nil, nil, p)
}

func (s *Store) structuredRun(key string) (Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[s.structuredKeys[scopedRequestKey(decision.LocalTenant, key)]]
	if r == nil {
		return Run{}, false
	}
	return cloneRun(r), true
}

// retryStructured starts a fresh attempt of admitted structured work. The brief,
// including its frozen definition revision and digest, is reused exactly; it is
// never re-admitted against the current library.
func (e *Engine) retryStructured(b Brief, parent string) (Run, error) {
	prior, err := e.Store.Get(parent)
	if err != nil {
		return Run{}, err
	}
	if prior.Brief.Structured == nil || b.Structured == nil || prior.Brief.Structured.RequestHash != b.Structured.RequestHash || b.Structured.RequestHash == "" {
		return Run{}, errors.New("a structured retry must repeat its parent's admitted request")
	}
	d, err := e.exactDefinition(b.Structured.Definition, b.Structured.Digest)
	if err != nil {
		return Run{}, err
	}
	// Retries of pre-upgrade attempts carried no frozen definition body; the
	// child freezes the same exact revision instead of inheriting the gap.
	if b.Structured.Frozen == nil {
		frozen := d
		b.Structured.Frozen = &frozen
	}
	return e.Store.Create(b, d.Name, parent, false)
}

// hydrateStructuredDefinitions freezes the admitted definition body of
// structured runs recorded before definition bodies were persisted with the
// brief. Legacy runs stored only the definition reference and digest; the
// exact revision is resolved and persisted once so pre-upgrade work stays
// executable without ever being re-admitted against the latest library.
func (e *Engine) hydrateStructuredDefinitions() error {
	s := e.Store
	for _, r := range s.List() {
		w := r.Brief.Structured
		if w == nil || w.Frozen != nil || w.Definition == "" || w.Digest == "" || r.Terminal() {
			continue
		}
		d, err := e.exactDefinition(w.Definition, w.Digest)
		if err != nil {
			// Unresolvable definitions keep their recorded state; the claim
			// path keeps refusing them instead of guessing a revision.
			continue
		}
		s.mu.Lock()
		current := s.runs[r.ID]
		if current != nil && current.Brief.Structured != nil && current.Brief.Structured.Frozen == nil {
			err = s.commit(r.ID, "effect", "structured_definition_hydrated", change{StructuredDefinition: &d})
		}
		s.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// exactDefinition reads one immutable revision, never the latest of an ID.
func (e *Engine) exactDefinition(ref, digest string) (agents.Definition, error) {
	if frozen := e.Store.frozenAgents.Load(); frozen != nil {
		if d, ok := (*frozen)[ref]; ok && d.Digest == digest {
			return graphClone(d), nil
		}
	}
	id, revision, _ := strings.Cut(ref, "@")
	saved, err := e.readLibrary()
	if err != nil {
		return agents.Definition{}, err
	}
	bundled, err := agents.Library()
	if err != nil {
		return agents.Definition{}, err
	}
	for _, d := range append(bundled, saved.Revisions[id]...) {
		if d.ID == id && fmt.Sprint(d.Revision) == revision && d.Digest == digest {
			return d, nil
		}
	}
	return agents.Definition{}, fmt.Errorf("agent definition %s (%s) is no longer available", ref, digest)
}

func headlessRuntime(runtime string) string { return runtime + "-headless" }

func (e *Engine) performStructured(ctx context.Context, r Run) error {
	e.Store.mu.RLock()
	lease, ok := e.Store.workerLeases[r.ID]
	e.Store.mu.RUnlock()
	if !ok {
		return ErrStaleBinding
	}
	e.mu.Lock()
	token := e.localWorkerToken
	e.mu.Unlock()
	assignment, err := e.WorkerContext(token, lease.Binding)
	if err != nil {
		return err
	}
	app, ok := e.agentApplication(r.Brief.AppID)
	if !ok {
		return &agentPrerequisiteError{summary: "Codex is unavailable. Install it and sign in on this workstation, then retry."}
	}
	native, err := e.Desk.Agents.Prepare(agentcli.Request{AgentID: app.Agent.Runtime})
	if err != nil {
		return err
	}
	scoped := runTenant(&r) != decision.LocalTenant
	provider := e.provider
	if scoped {
		if !e.confinement.Enabled() || confinementBlocksTransport(e.confinement) {
			return errors.New("application_execution_unavailable: configure MITOS_AGENT_CONFINEMENT=bwrap and MITOS_AGENT_NETWORK=unrestricted for isolated model transport")
		}
		// Mediated transport is mandatory for tenant workers: without it the
		// sandbox would share the host network namespace and could reach
		// loopback services. Fail closed when the relay is absent.
		if e.egressEndpoint == "" {
			return errors.New("application_execution_unavailable: the mediated model transport is not running")
		}
		provider = e.agentProvider
	} else {
		native.Env, err = codexWorkerEnvironment(e.Store.Dir(), native.Env)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewDone := make(chan struct{})
	defer func() { cancel(); <-renewDone }()
	go func() {
		defer close(renewDone)
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, err := e.Store.WorkerMutate(token, structuredworker.Mutation{Binding: lease.Binding, RequestID: newID("renew"), Kind: "renew"}); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	preview, closePreview := e.beginAgentOutput(r.ID)
	defer closePreview()
	egress := ""
	if scoped {
		egress = "unix://" + e.egressEndpoint
	}
	executor := structuredworker.Executor{Provider: provider, Executable: native.Executable, Environment: native.Env, Base: filepath.Join(e.Store.Dir(), "workspaces", r.ID), Scoped: scoped, EgressEndpoint: egress, Preview: preview, Activity: preview.activity}
	executor.Action = func(ctx context.Context, kind string, raw json.RawMessage) (structuredworker.Receipt, error) {
		return e.WorkerAction(ctx, token, structuredworker.Mutation{Binding: lease.Binding, RequestID: newID("action"), Kind: kind, Data: raw})
	}
	executor.Wait = func(ctx context.Context, raw json.RawMessage) (structuredworker.Receipt, error) {
		return e.Store.WorkerMutate(token, structuredworker.Mutation{Binding: lease.Binding, RequestID: newID("wait"), Kind: "wait", Data: raw})
	}
	result, err := executor.Execute(ctx, assignment)
	if errors.Is(err, structuredworker.ErrDormant) {
		return nil
	}
	if err != nil {
		// Failed attempts keep their captured evidence for diagnosis.
		if len(result.Artifacts) > 0 {
			data := make([]ArtifactData, 0, len(result.Artifacts))
			for _, artifact := range result.Artifacts {
				data = append(data, ArtifactData{Name: artifact.Name, Data: artifact.Data})
			}
			_ = e.Store.SaveArtifacts(r.ID, data...)
		}
		_ = recordStructuredUsage(e.Store, r, result.Usage)
		return err
	}
	// Validate the original bytes before marshaling the transport envelope.
	schema, err := compileResultSchema(r.Brief.Structured.ResultSchema)
	if err != nil {
		return err
	}
	if _, err = validateResult(schema, result.Value); err != nil {
		_ = e.Store.SaveArtifact(r.ID, "rejected-result.txt", result.Value)
		return fmt.Errorf("result_schema_mismatch: %w", err)
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = e.Store.WorkerMutate(token, structuredworker.Mutation{Binding: lease.Binding, RequestID: newID("result"), Kind: "result", Data: body})
	return err
}

// StructuredResult returns the accepted typed result of finished structured work.
func (e *Engine) StructuredResult(id string) (json.RawMessage, error) {
	r, err := e.Store.Get(id)
	if err != nil {
		return nil, err
	}
	if r.Brief.Structured == nil {
		return nil, ErrNotFound
	}
	if r.Status != "done" || !r.Verified {
		return nil, ErrConflict
	}
	data, err := e.Store.Artifact(id, structuredResultArtifact)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// StructuredCapabilities publishes what headless structured work actually supports.
func StructuredCapabilities() map[string]any {
	return map[string]any{
		"profile":                          "structured-work/v1",
		"executor":                         "agent definitions (agent:<id>, worker kind) on the codex-headless runtime profile",
		"workspace":                        false,
		"git":                              false,
		"pty":                              false,
		"input":                            map[string]any{"type": "JSON object", "max_bytes": maxStructuredInputBytes},
		"result_schema":                    map[string]any{"max_bytes": maxResultSchemaBytes, "keywords": schemaKeywords, "rules": "root object; every object sets additionalProperties false and requires all properties; max depth 16"},
		"result":                           map[string]any{"max_bytes": maxStructuredResultBytes, "acceptance": "exact validation against the declared schema; a mismatch fails the attempt"},
		"idempotency":                      "structured.key; the same key with a different request conflicts",
		"enforcement_policy":               EnforcementAdvisory,
		"definition_binding":               "exact revision and digest frozen at admission",
		"project_scoped":                   false,
		"requires_database":                true,
		"requires_database_service":        false,
		"application_requires_confinement": true,
		"application_model_credentials":    "dedicated_api_key",
		"requires_broker":                  false,
		"requires_remote_workers":          false,
		"inputs":                           map[string]any{"durable": true, "cursor": "per-task sequence", "max_bytes": maxStructuredInputBytes, "max_page_items": ReadMaxLimit},
		"checkpoint":                       map[string]any{"schema_declared": true, "max_bytes": 64 << 10, "portable_json": true},
		"wait":                             "atomic input-cursor check and portable checkpoint; dormant attempts hold no execution capacity",
		"serialization_key":                "one active attempt per tenant/key; released while dormant",
		"consumed_versions":                "tenant CAS registers revalidated at admission and result publication",
	}
}
