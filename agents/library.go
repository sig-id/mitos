// Package agents contains the portable default library shipped with Mitos.
package agents

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
)

//go:embed defaults
var bundle embed.FS

type Definition struct {
	ID           string          `json:"id"`
	Revision     int             `json:"revision"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Kind         string          `json:"kind"`
	Digest       string          `json:"digest"`
	Instructions string          `json:"instructions"`
	Manifest     json.RawMessage `json:"manifest"`
}

func Library() ([]Definition, error) {
	raw, err := bundle.ReadFile("defaults/library.json")
	if err != nil {
		return nil, err
	}
	var index struct {
		Agents []struct {
			ID       string
			Revision int
			Path     string
		}
	}
	if err = json.Unmarshal(raw, &index); err != nil {
		return nil, err
	}
	out := []Definition{}
	for _, entry := range index.Agents {
		raw, err := bundle.ReadFile("defaults/" + entry.Path)
		if err != nil {
			return nil, err
		}
		var m struct {
			ID, Name, Description, Kind string
			Revision                    int
			Instructions                struct{ Path, SHA256 string }
		}
		if err = json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		body, err := bundle.ReadFile(path.Join("defaults", path.Dir(entry.Path), m.Instructions.Path))
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(body)
		if hex.EncodeToString(hash[:]) != m.Instructions.SHA256 || m.ID != entry.ID || m.Revision != entry.Revision {
			return nil, fmt.Errorf("invalid bundled agent %s", entry.ID)
		}
		hash = sha256.Sum256(raw)
		out = append(out, Definition{m.ID, m.Revision, m.Name, m.Description, m.Kind, hex.EncodeToString(hash[:]), string(body), raw})
	}
	return out, nil
}
