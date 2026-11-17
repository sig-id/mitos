// Package apiversion defines the negotiated version of the Studio /api surface.
// Every /api request names the version it speaks; servers refuse versions they
// do not implement instead of guessing.
package apiversion

import (
	"net/http"
	"slices"
	"strings"
)

const (
	// Header carries the API version on ordinary HTTP requests.
	Header = "Mitos-API-Version"
	// Current is the version this build speaks as a client.
	Current = "1"
	// WebSocketProtocol carries the version on WebSocket upgrades, where
	// browsers cannot set custom headers.
	WebSocketProtocol = "mitos-api.v1"
)

// Supported lists every version this server implements.
var Supported = []string{Current}

// Set marks an outgoing request with the current version.
func Set(r *http.Request) { r.Header.Set(Header, Current) }

// Requested returns the version a request asked for, and whether it was stated.
func Requested(r *http.Request) (string, bool) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		for _, field := range r.Header.Values("Sec-WebSocket-Protocol") {
			for _, p := range strings.Split(field, ",") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(p), "mitos-api.v"); ok {
					return v, true
				}
			}
		}
		return "", false
	}
	v := strings.TrimSpace(r.Header.Get(Header))
	return v, v != ""
}

func IsSupported(v string) bool { return slices.Contains(Supported, v) }
