package studio

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mitos/internal/decision"
)

type ActionTarget struct {
	ID            string          `json:"id"`
	Tenant        decision.Tenant `json:"tenant"`
	Agents        []string        `json:"agents"`
	Endpoint      string          `json:"endpoint"`
	CredentialRef string          `json:"credential_ref,omitempty"`
	InputSchema   json.RawMessage `json:"input_schema"`
	ResultSchema  json.RawMessage `json:"result_schema"`
	Revision      string          `json:"revision"`
	Disabled      bool            `json:"disabled,omitempty"`
}
type ActionTargetInput struct {
	ID           string          `json:"id"`
	Tenant       decision.Tenant `json:"tenant"`
	Agents       []string        `json:"agents"`
	Endpoint     string          `json:"endpoint"`
	Bearer       string          `json:"bearer,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	ResultSchema json.RawMessage `json:"result_schema"`
	Disabled     bool            `json:"disabled,omitempty"`
}

func (s *Store) ConfigureActionTarget(in ActionTargetInput, p decision.Principal) (ActionTarget, error) {
	if !localOperator(p) {
		return ActionTarget{}, ErrForbidden
	}
	u, err := url.Parse(in.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && (u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback())) {
		return ActionTarget{}, errors.New("action target requires HTTPS or literal loopback HTTP")
	}
	if !ValidDeviceID(in.ID) || !ValidDeviceID(in.Tenant.Org) || !ValidDeviceID(in.Tenant.Project) || len(in.Agents) == 0 || len(in.Agents) > 128 || len(in.Bearer) > 16<<10 {
		return ActionTarget{}, errors.New("invalid action scope")
	}
	for _, id := range in.Agents {
		if !ValidDeviceID(id) {
			return ActionTarget{}, errors.New("invalid action agent")
		}
	}
	if _, err = compileResultSchema(in.InputSchema); err != nil {
		return ActionTarget{}, err
	}
	if _, err = compileResultSchema(in.ResultSchema); err != nil {
		return ActionTarget{}, err
	}
	target := ActionTarget{ID: in.ID, Tenant: in.Tenant, Agents: slices.Clone(in.Agents), Endpoint: strings.TrimRight(in.Endpoint, "/"), InputSchema: in.InputSchema, ResultSchema: in.ResultSchema, Revision: newID("action-policy"), Disabled: in.Disabled}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Credentials are immutable private files, copied by backup and never sent to workers.
	if in.Bearer != "" {
		ref := newID("action-credential")
		dir := filepath.Join(s.dir, "credentials")
		if err = os.MkdirAll(dir, 0700); err != nil {
			return ActionTarget{}, err
		}
		path := filepath.Join(dir, ref)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return ActionTarget{}, err
		}
		_, writeErr := f.Write([]byte(in.Bearer))
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := f.Close()
		if writeErr != nil {
			return ActionTarget{}, writeErr
		}
		if closeErr != nil {
			return ActionTarget{}, closeErr
		}
		if err = syncDirectory(dir); err != nil {
			return ActionTarget{}, err
		}
		if err = syncDirectory(s.dir); err != nil {
			return ActionTarget{}, err
		}
		target.CredentialRef = ref
	}
	if err = s.commit("", "effect", "action_target_configured", change{Action: &actionChange{Target: &target}}); err != nil {
		return ActionTarget{}, err
	}
	public := target
	public.CredentialRef = ""
	return public, nil
}
func (s *Store) actionTargetAllowedLocked(r *Run, id string) (ActionTarget, error) {
	target, ok := s.actionTargets[id]
	if !ok || target.Disabled || target.Tenant != runTenant(r) || r.Brief.Structured == nil || r.Brief.Structured.Frozen == nil || !slices.Contains(r.Brief.Structured.ActionTargets, id) || !slices.Contains(target.Agents, r.Brief.Structured.Frozen.ID) {
		return ActionTarget{}, ErrForbidden
	}
	return target, nil
}
