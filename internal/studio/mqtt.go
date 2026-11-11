package studio

import (
	"errors"
	"mitos/internal/messaging"
)

// StartMessaging is configured once, before serving requests. The broker can be
// offline at startup; pending journal entries are reconciled after reconnect.
func (e *Engine) StartMessaging(cfg messaging.Config) error {
	if e.Messages != nil {
		return errors.New("MQTT messaging already started")
	}
	key, err := messaging.LoadIdentity(e.Store.Dir())
	if err != nil {
		return err
	}
	e.Messages, err = messaging.New(e.ctx, cfg, e.Store.Home().ID, key, e.Store)
	return err
}
