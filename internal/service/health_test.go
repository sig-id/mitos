package service

import "testing"

func TestHealthSchema(t *testing.T) {
	for _, h := range []Health{{}, {Type: "file", Path: ".state/heartbeat"}, {Type: "command", Command: "test -f ready"}, {Type: "heartbeat", MaxAge: 1, Interval: 1, OnUnhealthy: "restart"}} {
		if err := h.Validate(); err != nil {
			t.Fatal(h, err)
		}
	}
	for _, h := range []Health{{Type: "file", Path: "../outside"}, {Type: "file", Path: "/outside"}, {Type: "file", Path: "a", Command: "true"}, {Type: "command"}, {Type: "heartbeat", Path: "beat"}, {Interval: -1}, {MaxAge: -1}, {OnUnhealthy: "ignore"}, {Type: "http"}} {
		if err := h.Validate(); err == nil {
			t.Fatal("accepted invalid health", h)
		}
	}
	if h := (Health{}).Effective(); h.Type != "heartbeat" || h.MaxAge != 60 || h.Interval != 30 || h.OnUnhealthy != "log" {
		t.Fatal("health defaults", h)
	}
}
