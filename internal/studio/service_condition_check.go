package studio

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mitos/internal/compute"
	workspacefs "mitos/internal/workspace"
)

// Globs inspect the selected source, including ignored control files. Walk only
// matching directory components, never execute shell expansion, and cap reads.
func conditionGlob(ctx context.Context, root *os.Root, pattern string) (bool, error) {
	parts := strings.Split(pattern, "/")
	visited := 0
	var walk func(string, int) (bool, error)
	walk = func(dir string, depth int) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		f, err := root.Open(dir)
		if err != nil {
			return false, err
		}
		defer f.Close()
		for {
			entries, readErr := f.ReadDir(128)
			for _, entry := range entries {
				visited++
				if visited > 10000 {
					return false, errors.New("condition glob exceeded 10000 directory entries")
				}
				if err := ctx.Err(); err != nil {
					return false, err
				}
				if strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(parts[depth], ".") {
					continue
				}
				matches, _ := path.Match(parts[depth], entry.Name())
				if !matches {
					continue
				}
				name := path.Join(dir, entry.Name())
				info, err := root.Stat(name)
				if err != nil {
					return false, err
				}
				if depth == len(parts)-1 {
					return true, nil
				}
				if info.IsDir() {
					found, err := walk(name, depth+1)
					if err != nil || found {
						return found, err
					}
				}
			}
			if readErr == io.EOF {
				return false, nil
			}
			if readErr != nil {
				return false, readErr
			}
		}
	}
	return walk(".", 0)
}
func conditionEnvironment() map[string]string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}
func conditionEnvironmentFacts(h *ServiceConditionState, values map[string]string) bool {
	add := func(kind, name string, passed bool) bool {
		h.Facts = append(h.Facts, ServiceConditionFact{Kind: kind, Name: name, Passed: passed})
		return passed
	}
	if h.Policy.EnvSet != "" && !add("env_set", h.Policy.EnvSet, values[h.Policy.EnvSet] != "") {
		return false
	}
	for _, kind := range []string{"env_equals", "env_not_equals"} {
		wanted := h.Policy.EnvEquals
		if kind == "env_not_equals" {
			wanted = h.Policy.EnvNotEquals
		}
		names := []string{}
		for name := range wanted {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			passed := values[name] == wanted[name]
			if kind == "env_not_equals" {
				passed = !passed
			}
			if !add(kind, name, passed) {
				return false
			}
		}
	}
	return true
}
func (e *Engine) checkServiceCondition(ctx context.Context, r Run) error {
	h, err := e.Store.beginServiceCondition(r.ID)
	if err != nil || h == nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	h.Status = "passed"
	// Sample the server environment once. Actual values and command output are not
	// placed in evidence or passed to the condition subprocess.
	values := conditionEnvironment()
	if !conditionEnvironmentFacts(h, values) {
		h.Status = "skipped"
		h.Detail = "An environment condition did not match."
	}
	source := ""
	if h.Status == "passed" && (h.Policy.FileExists != "" || h.Policy.FileNotExists != "" || h.Policy.Command != "") {
		source, err = e.Workspace(r.Brief.Workspace)
		if err != nil {
			h.Status = "error"
			h.Detail = "The condition workspace is unavailable."
		}
	}
	if h.Status == "passed" && (h.Policy.FileExists != "" || h.Policy.FileNotExists != "") {
		root, openErr := os.OpenRoot(source)
		if openErr != nil {
			h.Status = "error"
			h.Detail = "The condition workspace cannot be opened."
		} else {
			for _, kind := range []string{"file_exists", "file_not_exists"} {
				pattern := h.Policy.FileExists
				if kind == "file_not_exists" {
					pattern = h.Policy.FileNotExists
				}
				if pattern == "" {
					continue
				}
				found, globErr := conditionGlob(checkCtx, root, pattern)
				if globErr != nil {
					h.Status = "error"
					h.Detail = "Condition glob lookup failed, escaped its root, or exceeded its time/entry limit."
					break
				}
				passed := found
				if kind == "file_not_exists" {
					passed = !found
				}
				h.Facts = append(h.Facts, ServiceConditionFact{Kind: kind, Name: pattern, Passed: passed})
				if !passed {
					h.Status = "skipped"
					h.Detail = "A file condition did not match."
					break
				}
			}
			_ = root.Close()
		}
	}
	if h.Status == "passed" && h.Policy.Command != "" {
		output, commandErr := e.runConditionCommand(checkCtx, r, h, source)
		sum := sha256.Sum256(output)
		h.OutputSHA256 = fmt.Sprintf("%x", sum)
		h.OutputBytes = len(output)
		h.Facts = append(h.Facts, ServiceConditionFact{Kind: "command", Passed: commandErr == nil})
		if commandErr != nil {
			h.Status = "skipped"
			h.Detail = "The condition command could not run or did not exit successfully within its deadline."
		}
	}
	if checkCtx.Err() != nil {
		h.Status = "error"
		h.Detail = "The condition check exceeded its deadline."
	}
	if ctx.Err() != nil {
		h.Status = "interrupted"
		h.Detail = "Execution stopped during the condition check."
	}
	if err = e.Store.finishServiceCondition(r.ID, h); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	current, err := e.Store.Get(r.ID)
	if err != nil {
		return err
	}
	if current.Status != "running" || current.Condition.Status != "passed" {
		return errServiceCondition
	}
	return nil
}
func (e *Engine) runConditionCommand(ctx context.Context, r Run, h *ServiceConditionState, source string) ([]byte, error) {
	app, ok := e.App(r.Brief.AppID)
	if r.Execution != nil {
		app = r.Execution.Application
		ok = true
	}
	if !ok {
		return nil, ErrNotFound
	}
	launch, err := compileApplication(app)
	if err != nil {
		return nil, err
	}
	if r.Execution != nil && r.Execution.Launch != nil {
		launch = *r.Execution.Launch
	}
	base := filepath.Join(e.Store.Dir(), "conditions", r.ID, h.CheckID)
	before, err := workspacefs.CaptureSource(ctx, source, e.Store.Dir(), workspacefs.DefaultLimits)
	if err != nil {
		return nil, err
	}
	history, err := workspacefs.Prepare(ctx, base, before)
	if err != nil {
		return nil, err
	}
	launch.RunID = r.ID
	launch.CapabilityRef = app.ID
	launch.Base = base
	launch.Workspace = compute.Workspace{Mode: launch.Workspace.Mode, Directory: filepath.Join(base, "workspace"), HistoryDirectory: history.Directory, BaselineCommit: history.BaselineCommit, BaselineFingerprint: before.Fingerprint}
	launch.Posture.Deadline, _ = ctx.Deadline()
	_, output, err := executeProviderCommand(ctx, e.provider, launch, []string{"/bin/sh", "-c", h.Policy.Command}, launch.Workspace.Directory, nil, nil)
	return output, err
}
