package web

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/internal/studio"
)

func TestAutonomySurfacesDoNotTurnExamplesOrMissingDataIntoSuccess(t *testing.T) {
	server, engine := testServer(t)
	if err := studio.SeedDemo(engine.Store); err != nil {
		t.Fatal(err)
	}
	api := request(server, "GET", "/api/autonomy", "", false)
	var report studio.AutonomyReport
	if api.Code != 200 || json.Unmarshal(api.Body.Bytes(), &report) != nil {
		t.Fatal("metrics API unavailable")
	}
	if report.Runs != 0 || report.SampleRunsExcluded != 5 || report.GreenRate != nil || report.AutonomyRatioMS != nil {
		t.Fatal("examples manufactured measured success")
	}
	page := request(server, "GET", "/autonomy", "", false)
	if page.Code != 200 {
		t.Fatalf("metrics page failed: %s", page.Body.String())
	}
	for _, text := range []string{"Autonomy evidence", "Not measured", "5 examples excluded", "Permission to run unattended"} {
		if !strings.Contains(page.Body.String(), text) {
			t.Fatalf("missing %s", text)
		}
	}
	if strings.Contains(page.Body.String(), "%!f") {
		t.Fatal("nullable metric did not render")
	}
}
