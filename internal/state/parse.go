package state

import (
	"bufio"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

var (
	headerRe   = regexp.MustCompile(`^([A-Za-z0-9_.]+): flags=([0-9a-f]+)(?:<([^>]*)>)? (?:metric \d+ )?mtu (\d+)`)
	bitsRe     = regexp.MustCompile(`=([0-9a-fA-F]+)(?:<([^>]*)>)?`)
	memberRe   = regexp.MustCompile(`^member: (\S+) flags=[0-9a-fA-F]+(?:<([^>]*)>)?`)
	macEntryRe = regexp.MustCompile(`^([0-9a-f]{1,2}(?::[0-9a-f]{1,2}){5}) Vlan(\d+) (\S+) (\d+) flags=[0-9a-fA-F]+(?:<([^>]*)>)?`)
	laggPortRe = regexp.MustCompile(`^laggport: (\S+) flags=[0-9a-fA-F]+(?:<([^>]*)>)?`)
)

// ParseIfconfig parses the output of `ifconfig -a -m -D` (any subset of
// those flags works). cloners is the output of `ifconfig -C` split into
// words; interfaces whose name starts with a cloner prefix are marked
// Cloned.
func ParseIfconfig(out string, cloners []string) ([]Interface, error) {
	var ifs []Interface
	var cur *Interface
	inSupported := false
	var pendingMember *Member
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		raw := sc.Text()
		if raw == "" {
			continue
		}
		if raw[0] != '\t' && raw[0] != ' ' {
			m := headerRe.FindStringSubmatch(raw)
			if m == nil {
				return nil, fmt.Errorf("unexpected ifconfig line: %q", raw)
			}
			ifs = append(ifs, Interface{Name: m[1]})
			cur = &ifs[len(ifs)-1]
			cur.Flags = splitList(m[3])
			cur.MTU, _ = strconv.Atoi(m[4])
			cur.Cloned = isCloned(cur.Name, cloners)
			inSupported = false
			pendingMember = nil
			continue
		}
		if cur == nil {
			continue
		}
		// Supported media list entries are indented with two tabs.
		if strings.HasPrefix(raw, "\t\t") && inSupported {
			cur.SupportedMedia = append(cur.SupportedMedia, strings.TrimSpace(raw))
			continue
		}
		line := strings.TrimSpace(raw)
		// Continuation lines of a bridge member are indented with tab +
		// 8 spaces ("\t        port 2 priority 128 ...").
		if pendingMember != nil && strings.HasPrefix(raw, "\t ") {
			parseMemberAttrs(pendingMember, line)
			continue
		}
		if pendingMember != nil {
			cur.Bridge.Members = append(cur.Bridge.Members, *pendingMember)
			pendingMember = nil
		}
		inSupported = false
		key, rest := splitKey(line)
		switch {
		case key == "description:":
			cur.Description = rest
		case strings.HasPrefix(line, "options="):
			cur.Options = bitsList(line)
		case strings.HasPrefix(line, "capabilities="):
			cur.Capabilities = bitsList(line)
		case key == "capabilities":
			cur.Capabilities = strings.Split(rest, ",")
		case key == "ether":
			cur.MAC = strings.ToLower(rest)
			cur.IsEthernet = true
		case key == "hwaddr":
			// real hardware address, informational
		case key == "inet":
			if a := parseInet(rest); a != "" {
				cur.IPv4 = append(cur.IPv4, a)
			}
		case key == "inet6":
			if a := parseInet6(rest); a != "" {
				cur.IPv6 = append(cur.IPv6, a)
			}
		case key == "media:":
			cur.Media, cur.MediaActive = parseMedia(rest)
			if strings.HasPrefix(rest, "Ethernet") {
				cur.IsEthernet = cur.IsEthernet || cur.MAC != ""
			}
		case key == "status:":
			cur.Status = rest
		case line == "supported media:":
			inSupported = true
		case key == "drivername:":
			cur.DriverUnit = rest
			cur.Driver = strings.TrimRight(rest, "0123456789")
		case key == "groups:":
			cur.Groups = strings.Fields(rest)
		case key == "vlan:":
			parseVLAN(cur, line)
		case key == "laggproto":
			if cur.Lagg == nil {
				cur.Lagg = &Lagg{}
			}
			f := strings.Fields(rest)
			if len(f) > 0 {
				cur.Lagg.Proto = f[0]
			}
			for i := 0; i+1 < len(f); i++ {
				if f[i] == "lagghash" {
					cur.Lagg.Hash = f[i+1]
				}
			}
		case key == "laggport:":
			if m := laggPortRe.FindStringSubmatch(line); m != nil {
				if cur.Lagg == nil {
					cur.Lagg = &Lagg{}
				}
				cur.Lagg.Ports = append(cur.Lagg.Ports, LaggPort{Name: m[1], Flags: splitList(m[2])})
			}
		case key == "lag" && strings.HasPrefix(rest, "id:"):
			if cur.Lagg != nil {
				cur.Lagg.LACP = strings.TrimSpace(strings.TrimPrefix(rest, "id:"))
			}
		case key == "id" && cur.Bridge == nil && strings.Contains(rest, "hellotime"):
			cur.Bridge = &Bridge{}
			parseBridgeKV(cur.Bridge, line)
		case cur.Bridge != nil && (key == "maxage" || key == "root"):
			parseBridgeKV(cur.Bridge, line)
		case cur.Bridge != nil && key == "bridge":
			m := bitsRe.FindStringSubmatch(line)
			if m != nil {
				cur.Bridge.Flags = splitList(m[2])
			}
			if i := strings.Index(line, "defuntagged="); i >= 0 {
				cur.Bridge.DefUntagged, _ = strconv.Atoi(strings.Fields(line[i+len("defuntagged="):])[0])
			}
		case cur.Bridge != nil && key == "member:":
			m := memberRe.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("unexpected bridge member line: %q", line)
			}
			pendingMember = &Member{Name: m[1], Flags: splitList(m[2]), Tagged: config.VIDSet{}}
			if has(pendingMember.Flags, "SPAN") {
				cur.Bridge.Spans = append(cur.Bridge.Spans, m[1])
			}
		}
	}
	if pendingMember != nil && cur != nil && cur.Bridge != nil {
		cur.Bridge.Members = append(cur.Bridge.Members, *pendingMember)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// Span ports are listed as members with SPAN flag; keep them out of
	// the member list.
	for i := range ifs {
		if b := ifs[i].Bridge; b != nil {
			var mem []Member
			for _, m := range b.Members {
				if !m.Has("SPAN") {
					mem = append(mem, m)
				}
			}
			b.Members = mem
		}
		if ifs[i].Driver == "" {
			ifs[i].Driver = strings.TrimRight(ifs[i].Name, "0123456789")
		}
	}
	return ifs, nil
}

