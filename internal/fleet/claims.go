package fleet

import (
	"errors"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

type Ownership struct {
	Paths  []string `json:"paths"`
	Leases []string `json:"leases"`
}

func (o Ownership) Validate() error {
	if len(o.Paths) > 128 || len(o.Leases) > 32 {
		return errors.New("ownership allows at most 128 path prefixes and 32 leases")
	}
	seen := map[string]bool{}
	for _, p := range o.Paths {
		if p == "" || len(p) > 1024 || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\*?[\x00\n\r") || seen["path:"+p] {
			return errors.New("owned paths must be unique literal workspace-relative prefixes")
		}
		seen["path:"+p] = true
	}
	for _, name := range o.Leases {
		if name == "" || len(name) > 128 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\x00\n\r\t") || seen["lease:"+name] {
			return errors.New("leases require unique bounded resource names")
		}
		seen["lease:"+name] = true
	}
	return nil
}
func (o Ownership) Contains(p string) bool {
	for _, owned := range o.Paths {
		if owned == "." || p == owned || strings.HasPrefix(p, owned+"/") {
			return true
		}
	}
	return false
}

type Claim struct {
	ID            string     `json:"claim_id"`
	RunID         string     `json:"run_id"`
	Workspace     string     `json:"workspace"`
	Ownership     Ownership  `json:"ownership"`
	AcquiredAt    time.Time  `json:"acquired_at"`
	ReleasedAt    *time.Time `json:"released_at,omitempty"`
	ReleaseReason string     `json:"release_reason,omitempty"`
}
type Conflict struct {
	RunID    string `json:"run_id"`
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
}

func Conflicts(workspace string, ownership Ownership, held []Claim) []Conflict {
	conflicts := []Conflict{}
	for _, claim := range held {
		if claim.ReleasedAt != nil {
			continue
		}
		for _, name := range ownership.Leases {
			for _, other := range claim.Ownership.Leases {
				if name == other {
					conflicts = append(conflicts, Conflict{claim.RunID, "lease", name})
				}
			}
		}
		for _, p := range ownership.Paths {
			candidate := filepath.Join(workspace, filepath.FromSlash(p))
			for _, other := range claim.Ownership.Paths {
				existing := filepath.Join(claim.Workspace, filepath.FromSlash(other))
				if contains(candidate, existing) || contains(existing, candidate) {
					conflicts = append(conflicts, Conflict{claim.RunID, "path", candidate})
				}
			}
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		a, b := conflicts[i], conflicts[j]
		if a.RunID != b.RunID {
			return a.RunID < b.RunID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Resource < b.Resource
	})
	return slices.Compact(conflicts)
}
func contains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
