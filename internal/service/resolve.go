package service

import (
	"fmt"
	"sort"
)

type node struct {
	kind, trigger string
	relations     Relations
}
type demand struct {
	id, source, relation, root string
	hard                       bool
}

func strength(hard bool) string {
	if hard {
		return "hard"
	}
	return "soft"
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// A requires closure is admitted as one bundle. Dropping an optional bundle
// cannot strand its parent or remove members independently required elsewhere.
func Resolve(c Config) (Resolution, error) {
	result := Resolution{Rows: []Row{}, Order: []string{}, Issues: []Issue{}}
	if err := c.Validate(); err != nil {
		return result, err
	}
	nodes := map[string]node{}
	ids := []string{}
	for _, u := range c.Units {
		nodes[u.ID] = node{"unit", u.TriggerKind(), u.Relations}
		ids = append(ids, u.ID)
	}
	for _, t := range c.Targets {
		nodes[t.ID] = node{"target", "", t.Relations}
		ids = append(ids, t.ID)
	}
	sort.Strings(ids)
	disabled := map[string]bool{}
	for _, id := range c.Intent.Disable {
		disabled[id] = true
	}
	rows := map[string]*Row{}
	for _, id := range ids {
		n := nodes[id]
		rows[id] = &Row{ID: id, Kind: n.kind, State: "inactive", Trigger: n.trigger, Why: []Why{}, PartOf: n.relations.PartOf}
		if disabled[id] {
			rows[id].Why = append(rows[id].Why, Why{Source: "project", Relation: "disable", Strength: "hard", Root: "project:disable"})
		}
	}
	for _, unit := range c.Units {
		rows[unit.ID].DependsOn = append([]string(nil), unit.DependsOn...)
		if unit.Schedule != nil {
			if unit.TriggerKind() == "webhook" {
				rows[unit.ID].IngressSource = unit.Schedule.Path
			}
			if unit.TriggerKind() == "external_signal" {
				rows[unit.ID].IngressSource = unit.Schedule.Source
			}
		}
	}
	pending := []demand{}
	for _, p := range c.Presets {
		if p.ProjectType == c.Intent.ProjectType {
			for _, id := range p.Enable {
				if !contains(p.Disable, id) {
					pending = append(pending, demand{id, "preset:" + p.ProjectType, "enable", "preset:" + p.ProjectType, false})
				}
			}
		}
	}
	for _, id := range c.Intent.Targets {
		pending = append(pending, demand{id, "project", "target", "target:" + id, true})
	}
	for _, id := range c.Intent.Enable {
		pending = append(pending, demand{id, "project", "enable", "explicit:" + id, true})
	}
	active := map[string]bool{}
	hard := map[string]bool{}
	owners := map[string]string{}
	expanded := map[string]bool{}
	seenDemands := map[demand]bool{}
	conflict := func(a, b string) bool {
		return contains(nodes[a].relations.ConflictsWith, b) || contains(nodes[b].relations.ConflictsWith, a)
	}
	for len(pending) > 0 {
		sort.Slice(pending, func(i, j int) bool {
			a, b := pending[i], pending[j]
			if a.hard != b.hard {
				return a.hard
			}
			if a.id != b.id {
				return a.id < b.id
			}
			if a.root != b.root {
				return a.root < b.root
			}
			return a.source < b.source
		})
		d := pending[0]
		pending = pending[1:]
		if seenDemands[d] {
			continue
		}
		seenDemands[d] = true
		if _, ok := nodes[d.id]; !ok {
			result.Issues = append(result.Issues, Issue{ID: d.source, Related: d.id, Kind: "missing_want"})
			continue
		}
		if disabled[d.id] {
			continue
		}
		bundle := map[string]bool{}
		reasons := map[string][]Why{}
		missing := ""
		var collect func(string, string, string)
		collect = func(id, source, relation string) {
			if _, ok := nodes[id]; !ok || disabled[id] {
				if missing == "" {
					missing = id
				}
				return
			}
			reasons[id] = append(reasons[id], Why{Source: source, Relation: relation, Strength: strength(d.hard), Root: d.root})
			if bundle[id] {
				return
			}
			bundle[id] = true
			required := append([]string{}, nodes[id].relations.Requires...)
			sort.Strings(required)
			for _, dep := range required {
				collect(dep, id, "requires")
			}
		}
		collect(d.id, d.source, d.relation)
		if missing != "" {
			if !active[d.id] {
				rows[d.id].State = "failed"
			}
			result.Issues = append(result.Issues, Issue{ID: d.id, Related: missing, Kind: "required_unavailable"})
			continue
		}
		members := []string{}
		for id := range bundle {
			members = append(members, id)
		}
		sort.Strings(members)
		blocker, blocked, internal := "", "", false
		for _, a := range members {
			for _, b := range members {
				if conflict(a, b) {
					blocked = a
					blocker = b
					internal = true
					break
				}
			}
			if blocker != "" {
				break
			}
			for _, b := range ids {
				if active[b] && !bundle[b] && conflict(a, b) {
					blocked = a
					blocker = b
					break
				}
			}
			if blocker != "" {
				break
			}
		}
		if blocker != "" {
			if d.hard && (internal || hard[blocker]) {
				owner := owners[blocker]
				if internal {
					owner = d.root
				}
				return result, fmt.Errorf("hard service conflict: %s demanded by %s conflicts with %s demanded by %s", blocked, d.root, blocker, owner)
			}
			if !active[d.id] {
				rows[d.id].State = "masked"
			}
			result.Issues = append(result.Issues, Issue{ID: d.id, Related: blocker, Kind: "conflict_masked", Detail: "optional requires bundle was not activated"})
			continue
		}
		for _, id := range members {
			active[id] = true
			hard[id] = hard[id] || d.hard
			if owners[id] == "" || d.hard {
				owners[id] = d.root
			}
			rows[id].State = "active"
			for _, why := range reasons[id] {
				duplicate := false
				for _, prior := range rows[id].Why {
					if prior == why {
						duplicate = true
					}
				}
				if !duplicate {
					rows[id].Why = append(rows[id].Why, why)
				}
			}
			if !expanded[id] {
				expanded[id] = true
				for _, wanted := range nodes[id].relations.Wants {
					pending = append(pending, demand{wanted, id, "wants", d.root, false})
				}
			}
		}
	}
	// Ordering only considers active nodes and never pulls in an inactive unit.
	indegree := map[string]int{}
	edges := map[string]map[string]bool{}
	for id := range active {
		indegree[id] = 0
		edges[id] = map[string]bool{}
	}
	add := func(a, b string) {
		if active[a] && active[b] && !edges[a][b] {
			edges[a][b] = true
			indegree[b]++
		}
	}
	for id := range active {
		for _, before := range nodes[id].relations.Before {
			add(id, before)
		}
		for _, after := range nodes[id].relations.After {
			add(after, id)
		}
	}
	for len(indegree) > 0 {
		available := []string{}
		for id, count := range indegree {
			if count == 0 {
				available = append(available, id)
			}
		}
		sort.Strings(available)
		if len(available) == 0 {
			cycle := []string{}
			for id := range indegree {
				cycle = append(cycle, id)
			}
			sort.Strings(cycle)
			return result, fmt.Errorf("service ordering cycle among %v", cycle)
		}
		id := available[0]
		delete(indegree, id)
		result.Order = append(result.Order, id)
		for to := range edges[id] {
			indegree[to]--
		}
	}
	for _, id := range ids {
		row := rows[id]
		sort.Slice(row.Why, func(i, j int) bool {
			a, b := row.Why[i], row.Why[j]
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			if a.Relation != b.Relation {
				return a.Relation < b.Relation
			}
			return a.Root < b.Root
		})
		result.Rows = append(result.Rows, *row)
	}
	sort.Slice(result.Issues, func(i, j int) bool {
		a, b := result.Issues[i], result.Issues[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.Related != b.Related {
			return a.Related < b.Related
		}
		return a.Kind < b.Kind
	})
	return result, nil
}
