package harness

// Codex native delegation features that Mitos-owned launches switch off. Child
// agents would bypass the graph's scheduling, budgets and journal, so work is
// delegated only through Mitos handoff tools. This list is a backstop: an
// upstream rename defeats a denylist, so callers also isolate CODEX_HOME so
// that user configuration cannot enable features in the first place.
var codexDisabledFeatures = []string{"multi_agent", "multi_agent_v2"}

// CodexArguments returns the app-server argv (after the executable) for a
// Mitos-owned headless Codex launch. Each config entry is passed as one -c
// override after the fixed feature lockdown.
func CodexArguments(config ...string) []string {
	args := []string{"app-server"}
	for _, feature := range codexDisabledFeatures {
		args = append(args, "-c", "features."+feature+"=false")
	}
	for _, entry := range config {
		args = append(args, "-c", entry)
	}
	return args
}
