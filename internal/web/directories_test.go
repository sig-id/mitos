package web

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestFolderBrowserAndSuggestedProjectName(t *testing.T) {
	server, engine := testServer(t)
	for _, name := range []string{"alpha", "Space & <name>", ".hidden"} {
		if err := os.Mkdir(filepath.Join(engine.Root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(engine.Root, "alpha"), filepath.Join(engine.Root, "linked")); err != nil {
		t.Fatal(err)
	}
	var listing struct {
		Path, Name, Parent string
		Directories        []directoryEntry
	}
	response := request(server, "GET", "/api/directories", "", false)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &listing) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	if listing.Path != engine.Root || len(listing.Directories) != 3 {
		t.Fatal("unexpected folder listing", listing)
	}
	for _, entry := range listing.Directories {
		if entry.Name == "hello.txt" || entry.Name == ".hidden" {
			t.Fatal("file or hidden folder exposed", entry)
		}
	}
	response = request(server, "GET", "/api/directories?hidden=true&path="+url.QueryEscape(engine.Root), "", false)
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &listing) != nil || len(listing.Directories) != 4 {
		t.Fatal(response.Body.String())
	}
	if response = request(server, "GET", "/api/directories?path="+url.QueryEscape(filepath.Join(engine.Root, "hello.txt")), "", false); response.Code != 400 {
		t.Fatal("file accepted as folder")
	}
	project, err := engine.Desk.CreateProject("", filepath.Join(engine.Root, "Space & <name>")+"/")
	if err != nil || project.Name != "Space & <name>" {
		t.Fatal(project, err)
	}
}
