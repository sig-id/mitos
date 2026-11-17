package studio

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"mitos/internal/failpoint"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var journalMigrations embed.FS

const runtimeDatabase = "runtime.db"
const runtimeLock = "runtime.lock"
const journalSchemaVersion = 5

type sqliteJournal struct {
	db     *sql.DB
	lock   *os.File
	closed bool
}

func lockRuntime(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, runtimeLock), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("this data directory is already open by another mitos process")
	}
	return f, nil
}

func sqliteDSN(path string, readonly bool) string {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	if readonly {
		q.Set("mode", "ro")
	} else {
		q.Set("mode", "rw")
	}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	return u.String()
}

func openSQLiteJournal(dir string) (_ *sqliteJournal, finalErr error) {
	lock, err := lockRuntime(dir)
	if err != nil {
		return nil, err
	}
	j := &sqliteJournal{lock: lock}
	defer func() {
		if finalErr != nil {
			j.Close()
		}
	}()
	path := filepath.Join(dir, runtimeDatabase)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	j.db, err = sql.Open("sqlite", sqliteDSN(path, false))
	if err != nil {
		return nil, err
	}
	j.db.SetMaxOpenConns(1)
	if err = migrateJournal(j.db); err != nil {
		return nil, err
	}
	var mode string
	if err = j.db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return nil, err
	}
	if mode != "wal" {
		return nil, fmt.Errorf("SQLite WAL unavailable: %s", mode)
	}
	if err = j.importJSONL(dir); err != nil {
		return nil, err
	}
	if err = syncDirectory(dir); err != nil {
		return nil, err
	}
	if err = syncDirectory(filepath.Dir(dir)); err != nil {
		return nil, err
	}
	return j, nil
}

func migrateJournal(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > journalSchemaVersion {
		return fmt.Errorf("unsupported runtime schema %d (maximum %d)", version, journalSchemaVersion)
	}
	if version == 0 {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("unknown unversioned runtime schema")
		}
	}
	for version < journalSchemaVersion {
		next := version + 1
		schema, err := journalMigrations.ReadFile(fmt.Sprintf("migrations/%03d.sql", next))
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(string(schema)); err == nil {
			_, err = tx.Exec("PRAGMA user_version=" + strconv.Itoa(next))
		}
		if err == nil {
			_, err = tx.Exec("INSERT INTO metadata(key,value) VALUES('schema_digest',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", digestText(string(schema)))
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		version = next
	}
	schema, err := journalMigrations.ReadFile(fmt.Sprintf("migrations/%03d.sql", version))
	if err != nil {
		return err
	}
	var signature string
	if err = db.QueryRow("SELECT value FROM metadata WHERE key='schema_digest'").Scan(&signature); err != nil || signature != digestText(string(schema)) {
		return errors.New("unknown runtime schema signature")
	}
	// Refuse a database merely stamped with a known version but unknown layout.
	for _, query := range []string{"SELECT seq,run_id,event FROM events LIMIT 0", "SELECT namespace,key,value FROM projections LIMIT 0", "SELECT key,value FROM metadata LIMIT 0"} {
		rows, err := db.Query(query)
		if err != nil {
			return fmt.Errorf("unknown runtime schema: %w", err)
		}
		rows.Close()
	}
	return nil
}

func (j *sqliteJournal) Replay(visit func(Event) error) error {
	if j.closed {
		return errJournalClosed
	}
	rows, err := j.db.Query("SELECT seq,event FROM events ORDER BY seq")
	if err != nil {
		return err
	}
	defer rows.Close()
	var seq int64
	var previous string
	for rows.Next() {
		var storedSeq int64
		var raw []byte
		var e Event
		if err = rows.Scan(&storedSeq, &raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &e); err != nil {
			return err
		}
		if storedSeq != e.Seq {
			return errors.New("journal sequence disagrees with record")
		}
		if err = validateJournalEvent(e, seq, previous); err != nil {
			return fmt.Errorf("event chain damaged at sequence %d: %w", e.Seq, err)
		}
		if err = visit(e); err != nil {
			return err
		}
		seq, previous = e.Seq, e.Hash
	}
	return rows.Err()
}

func (j *sqliteJournal) Append(events []Event, projections []journalProjection) error {
	if j.closed {
		return errJournalClosed
	}
	if len(events) == 0 {
		if len(projections) > 0 {
			return errors.New("projections require journal events")
		}
		return nil
	}
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seq int64
	var previous string
	var raw []byte
	err = tx.QueryRow("SELECT seq,event FROM events ORDER BY seq DESC LIMIT 1").Scan(&seq, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(raw) > 0 {
		var tail Event
		if err = json.Unmarshal(raw, &tail); err != nil {
			return err
		}
		previous = tail.Hash
	}
	for _, e := range events {
		if err = validateJournalEvent(e, seq, previous); err != nil {
			return err
		}
		raw, err = json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO events(seq,run_id,event) VALUES(?,?,?)", e.Seq, e.RunID, raw); err != nil {
			return err
		}
		seq, previous = e.Seq, e.Hash
	}
	if err = writeJournalProjections(tx, projections); err != nil {
		return err
	}
	failpoint.Hit("sqlite.before_commit")
	if err = tx.Commit(); err != nil {
		return err
	}
	failpoint.Hit("sqlite.after_commit")
	return nil
}

func (j *sqliteJournal) Close() error {
	if j.closed {
		return nil
	}
	j.closed = true
	var dbErr, lockErr error
	if j.db != nil {
		dbErr = j.db.Close()
	}
	if j.lock != nil {
		lockErr = j.lock.Close()
	}
	return errors.Join(dbErr, lockErr)
}

func openReadJournal(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path, true))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version < 1 || version > journalSchemaVersion {
		db.Close()
		return nil, fmt.Errorf("unsupported runtime schema %d", version)
	}
	return db, nil
}

// Keep driver-specific work at the adapter boundary, not in Store or workers.
func (j *sqliteJournal) withConnection(fn func(any) error) error {
	conn, err := j.db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(fn)
}

func writeJournalProjections(tx *sql.Tx, projections []journalProjection) error {
	var err error
	for _, p := range projections {
		if p.Namespace == "" || p.Key == "" {
			return errors.New("invalid projection")
		}
		if p.Value == nil {
			_, err = tx.Exec("DELETE FROM projections WHERE namespace=? AND key=?", p.Namespace, p.Key)
		} else {
			_, err = tx.Exec("INSERT INTO projections(namespace,key,value) VALUES(?,?,?) ON CONFLICT(namespace,key) DO UPDATE SET value=excluded.value", p.Namespace, p.Key, p.Value)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
