// Package auth implements local users (bcrypt), roles, sessions, API
// tokens and login rate limiting.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"

	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

const (
	RoleAdmin    = "admin"
	RoleReadOnly = "readonly"
	MinPassword  = 8
)

// BcryptCost is the bcrypt work factor (lowered in tests).
var BcryptCost = 12

var (
	ErrBadCredentials = errors.New("invalid username or password")
	ErrRateLimited    = errors.New("too many failed login attempts; try again later")
	ErrNotFound       = errors.New("no such user")
	ErrLastAdmin      = errors.New("cannot remove or demote the last administrator")
	userRe            = regexp.MustCompile(`^[a-z_][a-z0-9_.\-]{0,31}$`)
)

// User is a stored account.
type User struct {
	Name       string    `yaml:"name" json:"name"`
	Role       string    `yaml:"role" json:"role"`
	Hash       string    `yaml:"hash" json:"-"`
	MustChange bool      `yaml:"must_change,omitempty" json:"must_change"`
	Created    time.Time `yaml:"created" json:"created"`
	Changed    time.Time `yaml:"changed,omitempty" json:"changed"`
}

// Token is an API token. Only the SHA-256 of the secret is stored.
type Token struct {
	ID       string    `yaml:"id" json:"id"`
	Name     string    `yaml:"name" json:"name"`
	User     string    `yaml:"user" json:"user"`
	Role     string    `yaml:"role" json:"role"`
	Hash     string    `yaml:"hash" json:"-"`
	Created  time.Time `yaml:"created" json:"created"`
	Expires  time.Time `yaml:"expires,omitempty" json:"expires"`
	LastUsed time.Time `yaml:"last_used,omitempty" json:"last_used"`
}

type store struct {
	Users  []User  `yaml:"users"`
	Tokens []Token `yaml:"tokens"`
}

// Session is a logged-in browser/CLI session.
type Session struct {
	ID         string    `json:"-"`
	User       string    `json:"user"`
	Role       string    `json:"role"`
	CSRF       string    `json:"csrf"`
	Created    time.Time `json:"created"`
	LastSeen   time.Time `json:"last_seen"`
	Remote     string    `json:"remote"`
	MustChange bool      `json:"must_change"`
	ViaToken   bool      `json:"via_token"`
}

// Manager is safe for concurrent use.
type Manager struct {
	mu       sync.Mutex
	path     string
	st       store
	sessions map[string]*Session
	limiter  *limiter
	// IdleTimeout for sessions.
	IdleTimeout time.Duration
	Now         func() time.Time
}

// Open loads (or creates) the user database.
func Open(path string) (*Manager, error) {
	m := &Manager{path: path, sessions: map[string]*Session{}, limiter: newLimiter(), IdleTimeout: 30 * time.Minute, Now: time.Now}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := yaml.Unmarshal(b, &m.st); err != nil {
			return nil, fmt.Errorf("users file: %w", err)
		}
	}
	return m, nil
}

func (m *Manager) saveLocked() error {
	b, err := yaml.Marshal(&m.st)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(m.path, b, 0o600)
}

// RandomToken returns n random bytes base64url encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// RandomPassword returns a human-typable random password.
func RandomPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 14)
	rand.Read(b)
	out := make([]byte, len(b))
	for i, c := range b {
		out[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(out[:4]) + "-" + string(out[4:9]) + "-" + string(out[9:])
}

// HasUsers reports whether any user exists.
func (m *Manager) HasUsers() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.st.Users) > 0
}

// EnsureAdmin creates the "admin" account with a random one-time password
// if there are no users. The password must be changed at first login.
// Returns the password ("" if nothing was created).
func (m *Manager) EnsureAdmin() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.st.Users) > 0 {
		return "", nil
	}
	pw := RandomPassword()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), BcryptCost)
	if err != nil {
		return "", err
	}
	now := m.Now()
	m.st.Users = append(m.st.Users, User{Name: "admin", Role: RoleAdmin, Hash: string(h), MustChange: true, Created: now, Changed: now})
	return pw, m.saveLocked()
}

// ResetPassword sets a new random one-time password (console menu 3).
// The user is created as admin if it does not exist.
func (m *Manager) ResetPassword(name string) (string, error) {
	pw := RandomPassword()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), BcryptCost)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.userLocked(name)
	if u == nil {
		m.st.Users = append(m.st.Users, User{Name: name, Role: RoleAdmin, Created: m.Now()})
		u = &m.st.Users[len(m.st.Users)-1]
	}
	u.Hash = string(h)
	u.MustChange = true
	u.Changed = m.Now()
	m.dropSessionsLocked(name)
	m.limiter.reset(name)
	return pw, m.saveLocked()
}

func (m *Manager) userLocked(name string) *User {
	for i := range m.st.Users {
		if m.st.Users[i].Name == name {
			return &m.st.Users[i]
		}
	}
	return nil
}

