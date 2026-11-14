package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"mitos/internal/decision"
)

func confirmationRequest(schema string) json.RawMessage {
	return json.RawMessage(`{"id":99,"method":"mcpServer/elicitation/request","params":{"threadId":"t","turnId":null,"serverName":"github","mode":"form","message":"Create the prepared issue?","requestedSchema":` + schema + `}}`)
}

func TestElicitationExactResponses(t *testing.T) {
	for _, tc := range []struct{ schema, choice, want string }{
		{`{"type":"object","properties":{}}`, "approve", `{"action":"accept","content":{},"_meta":null}`},
		{`{"type":"object","properties":{}}`, "deny", `{"action":"decline","content":null,"_meta":null}`},
		{`{"type":"object","properties":{"approved":{"type":"boolean","default":true}},"required":["approved"]}`, "false", `{"action":"accept","content":{"approved":false},"_meta":null}`},
		{`{"type":"object","properties":{"choice":{"type":"string","enum":["once","never"]}}}`, "value_0", `{"action":"accept","content":{"choice":"once"},"_meta":null}`},
	} {
		r, err := Normalize("codex", confirmationRequest(tc.schema))
		if err != nil {
			t.Fatal(err)
		}
		if r.Cards[0].Default != nil || len(r.Questions) != 0 {
			t.Fatal("connector permission became an implicit answer or ordinary question")
		}
		body, err := Response("codex", r, []decision.Resolution{{Choice: tc.choice}})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(body, &got) != nil || got.ID != 99 {
			t.Fatal(string(body))
		}
		var expected, actual any
		json.Unmarshal([]byte(tc.want), &expected)
		json.Unmarshal(got.Result, &actual)
		a, _ := json.Marshal(actual)
		b, _ := json.Marshal(expected)
		if !bytes.Equal(a, b) {
			t.Fatalf("%s != %s", a, b)
		}
	}
}

func TestElicitationUnsupportedSchemasRemainNative(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","properties":{"password":{"type":"string"}}}`,
		`{"type":"object","properties":{"x":{"type":"boolean","const":true}}}`,
		`{"type":"object","properties":{"x":{"type":"object"}}}`,
		`{"type":"object","properties":{},"required":["missing"]}`,
		`{"type":"object","properties":{},"allOf":[]}`,
		`{"type":"object","properties":{},"properties":{"x":{"type":"boolean"}}}`,
	} {
		if _, err := Normalize("codex", confirmationRequest(schema)); err == nil {
			t.Fatalf("accepted unsupported schema: %s", schema)
		}
	}
}

type confirmationBroker struct {
	answer   Delivery
	requests int
}

func (b *confirmationBroker) Submit(_ context.Context, raw json.RawMessage) (Delivery, error) {
	b.requests++
	r, err := Normalize("codex", raw)
	if err != nil {
		return Delivery{}, err
	}
	body, err := Response("codex", r, []decision.Resolution{{Choice: "approve"}})
	b.answer = Delivery{RequestID: r.Key, State: "accepted", Body: body, Digest: "recorded"}
	return b.answer, err
}
func (b *confirmationBroker) Get(context.Context, string) (Delivery, error)         { return b.answer, nil }
func (b *confirmationBroker) Deliver(context.Context, string, string, string) error { return nil }
func (b *confirmationBroker) Invalidate(context.Context, string) error              { return nil }

func TestHeadlessConnectorRequestPreservesFinalResult(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			received := make(chan string, 1)
			go func() {
				defer server.Close()
				scan := bufio.NewScanner(server)
				for scan.Scan() {
					var req Envelope
					json.Unmarshal(scan.Bytes(), &req)
					switch req.Method {
					case "initialize":
						fmt.Fprintln(server, `{"id":1,"result":{}}`)
					case "thread/start":
						fmt.Fprintln(server, `{"id":2,"result":{"thread":{"id":"t"}}}`)
					case "turn/start":
						fmt.Fprintln(server, `{"id":3,"result":{"turn":{"id":"u"}}}`)
						schema := `{"type":"object","properties":{}}`
						if !supported {
							schema = `{"type":"object","properties":{"code":{"type":"number"}}}`
						}
						fmt.Fprintln(server, string(confirmationRequest(schema)))
					case "":
						if string(req.ID) != "99" {
							continue
						}
						received <- scan.Text()
						fmt.Fprintln(server, `{"method":"serverRequest/resolved","params":{"threadId":"t","requestId":99}}`)
						fmt.Fprintln(server, `{"method":"item/completed","params":{"item":{"type":"agentMessage","phase":"final_answer","text":"Saved publication report."}}}`)
						fmt.Fprintln(server, `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"u","status":"completed"}}}`)
						return
					}
				}
			}()
			broker := &confirmationBroker{}
			var output bytes.Buffer
			result := ""
			err := RunCodex(ctx, broker, client, strings.NewReader(""), &output, t.TempDir(), "Publish", CodexOptions{Headless: true, Result: func(s string) error { result = s; return nil }})
			if err != nil || result != "Saved publication report." {
				t.Fatalf("%v %q %s", err, result, output.String())
			}
			want := "accept"
			if !supported {
				want = "cancel"
			}
			if body := <-received; !strings.Contains(body, `"action":"`+want+`"`) {
				t.Fatal(body)
			}
			if supported && broker.requests != 1 || !supported && broker.requests != 0 {
				t.Fatal("incorrect broker routing", broker.requests)
			}
		})
	}
}
