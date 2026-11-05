// Package config defines the declarative bsdswitch configuration
// (config.yaml), its defaults, parsing, serialisation and validation.
package config

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

const (
	SchemaVersion = 1
	MinVID        = 1
	MaxVID        = 4094
	DefaultVID    = 1
	MinMTU        = 576
	MaxMTU        = 9216
	DefaultMTU    = 1500

	ModeAccess = "access"
	ModeTrunk  = "trunk"

	IPModeNone   = "none"
	IPModeDHCP   = "dhcp"
	IPModeStatic = "static"
	IPModeSLAAC  = "slaac"

	STPModeRSTP = "rstp"
	STPModeSTP  = "stp"
)

// Config is the complete desired state of the appliance.
type Config struct {
	Version    int        `yaml:"version" json:"version"`
	System     System     `yaml:"system" json:"system"`
	Management Management `yaml:"management" json:"management"`
	Bridge     Bridge     `yaml:"bridge" json:"bridge"`
	VLANs      []VLAN     `yaml:"vlans" json:"vlans"`
	Ports      []Port     `yaml:"ports" json:"ports"`
	StaticMACs []StaticMAC `yaml:"static_macs,omitempty" json:"static_macs"`
	// Unused lists detected interfaces the admin explicitly marked as
	// unused (neither management nor switch port). Purely informational.
	Unused []string `yaml:"unused,omitempty" json:"unused"`

	// Phase 2 features.
	LAGs       []LAG       `yaml:"lags,omitempty" json:"lags"`
	Mirrors    []Mirror    `yaml:"mirrors,omitempty" json:"mirrors"`
	ACLs       []ACL       `yaml:"acls,omitempty" json:"acls"`
	RateLimits []RateLimit `yaml:"rate_limits,omitempty" json:"rate_limits"`
	SNMP       SNMP        `yaml:"snmp,omitempty" json:"snmp"`
	LLDP       LLDP        `yaml:"lldp,omitempty" json:"lldp"`
	Syslog     Syslog      `yaml:"syslog,omitempty" json:"syslog"`
}

type System struct {
	Hostname string   `yaml:"hostname" json:"hostname"`
	Domain   string   `yaml:"domain,omitempty" json:"domain"`
	Timezone string   `yaml:"timezone,omitempty" json:"timezone"`
	DNS      []string `yaml:"dns,omitempty" json:"dns"`
	NTP      []string `yaml:"ntp,omitempty" json:"ntp"`
}

type IPConfig struct {
	Mode    string `yaml:"mode" json:"mode"`
	Address string `yaml:"address,omitempty" json:"address"` // CIDR
	Gateway string `yaml:"gateway,omitempty" json:"gateway"`
}

type InBand struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	VLAN    int      `yaml:"vlan,omitempty" json:"vlan"`
	IPv4    IPConfig `yaml:"ipv4,omitempty" json:"ipv4"`
	IPv6    IPConfig `yaml:"ipv6,omitempty" json:"ipv6"`
}

type SSH struct {
	Enabled      bool `yaml:"enabled" json:"enabled"`
	PasswordAuth bool `yaml:"password_auth" json:"password_auth"`
	Port         int  `yaml:"port,omitempty" json:"port"`
}

type Management struct {
	Interface      string   `yaml:"interface" json:"interface"`
	IPv4           IPConfig `yaml:"ipv4" json:"ipv4"`
	IPv6           IPConfig `yaml:"ipv6" json:"ipv6"`
	InBand         InBand   `yaml:"inband" json:"inband"`
	AllowedSubnets []string `yaml:"allowed_subnets,omitempty" json:"allowed_subnets"`
	SSH            SSH      `yaml:"ssh" json:"ssh"`
	HTTPSPort      int      `yaml:"https_port,omitempty" json:"https_port"`
	HTTPPort       int      `yaml:"http_port,omitempty" json:"http_port"`
	SessionTimeout int      `yaml:"session_timeout,omitempty" json:"session_timeout"` // minutes
}

type STP struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	Mode      string `yaml:"mode" json:"mode"`
	Priority  int    `yaml:"priority" json:"priority"`
	HelloTime int    `yaml:"hello" json:"hello"`
	MaxAge    int    `yaml:"max_age" json:"max_age"`
	FwdDelay  int    `yaml:"fwd_delay" json:"fwd_delay"`
	HoldCount int    `yaml:"hold_count,omitempty" json:"hold_count"`
}

type Bridge struct {
	Name         string `yaml:"name" json:"name"`
	MACAging     int    `yaml:"mac_aging" json:"mac_aging"`
	MaxAddresses int    `yaml:"max_addresses" json:"max_addresses"`
	MTU          int    `yaml:"mtu,omitempty" json:"mtu"`
	STP          STP    `yaml:"stp" json:"stp"`
	// AllowVirtual allows non-physical interfaces (epair, tap) as switch
	// ports. Used by the integration test lab.
	AllowVirtual bool `yaml:"allow_virtual,omitempty" json:"allow_virtual"`
}

