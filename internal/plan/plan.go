// Package plan computes the ordered list of system commands that turn the
// live kernel state into the desired configuration. Only differences are
// emitted: running the planner twice against an unchanged system yields
// an empty plan.
package plan

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/state"
)

// Input is everything the planner needs.
type Input struct {
	Desired *config.Config
	// Prev is the configuration that was last applied. It is used for
	// settings that cannot be observed in the kernel (DHCP client, sshd,
	// NTP, admin-set path cost ...). nil means "unknown" (boot): all such
	// settings are (re)applied.
	Prev    *config.Config
	Snap    *state.Snapshot
	MACs    []state.MACEntry
	Routes  backend.Routes
	Sysctls map[string]string
}

// Section is a titled group of operations.
type Section struct {
	Title string       `json:"title"`
	Ops   []backend.Op `json:"ops"`
}

// Plan is the result of planning.
type Plan struct {
	Sections []Section `json:"sections"`
	Warnings []string  `json:"warnings"`
}

// Ops returns all operations in execution order.
func (p *Plan) Ops() []backend.Op {
	var out []backend.Op
	for _, s := range p.Sections {
		out = append(out, s.Ops...)
	}
	return out
}

// Empty reports whether there is nothing to do.
func (p *Plan) Empty() bool { return len(p.Ops()) == 0 }

// Commands renders the plan as shell commands with section comments.
func (p *Plan) Commands() []string {
	out := []string{}
	for _, s := range p.Sections {
		out = append(out, "# "+s.Title)
		for _, o := range s.Ops {
			out = append(out, o.String())
		}
	}
	return out
}

// Script renders the plan as a shell script.
func (p *Plan) Script() string { return strings.Join(p.Commands(), "\n") + "\n" }

type builder struct {
	in   Input
	plan Plan
	cur  *Section
	// removed holds bridge members removed by this plan (their dynamic
	// and static MAC entries disappear with them).
	removed map[string]bool
}

func (b *builder) section(title string) {
	b.plan.Sections = append(b.plan.Sections, Section{Title: title})
	b.cur = &b.plan.Sections[len(b.plan.Sections)-1]
}

func (b *builder) op(o backend.Op) { b.cur.Ops = append(b.cur.Ops, o) }

func (b *builder) exec(argv ...string) { b.op(backend.Exec(argv...)) }

func (b *builder) bestEffort(argv ...string) {
	b.op(backend.Op{Argv: argv, IgnoreError: true})
}

func (b *builder) warn(f string, a ...any) { b.plan.Warnings = append(b.plan.Warnings, fmt.Sprintf(f, a...)) }

// Build computes the plan.
func Build(in Input) *Plan {
	if in.Snap == nil {
		in.Snap = &state.Snapshot{}
	}
	if in.Sysctls == nil {
		in.Sysctls = map[string]string{}
	}
	b := &builder{in: in, removed: map[string]bool{}}
	b.sysctls()
	b.management()
	b.lags()
	b.portPrep()
	b.bridge()
	b.staticMACs()
	b.inband()
	b.routes()
	b.services()
	// drop empty sections
	var secs []Section
	for _, s := range b.plan.Sections {
		if len(s.Ops) > 0 {
			secs = append(secs, s)
		}
	}
	b.plan.Sections = secs
	return &b.plan
}

func (b *builder) prevChanged(f func(c *config.Config) any) bool {
	if b.in.Prev == nil {
		return true
	}
	return fmt.Sprint(f(b.in.Prev)) != fmt.Sprint(f(b.in.Desired))
}

// ---- sysctls ---------------------------------------------------------------

// DesiredSysctls returns the sysctl values bsdswitch enforces.
func DesiredSysctls(c *config.Config) map[string]string {
	pfil := "0"
	if FilteringEnabled(c) {
		pfil = "1"
	}
	return map[string]string{
		// pf only sees bridged traffic when ACLs are configured
		"net.link.bridge.pfil_member": pfil,
		"net.link.bridge.pfil_bridge": "0",
		"net.link.bridge.pfil_onlyip": "0",
		"net.link.bridge.log_mac_flap": "1",
		// a switch never routes between its management networks
		"net.inet.ip.forwarding":   "0",
		"net.inet6.ip6.forwarding": "0",
	}
}

func (b *builder) sysctls() {
	b.section("Kernel settings (sysctl)")
	want := DesiredSysctls(b.in.Desired)
	var keys []string
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var args []string
	for _, k := range keys {
		if cur, ok := b.in.Sysctls[k]; !ok || cur != want[k] {
			args = append(args, k+"="+want[k])
		}
	}
	if len(args) > 0 {
		b.exec(append([]string{"sysctl"}, args...)...)
	}
}

