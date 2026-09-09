package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type History struct{ Base, Directory, BaselineCommit string }

type Change struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
	BeforeMode   string `json:"before_mode,omitempty"`
	AfterMode    string `json:"after_mode,omitempty"`
}

type Harvest struct {
	BaselineCommit string   `json:"baseline_commit"`
	ResultCommit   string   `json:"result_commit"`
	Fingerprint    string   `json:"fingerprint"`
	Changes        []Change `json:"changes"`
	Patch          []byte   `json:"-"`
	Snapshot       Snapshot `json:"-"`
}

func Prepare(ctx context.Context, base string, snapshot Snapshot) (*History, error) {
	history := &History{Base: base, Directory: filepath.Join(base, "history.git")}
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(history.Directory); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("workspace history already exists")
	}
	if _, err := git(ctx, base, "", nil, "init", "--bare", "--object-format=sha1", "--template=", history.Directory); err != nil {
		return nil, err
	}
	commit, err := history.commit(ctx, snapshot, "", "Mitos workspace baseline\n")
	if err != nil {
		return nil, err
	}
	history.BaselineCommit = commit
	if _, err = git(ctx, base, history.Directory, nil, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return nil, err
	}
	if _, err = git(ctx, base, history.Directory, nil, "update-ref", "refs/heads/main", commit); err != nil {
		return nil, err
	}
	worktree := filepath.Join(base, "workspace")
	if err = snapshot.WriteTo(worktree); err != nil {
		return nil, err
	}
	if err = history.seedWorktree(ctx, worktree); err != nil {
		return nil, err
	}

	return history, nil
}

// Each materialized tree receives private Git metadata; no object hardlinks or
// remotes can mutate the protected baseline through ordinary worker Git calls.
func (h *History) seedWorktree(ctx context.Context, directory string) error {
	seed, err := os.MkdirTemp(h.Base, "git-seed-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(seed)
	if _, err = git(ctx, h.Base, "", nil, "-c", "protocol.file.allow=always", "clone", "--no-local", "--no-checkout", h.Directory, seed); err != nil {
		return err
	}
	if err = os.Rename(filepath.Join(seed, ".git"), filepath.Join(directory, ".git")); err != nil {
		return err
	}
	// A checkpoint may be reachable only through a private recovery ref, which a
	// normal clone does not fetch. Transfer that exact commit before removing the
	// temporary local origin; do not substitute the source's current HEAD.
	if _, objectErr := git(ctx, directory, "", nil, "cat-file", "-e", h.BaselineCommit+"^{commit}"); objectErr != nil {
		if _, err = git(ctx, directory, "", nil, "-c", "protocol.file.allow=always", "fetch", "--no-tags", "--no-write-fetch-head", h.Directory, h.BaselineCommit); err != nil {
			return err
		}
	}
	if _, err = git(ctx, directory, "", nil, "remote", "remove", "origin"); err != nil {
		return err
	}
	for _, setting := range [][2]string{{"user.name", "Mitos"}, {"user.email", "mitos@localhost"}, {"commit.gpgSign", "false"}, {"core.hooksPath", "/dev/null"}} {
		if _, err = git(ctx, directory, "", nil, "config", setting[0], setting[1]); err != nil {
			return err
		}
	}
	_, err = git(ctx, directory, "", nil, "reset", "--mixed", h.BaselineCommit)
	return err
}

func (h *History) commit(ctx context.Context, snapshot Snapshot, parent, message string) (string, error) {
	if _, err := git(ctx, h.Base, h.Directory, nil, "read-tree", "--empty"); err != nil {
		return "", err
	}
	var index bytes.Buffer
	for _, file := range snapshot.Files {
		if !safePath(file.Path) {
			return "", fmt.Errorf("invalid snapshot path %q", file.Path)
		}
		oid, err := git(ctx, h.Base, h.Directory, file.Data, "hash-object", "-w", "--stdin", "--no-filters")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&index, "%s %s\t%s\x00", file.Mode, strings.TrimSpace(string(oid)), file.Path)
	}
	if _, err := git(ctx, h.Base, h.Directory, index.Bytes(), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	tree, err := git(ctx, h.Base, h.Directory, nil, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", strings.TrimSpace(string(tree))}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	commit, err := git(ctx, h.Base, h.Directory, []byte(message), args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(commit)), nil
}

func (h *History) Harvest(ctx context.Context, before Snapshot) (Harvest, error) {
	after, err := CaptureResult(ctx, filepath.Join(h.Base, "workspace"), DefaultLimits)
	if err != nil {
		return Harvest{}, err
	}
	commit, err := h.commit(ctx, after, h.BaselineCommit, "Mitos workspace result\n")
	if err != nil {
		return Harvest{}, err
	}
	patch, err := git(ctx, h.Base, h.Directory, nil, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", h.BaselineCommit, commit, "--")
	if err != nil {
		return Harvest{}, err
	}
	if _, err = git(ctx, h.Base, h.Directory, nil, "update-ref", "refs/mitos/result", commit); err != nil {
		return Harvest{}, err
	}
	return Harvest{BaselineCommit: h.BaselineCommit, ResultCommit: commit, Fingerprint: after.Fingerprint, Changes: Changes(before, after), Patch: patch, Snapshot: after}, nil
}

func Changes(before, after Snapshot) []Change {
	old := map[string]File{}
	new := map[string]File{}
	for _, file := range before.Files {
		old[file.Path] = file
	}
	for _, file := range after.Files {
		new[file.Path] = file
	}
	paths := map[string]bool{}
	for path := range old {
		paths[path] = true
	}
	for path := range new {
		paths[path] = true
	}
	var ordered []string
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	changes := []Change{}
	for _, path := range ordered {
		a, existsBefore := old[path]
		b, existsAfter := new[path]
		if existsBefore && existsAfter && a.SHA256 == b.SHA256 && a.Mode == b.Mode {
			continue
		}
		kind := "modified"
		if !existsBefore {
			kind = "added"
		}
		if !existsAfter {
			kind = "deleted"
		}
		changes = append(changes, Change{path, kind, a.SHA256, b.SHA256, a.Mode, b.Mode})
	}
	return changes
}

// VerificationTree replays the patch onto a pristine baseline and then checks
// its exact content/mode manifest against harvest. The verifier never consumes
// the mutable worker tree or its .git configuration.
func (h *History) VerificationTree(ctx context.Context, before Snapshot, harvest Harvest, directory string) error {
	if err := before.WriteTo(directory); err != nil {
		return err
	}
	if err := h.seedWorktree(ctx, directory); err != nil {
		return err
	}
	if len(harvest.Patch) > 0 {
		if _, err := git(ctx, directory, "", harvest.Patch, "apply", "--index", "--binary", "--whitespace=nowarn", "--", "-"); err != nil {
			return err
		}
	}
	rebuilt, err := CaptureResult(ctx, directory, DefaultLimits)
	if err != nil {
		return err
	}
	if rebuilt.Fingerprint != harvest.Fingerprint {
		return errors.New("reconstructed acceptance tree does not match the harvested result")
	}
	// Materialization validation includes symlink containment, independently of
	// whether Git accepted a patch that creates a link pointing outside the tree.
	for _, file := range rebuilt.Files {
		if file.Mode == "120000" {
			target := string(file.Data)
			if filepath.IsAbs(target) || !filepath.IsLocal(filepath.Join(filepath.Dir(file.Path), target)) {
				return fmt.Errorf("symlink %q escapes the acceptance tree", file.Path)
			}
		}
	}
	return nil
}

func (h Harvest) Manifest() ([]byte, error) { return json.MarshalIndent(h, "", "  ") }
