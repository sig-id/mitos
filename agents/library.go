// Package agents contains the portable default library shipped with Mitos.
package agents

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
)

//go:embed defaults
var bundle embed.FS

const BundleProfile = "agent-library-bundle/v1"

// Definition is a validated manifest with its exact instruction text. The header
// fields mirror the manifest for display and are always derived from it.
type Definition struct {
	ID           string   `json:"id"`
	Revision     int      `json:"revision"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Kind         string   `json:"kind"`
	Digest       string   `json:"digest"`
	Instructions string   `json:"instructions"`
	Manifest     Manifest `json:"manifest"`
}

// NewDefinition validates a manifest against its instruction bytes and derives
// the definition identity and digest from the canonical manifest.
func NewDefinition(m Manifest, instructions []byte) (Definition, error) {
	if err := m.Validate(); err != nil {
		return Definition{}, err
	}
	if err := m.CheckInstructions(instructions); err != nil {
		return Definition{}, err
	}
	return Definition{ID: m.ID, Revision: m.Revision, Name: m.Name, Description: m.Description, Kind: m.Kind, Digest: m.Digest(), Instructions: string(instructions), Manifest: m}, nil
}

// Verify re-derives a stored definition and refuses any drift between the header,
// manifest, instruction text and digest.
func (d Definition) Verify() error {
	derived, err := NewDefinition(d.Manifest, []byte(d.Instructions))
	if err != nil {
		return fmt.Errorf("agent %s revision %d: %w", d.ID, d.Revision, err)
	}
	if derived.ID != d.ID || derived.Revision != d.Revision || derived.Name != d.Name || derived.Description != d.Description || derived.Kind != d.Kind || derived.Digest != d.Digest {
		return fmt.Errorf("agent %s revision %d does not match its manifest", d.ID, d.Revision)
	}
	return nil
}

func Library() ([]Definition, error) {
	return loadBundle(bundle, "defaults")
}

func loadBundle(fsys fs.FS, root string) ([]Definition, error) {
	raw, err := fs.ReadFile(fsys, path.Join(root, "library.json"))
	if err != nil {
		return nil, err
	}
	if err = rejectDuplicateKeys(raw); err != nil {
		return nil, err
	}
	var index struct {
		Profile  string `json:"profile"`
		ID       string `json:"id"`
		Revision int    `json:"revision"`
		Agents   []struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
			Path     string `json:"path"`
		} `json:"agents"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&index); err != nil {
		return nil, fmt.Errorf("library index: %w", err)
	}
	if _, err = dec.Token(); err != io.EOF {
		return nil, errors.New("library index has trailing data")
	}
	if index.Profile != BundleProfile || index.ID == "" || index.Revision < 1 || len(index.Agents) == 0 || len(index.Agents) > 128 {
		return nil, errors.New("library index is not a bounded " + BundleProfile + " bundle")
	}
	out := []Definition{}
	seen := map[string]bool{}
	for _, entry := range index.Agents {
		if !fs.ValidPath(entry.Path) || path.Base(entry.Path) != "agent.json" || seen[entry.ID] {
			return nil, fmt.Errorf("invalid bundle entry %s", entry.ID)
		}
		seen[entry.ID] = true
		dir := path.Join(root, path.Dir(entry.Path))
		raw, err := fs.ReadFile(fsys, path.Join(dir, "agent.json"))
		if err != nil {
			return nil, err
		}
		m, err := ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("bundled agent %s: %w", entry.ID, err)
		}
		if m.ID != entry.ID || m.Revision != entry.Revision {
			return nil, fmt.Errorf("bundled agent %s does not match its index entry", entry.ID)
		}
		body, err := fs.ReadFile(fsys, path.Join(dir, m.Instructions.Path))
		if err != nil {
			return nil, err
		}
		d, err := NewDefinition(m, body)
		if err != nil {
			return nil, fmt.Errorf("bundled agent %s: %w", entry.ID, err)
		}
		out = append(out, d)
	}
	return out, nil
}