// ---- management -------------------------------------------------------------

func (b *builder) bridgeState() *state.Bridge {
	if i := b.in.Snap.Get(b.in.Desired.Bridge.Name); i != nil && i.Bridge != nil {
		return i.Bridge
	}
	return nil
}

func (b *builder) management() {
	c := b.in.Desired
	m := c.Management
	b.section(fmt.Sprintf("Management interface %s (outside the bridge)", m.Interface))
	ifc := b.in.Snap.Get(m.Interface)
	if ifc == nil {
		b.warn("management interface %s does not exist", m.Interface)
		return
	}
	if br := b.bridgeState(); br != nil && br.Member(m.Interface) != nil {
		b.exec("ifconfig", c.Bridge.Name, "deletem", m.Interface)
	}
	if !ifc.AdminUp() {
		b.exec("ifconfig", m.Interface, "up")
	}
	var prevIP4, prevIP6 config.IPConfig
	prevIf := ""
	if b.in.Prev != nil {
		prevIP4, prevIP6, prevIf = b.in.Prev.Management.IPv4, b.in.Prev.Management.IPv6, b.in.Prev.Management.Interface
	}
	samePrev := b.in.Prev != nil && prevIf == m.Interface
	b.addresses(m.Interface, ifc, m.IPv4, m.IPv6, samePrev, prevIP4, prevIP6)

	// Old management interface (changed in this commit): unconfigure it
	// so it can become a bridge member (FreeBSD rejects bridge members
	// that carry IP addresses).
	if b.in.Prev != nil && prevIf != "" && prevIf != m.Interface {
		old := b.in.Snap.Get(prevIf)
		if old != nil {
			b.section(fmt.Sprintf("Previous management interface %s", prevIf))
			if prevIP4.Mode == config.IPModeDHCP {
				b.bestEffort("pkill", "-f", "dhclient: "+prevIf)
			}
			for _, a := range old.IPv4 {
				b.exec("ifconfig", prevIf, "inet", a, "-alias")
			}
			for _, a := range old.IPv6 {
				if !isLinkLocal(a) {
					b.exec("ifconfig", prevIf, "inet6", a, "-alias")
				}
			}
		}
	}
}

func isLinkLocal(cidr string) bool {
	p, err := netip.ParsePrefix(cidr)
	return err == nil && p.Addr().IsLinkLocalUnicast()
}

// addresses converges IPv4/IPv6 configuration of a host interface.
func (b *builder) addresses(name string, ifc *state.Interface, v4, v6 config.IPConfig, samePrev bool, p4, p6 config.IPConfig) {
	// IPv4
	switch v4.Mode {
	case config.IPModeStatic:
		if samePrev && p4.Mode == config.IPModeDHCP || !samePrev && b.in.Prev == nil {
			b.bestEffort("pkill", "-f", "dhclient: "+name)
		}
		if !containsStr(ifc.IPv4, v4.Address) {
			b.exec("ifconfig", name, "inet", v4.Address, "alias")
		}
		for _, a := range ifc.IPv4 {
			if a != v4.Address {
				b.exec("ifconfig", name, "inet", a, "-alias")
			}
		}
	case config.IPModeDHCP:
		if !samePrev || p4.Mode != config.IPModeDHCP {
			if samePrev && p4.Mode == config.IPModeStatic && containsStr(ifc.IPv4, p4.Address) {
				b.exec("ifconfig", name, "inet", p4.Address, "-alias")
			}
			// dhclient exits with an error if it is already running
			// for this interface; that is fine.
			b.bestEffort("dhclient", "-b", name)
		}
	case config.IPModeNone:
		if samePrev && p4.Mode == config.IPModeDHCP {
			b.bestEffort("pkill", "-f", "dhclient: "+name)
		}
		for _, a := range ifc.IPv4 {
			b.exec("ifconfig", name, "inet", a, "-alias")
		}
	}
	// IPv6
	switch v6.Mode {
	case config.IPModeStatic:
		if !containsStr(ifc.IPv6, v6.Address) {
			if !samePrev || p6.Mode != config.IPModeStatic {
				b.exec("ifconfig", name, "inet6", "-ifdisabled")
			}
			b.exec("ifconfig", name, "inet6", v6.Address, "alias")
		}
		for _, a := range ifc.IPv6 {
			if a != v6.Address && !isLinkLocal(a) {
				b.exec("ifconfig", name, "inet6", a, "-alias")
			}
		}
	case config.IPModeSLAAC, config.IPModeDHCP:
		if !samePrev || p6.Mode != v6.Mode {
			b.exec("ifconfig", name, "inet6", "accept_rtadv", "-ifdisabled")
			b.bestEffort("rtsol", name)
		}
	case config.IPModeNone:
		if samePrev && p6.Mode != config.IPModeNone {
			for _, a := range ifc.IPv6 {
				if !isLinkLocal(a) {
					b.exec("ifconfig", name, "inet6", a, "-alias")
				}
			}
			b.exec("ifconfig", name, "inet6", "-accept_rtadv")
		}
	}
}

