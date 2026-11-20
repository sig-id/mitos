package agentcli

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// Native tools over a streamable HTTP MCP server (issue #101). A CLI with an
// adapter receives the Mitos tools as native model tools and ends its attempt
// with a terminal tool call instead of the text-frame protocol; a CLI without
// one keeps the text protocol.

// MCPConfig names one per-attempt streamable HTTP MCP server.
type MCPConfig struct {
	// URL is the endpoint, for example http://127.0.0.1:3128/mitos/mcp.
	URL string
	// Headers authenticate the attempt, such as Authorization: Bearer ….
	Headers map[string]string
	// Timeout bounds one MCP request; it must outlast the attempt deadline,
	// because operator decisions block until they are answered.
	Timeout time.Duration
}

// MCPFiles describes how a CLI is told about an HTTP MCP server: files
// written relative to its configuration directory, and the argument changes
// that replace the text protocol's MCP flags.
type MCPFiles struct {
	Files map[string][]byte
	// Add are arguments appended to the print arguments.
	Add []string
	// Drop are arguments removed from the print arguments.
	Drop []string
}

// SupportsMCPHTTP reports whether the CLI has an HTTP MCP adapter and can be
// started once with native tools instead of the text protocol.
func SupportsMCPHTTP(id string) bool { return id == "pi" }

// MCPHTTP returns the pi configuration for an HTTP MCP server: an mcp.json
// entry named mitos with direct exposure, so the tools are declared to the
// model, and a per-request timeout that covers blocking operator decisions.
func MCPHTTP(id string, config MCPConfig) (MCPFiles, error) {
	if !SupportsMCPHTTP(id) {
		return MCPFiles{}, fmt.Errorf("%s has no HTTP MCP adapter; it keeps the text protocol", id)
	}
	timeout := int(config.Timeout.Round(time.Second) / time.Second)
	if timeout < 1 {
		timeout = 1
	}
	body, err := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			mcpServerName: map[string]any{
				"url":      config.URL,
				"headers":  config.Headers,
				"exposure": "direct",
				"timeout":  timeout,
			},
		},
	}, "", "  ")
	if err != nil {
		return MCPFiles{}, err
	}
	return MCPFiles{
		Files: map[string][]byte{"mcp.json": append(body, '\n')},
		// --no-mcp would drop the server; --no-extensions together with
		// -e builtin:mcp loads only the built-in MCP support, and the plain
		// --tools names keep the mitos tools declared (only an mcp__ entry
		// would filter them away).
		Add:  []string{"-e", "builtin:mcp"},
		Drop: []string{"--no-mcp", "--no-session"},
	}, nil
}

// mcpServerName is the server name shared by every adapter; MCP tools are
// exposed to the model as mcp__<server>__<tool>.
const mcpServerName = "mitos"

// ApplyArguments adjusts print arguments for the MCP mode: the text
// protocol's MCP flags are removed and the adapter's additions appended.
func (f MCPFiles) ApplyArguments(args []string) []string {
	out := make([]string, 0, len(args)+len(f.Add))
	for _, arg := range args {
		if !slices.Contains(f.Drop, arg) {
			out = append(out, arg)
		}
	}
	return append(out, f.Add...)
}
