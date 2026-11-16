package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

func artifactID(run string, a Artifact) string {
	return "artifact_" + graphDigest([]string{run, a.Name, a.SHA256})
}

func artifactDescriptor(r *Run, a Artifact) ArtifactDescriptor {
	name, role := a.DisplayName, a.Role
	if name == "" {
		name = a.Name
	}
	if role == "" {
		role = "file"
	}
	media := mime.TypeByExtension(filepath.Ext(name))
	if media == "" {
		media = "application/octet-stream"
	}
	d := ArtifactDescriptor{ID: artifactID(r.ID, a), RunID: r.ID, TaskID: r.TaskID, Name: name, Role: role, MediaType: media, Bytes: a.Bytes, SHA256: a.SHA256, Availability: "retained", Retention: "until_store_deleted"}
	if r.Result != nil {
		d.ResultID = r.Result.ID
	}
	return d
}

// Open the retained object, never a producer's scratch directory. Hash from the
// same descriptor used for the range read; reject symlinks, type and size changes.
func (s *Store) verifiedArtifact(r *Run, a Artifact) (*os.File, error) {
	root, err := os.OpenRoot(filepath.Join(s.dir, "artifacts"))
	if err != nil {
		return nil, handoffError("unavailable", 0)
	}
	defer root.Close()
	info, err := root.Lstat(r.ID)
	if err != nil || !info.IsDir() {
		return nil, handoffError("unsafe_path", 0)
	}
	f, err := root.OpenFile(r.ID+"/"+a.Name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, handoffError("unavailable", 0)
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() != a.Bytes {
		return nil, handoffError("integrity_failure", 0)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, a.Bytes+1))
	if err != nil || n != a.Bytes || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return nil, handoffError("integrity_failure", 0)
	}
	if _, err = f.Seek(0, 0); err != nil {
		return nil, err
	}
	ok = true
	return f, nil
}

// Caller holds the store lock. Review grants are frozen on the consumer attempt;
// worker grants come from selected dependency witnesses, never latest-run lookup.
func (s *Store) handoffFullProducer(consumer, producer string) (*Run, error) {
	c, p := s.runs[consumer], s.runs[producer]
	if c == nil || p == nil || c.ProjectID != p.ProjectID {
		return nil, handoffError("forbidden", 0)
	}
	allowed := consumer == producer || slices.Contains(c.ReviewRuns, producer)
	if t := s.tasks[c.TaskID]; t != nil && t.CurrentRunID == c.ID {
		for _, w := range t.DependencyEvidence {
			if w.RunID == producer && w.InputOmitted == "" {
				for _, dep := range t.Scheduling.Deps {
					if dep.From == w.TaskID && dep.Payload == "data" {
						allowed = true
					}
				}
			}
		}
	}
	if !allowed {
		return nil, handoffError("forbidden", 0)
	}
	return p, nil
}

func (s *Store) listArtifacts(consumer string, in ArtifactList) (ArtifactPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, err := s.handoffProducer(consumer, in.RunID)
	if err != nil {
		return ArtifactPage{}, err
	}
	visible := []Artifact{}
	for _, a := range r.Artifacts {
		if s.handoffArtifactAllowed(consumer, r, a) {
			visible = append(visible, a)
		}
	}
	if in.Limit < 1 || in.Limit > 32 || in.Cursor < 0 || in.Cursor > len(visible) {
		return ArtifactPage{}, handoffError("invalid_page", int64(in.Cursor))
	}
	page := ArtifactPage{Artifacts: []ArtifactDescriptor{}, RetrievalBytesUsed: s.runs[consumer].HandoffBytes, RetrievalBytesLimit: HandoffRetrievalBudget}
	end := in.Cursor
	for end < len(visible) && end-in.Cursor < in.Limit {
		a := visible[end]
		d := artifactDescriptor(r, a)
		if f, err := s.verifiedArtifact(r, a); err != nil {
			d.Availability = "unavailable"
			var failure *HandoffFailure
			if errors.As(err, &failure) {
				d.Availability = failure.Code
			}
		} else {
			f.Close()
		}
		page.Artifacts = append(page.Artifacts, d)
		body, _ := json.Marshal(page)
		if len(body) > 48<<10 {
			page.Artifacts = page.Artifacts[:len(page.Artifacts)-1]
			break
		}
		end++
	}
	if end < len(visible) {
		page.Next = &end
	}
	return page, nil
}

