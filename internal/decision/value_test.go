package decision

import (
	"encoding/json"
	"testing"
)

func TestStringSchemaAuthority(t *testing.T) {
	c := Card{ValueSchema: json.RawMessage(`{"type":"string","minLength":2,"maxLength":5,"pattern":"^[a-z]+$","enum":["alpha","beta"]}`)}
	for _, value := range []string{`"alpha"`, `"beta"`} {
		if err := c.ValidateValue(json.RawMessage(value)); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{`null`, `{}`, `1`, `"a"`, `"abcdef"`, `"ALPHA"`, `"gamma"`} {
		if c.ValidateValue(json.RawMessage(value)) == nil {
			t.Fatal("invalid value authorized", value)
		}
	}
	for _, schema := range []string{`{"type":"string","format":"password"}`, `{"type":"string","writeOnly":true}`, `{"type":"string","unknown":false}`, `{"type":"string","type":"string"}`, `{"Type":"string"}`, `{"type":"object"}`, `{"type":"string","minLength":null}`, `{"type":"string","pattern":"(?=unsafe)"}`, `{"type":"string","enum":[]}`} {
		c.ValueSchema = json.RawMessage(schema)
		if c.InputProfile() != "unsupported" || c.ValidateValue(json.RawMessage(`"text"`)) == nil {
			t.Fatal("unsupported schema accepted", schema)
		}
	}
}
