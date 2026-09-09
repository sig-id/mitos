package service

import (
	"errors"
	"path"
	"regexp"
	"strings"
)

// Condition is the retained conjunctive service execution gate.
type Condition struct {
	FileExists    string            `json:"file_exists,omitempty"`
	FileNotExists string            `json:"file_not_exists,omitempty"`
	EnvSet        string            `json:"env_set,omitempty"`
	EnvEquals     map[string]string `json:"env_equals,omitempty"`
	EnvNotEquals  map[string]string `json:"env_not_equals,omitempty"`
	Command       string            `json:"command,omitempty"`
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (c Condition) Validate() error {
	for _, pattern := range []string{c.FileExists, c.FileNotExists} {
		if pattern == "" {
			continue
		}
		if len(pattern) > 1024 || strings.ContainsAny(pattern, "\\\x00\r\n") || strings.HasPrefix(pattern, "/") || len(strings.Split(pattern, "/")) > 32 {
			return errors.New("condition file globs require bounded relative paths")
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == "" || part == "." || part == ".." {
				return errors.New("condition file globs cannot contain empty, dot, or parent components")
			}
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return errors.New("invalid condition file glob")
		}
	}
	if c.EnvSet != "" && !environmentName.MatchString(c.EnvSet) {
		return errors.New("invalid condition environment name")
	}
	for _, values := range []map[string]string{c.EnvEquals, c.EnvNotEquals} {
		if len(values) > 32 {
			return errors.New("at most 32 condition environment comparisons per kind")
		}
		for key, value := range values {
			if !environmentName.MatchString(key) || len(value) > 1024 || strings.ContainsRune(value, '\x00') {
				return errors.New("invalid condition environment comparison")
			}
		}
	}
	if len(c.Command) > 4096 || strings.ContainsRune(c.Command, '\x00') || c.Command != "" && strings.TrimSpace(c.Command) == "" {
		return errors.New("condition command requires a nonblank shell command of at most 4096 bytes")
	}
	return nil
}
