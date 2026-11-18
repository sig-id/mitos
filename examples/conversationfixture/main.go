// conversationfixture coordinates private negotiations and a personal thread.
// It owns its database and uses only the application HTTPS boundary.
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
	"strconv"
	"strings"
	"time"
)

const outcomeSchema = `{"type":"object","additionalProperties":false,"required":["outcome"],"properties":{"outcome":{"type":"string","enum":["accepted","declined"]}}}`
const replySchema = `{"type":"object","additionalProperties":false,"required":["reply"],"properties":{"reply":{"type":"string","maxLength":512}}}`
const checkpointSchema = `{"type":"object","additionalProperties":false,"required":["state"],"properties":{"state":{"type":"string"}}}`

type task struct {
	ID     string `json:"id"`
	Run    string `json:"current_run_id"`
	Status string `json:"status"`
}
type outcome struct {
	Outcome string `json:"outcome"`
}
type reply struct {
	Reply string `json:"reply"`
}
type application struct {
	db                                            *sql.DB
	http                                          *http.Client
	base, token, conversation, candidate, company string
	early                                         bool
}

func hash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func hit(name string) {
	if os.Getenv("MITOS_ENABLE_FAILPOINTS") == "1" && os.Getenv("MITOS_CONVERSATION_FAILPOINT") == name {
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
	database := flag.String("database", "conversation.db", "Application-owned database")
	authority := flag.String("mitos", "", "Application HTTPS URL")
	ca := flag.String("ca-file", "", "Authority PEM CA")
	tokenEnv := flag.String("token-env", "MITOS_APPLICATION_TOKEN", "Application credential environment variable")
	conversation := flag.String("conversation", "personal-one", "Stable conversation identity")
	candidate := flag.String("candidate-context", "private candidate context", "Negotiation-only candidate data")
	company := flag.String("company-context", "private company context", "Negotiation-only company data")
	version := flag.Uint64("preferences-version", 0, "Application preference version")
	tone := flag.String("tone", "concise", "Current personal-thread preference")
	early := flag.Bool("answer-early", false, "Deliver the answer as soon as negotiation context is prepared")
	timeout := flag.Duration("timeout", 90*time.Second, "Invocation bound")
	flag.Parse()
	if !strings.HasPrefix(*authority, "https://") || *conversation == "" || len(*conversation) > 128 || *version > 1000000 {
		return errors.New("supply a bounded conversation and HTTPS authority")
	}
	token := os.Getenv(*tokenEnv)
	if token == "" {
		return errors.New("application credential missing")
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
			return errors.New("invalid authority CA")
		}
	}
	private, err := os.OpenFile(*database, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = private.Chmod(0600); err != nil {
		private.Close()
		return err
	}
	if err = private.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", *database)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS preferences(id INTEGER PRIMARY KEY CHECK(id=1),version INTEGER NOT NULL,tone TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS version_intents(version INTEGER PRIMARY KEY,body BLOB NOT NULL,done INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS intents(key TEXT PRIMARY KEY,kind TEXT NOT NULL,request BLOB NOT NULL,task_id TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS input_intents(key TEXT PRIMARY KEY,task_id TEXT NOT NULL,body BLOB NOT NULL,done INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS outcomes(conversation TEXT PRIMARY KEY,run_id TEXT NOT NULL,digest TEXT NOT NULL,body BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS replies(conversation TEXT NOT NULL,version INTEGER NOT NULL,run_id TEXT NOT NULL,digest TEXT NOT NULL,body BLOB NOT NULL,PRIMARY KEY(conversation,version));`)
	if err != nil {
		return err
	}
	a := &application{db: db, http: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, base: strings.TrimRight(*authority, "/"), token: token, conversation: *conversation, candidate: *candidate, company: *company, early: *early}
	if err = a.savePreferences(*version, *tone); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	for {
		if err = a.syncVersions(ctx); err != nil {
			return err
		}
		done, body, err := a.step(ctx)
		if err != nil {
			return err
		}
		if done {
			fmt.Println(string(body))
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func (a *application) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Mitos-API-Version", "1")
	request.Header.Set("Authorization", "Bearer "+a.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16<<20+1))
	if err != nil {
		return err
	}
	if len(raw) > 16<<20 {
		return errors.New("response bound exceeded")
	}
	if response.StatusCode >= 300 {
		return fmt.Errorf("application HTTP %d", response.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
func (a *application) savePreferences(version uint64, tone string) error {
	if len(tone) > 128 || len(a.candidate) > 4096 || len(a.company) > 4096 {
		return errors.New("domain input bound exceeded")
	}
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prior uint64
	err = tx.QueryRow("SELECT version FROM preferences WHERE id=1").Scan(&prior)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && version <= prior {
		return nil
	}
	if version > prior+1 {
		return errors.New("advance preferences one version at a time")
	}
	if _, err = tx.Exec("INSERT INTO preferences(id,version,tone) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version,tone=excluded.tone", version, tone); err != nil {
		return err
	}
	if version > 0 {
		raw, _ := json.Marshal(map[string]any{"key": "profile-" + strconv.FormatUint(version, 10), "data": map[string]string{"tone": tone}, "versions": []map[string]any{{"name": "preferences", "expected": prior, "value": version}}})
		if _, err = tx.Exec("INSERT OR IGNORE INTO version_intents(version,body) VALUES(?,?)", version, raw); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (a *application) syncVersions(ctx context.Context) error {
	for {
		var version uint64
		var raw []byte
		err := a.db.QueryRow("SELECT version,body FROM version_intents WHERE done=0 ORDER BY version LIMIT 1").Scan(&version, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = a.call(ctx, "POST", "/api/inputs", json.RawMessage(raw), nil); err != nil {
			return err
		}
		if _, err = a.db.Exec("UPDATE version_intents SET done=1 WHERE version=?", version); err != nil {
			return err
		}
	}
}
func (a *application) admit(ctx context.Context, key, kind string, request any) (task, error) {
	raw, _ := json.Marshal(request)
	if _, err := a.db.Exec("INSERT OR IGNORE INTO intents(key,kind,request) VALUES(?,?,?)", key, kind, raw); err != nil {
		return task{}, err
	}
	var id string
	if err := a.db.QueryRow("SELECT request,task_id FROM intents WHERE key=?", key).Scan(&raw, &id); err != nil {
		return task{}, err
	}
	if id == "" {
		var admitted task
		if err := a.call(ctx, "POST", "/api/tasks", json.RawMessage(raw), &admitted); err != nil {
			return task{}, err
		}
		id = admitted.ID
		if _, err := a.db.Exec("UPDATE intents SET task_id=? WHERE key=? AND task_id=''", id, key); err != nil {
			return task{}, err
		}
	}
	var current task
	err := a.call(ctx, "GET", "/api/tasks/"+id, nil, &current)
	return current, err
}
func (a *application) sendAnswer(ctx context.Context, id string) error {
	key := "answer-" + hash([]byte(a.conversation))[:24]
	raw, _ := json.Marshal(map[string]any{"key": key, "data": map[string]any{"resume": true, "answer": "accepted"}})
	if _, err := a.db.Exec("INSERT OR IGNORE INTO input_intents(key,task_id,body) VALUES(?,?,?)", key, id, raw); err != nil {
		return err
	}
	hit("after_wait")
	var done bool
	if err := a.db.QueryRow("SELECT body,done FROM input_intents WHERE key=?", key).Scan(&raw, &done); err != nil {
		return err
	}
	if done {
		return nil
	}
	if err := a.call(ctx, "POST", "/api/tasks/"+id+"/inputs", json.RawMessage(raw), nil); err != nil {
		return err
	}
	_, err := a.db.Exec("UPDATE input_intents SET done=1 WHERE key=?", key)
	return err
}
func (a *application) step(ctx context.Context) (bool, []byte, error) {
	var version uint64
	var tone string
	if err := a.db.QueryRow("SELECT version,tone FROM preferences WHERE id=1").Scan(&version, &tone); err != nil {
		return false, nil, err
	}
	var saved []byte
	err := a.db.QueryRow("SELECT body FROM replies WHERE conversation=? AND version=?", a.conversation, version).Scan(&saved)
	if err == nil {
		return true, saved, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, nil, err
	}
	var negotiated outcome
	err = a.db.QueryRow("SELECT body FROM outcomes WHERE conversation=?", a.conversation).Scan(&saved)
	if errors.Is(err, sql.ErrNoRows) {
		key := "negotiation-" + hash([]byte(a.conversation))[:24]
		request := map[string]any{"title": "Private negotiation", "intent": "Keep candidate and company source context within this negotiation. Checkpoint with mitos_wait at cursor zero until an answer input arrives. Return only the typed outcome.", "app_id": "agent:mitos.generalist", "timeout_minutes": 1, "structured": map[string]any{"key": key, "serialization_key": key, "consumed_versions": map[string]uint64{"preferences": version}, "input": map[string]any{"kind": "negotiation", "candidate_context": a.candidate, "company_context": a.company}, "checkpoint_schema": json.RawMessage(checkpointSchema), "result_schema": json.RawMessage(outcomeSchema)}}
		current, err := a.admit(ctx, key, "negotiation", request)
		if err != nil {
			return false, nil, err
		}
		if current.Status == "failed" || current.Status == "cancelled" {
			return false, nil, errors.New("negotiation did not complete")
		}
		if current.Status == "done" {
			var raw json.RawMessage
			if err = a.call(ctx, "GET", "/api/runs/"+current.Run+"/result", nil, &raw); err != nil {
				return false, nil, err
			}
			d := json.NewDecoder(bytes.NewReader(raw))
			d.DisallowUnknownFields()
			if d.Decode(&negotiated) != nil || d.Decode(new(any)) != io.EOF || (negotiated.Outcome != "accepted" && negotiated.Outcome != "declined") {
				return false, nil, errors.New("invalid typed negotiation outcome")
			}
			body, _ := json.Marshal(negotiated)
			if _, err = a.db.Exec("INSERT OR IGNORE INTO outcomes(conversation,run_id,digest,body) VALUES(?,?,?,?)", a.conversation, current.Run, hash(body), body); err != nil {
				return false, nil, err
			}
		} else {
			ready := current.Status == "dormant"
			if a.early && !ready {
				var events []struct {
					Kind string `json:"kind"`
				}
				if err = a.call(ctx, "GET", "/api/runs/"+current.Run+"/events?after=0&limit=200", nil, &events); err != nil {
					return false, nil, err
				}
				for _, event := range events {
					if event.Kind == "worker_context_prepared" {
						ready = true
					}
				}
			}
			if ready {
				if err = a.sendAnswer(ctx, current.ID); err != nil {
					return false, nil, err
				}
			}
			return false, nil, nil
		}
	} else if err != nil {
		return false, nil, err
	} else {
		if err = json.Unmarshal(saved, &negotiated); err != nil {
			return false, nil, err
		}
	}
	// Decode and re-encode the typed outcome: source context cannot cross here.
	key := "personal-" + hash([]byte(a.conversation))[:24] + "-v" + strconv.FormatUint(version, 10)
	request := map[string]any{"title": "Personal conversation update", "intent": "Summarize only the typed negotiation outcome using the supplied preferences. No candidate or company source context is provided or authorized.", "app_id": "agent:mitos.generalist", "timeout_minutes": 1, "structured": map[string]any{"key": key, "serialization_key": "personal-" + hash([]byte(a.conversation))[:24], "consumed_versions": map[string]uint64{"preferences": version}, "input": map[string]any{"kind": "personal", "outcome": negotiated, "preferences": map[string]any{"version": version, "tone": tone}}, "result_schema": json.RawMessage(replySchema)}}
	current, err := a.admit(ctx, key, "personal", request)
	if err != nil {
		return false, nil, err
	}
	hit("after_personal_admission")
	if current.Status == "failed" || current.Status == "cancelled" {
		return false, nil, errors.New("personal update needs a current preference snapshot")
	}
	if current.Status != "done" {
		return false, nil, nil
	}
	var raw json.RawMessage
	if err = a.call(ctx, "GET", "/api/runs/"+current.Run+"/result", nil, &raw); err != nil {
		return false, nil, err
	}
	var value reply
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF || len(value.Reply) > 512 {
		return false, nil, errors.New("invalid typed personal reply")
	}
	body, _ := json.Marshal(value)
	tx, err := a.db.Begin()
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback()
	var latest uint64
	if err = tx.QueryRow("SELECT version FROM preferences WHERE id=1").Scan(&latest); err != nil {
		return false, nil, err
	}
	if latest != version {
		return false, nil, nil
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO replies(conversation,version,run_id,digest,body) VALUES(?,?,?,?,?)", a.conversation, version, current.Run, hash(body), body); err != nil {
		return false, nil, err
	}
	if err = tx.Commit(); err != nil {
		return false, nil, err
	}
	return true, body, nil
}