// ---- link aggregation (Phase 2) -------------------------------------------

func (b *builder) lags() {
	c := b.in.Desired
	b.section("Link aggregation (lagg)")
	desired := map[string]bool{}
	for _, l := range c.LAGs {
		desired[l.Name] = true
		ifc := b.in.Snap.Get(l.Name)
		if ifc == nil {
			b.exec("ifconfig", l.Name, "create")
		}
		cur := &state.Lagg{}
		if ifc != nil && ifc.Lagg != nil {
			cur = ifc.Lagg
		}
		if cur.Proto != l.Protocol {
			b.exec("ifconfig", l.Name, "laggproto", l.Protocol)
		}
		if l.Hash != "" && cur.Hash != l.Hash {
			b.exec("ifconfig", l.Name, "lagghash", l.Hash)
		}
		if l.Protocol == "lacp" && b.prevChanged(func(c *config.Config) any { return lagFast(c, l.Name) }) {
			if l.LACPFast {
				b.exec("ifconfig", l.Name, "lacp_fast_timeout")
			} else if ifc != nil {
				b.exec("ifconfig", l.Name, "-lacp_fast_timeout")
			}
		}
		curPorts := map[string]bool{}
		for _, p := range cur.Ports {
			curPorts[p.Name] = true
		}
		for _, p := range cur.Ports {
			if !containsStr(l.Members, p.Name) {
				b.exec("ifconfig", l.Name, "-laggport", p.Name)
			}
		}
		for _, mem := range l.Members {
			mi := b.in.Snap.Get(mem)
			if mi == nil {
				b.warn("LAG %s: member %s does not exist", l.Name, mem)
				continue
			}
			if !curPorts[mem] {
				if br := b.bridgeState(); br != nil && br.Member(mem) != nil {
					b.exec("ifconfig", c.Bridge.Name, "deletem", mem)
					b.removed[mem] = true
				}
				if mi.MTU != c.Bridge.MTU {
					b.exec("ifconfig", mem, "mtu", strconv.Itoa(c.Bridge.MTU))
				}
				b.offloads(mem, mi, "safe")
				if !mi.AdminUp() {
					b.exec("ifconfig", mem, "up")
				}
				b.exec("ifconfig", l.Name, "laggport", mem)
			}
		}
		if ifc == nil || !ifc.AdminUp() {
			b.exec("ifconfig", l.Name, "up")
		}
	}
	// destroy LAGs that are no longer configured
	for _, i := range b.in.Snap.Interfaces {
		if i.Lagg != nil && !desired[i.Name] && b.wasManagedLAG(i.Name) {
			if br := b.bridgeState(); br != nil && br.Member(i.Name) != nil {
				b.exec("ifconfig", c.Bridge.Name, "deletem", i.Name)
				b.removed[i.Name] = true
			}
			b.exec("ifconfig", i.Name, "destroy")
		}
	}
}

func lagFast(c *config.Config, name string) bool {
	for _, l := range c.LAGs {
		if l.Name == name {
			return l.LACPFast
		}
	}
	return false
}

// wasManagedLAG: we only destroy laggs we created (present in Prev) or,
// at boot (Prev nil), any lagg - bsdswitch owns all laggs on the box.
func (b *builder) wasManagedLAG(name string) bool {
	if b.in.Prev == nil {
		return true
	}
	for _, l := range b.in.Prev.LAGs {
		if l.Name == name {
			return true
		}
	}
	return false
}

// ---- port preparation --------------------------------------------------------

