package api

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/plan"
	"github.com/Lukas0923xcv/freebsdswitch/internal/state"
	"github.com/Lukas0923xcv/freebsdswitch/internal/stats"
)

// NA is shown wherever a value is not applicable/available (e.g. speed
// on virtio).
const NA = "n/a"

type STPView struct {
	Enabled  bool   `json:"enabled"`
	Edge     bool   `json:"edge"`
	AutoEdge bool   `json:"auto_edge"`
	Priority int    `json:"priority"`
	PathCost int    `json:"path_cost"`
	OperCost int    `json:"oper_cost"`
	Role     string `json:"role"`
	State    string `json:"state"`
	OperEdge bool   `json:"oper_edge"`
	PTP      bool   `json:"ptp"`
}

type PortView struct {
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Exists        bool             `json:"exists"`
	Driver        string           `json:"driver"`
	MAC           string           `json:"mac"`
	AdminUp       bool             `json:"admin_up"`
	LinkUp        bool             `json:"link_up"`
	Shutdown      bool             `json:"shutdown"`
	Speed         string           `json:"speed"`  // operational, Mbit/s or n/a
	Duplex        string           `json:"duplex"` // operational
	SpeedSetting  string           `json:"speed_setting"`
	DuplexSetting string           `json:"duplex_setting"`
	SpeedSettable bool             `json:"speed_settable"`
	SpeedOptions  []string         `json:"speed_options"`
	Media         string           `json:"media"`
	MTU           int              `json:"mtu"`
	Mode          string           `json:"mode"`
	PVID          int              `json:"pvid"`
	Native        int              `json:"native"`
	Tagged        config.VIDSet    `json:"tagged"`
	IsLAG         bool             `json:"is_lag"`
	LAGMembers    []string         `json:"lag_members"`
	InBridge      bool             `json:"in_bridge"`
	KernelPVID    int              `json:"kernel_pvid"`
	KernelTagged  config.VIDSet    `json:"kernel_tagged"`
	STP           STPView          `json:"stp"`
	MaxAddr       int              `json:"max_addresses"`
	Isolated      bool             `json:"isolated"`
	Counters      backend.Counters `json:"counters"`
	Rate          stats.Sample     `json:"rate"`
	Staged        bool             `json:"staged"`
	Capabilities  []string         `json:"capabilities"`
	Options       []string         `json:"options"`
	Mirror        bool             `json:"mirror"`
}

func speedOptions(ifc *state.Interface) []string {
	opts := []string{"auto"}
	if ifc == nil || !ifc.SpeedSettable() {
		return opts
	}
	seen := map[int]bool{}
	for _, m := range ifc.SupportedMedia {
		if sp := state.MediaSpeed(m); sp > 0 && !seen[sp] {
			seen[sp] = true
			opts = append(opts, strconv.Itoa(sp))
		}
	}
	return opts
}

func (s *Server) portViews(ctx context.Context) ([]PortView, error) {
	run := s.opt.Engine.Running()
	cand, _ := s.opt.Engine.Candidate()
	snap, err := s.opt.Backend.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var br *state.Bridge
	if b := snap.Get(run.Bridge.Name); b != nil {
		br = b.Bridge
	}
	latest := s.opt.Stats.Latest()
	views := []PortView{}
	for _, p := range run.Ports {
		views = append(views, s.portView(run, cand, snap, br, latest, p))
	}
	return views, nil
}

