package studio

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readTestJournal(dir string) ([]byte, error) {
	db, err := openReadJournal(filepath.Join(dir, runtimeDatabase))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT event FROM events ORDER BY seq")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var data []byte
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		data = append(data, bytes.TrimSpace(raw)...)
		data = append(data, '\n')
	}
	return data, rows.Err()
}

// Deliberate direct database tampering is reserved for integrity tests.
func writeTestJournal(dir string, data []byte) error {
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(dir, runtimeDatabase), false))
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM events"); err != nil {
		return err
	}
	for i, line := range bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var e Event
		_ = json.Unmarshal(line, &e)
		if _, err = tx.Exec("INSERT INTO events(seq,run_id,event) VALUES(?,?,?)", i+1, e.RunID, line); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func TestSQLiteJournalConformance(t *testing.T) {
	journalConformance(t, func(t *testing.T) journalHarness {
		dir := t.TempDir()
		var current *sqliteJournal
		return journalHarness{open: func() journal {
			j, err := openSQLiteJournal(dir)
			if err != nil {
				t.Fatal(err)
			}
			current = j
			return j
		}, projection: func(namespace, key string) ([]byte, bool) {
			var value []byte
			err := current.db.QueryRow("SELECT value FROM projections WHERE namespace=? AND key=?", namespace, key).Scan(&value)
			if err == sql.ErrNoRows {
				return nil, false
			}
			if err != nil {
				t.Fatal(err)
			}
			return value, true
		}}
	})
}

func TestSQLiteRefusesUnknownSchema(t *testing.T) {
	for _, mode := range []string{"newer", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(dir, runtimeDatabase), false))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "newer" {
				_, err = db.Exec("PRAGMA user_version=999")
			} else {
				_, err = db.Exec("PRAGMA user_version=0")
			}
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if s, err = OpenStore(dir); err == nil {
				s.Close()
				t.Fatal("unknown schema accepted")
			}
		})
	}
}

func TestJSONLImportOnlyOnceAndPreservesBackup(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	raw, err := readTestJournal(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	backup := append(bytes.Clone(raw), []byte(`{"seq":2`)...)
	if err = os.WriteFile(path, backup, 0600); err != nil {
		t.Fatal(err)
	}
	imported, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = imported.Get(r.ID); err != nil {
		t.Fatal(err)
	}
	imported.Close()
	original, _ := os.ReadFile(path)
	if !bytes.Equal(original, backup) {
		t.Fatal("import modified original backup")
	}
	if err = os.WriteFile(path, []byte("deliberately corrupt backup\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal("runtime reread imported backup", err)
	}
	defer reopened.Close()
	if _, err = reopened.Get(r.ID); err != nil {
		t.Fatal(err)
	}
}
