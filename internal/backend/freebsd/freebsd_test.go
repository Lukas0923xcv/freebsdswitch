package freebsd

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/mock"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/plan"
)

func TestParseIfList(t *testing.T) {
	msg := make([]byte, 16+152+20) // header + if_data + trailing sockaddr
	le := binary.LittleEndian
	le.PutUint16(msg[0:], uint16(len(msg)))
	msg[2] = 5
	msg[3] = rtmIFINFO
	le.PutUint16(msg[12:], 3) // index
	d := msg[16:]
	d[4] = 2 // LINK_STATE_UP
	le.PutUint64(d[16:], 1000000000)
	le.PutUint64(d[24:], 11) // ipackets
	le.PutUint64(d[32:], 1)  // ierrors
	le.PutUint64(d[40:], 22) // opackets
	le.PutUint64(d[64:], 1100)
	le.PutUint64(d[72:], 2200)
	le.PutUint64(d[96:], 4)  // iqdrops
	le.PutUint64(d[104:], 5) // oqdrops
	other := make([]byte, 24)
	le.PutUint16(other[0:], 24)
	other[3] = 0x0c // RTM_NEWADDR, ignored
	c := ParseIfList(append(msg, other...))
	got := c[3]
	want := backend.Counters{LinkState: 2, Baudrate: 1e9, RxPackets: 11, RxErrors: 1, TxPackets: 22, RxBytes: 1100, TxBytes: 2200, RxDrops: 4, TxDrops: 5}
	if got != want || len(c) != 1 {
		t.Fatalf("got %+v", c)
	}
}

func TestRouteGateway(t *testing.T) {
	out := "   route to: default\ndestination: default\n       mask: default\n    gateway: 192.168.1.1\n        fib: 0\n  interface: vtnet0\n"
	if g := ParseRouteGateway(out); g != "192.168.1.1" {
		t.Fatal(g)
	}
	if g := ParseRouteGateway("    gateway: fe80::1%vtnet0\n"); g != "fe80::1" {
		t.Fatal(g)
	}
}

// Every command the planner can generate for a broad config must pass
// the privilege policy.
func TestPlannerOutputPassesPolicy(t *testing.T) {
	sim := mock.Lab()
	c := config.Default("vtnet0", []string{"vtnet1", "vtnet2", "vtnet3", "igb1"})
	c.System.DNS = []string{"1.1.1.1"}
	c.System.NTP = []string{"pool.ntp.org"}
	c.System.Domain = "lab.example"
	c.VLANs = append(c.VLANs, config.VLAN{ID: 10, Name: "a"}, config.VLAN{ID: 99, Name: "m"})
	c.Port("vtnet3").Mode = "trunk"
	c.Port("vtnet3").PVID = 0
	c.Port("vtnet3").Native = 1
	c.Port("vtnet3").Tagged = config.VIDSet{10, 99}
	c.Port("vtnet2").Description = "Printer (2nd floor)"
	c.Port("igb1").Speed = "100"
	c.Port("igb1").Duplex = "full"
	c.Port("vtnet1").MaxAddr = 5
	c.Management.IPv4 = config.IPConfig{Mode: "static", Address: "192.168.1.2/24", Gateway: "192.168.1.1"}
	c.Management.SSH.PasswordAuth = false
	c.Management.InBand = config.InBand{Enabled: true, VLAN: 99, IPv4: config.IPConfig{Mode: "dhcp"}, IPv6: config.IPConfig{Mode: "none"}}
	c.StaticMACs = []config.StaticMAC{{MAC: "02:00:00:00:00:01", Port: "vtnet1", VLAN: 1}}
	c.SNMP = config.SNMP{Enabled: true, Community: "public"}
	c.LLDP.Enabled = true
	c.Syslog.Servers = []string{"192.168.1.5"}
	c.ApplyDefaults()
	if err := config.Validate(c, nil); err != nil {
		t.Fatal(err)
	}
	in, err := plan.Gather(context.Background(), sim, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := plan.Build(in)
	for _, o := range p.Ops() {
		if err := CheckOp(o); err != nil {
			t.Errorf("%s: %v", o.String(), err)
		}
	}
}

func TestPolicyRejects(t *testing.T) {
	bad := [][]string{
		{"sh", "-c", "id"},
		{"ifconfig", "vtnet0;id"},
		{"sysctl", "kern.securelevel=-1"},
		{"sysrc", "foo_enable=YES"},
		{"sysrc", "sshd_flags=$(id)"},
		{"service", "cron", "stop"},
		{"pkill", "-f", "sshd"},
		{"route", "add", "10.0.0.0/8", "1.2.3.4"},
		{"shutdown", "-h", "+5"},
		{"ifconfig", "vtnet0", "description", "a`id`"},
		{"pkg", "install", "-y", "nmap"},
		{"tcpdump", "-n", "-i", "vtnet1", "-c", "10", "-s", "0", "-w", "/etc/passwd"},
	}
	for _, a := range bad {
		if err := CheckOp(backend.Op{Argv: a}); err == nil {
			t.Errorf("%v should be rejected", a)
		}
	}
	if err := CheckOp(backend.Op{Path: "/etc/master.passwd", Content: "x"}); err == nil {
		t.Error("file write should be rejected")
	}
	good := [][]string{
		{"ifconfig", "bridge0", "addm", "vtnet1", "untagged", "1"},
		{"ifconfig", "bridge0.99", "create"},
		{"sysrc", "-x", "ifconfig_vtnet1"},
		{"tcpdump", "-n", "-i", "vtnet1", "-c", "1000", "-s", "0", "-w", "/var/db/bsdswitch/capture/0123456789abcdef.pcap", "port", "53"},
	}
	for _, a := range good {
		if err := CheckOp(backend.Op{Argv: a}); err != nil {
			t.Errorf("%v: %v", a, err)
		}
	}
	_ = strings.TrimSpace
}