func (s *Server) portView(run, cand *config.Config, snap *state.Snapshot, br *state.Bridge, latest map[string]stats.Sample, p config.Port) PortView {
	v := PortView{Name: p.Name, Description: p.Description, Shutdown: p.Shutdown, Mode: p.Mode, PVID: p.PVID,
		Native: p.Native, Tagged: p.TaggedSet(), SpeedSetting: p.Speed, DuplexSetting: p.Duplex,
		MaxAddr: p.MaxAddr, Isolated: p.Private, Speed: NA, Duplex: NA, Media: NA,
		KernelTagged: config.VIDSet{}, LAGMembers: []string{}, Capabilities: []string{}, Options: []string{}}
	v.STP = STPView{Enabled: run.Bridge.STP.Enabled && config.Bool(p.STP.Enabled, true), Edge: p.STP.Edge,
		AutoEdge: config.Bool(p.STP.AutoEdge, true), Priority: p.STP.Priority, PathCost: p.STP.PathCost}
	for _, l := range run.LAGs {
		if l.Name == p.Name {
			v.IsLAG = true
			v.LAGMembers = append([]string{}, l.Members...)
		}
	}
	for _, mr := range run.Mirrors {
		if mr.Destination == p.Name {
			v.Mirror = true
		}
	}
	if cp := cand.Port(p.Name); cp == nil || fmt.Sprint(*cp) != fmt.Sprint(p) {
		v.Staged = true
	}
	if ifc := snap.Get(p.Name); ifc != nil {
		v.Exists = true
		v.Driver = ifc.Driver
		v.MAC = ifc.MAC
		v.AdminUp = ifc.AdminUp()
		v.LinkUp = ifc.LinkUp()
		v.MTU = ifc.MTU
		v.Capabilities = ifc.Capabilities
		v.Options = ifc.Options
		v.SpeedSettable = ifc.SpeedSettable() && ifc.Lagg == nil
		v.SpeedOptions = speedOptions(ifc)
		if ifc.Lagg != nil {
			v.Driver = "lagg (" + ifc.Lagg.Proto + ")"
		}
		if v.LinkUp && !ifc.IsVirtio() {
			if sp := ifc.SpeedMbps(); sp > 0 {
				v.Speed = strconv.Itoa(sp)
			}
			if d := ifc.Duplex(); d != "" {
				v.Duplex = d
			}
		}
		if !ifc.IsVirtio() && ifc.Media != "" {
			v.Media = strings.TrimPrefix(ifc.Media, "Ethernet ")
		}
		if !v.SpeedSettable {
			v.SpeedSetting, v.DuplexSetting = NA, NA
		}
	}
	if br != nil {
		if m := br.Member(p.Name); m != nil {
			v.InBridge = true
			v.KernelPVID = m.Untagged
			v.KernelTagged = m.Tagged
			v.STP.Role, v.STP.State = m.Role, m.State
			v.STP.OperEdge = m.Has("EDGE")
			v.STP.PTP = m.Has("PTP")
			v.STP.OperCost = m.PathCost
			if !m.Has("STP") {
				v.STP.Role, v.STP.State = "", "forwarding"
			}
		}
	}
	if c, ok := s.opt.Stats.Counters(p.Name); ok {
		v.Counters = c
	}
	v.Rate = latest[p.Name]
	return v
}

// InterfaceView is one detected NIC on the assignment page.
type InterfaceView struct {
	Name          string   `json:"name"`
	Driver        string   `json:"driver"`
	MAC           string   `json:"mac"`
	Role          string   `json:"role"` // management, port, lag-member, mirror, unused
	Exists        bool     `json:"exists"`
	Physical      bool     `json:"physical"`
	LinkUp        bool     `json:"link_up"`
	AdminUp       bool     `json:"admin_up"`
	Speed         string   `json:"speed"`
	Media         string   `json:"media"`
	MTU           int      `json:"mtu"`
	SpeedSettable bool     `json:"speed_settable"`
	SupportedMedia []string `json:"supported_media"`
	Capabilities  []string `json:"capabilities"`
	Options       []string `json:"options"`
	IPv4          []string `json:"ipv4"`
	IPv6          []string `json:"ipv6"`
	Notes         []string `json:"notes"`
}

func (s *Server) interfaceViews(ctx context.Context) ([]InterfaceView, error) {
	run := s.opt.Engine.Running()
	snap, err := s.opt.Backend.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	role := map[string]string{run.Management.Interface: "management"}
	for _, p := range run.Ports {
		role[p.Name] = "port"
	}
	for _, l := range run.LAGs {
		for _, m := range l.Members {
			role[m] = "lag-member (" + l.Name + ")"
		}
	}
	for _, m := range run.Mirrors {
		role[m.Destination] = "mirror"
	}
	out := []InterfaceView{}
	seen := map[string]bool{}
	for _, i := range snap.Interfaces {
		virtualOK := run.Bridge.AllowVirtual && i.IsEthernet && i.Bridge == nil && i.Lagg == nil && i.VLANParent == "" && i.Name != "lo0"
		if !i.Physical() && !virtualOK {
			continue
		}
		seen[i.Name] = true
		v := InterfaceView{Name: i.Name, Driver: i.Driver, MAC: i.MAC, Role: role[i.Name], Exists: true,
			Physical: i.Physical(), LinkUp: i.LinkUp(), AdminUp: i.AdminUp(), Speed: NA, Media: NA, MTU: i.MTU,
			SpeedSettable: i.SpeedSettable(), SupportedMedia: i.SupportedMedia, Capabilities: i.Capabilities,
			Options: i.Options, IPv4: i.IPv4, IPv6: i.IPv6, Notes: []string{}}
		if v.Role == "" {
			v.Role = "unused"
		}
		if v.IPv4 == nil {
			v.IPv4 = []string{}
		}
		if v.IPv6 == nil {
			v.IPv6 = []string{}
		}
		if !i.IsVirtio() {
			if sp := i.SpeedMbps(); sp > 0 && i.LinkUp() {
				v.Speed = strconv.Itoa(sp)
			}
			v.Media = strings.TrimPrefix(i.Media, "Ethernet ")
		} else {
			v.Notes = append(v.Notes, "virtio-net: link speed/duplex is virtual and cannot be set")
		}
		if i.HasOption("LRO") || i.HasOption("TSO4") {
			if v.Role == "port" {
				v.Notes = append(v.Notes, "LRO/TSO still enabled (will be disabled on apply)")
			}
		}
		if !i.Physical() {
			v.Notes = append(v.Notes, "virtual interface (allowed by bridge.allow_virtual)")
		}
		out = append(out, v)
	}
	// configured but missing
	for name, r := range role {
		if !seen[name] && name != "" && !strings.HasPrefix(name, "lagg") {
			out = append(out, InterfaceView{Name: name, Role: r, Exists: false, Speed: NA, Media: NA,
				Notes: []string{"configured but not present on this system"}, IPv4: []string{}, IPv6: []string{},
				Capabilities: []string{}, Options: []string{}, SupportedMedia: []string{}})
		}
	}
	sortIfaces(out)
	return out, nil
}

