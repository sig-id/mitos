package agentcli

import "fmt"

// PrepareProjectContext creates a fresh, noninteractive invocation. It never
// resumes the supervisor's conversation or grants edit/approval bypass flags.
// Custom adapters explicitly opt into the JSON-on-stdout context contract.
func (r *Registry) PrepareProjectContext(request Request, outputPath string) (Launch, error) {
	adapter, err := r.adapter(request.AgentID)
	if err != nil {
		return Launch{}, err
	}
	if !adapter.Capabilities().ProjectContext {
		return Launch{}, fmt.Errorf("%s has no background project-context adapter; choose Codex, Claude or Pi", adapter.Definition().Name)
	}
	launch, err := r.Prepare(Request{AgentID: request.AgentID, ProfileID: request.ProfileID})
	if err != nil {
		return Launch{}, err
	}
	definition := adapter.Definition()
	launch.NativeSessionID = ""
	switch {
	case len(definition.ContextArgs) > 0:
		launch.Args = append(append([]string{}, definition.ContextArgs...), "--", request.Prompt)
	case definition.ID == "codex":
		launch.Args = []string{"exec", "--sandbox", "read-only", "--ephemeral", "--skip-git-repo-check", "--color", "never", "--output-last-message", outputPath, "--", request.Prompt}
	case definition.ID == "claude":
		launch.Args = []string{"--print", "--no-session-persistence", "--output-format", "text", "--tools", "Read,Glob,Grep", "--allowedTools", "Read,Glob,Grep", "--strict-mcp-config", "--", request.Prompt}
	case definition.ID == "pi":
		launch.Args = []string{"--print", "--no-session", "--no-extensions", "--tools", "read,grep,find,ls", "--", request.Prompt}
	}
	return launch, nil
}
