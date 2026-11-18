package structuredworker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

func RequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (c *HTTPClient) Work(ctx context.Context, id string, capabilities []string, executor Executor, once bool) error {
	identity := Identity{ID: id, Incarnation: RequestID()}
	var registered Receipt
	if err := c.Call(ctx, "register", Register{RequestID: RequestID(), Incarnation: identity.Incarnation, Capabilities: capabilities}, &registered); err != nil {
		return err
	}
	for {
		var claimed Receipt
		if err := c.Call(ctx, "claim?wait=20", Claim{Identity: identity, RequestID: RequestID()}, &claimed); err != nil {
			return err
		}
		if claimed.Binding == nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		binding := *claimed.Binding
		if err := c.workAttempt(ctx, binding, executor); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var remote *HTTPError
			if !errors.As(err, &remote) || remote.Status != 409 {
				return err
			}
		}
		if once {
			return nil
		}
	}
}
func (c *HTTPClient) workAttempt(ctx context.Context, b Binding, executor Executor) error {
	var assignment Context
	if err := c.Call(ctx, "context", b, &assignment); err != nil {
		return err
	}
	if assignment.Binding != b {
		return errors.New("worker context binding mismatch")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Duration(b.LeaseSeconds/3) * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				var receipt Receipt
				if err := c.Call(ctx, "renew", Mutation{Binding: b, RequestID: RequestID(), Kind: "renew"}, &receipt); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	executor.Base = filepath.Join(executor.Base, b.RunID)
	result, err := executor.Execute(ctx, assignment)
	if err == nil {
		var raw []byte
		raw, err = json.Marshal(result)
		if err == nil {
			var receipt Receipt
			err = c.Call(ctx, "result", Mutation{Binding: b, RequestID: RequestID(), Kind: "result", Data: raw}, &receipt)
			if err == nil && receipt.State != "accepted" {
				err = errors.New("result not accepted")
			}
		}
	}
	if err != nil {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		var receipt Receipt
		_ = c.Call(releaseCtx, "release", Mutation{Binding: b, RequestID: RequestID(), Kind: "release"}, &receipt)
	}
	return err
}
