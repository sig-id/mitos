package studio

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"time"

	"mitos/internal/decision"
	"mitos/internal/desk"
	"mitos/internal/workpool"
)

type HomeServer struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Protocol     int             `json:"protocol"`
	Capabilities map[string]bool `json:"capabilities"`
}

func (s *Store) ensureHome() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.home.ID != "" {
		return nil
	}
	name, _ := os.Hostname()
	if name == "" {
		name = "Local server"
	}
	return s.commit("", "effect", "home_server_created", change{Home: &HomeServer{ID: newID("server"), Name: name, Protocol: 1}})
}
func (s *Store) Home() HomeServer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.home
	v.Capabilities = map[string]bool{"journal_work_identity": true, "interactive_task_binding": true, "human_report_review": true, "durable_supervision_identity": true, "paired_remote_clients": false, "remote_placement": false, "enforced_confinement": false, "surviving_process_reattachment": false, "durable_control_application": false}
	return v
}
func (s *Store) LoadDesk() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]byte(nil), s.deskState...), nil
}

type deskProjection struct {
	Version  int                   `json:"version"`
	Work     []desk.WorkAssignment `json:"work"`
	Sessions []desk.Session        `json:"sessions"`
}

func decodeDesk(b []byte) (deskProjection, error) {
	var p deskProjection
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	if p.Version != 1 {
		return p, errors.New("unsupported workspace snapshot version")
	}
	tasks := map[string]bool{}
	attempts := map[string]bool{}
	for _, t := range p.Work {
		if t.ID == "" || tasks[t.ID] {
			return p, errors.New("duplicate or missing task identity")
		}
		tasks[t.ID] = true
	}
	for _, v := range p.Sessions {
		if v.TaskID == "" {
			continue
		}
		if !tasks[v.TaskID] || v.AttemptID == "" || v.ProcessID == "" || attempts[v.AttemptID] {
			return p, errors.New("invalid interactive execution binding")
		}
		attempts[v.AttemptID] = true
	}
	return p, nil
}

