package harness

import (
	"encoding/json"
	"fmt"
	"strings"
)

// asyncRequest converts a native asynchronous question item into a durable request.
// It retains the source item; answers are delivered by turn/steer, never as RPC
// replies to an item notification.
func asyncRequest(e Envelope) (json.RawMessage, bool) {
	if e.Method != "item/completed" {
		return nil, false
	}
	var p struct {
		Thread string `json:"threadId"`
		Turn   string `json:"turnId"`
		Item   struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Delivery  string `json:"delivery"`
			Questions []struct {
				Title   string   `json:"title"`
				Options []string `json:"options"`
			} `json:"questions"`
		} `json:"item"`
	}
	if json.Unmarshal(e.Params, &p) != nil || p.Item.Type != "agentMessage" || p.Item.Delivery != "async" || p.Item.ID == "" || len(p.Item.Questions) == 0 {
		return nil, false
	}
	questions := []map[string]any{}
	for i, q := range p.Item.Questions {
		// Always retain free text support. Native async options have no descriptions.
		questions = append(questions, map[string]any{"id": fmt.Sprintf("q%d", i), "question": q.Title + func() string {
			if len(q.Options) == 0 {
				return ""
			}
			return " Suggested answers: " + strings.Join(q.Options, ", ")
		}(), "isOther": true})
	}
	raw, err := json.Marshal(map[string]any{"id": "async:" + p.Item.ID, "method": "mitos/asyncQuestion", "params": map[string]any{"threadId": p.Thread, "turnId": p.Turn, "questions": questions, "source": json.RawMessage(e.Params)}})
	return raw, err == nil
}
func steerAnswer(id, thread, turn string, questions []Question, body json.RawMessage) (json.RawMessage, error) {
	var answer struct {
		Result struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, err
	}
	lines := []string{}
	for i := 0; i < len(answer.Result.Answers); i++ {
		q := fmt.Sprintf("q%d", i)
		value := answer.Result.Answers[q]
		label := q
		if i < len(questions) {
			label = questions[i].Question
		}
		lines = append(lines, label+": "+strings.Join(value.Answers, "; "))
	}
	return json.Marshal(map[string]any{"id": id, "method": "turn/steer", "params": map[string]any{"threadId": thread, "expectedTurnId": turn, "input": []any{map[string]any{"type": "text", "text": "Answers to your pending clarification:\n" + strings.Join(lines, "\n")}}}})
}
