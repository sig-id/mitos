package service

import (
	"fmt"
	"sort"
)

type LifecycleMember struct {
	ID  string `json:"id"`
	Via string `json:"via,omitempty"`
}

// LifecycleMembers follows reverse part_of edges without activating any member.
// A visited set makes overlapping membership and cycles one stop per node.
func LifecycleMembers(c Config, root string) ([]LifecycleMember, error) {
	relations := map[string]Relations{}
	for _, u := range c.Units {
		relations[u.ID] = u.Relations
	}
	for _, t := range c.Targets {
		relations[t.ID] = t.Relations
	}
	if _, ok := relations[root]; !ok {
		return nil, fmt.Errorf("unknown service or target %s", root)
	}
	ids := []string{}
	for id := range relations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := []LifecycleMember{{ID: root}}
	seen := map[string]bool{root: true}
	for i := 0; i < len(result); i++ {
		for _, id := range ids {
			if !seen[id] && contains(relations[id].PartOf, result[i].ID) {
				seen[id] = true
				result = append(result, LifecycleMember{ID: id, Via: result[i].ID})
			}
		}
	}
	return result, nil
}