type VLAN struct {
	ID   int    `yaml:"id" json:"id"`
	Name string `yaml:"name" json:"name"`
}

type PortSTP struct {
	Enabled  *bool `yaml:"enabled,omitempty" json:"enabled"`
	Edge     bool  `yaml:"edge,omitempty" json:"edge"`
	AutoEdge *bool `yaml:"auto_edge,omitempty" json:"auto_edge"`
	Priority int   `yaml:"priority,omitempty" json:"priority"`   // 0-240 step 16, default 128
	PathCost int   `yaml:"path_cost,omitempty" json:"path_cost"` // 0 = auto
}

type Port struct {
	Name        string  `yaml:"name" json:"name"`
	Description string  `yaml:"description,omitempty" json:"description"`
	Shutdown    bool    `yaml:"shutdown,omitempty" json:"shutdown"`
	MTU         int     `yaml:"mtu,omitempty" json:"mtu"`
	Speed       string  `yaml:"speed,omitempty" json:"speed"`   // auto | 10 | 100 | 1000 | 2500 | 5000 | 10000 | 25000 | 40000 | 100000
	Duplex      string  `yaml:"duplex,omitempty" json:"duplex"` // auto | full | half
	Mode        string  `yaml:"mode" json:"mode"`
	PVID        int     `yaml:"pvid,omitempty" json:"pvid"`     // access VLAN
	Native      int     `yaml:"native,omitempty" json:"native"` // trunk untagged VLAN, 0 = none
	Tagged      VIDSet  `yaml:"tagged,omitempty" json:"tagged"`
	STP         PortSTP `yaml:"stp,omitempty" json:"stp"`
	MaxAddr     int     `yaml:"max_addresses,omitempty" json:"max_addresses"` // port security, 0 = unlimited
	Private     bool    `yaml:"isolated,omitempty" json:"isolated"`           // protected port
	QinQ        bool    `yaml:"qinq,omitempty" json:"qinq"`
	VLANProto   string  `yaml:"vlan_proto,omitempty" json:"vlan_proto"` // 802.1q | 802.1ad
	Offloads    string  `yaml:"offloads,omitempty" json:"offloads"`     // "safe" (default: -lro -tso -vlanhwfilter) | "keep"
}

type StaticMAC struct {
	MAC  string `yaml:"mac" json:"mac"`
	Port string `yaml:"port" json:"port"`
	VLAN int    `yaml:"vlan" json:"vlan"`
}

// ---- Phase 2 -----------------------------------------------------------

type LAG struct {
	Name     string   `yaml:"name" json:"name"` // lagg0
	Protocol string   `yaml:"protocol" json:"protocol"` // lacp | failover | loadbalance | roundrobin
	Members  []string `yaml:"members" json:"members"`
	Hash     string   `yaml:"hash,omitempty" json:"hash"` // l2,l3,l4
	LACPFast bool     `yaml:"lacp_fast,omitempty" json:"lacp_fast"`
}

type Mirror struct {
	Destination string `yaml:"destination" json:"destination"` // span port (receives copy of all bridge traffic)
}

type ACLRule struct {
	Action   string `yaml:"action" json:"action"` // pass | block
	Proto    string `yaml:"proto,omitempty" json:"proto"`
	Src      string `yaml:"src,omitempty" json:"src"`
	Dst      string `yaml:"dst,omitempty" json:"dst"`
	DstPort  string `yaml:"dst_port,omitempty" json:"dst_port"`
	Comment  string `yaml:"comment,omitempty" json:"comment"`
}

type ACL struct {
	Name      string    `yaml:"name" json:"name"`
	Port      string    `yaml:"port,omitempty" json:"port"`
	Direction string    `yaml:"direction" json:"direction"` // in | out
	Default   string    `yaml:"default" json:"default"`     // pass | block
	Rules     []ACLRule `yaml:"rules" json:"rules"`
}

type RateLimit struct {
	Port    string `yaml:"port" json:"port"`
	Ingress int    `yaml:"ingress_kbps,omitempty" json:"ingress_kbps"`
	Egress  int    `yaml:"egress_kbps,omitempty" json:"egress_kbps"`
}

type SNMP struct {
	Enabled   bool   `yaml:"enabled,omitempty" json:"enabled"`
	Community string `yaml:"community,omitempty" json:"community"`
	Location  string `yaml:"location,omitempty" json:"location"`
	Contact   string `yaml:"contact,omitempty" json:"contact"`
}

type LLDP struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled"`
}

