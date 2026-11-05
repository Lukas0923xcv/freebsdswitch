package commit

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/mock"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

func setup(t *testing.T) (*Engine, *mock.Sim) {
	t.Helper()
	dir := t.TempDir()
	sim := mock.Lab()
	e, err := New(Paths{Startup: dir + "/etc/config.yaml", State: dir + "/db"}, sim, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	e.Persist = true
	st := config.Default("vtnet0", []string{"vtnet1", "vtnet2", "vtnet3", "vtnet4"})
	if err := config.Save(e.paths.Startup, st); err != nil {
		t.Fatal(err)
	}
	if err := e.Boot(context.Background(), true, ""); err != nil {
		t.Fatal(err)
	}
	return e, sim
}

func member(t *testing.T, sim *mock.Sim, port string) (int, string) {
	snap, _ := sim.Snapshot(context.Background())
	b := snap.Get("bridge0")
	if b == nil || b.Bridge == nil {
		t.Fatal("no bridge")
	}
	m := b.Bridge.Member(port)
	if m == nil {
		return -1, ""
	}
	return m.Untagged, m.Tagged.String()
}

func stageVLAN10(t *testing.T, e *Engine) {
	t.Helper()
	_, err := e.Modify(context.Background(), "admin", func(c *config.Config) error {
		c.VLANs = append(c.VLANs, config.VLAN{ID: 10, Name: "office"})
		c.Port("vtnet2").PVID = 10
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBootAppliesStartup(t *testing.T) {
	e, sim := setup(t)
	if e.BootMode() != "startup" {
		t.Fatal(e.BootMode())
	}
	if u, _ := member(t, sim, "vtnet1"); u != 1 {
		t.Fatalf("vtnet1 untagged %d", u)
	}
	if len(e.History()) != 1 {
		t.Fatal("initial history entry missing")
	}
}

func TestCommitConfirm(t *testing.T) {
	e, sim := setup(t)
	stageVLAN10(t, e)
	if u, _ := member(t, sim, "vtnet2"); u != 1 {
		t.Fatal("staged change must not be applied yet")
	}
	pv, err := e.Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Changed || !strings.Contains(pv.Diff, "+  - id: 10") || !strings.Contains(strings.Join(pv.Commands, "\n"), "ifconfig bridge0 ifuntagged vtnet2 10") {
		t.Fatalf("preview: %+v", pv)
	}
	p, err := e.Commit(context.Background(), "admin", "office vlan", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := member(t, sim, "vtnet2"); u != 10 {
		t.Fatal("not applied")
	}
	if _, err := e.Commit(context.Background(), "admin", "", time.Minute); !errors.Is(err, ErrPending) {
		t.Fatalf("expected ErrPending, got %v", err)
	}
	if err := e.Save(context.Background(), "admin"); !errors.Is(err, ErrPending) {
		t.Fatal("save must be refused while pending")
	}
	if err := e.Confirm("admin"); err != nil {
		t.Fatal(err)
	}
	if e.PendingCommit() != nil {
		t.Fatal("still pending")
	}
	if !e.StartupDiffers() {
		t.Fatal("startup should differ before save")
	}
	if err := e.Save(context.Background(), "admin"); err != nil {
		t.Fatal(err)
	}
	if e.StartupDiffers() {
		t.Fatal("startup should equal running after save")
	}
	if !strings.Contains(strings.Join(sim.Commands(), "\n"), "sysrc hostname=bsdswitch ifconfig_vtnet0=DHCP") {
		t.Fatal("rc.conf not persisted")
	}
	h := e.History()
	if len(h) != 2 || h[0].ID != p.ID || h[0].Comment != "office vlan" {
		t.Fatalf("history %+v", h)
	}
}

func TestAutoRollback(t *testing.T) {
	e, sim := setup(t)
	stageVLAN10(t, e)
	// shorten the timer for the test
	if _, err := e.Commit(context.Background(), "admin", "", MinConfirmTimeout); err != nil {
		t.Fatal(err)
	}
	ch := e.Subscribe()
	defer e.Unsubscribe(ch)
	e.mu.Lock()
	e.armTimerLocked(50 * time.Millisecond)
	e.mu.Unlock()
	select {
	case ev := <-ch:
		if ev.Type != "auto-rollback" {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no auto rollback")
	}
	if u, _ := member(t, sim, "vtnet2"); u != 1 {
		t.Fatal("not rolled back in kernel")
	}
	if e.Running().VLAN(10) != nil {
		t.Fatal("running config not rolled back")
	}
	if e.PendingCommit() != nil {
		t.Fatal("pending not cleared")
	}
}

func TestCancel(t *testing.T) {
	e, sim := setup(t)
	stageVLAN10(t, e)
	e.Commit(context.Background(), "admin", "", time.Minute)
	if err := e.Cancel(context.Background(), "admin"); err != nil {
		t.Fatal(err)
	}
	if u, _ := member(t, sim, "vtnet2"); u != 1 {
		t.Fatal("not rolled back")
	}
}

func TestFailedApplyRestores(t *testing.T) {
	e, sim := setup(t)
	stageVLAN10(t, e)
	sim.FailOn = regexp.MustCompile(`ifuntagged vtnet2 10`)
	_, err := e.Commit(context.Background(), "admin", "", time.Minute)
	if err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("expected failure, got %v", err)
	}
	sim.FailOn = nil
	if e.PendingCommit() != nil {
		t.Fatal("no pending expected")
	}
	if u, _ := member(t, sim, "vtnet2"); u != 1 {
		t.Fatal("state changed")
	}
	// candidate is kept so the user can fix it
	if _, staged := e.Candidate(); !staged {
		t.Fatal("candidate should be kept")
	}
}

func TestDaemonRestartResumesPending(t *testing.T) {
	e, sim := setup(t)
	stageVLAN10(t, e)
	e.Commit(context.Background(), "admin", "", time.Minute)
	e.mu.Lock()
	e.timer.Stop()
	// simulate deadline passing while the daemon was down
	e.pending.Deadline = time.Now().Add(-time.Second)
	e.savePendingLocked()
	e.mu.Unlock()
	e2, _ := New(e.paths, sim, t.Logf)
	if err := e2.Boot(context.Background(), false, ""); err != nil {
		t.Fatal(err)
	}
	if e2.PendingCommit() != nil || e2.Running().VLAN(10) != nil {
		t.Fatal("expired pending commit must be rolled back on restart")
	}
	if u, _ := member(t, sim, "vtnet2"); u != 1 {
		t.Fatal("kernel not rolled back")
	}
}

func TestDaemonRestartKeepsTimer(t *testing.T) {
	e, sim := setup(t)
	stageVLAN10(t, e)
	e.Commit(context.Background(), "admin", "", time.Minute)
	e.mu.Lock()
	e.timer.Stop()
	e.mu.Unlock()
	e2, _ := New(e.paths, sim, t.Logf)
	e2.Boot(context.Background(), false, "")
	p := e2.PendingCommit()
	if p == nil || time.Until(p.Deadline) < 30*time.Second {
		t.Fatalf("pending not resumed: %+v", p)
	}
	if err := e2.Confirm("admin"); err != nil {
		t.Fatal(err)
	}
}

func TestRebootDiscardsUnconfirmed(t *testing.T) {
	e, sim := setup(t)
	stageVLAN10(t, e)
	e.Commit(context.Background(), "admin", "", time.Minute)
	e.mu.Lock()
	e.timer.Stop()
	e.mu.Unlock()
	sim2 := mock.Lab() // fresh kernel after reboot
	e2, _ := New(e.paths, sim2, t.Logf)
	if err := e2.Boot(context.Background(), true, ""); err != nil {
		t.Fatal(err)
	}
	if e2.Running().VLAN(10) != nil || e2.PendingCommit() != nil {
		t.Fatal("reboot must come up with startup config")
	}
	_ = sim
}

func TestBootFallsBackToSafe(t *testing.T) {
	dir := t.TempDir()
	sim := mock.Lab()
	e, _ := New(Paths{Startup: dir + "/config.yaml", State: dir + "/db"}, sim, t.Logf)
	// startup config references a NIC that does not exist
	st := config.Default("vtnet0", []string{"vtnet1", "ix0"})
	st.Management.IPv4 = config.IPConfig{Mode: "static", Address: "192.168.1.10/24"}
	config.Save(dir+"/config.yaml", st)
	err := e.Boot(context.Background(), true, "")
	if err == nil || e.BootMode() != "safe" {
		t.Fatalf("expected safe mode, got %v %s", err, e.BootMode())
	}
	r := e.Running()
	if r.Management.Interface != "vtnet0" || r.Management.IPv4.Address != "192.168.1.10/24" || len(r.Ports) != 0 {
		t.Fatalf("safe config %+v", r.Management)
	}
	snap, _ := sim.Snapshot(context.Background())
	if v := snap.Get("vtnet0"); len(v.IPv4) != 1 || v.IPv4[0] != "192.168.1.10/24" {
		t.Fatal("management not configured in safe mode")
	}
}

func TestRollbackFromHistory(t *testing.T) {
	e, sim := setup(t)
	first := e.History()[0].ID
	stageVLAN10(t, e)
	e.Commit(context.Background(), "admin", "", time.Minute)
	e.Confirm("admin")
	if err := e.LoadRollback(context.Background(), first, "admin"); err != nil {
		t.Fatal(err)
	}
	e.Commit(context.Background(), "admin", "rollback", time.Minute)
	e.Confirm("admin")
	if u, _ := member(t, sim, "vtnet2"); u != 1 {
		t.Fatal("rollback not applied")
	}
}

func TestHistoryLimit(t *testing.T) {
	e, _ := setup(t)
	for i := 0; i < 14; i++ {
		e.Modify(context.Background(), "admin", func(c *config.Config) error {
			c.Port("vtnet1").Description = "rev" + string(rune('a'+i))
			return nil
		})
		if _, err := e.Commit(context.Background(), "admin", "", time.Minute); err != nil {
			t.Fatal(err)
		}
		e.Confirm("admin")
	}
	if n := len(e.History()); n != HistorySize {
		t.Fatalf("history size %d", n)
	}
}

func TestValidationOnStage(t *testing.T) {
	e, _ := setup(t)
	_, err := e.Modify(context.Background(), "admin", func(c *config.Config) error {
		c.Port("vtnet1").PVID = 999
		return nil
	})
	var ve *config.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected validation error, got %v", err)
	}
	_, err = e.Modify(context.Background(), "admin", func(c *config.Config) error {
		c.Ports = append(c.Ports, config.Port{Name: "ix9", Mode: "access", PVID: 1})
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("expected inventory error, got %v", err)
	}
}

func TestMgmtImpact(t *testing.T) {
	a := config.Default("vtnet0", []string{"vtnet1", "vtnet2"})
	a.VLANs = append(a.VLANs, config.VLAN{ID: 99})
	a.Port("vtnet1").Mode = "trunk"
	a.Port("vtnet1").PVID = 0
	a.Port("vtnet1").Native = 1
	a.Port("vtnet1").Tagged = config.VIDSet{99}
	a.Management.InBand = config.InBand{Enabled: true, VLAN: 99, IPv4: config.IPConfig{Mode: "static", Address: "10.0.0.2/24"}}
	a.ApplyDefaults()
	b := a.Clone()
	b.Port("vtnet1").Tagged = config.VIDSet{}
	im := MgmtImpact(a, b)
	if len(im) != 1 || !strings.Contains(im[0], "leaves in-band") {
		t.Fatal(im)
	}
}
