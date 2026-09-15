package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type ArtifactData struct {
	Name string
	Data []byte
}

func (s *Store) SaveArtifact(id, name string, data []byte) error {
	return s.SaveArtifacts(id, ArtifactData{Name: name, Data: data})
}

// SaveArtifacts persists a harvest together. Files are all written before any
// are synced, shared directories are synced once, and the individual artifact
// events share one log flush. Readers see artifacts only after that flush.
func (s *Store) SaveArtifacts(id string, artifacts ...ArtifactData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	if s.writeErr != nil {
		return s.writeErr
	}
	names := map[string]bool{}
	for _, a := range r.Artifacts {
		names[a.Name] = true
	}
	events := make([]Event, 0, len(artifacts))
	seq, previous := s.seq, s.hash
	for _, a := range artifacts {
		if filepath.Base(a.Name) != a.Name || a.Name == "." || a.Name == ".." || a.Name == "" {
			return errors.New("invalid artifact name")
		}
		if names[a.Name] {
			return errors.New("artifact already exists")
		}
		names[a.Name] = true
		sum := sha256.Sum256(a.Data)
		c := change{Artifact: &Artifact{Name: a.Name, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(a.Data))}}
		// Artifact records do not change scheduling or lifecycle state and can
		// therefore be prepared together against the same durable state.
		e, err := s.prepareEvent(id, "effect", "artifact_harvested", c, seq+1, previous)
		if err != nil {
			return err
		}
		events = append(events, e)
		seq, previous = e.Seq, e.Hash
	}
	if len(events) == 0 {
		return nil
	}
	root := filepath.Join(s.dir, "artifacts")
	dir := filepath.Join(root, id)
	dirs := []string{dir}
	if !s.artifactDirs[dir] {
		dirs = append(dirs, root)
	}
	if !s.artifactDirs[root] {
		dirs = append(dirs, s.dir)
	}
	err := s.mu.io(func() error {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		files := make([]*os.File, 0, len(artifacts))
		defer func() {
			for _, f := range files {
				_ = f.Close()
			}
		}()
		for _, a := range artifacts {
			f, err := os.OpenFile(filepath.Join(dir, a.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			files = append(files, f)
			n, err := f.Write(a.Data)
			if err != nil {
				return err
			}
			if n != len(a.Data) {
				return io.ErrShortWrite
			}
		}
		for _, f := range files {
			if err := f.Sync(); err != nil {
				return err
			}
		}
		for len(files) > 0 {
			f := files[0]
			files = files[1:]
			if err := f.Close(); err != nil {
				return err
			}
		}
		// Persist names and new parents before the log can promise availability.
		for _, path := range dirs {
			if err := syncDirectory(path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if s.artifactDirs == nil {
		s.artifactDirs = map[string]bool{}
	}
	s.artifactDirs[root], s.artifactDirs[dir] = true, true
	return s.persist(events...)
}
