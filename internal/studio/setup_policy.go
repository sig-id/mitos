package studio

import (
	"errors"
	"strings"
)

// ExecutionPolicy is the optional, deterministic part of the instructions file.
// Prose elsewhere in the file never silently changes enforced limits.
type ExecutionPolicy struct {
	Runtime  string `json:"runtime,omitempty"`
	Model    string `json:"model,omitempty"`
	Minutes  int    `json:"minutes,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
}

func documentPolicy(document string) (RuntimePreferences, error) {
	var result RuntimePreferences
	lines := strings.Split(strings.ReplaceAll(document, "\r\n", "\n"), "\n")
	found := false
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "```mitos" {
			continue
		}
		if found {
			return result, errors.New("use only one mitos execution settings block")
		}
		found = true
		start := i + 1
		i++
		for i < len(lines) && strings.TrimSpace(lines[i]) != "```" {
			i++
		}
		if i == len(lines) {
			return result, errors.New("close the mitos settings block with ```")
		}
		var v ExecutionPolicy
		if err := DecodeGraphJSON([]byte(strings.Join(lines[start:i], "\n")), &v); err != nil {
			return result, err
		}
		result = RuntimePreferences{Runtime: v.Runtime, Model: v.Model, Minutes: v.Minutes, Attempts: v.Attempts}
		if err := validatePreferences(result); err != nil {
			return result, err
		}
	}
	return result, nil
}
