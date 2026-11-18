package structuredworker

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"mitos/internal/failpoint"
	"path/filepath"
	"time"
)

func RequestID() string {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (c *HTTPClient) Work(ctx context.Context, id string, capabilities []string, executor Executor, once bool) error {
	identity := Identity{ID: id, Incarnation: RequestID()}
	var registered Receipt
	if err := c.Call(ctx, "register", Register{Features: Features, RequestID: RequestID(), Incarnation: identity.Incarnation, Capabilities: capabilities}, &registered); err != nil {
		return err
	}
	var wake <-chan struct{}
	if c.WakeFactory != nil {
		var close func()
		var err error
		wake, close, err = c.WakeFactory(ctx, registered.WakeAuthority)
		if err != nil {
			return err
		}
		defer close()
	}
	for {
		var claimed Receipt
		if err := c.Call(ctx, "claim", Claim{Identity: identity, RequestID: RequestID()}, &claimed); err != nil {
			return err
		}
		if claimed.Binding == nil {
			timer := time.NewTimer(time.Duration(1000+rand.IntN(1001)) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-wake:
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		binding := *claimed.Binding
		failpoint.Hit("worker.after_claim")
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
	// renewErr carries the renewal failure out of the watch goroutine: a
	// stale binding (409) must take the existing recovery path and keep the
	// worker polling, not exit it as a process error.
	var renewErr error
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
					renewErr = err
					cancel()
					return
				}
			}
		}
	}()
	executor.Base = filepath.Join(executor.Base, b.RunID)
	executor.Action = func(ctx context.Context, kind string, raw json.RawMessage) (Receipt, error) {
		var receipt Receipt
		err := c.Call(ctx, kind, Mutation{Binding: b, RequestID: RequestID(), Kind: kind, Data: raw}, &receipt)
		return receipt, err
	}
	executor.Wait = func(ctx context.Context, raw json.RawMessage) (Receipt, error) {
		var receipt Receipt
		err := c.Call(ctx, "wait", Mutation{Binding: b, RequestID: RequestID(), Kind: "wait", Data: raw}, &receipt)
		return receipt, err
	}
	result, err := executor.Execute(ctx, assignment)
	if errors.Is(err, ErrDormant) {
		return nil
	}
	if err != nil {
		// Synchronize with the renewal watcher before reading its failure, so
		// a stale binding surfaces as its own 409 instead of a process error
		// that would stop the whole worker.
		cancel()
		<-done
		if renewErr != nil {
			err = renewErr
		}
	} else {
		var raw []byte
		if raw, err = json.Marshal(result); err == nil {
			var receipt Receipt
			request := Mutation{Binding: b, RequestID: RequestID(), Kind: "result", Data: raw}
			failpoint.Hit("worker.before_result")
			err = c.Call(ctx, "result", request, &receipt)
			if err == nil && failpoint.Enabled("worker.duplicate_result") {
				var duplicate Receipt
				err = c.Call(ctx, "result", request, &duplicate)
				if err == nil && (duplicate.Seq != receipt.Seq || duplicate.SHA256 != receipt.SHA256 || duplicate.State != receipt.State) {
					err = errors.New("duplicate result changed receipt")
				}
			}

			if err == nil && receipt.State != "accepted" {
				err = errors.New("result not accepted")
			}
			// An accepted result already journaled its usage atomically; only
			// unaccepted attempts may still forward it with their release.
			if err == nil {
				result = Result{}
			}
		}
	}
	if err != nil {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		// Failed attempts still forward their settled usage: the server
		// journals it with the release so consumed tokens are not lost.
		var receipt Receipt
		var data json.RawMessage
		if result.Usage != nil {
			data, _ = json.Marshal(Release{Usage: result.Usage})
		}
		_ = c.Call(releaseCtx, "release", Mutation{Binding: b, RequestID: RequestID(), Kind: "release", Data: data}, &receipt)
	}
	return err
}
