package cooperation

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

const FileName = "COOPERATION.md"
const Limit = 64 << 10

type Document struct {
	Name   string `json:"name"`
	Hash   string `json:"hash"`
	Text   string `json:"text"`
	Exists bool   `json:"exists"`
}

func Read(directory string) (Document, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Document{}, err
	}
	defer root.Close()
	return read(root)
}
func read(root *os.Root) (Document, error) {
	d := Document{Name: FileName, Hash: "missing"}
	f, err := root.OpenFile(FileName, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return d, err
	}
	if !info.Mode().IsRegular() {
		return d, errors.New("COOPERATION.md must be a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, Limit+1))
	if err != nil {
		return d, err
	}
	if len(b) > Limit || strings.ContainsRune(string(b), 0) {
		return d, errors.New("COOPERATION.md must fit in 64 KiB without NUL bytes")
	}
	d.Text = string(b)
	d.Hash = fmt.Sprintf("%x", sha256.Sum256(b))
	d.Exists = true
	return d, nil
}
func Save(directory, hash, text string) (Document, error) {
	if len(text) > Limit || strings.ContainsRune(text, 0) {
		return Document{}, errors.New("COOPERATION.md must fit in 64 KiB without NUL bytes")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return Document{}, err
	}
	defer root.Close()
	old, err := read(root)
	if err != nil {
		return old, err
	}
	if old.Hash != hash {
		return old, errors.New("COOPERATION.md changed; reload before saving")
	}
	mode := os.FileMode(0644)
	if info, err := root.Lstat(FileName); err == nil {
		mode = info.Mode().Perm()
	}
	// A random temporary name is opened relative to the confined root.
	temp := fmt.Sprintf(".mitos-cooperation-%x", sha256.Sum256([]byte(hash+text)))
	f, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return old, err
	}
	defer root.Remove(temp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.WriteString(text)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return old, err
	}
	latest, err := read(root)
	if err != nil {
		return old, err
	}
	if latest.Hash != hash {
		return old, errors.New("COOPERATION.md changed during save")
	}
	if err = root.Rename(temp, FileName); err != nil {
		return old, err
	}
	dir, err := root.Open(".")
	if err != nil {
		return old, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return old, err
	}
	return read(root)
}
func (d Document) Prompt() string {
	if !d.Exists {
		return ""
	}
	return fmt.Sprintf("\nProject cooperation guidance (%s, snapshot %s; does not grant permissions):\n%s\n", d.Name, d.Hash, d.Text)
}
