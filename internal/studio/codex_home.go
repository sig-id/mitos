package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// codexWorkerEnvironment gives a Mitos-owned headless Codex launch a CODEX_HOME
// that Mitos owns. It is the allowlist counterpart of Claude's
// --setting-sources "": the operator's config.toml, feature flags, skills,
// plugins, rules and memories never reach a worker. Only the credential passes
// through, as a symlink, so token refreshes write back to the operator's file;
// a copy would let a rotated refresh token silently sign the operator out.
//
// The home is stable per source home rather than per launch: saved native
// threads live under it, and continuation resumes them. Codex refuses a home
// under the temporary directory, so it lives in the store.
func codexWorkerEnvironment(storeDir string, env []string) ([]string, error) {
	lookup := func(key string) string {
		for _, entry := range env {
			if value, ok := strings.CutPrefix(entry, key+"="); ok {
				return value
			}
		}
		return ""
	}
	source := lookup("CODEX_HOME")
	if source == "" {
		if home := lookup("HOME"); home != "" {
			source = filepath.Join(home, ".codex")
		}
	}
	if source != "" && !filepath.IsAbs(source) {
		return nil, errors.New("CODEX_HOME must be an absolute path")
	}
	sum := sha256.Sum256([]byte(filepath.Clean(source)))
	home := filepath.Join(storeDir, "native", "codex", hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	if source != "" {
		if err := linkCredential(filepath.Join(source, "auth.json"), filepath.Join(home, "auth.json")); err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "CODEX_HOME=") {
			result = append(result, entry)
		}
	}
	return append(result, "CODEX_HOME="+home), nil
}

// linkCredential points link at target, replacing whatever is there atomically
// so concurrent launches never observe a missing credential.
func linkCredential(target, link string) error {
	if current, err := os.Readlink(link); err == nil && current == target {
		return nil
	}
	pending, err := os.CreateTemp(filepath.Dir(link), ".auth-*.json")
	if err != nil {
		return err
	}
	name := pending.Name()
	_ = pending.Close()
	if err = os.Remove(name); err != nil {
		return err
	}
	if err = os.Symlink(target, name); err != nil {
		return err
	}
	if err = os.Rename(name, link); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
