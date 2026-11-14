package agentcli

import (
	"errors"
	"strings"
)

// LaunchDefaults are operator-owned settings for native interactive sessions.
// Args are individual argv entries, never shell code.
type LaunchDefaults struct {
	ProfileID string   `json:"profile_id,omitempty"`
	Args      []string `json:"args"`
}

func (d LaunchDefaults) Validate() error {
	if len(d.ProfileID) > 100 || len(d.Args) > 64 {
		return errors.New("agent defaults exceed limit")
	}
	size := 0
	for _, arg := range d.Args {
		size += len(arg)
		if arg == "" || strings.ContainsRune(arg, 0) {
			return errors.New("launch arguments must be nonempty and contain no NUL bytes")
		}
	}
	if size > 8000 {
		return errors.New("launch arguments exceed 8000 bytes")
	}
	return nil
}
