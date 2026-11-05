package config

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
)

// FieldError is a single validation problem, addressed by a path such as
// "ports[vtnet2].pvid".
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Path + ": " + e.Message }

// ValidationError aggregates all problems found.
type ValidationError struct {
	Errors []FieldError `json:"errors"`
}

func (v *ValidationError) Error() string {
	var s []string
	for _, e := range v.Errors {
		s = append(s, e.Error())
	}
	return "invalid configuration: " + strings.Join(s, "; ")
}

func (v *ValidationError) add(path, f string, a ...any) {
	v.Errors = append(v.Errors, FieldError{Path: path, Message: fmt.Sprintf(f, a...)})
}

// Inventory describes interfaces present on the system, used for
// optional existence checks.
type Inventory interface {
	// Lookup reports whether the interface exists and whether it is a
	// physical Ethernet NIC (as opposed to a cloned/virtual interface).
	Lookup(name string) (exists, physical bool)
}

var (
	ifNameRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,10}[0-9]+$`)
	hostnameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	domainRe   = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	vlanNameRe = regexp.MustCompile(`^[A-Za-z0-9_.\- ]{0,32}$`)
	tzRe       = regexp.MustCompile(`^[A-Za-z0-9_+\-]+(/[A-Za-z0-9_+\-]+)*$`)
	descRe     = regexp.MustCompile(`^[^\x00-\x1f"'\\` + "`" + `$;|&<>]{0,64}$`)
	commRe     = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,32}$`)
	hostPortRe = regexp.MustCompile(`^[A-Za-z0-9.\-:\[\]]+$`)
)

// ValidSpeeds lists speed settings understood by the media mapper.
var ValidSpeeds = []string{"auto", "10", "100", "1000", "2500", "5000", "10000", "25000", "40000", "50000", "100000"}

// ValidIfName reports whether s is a syntactically valid interface name.
func ValidIfName(s string) bool { return ifNameRe.MatchString(s) }

// ValidMAC reports whether s is a unicast MAC in xx:xx:xx:xx:xx:xx form.
func ValidMAC(s string) bool {
	hw, err := net.ParseMAC(s)
	if err != nil || len(hw) != 6 || !strings.Contains(s, ":") || len(s) != 17 {
		return false
	}
	return hw[0]&1 == 0 // not multicast/broadcast
}

