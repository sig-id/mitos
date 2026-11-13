package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

// WithInstructions freezes the root guidance in the attempt baseline, so a later
// project edit neither changes an existing goal nor appears as an agent patch.
func WithInstructions(snapshot Snapshot, name string, data []byte) (Snapshot, error) {
	if name != "AGENTS.md" && name != "agents.md" {
		return Snapshot{}, errors.New("invalid instructions filename")
	}
	return withDocument(snapshot, name, data, []string{"AGENTS.md", "agents.md"}, true)
}

func WithCooperation(snapshot Snapshot, data []byte, exists bool) (Snapshot, error) {
	return withDocument(snapshot, "COOPERATION.md", data, []string{"COOPERATION.md"}, exists)
}

func withDocument(snapshot Snapshot, name string, data []byte, remove []string, exists bool) (Snapshot, error) {
	files := make([]File, 0, len(snapshot.Files)+1)
	var size int64
	for _, file := range snapshot.Files {
		skip := false
		for _, name := range remove {
			skip = skip || file.Path == name
		}
		if skip {
			continue
		}
		files = append(files, file)
		size += file.Bytes
	}
	sum := sha256.Sum256(data)
	if exists {
		files = append(files, File{Path: name, Data: append([]byte(nil), data...), Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Mode: "100644"})
		size += int64(len(data))
	}
	if size > DefaultLimits.Bytes || len(files) > DefaultLimits.Files {
		return Snapshot{}, errors.New("frozen project instructions exceed workspace limits")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	raw, err := json.Marshal(files)
	if err != nil {
		return Snapshot{}, err
	}
	sum = sha256.Sum256(raw)
	return Snapshot{Files: files, TotalBytes: size, Fingerprint: hex.EncodeToString(sum[:])}, nil
}
