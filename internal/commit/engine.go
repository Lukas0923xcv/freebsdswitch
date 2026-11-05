// Package commit owns the configuration lifecycle: candidate (staged)
// edits, commit-confirm with automatic rollback, running vs. startup
// config, history and boot-time apply with safe fallback.
package commit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/plan"
	"github.com/Lukas0923xcv/freebsdswitch/internal/state"
	"github.com/Lukas0923xcv/freebsdswitch/internal/textdiff"
)

const (
	DefaultConfirmTimeout = 60 * time.Second
	MinConfirmTimeout     = 10 * time.Second
	MaxConfirmTimeout     = time.Hour
	HistorySize           = 10
)

var (
	ErrPending       = errors.New("a commit is waiting for confirmation; confirm or roll it back first")
	ErrNoPending     = errors.New("no commit is waiting for confirmation")
	ErrNoChanges     = errors.New("no changes to commit")
	ErrNoSuchVersion = errors.New("no such configuration version")
)

// Paths groups the on-disk locations.
type Paths struct {
	Startup string // /usr/local/etc/bsdswitch/config.yaml
	State   string // /var/db/bsdswitch
}

func (p Paths) running() string   { return filepath.Join(p.State, "running.yaml") }
func (p Paths) candidate() string { return filepath.Join(p.State, "candidate.yaml") }
func (p Paths) pending() string   { return filepath.Join(p.State, "pending.json") }
func (p Paths) history() string   { return filepath.Join(p.State, "history") }
func (p Paths) audit() string     { return filepath.Join(p.State, "audit.log") }

// Pending describes an unconfirmed commit.
type Pending struct {
	ID       int       `json:"id"`
	User     string    `json:"user"`
	Comment  string    `json:"comment"`
	Started  time.Time `json:"started"`
	Deadline time.Time `json:"deadline"`
	Previous string    `json:"previous"` // YAML of the config to roll back to
	New      string    `json:"new"`
}

// Version is one entry of the commit history.
type Version struct {
	ID      int       `json:"id"`
	Time    time.Time `json:"time"`
	User    string    `json:"user"`
	Comment string    `json:"comment"`
	Index   int       `json:"index"` // 0 = current running, 1 = previous, ...
}

// Event is published to subscribers on every lifecycle change.
type Event struct {
	Type    string    `json:"type"` // commit, confirm, rollback, auto-rollback, save, candidate, apply-error
	User    string    `json:"user,omitempty"`
	Message string    `json:"message,omitempty"`
	Time    time.Time `json:"time"`
}

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	Time   time.Time `json:"time"`
	User   string    `json:"user"`
	Remote string    `json:"remote,omitempty"`
	Action string    `json:"action"`
	Detail string    `json:"detail,omitempty"`
	OK     bool      `json:"ok"`
}

// Engine is safe for concurrent use.
type Engine struct {
	mu        sync.Mutex
	paths     Paths
	be        backend.Backend
	running   *config.Config
	candidate *config.Config // nil = no staged changes
	candUser  string
	pending   *Pending
	timer     *time.Timer
	nextID    int
	logf      func(string, ...any)
	subs      map[chan Event]bool
	subMu     sync.Mutex
	// Persist controls whether Save also writes rc.conf (disabled in sim).
	Persist bool
	// Now is overridable for tests.
	Now func() time.Time
	// lastApplyLog keeps the command output of the most recent apply.
	lastApply []string
	bootMode  string // "startup", "safe", "factory"
}

// New creates an engine. Call Boot afterwards.
func New(paths Paths, be backend.Backend, logf func(string, ...any)) (*Engine, error) {
	if logf == nil {
		logf = log.Printf
	}
	for _, d := range []string{paths.State, paths.history(), filepath.Dir(paths.Startup)} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	e := &Engine{paths: paths, be: be, logf: logf, subs: map[chan Event]bool{}, Persist: true, Now: time.Now}
	e.nextID = e.maxHistoryID() + 1
	return e, nil
}

// ---- subscription --------------------------------------------------------

func (e *Engine) Subscribe() chan Event {
	ch := make(chan Event, 16)
	e.subMu.Lock()
	e.subs[ch] = true
	e.subMu.Unlock()
	return ch
}

func (e *Engine) Unsubscribe(ch chan Event) {
	e.subMu.Lock()
	delete(e.subs, ch)
	e.subMu.Unlock()
}

