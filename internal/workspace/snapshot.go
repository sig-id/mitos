package workspace

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

type Limits struct {
	Bytes int64
	Files int
}

var DefaultLimits = Limits{Bytes: 64 << 20, Files: 10000}

type File struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
	Data   []byte `json:"-"`
}

type Snapshot struct {
	Files       []File `json:"files"`
	TotalBytes  int64  `json:"total_bytes"`
	Fingerprint string `json:"fingerprint"`
}

type LimitError struct {
	Bytes   int64
	Files   int
	Limits  Limits
	Largest []File
}

func (e *LimitError) Error() string {
	var names []string
	for _, f := range e.Largest {
		names = append(names, fmt.Sprintf("%q (%d bytes)", f.Path, f.Bytes))
	}
	return fmt.Sprintf("workspace exceeds %d bytes or %d files: %d bytes in %d files; largest contributors: %s", e.Limits.Bytes, e.Limits.Files, e.Bytes, e.Files, strings.Join(names, ", "))
}

// CaptureSource selects tracked and untracked-but-not-ignored paths using Git.
// Symlinks are omitted on intake. No source branch, index, or file is changed.
func CaptureSource(ctx context.Context, source, dataDir string, limits Limits) (Snapshot, error) {
	paths, err := sourcePaths(ctx, source)
	if err != nil {
		return Snapshot{}, err
	}
	return capture(ctx, source, paths, dataDir, limits, false)
}

// CaptureResult ignores worker Git metadata but never trusts worker ignore rules:
// a worker cannot conceal an oracle edit by adding its path to .gitignore.
func CaptureResult(ctx context.Context, root string, limits Limits) (Snapshot, error) {
	return captureResult(ctx, root, limits, true)
}

// CaptureData includes every data file, including names used for Git metadata.
func CaptureData(ctx context.Context, root string, limits Limits) (Snapshot, error) {
	return captureResult(ctx, root, limits, false)
}
func captureResult(ctx context.Context, root string, limits Limits, omitGit bool) (Snapshot, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if omitGit && d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return capturePaths(ctx, root, paths, "", limits, true, !omitGit)
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func safePath(path string) bool {
	if !safeDataPath(path) {
		return false
	}
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if component == ".git" {
			return false
		}
	}
	return true
}
func safeDataPath(path string) bool {
	return filepath.IsLocal(path) && path != "." && utf8.ValidString(path) && !strings.ContainsRune(path, 0)
}

