package studio

import (
	"fmt"
	"mitos/internal/compute"
	"mitos/internal/resourcepolicy"
)

func workflowResourceEnforcement() resourcepolicy.Enforcement {
	caps := (&compute.Local{}).Capabilities()
	// Native agent credentials bypass a reserving model gateway. Reported token
	// usage is telemetry, so it cannot justify a hard workflow token budget.
	return resourcepolicy.Enforcement{Elapsed: true, Attempts: true, CPU: caps.CPUQuota, Memory: caps.MemoryLimit}
}
func preferenceResources(v RuntimePreferences) resourcepolicy.Policy {
	var p resourcepolicy.Policy
	if v.Resources != nil {
		p = *v.Resources
	}
	if p.Workflow.ElapsedMinutes.Inherits() && v.Minutes != 0 {
		p.Workflow.ElapsedMinutes = resourcepolicy.Capped(int64(v.Minutes))
	}
	if p.Workflow.Attempts.Inherits() && v.Attempts != 0 {
		p.Workflow.Attempts = resourcepolicy.Capped(int64(v.Attempts))
	}
	return p
}
func validateResourcePreferences(v RuntimePreferences) error {
	if v.Resources != nil {
		if err := v.Resources.Validate(); err != nil {
			return err
		}
		for _, pair := range []struct {
			name   string
			legacy int
			limit  resourcepolicy.Limit
		}{{"minutes", v.Minutes, v.Resources.Workflow.ElapsedMinutes}, {"attempts", v.Attempts, v.Resources.Workflow.Attempts}} {
			if pair.legacy != 0 && !pair.limit.Inherits() && pair.limit != resourcepolicy.Capped(int64(pair.legacy)) {
				return fmt.Errorf("%s conflicts with resources; use one value for this budget", pair.name)
			}
		}
	}
	return preferenceResources(v).Check(workflowResourceEnforcement())
}
