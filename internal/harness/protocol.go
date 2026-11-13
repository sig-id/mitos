// Package harness translates native waiting requests, never agent prose, into
// canonical questions and one-shot permissions. Original payloads remain evidence.
package harness

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"mitos/internal/decision"
)

var ErrUnsupported = errors.New("request requires the native console")

type Envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type Question struct {
	ID          string `json:"id"`
	Header      string `json:"header"`
	Question    string `json:"question"`
	IsOther     bool   `json:"isOther"`
	IsSecret    bool   `json:"isSecret"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
	Schema json.RawMessage `json:"schema,omitempty"`
}
type Request struct {
	Key       string          `json:"key"`
	Thread    string          `json:"thread"`
	Turn      string          `json:"turn"`
	Questions []Question      `json:"questions"`
	Cards     []decision.Card `json:"cards"`
	Payload   json.RawMessage `json:"payload"`
	Method    string          `json:"method"`
	RPCID     json.RawMessage `json:"rpc_id"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	Denial    string          `json:"denial,omitempty"`
}

func digest(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
func Normalize(adapter string, raw json.RawMessage) (Request, error) {
	if len(raw) > 24<<10 || !json.Valid(raw) {
		return Request{}, ErrUnsupported
	}
	// Reject duplicate keys throughout the source envelope before normalization.
	if !uniqueJSON(raw) {
		return Request{}, ErrUnsupported
	}
	out := Request{Payload: append(json.RawMessage(nil), raw...)}
	switch adapter {
	case "codex":
		var e Envelope
		if json.Unmarshal(raw, &e) != nil || len(e.ID) == 0 || string(e.ID) == "null" {
			return out, ErrUnsupported
		}
		var id any
		if json.Unmarshal(e.ID, &id) != nil {
			return out, ErrUnsupported
		}
		switch id.(type) {
		case string, float64:
		default:
			return out, ErrUnsupported
		}
		out.Method = e.Method
		out.RPCID = e.ID
		var p struct {
			Thread    string            `json:"threadId"`
			Turn      string            `json:"turnId"`
			Questions []Question        `json:"questions"`
			Command   *string           `json:"command"`
			Decisions []json.RawMessage `json:"availableDecisions"`
		}
		if json.Unmarshal(e.Params, &p) != nil || p.Thread == "" || p.Turn == "" {
			return out, ErrUnsupported
		}
		out.Thread, out.Turn = p.Thread, p.Turn
		out.Key = digest([]byte(p.Thread + "\x00" + string(e.ID)))
		switch e.Method {
		case "item/commandExecution/requestApproval":
			if p.Command == nil {
				return out, ErrUnsupported
			}
			out.Denial = "decline"
			if p.Decisions != nil {
				allow, deny, cancel := false, false, false
				for _, v := range p.Decisions {
					allow = allow || string(v) == `"accept"`
					deny = deny || string(v) == `"decline"`
					cancel = cancel || string(v) == `"cancel"`
				}
				if !allow || (!deny && !cancel) {
					return out, ErrUnsupported
				}
				if !deny {
					out.Denial = "cancel"
				}
			}
			out.Cards = []decision.Card{approvalCard(raw)}
			if out.Denial == "cancel" {
				out.Cards[0].Options[1].Consequence = "Cancel this waiting request without executing it; the harness may end the current turn."
			}
		case "item/tool/requestUserInput", "mitos/asyncQuestion":
			out.Questions = p.Questions
		default:
			return out, ErrUnsupported
		}
	case "claude":
		var p struct {
			SessionID string          `json:"session_id"`
			ToolID    string          `json:"tool_use_id"`
			Event     string          `json:"hook_event_name"`
			Tool      string          `json:"tool_name"`
			Input     json.RawMessage `json:"tool_input"`
		}
		if json.Unmarshal(raw, &p) != nil || p.SessionID == "" || p.ToolID == "" {
			return out, ErrUnsupported
		}
		out.Thread = p.SessionID
		out.Key = digest([]byte(p.SessionID + "\x00" + p.ToolID))
		out.Method = p.Event
		out.ToolInput = p.Input
		// PreToolUse carries the stable tool_use_id required for retransmission
		// deduplication. PermissionRequest without it stays with Claude's native UI.
		if p.Event != "PreToolUse" {
			return out, ErrUnsupported
		}
		if p.Tool == "AskUserQuestion" {
			var input struct {
				Questions []Question `json:"questions"`
			}
			if json.Unmarshal(p.Input, &input) != nil {
				return out, ErrUnsupported
			}
			out.Questions = input.Questions
			for i := range out.Questions {
				out.Questions[i].ID = strconv.Itoa(i)
				out.Questions[i].IsOther = true
			}
		} else {
			// Only native tools with completely represented inputs have an approval
			// mapping. Never infer authorization for an arbitrary new tool name.
			switch p.Tool {
			case "Bash", "Write", "Edit":
				out.Cards = []decision.Card{approvalCard(raw)}
			default:
				return out, ErrUnsupported
			}
		}
	default:
		return out, ErrUnsupported
	}
	if len(out.Questions) > 0 {
		if len(out.Questions) > 3 {
			return out, ErrUnsupported
		}
		seen := map[string]bool{}
		questionTexts := map[string]bool{}
		for _, q := range out.Questions {
			if q.ID == "" || seen[q.ID] || q.Question == "" || sensitive(q) || q.MultiSelect || (adapter == "claude" && questionTexts[q.Question]) {
				return out, ErrUnsupported
			}
			seen[q.ID] = true
			questionTexts[q.Question] = true
			c := baseCard(raw)
			c.Question = q.Question
			if out.Method == "mitos/asyncQuestion" {
				c.Context.BlockedBy = "An asynchronous clarification is pending; independent work may continue."
			}
			c.Kind = "choose"
			if len(q.Options) == 0 || q.IsOther {
				c.Kind = "supply_value"
				c.ValueSchema = json.RawMessage(`{"type":"string","minLength":1,"maxLength":1024}`)
				if len(q.Schema) > 0 {
					if _, err := decision.ParseStringSchema(q.Schema); err != nil {
						return out, ErrUnsupported
					}
					c.ValueSchema = q.Schema
				}
				c.Options = []decision.Option{{ID: "answer", Label: "Send answer", Consequence: "Deliver the reviewed text to this waiting question."}, {ID: "omit", Label: "Leave unanswered", Consequence: "Send an empty answer; the reviewed text is discarded."}}
			} else {
				if len(q.Options) < 2 || len(q.Options) > 3 || len(q.Schema) > 0 {
					return out, ErrUnsupported
				}
				labels := map[string]bool{}
				for i, o := range q.Options {
					if o.Label == "" || o.Description == "" || labels[o.Label] {
						return out, ErrUnsupported
					}
					labels[o.Label] = true
					c.Options = append(c.Options, decision.Option{ID: fmt.Sprintf("option_%d", i), Label: o.Label, Consequence: o.Description})
				}
			}
			out.Cards = append(out.Cards, c)
		}
	}
	if len(out.Cards) == 0 {
		return out, ErrUnsupported
	}
	return out, nil
}
func baseCard(raw []byte) decision.Card {
	var compact bytes.Buffer
	_ = json.Compact(&compact, raw)
	return decision.Card{Kind: "approve", Question: "Allow this recorded tool request once?", Reversibility: "external", Context: decision.Context{Attempted: "Native harness request (untrusted source text):\n" + compact.String(), BlockedBy: "The originating harness is waiting for an explicit answer.", EvidenceRefs: []string{}}, BlastRadius: decision.BlastRadius{Paths: []string{}, Destinations: []string{}}, CostOfWrong: decision.Cost{Detail: "One-shot answer to the recorded request. Tool effects, paths, destinations and spending may extend beyond the workspace and are not bounded by this connector. Review the complete source payload. No persistent permission changes."}}
}
func approvalCard(raw []byte) decision.Card {
	c := baseCard(raw)
	c.Options = []decision.Option{{ID: "approve", Label: "Allow once", Consequence: "Permit exactly the recorded tool request once under the harness's existing policy."}, {ID: "deny", Label: "Deny", Consequence: "Do not execute the recorded tool request."}}
	return c
}

// Response is a deterministic native response. It must be journaled before a
// connector may transmit it; all decisions in a multi-question request are needed.
func Response(adapter string, r Request, answers []decision.Resolution) (json.RawMessage, error) {
	if len(answers) != len(r.Cards) {
		return nil, ErrUnsupported
	}
	for i, a := range answers {
		if !r.Cards[i].HasOption(a.Choice) || r.Cards[i].ValidateValue(a.Value) != nil {
			return nil, ErrUnsupported
		}
	}
	var result any
	if len(r.Questions) == 0 {
		if adapter == "codex" {
			choice := "decline"
			if r.Denial == "cancel" {
				choice = "cancel"
			}
			if answers[0].Choice == "approve" {
				choice = "accept"
			}
			result = map[string]any{"decision": choice}
		} else {
			choice := "deny"
			if answers[0].Choice == "approve" {
				choice = "allow"
			}
			result = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": choice, "permissionDecisionReason": "Confirmed by the Mitos operator for this request only."}}
		}
	} else {
		values := map[string]any{}
		claudeAnswers := map[string]string{}
		for i, q := range r.Questions {
			a := answers[i]
			value := ""
			if r.Cards[i].InputProfile() == "text" {
				if a.Choice != "omit" {
					_ = json.Unmarshal(a.Value, &value)
				}
			} else {
				index, err := strconv.Atoi(strings.TrimPrefix(a.Choice, "option_"))
				if err != nil || index < 0 || index >= len(q.Options) {
					return nil, ErrUnsupported
				}
				value = q.Options[index].Label
			}
			list := []string{}
			if a.Choice != "omit" {
				list = append(list, value)
			}
			values[q.ID] = map[string]any{"answers": list}
			claudeAnswers[q.Question] = value
		}
		if adapter == "codex" {
			result = map[string]any{"answers": values}
		} else {
			var input map[string]any
			if json.Unmarshal(r.ToolInput, &input) != nil {
				return nil, ErrUnsupported
			}
			input["answers"] = claudeAnswers
			result = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": input}}
		}
	}
	if adapter == "codex" {
		result = map[string]any{"id": r.RPCID, "result": result}
	}
	return json.Marshal(result)
}
func uniqueJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() bool
	walk = func() bool {
		t, err := d.Token()
		if err != nil {
			return false
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return false
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return false
				}
				seen[key] = true
				if !walk() {
					return false
				}
			}
			_, err = d.Token()
			return err == nil
		case json.Delim('['):
			for d.More() {
				if !walk() {
					return false
				}
			}
			_, err = d.Token()
			return err == nil
		}
		return true
	}
	return walk()
}

func SourceKey(thread, request string) string { return digest([]byte(thread + "\x00" + request)) }

func sensitive(q Question) bool {
	if q.IsSecret {
		return true
	}
	text := strings.ToLower(q.Header + " " + q.Question)
	for _, word := range []string{"password", "passphrase", "api key", "api_key", "access token", "secret key", "private key", "credential"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}
