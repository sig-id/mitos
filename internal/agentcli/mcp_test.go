package agentcli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMCPHTTPAdaptsPiAndRefusesOthers(t *testing.T) {
	config := MCPConfig{
		URL:     "http://127.0.0.1:3128/mitos/mcp",
		Headers: map[string]string{"Authorization": "Bearer attempt-token"},
		Timeout: 90*time.Minute + 14*time.Second,
	}
	files, err := MCPHTTP("pi", config)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		MCPServers map[string]struct {
			URL      string            `json:"url"`
			Headers  map[string]string `json:"headers"`
			Exposure string            `json:"exposure"`
			Timeout  int               `json:"timeout"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(files.Files["mcp.json"], &decoded) != nil {
		t.Fatal(files.Files["mcp.json"])
	}
	server := decoded.MCPServers["mitos"]
	if server.URL != config.URL || server.Headers["Authorization"] != "Bearer attempt-token" || server.Exposure != "direct" {
		t.Fatalf("mcp.json: %+v", server)
	}
	// The timeout is rounded up to whole seconds past the attempt deadline,
	// because operator decisions block until they are answered.
	if server.Timeout != int((90*time.Minute+14*time.Second)/time.Second) {
		t.Fatalf("timeout: %d", server.Timeout)
	}

	// Print arguments keep the tool allowlist, amended with the server's
	// pattern: a plain-name-only --tools list leaves MCP tools undeclared,
	// whatever their exposure. --no-mcp and --no-session make way for the
	// built-in MCP support and the native session continuation.
	base := []string{"--print", "--no-session", "--no-extensions", "--no-mcp", "--tools", "read,bash"}
	adjusted := files.ApplyArguments(base)
	joined := strings.Join(adjusted, " ")
	for _, missing := range []string{"--no-mcp ", "--no-session "} {
		if strings.Contains(joined+" ", missing) {
			t.Fatalf("argument kept: %s in %v", missing, adjusted)
		}
	}
	if !strings.Contains(joined, "--no-extensions") || !strings.Contains(joined, "-e builtin:mcp") || !strings.Contains(joined, "--tools read,bash,mcp__mitos__*") {
		t.Fatalf("arguments: %v", adjusted)
	}
	// A withheld allowlist becomes the server's tools alone. JSON event mode
	// streams the CLI's own progress to the host for the live preview.
	none := files.ApplyArguments([]string{"--print", "--no-extensions", "--no-tools"})
	if strings.Join(none, " ") != "--print --no-extensions --tools mcp__mitos__* -e builtin:mcp --mode json" {
		t.Fatalf("no-tools arguments: %v", none)
	}
	if !files.JSONStream {
		t.Fatal("the pi adapter must select JSON event mode for a live preview")
	}

	if SupportsMCPHTTP("opencode") {
		t.Fatal("opencode has no adapter yet")
	}
	for _, id := range []string{"opencode", "gemini", "cline", "codex"} {
		if _, err = MCPHTTP(id, config); err == nil {
			t.Fatalf("%s accepted an HTTP MCP server", id)
		}
	}
}
