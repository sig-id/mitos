package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mitos/internal/studio"
)

func TestLocalDeviceAdministration(t *testing.T) {
	server, engine := testServer(t)
	in := studio.DevicePairInput{Name: "Handheld", Permissions: studio.DevicePermissions{Read: true, Resolve: true, Voice: true}, ExpiresAt: time.Now().Add(time.Hour), STTDisclosure: "Local STT; no retention"}
	encoded, _ := json.Marshal(in)
	send := func(path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		return w
	}
	paired := send("/api/devices", encoded)
	var result struct {
		Device studio.Device `json:"device"`
		Token  string        `json:"token"`
	}
	if paired.Code != 201 || json.Unmarshal(paired.Body.Bytes(), &result) != nil || result.Token == "" || result.Device.Operator != "local-operator" {
		t.Fatal(paired.Code, paired.Body.String())
	}
	if result.Device.TokenHash != "" || paired.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response exposed caching/hash")
	}
	listed := request(server, "GET", "/api/devices", "", false)
	if listed.Code != 200 || strings.Contains(listed.Body.String(), result.Token) || strings.Contains(listed.Body.String(), "token_hash") {
		t.Fatal(listed.Code, listed.Body.String())
	}
	for _, bad := range []string{strings.Replace(string(encoded), `"read":true`, `"read":true,"read":true`, 1), strings.Replace(string(encoded), `"name":"Handheld"`, `"name":"Handheld","operator":"forged"`, 1)} {
		if got := send("/api/devices", []byte(bad)); got.Code != 400 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	in.STTDisclosure = ""
	bad, _ := json.Marshal(in)
	if got := send("/api/devices", bad); got.Code != 400 {
		t.Fatal(got.Code, got.Body.String())
	}
	expires, _ := json.Marshal(map[string]any{"expires_at": time.Now().Add(2 * time.Hour)})
	rotated := send("/api/devices/"+result.Device.ID+"/rotate", expires)
	var next struct {
		Token string `json:"token"`
	}
	if rotated.Code != 200 || json.Unmarshal(rotated.Body.Bytes(), &next) != nil || next.Token == "" || next.Token == result.Token {
		t.Fatal(rotated.Code, rotated.Body.String())
	}
	if _, err := engine.Store.AuthenticateDevice(result.Token, "read"); err == nil {
		t.Fatal("rotation left old token valid")
	}
	revoked := send("/api/devices/"+result.Device.ID+"/revoke", nil)
	if revoked.Code != 200 {
		t.Fatal(revoked.Code, revoked.Body.String())
	}
	if _, err := engine.Store.AuthenticateDevice(next.Token, "read"); err == nil {
		t.Fatal("revoked token works")
	}
	if got := send("/api/devices/missing/revoke", nil); got.Code != 404 {
		t.Fatal(got.Code, got.Body.String())
	}
	req := httptest.NewRequest("POST", "http://localhost/api/devices", strings.NewReader(string(encoded)))
	req.Header.Set("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross origin pairing", w.Code)
	}
}
