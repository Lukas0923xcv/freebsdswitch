package plan

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/mock"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

const example = `
version: 1
system: { hostname: sw01, timezone: Europe/Vienna, dns: [192.168.1.1], ntp: [pool.ntp.org] }
management:
  interface: vtnet0
  ipv4: { mode: static, address: 192.168.1.10/24, gateway: 192.168.1.1 }
  ipv6: { mode: none }
  ssh: { enabled: true, password_auth: true }
bridge:
  name: bridge0
  mac_aging: 300
  stp: { enabled: true, mode: rstp, priority: 32768 }
vlans: [{ id: 1, name: default }, { id: 10, name: office }, { id: 20, name: iot }]
ports:
  - { name: vtnet1, description: PC-1, mode: access, pvid: 1, stp: { edge: true } }
  - { name: vtnet2, description: PC-2, mode: access, pvid: 10, stp: { edge: true } }
  - { name: vtnet3, description: Camera, mode: access, pvid: 20, stp: { edge: true } }
  - { name: vtnet4, description: Uplink, mode: trunk, native: 1, tagged: "10,20" }
static_macs:
  - { mac: "00:11:22:33:44:55", port: vtnet1, vlan: 1 }
`

func parse(t *testing.T, s string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Validate(c, nil); err != nil {
		t.Fatal(err)
	}
	return c
}

func vmSim() *mock.Sim {
	return mock.New(mock.NIC{Name: "vtnet0", Link: true}, mock.NIC{Name: "vtnet1", Link: true},
		mock.NIC{Name: "vtnet2", Link: true}, mock.NIC{Name: "vtnet3", Link: true}, mock.NIC{Name: "vtnet4", Link: true})
}

func plan(t *testing.T, sim *mock.Sim, desired, prev *config.Config) *Plan {
	t.Helper()
	in, err := Gather(context.Background(), sim, desired, prev)
	if err != nil {
		t.Fatal(err)
	}
	return Build(in)
}

func apply(t *testing.T, sim *mock.Sim, p *Plan) {
	t.Helper()
	if _, err := Apply(context.Background(), sim, p.Ops(), nil); err != nil {
		t.Fatal(err)
	}
}

