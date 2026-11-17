package studio

import "errors"

// journal is the persistence boundary, not a file abstraction. Append owns its
// durability barrier; Store publishes events only after it returns successfully.
// Transactional adapters commit the entire event/projection batch or none of it.
// Replay visits the canonical chain in sequence without performing external work.
// Callers serialize Replay, Append and Close through the Store's writer gate.
type journal interface {
	Replay(func(Event) error) error
	Append(events []Event, projections []journalProjection) error
	Close() error
}

// journalProjection is a rebuildable index update in the same transaction as its
// events. A nil Value deletes the key; a non-nil empty Value is a stored value.
// Names identify runtime-owned indexes, never arbitrary caller-selected tables.
// The Store still folds all lifecycle semantics through apply, not through indexes.
type journalProjection struct {
	Namespace string
	Key       string
	Value     []byte
}

var errJournalClosed = errors.New("journal closed")

// validateJournalEvent is shared by adapters and Store replay. Validate the
// canonical Event bytes before apply can change any in-memory state.
func validateJournalEvent(e Event, seq int64, previous string) error {
	if e.Seq != seq+1 || e.Previous != previous || e.Hash != eventHash(e) {
		return errors.New("event chain damaged")
	}
	return nil
}