func (s *Store) artifactRange(consumer string, in ArtifactRead, search *ArtifactSearch) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operator := consumer == "operator:"+in.RunID
	principal := consumer
	if operator {
		principal = in.RunID
	}
	r, err := s.handoffProducer(principal, in.RunID)
	if err != nil {
		return nil, err
	}
	if in.Offset < 0 || in.Limit < 1 || in.Limit > HandoffReadLimit {
		return nil, handoffError("invalid_range", in.Offset)
	}
	var found *Artifact
	for _, a := range r.Artifacts {
		if artifactID(r.ID, a) == in.ArtifactID {
			copy := a
			found = &copy
			break
		}
	}
	if found != nil && !s.handoffArtifactAllowed(principal, r, *found) {
		return nil, handoffError("forbidden", in.Offset)
	}
	if found == nil {
		return nil, handoffError("artifact_missing", in.Offset)
	}
	if in.Offset > found.Bytes {
		return nil, handoffError("invalid_range", in.Offset)
	}
	c := s.runs[consumer]
	// Charge bytes processed and returned, even repeated reads; no claim of exact token accounting.
	charge := int64(in.Limit)
	if search != nil {
		charge += int64(len(search.Query))
	}
	charge = min(charge, found.Bytes-in.Offset)
	if !operator && c.HandoffBytes+charge > HandoffRetrievalBudget {
		return nil, handoffError("retrieval_budget_exhausted", in.Offset)
	}
	f, err := s.verifiedArtifact(r, *found)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, charge)
	if _, err = f.ReadAt(data, in.Offset); err != nil && err != io.EOF {
		return nil, err
	}
	if !operator {
		if err = s.commit(consumer, "effect", "artifact_retrieved", change{HandoffBytes: c.HandoffBytes + charge}); err != nil {
			return nil, err
		}
	}
	d := artifactDescriptor(r, *found)
	next := in.Offset + int64(len(data))
	if search != nil {
		end := min(in.Offset+int64(in.Limit), found.Bytes)
		out := ArtifactMatches{Artifact: d, Offsets: []int64{}, RetrievalBytesUsed: s.runs[principal].HandoffBytes, RetrievalBytesLimit: HandoffRetrievalBudget}
		for pos := 0; pos < len(data); {
			i := bytes.Index(data[pos:], []byte(search.Query))
			if i < 0 {
				break
			}
			pos += i
			at := in.Offset + int64(pos)
			if at >= end {
				break
			}
			out.Offsets = append(out.Offsets, at)
			pos++
			if len(out.Offsets) == search.Limit {
				end = at + 1
				break
			}
		}
		out.Complete = end >= found.Bytes
		if !out.Complete {
			out.Next = &end
		}
		return out, nil
	}
	out := ArtifactRange{Artifact: d, Offset: in.Offset, Data: data, Complete: next == found.Bytes, RetrievalBytesUsed: s.runs[principal].HandoffBytes, RetrievalBytesLimit: HandoffRetrievalBudget}
	if !out.Complete {
		out.Next = &next
	}
	return out, nil
}

