package service

import "strings"

// These names are owned by control-plane producers, not the custom event API.
func ValidCustomEventName(name string) bool {
	if !ValidID(name) || name == "scheduling_event" {
		return false
	}
	for _, prefix := range []string{"service.", "worker.", "task.", "run.", "decision."} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}
