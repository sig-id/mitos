package decisionmodel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDirectKeyIsWriteOnly(t *testing.T) {
	c := config("https://example.com/decisions")
	data, _ := json.Marshal(c)
	var input map[string]any
	_ = json.Unmarshal(data, &input)
	input["api_key"] = "direct-test-secret"
	data, _ = json.Marshal(input)
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "direct-test-secret" {
		t.Fatal("key not decoded")
	}
	data, _ = json.Marshal(c)
	if strings.Contains(string(data), "direct-test-secret") || strings.Contains(string(data), `"api_key"`) {
		t.Fatal("secret serialized")
	}
	for _, field := range []string{"api_key_env", "api_key_ref"} {
		input[field] = strings.Repeat("a", 64)
		data, _ = json.Marshal(input)
		if err := json.Unmarshal(data, &c); err == nil {
			t.Fatal("ambiguous credentials accepted", field)
		}
		delete(input, field)
	}
	for _, key := range []string{"bad\nkey", "bad key", strings.Repeat("x", 8193)} {
		input["api_key"] = key
		data, _ = json.Marshal(input)
		if err := json.Unmarshal(data, &c); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
}

func TestDedicatedDecisionProviderContracts(t *testing.T) {
	for _, protocol := range []string{"systemone", "openai_decisions", "gemini_decisions", "openrouter_decisions"} {
		for _, auth := range []string{"direct", "env"} {
			t.Run(protocol+"/"+auth, func(t *testing.T) {
				t.Setenv("DECISION_TEST_KEY", "test-key")
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if protocol == "gemini_decisions" {
						if r.Header.Get("x-goog-api-key") != "test-key" || r.Header.Get("Authorization") != "" {
							t.Error("incorrect Gemini authentication")
						}
					} else if r.Header.Get("Authorization") != "Bearer test-key" {
						t.Error("missing bearer key")
					}
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["messages"] != nil || body["response_format"] != nil {
						t.Error("used generative protocol")
					}
					if protocol == "gemini_decisions" || protocol == "openrouter_decisions" {
						field := "input"
						if protocol == "openrouter_decisions" {
							field = "state"
						}
						var state string
						if json.Unmarshal(body[field], &state) != nil || state != string(request().State) {
							t.Error("incorrect state")
						}
						var questions map[string]struct {
							Type         string
							Instructions string
							Criteria     map[string]string
						}
						if json.Unmarshal(body["questions"], &questions) != nil || questions["action"].Type != "choice" || len(questions["action"].Criteria) != 2 {
							t.Error("incorrect typed question")
						}
					}
					response := systemAnswer
					if protocol == "openai_decisions" {
						response = openAIAnswer
					}
					if protocol == "gemini_decisions" {
						response = strings.ReplaceAll(systemAnswer, `"input_tokens":12,"output_tokens":0`, `"total_input_tokens":12,"total_output_tokens":0`)
						response = strings.ReplaceAll(response, `"model":"fixture-v1",`, "")
					}
					_, _ = w.Write([]byte(response))
				}))
				defer server.Close()
				c := config(server.URL)
				c.Protocol = protocol
				if auth == "direct" {
					c.APIKey = "test-key"
				} else {
					c.APIKeyEnv = "DECISION_TEST_KEY"
				}
				answer, err := Decide(context.Background(), c, request())
				if err != nil || answer.Choice != "continue" || answer.Usage == nil || answer.Usage.InputTokens != 12 {
					t.Fatalf("invalid answer: %+v %v", answer, err)
				}
				if protocol == "gemini_decisions" && answer.Model != c.Model {
					t.Fatal("requested model lost")
				}
			})
		}
	}
}

func TestGeminiInvalidAnswersFailClosed(t *testing.T) {
	for _, reply := range []string{
		`{"answers":{"action":{"type":"refusal"}}}`,
		`{"answers":{"action":{"type":"choice","choice":"continue","probabilities":{"continue":1,"escalate":0}}}}`,
		strings.Replace(systemAnswer, `"continue":0.95`, `"continue":null`, 1),
		strings.Replace(systemAnswer, `"continue":0.95`, `"continue":0.2`, 1),
	} {
		if _, err := decodeGemini("gemini-decision-1", request(), []byte(reply)); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
}