// Validate checks the whole configuration. inv may be nil.
func Validate(c *Config, inv Inventory) error {
	v := &ValidationError{}
	if c.Version != SchemaVersion {
		v.add("version", "must be %d", SchemaVersion)
	}
	validateSystem(v, &c.System)
	validateBridge(v, &c.Bridge)

	// VLANs
	vlans := map[int]bool{}
	for i, vl := range c.VLANs {
		p := fmt.Sprintf("vlans[%d]", i)
		if vl.ID < MinVID || vl.ID > MaxVID {
			v.add(p+".id", "VLAN ID %d out of range %d-%d", vl.ID, MinVID, MaxVID)
		}
		if vlans[vl.ID] {
			v.add(p+".id", "duplicate VLAN %d", vl.ID)
		}
		vlans[vl.ID] = true
		if !vlanNameRe.MatchString(vl.Name) {
			v.add(p+".name", "VLAN name may only contain letters, digits, space, '.', '_' and '-' (max 32)")
		}
	}

	// Interfaces used by roles
	used := map[string]string{}
	claim := func(path, name, role string) {
		if prev, ok := used[name]; ok {
			v.add(path, "interface %s already used as %s", name, prev)
			return
		}
		used[name] = role
	}
	m := &c.Management
	if m.Interface == "" {
		v.add("management.interface", "a management interface is required")
	} else if !ValidIfName(m.Interface) {
		v.add("management.interface", "invalid interface name %q", m.Interface)
	} else {
		claim("management.interface", m.Interface, "management interface")
		if inv != nil {
			if ok, _ := inv.Lookup(m.Interface); !ok {
				v.add("management.interface", "interface %s does not exist", m.Interface)
			}
		}
	}
	validateIP(v, "management.ipv4", m.IPv4, 4, true)
	validateIP(v, "management.ipv6", m.IPv6, 6, true)
	if m.InBand.Enabled {
		if !vlans[m.InBand.VLAN] {
			v.add("management.inband.vlan", "in-band management VLAN %d is not defined", m.InBand.VLAN)
		}
		validateIP(v, "management.inband.ipv4", m.InBand.IPv4, 4, true)
		validateIP(v, "management.inband.ipv6", m.InBand.IPv6, 6, true)
		if m.InBand.IPv4.Mode == IPModeNone && m.InBand.IPv6.Mode == IPModeNone {
			v.add("management.inband", "in-band management needs an IPv4 or IPv6 address")
		}
		if m.InBand.IPv4.Gateway != "" && m.IPv4.Gateway != "" {
			v.add("management.inband.ipv4.gateway", "only one IPv4 default gateway may be configured (management or in-band)")
		}
		member := false
		for _, p := range c.Ports {
			if !p.Shutdown && p.Membership(m.InBand.VLAN) != MemberNone {
				member = true
			}
		}
		if !member {
			v.add("management.inband.vlan", "no enabled switch port is a member of in-band management VLAN %d", m.InBand.VLAN)
		}
	}
	for i, s := range m.AllowedSubnets {
		if _, err := netip.ParsePrefix(s); err != nil {
			v.add(fmt.Sprintf("management.allowed_subnets[%d]", i), "invalid subnet %q", s)
		}
	}
	checkPort(v, "management.ssh.port", m.SSH.Port)
	checkPort(v, "management.https_port", m.HTTPSPort)
	checkPort(v, "management.http_port", m.HTTPPort)
	if m.HTTPPort == m.HTTPSPort {
		v.add("management.http_port", "HTTP and HTTPS ports must differ")
	}
	if m.SessionTimeout < 1 || m.SessionTimeout > 1440 {
		v.add("management.session_timeout", "must be 1-1440 minutes")
	}

	// LAG names are valid port names.
	lagNames := map[string]bool{}
	for i, l := range c.LAGs {
		p := fmt.Sprintf("lags[%d]", i)
		if !regexp.MustCompile(`^lagg[0-9]+$`).MatchString(l.Name) {
			v.add(p+".name", "LAG name must be laggN")
		}
		if lagNames[l.Name] {
			v.add(p+".name", "duplicate LAG %s", l.Name)
		}
		lagNames[l.Name] = true
		switch l.Protocol {
		case "lacp", "failover", "loadbalance", "roundrobin", "none":
		default:
			v.add(p+".protocol", "unknown protocol %q", l.Protocol)
		}
		if len(l.Members) == 0 {
			v.add(p+".members", "a LAG needs at least one member")
		}
		for _, mem := range l.Members {
			if !ValidIfName(mem) {
				v.add(p+".members", "invalid interface %q", mem)
				continue
			}
			claim(p+".members", mem, "member of "+l.Name)
		}
		if l.Hash != "" {
			for _, h := range strings.Split(l.Hash, ",") {
				if h != "l2" && h != "l3" && h != "l4" {
					v.add(p+".hash", "hash must be a combination of l2,l3,l4")
				}
			}
		}
	}

	// Ports
	portNames := map[string]bool{}
	for _, p := range c.Ports {
		path := fmt.Sprintf("ports[%s]", p.Name)
		if !ValidIfName(p.Name) {
			v.add(path+".name", "invalid interface name %q", p.Name)
			continue
		}
		if portNames[p.Name] {
			v.add(path, "duplicate port")
			continue
		}
		portNames[p.Name] = true
		claim(path, p.Name, "switch port")
		if inv != nil && !lagNames[p.Name] {
			ok, phys := inv.Lookup(p.Name)
			if !ok {
				v.add(path, "interface %s does not exist", p.Name)
			} else if !phys && !c.Bridge.AllowVirtual {
				v.add(path, "%s is not a physical Ethernet interface (set bridge.allow_virtual to permit)", p.Name)
			}
		}
		if !descRe.MatchString(p.Description) {
			v.add(path+".description", "description may not contain quotes, control or shell characters (max 64)")
		}
		if p.MTU != 0 && p.MTU != c.Bridge.MTU {
			v.add(path+".mtu", "all bridge members must use the bridge MTU (%d); if_bridge forces members to the same MTU", c.Bridge.MTU)
		}
		if !contains(ValidSpeeds, p.Speed) {
			v.add(path+".speed", "invalid speed %q", p.Speed)
		}
		switch p.Duplex {
		case "auto", "full", "half":
		default:
			v.add(path+".duplex", "duplex must be auto, full or half")
		}
		if p.Speed == "auto" && p.Duplex != "auto" {
			v.add(path+".duplex", "duplex can only be forced together with a fixed speed")
		}
		if p.Duplex == "half" && p.Speed != "10" && p.Speed != "100" {
			v.add(path+".duplex", "half duplex is only possible at 10 or 100 Mbit/s")
		}
		switch p.Mode {
		case ModeAccess:
			if p.PVID != 0 && !vlans[p.PVID] {
				v.add(path+".pvid", "VLAN %d is not defined", p.PVID)
			}
			if p.PVID == 0 {
				v.add(path+".pvid", "an access port needs a VLAN")
			}
			if len(p.Tagged) > 0 {
				v.add(path+".tagged", "an access port cannot carry tagged VLANs; set mode: trunk")
			}
			if p.Native != 0 {
				v.add(path+".native", "native VLAN only applies to trunk ports; use pvid")
			}
		case ModeTrunk:
			if p.PVID != 0 {
				v.add(path+".pvid", "pvid only applies to access ports; use native on a trunk")
			}
			if p.Native != 0 && !vlans[p.Native] {
				v.add(path+".native", "VLAN %d is not defined", p.Native)
			}
			for _, t := range p.Tagged {
				if !vlans[t] {
					v.add(path+".tagged", "VLAN %d is not defined", t)
				}
			}
			if p.Native != 0 && p.Tagged.Contains(p.Native) {
				v.add(path+".tagged", "VLAN %d cannot be both native (untagged) and tagged", p.Native)
			}
			if p.Native == 0 && len(p.Tagged) == 0 {
				v.add(path, "trunk carries no VLANs")
			}
		default:
			v.add(path+".mode", "mode must be access or trunk")
		}
		if p.STP.Priority < 0 || p.STP.Priority > 240 || p.STP.Priority%16 != 0 {
			v.add(path+".stp.priority", "port priority must be 0-240 in steps of 16")
		}
		if p.STP.PathCost < 0 || p.STP.PathCost > 200000000 {
			v.add(path+".stp.path_cost", "path cost must be 0 (auto) or 1-200000000")
		}
		if p.MaxAddr < 0 || p.MaxAddr > 65535 {
			v.add(path+".max_addresses", "must be 0-65535")
		}
		switch p.VLANProto {
		case "", "802.1q", "802.1ad":
		default:
			v.add(path+".vlan_proto", "must be 802.1q or 802.1ad")
		}
		switch p.Offloads {
		case "safe", "keep":
		default:
			v.add(path+".offloads", "must be safe or keep")
		}
	}
	for i, l := range c.LAGs {
		if !portNames[l.Name] {
			v.add(fmt.Sprintf("lags[%d]", i), "LAG %s is not configured as a switch port", l.Name)
		}
	}

	// Static MACs
	seenMAC := map[string]bool{}
	for i, s := range c.StaticMACs {
		path := fmt.Sprintf("static_macs[%d]", i)
		if !ValidMAC(s.MAC) {
			v.add(path+".mac", "invalid unicast MAC %q (use xx:xx:xx:xx:xx:xx)", s.MAC)
		}
		key := fmt.Sprintf("%s/%d", strings.ToLower(s.MAC), s.VLAN)
		if seenMAC[key] {
			v.add(path, "duplicate entry for %s in VLAN %d", s.MAC, s.VLAN)
		}
		seenMAC[key] = true
		pt := c.Port(s.Port)
		if pt == nil {
			v.add(path+".port", "port %s is not a switch port", s.Port)
		} else if pt.Membership(s.VLAN) == MemberNone {
			v.add(path+".vlan", "port %s is not a member of VLAN %d", s.Port, s.VLAN)
		}
	}

	// Phase 2 validation
	for i, mr := range c.Mirrors {
		path := fmt.Sprintf("mirrors[%d].destination", i)
		if !ValidIfName(mr.Destination) {
			v.add(path, "invalid interface")
		} else {
			claim(path, mr.Destination, "mirror destination")
		}
	}
	for i, a := range c.ACLs {
		path := fmt.Sprintf("acls[%d]", i)
		if !commRe.MatchString(a.Name) {
			v.add(path+".name", "invalid ACL name")
		}
		if a.Port != "" && c.Port(a.Port) == nil {
			v.add(path+".port", "port %s is not a switch port", a.Port)
		}
		if a.Direction != "in" && a.Direction != "out" {
			v.add(path+".direction", "must be in or out")
		}
		if a.Default != "pass" && a.Default != "block" {
			v.add(path+".default", "must be pass or block")
		}
		for j, r := range a.Rules {
			rp := fmt.Sprintf("%s.rules[%d]", path, j)
			if r.Action != "pass" && r.Action != "block" {
				v.add(rp+".action", "must be pass or block")
			}
			switch r.Proto {
			case "", "any", "tcp", "udp", "icmp", "icmp6":
			default:
				v.add(rp+".proto", "unsupported protocol %q", r.Proto)
			}
			for _, addr := range []struct{ n, s string }{{"src", r.Src}, {"dst", r.Dst}} {
				if addr.s == "" || addr.s == "any" {
					continue
				}
				if _, err := netip.ParsePrefix(addr.s); err != nil {
					if _, err2 := netip.ParseAddr(addr.s); err2 != nil {
						v.add(rp+"."+addr.n, "invalid address %q", addr.s)
					}
				}
			}
			if r.DstPort != "" {
				if r.Proto != "tcp" && r.Proto != "udp" {
					v.add(rp+".dst_port", "a port requires proto tcp or udp")
				}
				if !regexp.MustCompile(`^[0-9]{1,5}(:[0-9]{1,5})?$`).MatchString(r.DstPort) {
					v.add(rp+".dst_port", "port must be N or N:M")
				}
			}
			if !descRe.MatchString(r.Comment) {
				v.add(rp+".comment", "invalid characters")
			}
		}
	}
	for i, r := range c.RateLimits {
		path := fmt.Sprintf("rate_limits[%d]", i)
		if c.Port(r.Port) == nil {
			v.add(path+".port", "port %s is not a switch port", r.Port)
		}
		if r.Ingress < 0 || r.Egress < 0 || r.Ingress > 100_000_000 || r.Egress > 100_000_000 {
			v.add(path, "rate must be 0-100000000 kbit/s")
		}
	}
	if c.SNMP.Enabled && !commRe.MatchString(c.SNMP.Community) {
		v.add("snmp.community", "community must be 1-32 characters [A-Za-z0-9_.-]")
	}
	if !descRe.MatchString(c.SNMP.Location) || !descRe.MatchString(c.SNMP.Contact) {
		v.add("snmp", "location/contact contain invalid characters")
	}
	for i, s := range c.Syslog.Servers {
		if !hostPortRe.MatchString(s) {
			v.add(fmt.Sprintf("syslog.servers[%d]", i), "invalid server %q", s)
		}
	}

	if len(v.Errors) > 0 {
		return v
	}
	return nil
}

