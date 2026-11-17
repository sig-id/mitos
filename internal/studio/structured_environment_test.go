package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStructuredApplicationEnvironmentOmitsOperatorHistoryAndUnrelatedCredentials(t *testing.T) {
	base := t.TempDir()
	env, err := structuredApplicationEnvironment(base, []string{"PATH=/bin", "HOME=/operator", "CODEX_HOME=/operator/history", "GITHUB_TOKEN=other-tenant-secret", "AWS_SECRET_ACCESS_KEY=other-secret", "OPENAI_API_KEY=dedicated-model-key"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "other-tenant") || strings.Contains(joined, "AWS_") || strings.Contains(joined, "/operator") {
		t.Fatal("operator credentials/history inherited")
	}
	raw, err := os.ReadFile(filepath.Join(base, "home", ".codex", "auth.json"))
	if err != nil || !strings.Contains(string(raw), "dedicated-model-key") {
		t.Fatal("model credential not staged", err)
	}
	if _, err = structuredApplicationEnvironment(t.TempDir(), []string{"HOME=/operator"}); err == nil {
		t.Fatal("silent OAuth/history fallback")
	}
}
