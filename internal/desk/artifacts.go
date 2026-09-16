package desk

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type Scratch struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	Partial   bool   `json:"partial"`
	Hold      string `json:"hold"`
}
type GitActivity struct {
	SessionID string `json:"session_id"`
	Command   string `json:"command"`
	At        string `json:"at"`
}

func scratchSize(path string) (int64, bool) {
	var n int64
	count := 0
	partial := false
	until := time.Now().Add(100 * time.Millisecond)
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			partial = true
			return nil
		}
		if time.Now().After(until) || count >= 2000 {
			partial = true
			return fs.SkipAll
		}
		count++
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				partial = true
			} else {
				n += info.Size()
			}
		}
		return nil
	})
	return n, partial
}
func (m *Manager) scratchHoldLocked(project string) string {
	for _, s := range m.state.Sessions {
		if s.ProjectID == project {
			switch s.Status {
			case "running", "starting", "stopping":
				return "A project session is active; scratch may still be in use."
			case "interrupted":
				return "An interrupted session needs process inspection."
			}
		}
	}
	for _, j := range m.state.Jobs {
		if j.ProjectID == project {
			switch j.Status {
			case "running", "queued", "interrupted":
				return "A managed job still holds project resources."
			}
		}
	}
	return ""
}

// Explicit local-operator cleanup only. It is limited to managed scratch and
// never treats arbitrary untracked or ignored project files as disposable.
func (m *Manager) CleanScratch(session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	s, err := m.sessionLocked(session)
	if err != nil {
		return err
	}
	if hold := m.scratchHoldLocked(s.ProjectID); hold != "" {
		return errors.New(hold)
	}
	if s.Scratch == "" {
		return nil
	}
	root, err := os.OpenRoot(m.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.RemoveAll(s.ID + ".tmp"); err != nil {
		return err
	}
	s.Scratch = ""
	return m.saveLocked()
}
func (m *Manager) artifactViews(project string) ([]Scratch, []GitActivity) {
	m.mu.Lock()
	hold := m.scratchHoldLocked(project)
	m.mu.Unlock()
	scratch := []Scratch{}
	git := []GitActivity{}
	sessions := m.Sessions(project)
	if len(sessions) > 64 {
		sessions = sessions[len(sessions)-64:]
	}
	for _, s := range sessions {
		if s.Scratch != "" {
			n, partial := scratchSize(s.Scratch)
			scratch = append(scratch, Scratch{SessionID: s.ID, Path: s.Scratch, Bytes: n, Partial: partial, Hold: hold})
		}
		if s.Scratch == "" {
			continue
		}
		f, err := os.Open(filepath.Join(s.Scratch, "git.jsonl"))
		if err != nil {
			continue
		}
		// Bounded inspection of the latest trace tail. No argv or environment is
		// exposed: command names establish actor provenance, not file authorship.
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			continue
		}
		start := max(int64(0), info.Size()-(128<<10))
		_, _ = f.Seek(start, io.SeekStart)
		scan := bufio.NewScanner(io.LimitReader(f, 128<<10))
		scan.Buffer(make([]byte, 4096), 64<<10)
		if start > 0 {
			scan.Scan()
		}
		for scan.Scan() {
			var row struct {
				Event string `json:"event"`
				Name  string `json:"name"`
				Time  string `json:"time"`
			}
			if json.Unmarshal(scan.Bytes(), &row) == nil && row.Event == "cmd_name" {
				git = append(git, GitActivity{SessionID: s.ID, Command: row.Name, At: row.Time})
			}
		}
		_ = f.Close()
	}
	if len(git) > 128 {
		git = git[len(git)-128:]
	}
	return scratch, git
}
