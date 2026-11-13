package agentcli

import "errors"

// WrapBridge preserves the selected native binary and credential environment.
func WrapBridge(launch Launch, binary, url, workspace, title string) (Launch, error) {
	if !launch.Bridge || (launch.AgentID != "codex" && launch.AgentID != "claude") {
		return Launch{}, errors.New("unsupported bridge adapter")
	}
	args := []string{"agent", launch.AgentID, "--url", url, "--workspace", workspace, "--title", title, "--executable", launch.Executable, "--bind-desk-session"}
	if launch.AgentID == "codex" {
		args = append(args, "--keep-open", "--prompt", launch.BridgePrompt)
		// Positional prompt is replaced by the bridge flag; retain CLI config flags.
		nativeArgs := launch.Args
		if len(nativeArgs) >= 2 && nativeArgs[len(nativeArgs)-2] == "--" {
			nativeArgs = nativeArgs[:len(nativeArgs)-2]
		}
		if len(nativeArgs) > 0 {
			args = append(args, "--")
			args = append(args, nativeArgs...)
		}
	} else {
		args = append(args, "--")
		args = append(args, launch.Args...)
	}
	launch.Executable, launch.Args = binary, args
	return launch, nil
}
