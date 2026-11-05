package cli

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Lukas0923xcv/freebsdswitch/internal/api"
	"github.com/Lukas0923xcv/freebsdswitch/internal/auth"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/mock"
	"github.com/Lukas0923xcv/freebsdswitch/internal/client"
	"github.com/Lukas0923xcv/freebsdswitch/internal/commit"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/logbuf"
	"github.com/Lukas0923xcv/freebsdswitch/internal/stats"
)

func setup(t *testing.T) (*client.Client, *commit.Engine, *mock.Sim) {
	auth.BcryptCost = 4
	dir := t.TempDir()
	sim := mock.Lab()
	eng, _ := commit.New(commit.Paths{Startup: dir + "/c.yaml", State: dir + "/db"}, sim, t.Logf)
	eng.Persist = false
	config.Save(dir+"/c.yaml", config.Default("vtnet0", []string{"vtnet1", "vtnet2", "vtnet3", "vtnet4"}))
	eng.Boot(context.Background(), true, "")
	am, _ := auth.Open(dir + "/u.yaml")
	am.EnsureAdmin()
	srv := api.New(api.Options{Engine: eng, Auth: am, Stats: stats.New(sim), Backend: sim, Logs: logbuf.New(10, nil), StateDir: dir})
	ts := httptest.NewServer(api.LocalSession(srv))
	t.Cleanup(ts.Close)
	return client.Remote(ts.URL, "", false), eng, sim
}

func run(t *testing.T, c *client.Client, line string) string {
	t.Helper()
	var out bytes.Buffer
	if err := Run(c, &out, strings.NewReader(""), strings.Fields(line)); err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return out.String()
}

func TestConfigureCommitConfirmSave(t *testing.T) {
	c, eng, _ := setup(t)
	var out bytes.Buffer
	err := Run(c, &out, strings.NewReader(""), []string{"configure",
		"-c", "vlan 10", "-c", "name office", "-c", "exit",
		"-c", "interface vtnet2", "-c", "description Printer 2nd floor", "-c", "switchport access vlan 10", "-c", "spanning-tree portfast", "-c", "exit",
		"-c", "interface vtnet4", "-c", "switchport mode trunk", "-c", "switchport trunk allowed vlan 10,20",
		"-c", "switchport trunk allowed vlan remove 20", "-c", "exit",
		"-c", "spanning-tree priority 4096", "-c", "hostname core-sw"})
	if err != nil {
		t.Fatal(err, out.String())
	}
	diff := run(t, c, "show configuration diff")
	for _, w := range []string{"ifconfig bridge0 ifuntagged vtnet2 10", "ifconfig bridge0 +iftagged vtnet4 10", "ifconfig bridge0 priority 4096", "hostname core-sw"} {
		if !strings.Contains(diff, w) {
			t.Errorf("diff missing %q:\n%s", w, diff)
		}
	}
	if !strings.Contains(run(t, c, "commit confirm 30 comment cli test"), "rolled back automatically in 30s") {
		t.Fatal("commit output")
	}
	if !strings.Contains(run(t, c, "show commit"), "waiting for confirmation") {
		t.Fatal("show commit")
	}
	run(t, c, "confirm")
	run(t, c, "write memory")
	r := eng.Running()
	if r.Port("vtnet2").PVID != 10 || r.Port("vtnet2").Description != "Printer 2nd floor" || !r.Port("vtnet4").Tagged.Equal(config.VIDSet{10}) || r.System.Hostname != "core-sw" {
		t.Fatalf("running config not as expected: %+v", r.Ports)
	}
	if eng.StartupDiffers() {
		t.Fatal("write memory did not save")
	}
	vl := run(t, c, "show vlan")
	if !strings.Contains(vl, "office") || !strings.Contains(vl, "VLAN0020") {
		t.Fatal(vl)
	}
	if !strings.Contains(run(t, c, "show history"), "cli test") {
		t.Fatal("history")
	}
}

func TestConfigureRejectsInvalid(t *testing.T) {
	c, _, _ := setup(t)
	var out bytes.Buffer
	err := Run(c, &out, strings.NewReader(""), []string{"configure", "-c", "interface vtnet1", "-c", "switchport trunk allowed vlan 10"})
	if err == nil || !strings.Contains(err.Error(), "not a trunk") {
		t.Fatalf("expected error, got %v", err)
	}
	err = Run(c, &out, strings.NewReader(""), []string{"configure", "-c", "interface vtnet1", "-c", "spanning-tree port-priority 100"})
	if err == nil || !strings.Contains(err.Error(), "steps of 16") {
		t.Fatalf("expected validation error, got %v", err)
	}
	err = Run(c, &out, strings.NewReader(""), []string{"configure", "-c", "interface em7"})
	if err == nil {
		t.Fatal("unknown interface accepted")
	}
}

func TestShowCommands(t *testing.T) {
	c, _, sim := setup(t)
	sim.Learn("bridge0", "02:00:00:00:00:99", 1, "vtnet3")
	for _, cmd := range []string{"show interfaces", "show interfaces counters", "show mac address-table vlan 1",
		"show spanning-tree", "show running-config", "show system", "show capabilities", "show audit"} {
		out := run(t, c, cmd)
		if len(out) < 10 {
			t.Errorf("%s: output too short: %q", cmd, out)
		}
	}
	if !strings.Contains(run(t, c, "show mac address-table interface vtnet3"), "02:00:00:00:00:99") {
		t.Fatal("mac table")
	}
}

func TestInteractiveConfigure(t *testing.T) {
	c, eng, _ := setup(t)
	var out bytes.Buffer
	in := "interface vtnet1\nshutdown\nend\n"
	if err := Run(c, &out, strings.NewReader(in), []string{"configure"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "bsdswitch(config-if-vtnet1)#") {
		t.Fatalf("prompt missing: %s", out.String())
	}
	cand, staged := eng.Candidate()
	if !staged || !cand.Port("vtnet1").Shutdown {
		t.Fatal("not staged")
	}
}
