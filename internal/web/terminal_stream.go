package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// CloseStreams releases upgraded connections when Studio shuts down.
func (s *Server) CloseStreams() { s.stopStreams() }

// terminalStream sends bounded WebSocket frames. Bytes are base64 encoded exactly
// as in the output API, so chunks may split UTF-8 and terminal control sequences.
// The output file is the replay buffer; a slow viewer gets no growing Go queue.
func (s *Server) terminalStream(w http.ResponseWriter, r *http.Request) {
	m, id := s.engine.Desk, r.PathValue("session")
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		deskResult(w, nil, errors.New("provide a nonnegative output offset"))
		return
	}
	if _, err := m.Session(id); err != nil {
		deskResult(w, nil, err)
		return
	}
	changed := m.OutputChanges()
	output, err := m.ReadOutput(id, offset)
	if err != nil {
		deskResult(w, nil, err)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(128)
	// The server owns upgraded connection lifetime; Request.Context is no
	// longer a reliable connection lifetime after HTTP hijacking.
	ctx, cancel := context.WithCancel(s.streamContext)
	defer cancel()
	acknowledgments := make(chan int64, 1)
	go func() {
		defer cancel()
		for {
			var ack struct {
				Next int64 `json:"next"`
			}
			if err := wsjson.Read(ctx, conn, &ack); err != nil {
				return
			}
			select {
			case acknowledgments <- ack.Next:
			case <-ctx.Done():
				return
			}
		}
	}()
	write := func(value any) error {
		deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return wsjson.Write(deadline, conn, value)
	}
	acknowledge := func(next int64) bool {
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		select {
		case n := <-acknowledgments:
			return n == next
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		}
	}
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	lastStatus := ""
	drain := true
	for {
		if ctx.Err() != nil {
			return
		}
		if len(output.Data) > 0 || output.Status != lastStatus || drain {
			if err := write(output); err != nil {
				return
			}
			// One frame in flight: the browser acknowledges only after rendering.
			// This bounds browser memory as well as server-side buffering.
			if !acknowledge(output.Next) {
				return
			}
			lastStatus = output.Status
			drain = false
		}
		offset = output.Next
		// Drain all saved bytes, including output preceding the final status.
		if len(output.Data) == 64<<10 {
			drain = true
			changed = m.OutputChanges()
			output, err = m.ReadOutput(id, offset)
			if err != nil {
				return
			}
			continue
		}
		if output.Status != "running" && output.Status != "starting" && output.Status != "stopping" {
			_ = conn.Close(websocket.StatusNormalClosure, "complete")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-heartbeat.C:
			if err := write(struct {
				Heartbeat bool `json:"heartbeat"`
			}{true}); err != nil {
				return
			}
		}
		changed = m.OutputChanges()
		output, err = m.ReadOutput(id, offset)
		if err != nil {
			return
		}
	}
}