type Syslog struct {
	Servers []string `yaml:"servers,omitempty" json:"servers"` // host[:port]
}

// ---- helpers -------------------------------------------------------------

func boolp(b bool) *bool { return &b }

// Bool returns the value of a *bool with a default.
func Bool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Default returns a factory-default configuration for the given
// management interface and switch ports.
func Default(mgmt string, ports []string) *Config {
	c := &Config{
		Version: SchemaVersion,
		System:  System{Hostname: "bsdswitch", Timezone: "UTC"},
		Management: Management{
			Interface: mgmt,
			IPv4:      IPConfig{Mode: IPModeDHCP},
			IPv6:      IPConfig{Mode: IPModeSLAAC},
			SSH:       SSH{Enabled: true, PasswordAuth: true},
		},
		Bridge: Bridge{
			Name: "bridge0",
			STP:  STP{Enabled: true, Mode: STPModeRSTP, Priority: 32768},
		},
		VLANs: []VLAN{{ID: 1, Name: "default"}},
	}
	for _, p := range ports {
		c.Ports = append(c.Ports, Port{Name: p, Mode: ModeAccess, PVID: 1})
	}
	c.ApplyDefaults()
	return c
}

// ApplyDefaults fills zero values with defaults. It is idempotent.
func (c *Config) ApplyDefaults() {
	if c.Version == 0 {
		c.Version = SchemaVersion
	}
	if c.System.Hostname == "" {
		c.System.Hostname = "bsdswitch"
	}
	if c.System.Timezone == "" {
		c.System.Timezone = "UTC"
	}
	m := &c.Management
	if m.IPv4.Mode == "" {
		m.IPv4.Mode = IPModeDHCP
	}
	if m.IPv6.Mode == "" {
		m.IPv6.Mode = IPModeNone
	}
	if m.InBand.IPv4.Mode == "" {
		m.InBand.IPv4.Mode = IPModeNone
	}
	if m.InBand.IPv6.Mode == "" {
		m.InBand.IPv6.Mode = IPModeNone
	}
	if m.SSH.Port == 0 {
		m.SSH.Port = 22
	}
	if m.HTTPSPort == 0 {
		m.HTTPSPort = 443
	}
	if m.HTTPPort == 0 {
		m.HTTPPort = 80
	}
	if m.SessionTimeout == 0 {
		m.SessionTimeout = 30
	}
	b := &c.Bridge
	if b.Name == "" {
		b.Name = "bridge0"
	}
	if b.MACAging == 0 {
		b.MACAging = 300
	}
	if b.MaxAddresses == 0 {
		b.MaxAddresses = 2000
	}
	if b.MTU == 0 {
		b.MTU = DefaultMTU
	}
	s := &b.STP
	if s.Mode == "" {
		s.Mode = STPModeRSTP
	}
	if s.HelloTime == 0 {
		s.HelloTime = 2
	}
	if s.MaxAge == 0 {
		s.MaxAge = 20
	}
	if s.FwdDelay == 0 {
		s.FwdDelay = 15
	}
	if s.HoldCount == 0 {
		s.HoldCount = 6
	}
	if len(c.VLANs) == 0 {
		c.VLANs = []VLAN{{ID: 1, Name: "default"}}
	}
	for i := range c.Ports {
		p := &c.Ports[i]
		if p.Mode == "" {
			p.Mode = ModeAccess
		}
		if p.Mode == ModeAccess && p.PVID == 0 {
			p.PVID = DefaultVID
		}
		if p.Speed == "" {
			p.Speed = "auto"
		}
		if p.Duplex == "" {
			p.Duplex = "auto"
		}
		if p.STP.Priority == 0 {
			p.STP.Priority = 128
		}
		if p.Tagged == nil {
			p.Tagged = VIDSet{}
		}
		if p.Offloads == "" {
			p.Offloads = "safe"
		}
	}
	for i := range c.StaticMACs {
		c.StaticMACs[i].MAC = strings.ToLower(c.StaticMACs[i].MAC)
		if c.StaticMACs[i].VLAN == 0 {
			c.StaticMACs[i].VLAN = DefaultVID
		}
	}
	c.Sort()
}

// Sort puts lists into canonical order so serialisation and diffs are
// stable.
func (c *Config) Sort() {
	sort.Slice(c.VLANs, func(i, j int) bool { return c.VLANs[i].ID < c.VLANs[j].ID })
	sort.SliceStable(c.Ports, func(i, j int) bool { return NaturalLess(c.Ports[i].Name, c.Ports[j].Name) })
	for i := range c.Ports {
		c.Ports[i].Tagged = c.Ports[i].Tagged.Normalize()
	}
	sort.Slice(c.StaticMACs, func(i, j int) bool {
		a, b := c.StaticMACs[i], c.StaticMACs[j]
		if a.VLAN != b.VLAN {
			return a.VLAN < b.VLAN
		}
		return a.MAC < b.MAC
	})
	sort.Slice(c.LAGs, func(i, j int) bool { return NaturalLess(c.LAGs[i].Name, c.LAGs[j].Name) })
}

