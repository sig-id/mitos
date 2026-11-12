package web

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type directoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Browse the same server-side directories that interactive project selection
// permits. This endpoint returns folder names only, never file contents.
func (s *Server) directories(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = s.engine.Root
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
			if r.URL.Query().Get("path") == "~" {
				path = home
			}
		}
	}
	path, err := s.engine.Desk.ResolveDirectory(path)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "This folder is unavailable. Check the path and access permissions."})
		return
	}
	folder, err := os.Open(path)
	if err != nil {
		writeJSON(w, 403, map[string]string{"error": "This folder cannot be read by the server."})
		return
	}
	defer folder.Close()
	entries, err := folder.ReadDir(4096)
	if err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, 403, map[string]string{"error": "This folder cannot be read by the server."})
		return
	}
	directories := []directoryEntry{}
	hidden := r.URL.Query().Get("hidden") == "true"
	for _, entry := range entries {
		if !hidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := os.Stat(filepath.Join(path, entry.Name()))
			isDir = err == nil && info.IsDir()
		}
		if isDir {
			directories = append(directories, directoryEntry{Name: entry.Name(), Path: filepath.Join(path, entry.Name())})
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.ToLower(directories[i].Name) < strings.ToLower(directories[j].Name)
	})
	home, _ := os.UserHomeDir()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"path": path, "name": filepath.Base(path), "parent": filepath.Dir(path), "home": home, "workspace": s.engine.Root, "directories": directories, "truncated": len(entries) == 4096})
}
