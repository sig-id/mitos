package observation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTailPartialRowsOffsetsAndDurableRetry(t *testing.T) {
	base := t.TempDir()
	tail, err := NewTail(base, "session.jsonl", JSONLNormalizer{})
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()
	var records []SessionRecord
	consume := func(r SessionRecord) error { records = append(records, r); return nil }
	if err = tail.Poll(consume); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(base, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	first := "{\"type\":\"status\",\"status\":\"working\"}\n"
	f.WriteString(first + "{\"type\":\"assistant_text\"")
	if err = tail.Poll(consume); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Offset != 0 || records[0].EndOffset != int64(len(first)) {
		t.Fatalf("partial record consumed: %+v", records)
	}
	f.WriteString(",\"text\":\"hello\"}\n")
	failure := errors.New("fsync failed")
	if err = tail.Poll(func(SessionRecord) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err = tail.Poll(consume); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].Offset != int64(len(first)) || records[1].Events[0].Text != "hello" {
		t.Fatalf("lost record after failed consumer: %+v", records)
	}
	if err = tail.Finish(consume); err != nil || len(records) != 2 {
		t.Fatalf("repeated records: %v %v", records, err)
	}
}

func TestTailDetectsContractFailures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(string)
	}{
		{"rewrite", func(p string) { os.WriteFile(p, []byte("{\"type\":\"status\",\"status\":\"changed\"}\n"), 0600) }},
		{"truncate", func(p string) { os.Truncate(p, 0) }},
		{"replace", func(p string) {
			os.Remove(p)
			os.WriteFile(p, []byte("{\"type\":\"status\",\"status\":\"working\"}\n"), 0600)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			p := filepath.Join(base, "session.jsonl")
			os.WriteFile(p, []byte("{\"type\":\"status\",\"status\":\"working\"}\n"), 0600)
			tail, err := NewTail(base, "session.jsonl", JSONLNormalizer{})
			if err != nil {
				t.Fatal(err)
			}
			defer tail.Close()
			consume := func(SessionRecord) error { return nil }
			if err = tail.Poll(consume); err != nil {
				t.Fatal(err)
			}
			tc.mutate(p)
			if err = tail.Poll(consume); err == nil {
				t.Fatal("changed source accepted")
			}
		})
	}
}

func TestTailRefusesEscapeMalformedAndMissingArtifacts(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "private")
	os.WriteFile(outside, []byte("{\"type\":\"final_result\"}\n"), 0600)
	os.Symlink(outside, filepath.Join(base, "escape"))
	for _, path := range []string{"escape", "missing"} {
		tail, err := NewTail(base, path, JSONLNormalizer{})
		if err != nil {
			t.Fatal(err)
		}
		if err = tail.Finish(func(SessionRecord) error { return nil }); err == nil {
			t.Fatal("unsafe or absent artifact accepted")
		}
		tail.Close()
	}
	for _, row := range []string{"{\"type\":\"unknown\"}\n", "{\"type\":\"token_usage\",\"input_tokens\":-1}\n", "{\"type\":\"final_result\",\"authority\":\"finish\"}\n", "{\"type\":\"status\"}", "{\"type\":\"final_result\"} {}\n"} {
		os.WriteFile(filepath.Join(base, "bad"), []byte(row), 0600)
		tail, _ := NewTail(base, "bad", JSONLNormalizer{})
		if err := tail.Finish(func(SessionRecord) error { return nil }); err == nil {
			t.Fatalf("invalid source accepted: %s", row)
		}
		tail.Close()
	}
}
