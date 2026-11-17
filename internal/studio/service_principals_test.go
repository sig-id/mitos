package studio

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mitos/internal/decision"
)

func serviceTestPrincipal(t *testing.T, s *Store, id, tenant string, write bool) (decision.Principal, string) {
	t.Helper()
	record, token, err := s.CreateServicePrincipal(ServicePrincipalInput{ID: id, Name: id, Tenant: decision.Tenant{Org: tenant, Project: "app"}, Read: true, Write: write, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	if record.TokenHash != "" || len(token) != 64 {
		t.Fatal("unsafe credential response")
	}
	p, err := s.AuthenticateServicePrincipal(token)
	if err != nil {
		t.Fatal(err)
	}
	return p, token
}

func TestApplicationScopeBindsTenantKeysAndPrincipalAndReplays(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	s := e.Store
	alpha, tokenA := serviceTestPrincipal(t, s, "alpha", "a", true)
	beta, _ := serviceTestPrincipal(t, s, "beta", "b", true)
	sibling, _ := serviceTestPrincipal(t, s, "sibling", "a", true)
	readOnly, _ := serviceTestPrincipal(t, s, "reader", "a", false)
	brief := structuredBrief("same-key", "agent:mitos.generalist")
	a, err := e.Application(alpha).Admit(brief)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Application(beta).Admit(brief)
	if err != nil || b.ID == a.ID || b.Tenant == a.Tenant {
		t.Fatal("tenant key collision", err)
	}
	if replay, err := e.Application(alpha).Admit(brief); err != nil || replay.ID != a.ID {
		t.Fatal("idempotent replay", err)
	}
	if _, err = e.Application(sibling).Admit(brief); !errors.Is(err, ErrConflict) {
		t.Fatal("request key not principal bound", err)
	}
	if _, err = e.Application(readOnly).Admit(structuredBrief("read-only", "agent:mitos.generalist")); !errors.Is(err, ErrForbidden) {
		t.Fatal("read-only principal admitted work", err)
	}
	if err = s.SaveArtifact(a.ID, "private.txt", []byte("alpha secret")); err != nil {
		t.Fatal(err)
	}
	view := s.As(beta)
	if _, err = view.Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-tenant run visible", err)
	}
	if _, err = view.Task(a.TaskID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-tenant task visible", err)
	}
	if _, err = view.Artifact(a.ID, "private.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-tenant artifact visible", err)
	}
	if err = e.Application(beta).Cancel(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-tenant cancellation allowed", err)
	}
	if _, err = s.As(decision.Principal{ID: alpha.ID, Tenant: alpha.Tenant, Authenticated: true}).Get(a.ID); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("forged cached principal accepted", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	principal, err := reopened.AuthenticateServicePrincipal(tokenA)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.As(principal).Get(a.ID); err != nil || got.PrincipalID != alpha.ID {
		t.Fatal("principal/task binding lost", err)
	}
	raw, err := readTestJournal(s.Dir())
	if err != nil || bytes.Contains(raw, []byte(tokenA)) {
		t.Fatal("plaintext service token persisted", err)
	}
}

func TestCachedApplicationViewsRevalidateRotationRevocationAndRestore(t *testing.T) {
	s := testStore(t)
	p, token := serviceTestPrincipal(t, s, "app", "tenant", true)
	view := s.As(p)
	if _, err := view.Runs(); err != nil {
		t.Fatal(err)
	}
	_, replacement, err := s.ChangeServicePrincipal(p.ID, true, time.Now().Add(time.Hour), decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateServicePrincipal(token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("rotated token retained access", err)
	}
	if _, err = view.Runs(); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("cached view survived rotation", err)
	}
	rotated, err := s.AuthenticateServicePrincipal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err = s.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ChangeServicePrincipal(p.ID, false, time.Time{}, decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.As(rotated).Runs(); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("cached view survived revocation", err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err = RestoreStore(backup, restored); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.AuthenticateServicePrincipal(replacement); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("token survived authority restore", err)
	}
}
