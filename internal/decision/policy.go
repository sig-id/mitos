package decision

import (
	"path"
	"strings"
)

// Act and Grants come from the substrate/provider, never an agent's claim.
// Destinations are exact registered identities, not free-form URL prefixes.
type Act struct {
	Paths        []string `json:"paths"`
	Destinations []string `json:"destinations"`
	Spend        float64  `json:"spend"`
	Unknown      bool     `json:"unknown"`
	MissingInput bool     `json:"missing_input"`
	Judgment     bool     `json:"judgment"`
}
type DestinationGrant struct {
	RequiresApproval bool
	Compensatable    bool
}
type Grants struct {
	WorkspaceConfined   bool
	CheckpointAvailable bool
	RecoverablePaths    []string
	Destinations        map[string]DestinationGrant
	SpendLimit          float64
}
type Assessment struct {
	Reversibility string `json:"reversibility"`
	Disposition   string `json:"disposition"`
	Reason        string `json:"reason"`
}

func Classify(act Act, grants Grants) Assessment {
	external := func(reason string) Assessment { return Assessment{"external", "park", reason} }
	if act.Unknown || !finiteNonnegative(act.Spend) || !finiteNonnegative(grants.SpendLimit) || act.Spend > grants.SpendLimit {
		return external("the consequence is unknown or exceeds the grant")
	}
	if act.MissingInput || act.Judgment {
		return external("the missing fact or judgment requires an explicit answer")
	}
	if !grants.WorkspaceConfined {
		return external("the provider does not enforce workspace confinement")
	}
	recoverable := false
	for _, p := range act.Paths {
		if p == "" || path.IsAbs(p) || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
			return external("a path is outside the verified workspace boundary")
		}
		for _, root := range grants.RecoverablePaths {
			if root != "" && (p == root || strings.HasPrefix(p, root+"/")) {
				recoverable = true
			}
		}
	}
	if len(act.Paths) > 0 && !grants.CheckpointAvailable {
		return external("workspace changes have no restorable checkpoint")
	}
	for _, dest := range act.Destinations {
		g, ok := grants.Destinations[dest]
		if !ok || g.RequiresApproval || !g.Compensatable {
			return external("a destination has no granted compensating action")
		}
		recoverable = true
	}
	if act.Spend > 0 {
		return external("spend is externally visible")
	}
	if recoverable {
		return Assessment{"recoverable", "digest", "the granted compensating action restores the consequence"}
	}
	return Assessment{"internal", "log", "the confined action is undone by checkpoint recovery"}
}
