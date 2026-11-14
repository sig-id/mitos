package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"

	"mitos/internal/decision"
)

// Only forms that can be represented completely by the decision UI are admitted.
// Unsupported schemas and verification challenges remain native-only; defaults
// never become answers. MCP confirmations are always reviewed by a person.
type ElicitationField struct {
	Name string   `json:"name"`
	Type string   `json:"type"`
	Enum []string `json:"enum,omitempty"`
}

func normalizeElicitation(r *Request, raw json.RawMessage) error {
	var p struct {
		Server  string          `json:"serverName"`
		Mode    string          `json:"mode"`
		Message string          `json:"message"`
		URL     string          `json:"url"`
		Schema  json.RawMessage `json:"requestedSchema"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Server == "" || p.Message == "" || len(p.Message) > 2000 || len(p.Server) > 80 {
		return ErrUnsupported
	}
	confirm := func() {
		c := approvalCard(r.Payload)
		c.Question = "Respond to " + p.Server + " connector request?"
		c.Context.BlockedBy = p.Message
		r.Cards = []decision.Card{c}
	}
	if p.Mode == "url" {
		u, err := url.Parse(p.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return ErrUnsupported
		}
		confirm()
		r.Cards[0].Options[0].Label = "Completed in browser"
		r.Cards[0].Options[0].Consequence = "Confirm that you completed the connector's browser step; this does not itself grant tool access."
		return nil
	}
	if p.Mode != "form" && p.Mode != "openai/form" && p.Mode != "openaiForm" {
		return ErrUnsupported
	}
	var schema struct {
		Schema      string                     `json:"$schema"`
		Type        string                     `json:"type"`
		Title       string                     `json:"title"`
		Description string                     `json:"description"`
		Properties  map[string]json.RawMessage `json:"properties"`
		Required    []string                   `json:"required"`
		Additional  *bool                      `json:"additionalProperties"`
	}
	d := json.NewDecoder(bytes.NewReader(p.Schema))
	d.DisallowUnknownFields()
	if d.Decode(&schema) != nil || schema.Type != "object" || len(schema.Properties) > 3 || schema.Additional != nil && *schema.Additional {
		return ErrUnsupported
	}
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; !ok {
			return ErrUnsupported
		}
	}
	if len(schema.Properties) == 0 {
		confirm()
		return nil
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		var field struct {
			Type        string          `json:"type"`
			Title       string          `json:"title"`
			Description string          `json:"description"`
			Enum        []string        `json:"enum"`
			EnumNames   []string        `json:"enumNames"`
			Min         *int            `json:"minLength"`
			Max         *int            `json:"maxLength"`
			Default     json.RawMessage `json:"default"`
		}
		d := json.NewDecoder(bytes.NewReader(schema.Properties[name]))
		d.DisallowUnknownFields()
		if d.Decode(&field) != nil || name == "" || len(name) > 60 || sensitive(Question{Header: name + " " + field.Title, Question: field.Description}) {
			return ErrUnsupported
		}
		c := baseCard(r.Payload)
		c.Question = "Choose " + name + " for " + p.Server + "."
		c.Context.BlockedBy = p.Message
		c.Kind = "choose"
		switch field.Type {
		case "boolean":
			if field.Enum != nil || field.EnumNames != nil || field.Min != nil || field.Max != nil {
				return ErrUnsupported
			}
			c.Options = []decision.Option{{ID: "true", Label: "Yes", Consequence: "Send true for this field."}, {ID: "false", Label: "No", Consequence: "Send false for this field."}}
		case "string":
			if field.Enum != nil {
				if len(field.Enum) < 1 || len(field.Enum) > 2 || field.Min != nil || field.Max != nil || len(field.EnumNames) != 0 && len(field.EnumNames) != len(field.Enum) {
					return ErrUnsupported
				}
				for i, value := range field.Enum {
					if value == "" || slices.Contains(field.Enum[:i], value) {
						return ErrUnsupported
					}
					label := value
					if len(field.EnumNames) > 0 {
						label = field.EnumNames[i]
					}
					c.Options = append(c.Options, decision.Option{ID: fmt.Sprintf("value_%d", i), Label: label, Consequence: "Send the exact value " + value + "."})
				}
			} else {
				return ErrUnsupported
			}

		default:
			return ErrUnsupported
		}
		c.Options = append(c.Options, decision.Option{ID: "deny", Label: "Decline request", Consequence: "Decline the entire connector request without submitting any fields."})
		r.Cards = append(r.Cards, c)
		r.Elicitation = append(r.Elicitation, ElicitationField{Name: name, Type: field.Type, Enum: field.Enum})
	}
	return nil
}

func elicitationResponse(r Request, answers []decision.Resolution) (json.RawMessage, error) {
	action := "accept"
	var content any
	values := map[string]any{}
	for i, a := range answers {
		if a.Choice == "deny" {
			action = "decline"
			break
		}
		if len(r.Elicitation) == 0 {
			continue
		}
		if i >= len(r.Elicitation) {
			return nil, ErrUnsupported
		}
		f := r.Elicitation[i]
		switch {
		case f.Type == "boolean":
			values[f.Name] = a.Choice == "true"
		case f.Enum != nil:
			for j, value := range f.Enum {
				if a.Choice == fmt.Sprintf("value_%d", j) {
					values[f.Name] = value
				}
			}
		default:
			return nil, ErrUnsupported
		}
	}
	if action == "accept" {
		var envelope struct {
			Params struct {
				Mode string `json:"mode"`
			} `json:"params"`
		}
		_ = json.Unmarshal(r.Payload, &envelope)
		if envelope.Params.Mode != "url" {
			content = values
		}
	}
	return json.Marshal(map[string]any{"id": r.RPCID, "result": map[string]any{"action": action, "content": content, "_meta": nil}})
}