// Users returns all users.
func (m *Manager) Users() []User {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]User{}, m.st.Users...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CheckPassword validates password strength.
func CheckPassword(user, pw string) error {
	if len(pw) < MinPassword {
		return fmt.Errorf("password must be at least %d characters", MinPassword)
	}
	if len(pw) > 72 {
		return fmt.Errorf("password must be at most 72 characters")
	}
	if strings.EqualFold(pw, user) || strings.Contains(strings.ToLower(pw), "password") || pw == "bsdswitch" {
		return fmt.Errorf("password is too easy to guess")
	}
	return nil
}

func validRole(r string) bool { return r == RoleAdmin || r == RoleReadOnly }

// AddUser creates a user.
func (m *Manager) AddUser(name, role, pw string, mustChange bool) error {
	if !userRe.MatchString(name) {
		return fmt.Errorf("invalid user name")
	}
	if !validRole(role) {
		return fmt.Errorf("role must be admin or readonly")
	}
	if err := CheckPassword(name, pw); err != nil {
		return err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), BcryptCost)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.userLocked(name) != nil {
		return fmt.Errorf("user %s already exists", name)
	}
	now := m.Now()
	m.st.Users = append(m.st.Users, User{Name: name, Role: role, Hash: string(h), MustChange: mustChange, Created: now, Changed: now})
	return m.saveLocked()
}

func (m *Manager) adminCountLocked() int {
	n := 0
	for _, u := range m.st.Users {
		if u.Role == RoleAdmin {
			n++
		}
	}
	return n
}

// UpdateUser changes role and/or password (empty = unchanged).
func (m *Manager) UpdateUser(name, role, pw string, mustChange *bool) error {
	var h []byte
	if pw != "" {
		if err := CheckPassword(name, pw); err != nil {
			return err
		}
		var err error
		if h, err = bcrypt.GenerateFromPassword([]byte(pw), BcryptCost); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.userLocked(name)
	if u == nil {
		return ErrNotFound
	}
	if role != "" {
		if !validRole(role) {
			return fmt.Errorf("role must be admin or readonly")
		}
		if u.Role == RoleAdmin && role != RoleAdmin && m.adminCountLocked() == 1 {
			return ErrLastAdmin
		}
		u.Role = role
		for _, s := range m.sessions {
			if s.User == name {
				s.Role = role
			}
		}
	}
	if h != nil {
		u.Hash = string(h)
		u.Changed = m.Now()
		m.dropSessionsLocked(name)
	}
	if mustChange != nil {
		u.MustChange = *mustChange
	}
	return m.saveLocked()
}

// DeleteUser removes a user and its tokens and sessions.
func (m *Manager) DeleteUser(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.userLocked(name)
	if u == nil {
		return ErrNotFound
	}
	if u.Role == RoleAdmin && m.adminCountLocked() == 1 {
		return ErrLastAdmin
	}
	var keep []User
	for _, x := range m.st.Users {
		if x.Name != name {
			keep = append(keep, x)
		}
	}
	m.st.Users = keep
	var kt []Token
	for _, t := range m.st.Tokens {
		if t.User != name {
			kt = append(kt, t)
		}
	}
	m.st.Tokens = kt
	m.dropSessionsLocked(name)
	return m.saveLocked()
}

// ChangeOwnPassword verifies the old password and sets a new one.
func (m *Manager) ChangeOwnPassword(name, oldPw, newPw string) error {
	if err := CheckPassword(name, newPw); err != nil {
		return err
	}
	if oldPw == newPw {
		return fmt.Errorf("the new password must differ from the old one")
	}
	m.mu.Lock()
	u := m.userLocked(name)
	if u == nil || bcrypt.CompareHashAndPassword([]byte(u.Hash), []byte(oldPw)) != nil {
		m.mu.Unlock()
		return ErrBadCredentials
	}
	m.mu.Unlock()
	h, err := bcrypt.GenerateFromPassword([]byte(newPw), BcryptCost)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u = m.userLocked(name)
	if u == nil {
		return ErrNotFound
	}
	u.Hash = string(h)
	u.MustChange = false
	u.Changed = m.Now()
	for _, s := range m.sessions {
		if s.User == name {
			s.MustChange = false
		}
	}
	return m.saveLocked()
}

// dummyHash is compared against when the user does not exist, so that
// timing does not reveal valid user names.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), 12)

// Login verifies credentials and creates a session.
func (m *Manager) Login(name, pw, remote string) (*Session, error) {
	if !m.limiter.allow(remote, name) {
		return nil, ErrRateLimited
	}
	m.mu.Lock()
	u := m.userLocked(name)
	var hash []byte
	var role string
	var must bool
	if u != nil {
		hash, role, must = []byte(u.Hash), u.Role, u.MustChange
	}
	m.mu.Unlock()
	if hash == nil {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(pw))
		m.limiter.fail(remote, name)
		return nil, ErrBadCredentials
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(pw)) != nil {
		m.limiter.fail(remote, name)
		return nil, ErrBadCredentials
	}
	m.limiter.success(remote, name)
	now := m.Now()
	s := &Session{ID: RandomToken(32), User: name, Role: role, CSRF: RandomToken(24), Created: now, LastSeen: now, Remote: remote, MustChange: must}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	return s, nil
}

