package studio

import "mitos/internal/structuredworker"

func structuredApplicationEnvironment(base string, env []string) ([]string, error) {
	return structuredworker.ApplicationEnvironment(base, env)
}
