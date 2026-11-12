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

	workspacefs "mitos/internal/workspace"
)

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
	if digest == "" {
		return nil, ErrNotFound
	}
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
		if int64(len(body)) != header.Size || hex.EncodeToString(sum[:]) != digest {
			return nil, errors.New("saved file does not match its manifest")
		}
		return body, nil
	}
	return nil, errors.New("workspace file limit exceeded")
}
