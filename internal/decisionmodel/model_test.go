package decisionmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func config(endpoint string) Config {
	return Config{Protocol: "systemone", Endpoint: endpoint, Model: "fixture", Hooks: []string{"before_segment"}, Instructions: "Choose the eligible action or escalate.", TimeoutMS: 1000, MaxCalls: 3, MinConfidence: .7, MinProbability: .8}
}
func request() Request {
	return Request{ID: "test-call", State: json.RawMessage(`{"intent":"inspect"}`), Choices: []Choice{{"continue", "Start the segment"}, {"escalate", "Ask a human"}}}
}

const systemAnswer = `{"model":"fixture-v1","answers":{"action":{"type":"choice","choice":"continue","confidence":0.9,"probabilities":{"continue":0.95,"escalate":0.05}}},"usage":{"input_tokens":12,"output_tokens":0}}`
const openAIAnswer = `{"model":"fixture-v1","answers":[{"type":"choice","name":"action","choice":"continue","confidence":0.9,"probabilities":[{"value":"continue","probability":0.95},{"value":"escalate","probability":0.05}]}],"usage":{"input_tokens":12,"output_tokens":0}}`

func TestProviderWireContracts(t *testing.T) {
	t.Setenv("MITOS_TEST_MODEL_KEY", "secret-for-test")
	for _, protocol := range []string{"systemone", "openai_decisions"} {
		t.Run(protocol, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer secret-for-test" || r.Header.Get("X-Client-Request-Id") != "test-call" {
					t.Error("missing request identity/auth")
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if string(body["model"]) != `"fixture"` {
					t.Error("model not preserved")
				}
				if protocol == "systemone" {
					if string(body["state"]) != string(request().State) || body["input"] != nil {
						t.Errorf("wrong state: %s", body["state"])
					}
					var questions map[string]struct {
						Type, Instructions string
						Criteria           map[string]string
					}
					if json.Unmarshal(body["questions"], &questions) != nil || questions["action"].Type != "choice" || len(questions["action"].Criteria) != 2 {
						t.Error("invalid System One question")
					}
					fmt.Fprint(w, systemAnswer)
				} else {
					var input string
					if json.Unmarshal(body["input"], &input) != nil || input != string(request().State) || body["state"] != nil {
						t.Error("invalid OpenAI input")
					}
					var questions []struct {
						Type, Name, Instructions string
						Choices                  []Choice
					}
					if json.Unmarshal(body["questions"], &questions) != nil || len(questions) != 1 || questions[0].Name != "action" || len(questions[0].Choices) != 2 {
						t.Error("invalid OpenAI question")
					}
					fmt.Fprint(w, openAIAnswer)
				}
			}))
			defer srv.Close()
			c := config(srv.URL)
			c.Protocol = protocol
			c.APIKeyEnv = "MITOS_TEST_MODEL_KEY"
			a, err := Decide(context.Background(), c, request())
			if err != nil || a.Choice != "continue" || a.Model != "fixture-v1" || a.Usage.InputTokens != 12 || !a.Meets(c) {
				t.Fatalf("bad normalization: %+v %v", a, err)
			}
		})
	}
}
func TestRejectMalformedOrUnsafeAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"wrong_choice":       strings.ReplaceAll(systemAnswer, `"choice":"continue"`, `"choice":"execute_shell"`),
		"missing_confidence": strings.ReplaceAll(systemAnswer, `"confidence":0.9,`, ``),
		"null_probability":   strings.ReplaceAll(systemAnswer, `"escalate":0.05`, `"escalate":null`),
		"bad_sum":            strings.ReplaceAll(systemAnswer, `"escalate":0.05`, `"escalate":0.5`),
		"not_maximum":        strings.ReplaceAll(systemAnswer, `"choice":"continue"`, `"choice":"escalate"`),
		"wrong_question":     strings.ReplaceAll(systemAnswer, `"action":`, `"other":`),
		"trailing_json":      systemAnswer + `{}`,
		"refusal":            `{"answers":{"action":{"type":"refusal"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decode("systemone", request(), []byte(body)); err == nil {
				t.Fatal("accepted invalid answer")
			}
		})
	}
	body := strings.ReplaceAll(openAIAnswer, `"value":"escalate"`, `"value":"continue"`)
	if _, err := decode("openai_decisions", request(), []byte(body)); err == nil {
		t.Fatal("accepted duplicate probability")
	}
}
func TestTransportBoundsNoRetryOrRedirect(t *testing.T) {
	for _, mode := range []string{"redirect", "error", "oversize", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch mode {
				case "redirect":
					http.Redirect(w, r, "/elsewhere", 307)
				case "error":
					http.Error(w, "secret diagnostic", 429)
				case "oversize":
					fmt.Fprint(w, strings.Repeat("x", MaxBytes+1))
				case "timeout":
					io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
				}
			}))
			c := config(srv.URL)
			c.TimeoutMS = 30
			_, err := Decide(context.Background(), c, request())
			srv.Close()
			if err == nil || strings.Contains(err.Error(), "secret") || calls != 1 {
				t.Fatalf("unsafe transport: calls=%d err=%v", calls, err)
			}
		})
	}
}
func TestConfigAndCancellation(t *testing.T) {
	for _, endpoint := range []string{"http://remote.example/v1/systemone", "https://user:pass@example.com/path", "https://example.com/path?key=secret", "file:///tmp/model"} {
		if config(endpoint).Validate() == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
	c := config("http://127.0.0.1:1/v1/systemone")
	c.APIKeyEnv = "MITOS_TEST_ABSENT_MODEL_KEY"
	t.Setenv(c.APIKeyEnv, "")
	if _, err := Decide(context.Background(), c, request()); err == nil || err.Error() != "credential_unavailable" {
		t.Fatal(err)
	}
	c.APIKeyEnv = ""
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := Decide(ctx, c, request())
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("cancelled request did not stop")
	}
}

func TestManifestRequiresExplicitThresholds(t *testing.T) {
	c := config("http://127.0.0.1:8000/v1/systemone")
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"min_confidence", "min_probability"} {
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, field)
		invalid, _ := json.Marshal(fields)
		var decoded Config
		if err := json.Unmarshal(invalid, &decoded); err == nil {
			t.Fatalf("omitted %s silently weakened policy", field)
		}
	}
	var decoded Config
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	c.Hooks = []string{"before_segment", "before_segment"}
	if c.Validate() == nil {
		t.Fatal("duplicate hook accepted")
	}
}
