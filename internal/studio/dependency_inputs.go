package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"mitos/internal/workpool"
	workspacefs "mitos/internal/workspace"
)

const dependencyBytes = 64 << 20
const dependencyFiles = 128

var errDependencyUnavailable = errors.New("dependency_input_unavailable")
var errDependencyChanged = errors.New("dependency_inputs_changed")

type DependencyInput struct {
	TaskID    string              `json:"task_id"`
	RunID     string              `json:"run_id"`
	Directory string              `json:"directory"`
	Artifacts []workpool.Artifact `json:"artifacts"`
}

// Caller holds the store lock. Bind only the chosen race/quorum witnesses,
// never all declared upstreams or a newer attempt discovered later.
func (s *Store) bindDependencyInputs(t *Task, witnesses []workpool.Witness) ([]workpool.Witness, error) {
	out := append([]workpool.Witness(nil), witnesses...)
	var total int64
	count := 0
	for i, w := range out {
		var dep *workpool.Dependency
		for j := range t.Scheduling.Deps {
			if t.Scheduling.Deps[j].From == w.TaskID {
				dep = &t.Scheduling.Deps[j]
				break
			}
		}
		if dep == nil || dep.Payload != "data" {
			continue
		}
		upstream := s.runs[w.RunID]
		if upstream == nil || upstream.TaskID != w.TaskID || runTenant(upstream) != t.Tenant || upstream.Sample {
			return nil, errors.New("invalid dependency producer")
		}
		refs := []workpool.Artifact{}
		var size int64
		problem := ""
		for _, name := range dep.Artifacts {
			found := false
			for _, a := range upstream.Artifacts {
				if a.Name == name {
					found = true
					if a.Bytes < 0 || a.Bytes > dependencyBytes || len(a.SHA256) != 64 {
						problem = "artifact_size_or_hash_invalid"
						break
					}
					refs = append(refs, workpool.Artifact{Name: a.Name, SHA256: a.SHA256, Bytes: a.Bytes})
					size += a.Bytes
					break
				}
			}
			if !found {
				problem = "artifact_missing"
			}
			if problem != "" {
				break
			}
		}
		if problem == "" && (total+size > dependencyBytes || count+len(refs) > dependencyFiles) {
			problem = "input_limit_exceeded"
		}
		if problem != "" {
			if dep.Strength == "soft" {
				out[i].InputOmitted = problem
				continue
			}
			return nil, fmt.Errorf("dependency %s: %s", w.TaskID, problem)
		}
		out[i].Artifacts = refs
		total += size
		count += len(refs)
	}
	return out, nil
}

