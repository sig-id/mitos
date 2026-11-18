package agentcli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
)

// Each environment has passed the same executable and environment checks as a
// native launch. Coordinated adapters can additionally advertise tier models.
type HarnessEnvironment struct {
	ProfileID string         `json:"profile_id"`
	Models    []HarnessModel `json:"models,omitempty"`
}

type HarnessModel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Default bool     `json:"default,omitempty"`
	Efforts []string `json:"efforts"` // nil when the CLI omits effort metadata
}

type modelProbe struct {
	done    chan struct{}
	expires time.Time
	models  []HarnessModel
}

func (r *Registry) ListWithModels(ctx context.Context, workspace string) []Installation {
	list := r.List(workspace)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range list {
		agent := &list[i]
		if agent.Kind != "agent" {
			continue
		}
		profiles := []string{""}
		for _, p := range agent.Profiles {
			profiles = append(profiles, p.ID)
		}
		for _, profile := range profiles {
			launch, err := r.Prepare(Request{AgentID: agent.ID, ProfileID: profile})
			if err != nil {
				continue
			}
			env := &HarnessEnvironment{ProfileID: profile}
			agent.HarnessEnvironments = append(agent.HarnessEnvironments, env)
			if agent.Capabilities.CoordinatedWork {
				wg.Go(func() { env.Models = r.catalog(ctx, launch) })
			}
		}
	}
	wg.Wait()
	return list
}

func (r *Registry) catalog(ctx context.Context, launch Launch) []HarnessModel {
	key := probeKey{launch.AgentID, launch.ProfileID, launch.Executable}
	r.mu.Lock()
	if prior := r.models[key]; prior != nil && (prior.expires.IsZero() || time.Now().Before(prior.expires)) {
		r.mu.Unlock()
		select {
		case <-prior.done:
			return prior.models
		case <-ctx.Done():
			return nil
		}
	}
	p := &modelProbe{done: make(chan struct{})}
	r.models[key] = p
	r.mu.Unlock()
	select {
	case r.modelSlots <- struct{}{}:
		p.models = probeModels(ctx, launch)
		<-r.modelSlots
	case <-ctx.Done():
	}
	ttl := 5 * time.Minute
	if len(p.models) == 0 {
		ttl = 10 * time.Second
	}
	r.mu.Lock()
	p.expires = time.Now().Add(ttl)
	close(p.done)
	r.mu.Unlock()
	return p.models
}

func probeModels(ctx context.Context, launch Launch) []HarnessModel {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"app-server"}
	if launch.AgentID == "claude" {
		args = []string{"--print", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json", "--setting-sources", "", "--strict-mcp-config", "--tools", "", "--no-session-persistence"}
	}
	// Avoid loading a project's hooks or plugins during catalog discovery.
	dir, err := os.MkdirTemp("", "mitos-models-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, launch.Executable, args...)
	cmd.Dir, cmd.Env = dir, launch.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	defer out.Close()
	if cmd.Start() != nil {
		return nil
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	stopRead := context.AfterFunc(ctx, func() { _ = out.Close() })
	defer stopRead()
	wire := bufio.NewScanner(io.LimitReader(out, 4<<20))
	wire.Buffer(make([]byte, 4096), 1<<20)
	send := json.NewEncoder(in)
	if launch.AgentID == "claude" {
		if send.Encode(map[string]any{"type": "control_request", "request_id": "models", "request": map[string]string{"subtype": "initialize"}}) != nil {
			return nil
		}
		return claudeCatalog(wire)
	}
	return codexCatalog(send, wire)
}

type modelCatalog []HarnessModel

var modelID = regexp.MustCompile(`^[[:alnum:]][[:alnum:]._:/+@\[\]-]{0,199}$`)

func (c *modelCatalog) add(id, name string, preferred bool, efforts []string) {
	if !modelID.MatchString(id) || len(*c) >= 256 || slices.ContainsFunc(*c, func(m HarnessModel) bool { return m.ID == id }) {
		return
	}
	model := HarnessModel{ID: id, Default: preferred}
	if name != id && len(name) <= 200 && strings.IndexFunc(name, unicode.IsControl) < 0 {
		model.Name = name
	}
	if efforts != nil {
		model.Efforts = []string{}
	}
	for _, e := range []string{"minimal", "low", "medium", "high", "xhigh", "max"} {
		if slices.Contains(efforts, e) {
			model.Efforts = append(model.Efforts, e)
		}
	}
	*c = append(*c, model)
}

func claudeCatalog(wire *bufio.Scanner) modelCatalog {
	for wire.Scan() {
		var frame struct {
			Type     string
			Response struct {
				ID       string `json:"request_id"`
				Subtype  string
				Response struct {
					Models []struct {
						Value, DisplayName    string
						SupportsEffort        *bool
						SupportedEffortLevels []string
					}
				}
			}
		}
		if json.Unmarshal(wire.Bytes(), &frame) != nil || frame.Type != "control_response" || frame.Response.ID != "models" {
			continue
		}
		var catalog modelCatalog
		if frame.Response.Subtype == "success" {
			for _, m := range frame.Response.Response.Models {
				// "default" is a picker action to clear the model override.
				if m.Value != "default" {
					efforts := m.SupportedEffortLevels
					if m.SupportsEffort != nil && !*m.SupportsEffort {
						efforts = []string{}
					}
					catalog.add(m.Value, m.DisplayName, false, efforts)
				}
			}
		}
		return catalog
	}
	return nil
}

func codexCatalog(send *json.Encoder, wire *bufio.Scanner) modelCatalog {
	call := func(id int, method string, params any, result any) bool {
		if send.Encode(map[string]any{"id": id, "method": method, "params": params}) != nil {
			return false
		}
		for wire.Scan() {
			var frame struct {
				ID     json.RawMessage
				Result json.RawMessage
			}
			if json.Unmarshal(wire.Bytes(), &frame) == nil && string(frame.ID) == strconv.Itoa(id) {
				return len(frame.Result) > 0 && (result == nil || json.Unmarshal(frame.Result, result) == nil)
			}
		}
		return false
	}
	if !call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "mitos", "version": "1"}}, nil) || send.Encode(map[string]string{"method": "initialized"}) != nil {
		return nil
	}
	var catalog modelCatalog
	params := map[string]any{"limit": 100}
	for page := 0; page < 8 && len(catalog) < 256; page++ {
		var result struct {
			Data []struct {
				Model, DisplayName        string
				IsDefault, Hidden         bool
				SupportedReasoningEfforts []struct{ ReasoningEffort string }
			}
			NextCursor string
		}
		if !call(page+2, "model/list", params, &result) {
			break
		}
		for _, m := range result.Data {
			if !m.Hidden {
				var efforts []string
				if m.SupportedReasoningEfforts != nil {
					efforts = make([]string, 0, len(m.SupportedReasoningEfforts))
				}
				for _, e := range m.SupportedReasoningEfforts {
					efforts = append(efforts, e.ReasoningEffort)
				}
				catalog.add(m.Model, m.DisplayName, m.IsDefault, efforts)
			}
		}
		if result.NextCursor == "" || params["cursor"] == result.NextCursor {
			break
		}
		params["cursor"] = result.NextCursor
	}
	return catalog
}
