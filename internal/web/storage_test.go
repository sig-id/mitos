package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mitos/internal/studio"
)

func TestStorageBackupUsesLiveStoreAndRestoreVerifiesIt(t *testing.T) {
	s, e := testServer(t)
	destination := filepath.Join(t.TempDir(), "backup")
	body, _ := json.Marshal(map[string]string{"directory": destination})
	response := request(s, "POST", "/api/storage/backup", string(body), false)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(destination, "runtime.db")); err != nil {
		t.Fatal(err)
	}
	// The live source remains usable after an online backup.
	if _, err := e.Store.Create(studio.Brief{Title: "A later task", Workspace: ".", AppID: "workspace-inventory", TimeoutMinutes: 1}, "Inventory", "", false); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := studio.RestoreStore(destination, restored); err != nil {
		t.Fatal(err)
	}
	store, err := studio.OpenStore(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if len(store.List()) != 0 {
		t.Fatal("backup included later admissions")
	}
	if response = request(s, "POST", "/api/storage/backup", string(body), false); response.Code != 422 {
		t.Fatal("overwrote backup", response.Code)
	}
}
