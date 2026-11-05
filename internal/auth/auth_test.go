package auth

import (
	"testing"
	"time"
)

func init() { BcryptCost = 4 }

func TestFirstAdminAndForcedChange(t *testing.T) {
	m, _ := Open(t.TempDir() + "/users.yaml")
	pw, err := m.EnsureAdmin()
	if err != nil || pw == "" {
		t.Fatal(err)
	}
	if again, _ := m.EnsureAdmin(); again != "" {
		t.Fatal("second EnsureAdmin must not create")
	}
	s, err := m.Login("admin", pw, "1.2.3.4")
	if err != nil || !s.MustChange || s.Role != RoleAdmin {
		t.Fatalf("%v %+v", err, s)
	}
	if err := m.ChangeOwnPassword("admin", pw, "short"); err == nil {
		t.Fatal("weak password accepted")
	}
	if err := m.ChangeOwnPassword("admin", pw, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if s2, _ := m.Session(s.ID); s2.MustChange {
		t.Fatal("must_change not cleared")
	}
	if _, err := m.Login("admin", pw, "1.2.3.4"); err != ErrBadCredentials {
		t.Fatal("old password still valid")
	}
	// reload from disk
	m2, _ := Open(m.path)
	if _, err := m2.Login("admin", "correct horse battery", "x"); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimit(t *testing.T) {
	m, _ := Open(t.TempDir() + "/users.yaml")
	m.AddUser("bob", RoleReadOnly, "bobs secret pw", false)
	for i := 0; i < 5; i++ {
		if _, err := m.Login("bob", "wrong", "10.0.0.1"); err != ErrBadCredentials {
			t.Fatal(err)
		}
	}
	if _, err := m.Login("bob", "bobs secret pw", "10.0.0.1"); err != ErrRateLimited {
		t.Fatalf("expected rate limit, got %v", err)
	}
	// user name is locked from other IPs too
	if _, err := m.Login("bob", "bobs secret pw", "10.0.0.2"); err != ErrRateLimited {
		t.Fatalf("expected user lock, got %v", err)
	}
}

func TestSessionsAndTimeout(t *testing.T) {
	m, _ := Open(t.TempDir() + "/users.yaml")
	m.AddUser("ann", RoleAdmin, "anns secret pw", false)
	now := time.Now()
	m.Now = func() time.Time { return now }
	s, _ := m.Login("ann", "anns secret pw", "x")
	if _, ok := m.Session(s.ID); !ok {
		t.Fatal("session missing")
	}
	now = now.Add(31 * time.Minute)
	if _, ok := m.Session(s.ID); ok {
		t.Fatal("session should have expired")
	}
}

func TestTokensAndRoles(t *testing.T) {
	m, _ := Open(t.TempDir() + "/users.yaml")
	m.AddUser("ann", RoleAdmin, "anns secret pw", false)
	m.AddUser("ro", RoleReadOnly, "readonly secret", false)
	sec, tok, err := m.CreateToken("ann", "ci", RoleReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := m.AuthToken(sec)
	if !ok || s.Role != RoleReadOnly || s.User != "ann" {
		t.Fatalf("%+v", s)
	}
	if _, _, err := m.CreateToken("ro", "x", RoleAdmin, 0); err == nil {
		t.Fatal("readonly user must not mint admin token")
	}
	if _, ok := m.AuthToken(sec + "x"); ok {
		t.Fatal("bad token accepted")
	}
	m.DeleteToken(tok.ID, "")
	if _, ok := m.AuthToken(sec); ok {
		t.Fatal("deleted token accepted")
	}
	if err := m.DeleteUser("ann"); err != ErrLastAdmin {
		t.Fatalf("expected last admin protection, got %v", err)
	}
	if err := m.UpdateUser("ann", RoleReadOnly, "", nil); err != ErrLastAdmin {
		t.Fatal("demote last admin")
	}
}
