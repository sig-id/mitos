package observation

import (
	"errors"
	"path/filepath"
	"time"
)

// Config belongs to a trusted installed adapter, never to the worker's output.
// The first adapter is a local command with an optional normalized JSONL tail.
type Config struct {
	ThinkTimeP95MS int64        `json:"think_time_p95_ms"`
	Session        *SessionSpec `json:"session_artifacts,omitempty"`
}

type SessionSpec struct {
	Path          string `json:"path_template"`
	Format        string `json:"format"`
	SchemaVersion string `json:"schema_version"`
	Tail          bool   `json:"tail"`
}

func (c Config) Validate() error {
	if c.ThinkTimeP95MS <= 0 || c.ThinkTimeP95MS > int64((24*time.Hour)/time.Millisecond) {
		return errors.New("observation needs a measured think_time_p95_ms between 1 and 86400000")
	}
	if c.Session != nil {
		s := c.Session
		if !filepath.IsLocal(s.Path) || s.Path == "." || s.Format != "mitos-jsonl" || s.SchemaVersion != "1" || !s.Tail {
			return errors.New("session_artifacts requires a local path, format mitos-jsonl, schema_version 1, and tail true")
		}
	}
	return nil
}

type Capabilities struct {
	StructuredOutput   string       `json:"structured_output"`
	SessionArtifacts   *SessionSpec `json:"session_artifacts,omitempty"`
	Interactive        bool         `json:"interactive"`
	PTYControl         bool         `json:"pty_control"`
	Resume             string       `json:"resume"`
	ThinkTimeP95MS     int64        `json:"think_time_p95_ms"`
	ExecCoverage       string       `json:"exec_coverage"`
	UnattendedEligible bool         `json:"unattended_eligible"`
}

func (c Config) Capabilities() Capabilities {
	return Capabilities{StructuredOutput: "none", SessionArtifacts: c.Session, Resume: "none", ThinkTimeP95MS: c.ThinkTimeP95MS, ExecCoverage: "sampled_procfs", UnattendedEligible: false}
}
