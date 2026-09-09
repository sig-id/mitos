package studio

import (
	"errors"

	"mitos/internal/service"
)

// Saved at intake (event allocation for event subscriptions). Existing queue
// entries retain their score across configuration changes, retries, and replay.
type ServiceQueueBinding struct {
	ServiceID  string `json:"service_id"`
	Revision   int64  `json:"catalog_revision"`
	Priority   string `json:"priority"`
	Adjustment int    `json:"adjustment"`
}

func (s *Store) serviceQueueBinding(id string, revision int64) *ServiceQueueBinding {
	c := s.serviceConcurrency(id)
	adjustment, _ := service.PriorityAdjustment(c.Priority)
	return &ServiceQueueBinding{ServiceID: id, Revision: revision, Priority: c.Priority, Adjustment: adjustment}
}
func validateServiceQueue(b *ServiceQueueBinding) error {
	if b == nil {
		return nil
	} // Historical calls had no named priority adjustment.
	adjustment, ok := service.PriorityAdjustment(b.Priority)
	if !service.ValidID(b.ServiceID) || b.Revision < 1 || b.Priority == "" || !ok || b.Adjustment != adjustment {
		return errors.New("invalid saved service queue priority")
	}
	return nil
}
func taskServiceAdjustment(t *Task) int {
	if t.ServiceQueue == nil {
		return 0
	}
	return t.ServiceQueue.Adjustment
}
