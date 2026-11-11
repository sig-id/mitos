package web

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"mitos/internal/messaging"
	"mitos/internal/studio"
)

func (s *Server) messagingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/messaging", func(w http.ResponseWriter, r *http.Request) {
		if s.engine.Messages == nil {
			writeJSON(w, 200, messaging.Status{Server: s.engine.Store.Home().ID})
			return
		}
		writeJSON(w, 200, s.engine.Messages.Status())
	})
	mux.HandleFunc("POST /api/messaging/outbox", func(w http.ResponseWriter, r *http.Request) {
		if s.engine.Messages == nil {
			writeJSON(w, 503, map[string]string{"error": "MQTT is disabled; configure --mqtt-config"})
			return
		}
		var in messaging.Request
		if !messageBody(w, r, &in) {
			return
		}
		out, err := s.engine.Messages.Send(in)
		if err != nil {
			messageError(w, err)
			return
		}
		writeJSON(w, 202, out)
	})
	mux.HandleFunc("GET /api/messaging/outbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.Store.OutboundMessage(r.PathValue("id"))
		if err != nil {
			messageError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/messaging/inbox", func(w http.ResponseWriter, r *http.Request) {
		project, box := r.URL.Query().Get("project"), r.URL.Query().Get("mailbox")
		after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		if r.URL.Query().Get("after") == "" {
			err = nil
		}
		if !messaging.ValidID(project) || !messaging.ValidID(box) || err != nil || after < 0 {
			writeJSON(w, 400, map[string]string{"error": "provide project, mailbox and a nonnegative after cursor"})
			return
		}
		writeJSON(w, 200, s.engine.Store.MessageInbox(project, box, after))
	})
	mux.HandleFunc("POST /api/messaging/inbox/{sender}/{id}/ack", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Generation int `json:"generation"`
		}
		if !messageBody(w, r, &in) {
			return
		}
		err := s.engine.Store.AcknowledgeMessage(r.PathValue("sender"), r.PathValue("id"), in.Generation)
		if err != nil {
			messageError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"acknowledged": true})
	})
}
func messageBody(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, 413, map[string]string{"error": "message request too large"})
		return false
	}
	if err = messaging.Decode(b, v); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid message request"})
		return false
	}
	return true
}
func messageError(w http.ResponseWriter, err error) {
	code := 400
	if errors.Is(err, studio.ErrNotFound) {
		code = 404
	}
	if errors.Is(err, studio.ErrConflict) || errors.Is(err, messaging.ErrConflict) {
		code = 409
	}
	if errors.Is(err, messaging.ErrCapacity) {
		code = 429
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
