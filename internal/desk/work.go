package desk

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mitos/internal/agentcli"
)

// WorkAssignment is the persisted interactive admission specification. The
// server journal folds it into the same Task projection as batch work.
type WorkAssignment struct {
	RequestKey       string       `json:"request_key,omitempty"`
	RequestDigest    string       `json:"request_digest,omitempty"`
	Role             string       `json:"role"`
	ID               string       `json:"id"`
	ProjectID        string       `json:"project_id"`
	GoalID           string       `json:"goal_id"`
	ParentID         string       `json:"parent_id,omitempty"`
	ResponsibilityID string       `json:"responsibility_id,omitempty"`
	Title            string       `json:"title"`
	Intent           string       `json:"intent"`
	Criteria         string       `json:"criteria"`
	CriteriaRevision int          `json:"criteria_revision"`
	Revision         int          `json:"revision"`
	Status           string       `json:"status"`
	CurrentSessionID string       `json:"current_session_id,omitempty"`
	Result           *WorkResult  `json:"result,omitempty"`
	PreviousReports  []WorkResult `json:"previous_reports,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
	UpdatedAt        time.Time    `json:"updated_at"`
}
type Goal struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	Intent    string    `json:"intent"`
	CreatedAt time.Time `json:"created_at"`
}
type Responsibility struct {
	ID                   string        `json:"id"`
	ProjectID            string        `json:"project_id"`
	TaskID               string        `json:"task_id"`
	Title                string        `json:"title"`
	Generation           int           `json:"generation"`
	CoordinatorSessionID string        `json:"coordinator_session_id"`
	Messages             []WorkMessage `json:"messages"`
}
type WorkMessage struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}
type WorkResult struct {
	SessionID        string     `json:"session_id"`
	AttemptID        string     `json:"attempt_id"`
	Body             string     `json:"body"`
	Digest           string     `json:"digest"`
	CriteriaRevision int        `json:"criteria_revision"`
	SubmittedAt      time.Time  `json:"submitted_at"`
	AcceptedAt       *time.Time `json:"accepted_at,omitempty"`
	Reviewer         string     `json:"reviewer,omitempty"`
	Review           string     `json:"review,omitempty"`
	Method           string     `json:"method"` // Explicit human review of the frozen report, not a repository oracle.
}
type WorkReceipt struct {
	Key       string `json:"key"`
	ProjectID string `json:"project_id"`
	Digest    string `json:"digest"`
	SessionID string `json:"session_id"`
}
type WorkView struct {
	Tasks            []WorkAssignment `json:"tasks"`
	Goals            []Goal           `json:"goals"`
	Responsibilities []Responsibility `json:"responsibilities"`
	Sessions         []Session        `json:"sessions"`
}

func (m *Manager) Work(project string) (WorkView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.projectLocked(project); err != nil {
		return WorkView{}, err
	}
	v := WorkView{Tasks: []WorkAssignment{}, Goals: []Goal{}, Responsibilities: []Responsibility{}, Sessions: []Session{}}
	for _, t := range m.state.Work {
		if t.ProjectID == project {
			v.Tasks = append(v.Tasks, t)
		}
	}
	for _, g := range m.state.Goals {
		if g.ProjectID == project {
			v.Goals = append(v.Goals, g)
		}
	}
	for _, r := range m.state.Responsibilities {
		if r.ProjectID == project {
			v.Responsibilities = append(v.Responsibilities, r)
		}
	}
	for _, s := range m.state.Sessions {
		if s.ProjectID == project {
			v.Sessions = append(v.Sessions, s)
		}
	}
	b, _ := json.Marshal(v)
	var copy WorkView
	_ = json.Unmarshal(b, &copy)
	return copy, nil
}
func (m *Manager) workLocked(id string) (*WorkAssignment, error) {
	for i := range m.state.Work {
		if m.state.Work[i].ID == id {
			return &m.state.Work[i], nil
		}
	}
	return nil, ErrNotFound
}
func requestDigest(r agentcli.Request) string {
	b, _ := json.Marshal(r)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (m *Manager) launchReceiptLocked(project string, r agentcli.Request) (Session, bool, error) {
	if !validText(r.Key, 100) {
		return Session{}, false, errors.New("invalid launch request key")
	}
	for _, v := range m.state.WorkReceipts {
		if v.ProjectID == project && v.Key == r.Key {
			if v.Digest != requestDigest(r) {
				return Session{}, true, fmt.Errorf("%w: request key has different content", ErrConflict)
			}
			s, err := m.sessionLocked(v.SessionID)
			if err != nil {
				return Session{}, true, err
			}
			return cloneSession(*s), true, nil
		}
	}
	if len(m.state.WorkReceipts) >= 10000 {
		return Session{}, false, errors.New("launch receipt capacity reached; existing receipts are retained")
	}
	return Session{}, false, nil
}
func (m *Manager) bindWorkLocked(p Project, s *Session, options startOptions) error {
	// A terminal without an assignment remains unbound. Never invent historical work.
	if m.storage == nil {
		return nil
	}
	if strings.TrimSpace(s.InitialPrompt) == "" {
		return nil
	}
	assignment := agentcli.Assignment{}
	if s.Assignment != nil {
		assignment = *s.Assignment
	}
	var task *WorkAssignment
	var source *Session
	if s.Continuation != nil {
		source, _ = m.sessionLocked(s.Continuation.SourceID)
		if source != nil && source.TaskID != "" && s.Continuation.Kind != "fork" {
			assignment.TaskID = source.TaskID
		} else {
			assignment.TaskID = ""
		}
	}
	if assignment.TaskID != "" {
		var err error
		task, err = m.workLocked(assignment.TaskID)
		if err != nil || task.ProjectID != p.ID {
			return errors.New("task must be a managed assignment in this project")
		}
		if task.Role != "" && task.Role != s.Role {
			return errors.New("replacement must preserve the assignment role")
		}
		if task.Status == "accepted" || task.Status == "cancelled" {
			return fmt.Errorf("%w: task is closed", ErrConflict)
		}
		if options.Request != nil && options.Request.ExpectedRevision != 0 && options.Request.ExpectedRevision != task.Revision {
			return ErrConflict
		}
		if current, err := m.sessionLocked(task.CurrentSessionID); err == nil && activeSession(*current) {
			return fmt.Errorf("%w: stop and settle the previous executor before replacement", ErrConflict)
		}
	}
	if assignment.RequestedBy == "" && s.CoordinatorID != "" {
		assignment.RequestedBy = s.CoordinatorID
	}
	parentID, goalID := "", assignment.GoalID
	if assignment.RequestedBy != "" && assignment.RequestedBy != "operator" {
		parent, err := m.sessionLocked(assignment.RequestedBy)
		if err != nil {
			return err
		}
		if parent.TaskID != "" {
			owner, _ := m.workLocked(parent.TaskID)
			if owner == nil || owner.CurrentSessionID != parent.ID || owner.Status == "accepted" || owner.Status == "cancelled" {
				return fmt.Errorf("%w: obsolete executor cannot delegate", ErrConflict)
			}
			parentID, goalID = owner.ID, owner.GoalID
		}
	}
	if source != nil && source.TaskID != "" && s.Continuation.Kind == "fork" {
		parentID = source.TaskID
	}
	if task != nil {
		goalID = task.GoalID
	}
	if goalID != "" {
		found := false
		for _, g := range m.state.Goals {
			if g.ID == goalID && g.ProjectID == p.ID {
				found = true
			}
		}
		if !found {
			return errors.New("goal must belong to this project")
		}
	}
	criteria := "Review the submitted report against the assignment. Repository changes require separate checks."
	if options.Request != nil && strings.TrimSpace(options.Request.Criteria) != "" {
		criteria = strings.TrimSpace(options.Request.Criteria)
	}
	if len(criteria) > 16000 {
		return errors.New("criteria exceed 16000 bytes")
	}
	now := time.Now().UTC()
	if task == nil {
		if len(m.state.Work) >= 10000 {
			return errors.New("task capacity reached")
		}
		if goalID == "" {
			goalID = id("goal")
			m.state.Goals = append(m.state.Goals, Goal{ID: goalID, ProjectID: p.ID, Title: s.Name, Intent: s.InitialPrompt, CreatedAt: now})
		}
		m.state.Work = append(m.state.Work, WorkAssignment{ID: id("task"), ProjectID: p.ID, GoalID: goalID, ParentID: parentID, Title: s.Name, Intent: s.InitialPrompt, Role: s.Role, Criteria: criteria, CriteriaRevision: 1, Revision: 1, Status: "starting", CreatedAt: now, UpdatedAt: now})
		task = &m.state.Work[len(m.state.Work)-1]
	}
	s.TaskID = task.ID
	s.AttemptID = id("local")
	s.ProcessID = id("process")
	s.Generation = 1
	for _, previous := range m.state.Sessions {
		if previous.TaskID == task.ID {
			s.Generation = max(s.Generation, previous.Generation+1)
		}
	}
	if s.Role == "coordinator" {
		var responsibility *Responsibility
		for i := range m.state.Responsibilities {
			if m.state.Responsibilities[i].TaskID == task.ID {
				responsibility = &m.state.Responsibilities[i]
			}
		}
		if responsibility == nil {
			m.state.Responsibilities = append(m.state.Responsibilities, Responsibility{ID: id("responsibility"), ProjectID: p.ID, TaskID: task.ID, Title: task.Title, Messages: []WorkMessage{}})
			responsibility = &m.state.Responsibilities[len(m.state.Responsibilities)-1]
		}
		responsibility.Generation++
		responsibility.CoordinatorSessionID = s.ID
		s.ResponsibilityID = responsibility.ID
		task.ResponsibilityID = responsibility.ID
	}
	task.CurrentSessionID = s.ID
	task.Status = "starting"
	task.Revision++
	task.UpdatedAt = now
	assignment.TaskID, assignment.GoalID = task.ID, task.GoalID
	s.Assignment = &assignment
	if options.Request != nil && options.Request.Key != "" {
		m.state.WorkReceipts = append(m.state.WorkReceipts, WorkReceipt{Key: options.Request.Key, ProjectID: p.ID, Digest: requestDigest(*options.Request), SessionID: s.ID})
	}
	return nil
}
func (m *Manager) refreshWorkLocked() {
	for i := range m.state.Work {
		t := &m.state.Work[i]
		if t.Status == "accepted" || t.Status == "cancelled" {
			continue
		}
		s, err := m.sessionLocked(t.CurrentSessionID)
		if err != nil {
			continue
		}
		status := "needs_executor"
		switch s.Status {
		case "starting", "running", "stopping":
			status = s.Status
		case "interrupted":
			status = "needs_inspection"
		}
		if t.Result != nil && t.Result.SessionID == s.ID && s.Status != "interrupted" {
			status = "reported_complete"
		}
		if status != t.Status {
			t.Status = status
			t.Revision++
			t.UpdatedAt = time.Now().UTC()
		}
	}
}

type WorkAction struct {
	Revision  int    `json:"revision"`
	SessionID string `json:"session_id,omitempty"`
	Body      string `json:"body,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

func (m *Manager) UpdateWork(project, taskID, action string, in WorkAction) (WorkAssignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return WorkAssignment{}, err
	}
	t, err := m.workLocked(taskID)
	if err != nil || t.ProjectID != project {
		return WorkAssignment{}, ErrNotFound
	}
	if in.Revision != t.Revision {
		return WorkAssignment{}, ErrConflict
	}
	if t.Status == "accepted" || t.Status == "cancelled" {
		return WorkAssignment{}, ErrConflict
	}
	current, _ := m.sessionLocked(t.CurrentSessionID)
	switch action {
	case "report":
		if current == nil || in.SessionID != current.ID {
			return WorkAssignment{}, fmt.Errorf("%w: report belongs to an obsolete attempt", ErrConflict)
		}
		if !validText(in.Body, 65536) {
			return WorkAssignment{}, errors.New("provide a report of at most 64 KiB")
		}
		if len(t.PreviousReports) >= 256 {
			return WorkAssignment{}, errors.New("report history capacity reached; earlier evidence is retained")
		}
		if t.Result != nil {
			t.PreviousReports = append(t.PreviousReports, *t.Result)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(in.Body)))
		t.Result = &WorkResult{SessionID: current.ID, AttemptID: current.AttemptID, Body: in.Body, Digest: digest, CriteriaRevision: t.CriteriaRevision, SubmittedAt: time.Now().UTC(), Method: "human_report_review"}
		t.Status = "reported_complete"
	case "accept":
		if current == nil || activeSession(*current) {
			return WorkAssignment{}, errors.New("stop and settle the executor before accepting its report")
		}
		if t.Result == nil || t.Result.SessionID != current.ID || t.Result.Digest != in.Digest || t.Result.CriteriaRevision != t.CriteriaRevision {
			return WorkAssignment{}, ErrConflict
		}
		for _, child := range m.state.Work {
			if child.ParentID == t.ID && child.Status != "accepted" && child.Status != "cancelled" {
				return WorkAssignment{}, errors.New("child work must be accepted or cancelled first")
			}
		}
		if !validText(in.Body, 8000) {
			return WorkAssignment{}, errors.New("record what you checked before accepting")
		}
		now := time.Now().UTC()
		t.Result.AcceptedAt = &now
		t.Result.Reviewer = "local-operator"
		t.Result.Review = in.Body
		t.Status = "accepted"
	case "cancel":
		if current != nil && activeSession(*current) {
			return WorkAssignment{}, errors.New("stop and settle the executor before cancelling this task")
		}
		for _, child := range m.state.Work {
			if child.ParentID == t.ID && child.Status != "accepted" && child.Status != "cancelled" {
				return WorkAssignment{}, errors.New("settle child tasks before cancelling their parent")
			}
		}
		t.Status = "cancelled"
	default:
		return WorkAssignment{}, errors.New("unknown work action")
	}
	t.Revision++
	t.UpdatedAt = time.Now().UTC()
	copy := *t
	if err = m.saveLocked(); err != nil {
		return WorkAssignment{}, err
	}
	return copy, nil
}
func (m *Manager) MessageResponsibility(project, responsibility, body string) (WorkMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return WorkMessage{}, err
	}
	if !validText(body, 8000) {
		return WorkMessage{}, errors.New("provide a message of at most 8000 bytes")
	}
	for i := range m.state.Responsibilities {
		r := &m.state.Responsibilities[i]
		if r.ID != responsibility || r.ProjectID != project {
			continue
		}
		if len(r.Messages) >= 256 {
			return WorkMessage{}, errors.New("responsibility inbox is full")
		}
		msg := WorkMessage{ID: id("message"), Body: body, CreatedAt: time.Now().UTC()}
		r.Messages = append(r.Messages, msg)
		return msg, m.saveLocked()
	}
	return WorkMessage{}, ErrNotFound
}