func (b *builder) offloads(name string, ifc *state.Interface, mode string) {
	if mode != "safe" {
		return
	}
	// if_bridge disables LRO itself; TSO and hardware VLAN filtering are
	// also unsafe on a bridge member (HW VLAN filter drops tags the bridge
	// must see).
	var args []string
	if ifc.HasOption("LRO") {
		args = append(args, "-lro")
	}
	if ifc.HasOption("TSO4") || ifc.HasOption("TSO6") {
		args = append(args, "-tso")
	}
	if ifc.HasOption("VLAN_HWFILTER") {
		args = append(args, "-vlanhwfilter")
	}
	if len(args) > 0 {
		b.bestEffort(append([]string{"ifconfig", name}, args...)...)
	}
}

func currentMedia(ifc *state.Interface) (speed, duplex string) {
	m := strings.TrimPrefix(ifc.Media, "Ethernet ")
	if m == "" || strings.HasPrefix(m, "autoselect") {
		return "auto", "auto"
	}
	sp := state.MediaSpeed(m)
	if sp == 0 {
		return "auto", "auto"
	}
	d := "half"
	if strings.Contains(m, "full-duplex") {
		d = "full"
	}
	return strconv.Itoa(sp), d
}

func (b *builder) portPrep() {
	c := b.in.Desired
	b.section("Switch port settings")
	for _, p := range c.Ports {
		ifc := b.in.Snap.Get(p.Name)
		if ifc == nil {
			if !b.isLAG(p.Name) {
				b.warn("port %s does not exist on this system; skipped", p.Name)
			}
			continue
		}
		if ifc.Lagg == nil {
			b.offloads(p.Name, ifc, p.Offloads)
		}
		if ifc.Description != p.Description {
			if p.Description == "" {
				b.exec("ifconfig", p.Name, "-description")
			} else {
				b.exec("ifconfig", p.Name, "description", p.Description)
			}
		}
		if ifc.MTU != c.Bridge.MTU {
			b.exec("ifconfig", p.Name, "mtu", strconv.Itoa(c.Bridge.MTU))
		}
		if ifc.Lagg == nil {
			if ifc.SpeedSettable() {
				cs, cd := currentMedia(ifc)
				if cs != p.Speed || (p.Speed != "auto" && cd != p.Duplex && p.Duplex != "auto") {
					args, ok := state.MediaFor(ifc.SupportedMedia, p.Speed, p.Duplex)
					if ok {
						b.exec(append([]string{"ifconfig", p.Name}, args...)...)
					} else {
						b.warn("port %s: %s Mbit/s %s duplex is not supported by %s", p.Name, p.Speed, p.Duplex, ifc.Driver)
					}
				}
			} else if p.Speed != "auto" {
				b.warn("port %s: speed/duplex cannot be set on %s (n/a), ignored", p.Name, ifc.Driver)
			}
		}
		if p.Shutdown && ifc.AdminUp() {
			b.exec("ifconfig", p.Name, "down")
		} else if !p.Shutdown && !ifc.AdminUp() {
			b.exec("ifconfig", p.Name, "up")
		}
	}
}

func (b *builder) isLAG(name string) bool {
	for _, l := range b.in.Desired.LAGs {
		if l.Name == name {
			return true
		}
	}
	return false
}

// ---- bridge ------------------------------------------------------------------

