package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxStructuredInputBytes  = 256 << 10
	maxResultSchemaBytes     = 64 << 10
	maxStructuredResultBytes = 256 << 10
	maxSchemaDepth           = 16
)

// resultSchema is the supported subset of JSON Schema for typed results. It is
// the strict structured-output subset: every object closes additional
// properties and requires every declared property, so the runtime can enforce
// it during generation and Mitos can validate it exactly afterwards.
type resultSchema struct {
	Type       string
	Properties map[string]*resultSchema
	Items      *resultSchema
	Enum       []any
	MinLength  *int
	MaxLength  *int
	Minimum    *big.Rat
	Maximum    *big.Rat
	MinItems   *int
	MaxItems   *int
}

var schemaKeywords = []string{"type", "properties", "required", "additionalProperties", "items", "enum", "minLength", "maxLength", "minimum", "maximum", "minItems", "maxItems", "description", "title"}

func compileResultSchema(raw json.RawMessage) (*resultSchema, error) {
	if len(raw) == 0 || len(raw) > maxResultSchemaBytes {
		return nil, errors.New("result schema is required and limited to 64 KiB")
	}
	var doc any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("result schema: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("result schema has trailing data")
	}
	s, err := compileSchemaNode(doc, "$", 0)
	if err != nil {
		return nil, err
	}
	if s.Type != "object" {
		return nil, errors.New("result schema root must be an object")
	}
	return s, nil
}

