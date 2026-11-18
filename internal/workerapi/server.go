// Package workerapi serves the independently versioned worker control boundary.
package workerapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mitos/internal/structuredworker"
	"mitos/internal/studio"
)

type Server struct{ Engine *studio.Engine }

func (s Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	send := func(status int, value any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(value) }
	if r.TLS == nil {
		send(403, map[string]string{"error": "tls_required"})
		return
	}
	if r.Header.Get(structuredworker.VersionHeader) != structuredworker.Version {
		send(400, map[string]string{"error": "unsupported_worker_version"})
		return
	}
	w.Header().Set(structuredworker.VersionHeader, structuredworker.Version)
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || len(auth[0]) != 71 {
		send(401, map[string]string{"error": "unauthenticated"})
		return
	}
	token := strings.TrimPrefix(auth[0], "Bearer ")
	if r.Method != "POST" {
		send(404, map[string]string{"error": "not_found"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 17<<20)
	decode := func(value any) error {
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(value); err != nil {
			return err
		}
		if d.Decode(new(any)) != io.EOF {
			return errors.New("trailing_data")
		}
		return nil
	}
	var value any
	var err error
	switch r.URL.Path {
	case "/worker/v1/register":
		var in structuredworker.Register
		if decode(&in) != nil {
			send(400, map[string]string{"error": "invalid_request"})
			return
		}
		value, err = s.Engine.Store.RegisterWorker(token, in)
	case "/worker/v1/claim":
		var in structuredworker.Claim
		if decode(&in) != nil {
			send(400, map[string]string{"error": "invalid_request"})
			return
		}
		wait := 0
		if text := r.URL.Query().Get("wait"); text != "" {
			wait, err = strconv.Atoi(text)
			if err != nil || wait < 0 || wait > 20 {
				send(400, map[string]string{"error": "invalid_wait"})
				return
			}
		}
		deadline := time.NewTimer(time.Duration(wait) * time.Second)
		defer deadline.Stop()
		for {
			var receipt structuredworker.Receipt
			receipt, err = s.Engine.Store.WorkerClaim(token, in)
			value = receipt
			if err != nil || receipt.State != "empty" || wait == 0 {
				break
			}
			select {
			case <-r.Context().Done():
				return
			case <-deadline.C:
				wait = 0
			case <-time.After(200 * time.Millisecond):
			}
		}
	case "/worker/v1/context":
		var in structuredworker.Binding
		if decode(&in) != nil {
			send(400, map[string]string{"error": "invalid_request"})
			return
		}
		value, err = s.Engine.WorkerContext(token, in)
	case "/worker/v1/progress", "/worker/v1/renew", "/worker/v1/checkpoint", "/worker/v1/result", "/worker/v1/release":
		var in structuredworker.Mutation
		if decode(&in) != nil {
			send(400, map[string]string{"error": "invalid_request"})
			return
		}
		if in.Kind != strings.TrimPrefix(r.URL.Path, "/worker/v1/") {
			send(400, map[string]string{"error": "operation_mismatch"})
			return
		}
		value, err = s.Engine.Store.WorkerMutate(token, in)
	default:
		send(404, map[string]string{"error": "not_found"})
		return
	}
	if err != nil {
		status, code := 422, "invalid_worker_request"
		switch {
		case errors.Is(err, studio.ErrUnauthenticated):
			status, code = 401, "unauthenticated"
		case errors.Is(err, studio.ErrForbidden):
			status, code = 403, "forbidden"
		case errors.Is(err, studio.ErrStaleBinding):
			status, code = 409, "stale_binding"
		case errors.Is(err, studio.ErrConflict):
			status, code = 409, "request_conflict"
		case errors.Is(err, studio.ErrNotFound):
			status, code = 404, "not_found"
		}
		send(status, map[string]string{"error": code})
		return
	}
	send(200, value)
}
