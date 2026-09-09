package studio

// SeedDemo adds explicitly labelled sample history only to an empty store.
// Sample rows never dispatch commands or claim that files were generated.
func SeedDemo(s *Store) error {
	if len(s.List()) > 0 {
		return nil
	}
	samples := []struct{ title, app, summary, status string }{
		{"Map the payment service", "Workspace inventory", "Example outcome: a workspace inventory with a reproducible content fingerprint.", "done"},
		{"Document the onboarding flow", "Documentation", "Example outcome: onboarding notes and acceptance evidence, ready for review.", "done"},
		{"Tidy the component library", "Workspace maintenance", "Example outcome: component cleanup completed and acceptance checks passed.", "done"},
		{"Prepare the release notes", "Release preparation", "Example work queued for its next execution slot.", "queued"},
		{"Finish the settings migration", "Workspace maintenance", "Example interruption: the previous run stopped before its result was checked.", "parked"},
	}
	for _, v := range samples {
		r, err := s.Create(Brief{Title: v.title, Intent: "Illustrative brief. Start new work with a real workspace to create an actual execution record.", Workspace: ".", AppID: "workspace-inventory", TimeoutMinutes: 10}, v.app, "", true)
		if err != nil {
			return err
		}
		if v.status == "queued" {
			continue
		}
		if err = s.Transition(r.ID, "run_started", "running", "Working", v.summary); err != nil {
			return err
		}
		if v.status == "parked" {
			if err = s.Park(r.ID, recoveryDecision()); err != nil {
				return err
			}
		} else {
			if err = s.Transition(r.ID, "verification_recorded", "running", "Example acceptance passed", v.summary); err != nil {
				return err
			}
			if err = s.Complete(r.ID, v.summary, true); err != nil {
				return err
			}
		}
	}
	return nil
}
