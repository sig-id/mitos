package studio

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// artifactStore retains bytes separately from journaled metadata. Put must make
// every object's bytes and names durable before returning. A failure may leave
// unreferenced files; only journaled metadata grants access to an object.
// Replacement is permitted only for names Store has verified are uncommitted.
type artifactStore interface {
	Put(run string, artifacts []ArtifactData, replaceUncommitted bool) error
	Open(run, name string) (artifactFile, error)
}

// artifactFile supports bounded reads and verification on the same descriptor.
// An adapter must refuse symlinks, special files and paths outside its namespace.
type artifactFile interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
	Stat() (fs.FileInfo, error)
}

type fileArtifactStore struct {
	dir  string
	dirs map[string]bool // Accessed only under Store's writer gate.
}

func validArtifactComponent(s string) bool {
	return s != "" && s != "." && s != ".." && filepath.Base(s) == s
}

func (f *fileArtifactStore) Put(id string, artifacts []ArtifactData, replaceUncommitted bool) error {
	if !validArtifactComponent(id) {
		return errors.New("invalid artifact run")
	}
	for _, a := range artifacts {
		if !validArtifactComponent(a.Name) {
			return errors.New("invalid artifact name")
		}
	}
	root := filepath.Join(f.dir, "artifacts")
	dir := filepath.Join(root, id)
	dirs := []string{dir}
	if !f.dirs[dir] {
		dirs = append(dirs, root)
	}
	if !f.dirs[root] {
		dirs = append(dirs, f.dir)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	files := make([]*os.File, 0, len(artifacts))
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	for _, a := range artifacts {
		path := filepath.Join(dir, a.Name)
		if replaceUncommitted {
			// The journal has no record for this name (checked above under
			// the store lock). The durable graph can reconstruct its bytes.
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		files = append(files, f)
		n, err := f.Write(a.Data)
		if err != nil {
			return err
		}
		if n != len(a.Data) {
			return io.ErrShortWrite
		}
	}
	for _, f := range files {
		if err := f.Sync(); err != nil {
			return err
		}
	}
	for len(files) > 0 {
		f := files[0]
		files = files[1:]
		if err := f.Close(); err != nil {
			return err
		}
	}
	// Persist names and new parents before the log can promise availability.
	for _, path := range dirs {
		if err := syncDirectory(path); err != nil {
			return err
		}
	}
	if f.dirs == nil {
		f.dirs = map[string]bool{}
	}
	f.dirs[root], f.dirs[dir] = true, true
	return nil
}

func (f *fileArtifactStore) Open(run, name string) (artifactFile, error) {
	if !validArtifactComponent(run) || !validArtifactComponent(name) {
		return nil, handoffError("unsafe_path", 0)
	}
	root, err := os.OpenRoot(filepath.Join(f.dir, "artifacts"))
	if err != nil {
		return nil, handoffError("unavailable", 0)
	}
	defer root.Close()
	info, err := root.Lstat(run)
	if err != nil || !info.IsDir() {
		return nil, handoffError("unsafe_path", 0)
	}
	file, err := root.OpenFile(run+"/"+name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, handoffError("unavailable", 0)
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, handoffError("integrity_failure", 0)
	}
	return file, nil
}

// openArtifact keeps object type and length checks at the common Store boundary,
// even when the backing adapter is replaced. Hash the returned descriptor in the
// caller's single pass, or in verifiedArtifact before bounded range reads.
func (s *Store) openArtifact(run string, a Artifact) (artifactFile, error) {
	f, err := s.artifacts.Open(run, a.Name)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() != a.Bytes {
		f.Close()
		return nil, handoffError("integrity_failure", 0)
	}
	return f, nil
}
