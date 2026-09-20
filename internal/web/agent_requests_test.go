package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/device"
	"mitos/internal/harness"
	"mitos/internal/studio"
)

func TestAgentConsoleAcceptsSchemaValidEmptyText(t *testing.T) {
	local, engine := testServer(t)
	session, token, err := engine.Store.CreateAgentSession("codex", "Optional answer", engine.Root, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	view, err := engine.Store.IngestAgentRequest(session.ID, token, json.RawMessage(`{"id":1,"method":"item/tool/requestUserInput","params":{"threadId":"thread","turnId":"turn","questions":[{"id":"q","question":"Optional name?","schema":{"type":"string","minLength":0}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	run, _ := engine.Store.Get(view.RunIDs[0])
	r := httptest.NewRequest(http.MethodPost, "http://localhost/runs/"+run.ID+"/decisions/"+run.Decision.ID, strings.NewReader("choice=answer&value="))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	local.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("empty answer rejected: %d %s", w.Code, w.Body.String())
	}
	answer, err := engine.Store.AgentRequestStatus(session.ID, token, view.RequestID)
	if err != nil || !strings.Contains(string(answer.Body), `"answers":[""]`) {
		t.Fatal(string(answer.Body), err)
	}
}

func TestAgentProxyQuestionRoundTripThroughDeviceAPI(t *testing.T) {
	for _, mode := range []string{"answer", "superseded"} {
		t.Run(mode, func(t *testing.T) {
			local, engine := testServer(t)
			httpServer := httptest.NewServer(local)
			defer httpServer.Close()
			broker, err := harness.NewClient(httpServer.URL, "", "")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err = broker.Start(ctx, "codex", "Live request", engine.Root); err != nil {
				t.Fatal(err)
			}
			_, token, err := engine.Store.PairDevice(studio.DevicePairInput{Name: "Board", Permissions: studio.DevicePermissions{Read: true, Resolve: true}, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
			if err != nil {
				t.Fatal(err)
			}
			remote := device.New(engine, nil)
			proxyEnd, harnessEnd := net.Pipe()
			defer harnessEnd.Close()
			clientIn, clientWrite := io.Pipe()
			defer clientWrite.Close()
			defer clientIn.Close()
			done := make(chan error, 1)
			go func() { done <- harness.ProxyCodex(ctx, broker, proxyEnd, clientIn, io.Discard) }()
			native := `{"id":42,"method":"item/tool/requestUserInput","params":{"threadId":"t","turnId":"u","itemId":"i","isBlocking":true,"questions":[{"id":"name","question":"What name?"}]}}`
			if _, err = io.WriteString(harnessEnd, native+"\n"); err != nil {
				t.Fatal(err)
			}
			var card studio.DeviceCard
			for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
				snap, err := engine.Store.DeviceSnapshot(token)
				if err != nil {
					t.Fatal(err)
				}
				if len(snap.Cards) > 0 {
					card = snap.Cards[0]
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if card.DecisionID == "" {
				t.Fatal("native request never reached device")
			}
			if mode == "superseded" {
				_, err = io.WriteString(harnessEnd, `{"method":"serverRequest/resolved","params":{"threadId":"t","requestId":42}}`+"\n")
				if err != nil {
					t.Fatal(err)
				}
				for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
					r, _ := engine.Store.Get(card.RunID)
					if r.Decision.SupersededAt != nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			command := studio.DeviceCommand{Version: 1, CommandID: "confirm", DecisionID: card.DecisionID, RunID: card.RunID, Revision: card.Revision, Choice: "answer", Value: json.RawMessage(`"Alpha"`)}
			body, _ := json.Marshal(command)
			req := httptest.NewRequest("POST", "https://board/api/device/v1/resolutions", strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			remote.ServeHTTP(w, req)
			if mode == "answer" {
				if w.Code != 202 {
					t.Fatal(w.Code, w.Body.String())
				}
				_ = harnessEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
				line, err := bufio.NewReader(harnessEnd).ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(line, `"answers":["Alpha"]`) {
					t.Fatal("wrong native response", line)
				}
				r, _ := engine.Store.Get(card.RunID)
				if r.Decision.AppliedAt != nil {
					t.Fatal("pipe write falsely marked applied")
				}
				_, err = io.WriteString(harnessEnd, `{"method":"serverRequest/resolved","params":{"threadId":"t","requestId":42}}`+"\n")
				if err != nil {
					t.Fatal(err)
				}
				for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
					r, _ = engine.Store.Get(card.RunID)
					if r.Decision.AppliedAt != nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if r.Decision.AppliedAt == nil {
					t.Fatal("source acknowledgement not recorded")
				}
			} else if w.Code != 409 {
				t.Fatal("cleared native request was actionable", w.Code, w.Body.String())
			}
			cancel()
			harnessEnd.Close()
			clientWrite.Close()
			<-done
		})
	}
}
