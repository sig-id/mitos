package workpool

import (
	"errors"
	"strings"
)

// AddInput groups explicit selections from the same successful producer.
// A conflicting status/strength remains an error rather than changing its guard.
func AddInput(deps *[]Dependency, value string) error {
	task, name, ok := strings.Cut(value, ":")
	if !ok || task == "" || !ArtifactName(name) {
		return errors.New("input must be TASK_ID:ARTIFACT_NAME")
	}
	for i := range *deps {
		d := &(*deps)[i]
		if d.From == task {
			if d.Guard != "done" || (d.Strength != "" && d.Strength != "hard") {
				return errors.New("input selection conflicts with the existing dependency")
			}
			d.Payload = "data"
			d.Artifacts = append(d.Artifacts, name)
			return nil
		}
	}
	*deps = append(*deps, Dependency{From: task, Payload: "data", Guard: "done", Artifacts: []string{name}})
	return nil
}
