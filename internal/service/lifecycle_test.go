package service

import (
	"reflect"
	"testing"
)

func TestLifecycleReversePartOfClosure(t *testing.T) {
	c := Config{Targets: []Target{{ID: "root", Relations: Relations{Requires: []string{"unrelated"}, PartOf: []string{"leaf"}}}, {ID: "nested", Relations: Relations{PartOf: []string{"root"}}}}, Units: []Unit{{ID: "leaf", Relations: Relations{PartOf: []string{"nested", "root"}}}, {ID: "unrelated"}, {ID: "child", Relations: Relations{PartOf: []string{"nested"}}}}}
	got, err := LifecycleMembers(c, "root")
	want := []LifecycleMember{{ID: "root"}, {ID: "leaf", Via: "root"}, {ID: "nested", Via: "root"}, {ID: "child", Via: "nested"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	if _, err = LifecycleMembers(c, "missing"); err == nil {
		t.Fatal("missing root accepted")
	}
}