// Read through a directory handle, reject non-regular files, and bound actual
// bytes as well as recorded size. No archive extraction or producer tree access.
func (s *Store) boundInput(w workpool.Witness, a workpool.Artifact) ([]byte, error) {
	if !workpool.ArtifactName(a.Name) || a.Bytes < 0 || a.Bytes > dependencyBytes {
		return nil, errors.New("invalid bound input")
	}
	producer, err := s.Get(w.RunID)
	if err != nil {
		return nil, err
	}
	matched := false
	for _, event := range producer.Events {
		if event.Seq == w.Seq && event.Hash == w.Hash {
			var c change
			if json.Unmarshal(event.Data, &c) == nil {
				if c.Task != nil && c.Task.ID == w.TaskID && c.Task.CurrentRunID == w.RunID && c.Task.Status == w.Status {
					matched = true
				}
				if c.Graph != nil {
					for _, task := range c.Graph.Tasks {
						if task.ID == w.TaskID && task.CurrentRunID == w.RunID && task.Status == w.Status {
							matched = true
						}
					}
				}
			}
			break
		}
	}
	if !matched {
		return nil, errors.New("dependency witness does not identify a recorded task result")
	}
	registered := false
	for _, ref := range producer.Artifacts {
		if ref.Name == a.Name && ref.Bytes == a.Bytes && ref.SHA256 == a.SHA256 {
			registered = true
			break
		}
	}
	if !registered {
		return nil, errors.New("dependency artifact registration changed")
	}
	root, err := os.OpenRoot(filepath.Join(s.dir, "artifacts", w.RunID))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.OpenFile(a.Name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != a.Bytes {
		return nil, errors.New("dependency artifact size/type mismatch")
	}
	data, err := io.ReadAll(io.LimitReader(file, a.Bytes+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != a.Bytes || hex.EncodeToString(sum[:]) != a.SHA256 {
		return nil, errors.New("dependency artifact checksum mismatch")
	}
	return data, nil
}

func (e *Engine) prepareDependencyInputs(ctx context.Context, r Run, base, prefix string) ([]DependencyInput, *workspacefs.Snapshot, error) {
	task, err := e.Store.Task(r.TaskID)
	if err != nil {
		return nil, nil, err
	}
	inputs := []DependencyInput{}
	for _, w := range task.DependencyEvidence {
		if len(w.Artifacts) > 0 {
			inputs = append(inputs, DependencyInput{TaskID: w.TaskID, RunID: w.RunID, Directory: filepath.Join(base, "inputs", w.TaskID), Artifacts: w.Artifacts})
		}
	}
	if len(inputs) == 0 {
		return inputs, nil, nil
	}
	staging, err := os.MkdirTemp(base, "inputs-staging-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(staging)
	for _, w := range task.DependencyEvidence {
		if len(w.Artifacts) == 0 {
			continue
		}
		if filepath.Base(w.TaskID) != w.TaskID || w.TaskID == "." || w.TaskID == ".." {
			return nil, nil, errors.New("invalid bound task identifier")
		}
		dir := filepath.Join(staging, w.TaskID)
		if err = os.Mkdir(dir, 0700); err != nil {
			return nil, nil, err
		}
		for _, a := range w.Artifacts {
			if err = ctx.Err(); err != nil {
				return nil, nil, err
			}
			data, readErr := e.Store.boundInput(w, a)
			if readErr != nil {
				return nil, nil, fmt.Errorf("dependency %s artifact %s: %w", w.TaskID, a.Name, readErr)
			}
			file, createErr := os.OpenFile(filepath.Join(dir, a.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0444)
			if createErr != nil {
				return nil, nil, createErr
			}
			_, writeErr := file.Write(data)
			if writeErr == nil {
				writeErr = file.Sync()
			}
			closeErr := file.Close()
			if writeErr != nil {
				return nil, nil, writeErr
			}
			if closeErr != nil {
				return nil, nil, closeErr
			}
		}
		if err = syncDirectory(dir); err != nil {
			return nil, nil, err
		}
	}
	snapshot, err := workspacefs.CaptureData(ctx, staging, workspacefs.DefaultLimits)
	if err != nil {
		return nil, nil, err
	}
	for i := range snapshot.Files {
		snapshot.Files[i].Data = nil
	}
	if err = syncDirectory(staging); err != nil {
		return nil, nil, err
	}
	target := filepath.Join(base, "inputs")
	if _, statErr := os.Lstat(target); statErr == nil {
		existing, captureErr := workspacefs.CaptureData(ctx, target, workspacefs.DefaultLimits)
		if captureErr != nil || existing.Fingerprint != snapshot.Fingerprint {
			return nil, nil, errDependencyChanged
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, nil, statErr
	} else if err = os.Rename(staging, target); err != nil {
		return nil, nil, err
	}
	if err = syncDirectory(base); err != nil {
		return nil, nil, err
	}
	manifest, err := json.MarshalIndent(inputs, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if err = e.Store.SaveArtifact(r.ID, prefix+"dependency-inputs.json", manifest); err != nil {
		return nil, nil, err
	}
	if err = e.Store.RecordEvidence(r.ID, "dependency_inputs_prepared", "Bound upstream artifacts were copied and verified before execution.", map[string]any{"fingerprint": snapshot.Fingerprint, "inputs": inputs}); err != nil {
		return nil, nil, err
	}
	return inputs, &snapshot, nil
}
func verifyDependencyInputs(ctx context.Context, base string, before *workspacefs.Snapshot) error {
	if before == nil {
		return nil
	}
	after, err := workspacefs.CaptureData(ctx, filepath.Join(base, "inputs"), workspacefs.DefaultLimits)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || after.Fingerprint != before.Fingerprint {
		return errDependencyChanged
	}
	return nil
}
func dependencyInputDecision() *HumanDecision {
	d := recoveryDecision()
	d.ReasonSignal = "dependency_inputs_changed"
	d.InterruptClass = "judgment"
	d.Question = "Review changed dependency inputs?"
	d.Context.BlockedBy = "Execution changed or removed its bound upstream input copies. The workspace patch is preserved and acceptance is withheld. Review the application before recovery."
	return d
}
