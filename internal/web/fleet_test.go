package web

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/internal/fleet"
	"mitos/internal/studio"
)

func TestFleetSurfacesAndExplicitClaimInspection(t *testing.T) {
	server, engine := testServer(t)
	// Stop dispatch while constructing the persisted state of an uncertain run.
	engine.Close()
	b := studio.Brief{Title: "Previous execution", AppID: "workspace-inventory", Workspace: engine.Root, TimeoutMinutes: 1, Ownership: fleet.Ownership{Paths: []string{"."}}}
	r, err := engine.Store.Create(b, "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := engine.Store.AcquireClaims(r.ID); err != nil || !ok {
		t.Fatal(err)
	}
	if err = engine.Store.Transition(r.ID, "run_cancelled", "cancelled", "Stopped", "private narration"); err != nil {
		t.Fatal(err)
	}
	api := request(server, "GET", "/api/runs/"+r.ID+"/evidence", "", false)
	var e fleet.Evidence
	if api.Code != 200 || json.Unmarshal(api.Body.Bytes(), &e) != nil || e.Run.RunID != r.ID || !e.Run.ClaimHeld || strings.Contains(api.Body.String(), "private narration") {
		t.Fatal("invalid evidence API")
	}
	expected, _ := engine.Store.FleetEvidence(r.ID)
	if e.Digest != expected.Digest {
		t.Fatal("API used a different reader")
	}
	index := request(server, "GET", "/api/fleet", "", false)
	if index.Code != 200 || !strings.Contains(index.Body.String(), r.ID) {
		t.Fatal("fleet index unavailable")
	}
	page := request(server, "GET", "/runs/"+r.ID, "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "I inspected and stopped") {
		t.Fatal("inspection form unavailable")
	}
	endpoint := "/api/runs/" + r.ID + "/claims/release"
	for _, body := range []string{`{}`, `{"confirmed_stopped":false}`, `{"confirmed_stopped":true,"extra":1}`, `{"confirmed_stopped":true} {}`} {
		res := request(server, "POST", endpoint, body, true)
		if res.Code != 400 {
			t.Fatalf("accepted %s: %d %s", body, res.Code, res.Body.String())
		}
	}
	res := request(server, "POST", endpoint, `{"confirmed_stopped":true}`, true)
	if res.Code != 200 {
		t.Fatalf("release failed: %s", res.Body.String())
	}
	after, _ := engine.Store.Get(r.ID)
	if after.Claim.ReleasedAt == nil || after.Claim.ReleaseReason != "operator_confirmed_stopped" {
		t.Fatal("confirmation not persisted")
	}
	missing := request(server, "GET", "/api/runs/missing/evidence", "", false)
	if missing.Code != 404 {
		t.Fatal("missing evidence status")
	}
}
