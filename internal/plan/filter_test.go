package plan

import (
	"strings"
	"testing"

	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/state"
)

func TestPFConfAndFilterPlan(t *testing.T) {
	sim := vmSim()
	c := parse(t, example)
	apply(t, sim, plan(t, sim, c, nil))
	n := c.Clone()
	n.ACLs = []config.ACL{{Name: "cam", Port: "vtnet3", Direction: "in", Default: "block", Rules: []config.ACLRule{
		{Action: "pass", Proto: "udp", Dst: "10.0.0.5", DstPort: "554", Comment: "rtsp to nvr"},
		{Action: "pass", Proto: "icmp"},
	}}}
	n.RateLimits = []config.RateLimit{{Port: "vtnet1", Ingress: 10000, Egress: 50000}}
	if err := config.Validate(n, nil); err != nil {
		t.Fatal(err)
	}
	conf := PFConf(n)
	for _, want := range []string{
		"set skip on { vtnet0 bridge0 }",
		`pass in quick on vtnet3 proto udp from any to 10.0.0.5 port 554 keep state label "rtsp to nvr"`,
		"pass in quick on vtnet3 proto icmp from any to any keep state",
		"block drop in quick on vtnet3 all",
		"pass in quick on vtnet1 all no state dnpipe 1001",
		"pass out quick on vtnet1 all no state dnpipe 1002",
		"pass quick all no state",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("pf.conf missing %q:\n%s", want, conf)
		}
	}
	p := plan(t, sim, n, c)
	s := p.Script()
	for _, want := range []string{"net.link.bridge.pfil_member=1", "kldload -n pf", "kldload -n dummynet",
		"dnctl pipe 1001 config bw 10000Kbit/s", "dnctl pipe 1002 config bw 50000Kbit/s", "pfctl -f /usr/local/etc/bsdswitch/pf.conf"} {
		if !strings.Contains(s, want) {
			t.Errorf("plan missing %q:\n%s", want, s)
		}
	}
	apply(t, sim, p)
	if p2 := plan(t, sim, n, n); !p2.Empty() {
		t.Fatalf("not idempotent:\n%s", p2.Script())
	}
	// removing everything disables pf again
	p3 := plan(t, sim, c, n)
	if !strings.Contains(p3.Script(), "pfctl -d") || !strings.Contains(p3.Script(), "pfil_member=0") {
		t.Fatal(p3.Script())
	}
}

func TestLAGAndMirror(t *testing.T) {
	sim := vmSim()
	c := parse(t, example)
	apply(t, sim, plan(t, sim, c, nil))
	n := c.Clone()
	// vtnet3 + vtnet4 become lagg0 (LACP trunk); vtnet2 becomes a span port
	var ports []config.Port
	for _, p := range n.Ports {
		if p.Name == "vtnet1" {
			ports = append(ports, p)
		}
	}
	ports = append(ports, config.Port{Name: "lagg0", Mode: "trunk", Native: 1, Tagged: config.VIDSet{10, 20}})
	n.Ports = ports
	n.LAGs = []config.LAG{{Name: "lagg0", Protocol: "lacp", Members: []string{"vtnet3", "vtnet4"}, Hash: "l2,l3"}}
	n.Mirrors = []config.Mirror{{Destination: "vtnet2"}}
	n.ApplyDefaults()
	if err := config.Validate(n, nil); err != nil {
		t.Fatal(err)
	}
	p := plan(t, sim, n, c)
	s := p.Script()
	for _, want := range []string{"ifconfig lagg0 create", "ifconfig lagg0 laggproto lacp", "ifconfig lagg0 lagghash l2,l3",
		"ifconfig bridge0 deletem vtnet3", "ifconfig lagg0 laggport vtnet3", "ifconfig lagg0 laggport vtnet4",
		"ifconfig bridge0 addm lagg0 untagged 1 tagged 10,20", "ifconfig bridge0 deletem vtnet2", "ifconfig bridge0 span vtnet2"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Index(s, "deletem vtnet3") > strings.Index(s, "laggport vtnet3") {
		t.Error("member must leave the bridge before joining the lagg")
	}
	apply(t, sim, p)
	if p2 := plan(t, sim, n, n); !p2.Empty() {
		t.Fatalf("not idempotent:\n%s", p2.Script())
	}
	// and back
	apply(t, sim, plan(t, sim, c, n))
	if p3 := plan(t, sim, c, c); !p3.Empty() {
		t.Fatalf("revert not converged:\n%s", p3.Script())
	}
}

func TestFixedMediaAndUnmanagedHost(t *testing.T) {
	fixed := state.Interface{Name: "epair1a", Driver: "epair", SupportedMedia: []string{"media 10Gbase-T mediaopt full-duplex"}}
	if fixed.SpeedSettable() {
		t.Fatal("fixed media must not be settable")
	}
	intel := state.Interface{Name: "em0", Driver: "em", SupportedMedia: []string{"media autoselect", "media 100baseTX mediaopt full-duplex"}}
	if !intel.SpeedSettable() {
		t.Fatal("em0 should be settable")
	}
	sim := vmSim()
	c := parse(t, example)
	f := false
	c.System.ManageHost = &f
	p := plan(t, sim, c, nil)
	s := p.Script()
	for _, bad := range []string{"route ", "hostname", "tzsetup", "sshd", "resolv.conf"} {
		if strings.Contains(s, bad) {
			t.Errorf("unmanaged host: unexpected %q in\n%s", bad, s)
		}
	}
	if !strings.Contains(s, "ifconfig bridge0 addm vtnet1") {
		t.Fatal("switch config must still be applied")
	}
}
