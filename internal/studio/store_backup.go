package studio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
)

// Backup writes a new backup directory. The writer gate freezes registrations,
// while readers remain available during IO. The SQLite backup API includes the
// committed WAL; copying runtime.db directly would not provide that guarantee.
func (s *Store) Backup(destination string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	j, ok := s.journal.(*sqliteJournal)
	if !ok {
		return errors.New("backup requires SQLite")
	}
	abs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if abs == s.dir {
		return errors.New("backup destination must differ from data directory")
	}
	var refs []struct {
		run string
		a   Artifact
	}
	for _, r := range s.runs {
		for _, a := range r.Artifacts {
			refs = append(refs, struct {
				run string
				a   Artifact
			}{r.ID, a})
		}
	}
	return s.mu.io(func() error {
		if err := os.Mkdir(abs, 0700); err != nil {
			return err
		}
		complete := false
		defer func() {
			if !complete {
				os.RemoveAll(abs)
			}
		}()
		dbpath := filepath.Join(abs, runtimeDatabase)
		if err := createPrivateFile(dbpath); err != nil {
			return err
		}
		if err := j.withConnection(func(raw any) error {
			conn, ok := raw.(interface {
				NewBackup(string) (*sqlite.Backup, error)
			})
			if !ok {
				return errors.New("SQLite driver has no backup API")
			}
			b, err := conn.NewBackup(sqliteDSN(dbpath, false))
			if err != nil {
				return err
			}
			_, stepErr := b.Step(-1)
			return errors.Join(stepErr, b.Finish())
		}); err != nil {
			return err
		}
		for _, ref := range refs {
			f, err := s.openArtifact(ref.run, ref.a)
			if err != nil {
				return err
			}
			data, readErr := io.ReadAll(io.LimitReader(f, ref.a.Bytes+1))
			closeErr := f.Close()
			if err = errors.Join(readErr, closeErr); err != nil {
				return err
			}
			// Verify exactly the bytes copied, independently of source metadata.
			if int64(len(data)) != ref.a.Bytes || digestText(string(data)) != ref.a.SHA256 {
				return errors.New("backup artifact checksum mismatch")
			}
			dir := filepath.Join(abs, "artifacts", ref.run)
			if err = os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			if err = writeSyncedFile(filepath.Join(dir, ref.a.Name), data); err != nil {
				return err
			}
			if err = syncDirectory(dir); err != nil {
				return err
			}
		}
		if err = copyIdentityFile(s.dir, abs); err != nil {
			return err
		}
		// Stored API-key references must remain usable with the restored runtime.
		if err = copyPrivateTree(filepath.Join(s.dir, "credentials"), filepath.Join(abs, "credentials")); err != nil {
			return err
		}
		if len(refs) > 0 {
			if err = syncDirectory(filepath.Join(abs, "artifacts")); err != nil {
				return err
			}
		}
		if err = syncFile(dbpath); err != nil {
			return err
		}
		if err = writeSyncedFile(filepath.Join(abs, "backup.version"), []byte("mitos-runtime-backup/v1\n")); err != nil {
			return err
		}
		if err = syncDirectory(abs); err != nil {
			return err
		}
		if err = syncDirectory(filepath.Dir(abs)); err != nil {
			return err
		}
		complete = true
		return nil
	})
}

// RestoreStore restores into a new directory. It verifies a staging copy before
// publication, never overwrites live state, and advances the journaled authority
// epoch so grants/tokens from either source or earlier restores cannot survive.
func RestoreStore(source, destination string) error {
	if raw, err := os.ReadFile(filepath.Join(source, "backup.version")); err != nil || string(raw) != "mitos-runtime-backup/v1\n" {
		return errors.New("unknown or incomplete runtime backup")
	}
	abs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(abs); !errors.Is(err, os.ErrNotExist) {
		return errors.New("restore destination must not exist")
	}
	stage, err := os.MkdirTemp(filepath.Dir(abs), ".mitos-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	j, err := openSQLiteJournal(stage)
	if err != nil {
		return err
	}
	db, err := openReadJournal(filepath.Join(source, runtimeDatabase))
	if err != nil {
		j.Close()
		return err
	}
	db.Close()
	err = j.withConnection(func(raw any) error {
		conn, ok := raw.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver has no restore API")
		}
		b, err := conn.NewRestore(sqliteDSN(filepath.Join(source, runtimeDatabase), true))
		if err != nil {
			return err
		}
		_, stepErr := b.Step(-1)
		return errors.Join(stepErr, b.Finish())
	})
	if err != nil {
		j.Close()
		return err
	}
	if err = copyPrivateTree(filepath.Join(source, "artifacts"), filepath.Join(stage, "artifacts")); err != nil {
		j.Close()
		return err
	}
	if err = copyPrivateTree(filepath.Join(source, "credentials"), filepath.Join(stage, "credentials")); err != nil {
		j.Close()
		return err
	}
	if err = copyIdentityFile(source, stage); err != nil {
		j.Close()
		return err
	}
	if err = migrateJournal(j.db); err != nil {
		j.Close()
		return err
	}
	s, err := openStoreWithJournal(stage, j, &fileArtifactStore{dir: stage})
	if err != nil {
		j.Close()
		return err
	}
	for _, r := range s.List() {
		for _, a := range r.Artifacts {
			if _, err = s.Artifact(r.ID, a.Name); err != nil {
				s.Close()
				return fmt.Errorf("restore artifact: %w", err)
			}
		}
	}
	s.mu.Lock()
	err = s.commit("", "effect", "authority_restored", change{AuthorityEpoch: newID("epoch")})
	s.mu.Unlock()
	if err != nil {
		s.Close()
		return err
	}
	if err = s.Close(); err != nil {
		return err
	}
	if err = syncDirectory(stage); err != nil {
		return err
	}
	// Atomically publish only if the destination is still absent. Never replace
	// a directory created by another process while verification was running.
	if err = renameNoReplace(stage, abs); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(abs))
}

func createPrivateFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return f.Close()
}
func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func writeSyncedFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	return errors.Join(writeErr, syncErr, f.Close())
}

// Backups contain private regular files only. Refuse links and special files;
// copying a symlink must never disclose files outside the requested data tree.
func copyPrivateTree(source, destination string) error {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("backup tree is not a directory")
	}
	var dirs []string
	err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			dirs = append(dirs, target)
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("backup contains non-regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = writeSyncedFile(target, data); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err = syncDirectory(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

func copyIdentityFile(source, destination string) error {
	path := filepath.Join(source, "mqtt-ed25519.key")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != 64 {
		return errors.New("invalid MQTT identity in backup")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65))
	if err != nil {
		return err
	}
	if len(raw) != 64 {
		return errors.New("invalid MQTT identity size")
	}
	return writeSyncedFile(filepath.Join(destination, "mqtt-ed25519.key"), raw)
}
