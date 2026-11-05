// Package freebsd implements backend.Backend on a real FreeBSD system.
package freebsd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
)

// Programs maps the command names used in plans to absolute paths.
// Nothing else can be executed.
var Programs = map[string]string{
	"ifconfig":    "/sbin/ifconfig",
	"sysctl":      "/sbin/sysctl",
	"route":       "/sbin/route",
	"hostname":    "/bin/hostname",
	"sysrc":       "/usr/sbin/sysrc",
	"service":     "/usr/sbin/service",
	"dhclient":    "/sbin/dhclient",
	"pkill":       "/bin/pkill",
	"tzsetup":     "/usr/sbin/tzsetup",
	"rtsol":       "/sbin/rtsol",
	"pfctl":       "/sbin/pfctl",
	"dnctl":       "/sbin/dnctl",
	"kldload":     "/sbin/kldload",
	"shutdown":    "/sbin/shutdown",
	"pkg":         "/usr/sbin/pkg",
	"bectl":       "/sbin/bectl",
	"lldpcli":     "/usr/local/sbin/lldpcli",
	"tcpdump":     "/usr/sbin/tcpdump",
}

var (
	ifRe       = `[a-z][a-z0-9_]{0,10}[0-9]+(\.[0-9]{1,4})?`
	ifOnlyRe   = regexp.MustCompile(`^` + ifRe + `$`)
	argRe      = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./\-<> ()\[\]]*$`)
	descRe     = regexp.MustCompile(`^[^\x00-\x1f"'\\` + "`" + `$;|&<>]{0,64}$`)
	sysrcKeyRe = regexp.MustCompile(`^(ifconfig_` + ifRe + `(_ipv6|_alias[0-9]+)?|ifconfig_` + `[a-z][a-z0-9_]{0,10}[0-9]+_[0-9]+(_ipv6)?|defaultrouter|ipv6_defaultrouter|ipv6_activate_all_interfaces|hostname|sshd_enable|sshd_flags|ntpd_enable|ntpd_sync_on_start|bsnmpd_enable|lldpd_enable|pf_enable|pf_rules|dummynet_enable|syslogd_flags|cloned_interfaces|bsdswitchd_enable)$`)
	services   = map[string]bool{"sshd": true, "ntpd": true, "bsnmpd": true, "lldpd": true, "syslogd": true, "pf": true, "dhclient": true, "bsdswitchd": true}
	svcActions = map[string]bool{"start": true, "stop": true, "restart": true, "reload": true, "onestart": true, "onestop": true, "onerestart": true, "status": true}
)

// AllowedFiles may be written through WriteFile.
var AllowedFiles = map[string]bool{
	"/etc/resolv.conf":                      true,
	"/etc/ntp.conf":                         true,
	"/etc/snmpd.config":                     true,
	"/usr/local/etc/lldpd.conf":             true,
	"/usr/local/etc/syslog.d/bsdswitch.conf": true,
	"/usr/local/etc/bsdswitch/pf.conf":      true,
	"/usr/local/etc/bsdswitch/tls/cert.pem": true,
	"/usr/local/etc/bsdswitch/tls/key.pem":  true,
	"/etc/motd.template":                    true,
}

// CheckOp validates an operation against the privilege policy. It is
// enforced by bsdswitch-privd before anything runs as root.
func CheckOp(o backend.Op) error {
	if o.IsFile() {
		if !AllowedFiles[o.Path] {
			return fmt.Errorf("policy: writing %s is not allowed", o.Path)
		}
		if len(o.Content) > 1<<20 {
			return fmt.Errorf("policy: file too large")
		}
		return nil
	}
	if len(o.Argv) == 0 {
		return fmt.Errorf("policy: empty command")
	}
	prog := o.Argv[0]
	if _, ok := Programs[prog]; !ok {
		return fmt.Errorf("policy: program %q is not allowed", prog)
	}
	args := o.Argv[1:]
	for i, a := range args {
		if strings.ContainsAny(a, "\x00\n\r") {
			return fmt.Errorf("policy: control character in argument")
		}
		// descriptions are free text (validated separately)
		if prog == "ifconfig" && i > 0 && args[i-1] == "description" {
			if !descRe.MatchString(a) {
				return fmt.Errorf("policy: invalid description")
			}
			continue
		}
		if prog == "sysrc" || prog == "tcpdump" {
			continue
		}
		if !argRe.MatchString(a) {
			return fmt.Errorf("policy: invalid argument %q", a)
		}
	}
	switch prog {
	case "ifconfig", "dhclient", "rtsol":
		if len(args) == 0 {
			return fmt.Errorf("policy: missing interface")
		}
		start := 0
		if prog == "dhclient" && args[0] == "-b" {
			start = 1
		}
		if len(args) <= start || !ifOnlyRe.MatchString(args[start]) {
			return fmt.Errorf("policy: %s needs an interface name", prog)
		}
	case "sysctl":
		for _, a := range args {
			if !regexp.MustCompile(`^net\.(link\.bridge|inet\.ip|inet6\.ip6)\.[a-z_]+=[0-9]+$`).MatchString(a) {
				return fmt.Errorf("policy: sysctl %q not allowed", a)
			}
		}
	case "route":
		r := strings.Join(args, " ")
		if !regexp.MustCompile(`^(-6 )?(add|change|delete) default( [0-9a-fA-F.:%a-z]+)?$`).MatchString(r) {
			return fmt.Errorf("policy: route %q not allowed", r)
		}
	case "sysrc":
		if len(args) > 0 && args[0] == "-x" {
			if len(args) < 2 {
				return fmt.Errorf("policy: sysrc -x needs names")
			}
			for _, k := range args[1:] {
				if !sysrcKeyRe.MatchString(k) {
					return fmt.Errorf("policy: sysrc -x %q not allowed", k)
				}
			}
			break
		}
		for _, a := range args {
			k, v, hasV := strings.Cut(a, "=")
			if !hasV || !sysrcKeyRe.MatchString(k) {
				return fmt.Errorf("policy: sysrc %q not allowed", a)
			}
			if strings.ContainsAny(v, "\"`$\\;|&<>") {
				return fmt.Errorf("policy: sysrc value contains forbidden characters")
			}
		}
	case "service":
		if len(args) < 2 || !services[args[0]] || !svcActions[args[1]] {
			return fmt.Errorf("policy: service %v not allowed", args)
		}
	case "pkill":
		if len(args) != 2 || args[0] != "-f" || !regexp.MustCompile(`^dhclient: `+ifRe+`$`).MatchString(args[1]) {
			return fmt.Errorf("policy: pkill %v not allowed", args)
		}
	case "hostname":
		if len(args) != 1 || !regexp.MustCompile(`^[A-Za-z0-9.\-]{1,253}$`).MatchString(args[0]) {
			return fmt.Errorf("policy: invalid hostname")
		}
	case "tzsetup":
		if len(args) != 1 || !regexp.MustCompile(`^[A-Za-z0-9_+\-]+(/[A-Za-z0-9_+\-]+)*$`).MatchString(args[0]) {
			return fmt.Errorf("policy: invalid time zone")
		}
	case "shutdown":
		j := strings.Join(args, " ")
		if j != "-r now" && j != "-p now" {
			return fmt.Errorf("policy: shutdown %q not allowed", j)
		}
	case "kldload":
		if len(args) != 2 || args[0] != "-n" || !map[string]bool{"pf": true, "dummynet": true, "if_lagg": true, "if_bridge": true, "if_epair": true}[args[1]] {
			return fmt.Errorf("policy: kldload %v not allowed", args)
		}
	case "pfctl":
		j := strings.Join(args, " ")
		if !map[string]bool{"-e": true, "-d": true, "-f /usr/local/etc/bsdswitch/pf.conf": true, "-nf /usr/local/etc/bsdswitch/pf.conf": true}[j] {
			return fmt.Errorf("policy: pfctl %q not allowed", j)
		}
	case "dnctl":
		if !(len(args) > 1 && (args[0] == "pipe" || args[0] == "-q" && args[1] == "flush")) {
			return fmt.Errorf("policy: dnctl %v not allowed", args)
		}
	case "pkg":
		j := strings.Join(args, " ")
		if !regexp.MustCompile(`^(-N|update|upgrade -y bsdswitch|install -y bsdswitch|add (-f )?/var/db/bsdswitch/update/bsdswitch-[0-9A-Za-z._,\-]+\.pkg|version -vRL= bsdswitch|rquery %v bsdswitch)$`).MatchString(j) {
			return fmt.Errorf("policy: pkg %q not allowed", j)
		}
	case "bectl":
		j := strings.Join(args, " ")
		if !regexp.MustCompile(`^(list -H|create bsdswitch-[0-9A-Za-z._\-]+|activate -t bsdswitch-[0-9A-Za-z._\-]+|mount bsdswitch-[0-9A-Za-z._\-]+ /var/db/bsdswitch/be|umount bsdswitch-[0-9A-Za-z._\-]+)$`).MatchString(j) {
			return fmt.Errorf("policy: bectl %q not allowed", j)
		}
	case "lldpcli":
		j := strings.Join(args, " ")
		if j != "-f json show neighbors details" && j != "-f json show neighbors" {
			return fmt.Errorf("policy: lldpcli %q not allowed", j)
		}
	case "tcpdump":
		// tcpdump is only allowed in capture form, built by the daemon:
		// tcpdump -n -i IF -c N -s SNAP -w FILE [filter words...]
		if len(args) < 9 || args[0] != "-n" || args[1] != "-i" || !ifOnlyRe.MatchString(args[2]) ||
			args[3] != "-c" || args[5] != "-s" || args[7] != "-w" ||
			!regexp.MustCompile(`^/var/db/bsdswitch/capture/[a-f0-9]{16}\.pcap$`).MatchString(args[8]) {
			return fmt.Errorf("policy: tcpdump invocation not allowed")
		}
		for _, a := range args[9:] {
			if !regexp.MustCompile(`^[A-Za-z0-9.:/\-]+$`).MatchString(a) {
				return fmt.Errorf("policy: invalid capture filter")
			}
		}
	}
	return nil
}
