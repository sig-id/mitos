package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SourcePolicy chooses among operations the local copy adapter can perform.
// The caller records the question and selection before capture uses the answer.
type SourcePolicy func(subject string, facts any, choices []SourceChoice) (string, error)

type submoduleInventory struct {
	Paths                  []string `json:"gitlinks"`
	ParentPaths            int      `json:"parent_paths"`
	DependencyPaths        int      `json:"dependency_paths"`
	LocalCheckoutAvailable bool     `json:"local_checkout_available"`
}

type SourceChoice struct {
	Value       string
	Description string
}

// MaxWorkflowLimits bounds memory used by the in-memory snapshot and archive.
// Larger workspaces require a streaming adapter.
var MaxWorkflowLimits = Limits{Bytes: 512 << 20, Files: 32000}

func (l Limits) Effective() Limits {
	if l == (Limits{}) {
		return DefaultLimits
	}
	return Limits{Bytes: min(l.Bytes, MaxWorkflowLimits.Bytes), Files: min(l.Files, MaxWorkflowLimits.Files)}
}

// CaptureWorkflowSource preserves the normal file selection until preparation
// encounters a genuine choice. The core copy operation still enforces its bounds.
func CaptureWorkflowSource(ctx context.Context, source, dataDir string, choose SourcePolicy) (Snapshot, error) {
	paths, err := sourcePaths(ctx, source)
	if err != nil {
		return Snapshot{}, err
	}
	modules, err := sourceGitlinks(ctx, source)
	if err != nil {
		return Snapshot{}, err
	}
	var omitted []string
	if len(modules) > 0 {
		parent, err := expandGitlinks(ctx, source, paths, modules, false, dataDir)
		if err != nil {
			return Snapshot{}, err
		}
		expanded, checkoutErr := expandGitlinks(ctx, source, paths, modules, true, dataDir)
		if ctx.Err() != nil {
			return Snapshot{}, ctx.Err()
		}
		facts := submoduleInventory{Paths: modules, ParentPaths: len(parent), LocalCheckoutAvailable: checkoutErr == nil}
		options := []SourceChoice{}
		if checkoutErr == nil {
			facts.DependencyPaths = len(expanded) - len(parent)
			options = append(options, SourceChoice{"include", "Use the complete local checkout for the assignment. Copy tracked and non-ignored submodule sources into the private workspace. No network access, checkout update or source mutation is required."})
		}
		options = append(options, SourceChoice{"omit", "Use the parent project's files. Its indexed submodule checkouts will be absent, and this limitation will be recorded."}, SourceChoice{"stop", "Stop preparation if the assignment requires a different source layout."})
		choice, err := choose("workspace_submodules", facts, options)
		if err != nil {
			return Snapshot{}, err
		}
		switch choice {
		case "include":
			if checkoutErr != nil {
				return Snapshot{}, checkoutErr
			}
			paths = expanded
		case "omit":
			omitted = modules
			paths = parent
		default:
			return Snapshot{}, errors.New("workspace submodule materialization was not selected")
		}
	}
	limits := DefaultLimits
	snapshot, err := capture(ctx, source, paths, dataDir, limits, false)
	var excess *LimitError
	if errors.As(err, &excess) && excess.Bytes <= MaxWorkflowLimits.Bytes && excess.Files <= MaxWorkflowLimits.Files {
		// Round upwards to leave space for worker edits within the chosen budget.
		for limits.Bytes < excess.Bytes {
			limits.Bytes = min(2*limits.Bytes, MaxWorkflowLimits.Bytes)
		}
		for limits.Files < excess.Files {
			limits.Files = min(2*limits.Files, MaxWorkflowLimits.Files)
		}
		choice, choiceErr := choose("workspace_capacity", struct {
			Bytes     int64  `json:"checkout_bytes"`
			Files     int    `json:"checkout_files"`
			Current   Limits `json:"current_limit"`
			Requested Limits `json:"proposed_limit"`
		}{excess.Bytes, excess.Files, DefaultLimits, limits}, []SourceChoice{
			{"expand", "Allocate the proposed bounded private workspace for the complete checkout and its results."},
			{"stop", "Keep the original workspace allowance and stop this attempt."},
		})
		if choiceErr != nil {
			return Snapshot{}, choiceErr
		}
		if choice == "expand" {
			snapshot, err = capture(ctx, source, paths, dataDir, limits, false)
			snapshot.Limits = limits
		}
	}
	snapshot.OmittedSubmodules = omitted
	return snapshot, err
}

// Gitlinks are index entries with mode 160000. Directory shape alone cannot
// distinguish a submodule from an unrelated embedded repository.
func sourceGitlinks(ctx context.Context, dir string) ([]string, error) {
	raw, err := git(ctx, dir, "", nil, "ls-files", "--stage", "-z", "--", ".")
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, row := range splitPaths(raw) {
		if !strings.HasPrefix(row, "160000 ") {
			continue
		}
		header, path, ok := strings.Cut(row, "\t")
		if !ok || !strings.HasSuffix(header, " 0") || !safePath(path) || filepath.Clean(path) != path {
			return nil, errors.New("invalid or conflicted submodule index entry")
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func expandGitlinks(ctx context.Context, dir string, paths, modules []string, include bool, dataDir string) ([]string, error) {
	links := make(map[string]bool, len(modules))
	for _, path := range modules {
		links[path] = true
	}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if !links[path] {
			result = append(result, path)
			continue
		}
		module := filepath.Join(dir, path)
		if !include || dataDir != "" && within(dataDir, module) {
			continue
		}
		actual, err := filepath.EvalSymlinks(module)
		if err != nil || actual != module {
			return nil, fmt.Errorf("submodule %q requires a local directory checkout", path)
		}
		metadata, err := os.Lstat(filepath.Join(module, ".git"))
		if err != nil || !(metadata.IsDir() || metadata.Mode().IsRegular()) {
			return nil, fmt.Errorf("submodule %q is not initialized locally", path)
		}
		children, err := sourcePaths(ctx, module)
		if err != nil {
			return nil, err
		}
		nested, err := sourceGitlinks(ctx, module)
		if err != nil {
			return nil, err
		}
		if len(nested) > 0 {
			children, err = expandGitlinks(ctx, module, children, nested, true, dataDir)
			if err != nil {
				return nil, err
			}
		}
		for _, child := range children {
			if !safePath(child) {
				return nil, fmt.Errorf("invalid submodule path %q", child)
			}
			result = append(result, filepath.Join(path, child))
		}
	}
	return result, nil
}
