package studio

import (
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
type savedLibrary struct {
	Revisions map[string][]agents.Definition `json:"revisions"`
	Receipts  map[string]libraryReceipt      `json:"receipts"`
}

func (e *Engine) readLibrary() (savedLibrary, error) {
	saved := savedLibrary{Revisions: map[string][]agents.Definition{}, Receipts: map[string]libraryReceipt{}}
	f, err := os.Open(filepath.Join(e.Store.Dir(), "agent-library.json"))
	if errors.Is(err, os.ErrNotExist) {
		return saved, nil
	}
	if err != nil {
		return saved, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if err != nil {
		return saved, err
	}
	if len(raw) > 8<<20 {
		return saved, errors.New("agent library exceeds 8 MiB")
	}
	err = json.Unmarshal(raw, &saved)
	return saved, err
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
	e.libraryMu.Lock()
	defer e.libraryMu.Unlock()
	saved, err := e.readLibrary()
	if err != nil {
		return nil, err
	}
	return libraryDefinitions(saved)
}

// All edits create immutable revisions. Running attempts retain their own frozen
// definition, so editing an agent cannot change an admitted assignment.
func (e *Engine) SaveAgent(req AgentEdit) (agents.Definition, error) {
	e.libraryMu.Lock()
	defer e.libraryMu.Unlock()
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
	if prior != nil && (prior.Revision != req.Revision || prior.Kind == "planner") {
		return agents.Definition{}, fmt.Errorf("agent changed or is the built-in planner: %w", ErrConflict)
	}
	if prior == nil && len(library) >= 128 {
		return agents.Definition{}, errors.New("agent library capacity reached")
	}
	id := req.ID
	revision := 1
	if prior == nil {
		id = "custom." + newID("agent")
	} else {
		revision = prior.Revision + 1
	}
	if len(saved.Revisions[id]) >= 64 || len(saved.Receipts) >= 2048 {
		return agents.Definition{}, errors.New("agent revision capacity reached")
	}
	d := agents.Definition{ID: id, Revision: revision, Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description), Kind: "worker", Instructions: req.Instructions}
	d.Digest = graphDigest(d)
	saved.Revisions[id] = append(saved.Revisions[id], d)
	saved.Receipts[req.Key] = libraryReceipt{Digest: digest, Definition: d}
	raw, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return agents.Definition{}, err
	}
	if len(raw) > 8<<20 {
		return agents.Definition{}, errors.New("agent library capacity reached")
	}
	file, err := os.CreateTemp(e.Store.Dir(), ".agent-library-*")
	if err != nil {
		return agents.Definition{}, err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return agents.Definition{}, err
	}
	if err = os.Rename(file.Name(), filepath.Join(e.Store.Dir(), "agent-library.json")); err != nil {
		return agents.Definition{}, err
	}
	dir, err := os.Open(e.Store.Dir())
	if err != nil {
		return agents.Definition{}, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return agents.Definition{}, err
	}
	return d, nil
}