func (s *Store) registerArtifact(run, workspace string, in ArtifactRegister) (ArtifactDescriptor, error) {
	if !validArtifactRole(in.Role) || !fs.ValidPath(in.Path) || in.Path == "." || len(in.Path) > 512 {
		return ArtifactDescriptor{}, handoffError("invalid_artifact", 0)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return ArtifactDescriptor{}, err
	}
	defer root.Close()
	parts := strings.Split(in.Path, "/")
	for i := range parts {
		st, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return ArtifactDescriptor{}, handoffError("unsafe_path", 0)
		}
	}
	f, err := root.OpenFile(in.Path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ArtifactDescriptor{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > dependencyBytes {
		return ArtifactDescriptor{}, handoffError("storage_limit", 0)
	}
	data, err := io.ReadAll(io.LimitReader(f, dependencyBytes+1))
	if err != nil || len(data) > dependencyBytes {
		return ArtifactDescriptor{}, handoffError("storage_limit", 0)
	}
	sum := sha256.Sum256(data)
	name := "evidence-" + graphDigest([]string{in.Path, in.Role, hex.EncodeToString(sum[:])}) + ".blob"
	// Saving is idempotent even if a response was lost. Immutable names include role/path/content.
	s.mu.RLock()
	r := s.runs[run]
	if r == nil {
		s.mu.RUnlock()
		return ArtifactDescriptor{}, ErrNotFound
	}
	copy := graphClone(*r)
	s.mu.RUnlock()
	for _, a := range copy.Artifacts {
		if a.Name == name {
			return artifactDescriptor(&copy, a), nil
		}
	}
	if err = s.saveArtifacts(run, true, ArtifactData{Name: name, DisplayName: in.Path, Role: in.Role, Data: data}); err != nil {
		// A concurrent identical registration may have won the journal race.
		latest, _ := s.Get(run)
		for _, a := range latest.Artifacts {
			if a.Name == name {
				return artifactDescriptor(&latest, a), nil
			}
		}
		return ArtifactDescriptor{}, err
	}
	rcopy, err := s.Get(run)
	if err != nil {
		return ArtifactDescriptor{}, err
	}
	for _, a := range rcopy.Artifacts {
		if a.Name == name {
			return artifactDescriptor(&rcopy, a), nil
		}
	}
	return ArtifactDescriptor{}, ErrNotFound
}

func (s *Store) handoffArtifactAllowed(consumer string, producer *Run, a Artifact) bool {
	if s.cooperationArtifactGrant(consumer, producer.ID, artifactID(producer.ID, a)) {
		return true
	}
	if consumer == producer.ID {
		return true
	}
	c := s.runs[consumer]
	if c == nil {
		return false
	}
	if slices.Contains(c.ReviewRuns, producer.ID) {
		return true
	}
	t := s.tasks[c.TaskID]
	if t == nil || t.CurrentRunID != consumer {
		return false
	}
	for _, w := range t.DependencyEvidence {
		if w.RunID != producer.ID || w.InputOmitted != "" {
			continue
		}
		data := false
		for _, dep := range t.Scheduling.Deps {
			if dep.From == w.TaskID && dep.Payload == "data" {
				data = true
			}
		}
		if !data {
			continue
		}
		for _, bound := range w.Artifacts {
			if bound.Name == a.Name && bound.SHA256 == a.SHA256 {
				return true
			}
		}
		if producer.Result != nil {
			for _, ref := range producer.Result.Submission.Artifacts {
				if ref.ArtifactID == artifactID(producer.ID, a) {
					return true
				}
			}
		}
	}
	return false
}

// A request is an explicit grant of its cited artifacts to named resolver
// attempts, not access to the producer's entire result or workspace archive.
func (s *Store) cooperationArtifactGrant(consumer, producer, id string) bool {
	c, p := s.runs[consumer], s.runs[producer]
	if c == nil || p == nil || c.ProjectID != p.ProjectID {
		return false
	}
	t := s.tasks[c.TaskID]
	if t == nil || t.CurrentRunID != consumer {
		return false
	}
	for _, rec := range s.cooperation {
		if rec.ProjectID != p.ProjectID || !rec.Message.Request() || len(rec.Message.Artifacts) == 0 {
			continue
		}
		if id != "" && !slices.Contains(rec.Message.Artifacts, id) {
			continue
		}
		if id == "" {
			shared := false
			for _, a := range p.Artifacts {
				if slices.Contains(rec.Message.Artifacts, artifactID(p.ID, a)) {
					shared = true
					break
				}
			}
			if !shared {
				continue
			}
		}
		state := s.cooperationState(rec, time.Now().UTC())
		if state != "pending" && state != "answered" {
			continue
		}
		for _, resolver := range rec.Message.PermittedResolvers {
			if resolver.RunID == consumer && resolver.TaskID == c.TaskID && resolver.Revision == t.Revision {
				return true
			}
		}
	}
	return false
}

func (s *Store) handoffProducer(consumer, producer string) (*Run, error) {
	if r, err := s.handoffFullProducer(consumer, producer); err == nil {
		return r, nil
	}
	if s.cooperationArtifactGrant(consumer, producer, "") {
		return s.runs[producer], nil
	}
	return nil, handoffError("forbidden", 0)
}