func isCloned(name string, cloners []string) bool {
	for _, c := range cloners {
		if c == "" {
			continue
		}
		if strings.HasPrefix(name, c) {
			rest := name[len(c):]
			if rest == "" {
				continue
			}
			// epair0a / epair0b, vlan10, bridge0, bridge0.10, lagg0, tap3
			if rest[0] >= '0' && rest[0] <= '9' {
				return true
			}
		}
	}
	// dotted vlan names (vtnet1.10, bridge0.99) are vlan(4) clones
	return strings.Contains(name, ".")
}

func splitKey(line string) (string, string) {
	i := strings.IndexByte(line, ' ')
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}

func splitList(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func bitsList(line string) []string {
	m := bitsRe.FindStringSubmatch(line)
	if m == nil {
		return []string{}
	}
	return splitList(m[2])
}

func parseMedia(rest string) (configured, active string) {
	// "Ethernet autoselect (1000baseT <full-duplex>)"
	if i := strings.Index(rest, " ("); i >= 0 && strings.HasSuffix(rest, ")") {
		return rest[:i], rest[i+2 : len(rest)-1]
	}
	// no parenthesised active media: configured == active
	r := strings.TrimPrefix(rest, "Ethernet ")
	return rest, r
}

func parseInet(rest string) string {
	f := strings.Fields(rest)
	if len(f) == 0 {
		return ""
	}
	if strings.Contains(f[0], "/") {
		return f[0]
	}
	for i := 1; i+1 < len(f); i++ {
		if f[i] == "netmask" {
			m := f[i+1]
			var bits int
			if strings.HasPrefix(m, "0x") {
				v, err := strconv.ParseUint(m[2:], 16, 32)
				if err != nil {
					return f[0]
				}
				for v != 0 {
					bits += int(v & 1)
					v >>= 1
				}
			} else if a, err := netip.ParseAddr(m); err == nil {
				b := a.As4()
				for _, x := range b {
					for ; x != 0; x >>= 1 {
						bits += int(x & 1)
					}
				}
			}
			return fmt.Sprintf("%s/%d", f[0], bits)
		}
	}
	return f[0] + "/32"
}

func parseInet6(rest string) string {
	f := strings.Fields(rest)
	if len(f) == 0 {
		return ""
	}
	addr := f[0]
	if i := strings.IndexByte(addr, '%'); i >= 0 {
		addr = addr[:i]
	}
	for i := 1; i+1 < len(f); i++ {
		if f[i] == "prefixlen" {
			return addr + "/" + f[i+1]
		}
	}
	return addr + "/128"
}

func parseVLAN(cur *Interface, line string) {
	// vlan: 10 vlanproto: 802.1q vlanpcp: 0 parent interface: bridge0
	f := strings.Fields(line)
	for i := 0; i < len(f); i++ {
		switch f[i] {
		case "vlan:":
			if i+1 < len(f) {
				cur.VLANTag, _ = strconv.Atoi(f[i+1])
			}
		case "interface:":
			if i+1 < len(f) {
				cur.VLANParent = f[i+1]
			}
		}
	}
}

// parseBridgeKV parses the bridge parameter lines:
//
//	id 58:9c:fc:10:ff:b6 priority 32768 hellotime 2 fwddelay 15
//	maxage 20 holdcnt 6 proto rstp maxaddr 2000 timeout 1200
//	root id 58:9c:fc:10:ff:b6 priority 32768 ifcost 0 port 0
func parseBridgeKV(b *Bridge, line string) {
	f := strings.Fields(line)
	root := len(f) > 0 && f[0] == "root"
	if root {
		f = f[1:]
	}
	for i := 0; i+1 < len(f); i += 2 {
		k, v := f[i], f[i+1]
		n, _ := strconv.Atoi(v)
		switch k {
		case "id":
			if root {
				b.RootID = v
			} else {
				b.ID = v
			}
		case "priority":
			if root {
				b.RootPriority = n
			} else {
				b.Priority = n
			}
		case "hellotime":
			b.HelloTime = n
		case "fwddelay":
			b.FwdDelay = n
		case "maxage":
			b.MaxAge = n
		case "holdcnt":
			b.HoldCount = n
		case "proto":
			b.Proto = v
		case "maxaddr":
			b.MaxAddr = n
		case "timeout":
			b.Timeout = n
		case "ifcost":
			b.RootCost = n
		case "port":
			b.RootPort = n
		}
	}
}

// parseMemberAttrs parses member continuation lines:
//
//	ifmaxaddr 10 port 2 priority 128 path cost 2000 proto rstp
//	role designated state forwarding vlan protocol 802.1ad untagged 10 tagged 20-30,40
func parseMemberAttrs(m *Member, line string) {
	f := strings.Fields(line)
	for i := 0; i < len(f); i++ {
		next := func() string {
			if i+1 < len(f) {
				i++
				return f[i]
			}
			return ""
		}
		switch f[i] {
		case "ifmaxaddr":
			m.IfMaxAddr, _ = strconv.Atoi(next())
		case "port":
			m.PortNo, _ = strconv.Atoi(next())
		case "priority":
			m.Priority, _ = strconv.Atoi(next())
		case "path":
			if i+2 < len(f) && f[i+1] == "cost" {
				i++
				m.PathCost, _ = strconv.Atoi(next())
			}
		case "proto":
			m.Proto = next()
		case "role":
			m.Role = next()
		case "state":
			m.State = next()
		case "vlan":
			if i+2 < len(f) && f[i+1] == "protocol" {
				i++
				m.VLANProto = next()
			}
		case "untagged":
			m.Untagged, _ = strconv.Atoi(next())
		case "tagged":
			s, err := config.ParseVIDSet(next())
			if err == nil {
				m.Tagged = s
			}
		}
	}
}

// ParseBridgeAddr parses `ifconfig bridge0 addr` output:
//
//	58:9c:fc:00:00:01 Vlan10 vtnet1 1190 flags=0<>
//	0:11:22:33:44:55 Vlan1 vtnet1 0 flags=1<STATIC>
func ParseBridgeAddr(out string) []MACEntry {
	var res []MACEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		m := macEntryRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		e := MACEntry{MAC: normMAC(m[1]), Port: m[3]}
		e.VLAN, _ = strconv.Atoi(m[2])
		e.Expire, _ = strconv.Atoi(m[4])
		fl := splitList(m[5])
		e.Static = has(fl, "STATIC")
		e.Sticky = has(fl, "STICKY")
		res = append(res, e)
	}
	return res
}

// normMAC turns ether_ntoa output ("0:11:2:33:44:55") into canonical
// zero-padded lowercase form.
func normMAC(s string) string {
	parts := strings.Split(strings.ToLower(s), ":")
	for i, p := range parts {
		if len(p) == 1 {
			parts[i] = "0" + p
		}
	}
	return strings.Join(parts, ":")
}

// ParseCloners parses `ifconfig -C`.
func ParseCloners(out string) []string { return strings.Fields(out) }