func (b *builder) bridge() {
	c := b.in.Desired
	bn := c.Bridge.Name
	b.section(fmt.Sprintf("Bridge %s", bn))
	br := b.bridgeState()
	brIf := b.in.Snap.Get(bn)
	if br == nil {
		b.exec("ifconfig", bn, "create")
		br = &state.Bridge{Members: nil, Timeout: 1200, MaxAddr: 2000, Proto: "rstp", Priority: 32768,
			HelloTime: 2, MaxAge: 20, FwdDelay: 15, HoldCount: 6}
	}
	// Enable VLAN filtering before adding any member, so that no port ever
	// forwards unfiltered.
	if !br.VLANFilter() {
		b.exec("ifconfig", bn, "vlanfilter")
	}
	if brIf != nil && brIf.MTU != c.Bridge.MTU && len(br.Members) > 0 {
		b.exec("ifconfig", bn, "mtu", strconv.Itoa(c.Bridge.MTU))
	}
	s := c.Bridge.STP
	var params []string
	addp := func(k string, cur, want int) {
		if cur != want {
			params = append(params, k, strconv.Itoa(want))
		}
	}
	addp("timeout", br.Timeout, c.Bridge.MACAging)
	addp("maxaddr", br.MaxAddr, c.Bridge.MaxAddresses)
	if br.Proto != s.Mode {
		params = append(params, "proto", s.Mode)
	}
	addp("priority", br.Priority, s.Priority)
	// Order matters for the kernel's 802.1D timer consistency checks:
	// raise fwddelay before maxage, lower maxage before fwddelay.
	if s.FwdDelay > br.FwdDelay {
		addp("fwddelay", br.FwdDelay, s.FwdDelay)
		addp("maxage", br.MaxAge, s.MaxAge)
	} else {
		addp("maxage", br.MaxAge, s.MaxAge)
		addp("fwddelay", br.FwdDelay, s.FwdDelay)
	}
	addp("hellotime", br.HelloTime, s.HelloTime)
	addp("holdcnt", br.HoldCount, s.HoldCount)
	if len(params) > 0 {
		b.exec(append([]string{"ifconfig", bn}, params...)...)
	}

	desired := map[string]*config.Port{}
	for i := range c.Ports {
		p := &c.Ports[i]
		if b.in.Snap.Get(p.Name) == nil && !b.isLAG(p.Name) {
			continue
		}
		desired[p.Name] = p
	}
	// Remove members that are no longer switch ports.
	for _, m := range br.Members {
		if desired[m.Name] == nil {
			if m.Name != c.Management.Interface && !b.isLAGMember(m.Name) && !b.removed[m.Name] {
				b.exec("ifconfig", bn, "deletem", m.Name)
			}
			b.removed[m.Name] = true
		}
	}
	// Mirrors: span ports
	wantSpan := map[string]bool{}
	for _, mr := range c.Mirrors {
		wantSpan[mr.Destination] = true
	}
	for _, sp := range br.Spans {
		if !wantSpan[sp] {
			b.exec("ifconfig", bn, "-span", sp)
		}
	}

	for _, p := range c.Ports {
		dp := desired[p.Name]
		if dp == nil {
			continue
		}
		m := br.Member(p.Name)
		untagged := p.Untagged()
		tagged := p.TaggedSet()
		if m == nil {
			args := []string{"ifconfig", bn, "addm", p.Name}
			if untagged != 0 {
				args = append(args, "untagged", strconv.Itoa(untagged))
			}
			if len(tagged) > 0 {
				args = append(args, "tagged", tagged.String())
			}
			b.exec(args...)
			m = &state.Member{Name: p.Name, Flags: []string{"LEARNING", "DISCOVER", "AUTOEDGE", "AUTOPTP"},
				Priority: 128, Untagged: untagged, Tagged: tagged}
		} else {
			// VLAN membership changes: remove before add so a VLAN never
			// is untagged and tagged at the same time.
			if rm := m.Tagged.Minus(tagged); len(rm) > 0 {
				b.exec("ifconfig", bn, "-iftagged", p.Name, rm.String())
			}
			if m.Untagged != untagged {
				if untagged == 0 {
					b.exec("ifconfig", bn, "-ifuntagged", p.Name)
				} else {
					b.exec("ifconfig", bn, "ifuntagged", p.Name, strconv.Itoa(untagged))
				}
			}
			if add := tagged.Minus(m.Tagged); len(add) > 0 {
				b.exec("ifconfig", bn, "+iftagged", p.Name, add.String())
			}
		}
		b.memberFlags(bn, &p, m)
	}
	for _, mr := range c.Mirrors {
		if !containsStr(br.Spans, mr.Destination) {
			if b.in.Snap.Get(mr.Destination) == nil {
				b.warn("mirror destination %s does not exist", mr.Destination)
				continue
			}
			if mi := b.in.Snap.Get(mr.Destination); mi != nil && !mi.AdminUp() {
				b.exec("ifconfig", mr.Destination, "up")
			}
			b.exec("ifconfig", bn, "span", mr.Destination)
		}
	}
	if brIf == nil || !brIf.AdminUp() {
		b.exec("ifconfig", bn, "up")
	}
}

func (b *builder) isLAGMember(name string) bool {
	for _, l := range b.in.Desired.LAGs {
		if containsStr(l.Members, name) {
			return true
		}
	}
	return false
}

func (b *builder) prevPort(name string) *config.Port {
	if b.in.Prev == nil {
		return nil
	}
	return b.in.Prev.Port(name)
}

