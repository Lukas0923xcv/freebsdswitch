// Package console implements bsdswitch-console: the numbered menu shown
// on the VGA/serial console (like pfSense) and the first-boot wizard.
package console

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/api"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/freebsd"
	"github.com/Lukas0923xcv/freebsdswitch/internal/client"
	"github.com/Lukas0923xcv/freebsdswitch/internal/commit"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/state"
	"github.com/Lukas0923xcv/freebsdswitch/internal/version"
)

// Console holds the menu state. Inputs and outputs are injectable for
// tests.
type Console struct {
	In        *bufio.Reader
	Out       io.Writer
	API       *client.Client
	Local     backend.Backend // for offline operation (daemon down)
	ConfigDir string
	StateDir  string
	Shell     string
	// Exec runs a system command (service restart, reboot); replaceable in tests.
	Exec  func(name string, args ...string) error
	Sleep func(time.Duration)
}

// Main is the entry point.
func Main(args []string) int {
	fl := flag.NewFlagSet("bsdswitch-console", flag.ContinueOnError)
	sock := fl.String("socket", "/var/run/bsdswitch/api.sock", "daemon API socket")
	cfgDir := fl.String("config-dir", "/usr/local/etc/bsdswitch", "configuration directory")
	stateDir := fl.String("state", "/var/db/bsdswitch", "state directory")
	wizard := fl.Bool("wizard", false, "run the first-boot wizard even if a configuration exists")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	shell := os.Getenv("SHELL")
	if shell == "" || strings.HasSuffix(shell, "bsdswitch-console") {
		shell = "/bin/sh"
	}
	c := &Console{In: bufio.NewReader(os.Stdin), Out: os.Stdout, API: client.Unix(*sock), Local: freebsd.New(),
		ConfigDir: *cfgDir, StateDir: *stateDir, Shell: shell, Exec: runCmd, Sleep: time.Sleep}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "bsdswitch-console must be run as root")
		return 1
	}
	if *wizard || c.needsWizard() {
		if err := c.Wizard(); err != nil {
			fmt.Fprintln(c.Out, "wizard:", err)
		}
	}
	c.Menu()
	return 0
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func (c *Console) startupPath() string { return filepath.Join(c.ConfigDir, "config.yaml") }

func (c *Console) needsWizard() bool {
	if _, err := os.Stat(c.startupPath()); os.IsNotExist(err) {
		return true
	}
	_, err := os.Stat(filepath.Join(c.StateDir, "wizard-pending"))
	return err == nil
}

func (c *Console) daemonUp() bool {
	var v map[string]string
	return c.API.Get("/api/v1/version", &v) == nil
}

func (c *Console) prompt(q, def string) string {
	if def != "" {
		fmt.Fprintf(c.Out, "%s [%s]: ", q, def)
	} else {
		fmt.Fprintf(c.Out, "%s: ", q)
	}
	l, err := c.In.ReadString('\n')
	if err != nil && l == "" {
		return def
	}
	l = strings.TrimSpace(l)
	if l == "" {
		return def
	}
	return l
}

func (c *Console) yes(q string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	a := strings.ToLower(c.prompt(q+" ("+d+")", ""))
	if a == "" {
		return def
	}
	return a == "y" || a == "yes"
}

func (c *Console) pause() { c.prompt("\nPress ENTER to continue", "") }

// ---- data helpers (online via API, offline via local backend + files) ------------

func (c *Console) snapshot() (*state.Snapshot, error) {
	return c.Local.Snapshot(context.Background())
}

func (c *Console) running() (*config.Config, error) {
	if c.daemonUp() {
		var cfg config.Config
		if err := c.API.Get("/api/v1/config/running", &cfg); err != nil {
			return nil, err
		}
		cfg.ApplyDefaults()
		return &cfg, nil
	}
	return config.Load(c.startupPath())
}

