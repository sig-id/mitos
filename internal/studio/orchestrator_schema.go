package studio

import (
	"encoding/json"
	"reflect"
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
			variants = append(variants, orchestratorObjectSchema(props))
		}
		return map[string]any{"anyOf": variants}
	}
	switch t.Kind() {
	case reflect.Struct:
		props := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if f.IsExported() && name != "-" && name != "" {
				props[name] = orchestratorTypeSchema(f.Type)
			}
		}
		return orchestratorObjectSchema(props)
	case reflect.Slice:
		return map[string]any{"type": "array", "items": orchestratorTypeSchema(t.Elem())}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	default:
		return map[string]any{"type": "string"}
	}
}

func orchestratorObjectSchema(props map[string]any) map[string]any {
	keys := []string{}
	for key := range props {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return map[string]any{"type": "object", "additionalProperties": false, "required": keys, "properties": props}
}
