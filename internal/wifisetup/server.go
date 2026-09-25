// Package wifisetup serves a static, client-only acoustic provisioning app.
// It has no credential API, Studio access or server-side secret handling.
package wifisetup

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed assets/*
var assets embed.FS
var files, _ = fs.Sub(assets, "assets")
var handler = http.FileServerFS(files)

func ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "Use a fragment for setup data", 400)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/wifi-setup/")
	switch name {
	case "", "index.html", "style.css", "app.mjs", "modem.mjs", "sw.js", "manifest.webmanifest", "icon.svg":
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Del("Content-Type")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; worker-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	http.StripPrefix("/wifi-setup/", handler).ServeHTTP(w, r)
}