func (e *Engine) publish(ev Event) {
	ev.Time = e.Now()
	e.subMu.Lock()
	defer e.subMu.Unlock()
	for ch := range e.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Audit appends to the audit log.
func (e *Engine) Audit(a AuditEntry) {
	if a.Time.IsZero() {
		a.Time = e.Now()
	}
	b, _ := json.Marshal(a)
	f, err := os.OpenFile(e.paths.audit(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		e.logf("audit: %v", err)
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// AuditLog returns the newest n audit entries (newest first).
func (e *Engine) AuditLog(n int) []AuditEntry {
	b, err := os.ReadFile(e.paths.audit())
	if err != nil {
		return []AuditEntry{}
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	out := []AuditEntry{}
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		var a AuditEntry
		if json.Unmarshal([]byte(lines[i]), &a) == nil {
			out = append(out, a)
		}
	}
	return out
}

// ---- accessors -------------------------------------------------------------

// Running returns a copy of the running config.
func (e *Engine) Running() *config.Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running.Clone()
}

// Startup returns the saved startup config (nil if none).
func (e *Engine) Startup() (*config.Config, error) { return config.Load(e.paths.Startup) }

// Candidate returns the staged config, or a copy of running if nothing
// is staged.
func (e *Engine) Candidate() (*config.Config, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.candidate == nil {
		return e.running.Clone(), false
	}
	return e.candidate.Clone(), true
}

// PendingCommit returns the pending commit, if any.
func (e *Engine) PendingCommit() *Pending {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending == nil {
		return nil
	}
	p := *e.pending
	return &p
}

// BootMode reports how the running config was established at boot.
func (e *Engine) BootMode() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bootMode
}

// LastApply returns the commands executed by the last apply.
func (e *Engine) LastApply() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.lastApply...)
}

func marshal(c *config.Config) string {
	b, err := c.Marshal()
	if err != nil {
		return ""
	}
	return string(b)
}

// StartupDiffers reports whether running differs from startup.
func (e *Engine) StartupDiffers() bool {
	st, err := e.Startup()
	if err != nil {
		return true
	}
	return marshal(st) != marshal(e.Running())
}

// ---- candidate ---------------------------------------------------------------

// SetCandidate stages a new configuration after validation.
func (e *Engine) SetCandidate(ctx context.Context, c *config.Config, user string) error {
	c.ApplyDefaults()
	if err := e.validate(ctx, c); err != nil {
		return err
	}
	e.mu.Lock()
	if marshal(c) == marshal(e.running) {
		e.candidate = nil
	} else {
		e.candidate = c.Clone()
		e.candUser = user
	}
	err := e.saveCandidateLocked()
	e.mu.Unlock()
	e.publish(Event{Type: "candidate", User: user})
	return err
}

// Modify applies f to the candidate (copy of running if none) and stages
// the result.
func (e *Engine) Modify(ctx context.Context, user string, f func(c *config.Config) error) (*config.Config, error) {
	c, _ := e.Candidate()
	if err := f(c); err != nil {
		return nil, err
	}
	if err := e.SetCandidate(ctx, c, user); err != nil {
		return nil, err
	}
	return c, nil
}

// DiscardCandidate drops all staged changes.
func (e *Engine) DiscardCandidate(user string) {
	e.mu.Lock()
	e.candidate = nil
	e.saveCandidateLocked()
	e.mu.Unlock()
	e.publish(Event{Type: "candidate", User: user, Message: "discarded"})
}

