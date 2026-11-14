package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Registry struct {
	adapters []Adapter
	profiles []Profile
	env      map[string]string
	mu       sync.RWMutex
	versions map[probeKey]probe
}

type probeKey struct{ agent, profile, executable string }

type probe struct {
	version, status string
	at              time.Time
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var versionNumber = regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?\b`)

func Open(path string, environ []string) (*Registry, error) {
	var config Config
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if info.Size() > 256<<10 {
			return nil, errors.New("agent catalog exceeds 256 KiB")
		}
		decoder := json.NewDecoder(io.LimitReader(file, 256<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return nil, fmt.Errorf("agent catalog: %w", err)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return nil, errors.New("agent catalog must contain one JSON object")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return New(Builtins(), config, environ)
}

func New(adapters []Adapter, config Config, environ []string) (*Registry, error) {
	r := &Registry{adapters: append([]Adapter{}, adapters...), profiles: config.Profiles, env: make(map[string]string), versions: make(map[probeKey]probe)}
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			r.env[name] = value
		}
	}
	for _, definition := range config.Applications {
		if definition.Kind != "application" && definition.Kind != "agent" {
			return nil, errors.New("catalog applications require kind agent or application")
		}
		r.adapters = append(r.adapters, CLI{definition})
	}
	ids := make(map[string]bool)
	for _, adapter := range r.adapters {
		d := adapter.Definition()
		if !identifier.MatchString(d.ID) || ids[d.ID] || strings.TrimSpace(d.Name) == "" || len(d.Executables) == 0 || (d.Prompt != "none" && d.Prompt != "positional" && !strings.HasPrefix(d.Prompt, "--")) {
			return nil, fmt.Errorf("invalid or duplicate agent definition %q", d.ID)
		}
		if d.SessionProtocol != "" && d.SessionProtocol != "codex" && d.SessionProtocol != "claude" && d.SessionProtocol != "opencode" && d.SessionProtocol != "pi" {
			return nil, fmt.Errorf("unknown session protocol %q", d.SessionProtocol)
		}
		ids[d.ID] = true
	}
	profileIDs := make(map[string]bool)
	for _, p := range r.profiles {
		if !identifier.MatchString(p.ID) || profileIDs[p.ID] || !ids[p.AgentID] || strings.TrimSpace(p.Name) == "" {
			return nil, fmt.Errorf("invalid or duplicate agent profile %q", p.ID)
		}
		profileIDs[p.ID] = true
		for name, source := range p.Env {
			if !environmentName.MatchString(name) || !environmentName.MatchString(source) {
				return nil, fmt.Errorf("profile %s env values must name server environment variables", p.ID)
			}
		}
		for _, name := range p.Unset {
			if !environmentName.MatchString(name) {
				return nil, fmt.Errorf("invalid unset variable in profile %s", p.ID)
			}
			if _, ok := p.Env[name]; ok {
				return nil, fmt.Errorf("profile %s both sets and unsets %s", p.ID, name)
			}
		}
	}
	return r, nil
}

func (r *Registry) adapter(id string) (Adapter, error) {
	for _, a := range r.adapters {
		if a.Definition().ID == id {
			return a, nil
		}
	}
	return nil, fmt.Errorf("unknown agent %q", id)
}

func (r *Registry) environment(agent, profile string) (map[string]string, *Profile, error) {
	env := make(map[string]string, len(r.env))
	for key, value := range r.env {
		env[key] = value
	}
	if profile == "" {
		return env, nil, nil
	}
	for _, p := range r.profiles {
		if p.ID != profile {
			continue
		}
		if p.AgentID != agent {
			return nil, nil, errors.New("profile belongs to a different agent")
		}
		for _, key := range p.Unset {
			delete(env, key)
		}
		for key, source := range p.Env {
			value, present := r.env[source]
			if !present || value == "" {
				return nil, nil, fmt.Errorf("profile %s requires environment variable %s", p.ID, source)
			}
			env[key] = value
		}
		return env, &p, nil
	}
	return nil, nil, fmt.Errorf("unknown environment profile %q", profile)
}

func executable(names []string, env map[string]string) string {
	for _, name := range names {
		candidates := []string{name}
		if !filepath.IsAbs(name) {
			if strings.ContainsRune(name, filepath.Separator) {
				continue
			}
			candidates = nil
			for _, dir := range filepath.SplitList(env["PATH"]) {
				if filepath.IsAbs(dir) {
					candidates = append(candidates, filepath.Join(dir, name))
				}
			}
		}
		for _, path := range candidates {
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
				return path
			}
		}
	}
	return ""
}

func envSlice(env map[string]string) []string {
	entries := make([]string, 0, len(env))
	for key, value := range env {
		entries = append(entries, key+"="+value)
	}
	sort.Strings(entries)
	return entries
}

func (r *Registry) Prepare(request Request) (Launch, error) {
	return r.prepare(request, "new", "")
}

func (r *Registry) PrepareContinuation(request Request, mode, reference string) (Launch, error) {
	if mode != "resume" && mode != "fork" {
		return Launch{}, errors.New("unknown native session operation")
	}
	return r.prepare(request, mode, reference)
}

func (r *Registry) prepare(request Request, mode, reference string) (Launch, error) {
	if request.Bridge && (mode != "new" || (request.AgentID != "codex" && request.AgentID != "claude")) {
		return Launch{}, errors.New("bridge sessions support new Codex or Claude sessions only")
	}
	adapter, err := r.adapter(request.AgentID)
	if err != nil {
		return Launch{}, err
	}
	if err := request.Defaults.Validate(); err != nil {
		return Launch{}, err
	}
	if request.ProfileID == "" {
		request.ProfileID = request.Defaults.ProfileID
	}
	env, profile, err := r.environment(request.AgentID, request.ProfileID)
	if err != nil {
		return Launch{}, err
	}
	d := adapter.Definition()
	names := d.Executables
	if profile != nil && profile.Executable != "" {
		names = []string{profile.Executable}
	}
	path := executable(names, env)
	if path == "" {
		return Launch{}, fmt.Errorf("%s is not installed on the selected PATH", d.Name)
	}
	nativeID := ""
	if mode == "new" && (d.SessionProtocol == "claude" || d.SessionProtocol == "pi") {
		nativeID = newSessionID()
		reference = nativeID
	} else if mode == "resume" {
		nativeID = reference
	}
	if len(request.Defaults.Args) > 0 {
		cli, ok := adapter.(CLI)
		if !ok {
			return Launch{}, errors.New("this adapter does not support launch flags")
		}
		cli.Spec.Args = append(append([]string{}, cli.Spec.Args...), request.Defaults.Args...)
		adapter = cli
	}
	args, err := adapter.SessionArguments(mode, reference, request.Prompt)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Bridge: request.Bridge, BridgePrompt: request.Prompt, NativeSessionID: nativeID, AgentID: d.ID, AgentName: d.Name, ProfileID: request.ProfileID, Kind: d.Kind, Executable: path, Args: args, Env: envSlice(env)}, nil
}

func (r *Registry) List(workspace string) []Installation {
	list := make([]Installation, 0, len(r.adapters))
	for _, adapter := range r.adapters {
		installation, _ := r.Inspect(adapter.Definition().ID, "", workspace)
		list = append(list, installation)
	}
	return list
}

func (r *Registry) Inspect(id, profileID, workspace string) (Installation, error) {
	adapter, err := r.adapter(id)
	if err != nil {
		return Installation{}, err
	}
	env, profile, err := r.environment(id, profileID)
	if err != nil {
		return Installation{}, err
	}
	d := adapter.Definition()
	names := d.Executables
	if profile != nil && profile.Executable != "" {
		names = []string{profile.Executable}
	}
	path := executable(names, env)
	i := Installation{Definition: d, Executable: path, Installed: path != "", Probe: "not_probed", Auth: "native authentication not verified", Profiles: []Profile{}, Environment: []Environment{}, Resources: adapter.Resources(workspace, env), Capabilities: adapter.Capabilities()}
	if path == "" {
		i.Probe = "not_installed"
	}
	r.mu.RLock()
	if p, ok := r.versions[probeKey{id, profileID, path}]; ok {
		i.Version, i.Probe, i.CheckedAt = p.version, p.status, p.at
	}
	r.mu.RUnlock()
	envNames := append([]string{}, d.EnvNames...)
	if profile != nil {
		for name := range profile.Env {
			envNames = append(envNames, name)
		}
		envNames = append(envNames, profile.Unset...)
	}
	sort.Strings(envNames)
	seen := make(map[string]bool)
	for _, name := range envNames {
		if seen[name] {
			continue
		}
		seen[name] = true
		source := name
		if profile != nil && profile.Env[name] != "" {
			source = profile.Env[name]
		}
		i.Environment = append(i.Environment, Environment{name, source, env[name] != ""})
	}
	for _, p := range r.profiles {
		if p.AgentID == id {
			i.Profiles = append(i.Profiles, p)
		}
	}
	return i, nil
}

// Version probes never start interactive sessions or authentication flows. They
// are bounded and only a version token is published, never raw process output.
func (r *Registry) Refresh(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, a := range r.adapters {
		versionArgs := a.Definition().VersionArgs
		if len(versionArgs) == 0 {
			continue
		}
		profiles := []string{""}
		for _, p := range r.profiles {
			if p.AgentID == a.Definition().ID {
				profiles = append(profiles, p.ID)
			}
		}
		for _, profile := range profiles {
			launch, err := r.Prepare(Request{AgentID: a.Definition().ID, ProfileID: profile})
			if err != nil {
				continue
			}
			key := probeKey{launch.AgentID, launch.ProfileID, launch.Executable}
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-slots }()
				probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				cmd := exec.CommandContext(probeCtx, launch.Executable, versionArgs...)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
				cmd.Env, cmd.WaitDelay = launch.Env, 100*time.Millisecond
				buffer := &boundedOutput{}
				cmd.Stdout, cmd.Stderr = buffer, io.Discard
				err := cmd.Run()
				p := probe{status: "unavailable", at: time.Now().UTC()}
				if err == nil {
					p.version = versionNumber.FindString(string(buffer.data))
					if p.version != "" {
						p.status = "ok"
					}
				}
				if probeCtx.Err() != nil {
					p.status = "timeout"
				}
				r.mu.Lock()
				r.versions[key] = p
				r.mu.Unlock()
			}()
		}
	}
	wg.Wait()
}

type boundedOutput struct{ data []byte }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := min(len(p), (16<<10)-len(b.data))
	b.data = append(b.data, p[:n]...)
	return len(p), nil
}
