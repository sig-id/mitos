package service

import (
	"errors"
	"path"
	"strings"
)

func ValidWebhookPath(value string) bool {
	if len(value) < 2 || len(value) > 256 || value[0] != '/' || path.Clean(value) != value || strings.ContainsAny(value, "%?#\\\x00\r\n ") {
		return false
	}
	first := strings.Split(strings.TrimPrefix(value, "/"), "/")[0]
	switch first {
	case "api", "runs", "services", "pools", "tasks", "new", "static", "healthz", "web", "work", "apps", "autonomy", "decisions":
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/-._~", r)) {
			return false
		}
	}
	return true
}
func (u Unit) validateIngress() error {
	s := u.Schedule
	if s == nil || s.Type != u.TriggerKind() || s.Interval != 0 || s.Jitter != 0 || s.Cron != "" || s.Timezone != "" || s.RunOnStartup || s.RestartDelay != nil || len(s.Trigger) > 0 {
		return errors.New("ingress requires only its webhook path or external signal source")
	}
	if u.TriggerKind() == "webhook" {
		if !ValidWebhookPath(s.Path) || s.Source != "" {
			return errors.New("webhook requires a bounded canonical path outside Studio's reserved routes")
		}
	} else if !ValidID(s.Source) || s.Path != "" {
		return errors.New("external_signal requires a named source and no webhook path")
	}
	return nil
}
