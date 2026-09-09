package observation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

const MaxSessionBytes = 16 << 20
const MaxRecordBytes = 1 << 20

// SessionEvent is C2 evidence, never an instruction to mutate the run. In
// particular final_result is a harness claim, not an acceptance verdict.
type SessionEvent struct {
	Type         string          `json:"type"`
	Text         string          `json:"text,omitempty"`
	ToolID       string          `json:"tool_id,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	Output       json.RawMessage `json:"output,omitempty"`
	InputTokens  int64           `json:"input_tokens,omitempty"`
	OutputTokens int64           `json:"output_tokens,omitempty"`
	Status       string          `json:"status,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
}

// Normalizer is the adapter seam for native harness session schemas. The
// substrate tails bytes and records offsets; adapters only decode complete rows.
type Normalizer interface {
	Decode([]byte) ([]SessionEvent, error)
}

type JSONLNormalizer struct{}

func (JSONLNormalizer) Decode(line []byte) ([]SessionEvent, error) {
	var e SessionEvent
	d := json.NewDecoder(bytes.NewReader(line))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		return nil, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("session row contains trailing JSON")
	}
	switch e.Type {
	case "assistant_text", "final_result":
	case "tool_use":
		if e.ToolID == "" || e.ToolName == "" {
			return nil, errors.New("tool_use needs tool_id and tool_name")
		}
	case "tool_result":
		if e.ToolID == "" {
			return nil, errors.New("tool_result needs tool_id")
		}
	case "token_usage":
		if e.InputTokens < 0 || e.OutputTokens < 0 {
			return nil, errors.New("negative token usage")
		}
	case "status":
		if e.Status == "" {
			return nil, errors.New("status needs a value")
		}
	default:
		return nil, fmt.Errorf("unknown session event type %q", e.Type)
	}
	return []SessionEvent{e}, nil
}

type SessionRecord struct {
	Path      string         `json:"path"`
	Offset    int64          `json:"offset"`
	EndOffset int64          `json:"end_offset"`
	SHA256    string         `json:"sha256"`
	Events    []SessionEvent `json:"events"`
}

type Tail struct {
	root       *os.Root
	path       string
	normalizer Normalizer
	identity   os.FileInfo
	file       *os.File
	prefix     []byte
	offset     int
}

func NewTail(base, path string, normalizer Normalizer) (*Tail, error) {
	if normalizer == nil {
		return nil, errors.New("session normalizer is required")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	return &Tail{root: root, path: path, normalizer: normalizer}, nil
}
func (t *Tail) Close() error {
	var err error
	if t.file != nil {
		err = t.file.Close()
	}
	return errors.Join(err, t.root.Close())
}

// Poll delivers only complete records. A failed durable consumer does not
// advance that row's offset. Prefix mutation, replacement, truncation, unknown
// schemas and quota violations are contract failures, not silent data loss.
func (t *Tail) Poll(consume func(SessionRecord) error) error {
	f, err := t.root.OpenFile(t.path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) && t.identity == nil {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		if f != t.file {
			_ = f.Close()
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("session artifact is not a regular file")
	}
	if t.identity != nil && !os.SameFile(t.identity, info) {
		return errors.New("session artifact replaced")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSessionBytes+1))
	if err != nil {
		return err
	}
	if len(data) > MaxSessionBytes {
		return errors.New("session artifact exceeds 16 MiB")
	}
	if len(data) < len(t.prefix) || !bytes.Equal(data[:len(t.prefix)], t.prefix) {
		return errors.New("session artifact is not append-only")
	}
	t.identity = info
	if t.file == nil {
		t.file = f
	}
	t.prefix = data
	for t.offset < len(data) {
		remaining := data[t.offset:]
		end := bytes.IndexByte(remaining, '\n')
		if end < 0 {
			if len(remaining) > MaxRecordBytes {
				return errors.New("session row exceeds 1 MiB")
			}
			return nil
		}
		if end > MaxRecordBytes {
			return errors.New("session row exceeds 1 MiB")
		}
		row := remaining[:end+1]
		events, err := t.normalizer.Decode(row)
		if err != nil {
			return fmt.Errorf("session offset %d: %w", t.offset, err)
		}
		digest := sha256.Sum256(row)
		record := SessionRecord{t.path, int64(t.offset), int64(t.offset + len(row)), hex.EncodeToString(digest[:]), events}
		if err = consume(record); err != nil {
			return err
		}
		t.offset += len(row)
	}
	return nil
}

func (t *Tail) Finish(consume func(SessionRecord) error) error {
	if err := t.Poll(consume); err != nil {
		return err
	}
	if t.identity == nil {
		return errors.New("declared session artifact never appeared")
	}
	if t.offset != len(t.prefix) {
		return errors.New("session artifact ended with an incomplete record")
	}
	return nil
}