// apply applies a configuration immediately (and saves it as startup).
// With the daemon down, the startup file is written and the daemon
// restarted.
func (c *Console) apply(cfg *config.Config) error {
	cfg.ApplyDefaults()
	if err := config.Validate(cfg, nil); err != nil {
		return err
	}
	if c.daemonUp() {
		return c.API.Post("/api/v1/console/apply?save=1", cfg, nil)
	}
	fmt.Fprintln(c.Out, "bsdswitchd is not running: writing the startup configuration and starting it.")
	if err := config.Save(c.startupPath(), cfg); err != nil {
		return err
	}
	return c.restartDaemon()
}

func (c *Console) restartDaemon() error {
	if err := c.Exec("/usr/sbin/service", "bsdswitchd", "restart"); err != nil {
		return err
	}
	for i := 0; i < 30 && !c.daemonUp(); i++ {
		c.Sleep(time.Second)
	}
	if !c.daemonUp() {
		return errors.New("bsdswitchd did not come up; see /var/log/messages")
	}
	return nil
}

// ---- menu ---------------------------------------------------------------------

func (c *Console) header() {
	fmt.Fprint(c.Out, "\033[H\033[2J")
	host, _ := os.Hostname()
	fmt.Fprintf(c.Out, "bsdswitch %s - %s\n", version.Version, host)
	fmt.Fprintln(c.Out, strings.Repeat("=", 72))
	cfg, err := c.running()
	snap, serr := c.snapshot()
	if err == nil && serr == nil {
		mg := snap.Get(cfg.Management.Interface)
		addrs := []string{}
		link := "no link"
		if mg != nil {
			addrs = append(addrs, mg.IPv4...)
			for _, a := range mg.IPv6 {
				if p, err := netip.ParsePrefix(a); err == nil && !p.Addr().IsLinkLocalUnicast() {
					addrs = append(addrs, a)
				}
			}
			if mg.LinkUp() {
				link = "link up"
			}
		}
		fmt.Fprintf(c.Out, " Management: %-8s (%s, %s) %s\n", cfg.Management.Interface, cfg.Management.IPv4.Mode, link, strings.Join(addrs, " "))
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a); err == nil {
				h := p.Addr().String()
				if p.Addr().Is6() {
					h = "[" + h + "]"
				}
				fmt.Fprintf(c.Out, " Web UI:     https://%s/\n", h)
			}
		}
		up := 0
		for _, p := range cfg.Ports {
			if i := snap.Get(p.Name); i != nil && i.LinkUp() && !p.Shutdown {
				up++
			}
		}
		fmt.Fprintf(c.Out, " Switch ports: %d (%d up)   VLANs: %d   Bridge: %s\n", len(cfg.Ports), up, len(cfg.VLANs), cfg.Bridge.Name)
	}
	if !c.daemonUp() {
		fmt.Fprintln(c.Out, " !! bsdswitchd is NOT running (option r restarts it)")
	} else {
		var st api.ConfigStatus
		if c.API.Get("/api/v1/commit", &st) == nil {
			if st.BootMode == "safe" {
				fmt.Fprintln(c.Out, " !! SAFE MODE: the startup configuration failed to apply (see logs)")
			}
			if st.Pending != nil {
				fmt.Fprintf(c.Out, " !! commit #%d awaiting confirmation (rolls back automatically)\n", st.Pending.ID)
			}
		}
		var pw map[string]string
		if c.API.Get("/api/v1/console/initial-password", &pw) == nil && strings.TrimSpace(pw["password"]) != "" {
			fmt.Fprintf(c.Out, " Initial web UI login: admin / %s  (must be changed at first login)\n", strings.TrimSpace(pw["password"]))
		}
	}
	fmt.Fprintln(c.Out, strings.Repeat("=", 72))
}

