package web

import (
	"net/http"
	"time"
)

// Operator-only loopback routes. Never install these on the application listener.
func (s *Server) storageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/storage/backup", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Directory string `json:"directory"`
		}
		if err := deskJSON(r, &body); err != nil || body.Directory == "" {
			writeJSON(w, 400, map[string]string{"error": "send a backup directory"})
			return
		}
		if err := s.engine.Store.Backup(body.Directory); err != nil {
			writeJSON(w, 422, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 201, map[string]string{"directory": body.Directory, "profile": "mitos-runtime-backup/v1"})
	})
	mux.HandleFunc("POST /api/storage/expire-receipts", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Before time.Time `json:"before"`
		}
		if err := deskJSON(r, &body); err != nil || body.Before.IsZero() || body.Before.After(time.Now()) {
			writeJSON(w, 400, map[string]string{"error": "send a past receipt cutoff"})
			return
		}
		count, err := s.engine.Store.ExpireStructuredKeys(body.Before)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]int{"expired": count})
	})
}
