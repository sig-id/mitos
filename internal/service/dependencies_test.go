package service

import "testing"

func TestServiceDependencyGraph(t *testing.T) {
	units := []Unit{{ID: "downstream", DependsOn: []string{"upstream"}}, {ID: "upstream"}}
	order, err := DependencyOrder(units)
	if err != nil || len(order) != 2 || order[0] != "upstream" {
		t.Fatal(order, err)
	}
	for _, bad := range [][]Unit{
		{{ID: "a", DependsOn: []string{"missing"}}},
		{{ID: "a", DependsOn: []string{"a"}}},
		{{ID: "a", DependsOn: []string{"b"}}, {ID: "b", DependsOn: []string{"a"}}},
		{{ID: "a", DependsOn: []string{"b", "b"}}, {ID: "b"}},
	} {
		if _, err := DependencyOrder(bad); err == nil {
			t.Fatal("invalid dependency graph accepted", bad)
		}
	}
}
