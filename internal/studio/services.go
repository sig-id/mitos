package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"mitos/internal/service"
)

type ServiceCatalog struct {
	Revision int64            `json:"revision"`
	Config   service.Config   `json:"config"`
	Work     map[string]Brief `json:"work"`
}
type ServiceCall struct {
	Lifecycle       *ServiceLifecycleCause `json:"lifecycle,omitempty"`
	Ingress         *ServiceIngress        `json:"ingress,omitempty"`
	Cycle           *ServiceCycleBinding   `json:"cycle,omitempty"`
	Replacement     *ServiceReplacement    `json:"replacement,omitempty"`
	Queue           *ServiceQueueBinding   `json:"queue,omitempty"`
	ContinuousEpoch int64                  `json:"continuous_epoch,omitempty"`
	Event           *ServiceEventCause     `json:"event,omitempty"`
	Origin          string                 `json:"origin,omitempty"`
	Tick            *ServiceTick           `json:"tick,omitempty"`
	ServiceID       string                 `json:"service_id"`
	Key             string                 `json:"idempotency_key"`
	Revision        int64                  `json:"catalog_revision"`
}
type ServiceView struct {
	ClockPreview []ServiceClock     `json:"clock_preview,omitempty"`
	States       []ServiceState     `json:"states"`
	Catalog      ServiceCatalog     `json:"catalog"`
	Desired      service.Resolution `json:"desired"`
	Runtime      string             `json:"runtime"`
}

