package studio

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"mitos/internal/agentcli"
	"mitos/internal/compute"
)

// confinementStageLimit bounds how much native authentication configuration is
// copied into a sandbox home: login files are small, and anything larger is
// refused rather than silently truncating credentials.
const confinementStageLimit = 8 << 20

// agentConfinementEnv names, per runtime, the environment variable holding its
// authentication/configuration directory and the default location inside the
// user's home directory.
var agentConfinementEnv = map[string]struct{ variable, defaultDir string }{
	"codex":  {"CODEX_HOME", ".codex"},
	"claude": {"CLAUDE_CONFIG_DIR", ".claude"},
}

// confinementBlocksTransport reports whether the configured confinement denies
// the network the agent runtime itself needs to reach its model endpoint.
// Bubblewrap's unshared network namespace provides only a down loopback, and no
// mediated transport is wired yet, so denying tool egress currently denies the
// model transport as well: admission refuses instead of launching an agent that
// can never answer.
func confinementBlocksTransport(c compute.Confinement) bool {
	return c.Enabled() && c.NetMode != compute.NetUnrestricted
}

// stageConfinementHome stages the runtime's native authentication directory
// into the sandbox home and rewrites the environment to point at it. The host
// home is never mounted; only the staged configuration and the fresh sandbox
// home are visible.
func stageConfinementHome(base, runtime string, env []string) ([]string, error) {
	conf, stageLogin := agentConfinementEnv[runtime]
	if !stageLogin && !agentcli.PrintRuntime(runtime) {
		return env, nil
	}
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	if !stageLogin {
		// These clients can use credentials supplied by their environment
		// profile. Keep their native caches and settings in the attempt home.
		if runtime == "antigravity" && envValue(env, "GEMINI_API_KEY") != "" {
			// agy requires an explicit provider selection even when the key
			// is set. Stage only that selection into the isolated home.
			config := filepath.Join(home, ".gemini", "antigravity-cli")
			if err := os.MkdirAll(config, 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(config, "settings.json"), []byte(`{"modelProvider":"gemini"}`), 0o600); err != nil {
				return nil, err
			}
		}
		out := []string{"HOME=" + home}
		for _, entry := range env {
			if !strings.HasPrefix(entry, "HOME=") {
				out = append(out, entry)
			}
		}
		return out, nil
	}
	source, envHome := "", ""
	for _, entry := range env {
		if value, found := strings.CutPrefix(entry, conf.variable+"="); found {
			source = value
		}
		if value, found := strings.CutPrefix(entry, "HOME="); found {
			envHome = value
		}
	}
	if source == "" {
		host := envHome
		if host == "" {
			if userHome, err := os.UserHomeDir(); err == nil {
				host = userHome
			}
		}
		if host != "" {
			source = filepath.Join(host, conf.defaultDir)
		}
	}
	target := filepath.Join(home, conf.defaultDir)
	out := make([]string, 0, len(env)+2)
	staged := false
	if info, err := os.Stat(source); err == nil && info.IsDir() {
		if err = copyConfinementDir(source, target); err != nil {
			return nil, fmt.Errorf("staging %s authentication for the sandbox: %w", runtime, err)
		}
		staged = true
	} else if err == nil {
		return nil, fmt.Errorf("%s authentication path %s is not a directory", runtime, source)
	}
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "HOME":
			entry = "HOME=" + home
		case conf.variable:
			entry = conf.variable + "=" + target
		}
		out = append(out, entry)
	}
	if !hasEnvKey(out, "HOME") {
		out = append(out, "HOME="+home)
	}
	if staged && !hasEnvKey(out, conf.variable) {
		out = append(out, conf.variable+"="+target)
	}
	return out, nil
}

func hasEnvKey(env []string, key string) bool {
	for _, entry := range env {
		if k, _, _ := strings.Cut(entry, "="); k == key {
			return true
		}
	}
	return false
}

// copyConfinementDir copies regular files and resolved symlinks, refusing
// sockets, devices and other special files: a login directory holds
// configuration and tokens, nothing exotic.
func copyConfinementDir(source, target string) error {
	total := int64(0)
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to stage special file or symlink %s", rel)
		}
		if total+info.Size() > confinementStageLimit {
			return fmt.Errorf("authentication directory exceeds the %d byte staging limit", confinementStageLimit)
		}
		total += info.Size()
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		return os.WriteFile(dest, body, 0o600)
	})
}
