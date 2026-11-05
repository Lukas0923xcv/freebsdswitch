package console

import (
	"bufio"
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/api"
	"github.com/Lukas0923xcv/freebsdswitch/internal/auth"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/mock"
	"github.com/Lukas0923xcv/freebsdswitch/internal/client"
	"github.com/Lukas0923xcv/freebsdswitch/internal/commit"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/logbuf"
	"github.com/Lukas0923xcv/freebsdswitch/internal/stats"
)

type env struct {
	c   *Console
	out *bytes.Buffer
	eng *commit.Engine
	am  *auth.Manager
	dir string
}

func setup(t *testing.T, input string) *env {
	auth.BcryptCost = 4
	dir := t.TempDir()
	sim := mock.Lab()
	eng, _ := commit.New(commit.Paths{Startup: dir + "/etc/config.yaml", State: dir + "/db"}, sim, t.Logf)
	eng.Persist = false
	config.Save(dir+"/etc/config.yaml", config.Default("vtnet0", []string{"vtnet1", "vtnet2"}))
	os.WriteFile(dir+"/db/wizard-pending", nil, 0o600)
	eng.Boot(context.Background(), true, "")
	am, _ := auth.Open(dir + "/etc/users.yaml")
	pw, _ := am.EnsureAdmin()
	os.WriteFile(dir+"/db/initial-admin-password", []byte(pw+"\n"), 0o600)
	srv := api.New(api.Options{Engine: eng, Auth: am, Stats: stats.New(sim), Backend: sim, Logs: logbuf.New(10, nil), StateDir: dir + "/db",
		Factory: func(ctx context.Context) (*config.Config, error) {
			return config.Default("vtnet0", []string{"vtnet1"}), nil
		}})
	ts := httptest.NewServer(api.LocalSession(srv))
	t.Cleanup(ts.Close)
	out := &bytes.Buffer{}
	c := &Console{In: bufio.NewReader(strings.NewReader(input)), Out: out, API: client.Remote(ts.URL, "", false), Local: sim,
		ConfigDir: dir + "/etc", StateDir: dir + "/db", Shell: "/bin/true",
		Exec: func(string, ...string) error { return nil }, Sleep: func(time.Duration) {}}
	return &env{c: c, out: out, eng: eng, am: am, dir: dir}
}

func TestWizard(t *testing.T) {
	// mgmt=#2 (vtnet1), hostname, static IPv4, gateway, dns, ipv6 none, apply, ENTER
	e := setup(t, "2\nsw9\nn\n192.168.5.2/24\n192.168.5.1\n9.9.9.9\nnone\ny\n\n")
	if !e.c.needsWizard() {
		t.Fatal("wizard-pending marker not honoured")
	}
	if err := e.c.Wizard(); err != nil {
		t.Fatal(err, e.out.String())
	}
	r := e.eng.Running()
	if r.Management.Interface != "vtnet1" || r.Management.IPv4.Address != "192.168.5.2/24" || r.System.Hostname != "sw9" {
		t.Fatalf("%+v", r.Management)
	}
	if r.Port("vtnet0") == nil || r.Port("vtnet1") != nil || len(r.Ports) != 6 {
		t.Fatalf("ports %+v", r.Ports)
	}
	if e.eng.StartupDiffers() {
		t.Fatal("wizard must save")
	}
	if e.c.needsWizard() {
		t.Fatal("wizard marker not removed")
	}
	if !strings.Contains(e.out.String(), "Web UI: https://192.168.5.2/") {
		t.Fatal(e.out.String())
	}
	if !strings.Contains(e.out.String(), "Login: admin / ") {
		t.Fatal("initial password not shown")
	}
}

func TestSetMgmtIPAndRollback(t *testing.T) {
	e := setup(t, "n\n10.0.0.2/24\n10.0.0.1\n\nnone\n1\ny\ny\n")
	if err := e.c.SetMgmtIP(); err != nil {
		t.Fatal(err)
	}
	if e.eng.Running().Management.IPv4.Address != "10.0.0.2/24" {
		t.Fatal("not applied")
	}
	if err := e.c.Rollback(); err != nil {
		t.Fatal(err, e.out.String())
	}
	if e.eng.Running().Management.IPv4.Mode != "dhcp" {
		t.Fatalf("rollback failed: %+v", e.eng.Running().Management.IPv4)
	}
}

func TestResetPasswordAndFactory(t *testing.T) {
	e := setup(t, "admin\nyes\n")
	if err := e.c.ResetPassword(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "New one-time password for admin") {
		t.Fatal(e.out.String())
	}
	e.eng.Modify(context.Background(), "x", func(c *config.Config) error { c.System.Hostname = "changed"; return nil })
	if err := e.c.FactoryReset(); err != nil {
		t.Fatal(err)
	}
	if e.eng.Running().System.Hostname != "bsdswitch" || len(e.eng.Running().Ports) != 1 {
		t.Fatal("factory reset not applied")
	}
}

func TestMenuHeader(t *testing.T) {
	e := setup(t, "0\n")
	e.c.Menu()
	o := e.out.String()
	for _, w := range []string{"Management: vtnet0", "1) Assign management interface", "Initial web UI login: admin /"} {
		if !strings.Contains(o, w) {
			t.Errorf("menu missing %q", w)
		}
	}
}
