package plan

import (
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

// PersistOps returns the rc.conf updates written when the running config
// is saved as startup config. Only the management interface (and services)
// go into rc.conf: if bsdswitchd ever fails to start, the box still comes
// up reachable on its management address, with all switch ports down.
// The bridge itself is deliberately *not* in rc.conf; bsdswitchd owns it.
func PersistOps(c, prev *config.Config) []backend.Op {
	m := c.Management
	var ops []backend.Op
	h := c.System.Hostname
	if c.System.Domain != "" {
		h += "." + c.System.Domain
	}
	args := []string{"sysrc", "hostname=" + h}
	switch m.IPv4.Mode {
	case config.IPModeDHCP:
		args = append(args, "ifconfig_"+m.Interface+"=DHCP")
	case config.IPModeStatic:
		args = append(args, "ifconfig_"+m.Interface+"=inet "+m.IPv4.Address)
	default:
		args = append(args, "ifconfig_"+m.Interface+"=up")
	}
	switch m.IPv6.Mode {
	case config.IPModeStatic:
		args = append(args, "ifconfig_"+m.Interface+"_ipv6=inet6 "+m.IPv6.Address)
	case config.IPModeSLAAC, config.IPModeDHCP:
		args = append(args, "ifconfig_"+m.Interface+"_ipv6=inet6 accept_rtadv")
	}
	gw4, gw6 := "", ""
	if m.IPv4.Mode == config.IPModeStatic {
		gw4 = m.IPv4.Gateway
	}
	if m.IPv6.Mode == config.IPModeStatic {
		gw6 = m.IPv6.Gateway
	}
	if gw4 != "" {
		args = append(args, "defaultrouter="+gw4)
	}
	if gw6 != "" {
		args = append(args, "ipv6_defaultrouter="+gw6)
	}
	ops = append(ops, backend.Exec(args...))
	var rm []string
	if gw4 == "" {
		rm = append(rm, "defaultrouter")
	}
	if gw6 == "" {
		rm = append(rm, "ipv6_defaultrouter")
	}
	if m.IPv6.Mode == config.IPModeNone {
		rm = append(rm, "ifconfig_"+m.Interface+"_ipv6")
	}
	if prev != nil && prev.Management.Interface != "" && prev.Management.Interface != m.Interface {
		rm = append(rm, "ifconfig_"+prev.Management.Interface, "ifconfig_"+prev.Management.Interface+"_ipv6")
	}
	if len(rm) > 0 {
		// sysrc -x fails for unset variables; that is harmless.
		ops = append(ops, backend.Op{Argv: append([]string{"sysrc", "-x"}, rm...), IgnoreError: true})
	}
	return ops
}