// TestExampleGolden documents the exact commands for the README example.
// Run with UPDATE_GOLDEN=1 to rewrite testdata/example.sh.
func TestExampleGolden(t *testing.T) {
	sim := vmSim()
	c := parse(t, example)
	p := plan(t, sim, c, nil)
	got := p.Script()
	if os.Getenv("UPDATE_GOLDEN") != "" {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile("testdata/example.sh", []byte(got), 0o644)
	}
	want, err := os.ReadFile("testdata/example.sh")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("plan mismatch:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

func TestIdempotent(t *testing.T) {
	sim := vmSim()
	c := parse(t, example)
	apply(t, sim, plan(t, sim, c, nil))
	p := plan(t, sim, c, c)
	if !p.Empty() {
		t.Fatalf("second plan not empty:\n%s", p.Script())
	}
}

func TestIncrementalChanges(t *testing.T) {
	sim := vmSim()
	c := parse(t, example)
	apply(t, sim, plan(t, sim, c, nil))

	n := c.Clone()
	n.Port("vtnet2").PVID = 20
	n.VLANs = append(n.VLANs, config.VLAN{ID: 30, Name: "guest"})
	n.Port("vtnet4").Tagged = config.VIDSet{20, 30}
	n.Bridge.STP.Priority = 4096
	n.StaticMACs = nil
	n.Port("vtnet3").Shutdown = true
	if err := config.Validate(n, nil); err != nil {
		t.Fatal(err)
	}
	p := plan(t, sim, n, c)
	got := strings.Join(p.Commands(), "\n")
	want := strings.Join([]string{
		"# Switch port settings",
		"ifconfig vtnet3 down",
		"# Bridge bridge0",
		"ifconfig bridge0 priority 4096",
		"ifconfig bridge0 ifuntagged vtnet2 20",
		"ifconfig bridge0 -iftagged vtnet4 10",
		"ifconfig bridge0 +iftagged vtnet4 30",
		"# Static MAC address entries",
		"ifconfig bridge0 deladdr 00:11:22:33:44:55 vlan 1",
	}, "\n")
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	apply(t, sim, p)
	if p2 := plan(t, sim, n, n); !p2.Empty() {
		t.Fatalf("not converged:\n%s", p2.Script())
	}
	// and back (this is what a rollback does)
	back := plan(t, sim, c, n)
	apply(t, sim, back)
	if p3 := plan(t, sim, c, c); !p3.Empty() {
		t.Fatalf("rollback did not converge:\n%s", p3.Script())
	}
}

func TestPortRemovalAndMgmtMove(t *testing.T) {
	sim := vmSim()
	c := parse(t, example)
	apply(t, sim, plan(t, sim, c, nil))
	n := c.Clone()
	// vtnet1 becomes management, vtnet0 becomes a switch port
	n.Management.Interface = "vtnet1"
	n.StaticMACs = nil
	var ports []config.Port
	for _, p := range n.Ports {
		if p.Name != "vtnet1" {
			ports = append(ports, p)
		}
	}
	n.Ports = append(ports, config.Port{Name: "vtnet0", Mode: "access", PVID: 1})
	n.ApplyDefaults()
	if err := config.Validate(n, nil); err != nil {
		t.Fatal(err)
	}
	p := plan(t, sim, n, c)
	s := p.Script()
	for _, want := range []string{
		"ifconfig bridge0 deletem vtnet1",
		"ifconfig vtnet1 inet 192.168.1.10/24 alias",
		"ifconfig vtnet0 inet 192.168.1.10/24 -alias",
		"ifconfig bridge0 addm vtnet0 untagged 1",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Index(s, "vtnet0 inet 192.168.1.10/24 -alias") > strings.Index(s, "addm vtnet0") {
		t.Error("address removal must precede addm")
	}
	apply(t, sim, p)
	if p2 := plan(t, sim, n, n); !p2.Empty() {
		t.Fatalf("not converged:\n%s", p2.Script())
	}
}

func TestInBandAndTrunkConversion(t *testing.T) {
	sim := vmSim()
	c := parse(t, example)
	apply(t, sim, plan(t, sim, c, nil))
	n := c.Clone()
	n.VLANs = append(n.VLANs, config.VLAN{ID: 99, Name: "mgmt"})
	n.Port("vtnet4").Tagged = config.VIDSet{10, 20, 99}
	n.Management.InBand = config.InBand{Enabled: true, VLAN: 99,
		IPv4: config.IPConfig{Mode: "static", Address: "10.99.0.2/24"}, IPv6: config.IPConfig{Mode: "none"}}
	n.Port("vtnet2").SetMembership(99, config.MemberTagged)
	if err := config.Validate(n, nil); err != nil {
		t.Fatal(err)
	}
	p := plan(t, sim, n, c)
	s := p.Script()
	for _, want := range []string{
		"ifconfig bridge0 +iftagged vtnet4 99",
		"ifconfig bridge0 +iftagged vtnet2 99",
		"ifconfig bridge0.99 create",
		"ifconfig bridge0.99 inet 10.99.0.2/24 alias",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	apply(t, sim, p)
	if p2 := plan(t, sim, n, n); !p2.Empty() {
		t.Fatalf("not converged:\n%s", p2.Script())
	}
	apply(t, sim, plan(t, sim, c, n))
	snap, _ := sim.Snapshot(context.Background())
	if snap.Get("bridge0.99") != nil {
		t.Error("vlan interface not destroyed")
	}
	if p3 := plan(t, sim, c, c); !p3.Empty() {
		t.Fatalf("not converged after disable:\n%s", p3.Script())
	}
}

func TestSpeedOnVirtioWarns(t *testing.T) {
	sim := vmSim()
	c := parse(t, example)
	c.Port("vtnet1").Speed = "1000"
	c.Port("vtnet1").Duplex = "full"
	p := plan(t, sim, c, nil)
	found := false
	for _, w := range p.Warnings {
		if strings.Contains(w, "n/a") {
			found = true
		}
	}
	if !found || strings.Contains(p.Script(), "media") {
		t.Fatalf("expected n/a warning and no media command: %v\n%s", p.Warnings, p.Script())
	}
}

func TestSpeedOnIntel(t *testing.T) {
	sim := mock.New(mock.NIC{Name: "em0", Link: true, Speeds: []int{10, 100, 1000}},
		mock.NIC{Name: "em1", Link: true, Speeds: []int{10, 100, 1000}})
	c := config.Default("em0", []string{"em1"})
	c.Port("em1").Speed = "100"
	c.Port("em1").Duplex = "full"
	p := plan(t, sim, c, nil)
	if !strings.Contains(p.Script(), "ifconfig em1 media 100baseTX mediaopt full-duplex") {
		t.Fatal(p.Script())
	}
	if !strings.Contains(p.Script(), "ifconfig em1 -lro -tso -vlanhwfilter") {
		t.Fatal("offloads", p.Script())
	}
	apply(t, sim, p)
	if p2 := plan(t, sim, c, c); !p2.Empty() {
		t.Fatalf("not converged:\n%s", p2.Script())
	}
}

func TestMissingPortSkipped(t *testing.T) {
	sim := vmSim()
	c := parse(t, example)
	c.Ports = append(c.Ports, config.Port{Name: "ix0", Mode: "access", PVID: 1})
	c.ApplyDefaults()
	p := plan(t, sim, c, nil)
	if strings.Contains(p.Script(), "ix0") {
		t.Fatal("missing port should be skipped")
	}
	if len(p.Warnings) == 0 {
		t.Fatal("expected warning")
	}
}

func TestSTPDisabled(t *testing.T) {
	sim := vmSim()
	c := parse(t, example)
	apply(t, sim, plan(t, sim, c, nil))
	n := c.Clone()
	n.Bridge.STP.Enabled = false
	p := plan(t, sim, n, c)
	if strings.Count(p.Script(), "-stp") != 4 {
		t.Fatal(p.Script())
	}
	apply(t, sim, p)
	if p2 := plan(t, sim, n, n); !p2.Empty() {
		t.Fatalf("not converged:\n%s", p2.Script())
	}
}