func (b *builder) memberFlags(bn string, p *config.Port, m *state.Member) {
	c := b.in.Desired
	var args []string
	flag := func(on bool, cur bool, word string) {
		if on && !cur {
			args = append(args, word, p.Name)
		} else if !on && cur {
			args = append(args, "-"+word, p.Name)
		}
	}
	stpOn := c.Bridge.STP.Enabled && config.Bool(p.STP.Enabled, true)
	flag(stpOn, m.Has("STP"), "stp")
	pp := b.prevPort(p.Name)
	// EDGE as reported by the kernel is the operational edge state; the
	// admin setting is not observable, so removal relies on Prev.
	if stpOn && p.STP.Edge && !m.Has("EDGE") {
		args = append(args, "edge", p.Name)
	} else if !p.STP.Edge && pp != nil && pp.STP.Edge {
		args = append(args, "-edge", p.Name)
	}
	flag(config.Bool(p.STP.AutoEdge, true), m.Has("AUTOEDGE"), "autoedge")
	flag(p.Private, m.Has("PRIVATE"), "private")
	flag(p.QinQ, m.Has("QINQ"), "qinq")
	if m.Priority != p.STP.Priority {
		args = append(args, "ifpriority", p.Name, strconv.Itoa(p.STP.Priority))
	}
	if p.STP.PathCost != 0 && m.PathCost != p.STP.PathCost {
		args = append(args, "ifpathcost", p.Name, strconv.Itoa(p.STP.PathCost))
	} else if p.STP.PathCost == 0 && pp != nil && pp.STP.PathCost != 0 {
		args = append(args, "ifpathcost", p.Name, "0")
	}
	if m.IfMaxAddr != p.MaxAddr {
		args = append(args, "ifmaxaddr", p.Name, strconv.Itoa(p.MaxAddr))
	}
	wantProto := p.VLANProto
	if wantProto == "802.1q" {
		wantProto = ""
	}
	curProto := m.VLANProto
	if curProto == "802.1q" {
		curProto = ""
	}
	if wantProto != curProto {
		pr := p.VLANProto
		if pr == "" {
			pr = "802.1q"
		}
		args = append(args, "ifvlanproto", p.Name, pr)
	}
	if len(args) > 0 {
		b.exec(append([]string{"ifconfig", bn}, args...)...)
	}
}

// ---- static MAC entries --------------------------------------------------------

func (b *builder) staticMACs() {
	c := b.in.Desired
	bn := c.Bridge.Name
	b.section("Static MAC address entries")
	type key struct {
		mac  string
		vlan int
	}
	cur := map[key]string{}
	for _, e := range b.in.MACs {
		if e.Static && !b.removed[e.Port] {
			cur[key{e.MAC, e.VLAN}] = e.Port
		}
	}
	want := map[key]string{}
	for _, s := range c.StaticMACs {
		want[key{strings.ToLower(s.MAC), s.VLAN}] = s.Port
	}
	var rm []key
	for k := range cur {
		if _, ok := want[k]; !ok {
			rm = append(rm, k)
		}
	}
	sort.Slice(rm, func(i, j int) bool { return rm[i].mac < rm[j].mac })
	for _, k := range rm {
		b.exec("ifconfig", bn, "deladdr", k.mac, "vlan", strconv.Itoa(k.vlan))
	}
	for _, s := range c.StaticMACs {
		k := key{strings.ToLower(s.MAC), s.VLAN}
		if cur[k] != s.Port {
			b.exec("ifconfig", bn, "static", s.Port, k.mac, "vlan", strconv.Itoa(s.VLAN))
		}
	}
}

// ---- in-band management -----------------------------------------------------

// InBandName returns the vlan(4) interface name for in-band management.
func InBandName(c *config.Config) string {
	return fmt.Sprintf("%s.%d", c.Bridge.Name, c.Management.InBand.VLAN)
}

func (b *builder) inband() {
	c := b.in.Desired
	ib := c.Management.InBand
	b.section("In-band management VLAN interface")
	want := ""
	if ib.Enabled {
		want = InBandName(c)
	}
	for _, i := range b.in.Snap.Interfaces {
		if i.VLANParent == c.Bridge.Name && i.Name != want {
			b.bestEffort("pkill", "-f", "dhclient: "+i.Name)
			b.exec("ifconfig", i.Name, "destroy")
		}
	}
	if !ib.Enabled {
		return
	}
	ifc := b.in.Snap.Get(want)
	var p4, p6 config.IPConfig
	same := false
	if b.in.Prev != nil && b.in.Prev.Management.InBand.Enabled && InBandName(b.in.Prev) == want {
		same = true
		p4, p6 = b.in.Prev.Management.InBand.IPv4, b.in.Prev.Management.InBand.IPv6
	}
	if ifc == nil {
		b.exec("ifconfig", want, "create")
		ifc = &state.Interface{Name: want}
		same = false
	}
	if !ifc.AdminUp() {
		b.exec("ifconfig", want, "up")
	}
	b.addresses(want, ifc, ib.IPv4, ib.IPv6, same, p4, p6)
}

