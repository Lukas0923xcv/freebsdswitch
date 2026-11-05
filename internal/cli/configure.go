package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

// configure implements a small Cisco-like configuration mode. Every
// accepted command updates the staged (candidate) configuration on the
// switch; nothing is applied until "commit confirm".
//
//	configure                    interactive
//	configure -c "cmd" -c "cmd"  one-shot
//	configure -f script.txt      from file ("-" = stdin)
func (t *ctl) configure(args []string) error {
	var cmds []string
	interactive := true
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c":
			if i+1 < len(args) {
				cmds = append(cmds, args[i+1])
				i++
				interactive = false
			}
		case "-f":
			if i+1 < len(args) {
				var f *os.File
				if args[i+1] == "-" {
					f = os.Stdin
				} else {
					var err error
					if f, err = os.Open(args[i+1]); err != nil {
						return err
					}
					defer f.Close()
				}
				sc := bufio.NewScanner(f)
				for sc.Scan() {
					cmds = append(cmds, sc.Text())
				}
				i++
				interactive = false
			}
		case "terminal", "t":
		default:
			return fmt.Errorf("unknown argument %q", args[i])
		}
	}
	s := &cfgSession{t: t}
	if err := s.load(); err != nil {
		return err
	}
	if !interactive {
		for _, c := range cmds {
			if err := s.exec(c); err != nil {
				return fmt.Errorf("%s: %w", c, err)
			}
		}
		fmt.Fprintln(t.out, "Changes staged. Review with 'show configuration diff', apply with 'commit confirm'.")
		return nil
	}
	fmt.Fprintln(t.out, "Enter configuration commands, one per line. End with 'end'. Changes are staged until 'commit'.")
	for {
		fmt.Fprint(t.out, s.prompt())
		line, err := t.in.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(t.out)
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "#") {
			continue
		}
		if err := s.exec(line); err != nil {
			if errors.Is(err, errEnd) {
				return nil
			}
			fmt.Fprintln(t.out, "% "+err.Error())
		}
	}
}

var errEnd = errors.New("end")

type cfgSession struct {
	t     *ctl
	c     *config.Config
	iface string // current interface in sub-mode
	vlan  int    // current vlan in sub-mode
}

func (s *cfgSession) load() error {
	var c config.Config
	if err := s.t.c.Get("/api/v1/config/candidate", &c); err != nil {
		return err
	}
	c.ApplyDefaults()
	s.c = &c
	return nil
}

func (s *cfgSession) prompt() string {
	h := s.c.System.Hostname
	switch {
	case s.iface != "":
		return fmt.Sprintf("%s(config-if-%s)# ", h, s.iface)
	case s.vlan != 0:
		return fmt.Sprintf("%s(config-vlan-%d)# ", h, s.vlan)
	}
	return h + "(config)# "
}

// stage pushes the modified copy; on validation failure the local copy is
// reloaded.
func (s *cfgSession) stage(mod func(c *config.Config) error) error {
	n := s.c.Clone()
	if err := mod(n); err != nil {
		return err
	}
	n.ApplyDefaults()
	if err := s.t.c.Do("PUT", "/api/v1/config/candidate", n, nil); err != nil {
		return err
	}
	s.c = n
	return nil
}

func (s *cfgSession) ensureVLAN(c *config.Config, id int) {
	if c.VLAN(id) == nil {
		c.VLANs = append(c.VLANs, config.VLAN{ID: id, Name: fmt.Sprintf("VLAN%04d", id)})
		fmt.Fprintf(s.t.out, "%% VLAN %d created\n", id)
	}
}

func atoiArg(w []string, i int) (int, error) {
	if i >= len(w) {
		return 0, errors.New("missing value")
	}
	n, err := strconv.Atoi(w[i])
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", w[i])
	}
	return n, nil
}

