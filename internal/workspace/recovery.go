package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func validCommit(commit string) bool {
	if len(commit) != 40 {
		return false
	}
	_, err := hex.DecodeString(commit)
	return err == nil
}

// Load reads only the named immutable commit, never the worker index, HEAD, or
// files. Recomputed content fingerprints bind recovery to the recorded snapshot.
func (h *History) Load(ctx context.Context, commit, fingerprint string) (Snapshot, error) {
	if !validCommit(commit) {
		return Snapshot{}, errors.New("invalid recovery commit")
	}
	listing, err := git(ctx, h.Base, h.Directory, nil, "ls-tree", "-r", "-z", "--long", commit, "--")
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Files: []File{}}
	type object struct {
		file File
		oid  string
	}
	var objects []object
	for _, row := range bytes.Split(listing, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		header, path, ok := bytes.Cut(row, []byte{'\t'})
		fields := strings.Fields(string(header))
		if !ok || len(fields) != 4 || fields[1] != "blob" || !safePath(string(path)) {
			return Snapshot{}, errors.New("unsupported recovery tree entry")
		}
		if fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000" {
			return Snapshot{}, errors.New("unsupported recovery file mode")
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 {
			return Snapshot{}, errors.New("invalid recovery file size")
		}
		snapshot.TotalBytes += size
		objects = append(objects, object{File{Path: string(path), Mode: fields[0], Bytes: size}, fields[2]})
		if snapshot.TotalBytes > DefaultLimits.Bytes || len(objects) > DefaultLimits.Files {
			return Snapshot{}, errors.New("recovery tree exceeds workspace limits")
		}
	}
	for _, object := range objects {
		data, err := git(ctx, h.Base, h.Directory, nil, "cat-file", "blob", object.oid)
		if err != nil {
			return Snapshot{}, err
		}
		if int64(len(data)) != object.file.Bytes {
			return Snapshot{}, errors.New("recovery blob size mismatch")
		}
		sum := sha256.Sum256(data)
		object.file.SHA256 = hex.EncodeToString(sum[:])
		object.file.Data = data
		snapshot.Files = append(snapshot.Files, object.file)
	}
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	encoded, err := json.Marshal(snapshot.Files)
	if err != nil {
		return Snapshot{}, err
	}
	sum := sha256.Sum256(encoded)
	snapshot.Fingerprint = hex.EncodeToString(sum[:])
	if snapshot.Fingerprint != fingerprint {
		return Snapshot{}, errors.New("recovery commit does not match the checkpoint fingerprint")
	}
	return snapshot, nil
}

func (h *History) Pin(ctx context.Context, commit string) error {
	if !validCommit(commit) {
		return errors.New("invalid checkpoint commit")
	}
	_, err := git(ctx, h.Base, h.Directory, nil, "update-ref", "refs/mitos/checkpoints/"+commit, commit)
	return err
}

// Restore publishes a fresh private workspace atomically. Prior segment trees
// are retained. Re-driving a logged pending rewind validates an existing
// destination instead of applying another patch or deleting abandoned work.
func (h *History) Restore(ctx context.Context, commit, fingerprint, destination string) error {
	snapshot, err := h.Load(ctx, commit, fingerprint)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid existing recovery destination")
		}
		existing, err := CaptureResult(ctx, filepath.Join(destination, "workspace"), DefaultLimits)
		if err != nil || existing.Fingerprint != fingerprint {
			return errors.New("existing recovery destination differs from the checkpoint")
		}
		metadata, err := os.Lstat(filepath.Join(destination, "workspace", ".git"))
		if err != nil || !metadata.IsDir() || metadata.Mode()&os.ModeSymlink != 0 {
			return errors.New("recovery Git metadata is missing or replaced")
		}
		head, err := git(ctx, filepath.Join(destination, "workspace"), filepath.Join(destination, "workspace", ".git"), nil, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(string(head)) != commit {
			return errors.New("recovery Git HEAD does not match the checkpoint")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(destination)
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	worktree := filepath.Join(temporary, "workspace")
	if err = snapshot.WriteTo(worktree); err != nil {
		return err
	}
	seed := *h
	seed.Base = temporary
	seed.BaselineCommit = commit
	if err = seed.seedWorktree(ctx, worktree); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = syncRecoveryTree(ctx, temporary); err != nil {
		return err
	}
	if err = os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("publish restored workspace: %w", err)
	}
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// syncRecoveryTree makes publication durable before checkpoint_restored can
// promise a reusable workspace. Directory metadata is synced after file data.
func syncRecoveryTree(ctx context.Context, root string) error {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err = ctx.Err(); err != nil {
			return err
		}
		directory, err := os.Open(directories[i])
		if err != nil {
			return err
		}
		err = directory.Sync()
		closeErr := directory.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
