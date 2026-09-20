package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"mitos/internal/decision"
	"mitos/internal/device"
	"mitos/internal/studio"
)

func (s *Server) deviceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.engine.Store.Devices(decision.LocalOperator()))
	})
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "malformed"})
			return
		}
		var in studio.DevicePairInput
		if device.DecodeObject(data, &in, []string{"name", "permissions", "expires_at", "stt_disclosure"}) != nil {
			writeJSON(w, 400, map[string]string{"error": "malformed"})
			return
		}
		// The permissions object is strict too, including duplicate keys.
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(data, &raw)
		if device.DecodeObject(raw["permissions"], &in.Permissions, []string{"read", "resolve", "voice"}) != nil {
			writeJSON(w, 400, map[string]string{"error": "malformed"})
			return
		}
		d, token, err := s.engine.Store.PairDevice(in, decision.LocalOperator())
		if err != nil {
			s.deviceAdminError(w, r, err)
			return
		}
		writeJSON(w, 201, map[string]any{"device": d, "token": token})
	})
	mux.HandleFunc("POST /api/devices/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		d, _, err := s.engine.Store.ChangeDevice(r.PathValue("id"), false, time.Time{}, decision.LocalOperator())
		if err != nil {
			s.deviceAdminError(w, r, err)
			return
		}
		writeJSON(w, 200, d)
	})
	mux.HandleFunc("POST /api/devices/{id}/rotate", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "malformed"})
			return
		}
		var in struct {
			ExpiresAt time.Time `json:"expires_at"`
		}
		if device.DecodeObject(data, &in, []string{"expires_at"}) != nil {
			writeJSON(w, 400, map[string]string{"error": "malformed"})
			return
		}
		d, token, err := s.engine.Store.ChangeDevice(r.PathValue("id"), true, in.ExpiresAt, decision.LocalOperator())
		if err != nil {
			s.deviceAdminError(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"device": d, "token": token})
	})
}

func (s *Server) deviceAdminError(w http.ResponseWriter, r *http.Request, err error) {
	var e *studio.DeviceError
	if errors.As(err, &e) {
		writeJSON(w, e.Status, map[string]string{"error": e.Code})
		return
	}
	s.fail(w, r, err)
}
