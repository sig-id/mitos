package harness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

type waiting struct {
	questions                 []Question
	key, thread, turn, digest string
	async                     bool
	steerID                   string
	written                   bool
	cancel                    context.CancelFunc
}

// ProxyCodex runs between an ordinary app-server client and the real app-server.
// It intercepts only supported server requests. No approvals are inferred from
// terminal output and unsupported protocols retain their native client UI.
func ProxyCodex(ctx context.Context, b Broker, server io.ReadWriteCloser, clientIn io.Reader, clientOut io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var writeMu, mu sync.Mutex
	pending := map[string]*waiting{}
	write := func(line []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		n, err := server.Write(append(append([]byte(nil), line...), '\n'))
		if err == nil && n != len(line)+1 {
			return io.ErrShortWrite
		}
		return err
	}
	errorsOut := make(chan error, 2)
	go func() { <-ctx.Done(); _ = server.Close() }()
	go func() {
		scan := nativeScanner(clientIn)
		for scan.Scan() {
			line := append([]byte(nil), scan.Bytes()...)
			var envelope Envelope
			_ = json.Unmarshal(line, &envelope)
			// A client response for an intercepted ID must not bypass canonical authority.
			intercepted := false
			if len(envelope.ID) > 0 && envelope.Method == "" {
				mu.Lock()
				for key := range pending {
					if stringsRequestID(key) == string(envelope.ID) {
						intercepted = true
					}
				}
				mu.Unlock()
			}
			if intercepted {
				continue
			}
			if err := write(line); err != nil {
				errorsOut <- err
				return
			}
		}
		err := nativeScannerError(scan)
		if err == nil {
			err = io.EOF
		}
		errorsOut <- err
	}()
	// The server reader must remain live while human input is pending.
	lines := make(chan nativeFrame)
	go readNativeFrames(ctx, server, lines)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errorsOut:
			return err
		case frame, ok := <-lines:
			if !ok {
				return ctx.Err()
			}
			if frame.err != nil {
				if errors.Is(frame.err, io.EOF) {
					return nil
				}
				return frame.err
			}
			line := frame.line
			var e Envelope
			if json.Unmarshal(line, &e) != nil {
				return errors.New("invalid app-server JSON")
			}
			if observer, ok := b.(Observer); ok && ObservationRelevant(line) {
				if err := observer.Observe(ctx, line); err != nil {
					return err
				}
			}
			originalLine := line
			async := false
			if raw, ok := asyncRequest(e); ok {
				line = raw
				_ = json.Unmarshal(line, &e)
				async = true
			}
			if len(e.ID) > 0 && e.Method == "" {
				mu.Lock()
				for rpcKey, p := range pending {
					id, _ := json.Marshal(p.steerID)
					if p.async && p.written && string(e.ID) == string(id) {
						var response struct {
							Error json.RawMessage `json:"error"`
						}
						_ = json.Unmarshal(line, &response)
						state := "applied"
						if len(response.Error) > 0 && string(response.Error) != "null" {
							state = "application_blocked"
						}
						err := b.Deliver(ctx, p.key, p.digest, state)
						p.cancel()
						delete(pending, rpcKey)
						if err != nil {
							mu.Unlock()
							return err
						}
					}
				}
				mu.Unlock()
			}
			if len(e.ID) > 0 && e.Method != "" {
				normalized, err := Normalize("codex", line)
				if err == nil {
					rpcKey := normalized.Thread + "\x00" + string(e.ID)
					mu.Lock()
					old := pending[rpcKey]
					mu.Unlock()
					delivery, err := b.Submit(ctx, line)
					if err == nil && old != nil {
						continue
					}
					if err != nil {
						var status *HTTPError
						if !errors.As(err, &status) || status.Status != 422 {
							return err
						}
					} else {
						requestCtx, stop := context.WithCancel(ctx)
						p := &waiting{questions: normalized.Questions, async: async, steerID: "mitos-async:" + delivery.RequestID, key: rpcKey, thread: normalized.Thread, turn: normalized.Turn, cancel: stop}
						mu.Lock()
						pending[rpcKey] = p
						mu.Unlock()
						go func(key string, p *waiting) {
							answer, err := Wait(requestCtx, b, delivery.RequestID)
							if err != nil {
								if requestCtx.Err() == nil {
									select {
									case errorsOut <- err:
									default:
									}
								}
								return
							}
							mu.Lock()
							defer mu.Unlock()
							if requestCtx.Err() != nil {
								return
							}
							body := answer.Body
							if p.async {
								body, err = steerAnswer(p.steerID, p.thread, p.turn, p.questions, body)
								if err != nil {
									return
								}
							}
							if err = write(body); err != nil {
								_ = b.Deliver(ctx, delivery.RequestID, answer.Digest, "application_blocked")
								select {
								case errorsOut <- err:
								default:
								}
								return
							}
							p.written = true
							p.digest = answer.Digest
							p.key = delivery.RequestID
						}(rpcKey, p)
						if async {
							if _, err := clientOut.Write(append(originalLine, '\n')); err != nil {
								return err
							}
						}
						continue
					}
				}
			}
			if e.Method == "serverRequest/resolved" || e.Method == "turn/completed" || e.Method == "thread/closed" {
				var notification struct {
					Thread    string          `json:"threadId"`
					RequestID json.RawMessage `json:"requestId"`
					Turn      struct {
						ID string `json:"id"`
					} `json:"turn"`
				}
				_ = json.Unmarshal(e.Params, &notification)
				mu.Lock()
				for rpcKey, p := range pending {
					matches := p.thread == notification.Thread
					if e.Method == "serverRequest/resolved" {
						matches = matches && rpcKey == notification.Thread+"\x00"+string(notification.RequestID)
					}
					if e.Method == "turn/completed" {
						matches = matches && p.turn == notification.Turn.ID
					}
					if !matches {
						continue
					}
					p.cancel()
					// The source's acknowledgement, not a successful pipe write, marks applied.
					key := p.key
					if !p.written {
						key = digest([]byte(rpcKey))
					}
					var err error
					if p.written && e.Method == "serverRequest/resolved" {
						err = b.Deliver(ctx, key, p.digest, "applied")
					} else {
						err = b.Invalidate(ctx, key)
					}
					if err != nil {
						mu.Unlock()
						return err
					}
					delete(pending, rpcKey)
				}
				mu.Unlock()
			}
			if _, err := clientOut.Write(append(originalLine, '\n')); err != nil {
				return err
			}
		}
	}
}
func stringsRequestID(key string) string {
	for i := range key {
		if key[i] == 0 {
			return key[i+1:]
		}
	}
	return ""
}
