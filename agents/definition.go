package agents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"unicode/utf8"
)

const (
	Profile              = "agent-definition/v1"
	MaxManifestBytes     = 64 << 10
	MaxInstructionBytes  = 64 << 10
	maxText              = 4000
	maxList              = 32
	minContextCharacters = 1000
	maxContextCharacters = 200000
)

var (
	idPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[a-z][a-z0-9-]*$`)
	digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	// Fields a specialization may be allowed to change.
	specializable = []string{"name", "description", "instructions", "knowledge", "skills", "runtime.preferences", "runtime.requires", "context.optional", "context.required", "capabilities.required", "capabilities.optional", "limits"}
)

type Match struct {
	UseFor   []string `json:"use_for"`
	AvoidFor []string `json:"avoid_for"`
}
type InstructionsRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Runtime struct {
	Requires    []string `json:"requires"`
	Preferences []string `json:"preferences"`
}
type Resource struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	SHA256   string `json:"sha256"`
	Required bool   `json:"required"`
}
type Context struct {
	Required      []string `json:"required"`
	Optional      []string `json:"optional"`
	MaxCharacters int      `json:"max_characters"`
	Overflow      string   `json:"overflow"`
}
type Memory struct {
	Scope       string `json:"scope"`
	Publication string `json:"publication"`
}
type Capabilities struct {
	Required []string `json:"required"`
	Optional []string `json:"optional"`
}
type Limits struct {
	TimeoutSeconds int `json:"timeout_seconds"`
	MaxTurns       int `json:"max_turns"`
}
type Specialization struct {
	Allow                []string `json:"allow"`
	RequiredCapabilities string   `json:"required_capabilities"`
	OptionalCapabilities string   `json:"optional_capabilities"`
	MaxInstructionBytes  int      `json:"max_instruction_bytes"`
}
type Base struct {
	Scope    string `json:"scope"`
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	SHA256   string `json:"sha256"`
}
type Generation struct {
	GraphID   string `json:"graph_id"`
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	Rationale string `json:"rationale"`
}
type Provenance struct {
	Origin         string      `json:"origin"`
	SourceProject  string      `json:"source_project,omitempty"`
	SourceRevision string      `json:"source_revision,omitempty"`
	SourcePaths    []string    `json:"source_paths,omitempty"`
	License        string      `json:"license"`
	Base           *Base       `json:"base,omitempty"`
	Generation     *Generation `json:"generation,omitempty"`
}

// Manifest is the typed agent-definition/v1 authoring record. Field order is the
// canonical serialization order; Canonical() is the digest input.
type Manifest struct {
	Profile        string          `json:"profile"`
	ID             string          `json:"id"`
	Revision       int             `json:"revision"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Kind           string          `json:"kind"`
	Match          Match           `json:"match"`
	Instructions   InstructionsRef `json:"instructions"`
	Runtime        Runtime         `json:"runtime"`
	Skills         []Resource      `json:"skills"`
	Knowledge      []Resource      `json:"knowledge"`
	Context        Context         `json:"context"`
	Memory         Memory          `json:"memory"`
	Capabilities   Capabilities    `json:"capabilities"`
	Limits         Limits          `json:"limits"`
	Specialization Specialization  `json:"specialization"`
	Evidence       []string        `json:"evidence"`
	Provenance     Provenance      `json:"provenance"`
}

// ParseManifest decodes a manifest strictly: duplicate keys, unknown fields,
// trailing data and missing required members are refused before semantic checks.
func ParseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) > MaxManifestBytes {
		return m, errors.New("manifest exceeds 64 KiB")
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return m, err
	}
	if err := requireMembers(raw); err != nil {
		return m, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return m, errors.New("manifest has trailing data")
	}
	return m, m.Validate()
}

// Canonical returns the compact canonical encoding covered by the definition digest.
// Instruction bytes are bound through instructions.sha256.
func (m Manifest) Canonical() []byte {
	raw, err := json.Marshal(m)
	if err != nil {
		panic(err) // only plain data types; marshaling cannot fail
	}
	return raw
}

func (m Manifest) Digest() string {
	sum := sha256.Sum256(m.Canonical())
	return hex.EncodeToString(sum[:])
}

