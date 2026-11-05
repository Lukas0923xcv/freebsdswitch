package state

import (
	"os"
	"reflect"
	"testing"

	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

var cloners = ParseCloners("bridge carp epair gif gre lagg lo pflog pfsync stf tap tun vlan vxlan wg")

func load(t *testing.T) []Interface {
	b, err := os.ReadFile("testdata/ifconfig_vm.txt")
	if err != nil {
		t.Fatal(err)
	}
	ifs, err := ParseIfconfig(string(b), cloners)
	if err != nil {
		t.Fatal(err)
	}
	return ifs
}

func TestParseIfconfig(t *testing.T) {
	ifs := load(t)
	snap := &Snapshot{Interfaces: ifs}
	if len(ifs) != 7 {
		t.Fatalf("got %d interfaces", len(ifs))
	}
	v0 := snap.Get("vtnet0")
	if v0.MAC != "bc:24:11:aa:00:00" || v0.MTU != 1500 || !v0.AdminUp() || !v0.LinkUp() {
		t.Errorf("vtnet0: %+v", v0)
	}
	if v0.Driver != "vtnet" || !v0.IsVirtio() || v0.SpeedSettable() {
		t.Errorf("vtnet0 driver: %+v", v0)
	}
	if v0.SpeedMbps() != 10000 || v0.Duplex() != "full" {
		t.Errorf("vtnet0 speed %d %s", v0.SpeedMbps(), v0.Duplex())
	}
	if !reflect.DeepEqual(v0.IPv4, []string{"192.168.1.10/24"}) || v0.IPv6[0] != "fe80::be24:11ff:feaa:0/64" {
		t.Errorf("addresses %v %v", v0.IPv4, v0.IPv6)
	}
	if !v0.HasOption("LRO") || !v0.HasCap("JUMBO_MTU") {
		t.Errorf("caps %v / %v", v0.Options, v0.Capabilities)
	}
	if snap.Get("vtnet1").Description != "PC-1" {
		t.Error("description")
	}
	em := snap.Get("em0")
	if em.AdminUp() || em.LinkUp() || !em.SpeedSettable() || em.Status != "no carrier" || len(em.SupportedMedia) != 7 {
		t.Errorf("em0: %+v", em)
	}
	if em.Media != "Ethernet 1000baseT <full-duplex>" {
		t.Errorf("em0 media %q", em.Media)
	}
	eth := snap.Ethernet()
	var names []string
	for _, e := range eth {
		names = append(names, e.Name)
	}
	if !reflect.DeepEqual(names, []string{"vtnet0", "vtnet1", "em0"}) {
		t.Errorf("ethernet = %v", names)
	}
	if ok, phys := snap.Lookup("epair0a"); !ok || phys {
		t.Error("epair should exist and not be physical")
	}
	vl := snap.Get("bridge0.99")
	if !vl.Cloned || vl.VLANTag != 99 || vl.VLANParent != "bridge0" {
		t.Errorf("vlan: %+v", vl)
	}
}

func TestParseBridge(t *testing.T) {
	snap := &Snapshot{Interfaces: load(t)}
	b := snap.Get("bridge0").Bridge
	if b == nil {
		t.Fatal("no bridge state")
	}
	if b.ID != "58:9c:fc:10:ff:b6" || b.Priority != 32768 || b.HelloTime != 2 || b.FwdDelay != 15 ||
		b.MaxAge != 20 || b.HoldCount != 6 || b.Proto != "rstp" || b.MaxAddr != 2000 || b.Timeout != 300 {
		t.Errorf("bridge params %+v", b)
	}
	if !b.VLANFilter() || !b.IsRoot() {
		t.Errorf("flags %v root %v", b.Flags, b.IsRoot())
	}
	if len(b.Members) != 3 || !reflect.DeepEqual(b.Spans, []string{"vtnet3"}) {
		t.Fatalf("members %+v spans %v", b.Members, b.Spans)
	}
	m4 := b.Member("vtnet4")
	if m4.PortNo != 5 || m4.Priority != 128 || m4.PathCost != 2000 || m4.Proto != "rstp" ||
		m4.Role != "designated" || m4.State != "forwarding" || m4.Untagged != 1 || !m4.Tagged.Equal(config.VIDSet{10, 20}) {
		t.Errorf("vtnet4 %+v", m4)
	}
	if !m4.Has("STP") || !m4.Has("AUTOEDGE") || m4.Has("EDGE") {
		t.Errorf("vtnet4 flags %v", m4.Flags)
	}
	m2 := b.Member("vtnet2")
	if m2.IfMaxAddr != 10 || !m2.Has("EDGE") || m2.Untagged != 10 || len(m2.Tagged) != 0 {
		t.Errorf("vtnet2 %+v", m2)
	}
	m1 := b.Member("vtnet1")
	if m1.Has("STP") || m1.Untagged != 1 || m1.State != "" {
		t.Errorf("vtnet1 %+v", m1)
	}
}

func TestParseBridgeAddr(t *testing.T) {
	b, _ := os.ReadFile("testdata/bridge_addr.txt")
	e := ParseBridgeAddr(string(b))
	want := []MACEntry{
		{MAC: "bc:24:11:bb:00:01", VLAN: 1, Port: "vtnet1", Expire: 287},
		{MAC: "bc:24:11:bb:00:02", VLAN: 10, Port: "vtnet2", Expire: 120},
		{MAC: "00:11:22:33:44:55", VLAN: 1, Port: "vtnet1", Static: true},
	}
	if !reflect.DeepEqual(e, want) {
		t.Errorf("got %+v", e)
	}
}

func TestMediaFor(t *testing.T) {
	snap := &Snapshot{Interfaces: load(t)}
	sup := snap.Get("em0").SupportedMedia
	cases := []struct {
		speed, duplex string
		want          []string
		ok            bool
	}{
		{"auto", "auto", []string{"media", "autoselect"}, true},
		{"1000", "full", []string{"media", "1000baseT", "mediaopt", "full-duplex"}, true},
		{"100", "half", []string{"media", "100baseTX"}, true},
		{"10", "full", []string{"media", "10baseT/UTP", "mediaopt", "full-duplex"}, true},
		{"10000", "full", nil, false},
	}
	for _, c := range cases {
		got, ok := MediaFor(sup, c.speed, c.duplex)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s/%s: %v %v", c.speed, c.duplex, got, ok)
		}
	}
	if MediaSpeed("2500Base-T <full-duplex>") != 2500 || MediaSpeed("25GBase-SR") != 25000 || MediaSpeed("autoselect") != 0 {
		t.Error("MediaSpeed")
	}
}