// Menu runs the main loop until logout.
func (c *Console) Menu() {
	for {
		c.header()
		fmt.Fprint(c.Out, ` 1) Assign management interface      6) Roll back to previous config
 2) Set management IP                 7) Shell
 3) Reset web UI admin password       8) Reboot
 4) Reset to factory defaults         9) Shut down
 5) Show status                       r) Restart bsdswitchd
                                      0) Logout
`)
		choice := c.prompt("\nEnter an option", "")
		var err error
		switch choice {
		case "1":
			err = c.AssignMgmt()
		case "2":
			err = c.SetMgmtIP()
		case "3":
			err = c.ResetPassword()
		case "4":
			err = c.FactoryReset()
		case "5":
			err = c.Status()
		case "6":
			err = c.Rollback()
		case "7":
			fmt.Fprintln(c.Out, "Type 'exit' to return to the menu.")
			c.Exec(c.Shell)
			continue
		case "8":
			if c.yes("Reboot now?", false) {
				c.Exec("/sbin/shutdown", "-r", "now")
				return
			}
			continue
		case "9":
			if c.yes("Shut down now?", false) {
				c.Exec("/sbin/shutdown", "-p", "now")
				return
			}
			continue
		case "r", "R":
			err = c.restartDaemon()
		case "0", "q", "exit", "logout":
			return
		case "":
			continue
		default:
			fmt.Fprintln(c.Out, "Invalid option.")
		}
		if err != nil {
			fmt.Fprintln(c.Out, "\nError:", err)
		}
		c.pause()
	}
}

func (c *Console) listInterfaces(snap *state.Snapshot, cfg *config.Config) []state.Interface {
	eth := snap.Ethernet()
	tw := tabwriter.NewWriter(c.Out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  #\tInterface\tDriver\tMAC\tLink\tRole")
	for i, e := range eth {
		role := "unused"
		if cfg != nil {
			if cfg.Management.Interface == e.Name {
				role = "management"
			} else if cfg.Port(e.Name) != nil {
				role = "switch port"
			}
		}
		link := "down"
		if e.LinkUp() {
			link = "up"
		}
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\t%s\n", i+1, e.Name, e.Driver, e.MAC, link, role)
	}
	tw.Flush()
	return eth
}

func (c *Console) pickInterface(eth []state.Interface, def string) (string, error) {
	a := c.prompt("Management interface (number or name)", def)
	if n, err := strconv.Atoi(a); err == nil && n >= 1 && n <= len(eth) {
		return eth[n-1].Name, nil
	}
	for _, e := range eth {
		if e.Name == a {
			return a, nil
		}
	}
	return "", fmt.Errorf("unknown interface %q", a)
}

// AssignMgmt is menu option 1.
func (c *Console) AssignMgmt() error {
	snap, err := c.snapshot()
	if err != nil {
		return err
	}
	cfg, err := c.running()
	if err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "\nDetected Ethernet interfaces:")
	eth := c.listInterfaces(snap, cfg)
	mg, err := c.pickInterface(eth, cfg.Management.Interface)
	if err != nil {
		return err
	}
	old := cfg.Management.Interface
	if mg == old {
		fmt.Fprintln(c.Out, "No change.")
		return nil
	}
	var ports []config.Port
	for _, p := range cfg.Ports {
		if p.Name != mg {
			ports = append(ports, p)
		}
	}
	if old != "" && c.yes(fmt.Sprintf("Make the old management interface %s a switch port (VLAN 1)?", old), true) {
		ports = append(ports, config.Port{Name: old, Mode: config.ModeAccess, PVID: 1})
	}
	cfg.Ports = ports
	cfg.Management.Interface = mg
	cfg.Management.InBand = config.InBand{}
	if err := c.ipDialog(cfg); err != nil {
		return err
	}
	if err := c.apply(cfg); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "\nManagement moved to %s.\n", mg)
	return nil
}

// SetMgmtIP is menu option 2.
func (c *Console) SetMgmtIP() error {
	cfg, err := c.running()
	if err != nil {
		return err
	}
	if err := c.ipDialog(cfg); err != nil {
		return err
	}
	if err := c.apply(cfg); err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "\nManagement IP configuration applied and saved.")
	return nil
}

