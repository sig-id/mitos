package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"

	"mitos/internal/failpoint"
)

type ArtifactData struct {
	DisplayName string
	Role        string
	Name        string
	Data        []byte
}

func (s *Store) SaveArtifact(id, name string, data []byte) error {
	return s.SaveArtifacts(id, ArtifactData{Name: name, Data: data})
}

// SaveArtifacts persists a harvest together. Files are all written before any
// are synced, shared directories are synced once, and the individual artifact
// events share one log flush. Readers see artifacts only after that flush.
func (s *Store) SaveArtifacts(id string, artifacts ...ArtifactData) error {
	return s.saveArtifacts(id, false, artifacts...)
}

// replaceUncommitted is reserved for results reproducible from durable state.
// Committed names are still immutable; only files without an artifact record
// may be discarded and rewritten after an interrupted save.
func (s *Store) saveArtifacts(id string, replaceUncommitted bool, artifacts ...ArtifactData) error {
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
	if len(r.Artifacts)+len(artifacts) > handoffArtifactLimit {
		return handoffError("storage_file_limit", 0)
	}
	var evidenceBytes int64
	for _, a := range r.Artifacts {
		if a.Role != "" {
			evidenceBytes += a.Bytes
		}
	}
	for _, a := range artifacts {
		if a.Role != "" {
			evidenceBytes += int64(len(a.Data))
		}
	}
	if evidenceBytes > 128<<20 {
		return handoffError("storage_byte_limit", 0)
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
		c := change{Artifact: &Artifact{Name: a.Name, DisplayName: a.DisplayName, Role: a.Role, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(a.Data))}}
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
	err := s.mu.io(func() error {
		if err := s.artifacts.Put(id, artifacts, replaceUncommitted); err != nil {
			return err
		}
		failpoint.Hit("artifacts.after_put")
		return nil
	})
	if err != nil {
		return err
	}
	return s.persist(events...)
}
