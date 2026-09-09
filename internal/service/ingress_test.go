package service

import "testing"

func TestIngressSchema(t *testing.T) {
	for _, u := range []Unit{{Trigger: "webhook", Schedule: &Schedule{Type: "webhook", Path: "/capabilities/resize/runs"}}, {Trigger: "external_signal", Schedule: &Schedule{Type: "external_signal", Source: "queue.jobs"}}} {
		if err := u.ValidateRuntime(); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"/", "/api/services", "/runs/a", "/hooks/../runs", "//hooks/a", "/hooks/%2F", "/hooks/a?b", "/hooks/a/", "/hooks/\\a"} {
		if ValidWebhookPath(p) {
			t.Fatal("invalid webhook path accepted", p)
		}
	}
	for _, u := range []Unit{{Trigger: "webhook", Schedule: &Schedule{Type: "webhook", Path: "/hooks/a", Source: "queue"}}, {Trigger: "external_signal", Schedule: &Schedule{Type: "external_signal"}}, {Trigger: "interval", Schedule: &Schedule{Type: "interval", Interval: 10, Path: "/hooks/a"}}, {Trigger: "webhook", Schedule: &Schedule{Type: "webhook", Path: "/hooks/a", Interval: 10}}} {
		if u.ValidateRuntime() == nil {
			t.Fatal("invalid ingress fields accepted", u)
		}
	}
}