// Session looks up and refreshes a session.
func (m *Manager) Session(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	now := m.Now()
	if now.Sub(s.LastSeen) > m.IdleTimeout || now.Sub(s.Created) > 24*time.Hour {
		delete(m.sessions, id)
		return nil, false
	}
	s.LastSeen = now
	c := *s
	return &c, true
}

// Logout deletes a session.
func (m *Manager) Logout(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func (m *Manager) dropSessionsLocked(user string) {
	for id, s := range m.sessions {
		if s.User == user {
			delete(m.sessions, id)
		}
	}
}

// Sessions lists active sessions.
func (m *Manager) Sessions() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Session
	for _, s := range m.sessions {
		out = append(out, *s)
	}
	return out
}

// ---- API tokens ---------------------------------------------------------------

func hashToken(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// CreateToken returns the plaintext token once.
func (m *Manager) CreateToken(user, name, role string, ttl time.Duration) (string, Token, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9 _.\-]{1,40}$`).MatchString(name) {
		return "", Token{}, fmt.Errorf("invalid token name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.userLocked(user)
	if u == nil {
		return "", Token{}, ErrNotFound
	}
	if role == "" {
		role = u.Role
	}
	if !validRole(role) || (u.Role == RoleReadOnly && role == RoleAdmin) {
		return "", Token{}, fmt.Errorf("invalid role for token")
	}
	id := RandomToken(6)
	secret := "bsw_" + id + "_" + RandomToken(32)
	t := Token{ID: id, Name: name, User: user, Role: role, Hash: hashToken(secret), Created: m.Now()}
	if ttl > 0 {
		t.Expires = m.Now().Add(ttl)
	}
	m.st.Tokens = append(m.st.Tokens, t)
	return secret, t, m.saveLocked()
}

// Tokens lists tokens (of one user, or all if user == "").
func (m *Manager) Tokens(user string) []Token {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Token{}
	for _, t := range m.st.Tokens {
		if user == "" || t.User == user {
			out = append(out, t)
		}
	}
	return out
}

// DeleteToken removes a token; if user != "" it must own it.
func (m *Manager) DeleteToken(id, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, t := range m.st.Tokens {
		if t.ID == id && (user == "" || t.User == user) {
			m.st.Tokens = append(m.st.Tokens[:i], m.st.Tokens[i+1:]...)
			return m.saveLocked()
		}
	}
	return ErrNotFound
}

// AuthToken validates a bearer token and returns a pseudo-session.
func (m *Manager) AuthToken(secret string) (*Session, bool) {
	if !strings.HasPrefix(secret, "bsw_") {
		return nil, false
	}
	h := hashToken(secret)
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.st.Tokens {
		t := &m.st.Tokens[i]
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(h)) == 1 {
			if !t.Expires.IsZero() && m.Now().After(t.Expires) {
				return nil, false
			}
			u := m.userLocked(t.User)
			if u == nil {
				return nil, false
			}
			role := t.Role
			if u.Role == RoleReadOnly {
				role = RoleReadOnly
			}
			if m.Now().Sub(t.LastUsed) > time.Minute {
				t.LastUsed = m.Now()
				m.saveLocked()
			}
			return &Session{User: t.User, Role: role, ViaToken: true, Created: t.Created, LastSeen: m.Now()}, true
		}
	}
	return nil, false
}

// ---- rate limiting ---------------------------------------------------------------

// limiter locks out a source IP or a user name after repeated failures:
// 5 failures within 10 minutes -> locked for 5 minutes (doubling up to 1h).
type limiter struct {
	mu sync.Mutex
	m  map[string]*attempts
}

type attempts struct {
	fails   []time.Time
	until   time.Time
	lockout time.Duration
}

func newLimiter() *limiter { return &limiter{m: map[string]*attempts{}} }

const (
	maxFails   = 5
	failWindow = 10 * time.Minute
)

func (l *limiter) keys(remote, user string) []string {
	return []string{"ip:" + remote, "user:" + user}
}

func (l *limiter) allow(remote, user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range l.keys(remote, user) {
		if a := l.m[k]; a != nil && now.Before(a.until) {
			return false
		}
	}
	return true
}

func (l *limiter) fail(remote, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range l.keys(remote, user) {
		a := l.m[k]
		if a == nil {
			a = &attempts{lockout: 5 * time.Minute}
			l.m[k] = a
		}
		var recent []time.Time
		for _, t := range a.fails {
			if now.Sub(t) < failWindow {
				recent = append(recent, t)
			}
		}
		a.fails = append(recent, now)
		if len(a.fails) >= maxFails {
			a.until = now.Add(a.lockout)
			a.lockout = min(a.lockout*2, time.Hour)
			a.fails = nil
		}
	}
}

func (l *limiter) success(remote, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range l.keys(remote, user) {
		delete(l.m, k)
	}
}

func (l *limiter) reset(user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, "user:"+user)
}
