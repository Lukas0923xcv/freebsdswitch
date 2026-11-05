package config

import (
	"strings"
	"testing"
)

const exampleYAML = `
version: 1
system:
  hostname: sw01
  timezone: Europe/Vienna
  dns: [192.168.1.1]
  ntp: [pool.ntp.org]
management:
  interface: vtnet0
  ipv4: { mode: static, address: 192.168.1.10/24, gateway: 192.168.1.1 }
  ipv6: { mode: slaac }
  ssh: { enabled: true, password_auth: true }
bridge:
  name: bridge0
  mac_aging: 300
  stp: { enabled: true, mode: rstp, priority: 32768 }
vlans:
  - { id: 1, name: default }
  - { id: 10, name: office }
  - { id: 20, name: iot }
ports:
  - { name: vtnet1, description: PC-1, mode: access, pvid: 1, stp: { edge: true } }
  - { name: vtnet2, description: PC-2, mode: access, pvid: 10, stp: { edge: true } }
  - { name: vtnet3, description: Camera, mode: access, pvid: 20, stp: { edge: true } }
  - { name: vtnet4, description: Uplink, mode: trunk, native: 1, tagged: "10,20" }
static_macs:
  - { mac: "00:11:22:33:44:55", port: vtnet1, vlan: 1 }
`

