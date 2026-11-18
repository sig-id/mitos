// appfixture owns its own database and communicates with Mitos through HTTP only.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	_ "modernc.org/sqlite"
	"net/http"
	"os"
	"strings"
	"time"
)

type application struct {
	db           *sql.DB
	http         *http.Client
	base, token  string
	actionTarget string
}
type task struct {
	Status string `json:"status"`
	ID     string `json:"id"`
	Run    string `json:"current_run_id"`
}

const schema = `{"type":"object","additionalProperties":false,"required":["reply"],"properties":{"reply":{"type":"string","maxLength":8192}}}`

func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func hit(p string) {
	if os.Getenv("MITOS_ENABLE_FAILPOINTS") == "1" && os.Getenv("MITOS_APPFIXTURE_FAILPOINT") == p {
		os.Exit(86)
	}
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	actionTarget := flag.String("action-target", "", "Optional admitted target for the external-effect fixture")
	path := flag.String("database", "application.db", "Application-owned SQLite file")
	base := flag.String("mitos", "", "Mitos application HTTPS URL")
	tokenEnv := flag.String("token-env", "MITOS_APPLICATION_TOKEN", "Application token environment variable")
	ca := flag.String("ca-file", "", "Trusted PEM CA")
	id := flag.String("message-id", "message-1", "Stable message identity")
	text := flag.String("text", "Hello", "Message body")
	timeout := flag.Duration("timeout", time.Minute, "Invocation bound")
	flag.Parse()
	if !strings.HasPrefix(*base, "https://") || *id == "" || len(*text) > 64<<10 {
		return errors.New("supply an HTTPS URL and bounded message")
	}
	token := os.Getenv(*tokenEnv)
	if token == "" {
		return errors.New("missing application token")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if *ca != "" {
		raw, err := os.ReadFile(*ca)
		if err != nil {
			return err
		}
		if !roots.AppendCertsFromPEM(raw) {
			return errors.New("invalid CA")
		}
	}
	db, err := sql.Open("sqlite", *path)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS messages(id TEXT PRIMARY KEY,body TEXT NOT NULL,published INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS dispatch_intents(message_id TEXT PRIMARY KEY,request BLOB NOT NULL,task_id TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS replies(message_id TEXT PRIMARY KEY,run_id TEXT NOT NULL,digest TEXT NOT NULL,body BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS cursor(id INTEGER PRIMARY KEY CHECK(id=1),seq INTEGER NOT NULL); INSERT OR IGNORE INTO cursor VALUES(1,0);`); err != nil {
		return err
	}
	a := &application{db: db, http: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}}, base: strings.TrimRight(*base, "/"), token: token, actionTarget: *actionTarget}
	if err = a.save(*id, *text); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	for {
		var result []byte
		err = db.QueryRow("SELECT body FROM replies WHERE message_id=?", *id).Scan(&result)
		if err == nil {
			fmt.Println(string(result))
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err = a.step(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func (a *application) save(id, text string) error {
	structured := map[string]any{"key": "fixture-" + hash([]byte(id))[:32], "input": map[string]string{"message": text}, "result_schema": json.RawMessage(schema)}
	if a.actionTarget != "" {
		structured["action_targets"] = []string{a.actionTarget}
	}
	request, _ := json.Marshal(map[string]any{"title": "Reply to message", "intent": "Return one typed reply to the supplied message.", "app_id": "agent:mitos.generalist", "timeout_minutes": 1, "structured": structured})

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prior string
	err = tx.QueryRow("SELECT body FROM messages WHERE id=?", id).Scan(&prior)
	if err == nil {
		if prior != text {
			return errors.New("message identity changed")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec("INSERT INTO messages(id,body) VALUES(?,?)", id, text); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO dispatch_intents(message_id,request) VALUES(?,?)", id, request); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *application) request(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Mitos-API-Version", "1")
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(raw) > 16<<20 {
		return nil, resp.StatusCode, errors.New("response exceeded bound")
	}
	return raw, resp.StatusCode, nil
}
func (a *application) step(ctx context.Context) error {
	rows, err := a.db.Query("SELECT message_id,request,task_id FROM dispatch_intents WHERE message_id IN (SELECT id FROM messages WHERE published=0) ORDER BY message_id LIMIT 100")
	if err != nil {
		return err
	}
	type intent struct {
		id, task string
		request  []byte
	}
	pending := []intent{}
	for rows.Next() {
		var p intent
		if err = rows.Scan(&p.id, &p.request, &p.task); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range pending {
		newAdmission := p.task == ""
		if newAdmission {
			hit("before_admission")
			raw, status, err := a.request(ctx, "POST", "/api/tasks", p.request)
			if err != nil {
				return err
			}
			if status != 201 {
				return fmt.Errorf("admission HTTP %d: %s", status, raw)
			}
			hit("after_admission")
			var receipt task
			if json.Unmarshal(raw, &receipt) != nil || receipt.ID == "" {
				return errors.New("invalid admission receipt")
			}
			p.task = receipt.ID
			if _, err = a.db.Exec("UPDATE dispatch_intents SET task_id=? WHERE message_id=? AND task_id=''", p.task, p.id); err != nil {
				return err
			}
		}
		// Admission reconciliation covers a result committed before a lost
		// admission response; steady-state delivery below is feed driven.
		if newAdmission {
			if err = a.reconcileTask(ctx, p.id, p.task); err != nil {
				return err
			}
		}
	}

	var cursor int64
	if err = a.db.QueryRow("SELECT seq FROM cursor WHERE id=1").Scan(&cursor); err != nil {
		return err
	}
	raw, status, err := a.request(ctx, "GET", fmt.Sprintf("/api/events?after=%d&limit=50", cursor), nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("feed HTTP %d", status)
	}
	var events []struct {
		Seq  int64  `json:"seq"`
		Run  string `json:"run_id"`
		Kind string `json:"kind"`
	}
	if err = json.Unmarshal(raw, &events); err != nil {
		return err
	}
	for _, e := range events {
		if e.Seq <= cursor {
			return errors.New("non-advancing event cursor")
		}
		if e.Kind == "run_finished" || e.Kind == "run_failed" {
			if err = a.consumeRun(ctx, e.Run); err != nil {
				return err
			}
		}
		cursor = e.Seq
	}
	_, err = a.db.Exec("UPDATE cursor SET seq=? WHERE id=1", cursor)
	return err
}
func (a *application) publish(message, run string, result []byte) error {
	var reply struct {
		Reply string `json:"reply"`
	}
	if json.Unmarshal(result, &reply) != nil || len(reply.Reply) > 8192 {
		return errors.New("invalid accepted result")
	}
	hit("before_publication")
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prior string
	err = tx.QueryRow("SELECT digest FROM replies WHERE message_id=?", message).Scan(&prior)
	if err == nil {
		if prior != hash(result) {
			return errors.New("published result changed")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec("INSERT INTO replies(message_id,run_id,digest,body) VALUES(?,?,?,?)", message, run, hash(result), result); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE messages SET published=1 WHERE id=?", message); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	hit("after_publication")
	return nil
}

func (a *application) reconcileTask(ctx context.Context, message, id string) error {
	raw, status, err := a.request(ctx, "GET", "/api/tasks/"+id, nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("task HTTP %d", status)
	}
	var current task
	if err = json.Unmarshal(raw, &current); err != nil {
		return err
	}
	if current.Status == "done" {
		return a.consumeRun(ctx, current.Run)
	}
	if current.Status == "failed" || current.Status == "cancelled" {
		return fmt.Errorf("runtime task ended %s", current.Status)
	}
	return nil
}
func (a *application) consumeRun(ctx context.Context, id string) error {
	raw, status, err := a.request(ctx, "GET", "/api/runs/"+id, nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("run HTTP %d", status)
	}
	var run struct {
		TaskID   string `json:"task_id"`
		Status   string `json:"status"`
		Verified bool   `json:"verified"`
	}
	if err = json.Unmarshal(raw, &run); err != nil {
		return err
	}
	var message string
	err = a.db.QueryRow("SELECT message_id FROM dispatch_intents WHERE task_id=? AND message_id IN (SELECT id FROM messages WHERE published=0)", run.TaskID).Scan(&message)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.Status != "done" || !run.Verified {
		if run.Status == "failed" {
			return a.reconcileTask(ctx, message, run.TaskID)
		}
		return nil
	}
	result, status, err := a.request(ctx, "GET", "/api/runs/"+id+"/result", nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("result HTTP %d", status)
	}
	return a.publish(message, id, result)
}