func validateSystem(v *ValidationError, s *System) {
	if !hostnameRe.MatchString(s.Hostname) {
		v.add("system.hostname", "invalid hostname %q", s.Hostname)
	}
	if s.Domain != "" && !domainRe.MatchString(s.Domain) {
		v.add("system.domain", "invalid domain %q", s.Domain)
	}
	if !tzRe.MatchString(s.Timezone) || strings.Contains(s.Timezone, "..") {
		v.add("system.timezone", "invalid time zone %q", s.Timezone)
	}
	if len(s.DNS) > 3 {
		v.add("system.dns", "at most 3 DNS servers")
	}
	for i, d := range s.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			v.add(fmt.Sprintf("system.dns[%d]", i), "invalid IP address %q", d)
		}
	}
	for i, n := range s.NTP {
		if _, err := netip.ParseAddr(n); err != nil && !domainRe.MatchString(n) {
			v.add(fmt.Sprintf("system.ntp[%d]", i), "invalid NTP server %q", n)
		}
	}
}

func validateBridge(v *ValidationError, b *Bridge) {
	if !regexp.MustCompile(`^bridge[0-9]+$`).MatchString(b.Name) {
		v.add("bridge.name", "bridge name must be bridgeN")
	}
	if b.MACAging < 10 || b.MACAging > 1000000 {
		v.add("bridge.mac_aging", "MAC aging must be 10-1000000 seconds")
	}
	if b.MaxAddresses < 1 || b.MaxAddresses > 1000000 {
		v.add("bridge.max_addresses", "must be 1-1000000")
	}
	if b.MTU < MinMTU || b.MTU > MaxMTU {
		v.add("bridge.mtu", "MTU must be %d-%d", MinMTU, MaxMTU)
	}
	s := b.STP
	if s.Mode != STPModeRSTP && s.Mode != STPModeSTP {
		v.add("bridge.stp.mode", "mode must be rstp or stp")
	}
	if s.Priority < 0 || s.Priority > 61440 || s.Priority%4096 != 0 {
		v.add("bridge.stp.priority", "bridge priority must be 0-61440 in steps of 4096")
	}
	if s.HelloTime < 1 || s.HelloTime > 2 {
		v.add("bridge.stp.hello", "hello time must be 1-2 seconds")
	}
	if s.MaxAge < 6 || s.MaxAge > 40 {
		v.add("bridge.stp.max_age", "max age must be 6-40 seconds")
	}
	if s.FwdDelay < 4 || s.FwdDelay > 30 {
		v.add("bridge.stp.fwd_delay", "forward delay must be 4-30 seconds")
	}
	// IEEE 802.1D: 2*(fwd_delay-1) >= max_age >= 2*(hello+1)
	if 2*(s.FwdDelay-1) < s.MaxAge {
		v.add("bridge.stp.max_age", "max age must be <= 2*(forward delay - 1)")
	}
	if s.MaxAge < 2*(s.HelloTime+1) {
		v.add("bridge.stp.max_age", "max age must be >= 2*(hello time + 1)")
	}
	if s.HoldCount < 1 || s.HoldCount > 10 {
		v.add("bridge.stp.hold_count", "hold count must be 1-10")
	}
}