// ---- routes ------------------------------------------------------------------

func (b *builder) routes() {
	c := b.in.Desired
	m := c.Management
	b.section("Default routes")
	gw4, gw6 := "", ""
	dyn4 := m.IPv4.Mode == config.IPModeDHCP || (m.InBand.Enabled && m.InBand.IPv4.Mode == config.IPModeDHCP)
	if m.IPv4.Mode == config.IPModeStatic {
		gw4 = m.IPv4.Gateway
	}
	if m.InBand.Enabled && m.InBand.IPv4.Mode == config.IPModeStatic && m.InBand.IPv4.Gateway != "" {
		gw4 = m.InBand.IPv4.Gateway
	}
	if m.IPv6.Mode == config.IPModeStatic {
		gw6 = m.IPv6.Gateway
	}
	if m.InBand.Enabled && m.InBand.IPv6.Mode == config.IPModeStatic && m.InBand.IPv6.Gateway != "" {
		gw6 = m.InBand.IPv6.Gateway
	}
	dyn6 := m.IPv6.Mode == config.IPModeSLAAC || m.IPv6.Mode == config.IPModeDHCP
	b.route("", b.in.Routes.Default4, gw4, dyn4)
	b.route("-6", b.in.Routes.Default6, gw6, dyn6)
}

func (b *builder) route(fam, cur, want string, dynamic bool) {
	pre := []string{"route"}
	if fam != "" {
		pre = append(pre, fam)
	}
	cmd := func(a ...string) { b.exec(append(append([]string{}, pre...), a...)...) }
	switch {
	case want != "" && cur == "":
		cmd("add", "default", want)
	case want != "" && cur != want:
		cmd("change", "default", want)
	case want == "" && cur != "" && !dynamic:
		cmd("delete", "default")
	}
}

// ---- services ------------------------------------------------------------------

func (b *builder) services() {
	c := b.in.Desired
	b.section("System services")
	s := c.System
	if b.prevChanged(func(c *config.Config) any { return c.System.Hostname + "." + c.System.Domain }) {
		h := s.Hostname
		if s.Domain != "" {
			h += "." + s.Domain
		}
		b.exec("hostname", h)
	}
	if b.prevChanged(func(c *config.Config) any { return c.System.Timezone }) {
		b.bestEffort("tzsetup", s.Timezone)
	}
	if len(s.DNS) > 0 && b.prevChanged(func(c *config.Config) any { return fmt.Sprint(c.System.DNS, c.System.Domain) }) {
		b.op(backend.Op{Path: "/etc/resolv.conf", Content: ResolvConf(c)})
	}
	if b.prevChanged(func(c *config.Config) any { return c.System.NTP }) {
		if len(s.NTP) > 0 {
			b.op(backend.Op{Path: "/etc/ntp.conf", Content: NTPConf(c)})
			b.exec("sysrc", "ntpd_enable=YES", "ntpd_sync_on_start=YES")
			b.bestEffort("service", "ntpd", "restart")
		} else if b.in.Prev != nil {
			b.bestEffort("service", "ntpd", "stop")
			b.exec("sysrc", "ntpd_enable=NO")
		}
	}
	if b.prevChanged(func(c *config.Config) any { return c.Management.SSH }) {
		ssh := c.Management.SSH
		if ssh.Enabled {
			b.exec("sysrc", "sshd_enable=YES", "sshd_flags="+SSHFlags(c))
			b.bestEffort("service", "sshd", "restart")
		} else {
			b.bestEffort("service", "sshd", "stop")
			b.exec("sysrc", "sshd_enable=NO")
		}
	}
	if b.prevChanged(func(c *config.Config) any { return c.Syslog.Servers }) && (b.in.Prev != nil || len(c.Syslog.Servers) > 0) {
		b.op(backend.Op{Path: "/usr/local/etc/syslog.d/bsdswitch.conf", Content: SyslogConf(c)})
		b.bestEffort("service", "syslogd", "restart")
	}
	if b.prevChanged(func(c *config.Config) any { return c.SNMP }) && (b.in.Prev != nil || c.SNMP.Enabled) {
		if c.SNMP.Enabled {
			b.op(backend.Op{Path: "/etc/snmpd.config", Content: SNMPConf(c)})
			b.exec("sysrc", "bsnmpd_enable=YES")
			b.bestEffort("service", "bsnmpd", "restart")
		} else {
			b.bestEffort("service", "bsnmpd", "stop")
			b.exec("sysrc", "bsnmpd_enable=NO")
		}
	}
	if b.prevChanged(func(c *config.Config) any { return fmt.Sprint(c.LLDP, portNames(c)) }) && (b.in.Prev != nil || c.LLDP.Enabled) {
		if c.LLDP.Enabled {
			b.op(backend.Op{Path: "/usr/local/etc/lldpd.conf", Content: LLDPConf(c)})
			b.exec("sysrc", "lldpd_enable=YES")
			b.bestEffort("service", "lldpd", "restart")
		} else {
			b.bestEffort("service", "lldpd", "stop")
			b.exec("sysrc", "lldpd_enable=NO")
		}
	}
	b.filters()
}

