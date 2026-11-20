package studio

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
)

// Derive the native structured-output schema from the same Go wire contract
// consumed by registered applications. Operation variants share validation's
// field allowlist so adding scheduling fields cannot strand the native adapter.
func OrchestratorSchema() []byte {
	body, _ := json.Marshal(orchestratorTypeSchema(reflect.TypeFor[OrchestratorPlan]()))
	return body
}

func orchestratorTypeSchema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{orchestratorTypeSchema(t.Elem()), map[string]any{"type": "null"}}}
	}
	if t == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t == reflect.TypeFor[GraphOperation]() {
		variants := []any{}
		kinds := []string{}
		for kind := range graphOperationFields {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			props := map[string]any{"kind": map[string]any{"type": "string", "enum": []string{kind}}}
			for _, name := range graphOperationFields[kind] {
				for i := 0; i < t.NumField(); i++ {
					f := t.Field(i)
					if strings.Split(f.Tag.Get("json"), ",")[0] == name {
						ft := f.Type
						if ft.Kind() == reflect.Pointer {
							ft = ft.Elem()
						}
						props[name] = orchestratorTypeSchema(ft)
					}
				}
			}
			// Operation fields are allowlisted per kind by validation; only the
			// discriminator itself is schema-required.
			variants = append(variants, orchestratorObjectSchema(props, []string{"kind"}))
		}
		return map[string]any{"anyOf": variants}
	}
	switch t.Kind() {
	case reflect.Struct:
		props := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			name := tag[0]
			// Project identity and workspace are runtime bindings, not model choices.
			// ProjectLimits belongs only to recurring workflow services.
			if t == reflect.TypeFor[Brief]() && (name == "execution" || name == "project_limits" || name == "workspace" || name == "project_id") {
				continue
			}
			if f.IsExported() && name != "-" && name != "" {
				props[name] = orchestratorTypeSchema(f.Type)
				// omitempty marks a field the wire contract accepts as absent;
				// requiring it in the advertised schema bounces reasonable calls
				// (for example a proposal without evidence artifact IDs).
				if !slices.Contains(tag, "omitempty") {
					required = append(required, name)
				}
			}
		}
		return orchestratorObjectSchema(props, required)
	case reflect.Slice:
		return map[string]any{"type": "array", "items": orchestratorTypeSchema(t.Elem())}
	case reflect.Map:
		// A map is an object with a uniform value type, never a string: a
		// schema that says string invites exactly what decoding refuses.
		return map[string]any{"type": "object", "additionalProperties": orchestratorTypeSchema(t.Elem())}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	default:
		return map[string]any{"type": "string"}
	}
}

func orchestratorObjectSchema(props map[string]any, required []string) map[string]any {
	sort.Strings(required)
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props}
}