func compileSchemaNode(v any, at string, depth int) (*resultSchema, error) {
	if depth > maxSchemaDepth {
		return nil, fmt.Errorf("%s: schema nesting exceeds %d levels", at, maxSchemaDepth)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: schema must be an object", at)
	}
	for k := range m {
		if !slices.Contains(schemaKeywords, k) {
			return nil, fmt.Errorf("%s: unsupported schema keyword %q", at, k)
		}
	}
	s := &resultSchema{}
	s.Type, _ = m["type"].(string)
	if !slices.Contains([]string{"object", "array", "string", "number", "integer", "boolean", "null"}, s.Type) {
		return nil, fmt.Errorf("%s: type must be one supported JSON type", at)
	}
	count := func(key string) (*int, error) {
		raw, ok := m[key]
		if !ok {
			return nil, nil
		}
		n, isNum := raw.(json.Number)
		i, err := n.Int64()
		if !isNum || err != nil || i < 0 || i > math.MaxInt32 {
			return nil, fmt.Errorf("%s: %s must be a non-negative integer", at, key)
		}
		v := int(i)
		return &v, nil
	}
	bound := func(key string) (*big.Rat, error) {
		raw, ok := m[key]
		if !ok {
			return nil, nil
		}
		n, isNum := raw.(json.Number)
		r, err := exactNumber(n)
		if !isNum || err != nil {
			return nil, fmt.Errorf("%s: %s must be a bounded number", at, key)
		}
		return r, nil
	}
	var err error
	allowed := map[string][]string{"object": {"properties", "required", "additionalProperties"}, "array": {"items", "minItems", "maxItems"}, "string": {"enum", "minLength", "maxLength"}, "number": {"enum", "minimum", "maximum"}, "integer": {"enum", "minimum", "maximum"}, "boolean": {"enum"}, "null": {}}[s.Type]
	for k := range m {
		if k != "type" && k != "description" && k != "title" && !slices.Contains(allowed, k) {
			return nil, fmt.Errorf("%s: %q does not apply to type %s", at, k, s.Type)
		}
	}
	if s.MinLength, err = count("minLength"); err != nil {
		return nil, err
	}
	if s.MaxLength, err = count("maxLength"); err != nil {
		return nil, err
	}
	if s.MinItems, err = count("minItems"); err != nil {
		return nil, err
	}
	if s.MaxItems, err = count("maxItems"); err != nil {
		return nil, err
	}
	if s.Minimum, err = bound("minimum"); err != nil {
		return nil, err
	}
	if s.Maximum, err = bound("maximum"); err != nil {
		return nil, err
	}
	if raw, ok := m["enum"]; ok {
		values, isList := raw.([]any)
		if !isList || len(values) == 0 || len(values) > 256 {
			return nil, fmt.Errorf("%s: enum must list 1–256 values", at)
		}
		s.Enum = values
	}
	switch s.Type {
	case "object":
		props, _ := m["properties"].(map[string]any)
		if props == nil || len(props) > 256 {
			return nil, fmt.Errorf("%s: object needs 1–256 properties", at)
		}
		if closed, ok := m["additionalProperties"].(bool); !ok || closed {
			return nil, fmt.Errorf("%s: object must set additionalProperties to false", at)
		}
		required, _ := m["required"].([]any)
		names := map[string]bool{}
		for _, r := range required {
			name, ok := r.(string)
			if !ok || names[name] {
				return nil, fmt.Errorf("%s: required must list unique property names", at)
			}
			names[name] = true
		}
		s.Properties = map[string]*resultSchema{}
		for name, child := range props {
			if !names[name] {
				return nil, fmt.Errorf("%s: every property must be required; use a null type alternative instead of optional %q", at, name)
			}
			if s.Properties[name], err = compileSchemaNode(child, at+"."+name, depth+1); err != nil {
				return nil, err
			}
		}
		if len(names) != len(props) {
			return nil, fmt.Errorf("%s: required names a property that is not declared", at)
		}
	case "array":
		if s.Items, err = compileSchemaNode(m["items"], at+"[]", depth+1); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// validateResult decodes exactly one JSON value and checks it against the schema.
func validateResult(s *resultSchema, raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > maxStructuredResultBytes {
		return nil, errors.New("result must be non-empty and at most 256 KiB")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("result is not JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("result has trailing data")
	}
	return v, s.check(v, "$")
}

func (s *resultSchema) check(v any, at string) error {
	if s.Enum != nil {
		found := false
		for _, e := range s.Enum {
			if jsonEqual(e, v) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s: value is not in the enum", at)
		}
	}
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object", at)
		}
		names := make([]string, 0, len(m))
		for k := range m {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if s.Properties[k] == nil {
				return fmt.Errorf("%s: unexpected property %q", at, k)
			}
		}
		for _, k := range sortedKeys(s.Properties) {
			child, ok := m[k]
			if !ok {
				return fmt.Errorf("%s: missing property %q", at, k)
			}
			if err := s.Properties[k].check(child, at+"."+k); err != nil {
				return err
			}
		}
	case "array":
		list, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array", at)
		}
		if (s.MinItems != nil && len(list) < *s.MinItems) || (s.MaxItems != nil && len(list) > *s.MaxItems) {
			return fmt.Errorf("%s: array length %d is out of bounds", at, len(list))
		}
		for i, item := range list {
			if err := s.Items.check(item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: expected string", at)
		}
		n := utf8.RuneCountInString(str)
		if (s.MinLength != nil && n < *s.MinLength) || (s.MaxLength != nil && n > *s.MaxLength) {
			return fmt.Errorf("%s: string length %d is out of bounds", at, n)
		}
	case "number", "integer":
		num, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("%s: expected %s", at, s.Type)
		}
		// Compare exactly; float64 rounding would accept out-of-bounds values.
		r, err := exactNumber(num)
		if err != nil {
			return fmt.Errorf("%s: %v", at, err)
		}
		if s.Type == "integer" && !r.IsInt() {
			return fmt.Errorf("%s: expected integer", at)
		}
		if (s.Minimum != nil && r.Cmp(s.Minimum) < 0) || (s.Maximum != nil && r.Cmp(s.Maximum) > 0) {
			return fmt.Errorf("%s: number is out of bounds", at)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: expected boolean", at)
		}
	case "null":
		if v != nil {
			return fmt.Errorf("%s: expected null", at)
		}
	}
	return nil
}

// exactNumber parses a JSON number without rounding. Length and exponent are
// bounded so a hostile value cannot force an enormous exact expansion.
func exactNumber(n json.Number) (*big.Rat, error) {
	s := n.String()
	if len(s) == 0 || len(s) > 128 {
		return nil, errors.New("number has too many digits")
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(strings.TrimPrefix(s[i+1:], "+"))
		if err != nil || exp > 400 || exp < -400 {
			return nil, errors.New("number exponent is out of range")
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, errors.New("invalid number")
	}
	return r, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func rejectDuplicateJSONKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var walk func(depth int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting is too deep")
		}
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				if seen[key.(string)] {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key.(string)] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case json.Delim('['):
			for dec.More() {
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	return walk(0)
}
