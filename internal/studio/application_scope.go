package studio

import (
	"encoding/json"
	"strings"

	"mitos/internal/decision"
)

// ScopedStore is the complete application data boundary. It exposes no raw
// Store, filesystem path, project, global configuration, or operator operation.
// Every ID lookup checks immutable tenant ownership and hides foreign IDs as 404.
type ScopedStore struct {
	store     *Store
	principal decision.Principal
}

func (s *Store) As(p decision.Principal) *ScopedStore { return &ScopedStore{store: s, principal: p} }

func (v *ScopedStore) runLocked(id string, write bool) (*Run, error) {
	if err := v.store.authorizeServiceLocked(v.principal, write); err != nil {
		return nil, err
	}
	r := v.store.runs[id]
	if r == nil || runTenant(r) != v.principal.Tenant || r.Brief.Structured == nil {
		return nil, ErrNotFound
	}
	return r, nil
}
func (v *ScopedStore) Get(id string) (Run, error) {
	v.store.mu.RLock()
	defer v.store.mu.RUnlock()
	r, err := v.runLocked(id, false)
	if err != nil {
		return Run{}, err
	}
	return cloneRun(r), nil
}
func (v *ScopedStore) Task(id string) (Task, error) {
	v.store.mu.RLock()
	defer v.store.mu.RUnlock()
	if err := v.store.authorizeServiceLocked(v.principal, false); err != nil {
		return Task{}, err
	}
	t := v.store.tasks[id]
	if t == nil || t.Tenant != v.principal.Tenant || t.Brief.Structured == nil {
		return Task{}, ErrNotFound
	}
	return graphClone(*t), nil
}
func (v *ScopedStore) Artifact(id, name string) ([]byte, error) {
	r, err := v.Get(id)
	if err != nil {
		return nil, err
	}
	return v.store.readRegisteredArtifact(&r, name)
}
func (v *ScopedStore) Result(id string) (json.RawMessage, error) {
	r, err := v.Get(id)
	if err != nil {
		return nil, err
	}
	if !r.Terminal() || r.Status != "done" || !r.Verified {
		return nil, ErrConflict
	}
	return v.Artifact(id, structuredResultArtifact)
}
func (v *ScopedStore) Events(id string) ([]Event, error) {
	r, err := v.Get(id)
	if err != nil {
		return nil, err
	}
	return r.Events, nil
}
func (v *ScopedStore) Runs() ([]Run, error) {
	v.store.mu.RLock()
	defer v.store.mu.RUnlock()
	if err := v.store.authorizeServiceLocked(v.principal, false); err != nil {
		return nil, err
	}
	out := []Run{}
	for _, r := range v.store.runs {
		if runTenant(r) == v.principal.Tenant && r.Brief.Structured != nil {
			out = append(out, cloneRun(r))
		}
	}
	return out, nil
}

type ApplicationSession struct {
	view   *ScopedStore
	engine *Engine
}

func (e *Engine) Application(p decision.Principal) *ApplicationSession {
	return &ApplicationSession{view: e.Store.As(p), engine: e}
}
func (a *ApplicationSession) Get(id string) (Run, error)   { return a.view.Get(id) }
func (a *ApplicationSession) Task(id string) (Task, error) { return a.view.Task(id) }
func (a *ApplicationSession) Artifact(id, name string) ([]byte, error) {
	return a.view.Artifact(id, name)
}
func (a *ApplicationSession) Result(id string) (json.RawMessage, error) { return a.view.Result(id) }
func (a *ApplicationSession) Events(id string) ([]Event, error)         { return a.view.Events(id) }
func (a *ApplicationSession) Runs() ([]Run, error)                      { return a.view.Runs() }
func (a *ApplicationSession) Admit(b Brief) (Run, error) {
	if b.Structured == nil {
		return Run{}, ErrForbidden
	}
	return a.engine.launchStructuredAs(a.view.principal, b)
}

// Resolve answers a pending decision on the application's own work, such as
// the recovery choice recorded when the server restarted mid-execution. The
// authenticated principal supplies the tenant identity, so the store's
// cross-tenant checks stay intact; choosing retry re-admits the frozen brief
// exactly like the console path.
func (a *ApplicationSession) Resolve(id, decisionID, choice, note string) (string, error) {
	s := a.view.store
	s.mu.RLock()
	_, err := a.view.runLocked(id, false)
	s.mu.RUnlock()
	if err != nil {
		return "", err
	}
	return a.engine.ResolveAs(id, decisionID, choice, note, "api", a.view.principal)
}

func (a *ApplicationSession) Cancel(id string) error {
	e, s := a.engine, a.view.store
	e.actions.Lock()
	defer e.actions.Unlock()
	s.mu.Lock()
	r, err := a.view.runLocked(id, true)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if r.Status == "cancelled" || r.Status == "stopping" {
		s.mu.Unlock()
		return nil
	}
	if r.Terminal() {
		s.mu.Unlock()
		return ErrConflict
	}
	err = s.commit(id, "effect", "cancellation_requested", change{Status: "stopping", Stage: "Stopping", Summary: "The application requested cancellation."})
	s.mu.Unlock()
	if err != nil {
		return err
	}
	e.mu.Lock()
	cancel := e.cancel[id]
	if cancel != nil {
		cancel()
	}
	e.mu.Unlock()
	if cancel == nil {
		return s.Transition(id, "run_cancelled", "cancelled", "Stopped", "The application cancelled queued work.")
	}
	return nil
}

func scopedRequestKey(tenant decision.Tenant, key string) string {
	raw, _ := json.Marshal([]string{tenant.Org, tenant.Project, key})
	return string(raw)
}
func legacyOrScopedKey(key string) string {
	if strings.HasPrefix(key, "[") {
		return key
	}
	return scopedRequestKey(decision.LocalTenant, key)
}
