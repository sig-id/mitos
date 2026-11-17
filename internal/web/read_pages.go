package web

import (
	"net/http"
	"strconv"

	"mitos/internal/studio"
)

func readPageParameters(r *http.Request) (int64, int, error) {
	after := int64(0)
	limit := studio.ReadDefaultLimit
	var err error
	if raw := r.URL.Query().Get("after"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, 0, studio.ErrInvalidPage
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, studio.ErrInvalidPage
		}
	}
	if after < 0 || limit < 1 || limit > studio.ReadMaxLimit {
		return 0, 0, studio.ErrInvalidPage
	}
	return after, limit, nil
}
func writePageHeaders(w http.ResponseWriter, next int64, id string, more bool) {
	w.Header().Set("Mitos-Next-After", strconv.FormatInt(next, 10))
	w.Header().Set("Mitos-Has-More", strconv.FormatBool(more))
	if id != "" {
		w.Header().Set("Mitos-Next-Run", id)
	}
}
