package studio

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

// Each match is one directory name, or an npm scope and one directory name.
var piNPMName = regexp.MustCompile(`^(@[a-zA-Z0-9_~-][a-zA-Z0-9._~-]*/)?[a-zA-Z0-9_~-][a-zA-Z0-9._~-]*$`)

// Pi resolves npm sources in <agent dir>/npm/node_modules before deciding
// whether to install them. Link individual installed packages so Node can
// resolve their transitive dependencies from the original installation.
// Keep the npm prefix, node_modules, and scope directories private: if Pi
// installs a missing or different version, npm can replace the directory
// entry and write its manifest and lockfile inside this attempt.
func stagePiNPM(host, config string) error {
	dst := filepath.Join(config, "npm")
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		return err // Keep the installation of a resumed or previously staged attempt.
	}
	src, err := filepath.Abs(filepath.Join(host, "npm"))
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(src, "package.json"))
	if err != nil {
		return nil // A missing or unreadable host installation is a cold start.
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if json.Unmarshal(raw, &manifest) != nil || len(manifest.Dependencies) == 0 {
		return nil // No installed package set to reuse. Let Pi populate its prefix.
	}
	stage, err := os.MkdirTemp(config, ".npm-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for name := range manifest.Dependencies {
		if !piNPMName.MatchString(name) {
			delete(manifest.Dependencies, name)
			continue
		}
		rel := filepath.Join("node_modules", filepath.FromSlash(name))
		target := filepath.Join(src, rel)
		info, err := os.Stat(filepath.Join(target, "package.json"))
		if err != nil || !info.Mode().IsRegular() {
			delete(manifest.Dependencies, name)
			continue
		}
		link := filepath.Join(stage, rel)
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			return err
		}
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}
	if len(manifest.Dependencies) == 0 {
		return nil
	}
	// Keep linked packages declared if npm needs to install another one.
	// Do not import the operator's project scripts or other npm settings.
	raw, err = json.Marshal(map[string]any{"name": "pi-extensions", "private": true, "dependencies": manifest.Dependencies})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "package.json"), raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(stage, dst); err != nil {
		if _, statErr := os.Stat(dst); statErr == nil {
			return nil // Another caller published the attempt prefix first.
		}
		return err
	}
	return nil
}
