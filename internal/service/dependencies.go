package service

import (
	"fmt"
	"sort"
)

// DependencyOrder validates runtime completion edges independently of activation
// and start-order relations. A dependency never pulls an inactive unit in.
func DependencyOrder(units []Unit) ([]string, error) {
	byID := map[string]Unit{}
	for _, u := range units {
		byID[u.ID] = u
		if err := refs(u.DependsOn); err != nil {
			return nil, err
		}
	}
	color := map[string]int{}
	order := []string{}
	var visit func(string) error
	visit = func(id string) error {
		if color[id] == 1 {
			return fmt.Errorf("service completion dependency cycle at %s", id)
		}
		if color[id] == 2 {
			return nil
		}
		unit, ok := byID[id]
		if !ok {
			return fmt.Errorf("service completion dependency refers to missing unit %s", id)
		}
		color[id] = 1
		deps := append([]string(nil), unit.DependsOn...)
		sort.Strings(deps)
		for _, dep := range deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		color[id] = 2
		order = append(order, id)
		return nil
	}
	ids := []string{}
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return order, nil
}
