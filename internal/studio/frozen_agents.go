package studio

import (
	"errors"
	"fmt"
	"mitos/agents"
)

// Definition bodies used by headless work are part of the admission event, not
// a reference to whichever bundled library happens to ship after a restart.
func (s *Store) freezeStructuredAgent(r *Run) error {
	if r.Brief.Structured == nil || r.Brief.Structured.Frozen == nil {
		return nil
	}
	d := r.Brief.Structured.Frozen
	if err := d.Verify(); err != nil {
		return err
	}
	ref := fmt.Sprintf("%s@%d", d.ID, d.Revision)
	if ref != r.Brief.Structured.Definition || d.Digest != r.Brief.Structured.Digest {
		return errors.New("frozen structured agent does not match admission")
	}
	current := s.frozenAgents.Load()
	if current != nil {
		if prior, ok := (*current)[ref]; ok {
			if prior.Digest != d.Digest {
				return errors.New("immutable agent revision changed")
			}
			return nil
		}
	}
	next := map[string]agents.Definition{}
	if current != nil {
		for key, value := range *current {
			next[key] = value
		}
	}
	next[ref] = graphClone(*d)
	s.frozenAgents.Store(&next)
	return nil
}