func sortIfaces(v []InterfaceView) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && config.NaturalLess(v[j].Name, v[j-1].Name); j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// SystemView is the dashboard system panel.
type SystemView struct {
	backend.SysInfo
	Version       string         `json:"version"`
	Commit        string         `json:"commit"`
	Management    MgmtView       `json:"management"`
	Config        ConfigStatus   `json:"config"`
	PortsTotal    int            `json:"ports_total"`
	PortsUp       int            `json:"ports_up"`
	VLANCount     int            `json:"vlan_count"`
	STPRoot       bool           `json:"stp_root"`
	Bridge        string         `json:"bridge"`
}

type MgmtView struct {
	Interface string   `json:"interface"`
	Addresses []string `json:"addresses"`
	InBand    string   `json:"inband"`
	InBandAddresses []string `json:"inband_addresses"`
	URLs      []string `json:"urls"`
	LinkUp    bool     `json:"link_up"`
	MAC       string   `json:"mac"`
	SSH       bool     `json:"ssh"`
}

type ConfigStatus struct {
	StartupDiffers bool        `json:"startup_differs"`
	Staged         bool        `json:"staged"`
	Pending        *PendingView `json:"pending"`
	BootMode       string      `json:"boot_mode"`
}

type PendingView struct {
	ID         int    `json:"id"`
	User       string `json:"user"`
	Comment    string `json:"comment"`
	DeadlineMs int64  `json:"deadline_ms"`
	StartedMs  int64  `json:"started_ms"`
	NowMs      int64  `json:"now_ms"`
}

func (s *Server) configStatus() ConfigStatus {
	_, staged := s.opt.Engine.Candidate()
	cs := ConfigStatus{StartupDiffers: s.opt.Engine.StartupDiffers(), Staged: staged, BootMode: s.opt.Engine.BootMode()}
	if p := s.opt.Engine.PendingCommit(); p != nil {
		cs.Pending = &PendingView{ID: p.ID, User: p.User, Comment: p.Comment, DeadlineMs: p.Deadline.UnixMilli(), StartedMs: p.Started.UnixMilli(), NowMs: nowMs()}
	}
	return cs
}

func urlFor(addr string, port int) string {
	p, err := netip.ParsePrefix(addr)
	if err != nil {
		return ""
	}
	a := p.Addr()
	if a.IsLinkLocalUnicast() {
		return ""
	}
	h := a.String()
	if a.Is6() {
		h = "[" + h + "]"
	}
	if port != 443 {
		h += ":" + strconv.Itoa(port)
	}
	return "https://" + h + "/"
}

func (s *Server) systemView(ctx context.Context) (SystemView, error) {
	info, err := s.opt.Backend.SysInfo(ctx)
	if err != nil {
		info = backend.SysInfo{}
	}
	run := s.opt.Engine.Running()
	v := SystemView{SysInfo: info, Config: s.configStatus(), Bridge: run.Bridge.Name, VLANCount: len(run.VLANs)}
	v.Version, v.Commit = versionInfo()
	snap, err := s.opt.Backend.Snapshot(ctx)
	if err != nil {
		return v, nil
	}
	m := MgmtView{Interface: run.Management.Interface, Addresses: []string{}, InBandAddresses: []string{}, URLs: []string{}, SSH: run.Management.SSH.Enabled}
	if i := snap.Get(m.Interface); i != nil {
		m.Addresses = append(append(m.Addresses, i.IPv4...), i.IPv6...)
		m.LinkUp = i.LinkUp()
		m.MAC = i.MAC
	}
	if run.Management.InBand.Enabled {
		m.InBand = plan.InBandName(run)
		if i := snap.Get(m.InBand); i != nil {
			m.InBandAddresses = append(append(m.InBandAddresses, i.IPv4...), i.IPv6...)
		}
	}
	for _, a := range append(append([]string{}, m.Addresses...), m.InBandAddresses...) {
		if u := urlFor(a, run.Management.HTTPSPort); u != "" {
			m.URLs = append(m.URLs, u)
		}
	}
	v.Management = m
	for _, p := range run.Ports {
		v.PortsTotal++
		if i := snap.Get(p.Name); i != nil && i.LinkUp() && i.AdminUp() {
			v.PortsUp++
		}
	}
	if b := snap.Get(run.Bridge.Name); b != nil && b.Bridge != nil {
		v.STPRoot = b.Bridge.IsRoot()
	}
	return v, nil
}
