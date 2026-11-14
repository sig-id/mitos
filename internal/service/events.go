package service

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type EventPatterns []string

func (p *EventPatterns) UnmarshalJSON(data []byte) error {
	var single string
	if json.Unmarshal(data, &single) == nil {
		*p = EventPatterns{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return errors.New("event trigger must be a string or string array")
	}
	*p = many
	return nil
}

type Triggers struct {
	OnComplete []string `json:"on_complete,omitempty"`
	OnFailure  []string `json:"on_failure,omitempty"`
	OnFinish   []string `json:"on_finish,omitempty"`
}

func (u Unit) TriggerKind() string {
	if u.Trigger == "" && u.Triggers != nil {
		return "event"
	}
	return u.Trigger
}
func ValidEventPattern(pattern string) bool {
	if pattern == "*" {
		return true
	}
	if ValidCustomEventName(strings.TrimSuffix(pattern, "*")) {
		return true
	}
	if len(pattern) > 128 {
		return false
	}
	for _, prefix := range []string{"service.succeeded:", "service.failed:", "service.completed:"} {
		if !strings.HasPrefix(pattern, prefix) {
			continue
		}
		target := strings.TrimPrefix(pattern, prefix)
		if target == "*" {
			return true
		}
		if strings.HasSuffix(target, "*") {
			target = strings.TrimSuffix(target, "*")
		}
		return ValidID(target)
	}
	return false
}
func (u Unit) EventPatterns() ([]string, error) {
	patterns := []string{}
	if len(u.Events) > 0 {
		if u.TriggerKind() != "cron" && u.TriggerKind() != "interval" && u.TriggerKind() != "manual" {
			return nil, errors.New("additional events require a cron, interval, or manual service")
		}
		patterns = append(patterns, u.Events...)
	} else if u.Triggers != nil {
		if u.Schedule != nil {
			return nil, errors.New("choose triggers hooks or an event schedule")
		}
		for _, hook := range []struct {
			ids    []string
			prefix string
		}{{u.Triggers.OnComplete, "service.succeeded:"}, {u.Triggers.OnFailure, "service.failed:"}, {u.Triggers.OnFinish, "service.completed:"}} {
			if err := refs(hook.ids); err != nil {
				return nil, err
			}
			for _, id := range hook.ids {
				patterns = append(patterns, hook.prefix+id)
			}
		}
	} else {
		if u.Schedule == nil || u.Schedule.Type != "event" || u.Schedule.Interval != 0 || u.Schedule.Jitter != 0 || u.Schedule.Cron != "" || u.Schedule.Timezone != "" || u.Schedule.RunOnStartup {
			return nil, errors.New("event services require an event schedule without timed fields")
		}
		patterns = append(patterns, u.Schedule.Trigger...)
	}
	if len(patterns) == 0 || len(patterns) > 32 {
		return nil, errors.New("event services require 1–32 patterns")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, pattern := range patterns {
		if !ValidEventPattern(pattern) {
			return nil, errors.New("event patterns require a supported service outcome or custom name with an optional trailing wildcard")
		}
		if !seen[pattern] {
			seen[pattern] = true
			out = append(out, pattern)
		}
	}
	sort.Strings(out)
	return out, nil
}
func MatchEvents(patterns, names []string) []string {
	out := []string{}
	for _, name := range names {
		for _, pattern := range patterns {
			if pattern == name || strings.HasSuffix(pattern, "*") && strings.HasPrefix(name, strings.TrimSuffix(pattern, "*")) {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
func (s Schedule) Timed() bool { return s.Type == "interval" || s.Type == "cron" }