func portNames(c *config.Config) []string {
	var n []string
	for _, p := range c.Ports {
		n = append(n, p.Name)
	}
	return n
}

// ResolvConf renders /etc/resolv.conf.
func ResolvConf(c *config.Config) string {
	var sb strings.Builder
	sb.WriteString("# Generated by bsdswitch - do not edit\n")
	if c.System.Domain != "" {
		sb.WriteString("search " + c.System.Domain + "\n")
	}
	for _, d := range c.System.DNS {
		sb.WriteString("nameserver " + d + "\n")
	}
	return sb.String()
}

// NTPConf renders /etc/ntp.conf.
func NTPConf(c *config.Config) string {
	var sb strings.Builder
	sb.WriteString("# Generated by bsdswitch - do not edit\n")
	sb.WriteString("tos minclock 3 maxclock 6\n")
	for _, n := range c.System.NTP {
		if strings.Contains(n, "pool") {
			sb.WriteString("pool " + n + " iburst\n")
		} else {
			sb.WriteString("server " + n + " iburst\n")
		}
	}
	sb.WriteString("restrict default limited kod nomodify notrap noquery nopeer\n")
	sb.WriteString("restrict source  limited kod nomodify notrap noquery\n")
	sb.WriteString("restrict 127.0.0.1\nrestrict ::1\n")
	sb.WriteString("leapfile \"/var/db/ntpd.leap-seconds.list\"\n")
	return sb.String()
}

// SSHFlags renders sshd_flags.
func SSHFlags(c *config.Config) string {
	ssh := c.Management.SSH
	f := []string{"-o", "Port=" + strconv.Itoa(ssh.Port)}
	if !ssh.PasswordAuth {
		f = append(f, "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no")
	}
	return strings.Join(f, " ")
}

// SyslogConf renders a syslogd include file for remote logging.
func SyslogConf(c *config.Config) string {
	var sb strings.Builder
	sb.WriteString("# Generated by bsdswitch - do not edit\n")
	for _, s := range c.Syslog.Servers {
		sb.WriteString("*.*\t@" + s + "\n")
	}
	return sb.String()
}

// SNMPConf renders a minimal bsnmpd configuration (read-only, MIB-II +
// bridge MIB via snmp_bridge).
func SNMPConf(c *config.Config) string {
	s := c.SNMP
	return fmt.Sprintf(`# Generated by bsdswitch - do not edit
location := "%s"
contact := "%s"
system := 1
read := "%s"
traphost := localhost
trapport := 162
begemotSnmpdPortStatus.0.0.0.0.161 = 1
begemotSnmpdLocalPortStatus."/var/run/snmpd.sock" = 1
begemotSnmpdLocalPortType."/var/run/snmpd.sock" = 4
begemotSnmpdCommunityString.0.1 = $(read)
begemotSnmpdCommunityDisable = 1
begemotSnmpdModulePath."mibII" = "/usr/lib/snmp_mibII.so"
begemotSnmpdModulePath."bridge" = "/usr/lib/snmp_bridge.so"
begemotBridgeDefaultBridgeIf = %s
`, s.Location, s.Contact, s.Community, c.Bridge.Name)
}

// LLDPConf renders lldpd.conf restricting LLDP to switch ports and the
// management interface.
func LLDPConf(c *config.Config) string {
	ifs := append([]string{c.Management.Interface}, portNames(c)...)
	for _, l := range c.LAGs {
		ifs = append(ifs, l.Members...)
	}
	return fmt.Sprintf("# Generated by bsdswitch - do not edit\nconfigure system hostname %s\nconfigure system interface pattern %s\nconfigure lldp tx-interval 30\n",
		c.System.Hostname, strings.Join(ifs, ","))
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