func (m Manifest) Validate() error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if m.Profile != Profile {
		fail("profile must be %s", Profile)
	}
	if len(m.ID) > 128 || !idPattern.MatchString(m.ID) {
		fail("id %q must be a namespaced lower-case identifier", m.ID)
	}
	if m.Revision < 1 {
		fail("revision must be positive")
	}
	text(fail, "name", m.Name)
	text(fail, "description", m.Description)
	if m.Kind != "worker" && m.Kind != "orchestrator" {
		fail("kind must be worker or orchestrator")
	}
	list(fail, "match.use_for", m.Match.UseFor)
	list(fail, "match.avoid_for", m.Match.AvoidFor)
	if m.Instructions.Path != "instructions.md" || !digestPattern.MatchString(m.Instructions.SHA256) {
		fail("instructions must reference instructions.md with a SHA-256 digest")
	}
	list(fail, "runtime.requires", m.Runtime.Requires)
	list(fail, "runtime.preferences", m.Runtime.Preferences)
	for name, refs := range map[string][]Resource{"skills": m.Skills, "knowledge": m.Knowledge} {
		if refs == nil {
			fail("%s is required", name)
		}
		// No skill or knowledge registry exists yet. A reference cannot be
		// resolved, so registration refuses it instead of silently dropping it.
		if len(refs) > 0 {
			fail("%s references are unsupported: no %s registry is available", name, name)
		}
	}
	list(fail, "context.required", m.Context.Required)
	list(fail, "context.optional", m.Context.Optional)
	disjoint(fail, "context", m.Context.Required, m.Context.Optional)
	if m.Context.MaxCharacters < minContextCharacters || m.Context.MaxCharacters > maxContextCharacters {
		fail("context.max_characters must be %d–%d", minContextCharacters, maxContextCharacters)
	}
	if !slices.Contains([]string{"omit_optional", "summarize_optional", "wait"}, m.Context.Overflow) {
		fail("context.overflow is unsupported")
	}
	if m.Memory.Scope != "attempt" && m.Memory.Scope != "assignment" {
		fail("memory.scope must be attempt or assignment")
	}
	if m.Memory.Publication != "none" && m.Memory.Publication != "propose" {
		fail("memory.publication must be none or propose")
	}
	list(fail, "capabilities.required", m.Capabilities.Required)
	list(fail, "capabilities.optional", m.Capabilities.Optional)
	disjoint(fail, "capabilities", m.Capabilities.Required, m.Capabilities.Optional)
	if m.Limits.TimeoutSeconds < 1 || m.Limits.TimeoutSeconds > 86400 || m.Limits.MaxTurns < 1 || m.Limits.MaxTurns > 1000 {
		fail("limits must be finite: timeout 1–86400 seconds, 1–1000 turns")
	}
	s := m.Specialization
	if s.Allow == nil || !unique(s.Allow) {
		fail("specialization.allow must be a unique list")
	}
	for _, field := range s.Allow {
		if !slices.Contains(specializable, field) {
			fail("specialization.allow has unknown field %q", field)
		}
	}
	if s.RequiredCapabilities != "retain" || s.OptionalCapabilities != "narrow" {
		fail("specialization must retain required and narrow optional capabilities")
	}
	if s.MaxInstructionBytes < 1 || s.MaxInstructionBytes > MaxInstructionBytes {
		fail("specialization.max_instruction_bytes must be 1–65536")
	}
	list(fail, "evidence", m.Evidence)
	p := m.Provenance
	switch p.Origin {
	case "adapted":
		if p.SourceProject == "" || p.SourceRevision == "" || len(p.SourcePaths) == 0 {
			fail("adapted provenance needs source project, revision and paths")
		}
	case "original":
	case "generated":
		if p.Base == nil || p.Generation == nil {
			fail("generated provenance needs base and generation")
		}
	default:
		fail("provenance.origin must be adapted, original or generated")
	}
	text(fail, "provenance.license", p.License)
	for _, v := range []string{p.SourceProject, p.SourceRevision} {
		if len(v) > maxText {
			fail("provenance text exceeds %d bytes", maxText)
		}
	}
	if p.SourcePaths != nil {
		list(fail, "provenance.source_paths", p.SourcePaths)
	}
	if b := p.Base; b != nil {
		if !slices.Contains([]string{"installation", "project", "graph"}, b.Scope) || b.ID == "" || b.Revision < 1 || !digestPattern.MatchString(b.SHA256) {
			fail("provenance.base must bind scope, id, revision and digest")
		}
	}
	if g := p.Generation; g != nil {
		for _, v := range []string{g.GraphID, g.TaskID, g.AttemptID, g.Rationale} {
			text(fail, "provenance.generation", v)
		}
	}
	return errors.Join(errs...)
}

// CheckInstructions verifies exact instruction bytes against the manifest.
func (m Manifest) CheckInstructions(body []byte) error {
	if len(body) == 0 || len(body) > MaxInstructionBytes || !utf8.Valid(body) {
		return errors.New("instructions must be non-empty UTF-8 within 64 KiB")
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != m.Instructions.SHA256 {
		return errors.New("instructions digest mismatch")
	}
	return nil
}

func text(fail func(string, ...any), field, v string) {
	if v == "" || len(v) > maxText {
		fail("%s must be 1–%d bytes", field, maxText)
	}
}
func list(fail func(string, ...any), field string, values []string) {
	if values == nil || len(values) > maxList || !unique(values) {
		fail("%s must be a unique list of at most %d entries", field, maxList)
		return
	}
	for _, v := range values {
		text(fail, field, v)
	}
}
func unique(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func disjoint(fail func(string, ...any), field string, a, b []string) {
	for _, v := range a {
		if slices.Contains(b, v) {
			fail("%s %q is both required and optional", field, v)
		}
	}
}

// requireMembers checks that every non-omitempty manifest member is present, so a
// missing list is not confused with an explicit empty one.
func requireMembers(raw []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	for _, key := range []string{"profile", "id", "revision", "name", "description", "kind", "match", "instructions", "runtime", "skills", "knowledge", "context", "memory", "capabilities", "limits", "specialization", "evidence", "provenance"} {
		if v, ok := top[key]; !ok || string(v) == "null" {
			return fmt.Errorf("manifest is missing %s", key)
		}
	}
	return nil
}

func rejectDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("manifest: %w", err)
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return fmt.Errorf("manifest: %w", err)
				}
				k := key.(string)
				if seen[k] {
					return fmt.Errorf("manifest has duplicate field %q", k)
				}
				seen[k] = true
				if err = walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case json.Delim('['):
			for dec.More() {
				if err = walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	return walk()
}
