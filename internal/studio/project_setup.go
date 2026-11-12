package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const setupLimit = 64 << 10
const setupStart = "<!-- mitos:project-setup:start -->"
const setupEnd = "<!-- mitos:project-setup:end -->"

type SetupSections struct {
	Context     string `json:"context"`
	Constraints string `json:"constraints"`
	Git         string `json:"git"`
}
type RuntimePreferences struct {
	Revision int    `json:"revision"`
	Runtime  string `json:"runtime"`
	Model    string `json:"model"`
	Minutes  int    `json:"minutes"`
	Attempts int    `json:"attempts"`
	Guidance string `json:"guidance"`
}
type ProjectSetup struct {
	Warning   string             `json:"warning,omitempty"`
	FileName  string             `json:"file_name"`
	FileHash  string             `json:"file_hash"`
	Exists    bool               `json:"exists"`
	Document  string             `json:"document"`
	Sections  SetupSections      `json:"sections"`
	Project   RuntimePreferences `json:"project"`
	Defaults  RuntimePreferences `json:"defaults"`
	Effective RuntimePreferences `json:"effective"`
}
type FrozenSetup struct {
	Document string `json:"document"`
	FileName string `json:"file_name"`
	FileHash string `json:"file_hash"`
	Personal string `json:"personal,omitempty"`
	Runtime  string `json:"runtime"`
	Model    string `json:"model,omitempty"`
	Minutes  int    `json:"minutes"`
	Attempts int    `json:"attempts"`
}
type InstructionsEdit struct {
	FileHash string         `json:"file_hash"`
	Sections *SetupSections `json:"sections,omitempty"`
	Document *string        `json:"document,omitempty"`
}

func setupHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func readInstructions(root *os.Root) (name, document, digest string, exists bool, err error) {
	name = "AGENTS.md"
	digest = "missing"
	upper, uerr := root.Lstat("AGENTS.md")
	lower, lerr := root.Lstat("agents.md")
	if uerr == nil && lerr == nil && !os.SameFile(upper, lower) {
		err = errors.New("both AGENTS.md and agents.md exist; consolidate them before editing setup")
		return
	}
	if uerr != nil && !errors.Is(uerr, os.ErrNotExist) {
		err = uerr
		return
	}
	if lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
		err = lerr
		return
	}
	if uerr != nil && lerr != nil {
		return
	}
	if uerr != nil {
		name = "agents.md"
	}
	f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		err = e
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		err = e
		return
	}
	if !info.Mode().IsRegular() {
		err = errors.New("instructions must be a regular file")
		return
	}
	raw, e := io.ReadAll(io.LimitReader(f, setupLimit+1))
	if e != nil {
		err = e
		return
	}
	if len(raw) > setupLimit {
		err = errors.New("AGENTS.md exceeds 64 KiB")
		return
	}
	document = string(raw)
	digest = setupHash(raw)
	exists = true
	return
}
func sectionBlock(v SetupSections) string {
	return setupStart + "\n\n<!-- mitos:context -->\n## Project context\n\n" + strings.TrimSpace(v.Context) + "\n\n<!-- mitos:constraints -->\n## Risk, budget and constraints\n\n" + strings.TrimSpace(v.Constraints) + "\n\n<!-- mitos:git -->\n## Git and review workflow\n\n" + strings.TrimSpace(v.Git) + "\n\n" + setupEnd
}
func splitSetup(document string) (SetupSections, int, int, error) {
	start, end := strings.Index(document, setupStart), strings.Index(document, setupEnd)
	if start < 0 && end < 0 {
		return SetupSections{}, -1, -1, nil
	}
	if start < 0 || end < start || strings.Count(document, setupStart) != 1 || strings.Count(document, setupEnd) != 1 {
		return SetupSections{}, 0, 0, errors.New("setup markers are incomplete or duplicated; edit the full document to repair them")
	}
	block := document[start:end]
	markers := []string{"<!-- mitos:context -->\n## Project context\n\n", "<!-- mitos:constraints -->\n## Risk, budget and constraints\n\n", "<!-- mitos:git -->\n## Git and review workflow\n\n"}
	values := []string{}
	positions := []int{}
	for _, marker := range markers {
		positions = append(positions, strings.Index(block, marker))
	}
	if positions[0] < 0 || positions[1] <= positions[0] || positions[2] <= positions[1] {
		return SetupSections{}, 0, 0, errors.New("setup sections are malformed; edit the full document to repair them")
	}
	for i, marker := range markers {
		stop := len(block)
		if i+1 < len(markers) {
			stop = positions[i+1]
		}
		values = append(values, strings.TrimSpace(block[positions[i]+len(marker):stop]))
	}
	return SetupSections{values[0], values[1], values[2]}, start, end + len(setupEnd), nil
}
func validatePreferences(v RuntimePreferences) error {
	if v.Runtime != "" && v.Runtime != "codex" {
		return errors.New("automatic work currently supports Codex; Pi is available only for native sessions")
	}
	if len(v.Model) > 200 || strings.ContainsAny(v.Model, "\x00\r\n") || strings.HasPrefix(v.Model, "-") || v.Minutes < 0 || v.Minutes > 43200 || v.Attempts < 0 || v.Attempts > 2048 || len(v.Guidance) > 16000 || strings.ContainsRune(v.Guidance, 0) {
		return errors.New("invalid runtime preference or limit")
	}
	return nil
}
func (e *Engine) preferencePath(project string) string {
	if project == "" {
		return filepath.Join(e.Store.Dir(), "preferences.json")
	}
	return filepath.Join(e.Store.Dir(), "project-preferences-"+project+".json")
}
func (e *Engine) readPreferences(project string) (RuntimePreferences, error) {
	var v RuntimePreferences
	f, err := os.Open(e.preferencePath(project))
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, setupLimit+1))
	if err != nil {
		return v, err
	}
	if len(raw) > setupLimit {
		return v, errors.New("preferences exceed limit")
	}
	err = json.Unmarshal(raw, &v)
	return v, err
}
func (e *Engine) Preferences(project string) (RuntimePreferences, error) {
	if project != "" {
		if _, err := e.Desk.Project(project); err != nil {
			return RuntimePreferences{}, err
		}
	}
	e.setupMu.Lock()
	defer e.setupMu.Unlock()
	return e.readPreferences(project)
}
func (e *Engine) SavePreferences(project string, v RuntimePreferences) (RuntimePreferences, error) {
	if project != "" {
		if _, err := e.Desk.Project(project); err != nil {
			return v, err
		}
	}
	if err := validatePreferences(v); err != nil {
		return v, err
	}
	e.setupMu.Lock()
	defer e.setupMu.Unlock()
	old, err := e.readPreferences(project)
	if err != nil {
		return v, err
	}
	if v.Revision != old.Revision {
		return v, ErrConflict
	}
	v.Revision++
	raw, _ := json.MarshalIndent(v, "", "  ")
	f, err := os.CreateTemp(e.Store.Dir(), ".preferences-*")
	if err != nil {
		return v, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return v, err
	}
	if err = os.Rename(f.Name(), e.preferencePath(project)); err != nil {
		return v, err
	}
	err = syncDirectory(e.Store.Dir())
	return v, err
}
func effectivePreferences(global, project RuntimePreferences) RuntimePreferences {
	v := global
	if project.Runtime != "" {
		v.Runtime = project.Runtime
	}
	if project.Model != "" {
		v.Model = project.Model
	}
	if project.Minutes != 0 {
		v.Minutes = project.Minutes
	}
	if project.Attempts != 0 {
		v.Attempts = project.Attempts
	}
	if v.Runtime == "" {
		v.Runtime = "codex"
	}
	if v.Minutes == 0 {
		v.Minutes = 120
	}
	if v.Attempts == 0 {
		v.Attempts = 96
	}
	v.Guidance = global.Guidance
	return v
}
func (e *Engine) ProjectSetup(id string) (ProjectSetup, error) {
	project, err := e.Desk.Project(id)
	if err != nil {
		return ProjectSetup{}, err
	}
	e.setupMu.Lock()
	defer e.setupMu.Unlock()
	root, err := os.OpenRoot(project.Directory)
	if err != nil {
		return ProjectSetup{}, err
	}
	defer root.Close()
	name, document, hash, exists, err := readInstructions(root)
	if err != nil {
		return ProjectSetup{}, err
	}
	sections, _, _, parseErr := splitSetup(document)
	warning := ""
	if parseErr != nil {
		warning = parseErr.Error()
	}
	if !exists {
		sections.Context = project.Profile.Context
		sections.Git = strings.Join([]string{project.Profile.GitRules, project.Profile.PushRules, project.Profile.MergeRules, project.Profile.ReviewRules}, "\n\n")
	}
	global, err := e.readPreferences("")
	if err != nil {
		return ProjectSetup{}, err
	}
	local, err := e.readPreferences(id)
	if err != nil {
		return ProjectSetup{}, err
	}
	return ProjectSetup{Warning: warning, FileName: name, FileHash: hash, Exists: exists, Document: document, Sections: sections, Project: local, Defaults: global, Effective: effectivePreferences(global, local)}, nil
}
func (e *Engine) SaveInstructions(id string, req InstructionsEdit) (ProjectSetup, error) {
	p, err := e.Desk.Project(id)
	if err != nil {
		return ProjectSetup{}, err
	}
	err = func() error {
		e.setupMu.Lock()
		defer e.setupMu.Unlock()
		root, err := os.OpenRoot(p.Directory)
		if err != nil {
			return err
		}
		defer root.Close()
		name, old, hash, _, err := readInstructions(root)
		if err != nil {
			return err
		}
		if req.FileHash != hash {
			return ErrConflict
		}
		if (req.Sections == nil) == (req.Document == nil) {
			return errors.New("save sections or the full document")
		}
		var next string
		if req.Document != nil {
			next = *req.Document
		} else {
			all := req.Sections.Context + req.Sections.Constraints + req.Sections.Git
			if strings.Contains(all, "<!-- mitos:") {
				return errors.New("section text cannot contain Mitos control markers")
			}
			_, start, end, err := splitSetup(old)
			if err != nil {
				return err
			}
			block := sectionBlock(*req.Sections)
			if start < 0 {
				next = old
				if next != "" {
					next += "\n\n"
				}
				next += block + "\n"
			} else {
				next = old[:start] + block + old[end:]
			}
		}
		if len(next) > setupLimit || strings.ContainsRune(next, 0) {
			return errors.New("instructions must fit in 64 KiB without NUL bytes")
		}
		if _, _, _, err := splitSetup(next); err != nil {
			return err
		}
		temporary := ".mitos-instructions-" + newID("save")
		mode := os.FileMode(0644)
		if info, err := root.Lstat(name); err == nil {
			mode = info.Mode().Perm()
		}
		f, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		defer root.Remove(temporary)
		// Restore the existing permissions exactly, independent of the process umask.
		if err = f.Chmod(mode); err == nil {
			_, err = f.WriteString(next)
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		_, _, latest, _, err := readInstructions(root)
		if err != nil {
			return err
		}
		if latest != hash {
			return ErrConflict
		}
		if err = root.Rename(temporary, name); err != nil {
			return err
		}
		d, err := root.Open(".")
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}()
	if err != nil {
		return ProjectSetup{}, err
	}
	return e.ProjectSetup(id)
}
func (e *Engine) freezeSetup(id string) (*FrozenSetup, error) {
	v, err := e.ProjectSetup(id)
	if err != nil {
		return nil, err
	}
	if err := validatePreferences(v.Effective); err != nil {
		return nil, err
	}
	document := v.Document
	if !v.Exists {
		document = sectionBlock(v.Sections) + "\n"
	}
	return &FrozenSetup{Document: document, FileName: v.FileName, FileHash: setupHash([]byte(document)), Personal: v.Defaults.Guidance, Runtime: v.Effective.Runtime, Model: v.Effective.Model, Minutes: v.Effective.Minutes, Attempts: v.Effective.Attempts}, nil
}
func (s *FrozenSetup) prompt() string {
	if s == nil {
		return ""
	}
	return fmt.Sprintf("\nPersonal defaults (project instructions and the current request take precedence):\n%s\nFull project instructions (%s, frozen for this work):\n%s\nRuntime: %s; model: %s. Work deadline: %d minutes; attempt allowance: %d. Monetary and qualitative budgets in the document are guidance, not enforced accounting.\n", s.Personal, s.FileName, s.Document, s.Runtime, s.Model, s.Minutes, s.Attempts)
}