func (c *Console) ipDialog(cfg *config.Config) error {
	m := &cfg.Management
	fmt.Fprintf(c.Out, "\nIPv4 for %s\n", m.Interface)
	if c.yes("Configure IPv4 via DHCP?", m.IPv4.Mode != config.IPModeStatic) {
		m.IPv4 = config.IPConfig{Mode: config.IPModeDHCP}
	} else {
		for {
			a := c.prompt("IPv4 address with prefix (e.g. 192.168.1.10/24)", m.IPv4.Address)
			if _, err := netip.ParsePrefix(a); err == nil {
				m.IPv4.Address = a
				break
			}
			fmt.Fprintln(c.Out, "Invalid address; use CIDR notation.")
		}
		m.IPv4.Mode = config.IPModeStatic
		m.IPv4.Gateway = c.prompt("IPv4 default gateway (empty for none)", m.IPv4.Gateway)
		if dns := c.prompt("DNS servers (space separated, empty to keep)", strings.Join(cfg.System.DNS, " ")); dns != "" {
			cfg.System.DNS = strings.Fields(dns)
		}
	}
	switch strings.ToLower(c.prompt("IPv6: none, slaac or static", m.IPv6.Mode)) {
	case "static":
		m.IPv6.Mode = config.IPModeStatic
		m.IPv6.Address = c.prompt("IPv6 address with prefix", m.IPv6.Address)
		m.IPv6.Gateway = c.prompt("IPv6 gateway (empty for none)", m.IPv6.Gateway)
	case "slaac":
		m.IPv6 = config.IPConfig{Mode: config.IPModeSLAAC}
	default:
		m.IPv6 = config.IPConfig{Mode: config.IPModeNone}
	}
	cfg.ApplyDefaults()
	return config.Validate(cfg, nil)
}

// ResetPassword is menu option 3.
func (c *Console) ResetPassword() error {
	user := c.prompt("User to reset", "admin")
	if !c.daemonUp() {
		return errors.New("bsdswitchd is not running (option r)")
	}
	var resp map[string]string
	if err := c.API.Post("/api/v1/console/reset-password", map[string]string{"user": user}, &resp); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "\nNew one-time password for %s: %s\nIt must be changed at the next login.\n", resp["user"], resp["password"])
	return nil
}

// FactoryReset is menu option 4.
func (c *Console) FactoryReset() error {
	fmt.Fprintln(c.Out, "\nThis resets the configuration, users and API tokens to factory defaults:")
	fmt.Fprintln(c.Out, "management via DHCP on the first interface with link, all other ports in VLAN 1.")
	if strings.ToLower(c.prompt("Type 'yes' to continue", "")) != "yes" {
		return nil
	}
	if !c.daemonUp() {
		os.Remove(c.startupPath())
		os.Remove(filepath.Join(c.StateDir, "running.yaml"))
		os.Remove(filepath.Join(c.ConfigDir, "users.yaml"))
		return c.restartDaemon()
	}
	var resp map[string]string
	if err := c.API.Post("/api/v1/console/factory-reset", nil, &resp); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "\nFactory defaults applied. Management: %s (DHCP)\nWeb UI login: admin / %s (must be changed)\n", resp["management"], resp["password"])
	return nil
}

