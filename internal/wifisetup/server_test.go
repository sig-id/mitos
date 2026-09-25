package wifisetup

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticBoundary(t *testing.T) {
	for _, test := range []struct {
		method, path string
		status       int
	}{{"GET", "/wifi-setup/", 200}, {"GET", "/wifi-setup/modem.mjs", 200}, {"GET", "/wifi-setup/sw.js", 200}, {"POST", "/wifi-setup/", 405}, {"GET", "/wifi-setup/?password=secret", 400}, {"GET", "/wifi-setup/../api/devices", 404}, {"GET", "/wifi-setup/missing", 404}} {
		r := httptest.NewRequest(test.method, test.path, nil)
		w := httptest.NewRecorder()
		w.Header().Set("Content-Type", "application/json")
		ServeHTTP(w, r)
		if w.Code != test.status {
			t.Errorf("%s %s: %d", test.method, test.path, w.Code)
		}
		if strings.Contains(w.Body.String(), "password=secret") {
			t.Fatal("request reflected")
		}
		if test.status == 200 {
			if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("missing browser boundary")
			}
			if w.Header().Get("Content-Type") == "application/json" {
				t.Fatal("inherited API content type")
			}
		}
	}
}