func validateIP(v *ValidationError, path string, ip IPConfig, fam int, allowDHCP bool) {
	switch ip.Mode {
	case IPModeNone:
		if ip.Address != "" || ip.Gateway != "" {
			v.add(path, "address/gateway set but mode is none")
		}
		return
	case IPModeDHCP:
		if !allowDHCP {
			v.add(path+".mode", "DHCP not allowed here")
		}
		if ip.Address != "" {
			v.add(path+".address", "address must be empty with DHCP")
		}
		return
	case IPModeSLAAC:
		if fam != 6 {
			v.add(path+".mode", "slaac is only valid for IPv6")
		}
		return
	case IPModeStatic:
	default:
		v.add(path+".mode", "mode must be none, dhcp, static%s", map[bool]string{true: " or slaac", false: ""}[fam == 6])
		return
	}
	pfx, err := netip.ParsePrefix(ip.Address)
	if err != nil {
		v.add(path+".address", "invalid address %q (use CIDR notation, e.g. 192.168.1.10/24)", ip.Address)
		return
	}
	a := pfx.Addr()
	if (fam == 4) != a.Is4() {
		v.add(path+".address", "not an IPv%d address", fam)
		return
	}
	if fam == 4 && (pfx.Bits() < 8 || pfx.Bits() > 32) {
		v.add(path+".address", "prefix length must be /8-/32")
	}
	if fam == 6 && (pfx.Bits() < 16 || pfx.Bits() > 128) {
		v.add(path+".address", "prefix length must be /16-/128")
	}
	if a.IsMulticast() || a.IsUnspecified() || a.IsLoopback() {
		v.add(path+".address", "address %s is not a usable host address", a)
	}
	if fam == 4 && pfx.Bits() < 31 {
		if a == pfx.Masked().Addr() {
			v.add(path+".address", "%s is the network address", a)
		}
		if a == broadcast(pfx) {
			v.add(path+".address", "%s is the broadcast address", a)
		}
	}
	if ip.Gateway != "" {
		gw, err := netip.ParseAddr(ip.Gateway)
		if err != nil || (fam == 4) != gw.Is4() {
			v.add(path+".gateway", "invalid IPv%d gateway %q", fam, ip.Gateway)
		} else if !(fam == 6 && gw.IsLinkLocalUnicast()) && !pfx.Masked().Contains(gw) {
			v.add(path+".gateway", "gateway %s is not in subnet %s", gw, pfx.Masked())
		} else if gw == a {
			v.add(path+".gateway", "gateway equals interface address")
		}
	}
}

func broadcast(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	n := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	n |= host
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

func checkPort(v *ValidationError, path string, p int) {
	if p < 1 || p > 65535 {
		v.add(path, "must be 1-65535")
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
