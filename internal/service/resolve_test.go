package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func unit(id string, r Relations) Unit {
	return Unit{ID: id, Scope: "project", Trigger: "manual", Relations: r}
}
func row(t *testing.T, r Resolution, id string) Row {
	t.Helper()
	for _, row := range r.Rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatal("missing row", id)
	return Row{}
}
func TestTargetsPresetsOverridesAndWhy(t *testing.T) {
	c := Config{Units: []Unit{unit("listener", Relations{Wants: []string{"meter", "optional"}, Before: []string{"delivery"}}), unit("meter", Relations{}), unit("delivery", Relations{})}, Targets: []Target{{ID: "seller.target", Scope: "project", Relations: Relations{Requires: []string{"listener", "delivery"}}}}, Presets: []Preset{{ProjectType: "local", Disable: []string{"listener"}}}, Intent: Intent{ProjectType: "local", Targets: []string{"seller.target"}, Enable: []string{"meter"}}}
	r, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"listener", "meter", "delivery", "seller.target"} {
		if row(t, r, id).State != "active" {
			t.Fatal("target membership/preset precedence failed", id)
		}
	}
	if len(row(t, r, "meter").Why) != 2 {
		t.Fatal("shared service lost its separate reasons")
	}
	if len(r.Issues) != 1 || r.Issues[0].Kind != "missing_want" {
		t.Fatal("missing soft member not reported")
	}
	positions := map[string]int{}
	for i, id := range r.Order {
		positions[id] = i
	}
	if positions["listener"] >= positions["delivery"] {
		t.Fatal("before ordering lost")
	}
	c.Intent.Disable = []string{"listener"}
	r, err = Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if row(t, r, "listener").State != "inactive" || row(t, r, "seller.target").State != "failed" || row(t, r, "delivery").State != "inactive" {
		t.Fatal("failed requires bundle leaked partial activation")
	}
	if row(t, r, "meter").State != "active" {
		t.Fatal("independent enable was removed")
	}
}
func TestConflictsResolveBundlesAndNameHardDemanders(t *testing.T) {
	c := Config{Units: []Unit{unit("webhook", Relations{ConflictsWith: []string{"poll"}}), unit("poll", Relations{})}, Targets: []Target{{ID: "endpoint.target", Scope: "project", Relations: Relations{Requires: []string{"webhook"}, Wants: []string{"poll"}}}}, Intent: Intent{Targets: []string{"endpoint.target"}}}
	r, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if row(t, r, "poll").State != "masked" || row(t, r, "webhook").State != "active" {
		t.Fatal("hard/soft conflict failed")
	}
	c.Intent.Enable = []string{"poll"}
	_, err = Resolve(c)
	if err == nil || !strings.Contains(err.Error(), "target:endpoint.target") || !strings.Contains(err.Error(), "explicit:poll") {
		t.Fatalf("hard demanders missing: %v", err)
	}
	c = Config{Units: []Unit{unit("a", Relations{Requires: []string{"b"}, ConflictsWith: []string{"b"}}), unit("b", Relations{})}, Presets: []Preset{{ProjectType: "soft", Enable: []string{"a", "b"}}}, Intent: Intent{ProjectType: "soft"}}
	r, err = Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if row(t, r, "a").State != "masked" || row(t, r, "b").State != "active" {
		t.Fatal("conflicting optional bundle stranded an independent member")
	}
}
func TestOrderingDoesNotActivateAndCyclesFail(t *testing.T) {
	c := Config{Units: []Unit{unit("a", Relations{After: []string{"b"}}), unit("b", Relations{After: []string{"a"}})}, Intent: Intent{Enable: []string{"a"}}}
	r, err := Resolve(c)
	if err != nil || row(t, r, "b").State != "inactive" {
		t.Fatal("ordering pulled a unit in")
	}
	c.Intent.Enable = append(c.Intent.Enable, "b")
	if _, err = Resolve(c); err == nil || !strings.Contains(err.Error(), "ordering cycle") {
		t.Fatal("active cycle accepted")
	}
}
func TestPermutationStableAndUnsupportedRuntimeRefused(t *testing.T) {
	c := Config{Units: []Unit{unit("b", Relations{ConflictsWith: []string{"a"}}), unit("a", Relations{})}, Presets: []Preset{{ProjectType: "soft", Enable: []string{"b", "a"}}}, Intent: Intent{ProjectType: "soft"}}
	first, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Units[0], c.Units[1] = c.Units[1], c.Units[0]
	c.Presets[0].Enable = []string{"a", "b"}
	second, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatal("resolution depends on catalog iteration order")
	}
	c.Units[0].Trigger = "continuous"
	if _, err = Resolve(c); err == nil {
		t.Fatal("unregistered trigger accepted")
	}
}
