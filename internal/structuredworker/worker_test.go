package structuredworker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"mitos/agents"
	"mitos/internal/compute"
)

func TestWorkerWakeAndBoundedPollingRecoverMissedHints(t *testing.T) {
	var polls atomic.Int64
	polled := make(chan struct{}, 16)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(VersionHeader, Version)
		if r.URL.Path == "/worker/v1/register" {
			_ = json.NewEncoder(w).Encode(Receipt{State: "registered", WakeAuthority: "home"})
			return
		}
		polls.Add(1)
		polled <- struct{}{}
		_ = json.NewEncoder(w).Encode(Receipt{State: "empty"})
	}))
	defer server.Close()
	wake := make(chan struct{}, 1)
	client := HTTPClient{URL: server.URL, Token: "test", HTTP: server.Client(), WakeFactory: func(context.Context, string) (<-chan struct{}, func(), error) { return wake, func() {}, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Work(ctx, "worker", []string{"mitos.generalist"}, Executor{}, false) }()
	select {
	case <-polled:
	case <-time.After(time.Second):
		t.Fatal("initial poll missing")
	}
	wake <- struct{}{}
	select {
	case <-polled:
	case <-time.After(700 * time.Millisecond):
		t.Fatal("wake did not prompt poll")
	}
	// Deliberately send no further notification: a bounded jittered scan still runs.
	select {
	case <-polled:
	case <-time.After(2200 * time.Millisecond):
		t.Fatal("missed hint stranded worker")
	}
	if polls.Load() > 4 {
		t.Fatal("unbounded scan", polls.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

// A stale binding during a running attempt (renewal answers 409) must take the
// recovery path: the worker releases, keeps polling and stays alive instead of
// exiting with a process error.
func TestRenewalStaleBindingKeepsWorkerPolling(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int64
	released := make(chan struct{}, 4)
	claimed := make(chan struct{}, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(VersionHeader, Version)
		switch r.URL.Path {
		case "/worker/v1/register":
			_ = json.NewEncoder(w).Encode(Receipt{State: "registered"})
		case "/worker/v1/claim":
			var in Claim
			_ = json.NewDecoder(r.Body).Decode(&in)
			if claims.Add(1) == 1 {
				binding := Binding{Identity: in.Identity, TaskID: "task_renew", RunID: "run_renew", Generation: 1, Epoch: "epoch", BudgetSeconds: 60, LeaseSeconds: 3}
				claimed <- struct{}{}
				_ = json.NewEncoder(w).Encode(Receipt{State: "claimed", Binding: &binding})
				return
			}
			_ = json.NewEncoder(w).Encode(Receipt{State: "empty"})
		case "/worker/v1/context":
			var b Binding
			_ = json.NewDecoder(r.Body).Decode(&b)
			_ = json.NewEncoder(w).Encode(Context{Binding: b, Definition: agents.Definition{ID: "mitos.generalist"}, ResultSchema: []byte(`{"type":"object"}`), MaxTurns: 5})
		case "/worker/v1/renew":
			http.Error(w, `{"error":"stale_binding"}`, http.StatusConflict)
		case "/worker/v1/release":
			released <- struct{}{}
			_ = json.NewEncoder(w).Encode(Receipt{State: "released"})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := HTTPClient{URL: server.URL, Token: "test", HTTP: server.Client()}
	executor := Executor{Provider: &compute.Local{}, Executable: script, Base: dir, Scoped: false, Preview: io.Discard, Activity: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Work(ctx, "worker-renew", []string{"mitos.generalist"}, executor, false) }()
	select {
	case <-claimed:
	case <-time.After(2 * time.Second):
		t.Fatal("no attempt was claimed")
	}
	// The renewal watch fires within a lease third and the attempt unwinds.
	select {
	case <-released:
	case <-time.After(4 * time.Second):
		t.Fatal("stale attempt was never released")
	}
	// The worker must keep polling after the stale attempt, not exit.
	select {
	case <-claimed: // a later claim (there is none; just guard against exit)
		t.Fatal("unexpected second claim")
	case err := <-done:
		t.Fatal("worker exited after a stale binding", err)
	case <-time.After(2 * time.Second):
	}
	if claims.Load() < 2 {
		t.Fatalf("worker did not resume polling after the stale attempt: %d claims", claims.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}
