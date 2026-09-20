package decision

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

var ErrValueSchema = errors.New("unsupported value schema")
var ErrValue = errors.New("value does not match the decision schema")

// StringSchema is the supported non-secret JSON Schema profile. Unknown
// constraints fail closed: accepting a subset would weaken the source contract.
type StringSchema struct {
	Type        string   `json:"type"`
	MinLength   *int     `json:"minLength,omitempty"`
	MaxLength   *int     `json:"maxLength,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
}

func ParseStringSchema(raw json.RawMessage) (StringSchema, error) {
	var s StringSchema
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || s.Type != "string" {
		return s, ErrValueSchema
	}
	// Reject duplicate keys as well as unknown or incorrectly cased keywords.
	d = json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return s, ErrValueSchema
	}
	seen := map[string]bool{}
	allowed := map[string]bool{"type": true, "minLength": true, "maxLength": true, "pattern": true, "enum": true, "title": true, "description": true}
	for d.More() {
		tok, err = d.Token()
		if err != nil {
			return s, ErrValueSchema
		}
		k, ok := tok.(string)
		if !ok || seen[k] || !allowed[k] {
			return s, ErrValueSchema
		}
		seen[k] = true
		var v json.RawMessage
		if d.Decode(&v) != nil || bytes.Equal(v, []byte("null")) {
			return s, ErrValueSchema
		}
	}
	if s.MinLength != nil && *s.MinLength < 0 || s.MaxLength != nil && *s.MaxLength < 0 || s.MinLength != nil && s.MaxLength != nil && *s.MinLength > *s.MaxLength {
		return s, ErrValueSchema
	}
	if len(s.Pattern) > 1024 {
		return s, ErrValueSchema
	}
	if _, err = regexp.Compile(s.Pattern); err != nil {
		return s, ErrValueSchema
	}
	if seen["enum"] && len(s.Enum) == 0 {
		return s, ErrValueSchema
	}
	unique := map[string]bool{}
	for _, v := range s.Enum {
		if unique[v] {
			return s, ErrValueSchema
		}
		unique[v] = true
	}
	return s, nil
}
func (c Card) InputProfile() string {
	if len(c.ValueSchema) == 0 || bytes.Equal(bytes.TrimSpace(c.ValueSchema), []byte("null")) {
		return "none"
	}
	if _, err := ParseStringSchema(c.ValueSchema); err != nil {
		return "unsupported"
	}
	return "text"
}
func (c Card) ValidateValue(raw json.RawMessage) error {
	if c.InputProfile() == "none" {
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		return ErrValue
	}
	s, err := ParseStringSchema(c.ValueSchema)
	if err != nil {
		return err
	}
	var value string
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || !utf8.ValidString(value) || len(value) > 1024 {
		return ErrValue
	}
	n := utf8.RuneCountInString(value)
	if s.MinLength != nil && n < *s.MinLength || s.MaxLength != nil && n > *s.MaxLength {
		return ErrValue
	}
	if s.Pattern != "" && !regexp.MustCompile(s.Pattern).MatchString(value) {
		return ErrValue
	}
	if s.Enum != nil {
		for _, v := range s.Enum {
			if value == v {
				return nil
			}
		}
		return ErrValue
	}
	return nil
}