func (e *Engine) saveCandidateLocked() error {
	if e.candidate == nil {
		err := os.Remove(e.paths.candidate())
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return config.Save(e.paths.candidate(), e.candidate)
}

func (e *Engine) validate(ctx context.Context, c *config.Config) error {
	snap, err := e.be.Snapshot(ctx)
	if err != nil {
		return config.Validate(c, nil)
	}
	return config.Validate(c, inventory{snap, c})
}

// inventory treats configured LAG interfaces as existing.
type inventory struct {
	snap *state.Snapshot
	c    *config.Config
}

func (i inventory) Lookup(n string) (bool, bool) {
	for _, l := range i.c.LAGs {
		if l.Name == n {
			return true, true
		}
	}
	return i.snap.Lookup(n)
}

// Preview describes what a commit would do.
type Preview struct {
	Changed    bool           `json:"changed"`
	Diff       string         `json:"diff"`
	Plan       []plan.Section `json:"plan"`
	Commands   []string       `json:"commands"`
	Warnings   []string       `json:"warnings"`
	MgmtImpact []string       `json:"mgmt_impact"`
}

// Preview computes the diff and plan for the candidate.
func (e *Engine) Preview(ctx context.Context) (*Preview, error) {
	cand, staged := e.Candidate()
	run := e.Running()
	pv := &Preview{Changed: staged, Plan: []plan.Section{}, Commands: []string{}, Warnings: []string{}, MgmtImpact: []string{}}
	pv.Diff = textdiff.Unified("running", "candidate", marshal(run), marshal(cand), 3)
	in, err := plan.Gather(ctx, e.be, cand, run)
	if err != nil {
		return nil, err
	}
	p := plan.Build(in)
	pv.Plan = p.Sections
	if pv.Plan == nil {
		pv.Plan = []plan.Section{}
	}
	if cmds := p.Commands(); cmds != nil {
		pv.Commands = cmds
	}
	if p.Warnings != nil {
		pv.Warnings = p.Warnings
	}
	pv.MgmtImpact = MgmtImpact(run, cand)
	return pv, nil
}

// MgmtImpact lists changes that may cut off management access.
func MgmtImpact(old, new *config.Config) []string {
	out := []string{}
	om, nm := old.Management, new.Management
	if om.Interface != nm.Interface {
		out = append(out, fmt.Sprintf("management interface changes from %s to %s", om.Interface, nm.Interface))
	}
	if fmt.Sprint(om.IPv4) != fmt.Sprint(nm.IPv4) || fmt.Sprint(om.IPv6) != fmt.Sprint(nm.IPv6) {
		out = append(out, "management IP configuration changes")
	}
	if fmt.Sprint(om.InBand) != fmt.Sprint(nm.InBand) {
		out = append(out, "in-band management VLAN settings change")
	}
	if om.InBand.Enabled && nm.InBand.Enabled && om.InBand.VLAN == nm.InBand.VLAN {
		vid := nm.InBand.VLAN
		for _, p := range old.Ports {
			np := new.Port(p.Name)
			was := p.Membership(vid) != config.MemberNone && !p.Shutdown
			is := np != nil && np.Membership(vid) != config.MemberNone && !np.Shutdown
			if was && !is {
				out = append(out, fmt.Sprintf("port %s leaves in-band management VLAN %d", p.Name, vid))
			} else if was && is && p.Membership(vid) != np.Membership(vid) {
				out = append(out, fmt.Sprintf("port %s changes tagging of in-band management VLAN %d", p.Name, vid))
			}
		}
	}
	if fmt.Sprint(om.AllowedSubnets) != fmt.Sprint(nm.AllowedSubnets) {
		out = append(out, "allowed management subnets change")
	}
	if om.HTTPSPort != nm.HTTPSPort {
		out = append(out, "web UI port changes")
	}
	return out
}

// ---- apply ---------------------------------------------------------------------

func (e *Engine) applyLocked(ctx context.Context, desired, prev *config.Config) error {
	in, err := plan.Gather(ctx, e.be, desired, prev)
	if err != nil {
		return fmt.Errorf("reading system state: %w", err)
	}
	p := plan.Build(in)
	for _, w := range p.Warnings {
		e.logf("plan warning: %s", w)
	}
	e.lastApply = nil
	_, err = plan.Apply(ctx, e.be, p.Ops(), func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		e.lastApply = append(e.lastApply, line)
		e.logf("%s", line)
	})
	return err
}