func capture(ctx context.Context, directory string, paths []string, dataDir string, limits Limits, links bool) (Snapshot, error) {
	return capturePaths(ctx, directory, paths, dataDir, limits, links, false)
}
func capturePaths(ctx context.Context, directory string, paths []string, dataDir string, limits Limits, links, allowGit bool) (Snapshot, error) {
	if limits.Bytes <= 0 || limits.Files <= 0 {
		return Snapshot{}, errors.New("workspace limits must be positive")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Snapshot{}, err
	}
	defer root.Close()
	sort.Strings(paths)
	result := Snapshot{Files: []File{}}
	seen := map[string]bool{}
	// Preflight every eligible file before any destination bytes are written.
	for _, path := range paths {
		if err = ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		if !safeDataPath(path) {
			return Snapshot{}, fmt.Errorf("unsupported data path %q", path)
		}
		if !allowGit && !safePath(path) {
			if strings.Contains(filepath.ToSlash(path), ".git/") || path == ".git" {
				continue
			}
			return Snapshot{}, fmt.Errorf("unsupported workspace path %q", path)
		}
		if dataDir != "" && within(dataDir, filepath.Join(directory, path)) {
			continue
		}
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Snapshot{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 && !links {
			continue
		}
		if info.IsDir() {
			return Snapshot{}, fmt.Errorf("nested repository or submodule %q requires an explicit materialization policy", path)
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return Snapshot{}, fmt.Errorf("unsupported workspace file type at %q", path)
		}
		mode := "100644"
		if info.Mode().Perm()&0111 != 0 {
			mode = "100755"
		}
		if info.Mode()&os.ModeSymlink != 0 {
			mode = "120000"
		}
		result.Files = append(result.Files, File{Path: filepath.ToSlash(path), Bytes: info.Size(), Mode: mode})
		result.TotalBytes += info.Size()
	}
	if result.TotalBytes > limits.Bytes || len(result.Files) > limits.Files {
		largest := append([]File(nil), result.Files...)
		sort.Slice(largest, func(i, j int) bool { return largest[i].Bytes > largest[j].Bytes })
		return Snapshot{}, &LimitError{result.TotalBytes, len(result.Files), limits, largest[:min(5, len(largest))]}
	}
	result.TotalBytes = 0
	for i := range result.Files {
		if err = ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		file := &result.Files[i]
		if file.Mode == "120000" {
			target, err := root.Readlink(file.Path)
			if err != nil {
				return Snapshot{}, err
			}
			file.Data = []byte(target)
		} else {
			f, err := root.OpenFile(file.Path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return Snapshot{}, err
			}
			info, err := f.Stat()
			if err != nil {
				f.Close()
				return Snapshot{}, err
			}
			if !info.Mode().IsRegular() {
				f.Close()
				return Snapshot{}, fmt.Errorf("file type changed at %q", file.Path)
			}
			file.Data, err = io.ReadAll(io.LimitReader(f, limits.Bytes-result.TotalBytes+1))
			closeErr := f.Close()
			if err != nil {
				return Snapshot{}, err
			}
			if closeErr != nil {
				return Snapshot{}, closeErr
			}
			if info.Mode().Perm()&0111 != 0 {
				file.Mode = "100755"
			} else {
				file.Mode = "100644"
			}
		}
		file.Bytes = int64(len(file.Data))
		result.TotalBytes += file.Bytes
		if result.TotalBytes > limits.Bytes {
			return Snapshot{}, fmt.Errorf("workspace grew beyond %d bytes while reading %q", limits.Bytes, file.Path)
		}
		sum := sha256.Sum256(file.Data)
		file.SHA256 = hex.EncodeToString(sum[:])
	}
	encoded, err := json.Marshal(result.Files)
	if err != nil {
		return Snapshot{}, err
	}
	sum := sha256.Sum256(encoded)
	result.Fingerprint = hex.EncodeToString(sum[:])
	return result, nil
}

func (s Snapshot) WriteTo(directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, file := range s.Files {
		if !safePath(file.Path) {
			return fmt.Errorf("invalid snapshot path %q", file.Path)
		}
		if err = root.MkdirAll(filepath.Dir(file.Path), 0700); err != nil {
			return err
		}
		if file.Mode == "120000" {
			// Snapshot links are materialized only if their lexical target stays in the
			// tree. The archive and patch still preserve rejected links as evidence.
			target := string(file.Data)
			if filepath.IsAbs(target) || !filepath.IsLocal(filepath.Join(filepath.Dir(file.Path), target)) {
				return fmt.Errorf("symlink %q escapes the result workspace", file.Path)
			}
			if err = root.Symlink(target, file.Path); err != nil {
				return err
			}
			continue
		}
		mode := os.FileMode(0600)
		if file.Mode == "100755" {
			mode = 0700
		}
		f, err := root.OpenFile(file.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		err = copyBytes(f, file.Data)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (s Snapshot) Archive() ([]byte, error) {
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for _, file := range s.Files {
		header := &tar.Header{Name: file.Path, Mode: 0600, Size: int64(len(file.Data)), Typeflag: tar.TypeReg}
		if file.Mode == "100755" {
			header.Mode = 0700
		}
		if file.Mode == "120000" {
			header.Typeflag = tar.TypeSymlink
			header.Linkname = string(file.Data)
			header.Size = 0
		}
		if err := archive.WriteHeader(header); err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg {
			if err := copyBytes(archive, file.Data); err != nil {
				return nil, err
			}
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if err := compressed.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
