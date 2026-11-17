package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mitos/agents"
)

type AgentEdit struct {
	Key          string `json:"key"`
	ID           string `json:"id"`
	Revision     int    `json:"revision"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}
type libraryReceipt struct {
	Digest     string            `json:"digest"`
	Definition agents.Definition `json:"definition"`
}

const libraryProfile = "agent-library-store/v1"

type savedLibrary struct {
	Profile   string                         `json:"profile"`
	Revisions map[string][]agents.Definition `json:"revisions"`
	Receipts  map[string]libraryReceipt      `json:"receipts"`
}

func emptySavedLibrary() savedLibrary {
	return savedLibrary{Profile: libraryProfile, Revisions: map[string][]agents.Definition{}, Receipts: map[string]libraryReceipt{}}
}

func (e *Engine) readLibrary() (savedLibrary, error) {
	// Immutable snapshots may be read while an orchestrator holds Store.mu.
	// Never acquire the writer/state lock recursively or invert the edit lock.
	saved := e.Store.agentLibrary.Load()
	if saved == nil {
		return emptySavedLibrary(), nil
	}
	return graphClone(*saved), nil
}

func validateSavedLibrary(saved savedLibrary) error {
	if saved.Revisions == nil {
		saved.Revisions = map[string][]agents.Definition{}
	}
	if saved.Receipts == nil {
		saved.Receipts = map[string]libraryReceipt{}
	}
	if saved.Profile != libraryProfile {
		return fmt.Errorf("agent library %s is not %s; move it aside to start from the bundled library", "agent-library.json", libraryProfile)
	}
	for _, revisions := range saved.Revisions {
		for _, d := range revisions {
			if err := d.Verify(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Only the one-time runtime import reads the legacy library snapshot.
func readLegacyLibrary(dir string) (*savedLibrary, error) {
	f, err := os.Open(filepath.Join(dir, "agent-library.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 8<<20 {
		return nil, errors.New("agent library exceeds 8 MiB")
	}
	var saved savedLibrary
	if err = json.Unmarshal(raw, &saved); err != nil {
		return nil, err
	}
	if err = validateSavedLibrary(saved); err != nil {
		return nil, err
	}
	if saved.Revisions == nil {
		saved.Revisions = map[string][]agents.Definition{}
	}
	if saved.Receipts == nil {
		saved.Receipts = map[string]libraryReceipt{}
	}
	return &saved, nil
}

func libraryDefinitions(saved savedLibrary) ([]agents.Definition, error) {
	library, err := agents.Library()
	if err != nil {
		return nil, err
	}
	indices := map[string]int{}
	for i, d := range library {
		indices[d.ID] = i
	}
	for id, revisions := range saved.Revisions {
		if len(revisions) == 0 {
			continue
		}
		d := revisions[len(revisions)-1]
		if i, ok := indices[id]; ok {
			library[i] = d
		} else {
			library = append(library, d)
		}
	}
	sort.Slice(library, func(i, j int) bool { return library[i].ID < library[j].ID })
	return library, nil
}
func (e *Engine) AgentLibrary() ([]agents.Definition, error) {
	saved, err := e.readLibrary()
	if err != nil {
		return nil, err
	}
	return libraryDefinitions(saved)
}

// All edits create immutable revisions. Running attempts retain their own frozen
// definition, so editing an agent cannot change an admitted assignment.
func (e *Engine) SaveAgent(req AgentEdit) (agents.Definition, error) {
	e.Store.mu.Lock()
	defer e.Store.mu.Unlock()
	if !validGraphKey(req.Key) || len(strings.TrimSpace(req.Name)) < 2 || len(req.Name) > 100 || len(strings.TrimSpace(req.Description)) < 3 || len(req.Description) > 2000 || len(strings.TrimSpace(req.Instructions)) < 3 || len(req.Instructions) > 32000 {
		return agents.Definition{}, errors.New("provide a name, description and instructions within the field limits")
	}
	saved, err := e.readLibrary()
	if err != nil {
		return agents.Definition{}, err
	}
	digest := graphDigest(req)
	if old, ok := saved.Receipts[req.Key]; ok {
		if old.Digest != digest {
			return agents.Definition{}, ErrConflict
		}
		return old.Definition, nil
	}
	library, err := libraryDefinitions(saved)
	if err != nil {
		return agents.Definition{}, err
	}
	var prior *agents.Definition
	for _, d := range library {
		if d.ID == req.ID {
			copy := d
			prior = &copy
		}
	}
	if req.ID != "" && prior == nil {
		return agents.Definition{}, ErrNotFound
	}
	if prior != nil && (prior.Revision != req.Revision || prior.Kind == "orchestrator") {
		return agents.Definition{}, fmt.Errorf("agent changed or is the built-in orchestrator: %w", ErrConflict)
	}
	if prior == nil && len(library) >= 128 {
		return agents.Definition{}, errors.New("agent library capacity reached")
	}
	if len(saved.Revisions[req.ID]) >= 64 || len(saved.Receipts) >= 2048 {
		return agents.Definition{}, errors.New("agent revision capacity reached")
	}
	d, err := editedDefinition(library, prior, req)
	if err != nil {
		return agents.Definition{}, err
	}
	id := d.ID
	saved.Revisions[id] = append(saved.Revisions[id], d)
	saved.Receipts[req.Key] = libraryReceipt{Digest: digest, Definition: d}
	raw, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return agents.Definition{}, err
	}
	if len(raw) > 8<<20 {
		return agents.Definition{}, errors.New("agent library capacity reached")
	}
	if err = e.Store.commit("", "effect", "agent_library_changed", change{AgentLibrary: &saved}); err != nil {
		return agents.Definition{}, err
	}

	return d, nil
}

// editedDefinition derives the next revision from the prior typed manifest, so an
// edit changes only the authored text and keeps runtime requirements, capability
// ceiling, context, limits and provenance. A new agent starts from the generalist.
func editedDefinition(library []agents.Definition, prior *agents.Definition, req AgentEdit) (agents.Definition, error) {
	var m agents.Manifest
	if prior != nil {
		m = prior.Manifest
		m.Revision = prior.Revision + 1
	} else {
		var base *agents.Definition
		for i := range library {
			if library[i].ID == "mitos.generalist" {
				base = &library[i]
			}
		}
		if base == nil {
			return agents.Definition{}, errors.New("the generalist base definition is unavailable")
		}
		m = base.Manifest
		m.ID = "custom." + strings.ReplaceAll(newID("agent"), "_", "-")
		m.Revision = 1
		m.Provenance = agents.Provenance{Origin: "original", License: "operator-authored", Base: &agents.Base{Scope: "installation", ID: base.ID, Revision: base.Revision, SHA256: base.Digest}}
	}
	m.Name = strings.TrimSpace(req.Name)
	m.Description = strings.TrimSpace(req.Description)
	body := []byte(req.Instructions)
	sum := sha256.Sum256(body)
	m.Instructions.SHA256 = hex.EncodeToString(sum[:])
	d, err := agents.NewDefinition(m, body)
	if err != nil {
		return agents.Definition{}, fmt.Errorf("edited agent is invalid: %w", err)
	}
	return d, nil
}
