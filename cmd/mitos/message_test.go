package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMessageCLIInboxRoutesQueryAndServerIdentity(t *testing.T) {
	seen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if r.Method != "GET" || r.URL.Path != "/api/messaging/inbox" || r.URL.Query().Get("mailbox") != "operator" || r.URL.Query().Get("after") != "12" || r.Header.Get("X-Mitos-Server") != "server_one" {
			t.Errorf("wrong messaging request: %s %s", r.Method, r.URL)
		}
		w.Write([]byte("[]\n"))
	}))
	defer server.Close()
	if err := run([]string{"message", "inbox", "--url", server.URL, "--server", "server_one", "--after", "12"}); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("CLI did not call server")
	}
	if err := run([]string{"message", "send"}); err == nil {
		t.Fatal("send without stable request file accepted")
	}
}