func cloneCatalog(c *ServiceCatalog) ServiceCatalog {
	if c == nil {
		return ServiceCatalog{Config: service.Config{Units: []service.Unit{}, Targets: []service.Target{}, Presets: []service.Preset{}}, Work: map[string]Brief{}}
	}
	data, _ := json.Marshal(c)
	var out ServiceCatalog
	_ = json.Unmarshal(data, &out)
	return out
}
func validateServiceEvent(e Event, c change) error {
	if e.RunID != "" || e.Type != "effect" || e.Kind != "services_configured" || c.Run != nil || c.Task != nil || c.Pool != nil || c.ServiceCall != nil || c.ServiceTick != nil || c.Services.Revision != e.Seq {
		return errors.New("invalid service catalog event")
	}
	if _, err := service.Resolve(c.Services.Config); err != nil {
		return err
	}
	return validateServiceClocks(e, c)
}
func validServiceKey(key string) bool {
	return key != "" && len(key) <= 128 && strings.TrimSpace(key) == key && !strings.ContainsAny(key, "\x00\r\n")
}
func validateServiceCall(e Event, c change) error {
	if err := validateLifecycleCall(e, c); err != nil {
		return err
	}
	if err := validateServiceIngress(e, c); err != nil {
		return err
	}
	if err := validateServiceDependencies(e, c); err != nil {
		return err
	}
	call := c.ServiceCall
	if call == nil || !service.ValidID(call.ServiceID) || !validServiceKey(call.Key) || call.Revision <= 0 || call.Revision >= e.Seq || c.Run == nil || c.Run.ID != e.RunID || e.Type != "effect" || e.Kind != "run_created" || c.Task == nil || c.Task.Source != "trigger:"+call.ServiceID {
		return errors.New("invalid service call binding")
	}
	if err := validateReplacement(call.Replacement, e.Seq); err != nil {
		return err
	}
	if call.Replacement != nil && (call.Replacement.ServiceID != call.ServiceID || call.Replacement.Revision > call.Revision || call.Replacement.TaskID == c.Task.ID || !reflect.DeepEqual(call.Replacement, c.Task.ServiceReplacement)) {
		return errors.New("replacement request is not bound to Task creation")
	}
	if err := validateServiceQueue(call.Queue); err != nil {
		return err
	}
	if call.Queue != nil && (call.Queue.ServiceID != call.ServiceID || call.Queue.Revision > call.Revision || !reflect.DeepEqual(c.Task.ServiceQueue, call.Queue)) {
		return errors.New("service queue policy is not bound to its Task")
	}
	if call.Origin != "" && call.Origin != "manual" && call.Origin != "interval" && call.Origin != "cron" && call.Origin != "event" && call.Origin != "continuous" && call.Origin != "webhook" && call.Origin != "external_signal" && call.Origin != "lifecycle" {
		return errors.New("invalid service trigger origin")
	}
	if call.Origin == "continuous" {
		if call.ContinuousEpoch < 1 || call.ContinuousEpoch > call.Revision || c.ServiceContinuous == nil || c.Task.ContinuousEpoch != call.ContinuousEpoch {
			return errors.New("invalid continuous service call")
		}
	} else if call.ContinuousEpoch != 0 {
		return errors.New("non-continuous call contains controller epoch")
	}
	if call.Origin == "event" {
		if !validServiceCause(call.Event, serviceEventDepth) || call.Event.Seq >= e.Seq || c.ServiceDelivery == nil || call.Tick != nil {
			return errors.New("invalid service event cause")
		}
	} else if call.Event != nil {
		return errors.New("non-event call contains event cause")
	}
	if call.Origin == "interval" || call.Origin == "cron" {
		if err := validateServiceTick(e, call.Tick, true); err != nil {
			return err
		}
		if call.Tick.ServiceID != call.ServiceID || call.Tick.Revision != call.Revision {
			return errors.New("service tick call mismatch")
		}
	} else if call.Tick != nil {
		return errors.New("manual call contains schedule occurrence")
	}
	return nil
}
func (s *Store) Services() (ServiceView, error) {
	s.mu.RLock()
	catalog := cloneCatalog(s.services)
	states := s.serviceRuntimeView()
	s.mu.RUnlock()
	resolved, err := service.Resolve(catalog.Config)
	return ServiceView{Catalog: catalog, Desired: resolved, States: states, Runtime: "manual_interval_cron_event_continuous_webhook_external_signal_task_triggers"}, err
}
func (e *Engine) prepareServices(c ServiceCatalog) (ServiceCatalog, error) {
	// Round-trip once to prevent caller-owned slices/maps from aliasing the saved catalog.
	c = cloneCatalog(&c)
	for i := range c.Config.Units {
		c.Config.Units[i].Trigger = c.Config.Units[i].TriggerKind()
	}
	if _, err := service.Resolve(c.Config); err != nil {
		return ServiceCatalog{}, err
	}
	if len(c.Work) != len(c.Config.Units) {
		return ServiceCatalog{}, errors.New("every service unit needs exactly one installed-application work binding")
	}
	active := activeServiceUnits(&c)
	paths := map[string]string{}
	for id, unit := range active {
		if unit.TriggerKind() == "webhook" {
			if prior := paths[unit.Schedule.Path]; prior != "" {
				return ServiceCatalog{}, fmt.Errorf("webhook path %s is active for both %s and %s", unit.Schedule.Path, prior, id)
			}
			paths[unit.Schedule.Path] = id
		}
	}
	for _, unit := range c.Config.Units {
		brief, ok := c.Work[unit.ID]
		if !ok {
			return ServiceCatalog{}, fmt.Errorf("service %s has no work binding", unit.ID)
		}
		if brief.AppID == WorkflowAppID && (unit.Condition != nil || unit.Health != nil) {
			return ServiceCatalog{}, fmt.Errorf("service %s: describe workflow conditions and health checks in the agent instructions", unit.ID)
		}
		// Operators must be able to disable a service whose installed app,
		// workspace, or absolute deadline has become unavailable.
		if _, enabled := active[unit.ID]; !enabled {
			if err := brief.Validate(); err != nil {
				return ServiceCatalog{}, fmt.Errorf("service %s: %w", unit.ID, err)
			}
			continue
		}
		if app, exists := e.App(brief.AppID); exists && app.Builtin && unit.Health != nil {
			return ServiceCatalog{}, fmt.Errorf("service %s: health monitoring requires a command application", unit.ID)
		}
		prepared, _, err := e.prepareBrief(brief)
		if err != nil {
			return ServiceCatalog{}, fmt.Errorf("service %s: %w", unit.ID, err)
		}
		c.Work[unit.ID] = prepared
	}
	return c, nil
}
func (e *Engine) PreviewServices(c ServiceCatalog) (ServiceView, error) {
	c, err := e.prepareServices(c)
	if err != nil {
		return ServiceView{}, err
	}
	e.Store.mu.RLock()
	defer e.Store.mu.RUnlock()
	for _, u := range c.Config.Units {
		if err = e.Store.validateTaskRequest("service-validation", c.Work[u.ID].Scheduling, false); err != nil {
			return ServiceView{}, err
		}
	}
	clocks, err := e.Store.planServiceClocks(c, time.Now().UTC())
	if err != nil {
		return ServiceView{}, err
	}
	resolved, err := service.Resolve(c.Config)
	return ServiceView{Catalog: c, Desired: resolved, ClockPreview: clocks, Runtime: "manual_interval_cron_event_continuous_webhook_external_signal_task_triggers"}, err
}
func (e *Engine) ConfigureServices(c ServiceCatalog, expected int64) (ServiceView, error) {
	var err error
	c, err = e.prepareServices(c)
	if err != nil {
		return ServiceView{}, err
	}
	e.Store.mu.Lock()
	previous := cloneCatalog(e.Store.services)
	if previous.Revision != expected {
		e.Store.mu.Unlock()
		return ServiceView{}, ErrConflict
	}
	c.Revision = previous.Revision
	if reflect.DeepEqual(previous, c) {
		e.Store.mu.Unlock()
		return e.Store.Services()
	}
	for _, unit := range c.Config.Units {
		if err := e.Store.validateTaskRequest("service-validation", c.Work[unit.ID].Scheduling, false); err != nil {
			e.Store.mu.Unlock()
			return ServiceView{}, err
		}
	}
	clocks, clockErr := e.Store.planServiceClocks(c, time.Now().UTC())
	if clockErr != nil {
		e.Store.mu.Unlock()
		return ServiceView{}, clockErr
	}
	c.Revision = e.Store.seq + 1
	err = e.Store.commit("", "effect", "services_configured", change{Services: &c, ServiceClocks: clocks})
	e.Store.mu.Unlock()
	if err != nil {
		return ServiceView{}, err
	}
	return e.Store.Services()
}
func (e *Engine) TriggerService(id, key string, cycle ...string) (Run, error) {
	if !service.ValidID(id) || !validServiceKey(key) {
		return Run{}, errors.New("service trigger requires an ID and a bounded nonempty idempotency key")
	}
	if len(cycle) > 1 || len(cycle) == 1 && cycle[0] != "" && !validServiceKey(cycle[0]) {
		return Run{}, errors.New("cycle requires a bounded key")
	}
	e.actions.Lock()
	defer e.actions.Unlock()
	e.Store.mu.RLock()
	if prior := e.Store.serviceCalls[serviceCallIndex(id, key, "manual")]; prior != "" {
		r := cloneRun(e.Store.runs[prior])
		e.Store.mu.RUnlock()
		return r, nil
	}
	catalog := cloneCatalog(e.Store.services)
	e.Store.mu.RUnlock()
	desired, err := service.Resolve(catalog.Config)
	if err != nil {
		return Run{}, err
	}
	active := false
	for _, row := range desired.Rows {
		if row.ID == id && row.Kind == "unit" && row.State == "active" {
			active = true
		}
	}
	if !active {
		return Run{}, errors.New("service is not active in the resolved project configuration")
	}
	brief, ok := catalog.Work[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	prepared, name, err := e.prepareBrief(brief)
	if err != nil {
		return Run{}, err
	}
	e.Store.mu.Lock()
	defer e.Store.mu.Unlock()
	if e.Store.services == nil || e.Store.services.Revision != catalog.Revision {
		return Run{}, ErrConflict
	}
	if e.Store.serviceAborted(id) {
		return Run{}, errors.New("the service abort policy is withholding new execution")
	}
	if !e.Store.serviceBreakerAllows(id, time.Now().UTC()) {
		return Run{}, errors.New("service circuit breaker is withholding new execution")
	}
	if !e.Store.serviceCanEnqueue(id) {
		return Run{}, errors.New("service concurrency or queue capacity is full")
	}
	call := ServiceCall{ServiceID: id, Key: key, Revision: catalog.Revision, Origin: "manual"}
	if len(cycle) == 1 && cycle[0] != "" {
		call.Cycle = e.Store.serviceCycleBinding(id, "manual:"+cycle[0], catalog.Revision)
	} else if len(e.Store.serviceCycleBinding(id, "", catalog.Revision).DependsOn) > 0 {
		return Run{}, errors.New("service dependencies require an explicit cycle key shared with prerequisite triggers")
	}
	return e.Store.createLocked(prepared, name, "", false, &call)
}
