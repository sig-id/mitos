package studio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mitos/internal/decisionmodel"
	"os"
	"path/filepath"
)

type decisionCredential struct {
	Endpoint string `json:"endpoint"`
	Key      string `json:"key"`
}

// saveDecisionCredential stores keys separately from the event log and plans.
// Immutable references keep existing execution plans valid after key rotation.
func (s *Store) saveDecisionCredential(config *decisionmodel.Config) (*decisionmodel.Config, error) {
	if config == nil {
		return nil, nil
	}
	c := *config
	if c.APIKeyRef != "" {
		if _, err := s.decisionCredential(c); err != nil {
			return nil, err
		}
	}
	if c.APIKey == "" {
		return &c, nil
	}
	dir := filepath.Join(s.dir, "credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("cannot store API key")
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, errors.New("cannot store API key")
	}
	ref := hex.EncodeToString(token[:])
	data, err := json.Marshal(decisionCredential{Endpoint: c.Endpoint, Key: c.APIKey})
	if err != nil {
		return nil, errors.New("cannot store API key")
	}
	path := filepath.Join(dir, ref)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("cannot store API key")
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = syncDirectory(dir)
	}
	if err == nil {
		err = syncDirectory(s.dir)
	}
	if err != nil {
		_ = os.Remove(path)
		return nil, errors.New("cannot store API key")
	}
	c.APIKey, c.APIKeyRef = "", ref
	return &c, nil
}

func (s *Store) decisionCredential(c decisionmodel.Config) (string, error) {
	// Validate before using a client-supplied reference as a filename.
	if err := c.Validate(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "credentials", c.APIKeyRef))
	var saved decisionCredential
	if err != nil || json.Unmarshal(data, &saved) != nil || saved.Key == "" || saved.Endpoint != c.Endpoint {
		return "", errors.New("saved API key unavailable for this endpoint; enter the key again")
	}
	return saved.Key, nil
}

// Decide resolves saved keys only for the outbound call, never into saved state.
func (e *Engine) Decide(ctx context.Context, c decisionmodel.Config, request decisionmodel.Request) (decisionmodel.Answer, error) {
	if err := c.Validate(); err != nil {
		return decisionmodel.Answer{}, err
	}
	if c.APIKeyRef != "" {
		key, err := e.Store.decisionCredential(c)
		if err != nil {
			return decisionmodel.Answer{}, err
		}
		c.APIKeyRef, c.APIKey = "", key
	}
	return decisionmodel.Decide(ctx, c, request)
}