func (s *cfgSession) exec(line string) error {
	w := strings.Fields(line)
	if len(w) == 0 {
		return nil
	}
	neg := false
	if w[0] == "no" {
		neg = true
		w = w[1:]
		if len(w) == 0 {
			return errors.New("incomplete command")
		}
	}
	switch {
	case w[0] == "end":
		return errEnd
	case w[0] == "exit":
		if s.iface == "" && s.vlan == 0 {
			return errEnd
		}
		s.iface, s.vlan = "", 0
		return nil
	case match(w[0], "show", 2):
		if len(w) > 1 && match(w[1], "diff", 1) || len(w) == 1 {
			return s.t.showDiff()
		}
		return s.t.show(w[1:])
	case match(w[0], "commit", 3):
		return s.t.commit(w[1:])
	case w[0] == "abort":
		if err := s.t.c.Do("DELETE", "/api/v1/config/candidate", nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(s.t.out, "% staged changes discarded")
		return s.load()
	}
	if s.iface != "" {
		if err := s.ifaceCmd(w, neg); err != errUnknown {
			return err
		}
	}
	if s.vlan != 0 {
		if w[0] == "name" && len(w) > 1 {
			return s.stage(func(c *config.Config) error {
				c.VLAN(s.vlan).Name = strings.Join(w[1:], " ")
				return nil
			})
		}
	}
	return s.globalCmd(w, neg)
}

var errUnknown = errors.New("unknown command")

func (s *cfgSession) globalCmd(w []string, neg bool) error {
	switch {
	case match(w[0], "interface", 3):
		if len(w) < 2 {
			return errors.New("interface name required")
		}
		if s.c.Port(w[1]) == nil {
			return fmt.Errorf("%s is not a switch port (assign it on the Interfaces page or in the YAML first)", w[1])
		}
		s.iface, s.vlan = w[1], 0
		return nil
	case w[0] == "vlan":
		id, err := atoiArg(w, 1)
		if err != nil {
			return err
		}
		if neg {
			return s.stage(func(c *config.Config) error {
				if id == 1 {
					return errors.New("VLAN 1 cannot be deleted")
				}
				var keep []config.VLAN
				for _, v := range c.VLANs {
					if v.ID != id {
						keep = append(keep, v)
					}
				}
				c.VLANs = keep
				for i := range c.Ports {
					c.Ports[i].SetMembership(id, config.MemberNone)
				}
				return nil
			})
		}
		if err := s.stage(func(c *config.Config) error { s.ensureVLAN(c, id); return nil }); err != nil {
			return err
		}
		s.vlan, s.iface = id, ""
		return nil
	case w[0] == "hostname":
		if len(w) < 2 {
			return errors.New("hostname required")
		}
		return s.stage(func(c *config.Config) error { c.System.Hostname = w[1]; return nil })
	case match(w[0], "spanning-tree", 4):
		if neg && len(w) == 1 {
			return s.stage(func(c *config.Config) error { c.Bridge.STP.Enabled = false; return nil })
		}
		if len(w) < 2 {
			return s.stage(func(c *config.Config) error { c.Bridge.STP.Enabled = true; return nil })
		}
		switch w[1] {
		case "mode":
			if len(w) < 3 {
				return errors.New("mode rstp|stp")
			}
			m := w[2]
			if m == "rapid-pvst" {
				m = "rstp"
			}
			return s.stage(func(c *config.Config) error { c.Bridge.STP.Mode = m; c.Bridge.STP.Enabled = true; return nil })
		case "priority":
			n, err := atoiArg(w, 2)
			if err != nil {
				return err
			}
			return s.stage(func(c *config.Config) error { c.Bridge.STP.Priority = n; return nil })
		case "hello-time", "max-age", "forward-time":
			n, err := atoiArg(w, 2)
			if err != nil {
				return err
			}
			return s.stage(func(c *config.Config) error {
				switch w[1] {
				case "hello-time":
					c.Bridge.STP.HelloTime = n
				case "max-age":
					c.Bridge.STP.MaxAge = n
				default:
					c.Bridge.STP.FwdDelay = n
				}
				return nil
			})
		}
	case w[0] == "mac" && len(w) >= 3 && w[1] == "address-table":
		switch w[2] {
		case "aging-time":
			n, err := atoiArg(w, 3)
			if err != nil {
				return err
			}
			return s.stage(func(c *config.Config) error { c.Bridge.MACAging = n; return nil })
		case "static":
			// mac address-table static MAC vlan N interface P
			if len(w) < 8 {
				return errors.New("usage: mac address-table static MAC vlan N interface PORT")
			}
			mac := strings.ToLower(w[3])
			vid, err := strconv.Atoi(w[5])
			if err != nil {
				return err
			}
			port := w[7]
			return s.stage(func(c *config.Config) error {
				var keep []config.StaticMAC
				for _, m := range c.StaticMACs {
					if !(m.MAC == mac && m.VLAN == vid) {
						keep = append(keep, m)
					}
				}
				if !neg {
					keep = append(keep, config.StaticMAC{MAC: mac, VLAN: vid, Port: port})
				}
				c.StaticMACs = keep
				return nil
			})
		}
	case w[0] == "ip" && len(w) >= 2 && w[1] == "name-server":
		return s.stage(func(c *config.Config) error {
			if neg {
				c.System.DNS = nil
			} else {
				c.System.DNS = w[2:]
			}
			return nil
		})
	case w[0] == "ntp" && len(w) >= 3 && w[1] == "server":
		return s.stage(func(c *config.Config) error {
			var keep []string
			for _, n := range c.System.NTP {
				if n != w[2] {
					keep = append(keep, n)
				}
			}
			if !neg {
				keep = append(keep, w[2])
			}
			c.System.NTP = keep
			return nil
		})
	case w[0] == "clock" && len(w) >= 3 && w[1] == "timezone":
		return s.stage(func(c *config.Config) error { c.System.Timezone = w[2]; return nil })
	case w[0] == "ssh":
		return s.stage(func(c *config.Config) error { c.Management.SSH.Enabled = !neg; return nil })
	}
	return fmt.Errorf("unknown command %q", strings.Join(w, " "))
}

func (s *cfgSession) ifaceCmd(w []string, neg bool) error {
	name := s.iface
	port := func(c *config.Config) *config.Port { return c.Port(name) }
	switch {
	case match(w[0], "description", 4):
		return s.stage(func(c *config.Config) error {
			if neg {
				port(c).Description = ""
			} else {
				port(c).Description = strings.Join(w[1:], " ")
			}
			return nil
		})
	case match(w[0], "shutdown", 4):
		return s.stage(func(c *config.Config) error { port(c).Shutdown = !neg; return nil })
	case w[0] == "speed":
		v := "auto"
		if !neg && len(w) > 1 {
			v = w[1]
		}
		return s.stage(func(c *config.Config) error {
			p := port(c)
			p.Speed = v
			if v == "auto" {
				p.Duplex = "auto"
			}
			return nil
		})
	case w[0] == "duplex":
		v := "auto"
		if !neg && len(w) > 1 {
			v = w[1]
		}
		return s.stage(func(c *config.Config) error { port(c).Duplex = v; return nil })
	case match(w[0], "switchport", 3):
		if len(w) < 2 {
			return errors.New("incomplete command")
		}
		switch {
		case w[1] == "mode" && len(w) >= 3:
			return s.stage(func(c *config.Config) error {
				p := port(c)
				if w[2] == p.Mode {
					return nil
				}
				switch w[2] {
				case "trunk":
					p.Mode, p.Native, p.PVID = config.ModeTrunk, p.PVID, 0
				case "access":
					p.Mode, p.PVID, p.Native, p.Tagged = config.ModeAccess, max(p.Native, 1), 0, config.VIDSet{}
				default:
					return errors.New("mode access|trunk")
				}
				return nil
			})
		case w[1] == "access" && len(w) >= 4 && w[2] == "vlan":
			id, err := atoiArg(w, 3)
			if err != nil {
				return err
			}
			return s.stage(func(c *config.Config) error {
				s.ensureVLAN(c, id)
				p := port(c)
				if p.Mode != config.ModeAccess {
					return errors.New("port is a trunk; use 'switchport mode access' first")
				}
				p.PVID = id
				return nil
			})
		case w[1] == "trunk" && len(w) >= 5 && w[2] == "native" && w[3] == "vlan":
			id, err := atoiArg(w, 4)
			if err != nil {
				return err
			}
			return s.stage(func(c *config.Config) error {
				p := port(c)
				if p.Mode != config.ModeTrunk {
					return errors.New("port is not a trunk")
				}
				if neg {
					p.Native = 0
					return nil
				}
				s.ensureVLAN(c, id)
				p.Tagged = p.Tagged.Remove(id)
				p.Native = id
				return nil
			})
		case w[1] == "trunk" && len(w) >= 5 && w[2] == "allowed" && w[3] == "vlan":
			op, list := "set", w[4]
			if (w[4] == "add" || w[4] == "remove" || w[4] == "except") && len(w) >= 6 {
				op, list = w[4], w[5]
			}
			set, err := config.ParseVIDSet(list)
			if err != nil {
				return err
			}
			return s.stage(func(c *config.Config) error {
				p := port(c)
				if p.Mode != config.ModeTrunk {
					return errors.New("port is not a trunk; use 'switchport mode trunk' first")
				}
				if op != "remove" {
					for _, v := range set {
						s.ensureVLAN(c, v)
					}
				}
				switch op {
				case "set":
					p.Tagged = set
				case "add":
					p.Tagged = append(p.Tagged, set...).Normalize()
				case "remove":
					p.Tagged = p.Tagged.Minus(set)
				default:
					return errors.New("'except' is not supported")
				}
				if p.Native != 0 {
					p.Tagged = p.Tagged.Remove(p.Native)
				}
				return nil
			})
		case w[1] == "port-security" && len(w) >= 4 && w[2] == "maximum":
			n, err := atoiArg(w, 3)
			if err != nil {
				return err
			}
			return s.stage(func(c *config.Config) error { port(c).MaxAddr = n; return nil })
		case w[1] == "port-security" && neg:
			return s.stage(func(c *config.Config) error { port(c).MaxAddr = 0; return nil })
		case w[1] == "protected":
			return s.stage(func(c *config.Config) error { port(c).Private = !neg; return nil })
		}
		return fmt.Errorf("unknown switchport command")
	case match(w[0], "spanning-tree", 4):
		if len(w) == 1 {
			return s.stage(func(c *config.Config) error { v := !neg; port(c).STP.Enabled = &v; return nil })
		}
		switch w[1] {
		case "portfast", "edge":
			return s.stage(func(c *config.Config) error { port(c).STP.Edge = !neg; return nil })
		case "cost":
			n := 0
			if !neg {
				var err error
				if n, err = atoiArg(w, 2); err != nil {
					return err
				}
			}
			return s.stage(func(c *config.Config) error { port(c).STP.PathCost = n; return nil })
		case "port-priority":
			n := 128
			if !neg {
				var err error
				if n, err = atoiArg(w, 2); err != nil {
					return err
				}
			}
			return s.stage(func(c *config.Config) error { port(c).STP.Priority = n; return nil })
		}
	}
	return errUnknown
}
