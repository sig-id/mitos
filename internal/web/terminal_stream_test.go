package web

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"mitos/internal/agentcli"
	"mitos/internal/desk"
)

func TestTerminalStreamReplayReconnectAndExit(t *testing.T) {
	s, e := testServer(t)
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	// A backlog crossing a frame boundary, arbitrary bytes, then wait for input.
	e.Desk.Agents, err = agentcli.New(nil, agentcli.Config{Applications: []agentcli.Definition{{ID: "stream", Name: "Stream", Kind: "application", Executables: []string{shell}, Args: []string{"-c", `printf '%070000d' 0; printf '\377\000'; read -r line; printf 'END:%s' "$line"`}, Prompt: "none"}}}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Desk.CreateProject("Stream", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	session, err := e.Desk.StartAgent(p.ID, agentcli.Request{AgentID: "stream"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	open := func(offset int64) (*websocket.Conn, *outputDecoder) {
		t.Helper()
		conn, res, err := websocket.Dial(ctx, strings.Replace(server.URL, "http:", "ws:", 1)+"/api/sessions/"+session.ID+"/stream?offset="+strconv.FormatInt(offset, 10), nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 101 {
			t.Fatal(res.Status)
		}
		conn.SetReadLimit(128 << 10)
		return conn, &outputDecoder{ctx, conn}
	}

	// A stream holds at most one unacknowledged frame, even with a large backlog.
	held, _ := open(0)
	var first desk.Output
	if err := wsjson.Read(ctx, held, &first); err != nil {
		t.Fatal(err)
	}
	next := make(chan error, 1)
	go func() { var frame desk.Output; next <- wsjson.Read(ctx, held, &frame) }()
	select {
	case err := <-next:
		t.Fatal("sent a second frame before acknowledgment", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := wsjson.Write(ctx, held, struct {
		Next int64 `json:"next"`
	}{first.Next}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-next:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("acknowledgment did not release output")
	}
	held.CloseNow()
	rejected, response, err := websocket.Dial(ctx, strings.Replace(server.URL, "http:", "ws:", 1)+"/api/sessions/"+session.ID+"/stream?offset=0", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://other.example"}}})
	if err == nil {
		rejected.CloseNow()
		t.Fatal("cross-origin terminal admitted")
	}
	if response == nil || response.StatusCode != 403 {
		t.Fatal("cross-origin terminal did not return forbidden", response)
	}
	res, decoder := open(0)
	var received []byte
	var offset int64
	for len(received) < 70002 {
		var frame desk.Output
		if err := decoder.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		if len(frame.Data) > 64<<10 || frame.Next != offset+int64(len(frame.Data)) {
			t.Fatal("invalid frame", frame.Next)
		}
		offset = frame.Next
		received = append(received, frame.Data...)
	}
	expected := append([]byte(strings.Repeat("0", 70000)), 255, 0)
	if !bytes.Equal(received, expected) {
		t.Fatal("replay changed terminal bytes")
	}
	res.CloseNow()
	running, _ := e.Desk.Session(session.ID)
	if running.Status != "running" {
		t.Fatal("disconnect stopped process")
	}
	res, decoder = open(offset)
	defer res.CloseNow()
	var frame desk.Output
	if err := decoder.Decode(&frame); err != nil {
		t.Fatal(err)
	}
	if len(frame.Data) != 0 || frame.Next != offset {
		t.Fatal("reconnect duplicated output")
	}
	if err := e.Desk.Input(session.ID, "finish", "done\n"); err != nil {
		t.Fatal(err)
	}
	var tail []byte
	status := ""
	for {
		if err := decoder.Decode(&frame); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if frame.Next != offset+int64(len(frame.Data)) {
			t.Fatal("stream skipped bytes")
		}
		offset = frame.Next
		tail = append(tail, frame.Data...)
		status = frame.Status
	}
	if !bytes.Contains(tail, []byte("END:done")) || status != "exited" {
		t.Fatal("missing live output or final status", string(tail), status)
	}
	res, decoder = open(0)
	defer res.CloseNow()
	var replay []byte
	for {
		if err := decoder.Decode(&frame); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		replay = append(replay, frame.Data...)
	}
	if !bytes.Equal(replay, append(received, tail...)) {
		t.Fatal("finished stream lost its tail")
	}
	for _, path := range []string{"/api/sessions/missing/stream?offset=0", "/api/sessions/" + session.ID + "/stream?offset=-1", "/api/sessions/" + session.ID + "/stream?offset=9999999"} {
		req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+path, nil)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode < 400 {
			t.Fatal("invalid stream admitted", path)
		}
	}
}

// The same acknowledgment boundary as the browser: after a frame is consumed.
type outputDecoder struct {
	ctx  context.Context
	conn *websocket.Conn
}

func (d *outputDecoder) Decode(frame *desk.Output) error {
	if err := wsjson.Read(d.ctx, d.conn, frame); err != nil {
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			return io.EOF
		}
		return err
	}
	return wsjson.Write(d.ctx, d.conn, struct {
		Next int64 `json:"next"`
	}{frame.Next})
}
