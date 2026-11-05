package api

// Capability describes how a typical smart-switch feature maps onto
// FreeBSD. It is shown in the UI (System > About) and in the README.
type Capability struct {
	Feature string `json:"feature"`
	Status  string `json:"status"` // supported, partial, unsupported, planned
	Notes   string `json:"notes"`
}

// Capabilities is the honest feature matrix.
var Capabilities = []Capability{
	{"802.1Q VLANs (access/trunk/native)", "supported", "if_bridge vlanfilter (FreeBSD 15.0+), 4094 VLANs"},
	{"VLAN learning", "supported", "independent VLAN learning (per-VLAN MAC table)"},
	{"RSTP / STP (802.1w / 802.1D)", "supported", "one instance for the whole bridge"},
	{"MSTP / PVST+", "unsupported", "if_bridge implements a single spanning tree; all VLANs share it"},
	{"Static MAC entries, aging, flush", "supported", ""},
	{"Port security (max MACs per port, sticky)", "supported", "ifmaxaddr / sticky"},
	{"Protected ports (port isolation)", "supported", "if_bridge private flag"},
	{"Link aggregation (LACP 802.3ad)", "supported", "lagg(4) as bridge member"},
	{"Port mirroring (SPAN)", "partial", "bridge span ports copy ALL bridged traffic, not per source port/VLAN; no RSPAN"},
	{"ACLs (L3/L4)", "partial", "pf on bridge member ports; IP/TCP/UDP/ICMP of untagged traffic only (tagged frames on trunks pass), costs CPU"},
	{"MAC ACLs", "unsupported", "pf does not filter on MAC addresses"},
	{"Rate limiting / policing", "partial", "dummynet pipes per port; software shaping, adds latency"},
	{"QoS / 802.1p priority queues", "unsupported", "no hardware queues; PCP is passed through unchanged"},
	{"Q-in-Q (802.1ad)", "partial", "per-port qinq / vlanproto 802.1ad; no selective QinQ"},
	{"IGMP / MLD snooping", "unsupported", "if_bridge floods multicast to all ports of the VLAN"},
	{"DHCP snooping / ARP inspection", "unsupported", ""},
	{"802.1X port authentication", "unsupported", ""},
	{"Storm control", "unsupported", "can be approximated with dummynet rate limits"},
	{"LLDP", "supported", "via the lldpd package"},
	{"SNMP (MIB-II, BRIDGE-MIB)", "supported", "bsnmpd with snmp_bridge, read-only"},
	{"Remote syslog", "supported", ""},
	{"Jumbo frames", "partial", "one MTU for the whole bridge; NIC must support it"},
	{"Speed/duplex settings", "partial", "only on NICs that support it; n/a on virtio"},
	{"Hardware offload / line rate", "unsupported", "every frame is forwarded by the CPU; expect a few Gbit/s total, not line rate on many ports"},
	{"PoE", "unsupported", "no PoE hardware"},
	{"Commit-confirm / rollback", "supported", "daemon-side timer, last 10 versions"},
}
