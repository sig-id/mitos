package device

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPTranscriberProtocolAndTimeout(t *testing.T) {
	pcm := wav()[44:]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-secret" {
			t.Error("missing provider credential")
		}
		if r.ParseMultipartForm(1<<20) != nil {
			t.Error("invalid multipart")
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer file.Close()
		b, _ := io.ReadAll(file)
		samples, err := parseWAV(b)
		if err != nil || !bytes.Equal(samples, pcm) || r.FormValue("model") != "test-model" || r.FormValue("language") != "en" {
			t.Error("invalid transcription payload")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "Reviewed answer"})
	}))
	defer server.Close()
	p, err := NewHTTPTranscriber(server.URL, "test-model", "local-secret")
	if err != nil {
		t.Fatal(err)
	}
	value, err := p.Transcribe(context.Background(), pcm, "en")
	if err != nil || value != "Reviewed answer" {
		t.Fatal(value, err)
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer slow.Close()
	defer close(release)
	p, _ = NewHTTPTranscriber(slow.URL, "test-model", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = p.Transcribe(ctx, pcm, "en"); err == nil {
		t.Fatal("provider ignored cancellation")
	}
	if _, err = NewHTTPTranscriber("http://remote.invalid/transcribe", "model", ""); err == nil {
		t.Fatal("plaintext remote STT accepted")
	}
}