type NewWork struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Intent   string `json:"intent"`
	Criteria string `json:"criteria"`
	Role     string `json:"role"`
	GoalID   string `json:"goal_id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
}

func (m *Manager) CreateWork(project string, in NewWork) (WorkAssignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return WorkAssignment{}, err
	}
	if _, err := m.projectLocked(project); err != nil {
		return WorkAssignment{}, err
	}
	if !validText(in.Key, 100) || !validText(in.Title, 160) || !validText(in.Intent, 32000) || !validText(in.Criteria, 16000) {
		return WorkAssignment{}, errors.New("provide an outcome, assignment, completion criteria, and request key")
	}
	if in.Role != "agent" && in.Role != "coordinator" {
		return WorkAssignment{}, errors.New("choose a worker or coordinated execution")
	}
	b, _ := json.Marshal(in)
	digest := fmt.Sprintf("%x", sha256.Sum256(b))
	for _, t := range m.state.Work {
		if t.ProjectID == project && t.RequestKey == in.Key {
			if t.RequestDigest != digest {
				return WorkAssignment{}, ErrConflict
			}
			return t, nil
		}
	}
	if len(m.state.Work) >= 10000 {
		return WorkAssignment{}, errors.New("task capacity reached")
	}
	if in.ParentID != "" {
		parent, err := m.workLocked(in.ParentID)
		if err != nil || parent.ProjectID != project {
			return WorkAssignment{}, ErrNotFound
		}
		if parent.Status == "accepted" || parent.Status == "cancelled" {
			return WorkAssignment{}, ErrConflict
		}
		in.GoalID = parent.GoalID
	}
	if in.GoalID != "" {
		found := false
		for _, g := range m.state.Goals {
			if g.ID == in.GoalID && g.ProjectID == project {
				found = true
			}
		}
		if !found {
			return WorkAssignment{}, ErrNotFound
		}
	}
	now := time.Now().UTC()
	if in.GoalID == "" {
		in.GoalID = id("goal")
		m.state.Goals = append(m.state.Goals, Goal{ID: in.GoalID, ProjectID: project, Title: in.Title, Intent: in.Intent, CreatedAt: now})
	}
	t := WorkAssignment{ID: id("task"), ProjectID: project, GoalID: in.GoalID, ParentID: in.ParentID, Title: in.Title, Intent: in.Intent, Criteria: in.Criteria, Role: in.Role, CriteriaRevision: 1, Revision: 1, Status: "queued", RequestKey: in.Key, RequestDigest: digest, CreatedAt: now, UpdatedAt: now}
	m.state.Work = append(m.state.Work, t)
	if in.Role == "coordinator" {
		r := Responsibility{ID: id("responsibility"), ProjectID: project, TaskID: t.ID, Title: t.Title, Messages: []WorkMessage{}}
		m.state.Responsibilities = append(m.state.Responsibilities, r)
		m.state.Work[len(m.state.Work)-1].ResponsibilityID = r.ID
		t.ResponsibilityID = r.ID
	}
	return t, m.saveLocked()
}
