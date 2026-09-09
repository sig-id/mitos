package service

import "testing"

func TestConditionSchema(t *testing.T) {
	for _, c := range []Condition{{}, {FileExists: ".ralph/workers/worker-*", FileNotExists: "pause", EnvSet: "GITHUB_TOKEN", EnvEquals: map[string]string{"MODE": "production"}, EnvNotEquals: map[string]string{"RUN_MODE": "merge-only"}, Command: "test -f config"}} {
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []Condition{{FileExists: "../outside"}, {FileExists: "/outside"}, {FileExists: "a//b"}, {FileExists: "["}, {FileNotExists: "a/../b"}, {EnvSet: "A=B"}, {EnvEquals: map[string]string{"BAD NAME": "x"}}, {EnvNotEquals: map[string]string{"OK": "x\x00"}}, {Command: " \n"}} {
		if c.Validate() == nil {
			t.Fatal("accepted invalid condition", c)
		}
	}
}
