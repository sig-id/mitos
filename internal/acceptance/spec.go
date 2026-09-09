// Package acceptance defines machine-checkable predicates and their evidence.
package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"mitos/internal/workspace"
)

type Spec struct {
	Cmd         []string `json:"cmd"`
	OraclePaths []string `json:"oracle_paths,omitempty"`
	TimeoutMS   int64    `json:"timeout_ms,omitempty"`
	Measure     []string `json:"measure,omitempty"`
}

// Legacy local apps used an argv array. Reading it remains supported; writing
// always emits the canonical acceptance object.
func (s *Spec) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '[' {
		var cmd []string
		if err := json.Unmarshal(data, &cmd); err != nil {
			return err
		}
		*s = Spec{Cmd: cmd}
		return nil
	}
	type plain Spec
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("acceptance has trailing JSON")
	}
	*s = Spec(value)
	return nil
}
func (s Spec) Validate() error {
	if len(s.Cmd) == 0 || !filepath.IsAbs(s.Cmd[0]) {
		return errors.New("acceptance.cmd requires an absolute executable path")
	}
	if s.TimeoutMS < 0 || s.TimeoutMS > 120*60*1000 {
		return errors.New("acceptance.timeout_ms must be between 1 and 7200000, or omitted for 60000")
	}
	if len(s.Measure) > 0 && !filepath.IsAbs(s.Measure[0]) {
		return errors.New("acceptance.measure requires an absolute executable path")
	}
	for _, argv := range [][]string{s.Cmd, s.Measure} {
		for _, arg := range argv {
			if strings.ContainsRune(arg, 0) {
				return errors.New("acceptance command contains NUL")
			}
		}
	}
	seen := map[string]bool{}
	for _, path := range s.OraclePaths {
		clean := filepath.ToSlash(filepath.Clean(path))
		if !filepath.IsLocal(path) || !utf8.ValidString(path) || strings.ContainsRune(path, 0) || clean != strings.TrimSuffix(path, "/") || seen[clean] {
			return fmt.Errorf("invalid or duplicate acceptance oracle path %q", path)
		}
		seen[clean] = true
	}
	return nil
}
func (s Spec) Timeout() time.Duration {
	if s.TimeoutMS == 0 {
		return time.Minute
	}
	return time.Duration(s.TimeoutMS) * time.Millisecond
}

func (s Spec) Touched(changes []workspace.Change) []string {
	touched := []string{}
	for _, change := range changes {
		for _, oracle := range s.OraclePaths {
			oracle = strings.TrimSuffix(oracle, "/")
			if oracle == "." || change.Path == oracle || strings.HasPrefix(change.Path, oracle+"/") {
				touched = append(touched, change.Path)
				break
			}
		}
	}
	return touched
}

func Measurement(data []byte) ([]float64, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("measurement must contain one JSON scalar or vector")
	}
	switch value := value.(type) {
	case float64:
		return []float64{value}, nil
	case []any:
		if len(value) == 0 || len(value) > 1024 {
			return nil, errors.New("measurement vector must contain 1 to 1024 numbers")
		}
		result := make([]float64, len(value))
		for i, item := range value {
			number, ok := item.(float64)
			if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, errors.New("measurement vector contains a non-finite number")
			}
			result[i] = number
		}
		return result, nil
	default:
		return nil, errors.New("measurement must be a JSON number or numeric vector")
	}
}
func Delta(before, after []float64) ([]float64, error) {
	if len(before) != len(after) {
		return nil, errors.New("measurement dimensions changed")
	}
	delta := make([]float64, len(before))
	for i := range before {
		delta[i] = after[i] - before[i]
		if math.IsInf(delta[i], 0) || math.IsNaN(delta[i]) {
			return nil, errors.New("measurement delta is not finite")
		}
	}
	return delta, nil
}

type Verdict struct {
	Verdict         string    `json:"verdict"`
	WorkspaceCommit string    `json:"workspace_commit"`
	EvidenceRef     string    `json:"evidence_ref"`
	BaselineMeasure []float64 `json:"baseline_measure,omitempty"`
	Measure         []float64 `json:"measure,omitempty"`
	Delta           []float64 `json:"delta,omitempty"`
	OracleIsolation string    `json:"oracle_isolation"`
}
