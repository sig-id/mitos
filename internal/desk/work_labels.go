package desk

import (
	"errors"
	"slices"
	"strings"
	"unicode"
)

// EditWorkLabels applies a set operation to project-owned IDs validated by the caller.
// Labels are organization metadata and do not change execution revisions.
func (m *Manager) EditWorkLabels(project string, ids []string, action, label string) error {
	label = strings.TrimSpace(label)
	if (action != "add" && action != "remove") || !validText(label, 64) || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return errors.New("choose add or remove and a label of 1–64 bytes")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	if _, err := m.projectLocked(project); err != nil {
		return err
	}
	byID := m.state.WorkLabels[project]
	for _, id := range ids {
		if action == "add" && !slices.Contains(byID[id], label) && len(byID[id]) >= 32 {
			return errors.New("a work item can have up to 32 labels")
		}
	}
	if m.state.WorkLabels == nil {
		m.state.WorkLabels = make(map[string]map[string][]string)
	}
	if byID == nil {
		byID = make(map[string][]string)
		m.state.WorkLabels[project] = byID
	}
	for _, id := range ids {
		names := byID[id]
		at, found := slices.BinarySearch(names, label)
		switch {
		case action == "add" && !found:
			byID[id] = slices.Insert(names, at, label)
		case action == "remove" && found:
			byID[id] = slices.Delete(names, at, at+1)
			if len(byID[id]) == 0 {
				delete(byID, id)
			}
		}
	}
	return m.saveLocked()
}