// NaturalLess compares interface names so that vtnet2 < vtnet10.
func NaturalLess(a, b string) bool {
	pa, na := splitNum(a)
	pb, nb := splitNum(b)
	if pa != pb {
		return pa < pb
	}
	if na != nb {
		return na < nb
	}
	return a < b
}

func splitNum(s string) (string, int) {
	i := len(s)
	for i > 0 && unicode.IsDigit(rune(s[i-1])) {
		i--
	}
	n, err := strconv.Atoi(s[i:])
	if err != nil {
		return s, -1
	}
	return s[:i], n
}

// Port returns a pointer to the named port or nil.
func (c *Config) Port(name string) *Port {
	for i := range c.Ports {
		if c.Ports[i].Name == name {
			return &c.Ports[i]
		}
	}
	return nil
}

func (c *Config) VLAN(id int) *VLAN {
	for i := range c.VLANs {
		if c.VLANs[i].ID == id {
			return &c.VLANs[i]
		}
	}
	return nil
}

// Untagged returns the untagged VLAN of a port (0 = none).
func (p *Port) Untagged() int {
	if p.Mode == ModeTrunk {
		return p.Native
	}
	return p.PVID
}

// TaggedSet returns the tagged VLANs (only trunks carry tagged VLANs).
func (p *Port) TaggedSet() VIDSet {
	if p.Mode == ModeTrunk {
		return p.Tagged.Normalize()
	}
	return VIDSet{}
}

// Membership of a port in a VLAN.
const (
	MemberNone     = ""
	MemberUntagged = "U"
	MemberTagged   = "T"
)

func (p *Port) Membership(vid int) string {
	if p.Untagged() == vid {
		return MemberUntagged
	}
	if p.TaggedSet().Contains(vid) {
		return MemberTagged
	}
	return MemberNone
}

// SetMembership changes a port's membership in vid, switching the port's
// mode as required (like the VLAN matrix on HP/Netgear smart switches):
//   - U on an access port moves its PVID; on a trunk it sets the native VLAN
//   - T converts an access port into a trunk whose native VLAN is the old PVID
//   - none removes the VLAN
func (p *Port) SetMembership(vid int, m string) {
	switch m {
	case MemberUntagged:
		if p.Mode == ModeTrunk {
			p.Tagged = p.Tagged.Remove(vid)
			p.Native = vid
		} else {
			p.PVID = vid
		}
	case MemberTagged:
		if p.Mode != ModeTrunk {
			p.Mode = ModeTrunk
			p.Native = p.PVID
			p.PVID = 0
		}
		if p.Native == vid {
			p.Native = 0
		}
		p.Tagged = p.Tagged.Add(vid)
	default:
		if p.Mode == ModeTrunk {
			p.Tagged = p.Tagged.Remove(vid)
			if p.Native == vid {
				p.Native = 0
			}
		} else if p.PVID == vid {
			// An access port must be in exactly one VLAN; removing
			// it falls back to the default VLAN (or none if vid is 1).
			if vid == DefaultVID {
				p.PVID = 0
			} else {
				p.PVID = DefaultVID
			}
		}
	}
}

// Clone returns a deep copy.
func (c *Config) Clone() *Config {
	b, err := c.Marshal()
	if err != nil {
		panic(err)
	}
	n, err := Parse(b)
	if err != nil {
		panic(fmt.Sprintf("config clone: %v", err))
	}
	return n
}

// Parse decodes YAML, applies defaults. It does not validate.
func Parse(data []byte) (*Config, error) {
	// Pre-populate fields where zero is a legitimate value so that an
	// omitted key gets the default but an explicit 0 is kept.
	c := Config{Bridge: Bridge{STP: STP{Enabled: true, Priority: 32768}}}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if err.Error() == "EOF" {
			return nil, fmt.Errorf("config is empty")
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Version > SchemaVersion {
		return nil, fmt.Errorf("config schema version %d is newer than supported (%d)", c.Version, SchemaVersion)
	}
	c.ApplyDefaults()
	return &c, nil
}

// Marshal renders canonical YAML.
func (c *Config) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# bsdswitch configuration - managed by bsdswitchd\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	enc.Close()
	return buf.Bytes(), nil
}

// Load reads and parses a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Save writes atomically (write temp + rename).
func Save(path string, c *Config) error {
	b, err := c.Marshal()
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, b, 0o640)
}

// WriteFileAtomic writes data to a temp file in the same dir, fsyncs and
// renames it over path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := path[:strings.LastIndex(path, "/")+1]
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
