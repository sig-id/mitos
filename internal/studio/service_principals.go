package studio

import (
	"errors"
	"slices"
	"sort"
	"time"

	"mitos/internal/decision"
)

var ErrUnauthenticated = errors.New("unauthenticated")
var ErrForbidden = errors.New("forbidden")

type ServicePrincipal struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Tenant         decision.Tenant `json:"tenant"`
	Read           bool            `json:"read"`
	Write          bool            `json:"write"`
	Agents         []string        `json:"agents"`
	ExpiresAt      time.Time       `json:"expires_at"`
	Revoked        bool            `json:"revoked"`
	Revision       string          `json:"revision"`
	AuthorityEpoch string          `json:"authority_epoch,omitempty"`
	TokenHash      string          `json:"token_hash,omitempty"`
}

type ServicePrincipalInput struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Tenant    decision.Tenant `json:"tenant"`
	Read      bool            `json:"read"`
	Write     bool            `json:"write"`
	Agents    []string        `json:"agents"`
	ExpiresAt time.Time       `json:"expires_at"`
}

func publicPrincipal(p ServicePrincipal) ServicePrincipal { p.TokenHash = ""; return p }
func localOperator(p decision.Principal) bool {
	return p.Authenticated && p.ID == decision.LocalOperator().ID && p.Tenant == decision.LocalTenant
}

func (s *Store) CreateServicePrincipal(in ServicePrincipalInput, operator decision.Principal) (ServicePrincipal, string, error) {
	if !localOperator(operator) {
		return ServicePrincipal{}, "", ErrForbidden
	}
	if in.ID == decision.LocalOperator().ID || !ValidDeviceID(in.ID) || in.Name == "" || len(in.Name) > 160 || !ValidDeviceID(in.Tenant.Org) || !ValidDeviceID(in.Tenant.Project) || in.Tenant == decision.LocalTenant || !in.Read || !in.ExpiresAt.After(time.Now()) || in.ExpiresAt.After(time.Now().Add(366*24*time.Hour)) {
		return ServicePrincipal{}, "", errors.New("invalid service principal")
	}
	if len(in.Agents) == 0 {
		in.Agents = []string{"mitos.generalist"}
	}
	if len(in.Agents) > 128 {
		return ServicePrincipal{}, "", errors.New("too many permitted agents")
	}
	for _, agent := range in.Agents {
		if !ValidDeviceID(agent) {
			return ServicePrincipal{}, "", errors.New("invalid permitted agent")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.servicePrincipals[in.ID]; ok {
		return ServicePrincipal{}, "", ErrConflict
	}
	token, err := newDeviceToken()
	if err != nil {
		return ServicePrincipal{}, "", err
	}
	p := ServicePrincipal{ID: in.ID, Name: in.Name, Tenant: in.Tenant, Read: in.Read, Write: in.Write, Agents: append([]string{}, in.Agents...), ExpiresAt: in.ExpiresAt.UTC(), Revision: newID("credential"), AuthorityEpoch: s.authorityEpoch, TokenHash: tokenHash(token)}
	if err = s.commit("", "effect", "service_principal_created", change{ServicePrincipal: &p}); err != nil {
		return ServicePrincipal{}, "", err
	}
	return publicPrincipal(p), token, nil
}

func (s *Store) ChangeServicePrincipal(id string, rotate bool, expires time.Time, operator decision.Principal) (ServicePrincipal, string, error) {
	if !localOperator(operator) {
		return ServicePrincipal{}, "", ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.servicePrincipals[id]
	if !ok {
		return ServicePrincipal{}, "", ErrNotFound
	}
	token := ""
	kind := "service_principal_revoked"
	if rotate {
		if p.Revoked || !expires.After(time.Now()) || expires.After(time.Now().Add(366*24*time.Hour)) {
			return ServicePrincipal{}, "", ErrConflict
		}
		var err error
		token, err = newDeviceToken()
		if err != nil {
			return ServicePrincipal{}, "", err
		}
		p.TokenHash = tokenHash(token)
		p.AuthorityEpoch = s.authorityEpoch
		p.ExpiresAt = expires.UTC()
		kind = "service_principal_rotated"
	} else {
		p.Revoked = true
	}
	p.Revision = newID("credential")
	if err := s.commit("", "effect", kind, change{ServicePrincipal: &p}); err != nil {
		return ServicePrincipal{}, "", err
	}
	return publicPrincipal(p), token, nil
}

func (s *Store) ServicePrincipals(operator decision.Principal) []ServicePrincipal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []ServicePrincipal{}
	if !localOperator(operator) {
		return out
	}
	for _, p := range s.servicePrincipals {
		out = append(out, publicPrincipal(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) applyServicePrincipal(p ServicePrincipal) {
	if s.servicePrincipals == nil {
		s.servicePrincipals = map[string]ServicePrincipal{}
		s.serviceTokens = map[string]string{}
	}
	if old, ok := s.servicePrincipals[p.ID]; ok {
		delete(s.serviceTokens, old.TokenHash)
	}
	s.servicePrincipals[p.ID] = graphClone(p)
	s.serviceTokens[p.TokenHash] = p.ID
}

func (s *Store) AuthenticateServicePrincipal(token string) (decision.Principal, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(token) != 64 {
		return decision.Principal{}, ErrUnauthenticated
	}
	p, ok := s.servicePrincipals[s.serviceTokens[tokenHash(token)]]
	if !ok || p.Revoked || p.AuthorityEpoch != s.authorityEpoch || !time.Now().Before(p.ExpiresAt) {
		return decision.Principal{}, ErrUnauthenticated
	}
	return decision.Principal{ID: p.ID, Tenant: p.Tenant, Authenticated: true, CredentialRevision: p.Revision, AuthorityEpoch: p.AuthorityEpoch}, nil
}

// Revalidate cached views on every operation. Rotation, expiry, restore and
// revocation take effect without waiting for a connection or request cache.
func (s *Store) authorizeServiceLocked(p decision.Principal, write bool) error {
	if localOperator(p) {
		return nil
	}
	current, ok := s.servicePrincipals[p.ID]
	if !p.Authenticated || !ok || current.Revoked || current.Tenant != p.Tenant || current.Revision != p.CredentialRevision || current.AuthorityEpoch != s.authorityEpoch || p.AuthorityEpoch != s.authorityEpoch || !time.Now().Before(current.ExpiresAt) {
		return ErrUnauthenticated
	}
	if !current.Read || write && !current.Write {
		return ErrForbidden
	}
	return nil
}

func (s *Store) principalAgentAllowedLocked(p decision.Principal, app string) bool {
	if localOperator(p) {
		return true
	}
	current := s.servicePrincipals[p.ID]
	return slices.Contains(current.Agents, app[len(agentAppPrefix):])
}

func (s *Store) admittedPrincipalAllowedLocked(id string, tenant decision.Tenant) bool {
	if tenant == decision.LocalTenant {
		return true
	}
	p, ok := s.servicePrincipals[id]
	return ok && p.Tenant == tenant && p.Write && !p.Revoked && p.AuthorityEpoch == s.authorityEpoch && time.Now().Before(p.ExpiresAt)
}
