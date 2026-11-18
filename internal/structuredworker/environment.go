package structuredworker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Application workers never inherit application credentials, operator history,
// MCP configuration or native conversations. The currently supported scoped
// model profile uses a dedicated API key and a fresh home; local operator S1
// work retains its existing native login behavior.
func ApplicationEnvironment(base string, env []string) ([]string, error) {
	key := ""
	out := []string{}
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "TZ": true, "TERM": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NIX_SSL_CERT_FILE": true}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if name == "OPENAI_API_KEY" {
			key = value
		}
		if allowed[name] {
			out = append(out, entry)
		}
	}
	if key == "" {
		return nil, errors.New("application_model_auth_required: configure a dedicated OPENAI_API_KEY; operator OAuth/history are not shared with application workers")
	}
	home := filepath.Join(base, "home")
	native := filepath.Join(home, ".codex")
	if err := os.MkdirAll(native, 0700); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(map[string]string{"OPENAI_API_KEY": key})
	if err := os.WriteFile(filepath.Join(native, "auth.json"), raw, 0600); err != nil {
		return nil, err
	}
	return append(out, "HOME="+home, "CODEX_HOME="+native, "OPENAI_API_KEY="+key), nil
}