// SaveDesk atomically publishes the metadata and its common Task/Run projection.
// The old state.json is retained as the pre-migration backup, never dual-written.
func (s *Store) SaveDesk(b []byte) error {
	// Keep individual records below the journal reader's 16 MiB bound, including
	// the event envelope. Refuse admission rather than write unreplayable history.
	if len(b) > 12<<20 {
		return errors.New("workspace metadata capacity reached; existing history is retained")
	}
	p, err := decodeDesk(b)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range p.Sessions {
		if v.AttemptID != "" {
			if old := s.runs[v.AttemptID]; old != nil && (old.ExecutionKind != "interactive" || old.SessionID != v.ID) {
				return errors.New("attempt identity collision")
			}
		}
	}
	for _, t := range p.Work {
		if old := s.tasks[t.ID]; old != nil && old.Source != "interactive" {
			return errors.New("task identity collision")
		}
	}
	if reflect.DeepEqual([]byte(s.deskState), b) {
		return nil
	}
	kind := "workspace_committed"
	if len(s.deskState) == 0 {
		kind = "workspace_imported_v1"
	}
	return s.commit("", "effect", kind, change{DeskState: json.RawMessage(b)})
}
func (s *Store) applyDesk(e Event, b []byte) error {
	p, err := decodeDesk(b)
	if err != nil {
		return err
	}
	if s.tasks == nil {
		s.tasks = map[string]*Task{}
	}
	byTask := map[string][]desk.Session{}
	for _, session := range p.Sessions {
		if session.TaskID != "" {
			byTask[session.TaskID] = append(byTask[session.TaskID], session)
		}
	}
	for _, w := range p.Work {
		request := workpool.Request{}
		request.Normalize()
		t := Task{ID: w.ID, ProjectID: w.ProjectID, GoalID: w.GoalID, ParentTaskID: w.ParentID, Criteria: w.Criteria, Revision: w.Revision, ResponsibilityID: w.ResponsibilityID, Tenant: decision.LocalTenant, Source: "interactive", Status: w.Status, Brief: Brief{Title: w.Title, Intent: w.Intent}, Scheduling: request, Runs: []string{}, CreatedAt: w.CreatedAt, ReadyAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, Head: TaskHead{Seq: e.Seq, Hash: e.Hash}}
		if w.Status == "accepted" {
			t.Status = "done"
		}
		for _, session := range byTask[w.ID] {
			var report *desk.WorkResult
			for i := range w.PreviousReports {
				if w.PreviousReports[i].SessionID == session.ID {
					report = &w.PreviousReports[i]
				}
			}
			if w.Result != nil && w.Result.SessionID == session.ID {
				report = w.Result
			}
			t.Runs = append(t.Runs, session.AttemptID)
			if session.ID == w.CurrentSessionID {
				t.CurrentRunID = session.AttemptID
				t.AdmittedRunID = session.AttemptID
				t.Brief.Workspace = session.Workspace
			}
			status, stage := "parked", "Needs an executor"
			switch session.Status {
			case "starting":
				status, stage = "queued", "Starting interactive executor"
			case "running":
				status, stage = "running", "Interactive execution"
			case "stopping":
				status, stage = "stopping", "Stopping executor"
			case "interrupted":
				stage = "Needs process inspection"
			case "failed":
				status, stage = "failed", "Executor failed"
			}
			if w.Status == "cancelled" && session.ID == w.CurrentSessionID {
				status, stage = "cancelled", "Task cancelled"
			}
			if report != nil {
				stage = "Report submitted; review pending"
				if report.AcceptedAt != nil {
					status, stage = "done", "Report accepted by operator"
				}
			}
			old := s.runs[session.AttemptID]
			r := Run{ID: session.AttemptID, TaskID: w.ID, ProjectID: w.ProjectID, SessionID: session.ID, ProcessID: session.ProcessID, ExecutionKind: "interactive", Tenant: decision.LocalTenant, Brief: Brief{Title: w.Title, Intent: w.Intent, Workspace: session.Workspace, AppID: session.AgentID}, AppName: session.Command, Status: status, Stage: stage, Summary: session.Detail, CreatedAt: session.CreatedAt, UpdatedAt: e.At}
			if session.EndedAt != nil {
				r.FinishedAt = *session.EndedAt
			}
			if report != nil {
				r.Summary = report.Body
				r.Verified = report.AcceptedAt != nil
				r.VerificationMethod = report.Method
			}
			if old != nil {
				r.RecordRefs = old.RecordRefs
				r.UpdatedAt = old.UpdatedAt
			}
			if old == nil || old.Status != r.Status || old.Stage != r.Stage || old.Summary != r.Summary || old.Verified != r.Verified {
				r.UpdatedAt = e.At
				r.RecordRefs = append(r.RecordRefs, RecordRef{Seq: e.Seq, Hash: e.Hash, At: e.At, Kind: e.Kind})
			}
			s.runs[r.ID] = &r
		}
		s.tasks[t.ID] = &t
	}
	s.deskState = append([]byte(nil), b...)
	return nil
}

// Local status read models carry their source identity and freshness explicitly.
type ProjectWork struct {
	Server     HomeServer    `json:"server"`
	ObservedAt time.Time     `json:"observed_at"`
	Project    desk.Project  `json:"project"`
	Work       desk.WorkView `json:"work"`
	BatchTasks []Task        `json:"batch_tasks"`
}

func (e *Engine) ProjectWork(id string) (ProjectWork, error) {
	p, err := e.Desk.Project(id)
	if err != nil {
		return ProjectWork{}, err
	}
	w, err := e.Desk.Work(id)
	if err != nil {
		return ProjectWork{}, err
	}
	v := ProjectWork{Server: e.Store.Home(), ObservedAt: time.Now().UTC(), Project: p, Work: w, BatchTasks: []Task{}}
	for _, t := range e.Store.Tasks() {
		if t.Source != "interactive" && t.Brief.ProjectID == id {
			v.BatchTasks = append(v.BatchTasks, t)
		}
	}
	return v, nil
}
