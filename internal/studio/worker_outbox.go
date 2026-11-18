package studio

import (
	"context"
	"errors"
	"sort"
	"time"

	"mitos/internal/messaging"
	"mitos/internal/workerwake"
)

func (s *Store) PendingWorkerOutbox(limit int) []WorkerOutbox {
	if limit < 1 || limit > 200 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]WorkerOutbox, 0, limit)
	for _, item := range s.workerOutbox {
		at := sort.Search(len(out), func(i int) bool { return out[i].Seq > item.Seq || out[i].Seq == item.Seq && out[i].ID > item.ID })
		if at >= limit {
			continue
		}
		out = append(out, WorkerOutbox{})
		copy(out[at+1:], out[at:])
		out[at] = item
		if len(out) > limit {
			out = out[:limit]
		}
	}
	return out
}
func (s *Store) ackWorkerOutbox(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.workerOutbox[id]
	if !ok {
		return nil
	}
	item.Published = true
	return s.commit("", "effect", "worker_wake_published", change{Worker: &workerChange{Outbox: &item}})
}
func (e *Engine) StartWorkerWake(cfg messaging.Config) error {
	if e.workerWake != nil {
		return errors.New("worker wake already started")
	}
	client, err := workerwake.New(e.ctx, cfg, e.Store.Home().ID, "")
	if err != nil {
		return err
	}
	e.workerWake = client
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer client.Close()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-tick.C:
				for _, item := range e.Store.PendingWorkerOutbox(50) {
					ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
					err := client.Publish(ctx, workerwake.Hint{Pool: item.Pool, Capability: item.Capability})
					cancel()
					if err != nil {
						break
					}
					if err = e.Store.ackWorkerOutbox(item.ID); err != nil {
						break
					}
				}
			}
		}
	}()
	return nil
}