// Status is menu option 5.
func (c *Console) Status() error {
	fmt.Fprintln(c.Out)
	if c.daemonUp() {
		var ports []api.PortView
		if err := c.API.Get("/api/v1/ports", &ports); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(c.Out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "Port\tDescription\tLink\tSpeed\tMode\tVLAN\tTagged\tSTP")
		for _, p := range ports {
			link := "down"
			if p.Shutdown {
				link = "disabled"
			} else if p.LinkUp {
				link = "up"
			}
			vl := strconv.Itoa(p.PVID)
			if p.Mode == config.ModeTrunk {
				vl = "native " + strconv.Itoa(p.Native)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", p.Name, p.Description, link, p.Speed, p.Mode, vl, p.Tagged.String(), p.STP.State)
		}
		return tw.Flush()
	}
	snap, err := c.snapshot()
	if err != nil {
		return err
	}
	cfg, _ := config.Load(c.startupPath())
	c.listInterfaces(snap, cfg)
	return nil
}

// Rollback is menu option 6.
func (c *Console) Rollback() error {
	if !c.daemonUp() {
		return errors.New("bsdswitchd is not running (option r)")
	}
	var h []commit.Version
	if err := c.API.Get("/api/v1/config/history", &h); err != nil {
		return err
	}
	if len(h) < 2 {
		return errors.New("no previous configuration stored")
	}
	tw := tabwriter.NewWriter(c.Out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "\n #\tTime\tUser\tComment")
	for _, v := range h {
		cur := ""
		if v.Index == 0 {
			cur = " (current)"
		}
		fmt.Fprintf(tw, " %d\t%s\t%s\t%s%s\n", v.Index, v.Time.Local().Format("2006-01-02 15:04"), v.User, v.Comment, cur)
	}
	tw.Flush()
	n, err := strconv.Atoi(c.prompt("Roll back to #", "1"))
	if err != nil || n < 1 || n >= len(h) {
		return errors.New("invalid selection")
	}
	if !c.yes(fmt.Sprintf("Apply version from %s now (no confirmation timer)?", h[n].Time.Local().Format("2006-01-02 15:04")), false) {
		return nil
	}
	if err := c.API.Post(fmt.Sprintf("/api/v1/console/apply-version/%d", h[n].ID), nil, nil); err != nil {
		return err
	}
	if c.yes("Save as startup configuration?", true) {
		return c.API.Post("/api/v1/config/save", nil, nil)
	}
	return nil
}

// Wizard is the first-boot setup.
func (c *Console) Wizard() error {
	fmt.Fprint(c.Out, "\033[H\033[2J")
	fmt.Fprintf(c.Out, "bsdswitch %s - initial setup\n%s\n", version.Version, strings.Repeat("=", 72))
	snap, err := c.snapshot()
	if err != nil {
		return err
	}
	eth := snap.Ethernet()
	if len(eth) == 0 {
		return errors.New("no Ethernet interfaces detected")
	}
	fmt.Fprintln(c.Out, "Detected Ethernet interfaces:")
	c.listInterfaces(snap, nil)
	def := eth[0].Name
	for _, e := range eth {
		if e.LinkUp() {
			def = e.Name
			break
		}
	}
	fmt.Fprintln(c.Out, "\nThe management interface stays outside the switch (bridge) so you cannot")
	fmt.Fprintln(c.Out, "lock yourself out by changing VLANs. All other interfaces become switch ports in VLAN 1.")
	mg, err := c.pickInterface(eth, def)
	if err != nil {
		return err
	}
	var ports []string
	for _, e := range eth {
		if e.Name != mg {
			ports = append(ports, e.Name)
		}
	}
	cfg := config.Default(mg, ports)
	cfg.System.Hostname = c.prompt("Hostname", cfg.System.Hostname)
	if err := c.ipDialog(cfg); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "\nSummary:\n  management: %s (%s %s)\n  switch ports (VLAN 1): %s\n", mg, cfg.Management.IPv4.Mode, cfg.Management.IPv4.Address, strings.Join(ports, " "))
	if !c.yes("Apply this configuration?", true) {
		return errors.New("aborted")
	}
	if c.daemonUp() {
		if err := c.API.Post("/api/v1/console/apply?save=1", cfg, nil); err != nil {
			return err
		}
	} else {
		if err := config.Save(c.startupPath(), cfg); err != nil {
			return err
		}
		if err := c.restartDaemon(); err != nil {
			return err
		}
	}
	os.Remove(filepath.Join(c.StateDir, "wizard-pending"))
	// wait for DHCP
	for i := 0; i < 15; i++ {
		s, err := c.snapshot()
		if err == nil {
			if i := s.Get(mg); i != nil && len(i.IPv4) > 0 {
				break
			}
		}
		c.Sleep(time.Second)
	}
	s, _ := c.snapshot()
	fmt.Fprintln(c.Out, "\nSetup complete.")
	if s != nil {
		if i := s.Get(mg); i != nil {
			for _, a := range i.IPv4 {
				p, _ := netip.ParsePrefix(a)
				fmt.Fprintf(c.Out, "Web UI: https://%s/\n", p.Addr())
			}
			if len(i.IPv4) == 0 {
				fmt.Fprintln(c.Out, "No IPv4 address yet (DHCP pending). The menu header shows the URL once assigned.")
			}
		}
	}
	var pw map[string]string
	if c.API.Get("/api/v1/console/initial-password", &pw) == nil && pw["password"] != "" {
		fmt.Fprintf(c.Out, "Login: admin / %s  (you must choose a new password at first login)\n", strings.TrimSpace(pw["password"]))
	}
	c.pause()
	return nil
}
