package agents

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBundledLibraryIsValidAndComplete(t *testing.T) {
	library, err := Library()
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, d := range library {
		if err := d.Verify(); err != nil {
			t.Fatal(err)
		}
		listed[d.ID] = true
	}
	// Every packaged definition must be indexed; an unlisted package is dead content.
	packages, err := fs.Glob(bundle, "defaults/*/agent.json")
	if err != nil || len(packages) != len(library) {
		t.Fatalf("indexed %d of %d packages: %v", len(library), len(packages), err)
	}
	for _, id := range []string{"mitos.generalist", "mitos.orchestrator"} {
		if !listed[id] {
			t.Fatalf("bootstrap definition %s missing", id)
		}
	}
}

func bundled(t *testing.T, id string) ([]byte, []byte) {
	t.Helper()
	dir := "defaults/" + strings.TrimPrefix(id, "mitos.")
	raw, err := fs.ReadFile(bundle, dir+"/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	body, err := fs.ReadFile(bundle, dir+"/instructions.md")
	if err != nil {
		t.Fatal(err)
	}
	return raw, body
}

func mutate(t *testing.T, raw []byte, change func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	change(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestManifestRejectsInvalidDefinitions(t *testing.T) {
	raw, _ := bundled(t, "mitos.engineer")
	if _, err := ParseManifest(raw); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"duplicate key":   []byte(strings.Replace(string(raw), `"kind": "worker",`, `"kind": "worker", "kind": "orchestrator",`, 1)),
		"unknown field":   mutate(t, raw, func(d map[string]any) { d["shell"] = "rm -rf /" }),
		"missing field":   mutate(t, raw, func(d map[string]any) { delete(d, "limits") }),
		"null list":       mutate(t, raw, func(d map[string]any) { d["skills"] = nil }),
		"wrong profile":   mutate(t, raw, func(d map[string]any) { d["profile"] = "agent-definition/v2" }),
		"bad id":          mutate(t, raw, func(d map[string]any) { d["id"] = "Engineer" }),
		"overlap":         mutate(t, raw, func(d map[string]any) { d["capabilities"].(map[string]any)["optional"] = []any{"workspace.read"} }),
		"unbounded turns": mutate(t, raw, func(d map[string]any) { d["limits"].(map[string]any)["max_turns"] = 0 }),
		"unresolved skill": mutate(t, raw, func(d map[string]any) {
			d["skills"] = []any{map[string]any{"id": "x.y", "revision": 1, "sha256": strings.Repeat("a", 64), "required": true}}
		}),
		"generated without base": mutate(t, raw, func(d map[string]any) { d["provenance"] = map[string]any{"origin": "generated", "license": "MIT"} }),
		"trailing data":          append(append([]byte{}, raw...), []byte(" {}")...),
		"oversized":              []byte(`{"pad":"` + strings.Repeat("x", MaxManifestBytes) + `"}`),
	}
	for name, input := range cases {
		if _, err := ParseManifest(input); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestDefinitionBindsInstructionsAndCanonicalDigest(t *testing.T) {
	raw, body := bundled(t, "mitos.researcher")
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDefinition(m, body)
	if err != nil {
		t.Fatal(err)
	}
	// Formatting does not change identity; content does.
	compact, _ := json.Marshal(json.RawMessage(raw))
	m2, err := ParseManifest(compact)
	if err != nil || m2.Digest() != d.Digest {
		t.Fatalf("digest depends on formatting: %v", err)
	}
	if _, err = NewDefinition(m, append(body, '\n')); err == nil {
		t.Fatal("instruction drift accepted")
	}
	d.Name = "Renamed"
	if d.Verify() == nil {
		t.Fatal("header drift accepted")
	}
}

func TestBundleRejectsEscapesAndMismatches(t *testing.T) {
	raw, body := bundled(t, "mitos.writer")
	index := func(path string, revision int) []byte {
		return []byte(`{"profile":"agent-library-bundle/v1","id":"t","revision":1,"agents":[{"id":"mitos.writer","revision":` + map[bool]string{true: "2", false: "3"}[revision == 1] + `,"path":"` + path + `"}]}`)
	}
	good := fstest.MapFS{"b/library.json": {Data: index("writer/agent.json", 1)}, "b/writer/agent.json": {Data: raw}, "b/writer/instructions.md": {Data: body}}
	if _, err := loadBundle(good, "b"); err != nil {
		t.Fatal(err)
	}
	for name, fsys := range map[string]fstest.MapFS{
		"escape":       {"b/library.json": {Data: index("../writer/agent.json", 1)}, "writer/agent.json": {Data: raw}, "writer/instructions.md": {Data: body}},
		"revision":     {"b/library.json": {Data: index("writer/agent.json", 2)}, "b/writer/agent.json": {Data: raw}, "b/writer/instructions.md": {Data: body}},
		"instructions": {"b/library.json": {Data: index("writer/agent.json", 1)}, "b/writer/agent.json": {Data: raw}, "b/writer/instructions.md": {Data: []byte("changed")}},
		"missing":      {"b/library.json": {Data: index("writer/agent.json", 1)}, "b/writer/agent.json": {Data: raw}},
	} {
		if _, err := loadBundle(fsys, "b"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
