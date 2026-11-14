package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestObservePreservesNativeJSON(t *testing.T) {
	raw := bytes.ReplaceAll(sizedObservation(MaxObservationBytes), []byte("x"), []byte("<"))
	if !json.Valid(raw) {
		t.Fatal("invalid fixture")
	}
	received := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, MaxObservationBytes+1))
		if err != nil {
			t.Error(err)
		}
		received <- body
		if len(body) > MaxObservationBytes {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "session", "token")
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Observe(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(<-received, raw) {
		t.Fatal("native evidence was re-encoded in transit")
	}
}
