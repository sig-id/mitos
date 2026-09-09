package service

import (
	"errors"
	"io/fs"
	"strings"
)

type Health struct {
	Type        string `json:"type"`
	Path        string `json:"path,omitempty"`
	Command     string `json:"command,omitempty"`
	MaxAge      int    `json:"max_age,omitempty"`
	Interval    int    `json:"interval,omitempty"`
	OnUnhealthy string `json:"on_unhealthy,omitempty"`
}

func (h Health) Effective() Health {
	if h.Type == "" {
		h.Type = "heartbeat"
	}
	if h.MaxAge == 0 {
		h.MaxAge = 60
	}
	if h.Interval == 0 {
		h.Interval = 30
	}
	if h.OnUnhealthy == "" {
		h.OnUnhealthy = "log"
	}
	return h
}
func (h Health) Validate() error {
	h = h.Effective()
	if h.MaxAge < 1 || h.MaxAge > 31536000 || h.Interval < 1 || h.Interval > 31536000 || (h.OnUnhealthy != "log" && h.OnUnhealthy != "kill" && h.OnUnhealthy != "restart") {
		return errors.New("health requires max_age/interval 1–31536000 seconds and on_unhealthy log, kill, or restart")
	}
	switch h.Type {
	case "file":
		if !fs.ValidPath(h.Path) || h.Path == "." || len(h.Path) > 1024 || strings.ContainsAny(h.Path, "\\\x00\r\n") || h.Command != "" {
			return errors.New("file health requires a confined relative file path and no command")
		}
	case "command":
		if strings.TrimSpace(h.Command) == "" || len(h.Command) > 4096 || strings.ContainsRune(h.Command, '\x00') || h.Path != "" {
			return errors.New("command health requires a bounded shell command and no path")
		}
	case "heartbeat":
		if h.Command != "" || h.Path != "" {
			return errors.New("heartbeat health does not take a path or command")
		}
	default:
		return errors.New("health types are file, command, and heartbeat")
	}
	return nil
}
