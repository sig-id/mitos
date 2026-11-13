package web

import (
	"mitos/internal/decisionmodel"
	"net/http"
)

func (s *Server) testDecisionProvider(w http.ResponseWriter, r *http.Request) {
	if _, err := s.engine.Desk.Project(r.PathValue("project")); err != nil {
		graphFailure(w, err)
		return
	}
	var in struct {
		Config decisionmodel.Config `json:"config"`
	}
	if !graphBody(w, r, &in) {
		return
	}
	in.Config.Instructions = "Connectivity test. Choose connected. No action will be executed."
	answer, err := decisionmodel.Decide(r.Context(), in.Config, decisionmodel.Request{ID: "operator-connection-test", State: []byte(`{"purpose":"connection test"}`), Choices: []decisionmodel.Choice{{Value: "connected", Description: "The API connection works."}, {Value: "unavailable", Description: "The API connection is unavailable."}}})
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "Connection test failed. Check the endpoint, model and server credential environment variable."})
		return
	}
	message := "Connected. The provider returned a valid decision."
	if !answer.Meets(in.Config) || answer.Choice != "connected" {
		message = "Connected, but the test decision did not meet your confidence thresholds."
	}
	writeJSON(w, 200, map[string]string{"message": message})
}
