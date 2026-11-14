// Package compute owns provider admission and process lifecycle, not run policy.
package compute

import (
	"errors"
	"fmt"
	"mitos/internal/resourcepolicy"
	"net/url"
	"slices"
	"time"
)

type Capabilities struct {
	CPUQuota          bool     `json:"cpu_quota"`
	MemoryLimit       bool     `json:"memory_limit"`
	Isolation         string   `json:"isolation"`
	Remote            bool     `json:"remote"`
	Egress            string   `json:"egress"`
	DataMount         string   `json:"data_mount"`
	Snapshot          bool     `json:"snapshot"`
	NetModes          []string `json:"net_modes"`
	WorkspaceConfined bool     `json:"workspace_confined"`
	Metered           bool     `json:"metered"`
}
type Request struct {
	Resources      resourcepolicy.Agent `json:"resources"`
	TrustTier      string               `json:"trust_tier"`
	Provider       string               `json:"provider,omitempty"`
	NetMode        string               `json:"net_mode,omitempty"`
	WorkspaceMode  string               `json:"workspace_mode,omitempty"`
	BrokerEndpoint string               `json:"broker_endpoint,omitempty"`
}
type Workspace struct {
	Source              string `json:"source"`
	Mode                string `json:"mode"`
	Directory           string `json:"directory"`
	HistoryDirectory    string `json:"history_directory,omitempty"`
	BaselineCommit      string `json:"baseline_commit,omitempty"`
	BaselineFingerprint string `json:"baseline_fingerprint,omitempty"`
}
type LaunchSpec struct {
	RunID         string       `json:"run_id"`
	CapabilityRef string       `json:"capability_ref"`
	TrustTier     string       `json:"trust_tier"`
	Provider      string       `json:"provider"`
	Workspace     Workspace    `json:"workspace"`
	Base          string       `json:"base"`
	Harness       string       `json:"harness"`
	Posture       Posture      `json:"posture"`
	Development   bool         `json:"development"`
	Capabilities  Capabilities `json:"capabilities"`
}
type Posture struct {
	Resources      resourcepolicy.Agent `json:"resources"`
	NetMode        string               `json:"net_mode"`
	BrokerEndpoint string               `json:"broker_endpoint,omitempty"`
	DataMount      string               `json:"data_mount"`
	Deadline       time.Time            `json:"deadline"`
}

// Compile is deterministic. The legacy development exception is supplied by the
// local operator's runtime, never inferred from a requested tier or provider.
func Compile(request Request, providers map[string]Capabilities, development bool) (LaunchSpec, error) {
	if err := (resourcepolicy.Policy{Agent: request.Resources}).Validate(); err != nil {
		return LaunchSpec{}, err
	}
	if request.TrustTier == "" {
		request.TrustTier = "T1"
	}
	if request.TrustTier != "T0" && request.TrustTier != "T1" && request.TrustTier != "T2" {
		return LaunchSpec{}, errors.New("trust_tier must be T0, T1, or T2")
	}
	if request.WorkspaceMode == "" {
		request.WorkspaceMode = "copy"
	}
	if request.WorkspaceMode != "copy" && request.WorkspaceMode != "in_place" {
		return LaunchSpec{}, errors.New("unknown workspace mode")
	}
	if request.TrustTier == "T2" && (development || request.WorkspaceMode == "in_place") {
		return LaunchSpec{}, errors.New("T2 forbids development execution and in_place workspaces")
	}
	var broker *url.URL
	if request.BrokerEndpoint != "" {
		var err error
		broker, err = url.Parse(request.BrokerEndpoint)
		if err != nil || broker.User != nil || broker.RawQuery != "" || broker.Fragment != "" || (broker.Scheme != "https" && broker.Scheme != "unix") || (broker.Scheme == "https" && broker.Host == "") || (broker.Scheme == "unix" && (broker.Host != "" || broker.Path == "" || broker.Path[0] != '/')) {
			return LaunchSpec{}, errors.New("broker_endpoint must be a credential-free HTTPS or local unix endpoint")
		}
	}
	netMode := request.NetMode
	if netMode == "" {
		netMode = "deny-all-except-broker"
		if development {
			netMode = "unrestricted"
		}
	}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	slices.Sort(names)
	if request.Provider != "" {
		names = []string{request.Provider}
	}
	var resourceErr error
	for _, name := range names {
		caps, ok := providers[name]
		if !ok || !slices.Contains(caps.NetModes, netMode) {
			continue
		}
		if development {
			if name != "local" || caps.Remote || caps.Isolation != "process" || netMode != "unrestricted" || request.BrokerEndpoint != "" {
				continue
			}
		} else {
			if caps.Isolation != "microvm" && caps.Isolation != "gvisor" && caps.Isolation != "container" {
				continue
			}
			if request.TrustTier == "T0" && caps.Isolation == "container" {
				continue
			}
			if caps.Egress != "broker" || netMode != "deny-all-except-broker" || request.BrokerEndpoint == "" || !caps.WorkspaceConfined {
				continue
			}
			if request.TrustTier == "T2" && (!caps.Remote || caps.Isolation != "microvm" || caps.DataMount != "snapshot-ro" || !caps.Metered) {
				continue
			}
			if caps.Remote && (broker == nil || broker.Scheme != "https") {
				continue
			}
			if request.TrustTier != "T2" && caps.DataMount != "bind-ro" && caps.DataMount != "snapshot-ro" {
				continue
			}
		}
		if err := request.Resources.Check(caps.CPUQuota, caps.MemoryLimit); err != nil {
			resourceErr = err
			continue
		}
		return LaunchSpec{TrustTier: request.TrustTier, Provider: name, Workspace: Workspace{Mode: request.WorkspaceMode}, Harness: "installed-command", Posture: Posture{Resources: request.Resources, NetMode: netMode, BrokerEndpoint: request.BrokerEndpoint, DataMount: caps.DataMount}, Development: development, Capabilities: caps}, nil
	}
	if resourceErr != nil {
		return LaunchSpec{}, resourceErr
	}
	return LaunchSpec{}, fmt.Errorf("no eligible provider for %s with %s; isolation will not be downgraded", request.TrustTier, netMode)
}