func mustParse(t *testing.T, s string) *Config {
	t.Helper()
	c, err := Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseExample(t *testing.T) {
	c := mustParse(t, exampleYAML)
	if err := Validate(c, nil); err != nil {
		t.Fatal(err)
	}
	if c.Bridge.MaxAddresses != 2000 || c.Bridge.MTU != 1500 || c.Bridge.STP.HelloTime != 2 {
		t.Errorf("defaults not applied: %+v", c.Bridge)
	}
	p := c.Port("vtnet4")
	if p.Mode != ModeTrunk || !p.Tagged.Equal(VIDSet{10, 20}) || p.Native != 1 {
		t.Errorf("trunk parsed wrong: %+v", p)
	}
	if p.STP.Priority != 128 {
		t.Errorf("port priority default: %d", p.STP.Priority)
	}
}

func TestRoundTrip(t *testing.T) {
	c := mustParse(t, exampleYAML)
	b, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	c2 := mustParse(t, string(b))
	b2, _ := c2.Marshal()
	if string(b) != string(b2) {
		t.Errorf("not stable:\n%s\n---\n%s", b, b2)
	}
	if !strings.Contains(string(b), `tagged: 10,20`) {
		t.Errorf("tagged not compact: %s", b)
	}
}

func TestExplicitZeroBridgePriority(t *testing.T) {
	c := mustParse(t, strings.Replace(exampleYAML, "priority: 32768", "priority: 0", 1))
	if c.Bridge.STP.Priority != 0 {
		t.Fatalf("explicit 0 priority lost: %d", c.Bridge.STP.Priority)
	}
	c = mustParse(t, strings.Replace(exampleYAML, ", priority: 32768", "", 1))
	if c.Bridge.STP.Priority != 32768 {
		t.Fatalf("default priority: %d", c.Bridge.STP.Priority)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	_, err := Parse([]byte("version: 1\nbogus: 1\n"))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestVIDSet(t *testing.T) {
	cases := map[string]string{
		"10,20,30-33": "10,20,30-33",
		"5,4,3,3":     "3-5",
		"":            "",
		"none":        "",
		"1, 2 ,4":     "1-2,4",
	}
	for in, want := range cases {
		s, err := ParseVIDSet(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if s.String() != want {
			t.Errorf("%q => %q want %q", in, s.String(), want)
		}
	}
	all, _ := ParseVIDSet("all")
	if len(all) != 4094 {
		t.Errorf("all = %d", len(all))
	}
	for _, bad := range []string{"0", "4095", "10-5", "x", "1-a"} {
		if _, err := ParseVIDSet(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestVIDSetYAMLList(t *testing.T) {
	c := mustParse(t, strings.Replace(exampleYAML, `tagged: "10,20"`, `tagged: [20, 10]`, 1))
	if !c.Port("vtnet4").Tagged.Equal(VIDSet{10, 20}) {
		t.Fatal(c.Port("vtnet4").Tagged)
	}
}

func expectErr(t *testing.T, c *Config, path string) {
	t.Helper()
	err := Validate(c, nil)
	if err == nil {
		t.Fatalf("expected error at %s", path)
	}
	ve := err.(*ValidationError)
	for _, e := range ve.Errors {
		if strings.HasPrefix(e.Path, path) {
			return
		}
	}
	t.Fatalf("expected error at %s, got %v", path, err)
}

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		mod  func(c *Config)
		path string
	}{
		{"vlan range", func(c *Config) { c.VLANs = append(c.VLANs, VLAN{ID: 4095}) }, "vlans[3].id"},
		{"vlan zero", func(c *Config) { c.VLANs = append(c.VLANs, VLAN{ID: 0}) }, "vlans[3].id"},
		{"duplicate vlan", func(c *Config) { c.VLANs = append(c.VLANs, VLAN{ID: 10}) }, "vlans[3].id"},
		{"undefined pvid", func(c *Config) { c.Port("vtnet1").PVID = 99 }, "ports[vtnet1].pvid"},
		{"undefined tagged", func(c *Config) { c.Port("vtnet4").Tagged = VIDSet{10, 99} }, "ports[vtnet4].tagged"},
		{"native also tagged", func(c *Config) { c.Port("vtnet4").Tagged = VIDSet{1, 10} }, "ports[vtnet4].tagged"},
		{"access with tagged", func(c *Config) { c.Port("vtnet1").Tagged = VIDSet{10} }, "ports[vtnet1].tagged"},
		{"trunk with pvid", func(c *Config) { c.Port("vtnet4").PVID = 10 }, "ports[vtnet4].pvid"},
		{"empty trunk", func(c *Config) { p := c.Port("vtnet4"); p.Native = 0; p.Tagged = VIDSet{} }, "ports[vtnet4]"},
		{"mtu mismatch", func(c *Config) { c.Port("vtnet1").MTU = 9000 }, "ports[vtnet1].mtu"},
		{"bridge mtu", func(c *Config) { c.Bridge.MTU = 100 }, "bridge.mtu"},
		{"mgmt is port", func(c *Config) { c.Management.Interface = "vtnet1" }, "ports[vtnet1]"},
		{"bad ipv4", func(c *Config) { c.Management.IPv4.Address = "300.1.1.1/24" }, "management.ipv4.address"},
		{"ipv4 no prefix", func(c *Config) { c.Management.IPv4.Address = "192.168.1.10" }, "management.ipv4.address"},
		{"network address", func(c *Config) { c.Management.IPv4.Address = "192.168.1.0/24" }, "management.ipv4.address"},
		{"gw outside", func(c *Config) { c.Management.IPv4.Gateway = "10.0.0.1" }, "management.ipv4.gateway"},
		{"v6 in v4", func(c *Config) { c.Management.IPv4.Address = "2001:db8::1/64" }, "management.ipv4.address"},
		{"slaac v4", func(c *Config) { c.Management.IPv4 = IPConfig{Mode: "slaac"} }, "management.ipv4.mode"},
		{"hostname", func(c *Config) { c.System.Hostname = "bad_host!" }, "system.hostname"},
		{"dns", func(c *Config) { c.System.DNS = []string{"x"} }, "system.dns[0]"},
		{"tz traversal", func(c *Config) { c.System.Timezone = "../../etc/passwd" }, "system.timezone"},
		{"stp prio", func(c *Config) { c.Bridge.STP.Priority = 1000 }, "bridge.stp.priority"},
		{"stp timers", func(c *Config) { c.Bridge.STP.MaxAge = 40; c.Bridge.STP.FwdDelay = 4 }, "bridge.stp.max_age"},
		{"port prio", func(c *Config) { c.Port("vtnet1").STP.Priority = 100 }, "ports[vtnet1].stp.priority"},
		{"description shell", func(c *Config) { c.Port("vtnet1").Description = "a; rm -rf /" }, "ports[vtnet1].description"},
		{"bad mac", func(c *Config) { c.StaticMACs[0].MAC = "zz:11:22:33:44:55" }, "static_macs[0].mac"},
		{"multicast mac", func(c *Config) { c.StaticMACs[0].MAC = "01:00:5e:00:00:01" }, "static_macs[0].mac"},
		{"static mac wrong vlan", func(c *Config) { c.StaticMACs[0].VLAN = 10 }, "static_macs[0].vlan"},
		{"inband undefined", func(c *Config) {
			c.Management.InBand = InBand{Enabled: true, VLAN: 99, IPv4: IPConfig{Mode: "static", Address: "10.0.0.2/24"}, IPv6: IPConfig{Mode: "none"}}
		}, "management.inband.vlan"},
		{"inband no addr", func(c *Config) {
			c.Management.InBand = InBand{Enabled: true, VLAN: 10, IPv4: IPConfig{Mode: "none"}, IPv6: IPConfig{Mode: "none"}}
		}, "management.inband"},
		{"half duplex gig", func(c *Config) { p := c.Port("vtnet1"); p.Speed = "1000"; p.Duplex = "half" }, "ports[vtnet1].duplex"},
		{"duplex auto speed", func(c *Config) { c.Port("vtnet1").Duplex = "full" }, "ports[vtnet1].duplex"},
		{"bad ifname", func(c *Config) {
			c.Ports = append(c.Ports, Port{Name: "eth0;ls", Mode: "access", PVID: 1, Speed: "auto", Duplex: "auto", STP: PortSTP{Priority: 128}, Offloads: "safe"})
		}, "ports[eth0;ls]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := mustParse(t, exampleYAML)
			tc.mod(c)
			expectErr(t, c, tc.path)
		})
	}
}

type fakeInv map[string]bool

func (f fakeInv) Lookup(n string) (bool, bool) { p, ok := f[n]; return ok, p }

func TestInventoryValidation(t *testing.T) {
	c := mustParse(t, exampleYAML)
	inv := fakeInv{"vtnet0": true, "vtnet1": true, "vtnet2": true, "vtnet3": true, "vtnet4": false}
	err := Validate(c, inv)
	if err == nil || !strings.Contains(err.Error(), "not a physical") {
		t.Fatalf("want physical error, got %v", err)
	}
	c.Bridge.AllowVirtual = true
	if err := Validate(c, inv); err != nil {
		t.Fatal(err)
	}
	delete(inv, "vtnet3")
	if err := Validate(c, inv); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("want missing error, got %v", err)
	}
}

func TestSetMembership(t *testing.T) {
	p := Port{Name: "x0", Mode: ModeAccess, PVID: 1}
	p.SetMembership(10, MemberUntagged)
	if p.PVID != 10 || p.Mode != ModeAccess {
		t.Fatalf("%+v", p)
	}
	p.SetMembership(20, MemberTagged)
	if p.Mode != ModeTrunk || p.Native != 10 || !p.Tagged.Equal(VIDSet{20}) {
		t.Fatalf("%+v", p)
	}
	p.SetMembership(20, MemberUntagged)
	if p.Native != 20 || len(p.Tagged) != 0 {
		t.Fatalf("%+v", p)
	}
	p.SetMembership(10, MemberTagged)
	p.SetMembership(20, MemberNone)
	if p.Native != 0 || !p.Tagged.Equal(VIDSet{10}) {
		t.Fatalf("%+v", p)
	}
	a := Port{Mode: ModeAccess, PVID: 10}
	a.SetMembership(10, MemberNone)
	if a.PVID != 1 {
		t.Fatalf("access removal should fall back to VLAN 1: %+v", a)
	}
}

func TestNaturalSort(t *testing.T) {
	c := Default("vtnet0", []string{"vtnet10", "vtnet2", "em1", "vtnet1"})
	var names []string
	for _, p := range c.Ports {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "em1,vtnet1,vtnet2,vtnet10" {
		t.Fatal(names)
	}
	if err := Validate(c, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultValid(t *testing.T) {
	c := Default("em0", []string{"em1", "em2"})
	if err := Validate(c, nil); err != nil {
		t.Fatal(err)
	}
	if !c.Management.SSH.Enabled || c.Management.IPv4.Mode != "dhcp" {
		t.Fatal("defaults")
	}
}

func TestInterfaceNames(t *testing.T) {
	for _, ok := range []string{"em0", "vtnet12", "epair0a", "epair12b", "uplink", "ix0", "lagg0"} {
		if !ValidIfName(ok) {
			t.Errorf("%s should be valid", ok)
		}
	}
	for _, bad := range []string{"", "0em", "em0;id", "Em0", "a234567890123456", "em 0", "-x"} {
		if ValidIfName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
