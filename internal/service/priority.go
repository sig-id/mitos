package service

// PriorityAdjustment is the fixed v1 mapping from service priority names to
// ready-queue score points. Keep historical values stable when adding policies.
func PriorityAdjustment(priority string) (int, bool) {
	switch priority {
	case "low":
		return -100, true
	case "", "normal":
		return 0, true
	case "high":
		return 100, true
	case "critical":
		return 200, true
	default:
		return 0, false
	}
}
