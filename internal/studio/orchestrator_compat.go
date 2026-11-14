package studio

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Historical journal field names remain readable. New writes use orchestrator
// names only; accepting an alias never permits conflicting or unknown fields.
func decodeOrchestratorAliases(raw []byte, target any, aliases map[string]string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for old, name := range aliases {
		if value, ok := fields[old]; ok {
			if _, exists := fields[name]; exists {
				return fmt.Errorf("conflicting fields %s and %s", old, name)
			}
			fields[name] = value
			delete(fields, old)
		}
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func (v *GraphLimits) UnmarshalJSON(raw []byte) error {
	type stored GraphLimits
	return decodeOrchestratorAliases(raw, (*stored)(v), map[string]string{"controller_calls": "orchestrator_calls"})
}

func (v *GraphPolicy) UnmarshalJSON(raw []byte) error {
	type stored GraphPolicy
	return decodeOrchestratorAliases(raw, (*stored)(v), map[string]string{"controller_application": "orchestrator_application"})
}

func (v *GraphMember) UnmarshalJSON(raw []byte) error {
	type stored GraphMember
	return decodeOrchestratorAliases(raw, (*stored)(v), map[string]string{"controller_for": "orchestrator_for"})
}

func (v *GraphExpansion) UnmarshalJSON(raw []byte) error {
	type stored GraphExpansion
	return decodeOrchestratorAliases(raw, (*stored)(v), map[string]string{"controller_task": "orchestrator_task"})
}

func (v *WorkGraph) UnmarshalJSON(raw []byte) error {
	type stored WorkGraph
	return decodeOrchestratorAliases(raw, (*stored)(v), map[string]string{"controller_generation": "orchestrator_generation", "controller_calls": "orchestrator_calls"})
}

func (v *Application) UnmarshalJSON(raw []byte) error {
	type stored Application
	return decodeOrchestratorAliases(raw, (*stored)(v), map[string]string{"graph_controller": "graph_orchestrator"})
}

func (v *GraphOperation) UnmarshalJSON(raw []byte) error {
	type stored GraphOperation
	if err := decodeOrchestratorAliases(raw, (*stored)(v), nil); err != nil {
		return err
	}
	if v.Kind == "replace_controller" {
		v.Kind = "replace_orchestrator"
	}
	return nil
}

func isOrchestratorKind(kind string) bool { return kind == "orchestrator" || kind == "planner" }
func orchestratorAgentID(id string) string {
	if id == "agent:mitos.planner" {
		return "agent:mitos.orchestrator"
	}
	return id
}