// Commit applies the candidate with commit-confirm. If applying fails,
// the previous configuration is restored immediately.
func (e *Engine) Commit(ctx context.Context, user, comment string, timeout time.Duration) (*Pending, error) {
	if timeout == 0 {
		timeout = DefaultConfirmTimeout
	}
	if timeout < MinConfirmTimeout || timeout > MaxConfirmTimeout {
		return nil, fmt.Errorf("confirm timeout must be between %v and %v", MinConfirmTimeout, MaxConfirmTimeout)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != nil {
		return nil, ErrPending
	}
	if e.candidate == nil {
		return nil, ErrNoChanges
	}
	cand := e.candidate.Clone()
	if err := e.validateUnlocked(ctx, cand); err != nil {
		return nil, err
	}
	prev := e.running.Clone()
	if err := e.applyLocked(ctx, cand, prev); err != nil {
		e.logf("commit failed, restoring previous configuration: %v", err)
		if rerr := e.applyLocked(context.Background(), prev, cand); rerr != nil {
			e.logf("restore after failed commit also failed: %v", rerr)
		}
		e.Audit(AuditEntry{User: user, Action: "commit", Detail: err.Error()})
		e.publish(Event{Type: "apply-error", User: user, Message: err.Error()})
		return nil, fmt.Errorf("apply failed (previous configuration restored): %w", err)
	}
	e.running = cand
	e.candidate = nil
	e.saveCandidateLocked()
	config.Save(e.paths.running(), e.running)
	now := e.Now()
	e.pending = &Pending{ID: e.nextID, User: user, Comment: comment, Started: now, Deadline: now.Add(timeout),
		Previous: marshal(prev), New: marshal(cand)}
	e.nextID++
	e.savePendingLocked()
	e.armTimerLocked(timeout)
	e.Audit(AuditEntry{User: user, Action: "commit", Detail: fmt.Sprintf("commit #%d (confirm within %v) %s", e.pending.ID, timeout, comment), OK: true})
	p := *e.pending
	e.publish(Event{Type: "commit", User: user, Message: fmt.Sprintf("commit #%d applied, confirm within %v", p.ID, timeout)})
	return &p, nil
}

func (e *Engine) validateUnlocked(ctx context.Context, c *config.Config) error {
	snap, err := e.be.Snapshot(ctx)
	if err != nil {
		return config.Validate(c, nil)
	}
	return config.Validate(c, inventory{snap, c})
}

func (e *Engine) armTimerLocked(d time.Duration) {
	if e.timer != nil {
		e.timer.Stop()
	}
	id := e.pending.ID
	e.timer = time.AfterFunc(d, func() { e.expire(id) })
}

func (e *Engine) expire(id int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending == nil || e.pending.ID != id {
		return
	}
	e.logf("commit #%d was not confirmed in time: rolling back", id)
	if err := e.rollbackPendingLocked(context.Background()); err != nil {
		e.logf("automatic rollback failed: %v", err)
	}
	e.Audit(AuditEntry{User: "system", Action: "auto-rollback", Detail: fmt.Sprintf("commit #%d not confirmed", id), OK: true})
	e.publish(Event{Type: "auto-rollback", Message: fmt.Sprintf("commit #%d was not confirmed and has been rolled back", id)})
}

func (e *Engine) rollbackPendingLocked(ctx context.Context) error {
	p := e.pending
	prev, err := config.Parse([]byte(p.Previous))
	if err != nil {
		return err
	}
	cur := e.running
	if cur == nil {
		cur, _ = config.Parse([]byte(p.New))
	}
	err = e.applyLocked(ctx, prev, cur)
	e.running = prev
	config.Save(e.paths.running(), e.running)
	e.pending = nil
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	os.Remove(e.paths.pending())
	return err
}

// Confirm makes the pending commit permanent (in the running config;
// Save writes it to startup).
func (e *Engine) Confirm(user string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending == nil {
		return ErrNoPending
	}
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	p := e.pending
	e.pending = nil
	os.Remove(e.paths.pending())
	e.addHistoryLocked(Version{ID: p.ID, Time: e.Now(), User: p.User, Comment: p.Comment}, e.running)
	e.Audit(AuditEntry{User: user, Action: "confirm", Detail: fmt.Sprintf("commit #%d", p.ID), OK: true})
	e.publish(Event{Type: "confirm", User: user, Message: fmt.Sprintf("commit #%d confirmed", p.ID)})
	return nil
}

// Cancel rolls back the pending commit immediately.
func (e *Engine) Cancel(ctx context.Context, user string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending == nil {
		return ErrNoPending
	}
	id := e.pending.ID
	err := e.rollbackPendingLocked(ctx)
	e.Audit(AuditEntry{User: user, Action: "rollback", Detail: fmt.Sprintf("commit #%d cancelled", id), OK: err == nil})
	e.publish(Event{Type: "rollback", User: user, Message: fmt.Sprintf("commit #%d rolled back", id)})
	return err
}

// Extend pushes the confirm deadline out (e.g. "I need more time").
func (e *Engine) Extend(user string, d time.Duration) (*Pending, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending == nil {
		return nil, ErrNoPending
	}
	if d < MinConfirmTimeout || d > MaxConfirmTimeout {
		return nil, fmt.Errorf("invalid timeout")
	}
	e.pending.Deadline = e.Now().Add(d)
	e.savePendingLocked()
	e.armTimerLocked(d)
	p := *e.pending
	e.publish(Event{Type: "commit", User: user, Message: "confirm deadline extended"})
	return &p, nil
}

// ApplyNow applies c immediately without commit-confirm. It is used by
// the local console (factory reset, rollback, management IP changes) where
// the operator is physically present.
func (e *Engine) ApplyNow(ctx context.Context, c *config.Config, user, action string) error {
	c.ApplyDefaults()
	if err := e.validate(ctx, c); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != nil {
		e.rollbackPendingLocked(ctx)
	}
	prev := e.running
	if err := e.applyLocked(ctx, c, prev); err != nil {
		e.Audit(AuditEntry{User: user, Action: action, Detail: err.Error()})
		return err
	}
	e.running = c.Clone()
	e.candidate = nil
	e.saveCandidateLocked()
	config.Save(e.paths.running(), e.running)
	id := e.nextID
	e.nextID++
	e.addHistoryLocked(Version{ID: id, Time: e.Now(), User: user, Comment: action}, e.running)
	e.Audit(AuditEntry{User: user, Action: action, OK: true})
	e.publish(Event{Type: "commit", User: user, Message: action})
	return nil
}

// ---- save / startup --------------------------------------------------------------

// Save writes the running config to the startup config (and rc.conf).
func (e *Engine) Save(ctx context.Context, user string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != nil {
		return ErrPending
	}
	var prevStartup *config.Config
	if st, err := config.Load(e.paths.Startup); err == nil {
		prevStartup = st
	}
	if err := config.Save(e.paths.Startup, e.running); err != nil {
		return err
	}
	if e.Persist {
		for _, op := range plan.PersistOps(e.running, prevStartup) {
			if _, err := backend.Execute(ctx, e.be, op); err != nil {
				e.logf("persist: %v", err)
			}
		}
	}
	e.Audit(AuditEntry{User: user, Action: "save", OK: true})
	e.publish(Event{Type: "save", User: user, Message: "running configuration saved to startup"})
	return nil
}

// ---- history ---------------------------------------------------------------------

func (e *Engine) histFile(id int) string {
	return filepath.Join(e.paths.history(), fmt.Sprintf("%06d.yaml", id))
}
func (e *Engine) histMeta(id int) string {
	return filepath.Join(e.paths.history(), fmt.Sprintf("%06d.json", id))
}

func (e *Engine) maxHistoryID() int {
	ids := e.historyIDs()
	if len(ids) == 0 {
		return 0
	}
	return ids[0]
}

// historyIDs returns ids newest first.
func (e *Engine) historyIDs() []int {
	ents, _ := os.ReadDir(e.paths.history())
	var ids []int
	for _, d := range ents {
		var id int
		if n, _ := fmt.Sscanf(d.Name(), "%06d.yaml", &id); n == 1 && strings.HasSuffix(d.Name(), ".yaml") {
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	return ids
}

func (e *Engine) addHistoryLocked(v Version, c *config.Config) {
	if err := config.Save(e.histFile(v.ID), c); err != nil {
		e.logf("history: %v", err)
		return
	}
	b, _ := json.Marshal(v)
	os.WriteFile(e.histMeta(v.ID), b, 0o640)
	ids := e.historyIDs()
	for _, id := range ids[min(len(ids), HistorySize):] {
		os.Remove(e.histFile(id))
		os.Remove(e.histMeta(id))
	}
}

// History lists stored versions, newest (= current) first.
func (e *Engine) History() []Version {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []Version{}
	for i, id := range e.historyIDs() {
		v := Version{ID: id}
		if b, err := os.ReadFile(e.histMeta(id)); err == nil {
			json.Unmarshal(b, &v)
		}
		v.Index = i
		out = append(out, v)
	}
	return out
}

// HistoryConfig loads a stored version.
func (e *Engine) HistoryConfig(id int) (*config.Config, error) {
	c, err := config.Load(e.histFile(id))
	if os.IsNotExist(err) {
		return nil, ErrNoSuchVersion
	}
	return c, err
}

// LoadRollback stages history version id as candidate (Junos "rollback N").
func (e *Engine) LoadRollback(ctx context.Context, id int, user string) error {
	c, err := e.HistoryConfig(id)
	if err != nil {
		return err
	}
	return e.SetCandidate(ctx, c, user)
}

// ---- persistence of pending commits ----------------------------------------------

func (e *Engine) savePendingLocked() {
	b, _ := json.MarshalIndent(e.pending, "", "  ")
	config.WriteFileAtomic(e.paths.pending(), b, 0o640)
}

// ---- boot ----------------------------------------------------------------------

// Boot establishes the running configuration at daemon start:
//
//  1. If the daemon restarted with a pending commit whose deadline has
//     not passed, the timer is re-armed; if it has passed, roll back.
//  2. Otherwise apply the startup config (on a fresh boot) or the last
//     running config (daemon restart without reboot).
//  3. If that fails, apply a minimal safe config that only brings up the
//     management interface.
//
// fallbackMgmt is used when no config exists at all (normally the console
// wizard creates one first).
func (e *Engine) Boot(ctx context.Context, freshBoot bool, fallbackMgmt string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// candidate survives daemon restarts
	if c, err := config.Load(e.paths.candidate()); err == nil {
		e.candidate = c
	}
	// daemon restart with a pending commit?
	if !freshBoot {
		if b, err := os.ReadFile(e.paths.pending()); err == nil {
			var p Pending
			if json.Unmarshal(b, &p) == nil {
				if cur, err := config.Parse([]byte(p.New)); err == nil {
					e.running = cur
					e.pending = &p
					if p.ID >= e.nextID {
						e.nextID = p.ID + 1
					}
					left := p.Deadline.Sub(e.Now())
					if left <= 0 {
						e.logf("pending commit #%d expired while the daemon was down: rolling back", p.ID)
						err := e.rollbackPendingLocked(ctx)
						e.Audit(AuditEntry{User: "system", Action: "auto-rollback", Detail: "deadline passed during daemon restart", OK: err == nil})
						e.bootMode = "running"
						return err
					}
					e.logf("resuming pending commit #%d (%v left)", p.ID, left.Round(time.Second))
					e.armTimerLocked(left)
					e.bootMode = "running"
					return nil
				}
			}
		}
		if c, err := config.Load(e.paths.running()); err == nil {
			e.running = c
			e.bootMode = "running"
			// make sure the kernel matches (e.g. after a crash mid-apply)
			if err := e.applyLocked(ctx, c, nil); err != nil {
				e.logf("re-applying running config failed: %v", err)
			}
			return nil
		}
	}
	// fresh boot: a pending commit from before the reboot was never
	// confirmed, so it is discarded and the startup config wins.
	os.Remove(e.paths.pending())
	st, err := config.Load(e.paths.Startup)
	if err == nil {
		err = e.validateUnlocked(ctx, st)
	}
	if err == nil {
		if aerr := e.applyLocked(ctx, st, nil); aerr == nil {
			e.running = st
			e.bootMode = "startup"
			config.Save(e.paths.running(), e.running)
			if len(e.historyIDs()) == 0 {
				e.addHistoryLocked(Version{ID: e.nextID, Time: e.Now(), User: "system", Comment: "initial startup config"}, st)
				e.nextID++
			}
			e.Audit(AuditEntry{User: "system", Action: "boot", Detail: "startup config applied", OK: true})
			return nil
		} else {
			err = aerr
		}
	}
	e.logf("startup config could not be applied (%v): falling back to safe config", err)
	safe := SafeConfig(st, fallbackMgmt)
	if aerr := e.applyLocked(ctx, safe, nil); aerr != nil {
		e.logf("safe config failed too: %v", aerr)
	}
	e.running = safe
	e.bootMode = "safe"
	config.Save(e.paths.running(), e.running)
	e.Audit(AuditEntry{User: "system", Action: "boot", Detail: "SAFE MODE: " + err.Error()})
	return fmt.Errorf("booted in safe mode: %w", err)
}

// SafeConfig keeps only the management interface settings of st (if
// usable) and no switch ports.
func SafeConfig(st *config.Config, fallbackMgmt string) *config.Config {
	mgmt := fallbackMgmt
	if st != nil && st.Management.Interface != "" {
		mgmt = st.Management.Interface
	}
	c := config.Default(mgmt, nil)
	if st != nil {
		m := st.Management
		m.InBand = config.InBand{}
		c.Management = m
		c.System = st.System
		// keep bridge-level settings (name, MTU, STP) but no ports
		c.Bridge = st.Bridge
		c.ApplyDefaults()
		if config.Validate(c, nil) != nil {
			c = config.Default(mgmt, nil)
		}
	}
	return c
}
