package decisionmodel

import (
	"encoding/json"
	"errors"
)

// Gemini returns the same named choice/probability map as System One, but uses
// different usage fields and does not include the requested model in its reply.
func decodeGemini(model string, r Request, data []byte) (Answer, error) {
	var response struct {
		Answers json.RawMessage `json:"answers"`
		Usage   *struct {
			Input  int64 `json:"total_input_tokens"`
			Output int64 `json:"total_output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &response) != nil {
		return Answer{}, errors.New("invalid_response")
	}
	var usage *Usage
	if response.Usage != nil {
		usage = &Usage{InputTokens: response.Usage.Input, OutputTokens: response.Usage.Output}
	}
	normalized, err := json.Marshal(struct {
		Model   string          `json:"model"`
		Answers json.RawMessage `json:"answers"`
		Usage   *Usage          `json:"usage,omitempty"`
	}{model, response.Answers, usage})
	if err != nil {
		return Answer{}, errors.New("invalid_response")
	}
	return decode("systemone", r, normalized)
}
