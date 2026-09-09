package studio

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"
)

type ServiceClock struct {
	ServiceID string    `json:"service_id"`
	Epoch     int64     `json:"schedule_epoch"`
	NextAt    time.Time `json:"next_at"`
}

// Newly calculated instants are saved with the catalog. Replay does not depend
// on a future host's timezone database to reconstruct the initial cron clock.
func (s *Store) planServiceClocks(c ServiceCatalog, now time.Time) ([]ServiceClock, error) {
	old := activeServiceUnits(s.services)
	active := activeServiceUnits(&c)
	clocks := []ServiceClock{}
	for id, unit := range active {
		if unit.Schedule == nil || !unit.Schedule.Timed() {
			continue
		}
		clock := ServiceClock{ServiceID: id, Epoch: s.seq + 1}
		previous, wasActive := old[id]
		state := s.serviceStates[id]
		if wasActive && state != nil && state.NextAt != nil && reflect.DeepEqual(previous.Schedule, unit.Schedule) {
			clock.Epoch = state.Epoch
			clock.NextAt = *state.NextAt
		} else {
			next, err := unit.Schedule.Next(now, id, clock.Epoch)
			if err != nil {
				return nil, fmt.Errorf("service %s: %w", id, err)
			}
			if unit.Schedule.RunOnStartup {
				next = now
			}
			clock.NextAt = next
		}
		clocks = append(clocks, clock)
	}
	sort.Slice(clocks, func(i, j int) bool { return clocks[i].ServiceID < clocks[j].ServiceID })
	return clocks, nil
}
func validateServiceClocks(e Event, c change) error {
	active := activeServiceUnits(c.Services)
	if c.ServiceClocks == nil {
		// Catalogs predating clock records only supported interval/manual triggers.
		for _, unit := range active {
			if unit.Trigger == "cron" {
				return errors.New("cron catalog is missing saved clocks")
			}
		}
		return nil
	}
	if len(c.ServiceClocks) > 256 {
		return errors.New("too many service clocks")
	}
	seen := map[string]bool{}
	for _, clock := range c.ServiceClocks {
		unit, exists := active[clock.ServiceID]
		if !exists || unit.Schedule == nil || !unit.Schedule.Timed() || seen[clock.ServiceID] || clock.Epoch < 1 || clock.Epoch > e.Seq || clock.NextAt.IsZero() {
			return errors.New("invalid saved service clock")
		}
		seen[clock.ServiceID] = true
	}
	for id, unit := range active {
		if unit.Schedule != nil && unit.Schedule.Timed() && !seen[id] {
			return errors.New("active schedule has no saved clock")
		}
	}
	return nil
}
