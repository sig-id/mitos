package studio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	workspacefs "mitos/internal/workspace"
)

// resolveResultFile binds a final-answer file item to the immutable evidence of
// one exact run. Registered artifacts are authoritative; changed and unchanged
// workspace files resolve through the checksum-verified retained archive.
// Caller holds the store lock.
func (s *Store) resolveResultFile(run, path string) (string, int64, error) {
	r := s.runs[run]
	if r == nil {
		return "", 0, ErrNotFound
	}
	for _, a := range r.Artifacts {
		if a.Name != path || a.Bytes < 0 || len(a.SHA256) != 64 {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, "artifacts", run, path))
		if err != nil {
			return "", 0, err
		}
		sum := sha256.Sum256(b)
		if int64(len(b)) != a.Bytes || hex.EncodeToString(sum[:]) != a.SHA256 {
			return "", 0, errors.New("saved artifact does not match its registration")
		}
		return a.SHA256, a.Bytes, nil
	}
	expected, deleted := "", false
	if raw, err := os.ReadFile(filepath.Join(s.dir, "artifacts", run, "changes.json")); err == nil && len(raw) <= graphMaxBytes {
		var manifest workspacefs.Harvest
		if json.Unmarshal(raw, &manifest) == nil {
			for _, c := range manifest.Changes {
				if c.Path == path {
					expected, deleted = c.AfterSHA256, len(c.AfterSHA256) != 64
				}
			}
		}
	}
	if deleted {
		return "", 0, errors.New("file was deleted by the accepted run")
	}
	archive, err := os.ReadFile(filepath.Join(s.dir, "artifacts", run, "workspace.tar.gz"))
	if err != nil {
		return "", 0, errors.New("no retained workspace archive")
	}
	zipped, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", 0, errors.New("retained workspace archive is unreadable")
	}
	defer zipped.Close()
	reader := tar.NewReader(io.LimitReader(zipped, workspacefs.DefaultLimits.Bytes+(20<<20)))
	for count := 0; count < workspacefs.DefaultLimits.Files; count++ {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, errors.New("retained workspace archive is unreadable")
		}
		if header.Name != path {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > workspacefs.DefaultLimits.Bytes {
			return "", 0, errors.New("file is not a bounded regular file")
		}
		body, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil || int64(len(body)) != header.Size {
			return "", 0, errors.New("saved file does not match its archive entry")
		}
		sum := sha256.Sum256(body)
		digest := hex.EncodeToString(sum[:])
		if expected != "" && digest != expected {
			return "", 0, errors.New("saved file does not match its manifest")
		}
		return digest, header.Size, nil
	}
	return "", 0, ErrNotFound
}

// GraphRunFiles exposes changed files from the immutable harvested result, never
// paths in the live project or a mutable worker directory.
func (s *Store) GraphRunFiles(graph, task, run string) ([]workspacefs.Change, error) {
	if _, err := s.GraphRun(graph, task, run); err != nil {
		return nil, err
	}
	raw, err := s.Artifact(run, "changes.json")
	if err != nil {
		return nil, err
	}
	if len(raw) > graphMaxBytes {
		return nil, errors.New("file manifest exceeds preview limit")
	}
	var manifest workspacefs.Harvest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	files := []workspacefs.Change{}
	for _, c := range manifest.Changes {
		if fs.ValidPath(c.Path) && c.Path != "." && len(c.AfterSHA256) == 64 {
			files = append(files, c)
		}
	}
	return files, nil
}
func (s *Store) GraphRunFile(graph, task, run, path string) ([]byte, error) {
	if !fs.ValidPath(path) || path == "." {
		return nil, ErrNotFound
	}
	files, err := s.GraphRunFiles(graph, task, run)
	if err != nil {
		return nil, err
	}
	digest := ""
	for _, f := range files {
		if f.Path == path {
			digest = f.AfterSHA256
			break
		}
	}
	// Unchanged source files are also readable from the checksum-verified archive.
	archive, err := s.Artifact(run, "workspace.tar.gz")
	if err != nil {
		return nil, err
	}
	zipped, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	defer zipped.Close()
	reader := tar.NewReader(io.LimitReader(zipped, workspacefs.DefaultLimits.Bytes+(20<<20)))
	for count := 0; count < workspacefs.DefaultLimits.Files; count++ {
		header, err := reader.Next()
		if err == io.EOF {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if header.Name != path {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > workspacefs.DefaultLimits.Bytes {
			return nil, errors.New("result is not a bounded regular file")
		}
		body, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		if int64(len(body)) != header.Size || (digest != "" && hex.EncodeToString(sum[:]) != digest) {
			return nil, errors.New("saved file does not match its manifest")
		}
		return body, nil
	}
	return nil, errors.New("workspace file limit exceeded")
}

// GraphRunWorkspaceFiles lists regular files from the retained archive so source
// citations can resolve without granting access to the live filesystem.
func (s *Store) GraphRunWorkspaceFiles(graph, task, run string) ([]workspacefs.Change, error) {
	if _, err := s.GraphRun(graph, task, run); err != nil {
		return nil, err
	}
	data, err := s.Artifact(run, "workspace.tar.gz")
	if err != nil {
		return nil, err
	}
	zipped, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zipped.Close()
	reader := tar.NewReader(io.LimitReader(zipped, workspacefs.DefaultLimits.Bytes+(20<<20)))
	files := []workspacefs.Change{}
	for count := 0; count < workspacefs.DefaultLimits.Files; count++ {
		h, err := reader.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && fs.ValidPath(h.Name) && h.Name != "." && h.Size >= 0 && h.Size <= workspacefs.DefaultLimits.Bytes {
			files = append(files, workspacefs.Change{Path: h.Name, Kind: "snapshot"})
		}
	}
	if _, err = reader.Next(); err == io.EOF {
		return files, nil
	}
	return nil, errors.New("workspace file limit exceeded")
}
