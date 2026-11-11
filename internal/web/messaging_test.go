package web

import (
	"fmt"
	"mitos/internal/messaging"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMessagingHTTPDisabledValidationAndServerScope(t *testing.T) {
	s, e := testServer(t)
	r := request(s, "GET", "/api/messaging", "", false)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"enabled":false`) || !strings.Contains(r.Body.String(), e.Store.Home().ID) {
		t.Fatal(r.Code, r.Body.String())
	}
	r = deliveryRequest(s, "/api/messaging/outbox", "", `{}`)
	if r.Code != 503 {
		t.Fatal("disabled MQTT accepted send", r.Code)
	}
	for _, path := range []string{"/api/messaging/inbox", "/api/messaging/inbox?project=studio&mailbox=%23", "/api/messaging/inbox?project=studio&mailbox=operator&after=-1"} {
		r = request(s, "GET", path, "", false)
		if r.Code != 400 {
			t.Fatal(path, r.Code)
		}
	}
	r = request(s, "GET", "/api/messaging/inbox?project=studio&mailbox=operator", "", false)
	if r.Code != 200 || strings.TrimSpace(r.Body.String()) != "[]" {
		t.Fatal(r.Code, r.Body.String())
	}
	r = request(s, "GET", "/api/messaging/outbox/missing", "", false)
	if r.Code != 404 {
		t.Fatal(r.Code)
	}
	r = deliveryRequest(s, "/api/messaging/inbox/sender/missing/ack", "", `{"generation":0}`)
	if r.Code != 404 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = deliveryRequest(s, "/api/messaging/inbox/sender/missing/ack", "", fmt.Sprintf(`{"generation":%q}`, "bad"))
	if r.Code != 400 {
		t.Fatal(r.Code)
	}
}

func TestMessagingHTTPQueueConflictBoundsAndOrigin(t *testing.T) {
	s, e := testServer(t)
	if err := e.StartMessaging(messaging.Config{Broker: "mqtt://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"id":"http-note","to":%q,"project":"studio","mailbox":"operator","kind":"note","type":"text","deadline":%q,"payload":{"body":"hello"}}`, e.Store.Home().ID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano))
	for range 2 {
		r := deliveryRequest(s, "/api/messaging/outbox", "", body)
		if r.Code != 202 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	r := deliveryRequest(s, "/api/messaging/outbox", "", strings.Replace(body, "hello", "changed", 1))
	if r.Code != 409 {
		t.Fatal("changed key content", r.Code, r.Body.String())
	}
	r = request(s, "GET", "/api/messaging/outbox/http-note", "", false)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"status":"pending"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	for header, value := range map[string]string{"X-Mitos-Server": "other", "Origin": "https://foreign.example"} {
		req := httptest.NewRequest("POST", "http://localhost/api/messaging/outbox", strings.NewReader(body))
		req.Header.Set(header, value)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 409 && w.Code != 403 {
			t.Fatal("unscoped mutation", header, w.Code)
		}
	}
	r = deliveryRequest(s, "/api/messaging/outbox", "", strings.Repeat(" ", 100<<10))
	if r.Code != 413 {
		t.Fatal("unbounded request", r.Code)
	}
}
