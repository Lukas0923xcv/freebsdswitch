// Package state models the live kernel networking state as observed via
// ifconfig(8) and contains the parsers for its output.
package state

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

// Interface is one network interface as reported by `ifconfig -a -m -D`.
type Interface struct {
	Name           string   `json:"name"`
	Driver         string   `json:"driver"`      // e.g. "em", from drivername (survives renames)
	DriverUnit     string   `json:"driver_unit"` // e.g. "em0"
	Flags          []string `json:"flags"`
	MTU            int      `json:"mtu"`
	MAC            string   `json:"mac"`
	Description    string   `json:"description"`
	Options        []string `json:"options"`      // enabled capabilities
	Capabilities   []string `json:"capabilities"` // supported capabilities
	Media          string   `json:"media"`        // configured, e.g. "Ethernet autoselect"
	MediaActive    string   `json:"media_active"` // e.g. "1000baseT <full-duplex>"
	Status         string   `json:"status"`       // "active", "no carrier", ""
	SupportedMedia []string `json:"supported_media"`
	IPv4           []string `json:"ipv4"`
	IPv6           []string `json:"ipv6"`
	Groups         []string `json:"groups"`
	Cloned         bool     `json:"cloned"`
	IsEthernet     bool     `json:"is_ethernet"`
	// Bridge holds bridge-specific state if this is an if_bridge.
	Bridge *Bridge `json:"bridge,omitempty"`
	// VLAN parent info for vlan(4) interfaces ("vlan: 10 vlanproto: 802.1q ... parent interface: bridge0")
	VLANTag    int    `json:"vlan_tag,omitempty"`
	VLANParent string `json:"vlan_parent,omitempty"`
	// Lagg info
	Lagg *Lagg `json:"lagg,omitempty"`
}

func (i *Interface) HasFlag(f string) bool   { return has(i.Flags, f) }
func (i *Interface) HasOption(o string) bool { return has(i.Options, o) }
func (i *Interface) HasCap(o string) bool {
	return has(i.Capabilities, o) || has(i.Options, o)
}

// AdminUp reports the administrative state (IFF_UP).
func (i *Interface) AdminUp() bool { return i.HasFlag("UP") }

// LinkUp reports carrier.
func (i *Interface) LinkUp() bool {
	if i.Status != "" {
		return i.Status == "active"
	}
	return i.HasFlag("RUNNING") && i.HasFlag("UP")
}

// Physical reports whether this is a real (or paravirtual) Ethernet NIC
// and not a cloned interface such as bridge, epair, vlan, lagg or tap.
func (i *Interface) Physical() bool { return i.IsEthernet && !i.Cloned }

// IsVirtio reports the virtio-net driver, which has no settable speed or
// duplex.
func (i *Interface) IsVirtio() bool { return i.Driver == "vtnet" }

// SpeedMbps parses the active media, e.g. "1000baseT <full-duplex>" ->
// 1000. Returns 0 if unknown.
func (i *Interface) SpeedMbps() int { return MediaSpeed(i.MediaActive) }

// Duplex returns "full", "half" or "".
func (i *Interface) Duplex() string {
	if strings.Contains(i.MediaActive, "full-duplex") {
		return "full"
	}
	if strings.Contains(i.MediaActive, "half-duplex") {
		return "half"
	}
	if MediaSpeed(i.MediaActive) > 0 && MediaSpeed(i.MediaActive) <= 100 {
		return "half"
	}
	return ""
}

// SpeedSettable reports whether speed/duplex can be forced. virtio and
// interfaces that only offer autoselect cannot.
func (i *Interface) SpeedSettable() bool {
	if i.IsVirtio() {
		return false
	}
	// Only NICs that offer autoselect plus at least one fixed speed can
	// be configured; fixed-media interfaces (epair, some 10G optics)
	// reject any media change.
	auto, fixed := false, false
	for _, m := range i.SupportedMedia {
		if strings.Contains(m, "autoselect") {
			auto = true
		} else if MediaSpeed(m) > 0 {
			fixed = true
		}
	}
	return auto && fixed
}

var mediaSpeedRe = regexp.MustCompile(`(?i)\b([0-9]+(?:\.[0-9]+)?)(G|M)?base`)

// MediaSpeed extracts the speed in Mbit/s from a media word.
func MediaSpeed(s string) int {
	m := mediaSpeedRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	if strings.EqualFold(m[2], "G") {
		f *= 1000
	}
	return int(f)
}

// MediaFor returns the ifconfig media arguments for a speed/duplex
// setting given the supported media list, e.g. ["media","1000baseT",
// "mediaopt","full-duplex"]. ok=false if unsupported.
func MediaFor(supported []string, speed, duplex string) ([]string, bool) {
	if speed == "auto" || speed == "" {
		for _, m := range supported {
			if strings.TrimSpace(m) == "media autoselect" {
				return []string{"media", "autoselect"}, true
			}
		}
		return nil, false
	}
	want, _ := strconv.Atoi(speed)
	var fallback []string
	for _, s := range supported {
		f := strings.Fields(s) // "media 1000baseT mediaopt full-duplex"
		if len(f) < 2 || f[0] != "media" || MediaSpeed(f[1]) != want {
			continue
		}
		opt := ""
		for j := 2; j+1 < len(f); j++ {
			if f[j] == "mediaopt" {
				opt = f[j+1]
			}
		}
		isFull := strings.Contains(opt, "full-duplex")
		switch duplex {
		case "full", "auto":
			if isFull {
				return []string{"media", f[1], "mediaopt", "full-duplex"}, true
			}
			if duplex == "auto" && fallback == nil {
				fallback = []string{"media", f[1]}
			}
		case "half":
			if !isFull {
				if strings.Contains(opt, "half-duplex") {
					return []string{"media", f[1], "mediaopt", "half-duplex"}, true
				}
				return []string{"media", f[1]}, true
			}
		}
	}
	if fallback != nil {
		return fallback, true
	}
	return nil, false
}

// Bridge is the state of an if_bridge(4) interface.
type Bridge struct {
	ID           string   `json:"id"`
	Priority     int      `json:"priority"`
	HelloTime    int      `json:"hello"`
	FwdDelay     int      `json:"fwd_delay"`
	MaxAge       int      `json:"max_age"`
	HoldCount    int      `json:"hold_count"`
	Proto        string   `json:"proto"`
	MaxAddr      int      `json:"max_addresses"`
	Timeout      int      `json:"timeout"`
	RootID       string   `json:"root_id"`
	RootPriority int      `json:"root_priority"`
	RootCost     int      `json:"root_cost"`
	RootPort     int      `json:"root_port"`
	Flags        []string `json:"flags"`
	DefUntagged  int      `json:"defuntagged"`
	Members      []Member `json:"members"`
	Spans        []string `json:"spans"`
}

func (b *Bridge) VLANFilter() bool { return has(b.Flags, "VLANFILTER") }

// IsRoot reports whether this bridge is the STP root.
func (b *Bridge) IsRoot() bool { return b.ID != "" && b.ID == b.RootID && b.RootCost == 0 }

func (b *Bridge) Member(name string) *Member {
	for i := range b.Members {
		if b.Members[i].Name == name {
			return &b.Members[i]
		}
	}
	return nil
}

// Member is one bridge member port.
type Member struct {
	Name      string        `json:"name"`
	Flags     []string      `json:"flags"`
	IfMaxAddr int           `json:"ifmaxaddr"`
	PortNo    int           `json:"port_no"`
	Priority  int           `json:"priority"`
	PathCost  int           `json:"path_cost"`
	Proto     string        `json:"proto"`
	Role      string        `json:"role"`
	State     string        `json:"state"`
	VLANProto string        `json:"vlan_proto"`
	Untagged  int           `json:"untagged"`
	Tagged    config.VIDSet `json:"tagged"`
}

func (m *Member) Has(f string) bool { return has(m.Flags, f) }

// Lagg is lagg(4) state.
type Lagg struct {
	Proto string     `json:"proto"`
	Hash  string     `json:"hash"`
	Ports []LaggPort `json:"ports"`
	LACP  string     `json:"lacp,omitempty"`
}

type LaggPort struct {
	Name  string   `json:"name"`
	Flags []string `json:"flags"` // ACTIVE, COLLECTING, DISTRIBUTING, MASTER
}

// MACEntry is one entry of the bridge forwarding table.
type MACEntry struct {
	MAC    string `json:"mac"`
	VLAN   int    `json:"vlan"`
	Port   string `json:"port"`
	Expire int    `json:"expire"`
	Static bool   `json:"static"`
	Sticky bool   `json:"sticky"`
}

// Snapshot is the complete observed state.
type Snapshot struct {
	Interfaces []Interface `json:"interfaces"`
}

func (s *Snapshot) Get(name string) *Interface {
	for i := range s.Interfaces {
		if s.Interfaces[i].Name == name {
			return &s.Interfaces[i]
		}
	}
	return nil
}

// Lookup implements config.Inventory.
func (s *Snapshot) Lookup(name string) (bool, bool) {
	i := s.Get(name)
	if i == nil {
		return false, false
	}
	return true, i.Physical()
}

// Ethernet returns all physical Ethernet interfaces in natural order.
func (s *Snapshot) Ethernet() []Interface {
	var out []Interface
	for _, i := range s.Interfaces {
		if i.Physical() {
			out = append(out, i)
		}
	}
	return out
}

func has(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
