// Package cli implements bsdswitchctl, a Cisco-flavoured CLI on top of the
// REST API.
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/Lukas0923xcv/freebsdswitch/internal/api"
	"github.com/Lukas0923xcv/freebsdswitch/internal/client"
	"github.com/Lukas0923xcv/freebsdswitch/internal/commit"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/state"
	"github.com/Lukas0923xcv/freebsdswitch/internal/stats"
)

const usage = `usage: bsdswitchctl [-H https://switch] [-t token | -u user] [-k] <command>

show commands:
  show interfaces [status|counters|<port>]   port status / counters
  show vlan [brief]                          VLANs and member ports
  show mac address-table [vlan N] [interface P] [address M]
  show spanning-tree                         RSTP root, roles and states
  show running-config | startup-config | candidate-config
  show configuration diff                    staged changes + commands to run
  show commit                                pending commit-confirm status
  show history                               last 10 committed configurations
  show system | version | audit | logging | lldp neighbors | capabilities

configuration:
  configure [-f file] [-c "cmd" ...]         enter configuration mode (staged)
  commit confirm [SECONDS] [comment TEXT]    apply staged changes (auto-rollback)
  confirm                                    keep the last commit
  rollback                                   undo the pending commit now
  rollback N                                 stage history version N
  discard                                    drop staged changes
  write memory | copy running-config startup-config
  clear counters [PORT] | clear mac address-table
  reload | poweroff

environment: BSDSWITCH_URL, BSDSWITCH_TOKEN, BSDSWITCH_INSECURE=1
`

type ctl struct {
	c   *client.Client
	out io.Writer
	in  *bufio.Reader
	tty bool
}

// Main is the entry point.
func Main(args []string) int {
	fl := flag.NewFlagSet("bsdswitchctl", flag.ContinueOnError)
	fl.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	url := fl.String("H", os.Getenv("BSDSWITCH_URL"), "switch URL (default: local socket)")
	token := fl.String("t", os.Getenv("BSDSWITCH_TOKEN"), "API token")
	user := fl.String("u", "", "user name (asks for the password)")
	insecure := fl.Bool("k", os.Getenv("BSDSWITCH_INSECURE") == "1", "do not verify the TLS certificate")
	sock := fl.String("socket", "/var/run/bsdswitch/api.sock", "local API socket")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	t := &ctl{out: os.Stdout, in: bufio.NewReader(os.Stdin), tty: term.IsTerminal(int(os.Stdin.Fd()))}
	if *url != "" {
		t.c = client.Remote(*url, *token, *insecure)
		if *user != "" {
			fmt.Fprint(os.Stderr, "Password: ")
			pw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return 1
			}
			if err := t.c.Login(*user, string(pw)); err != nil {
				fmt.Fprintln(os.Stderr, "login failed:", err)
				return 1
			}
		}
	} else {
		t.c = client.Unix(*sock)
	}
	words := fl.Args()
	if len(words) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	if err := t.run(words); err != nil {
		fmt.Fprintln(os.Stderr, "%", err)
		return 1
	}
	return 0
}

// Run executes one command line (used by tests).
func Run(c *client.Client, out io.Writer, in io.Reader, words []string) error {
	t := &ctl{c: c, out: out, in: bufio.NewReader(in)}
	return t.run(words)
}

func match(w, full string, min int) bool {
	return len(w) >= min && strings.HasPrefix(full, w)
}

func (t *ctl) run(w []string) error {
	switch {
	case match(w[0], "show", 2):
		return t.show(w[1:])
	case match(w[0], "configure", 4):
		return t.configure(w[1:])
	case match(w[0], "commit", 3):
		return t.commit(w[1:])
	case w[0] == "confirm":
		if err := t.c.Post("/api/v1/commit/confirm", nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(t.out, "Commit confirmed. Use 'write memory' to make it the startup configuration.")
		return nil
	case match(w[0], "rollback", 4):
		if len(w) == 1 {
			if err := t.c.Post("/api/v1/commit/cancel", nil, nil); err != nil {
				return err
			}
			fmt.Fprintln(t.out, "Pending commit rolled back.")
			return nil
		}
		id, err := t.historyID(w[1])
		if err != nil {
			return err
		}
		if err := t.c.Post(fmt.Sprintf("/api/v1/config/rollback/%d", id), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(t.out, "Version %d loaded into the candidate configuration. Review with 'show configuration diff', apply with 'commit confirm'.\n", id)
		return nil
	case w[0] == "discard":
		return t.c.Do("DELETE", "/api/v1/config/candidate", nil, nil)
	case match(w[0], "write", 2) || (w[0] == "copy" && len(w) >= 3 && match(w[1], "running-config", 3) && match(w[2], "startup-config", 3)):
		if err := t.c.Post("/api/v1/config/save", nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(t.out, "[OK] running configuration saved to startup configuration")
		return nil
	case w[0] == "clear":
		return t.clear(w[1:])
	case w[0] == "reload":
		return t.power("reboot")
	case w[0] == "poweroff":
		return t.power("shutdown")
	case w[0] == "help" || w[0] == "?":
		fmt.Fprint(t.out, usage)
		return nil
	}
	return fmt.Errorf("unknown command %q (try 'help')", strings.Join(w, " "))
}

func (t *ctl) historyID(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid version %q", s)
	}
	var h []commit.Version
	if err := t.c.Get("/api/v1/config/history", &h); err != nil {
		return 0, err
	}
	// accept either the index (0 = current, 1 = previous ...) or the id
	for _, v := range h {
		if v.Index == n && n < 10 {
			return v.ID, nil
		}
	}
	for _, v := range h {
		if v.ID == n {
			return v.ID, nil
		}
	}
	return 0, fmt.Errorf("no such version %d (see 'show history')", n)
}

func (t *ctl) power(action string) error {
	if !t.ask(fmt.Sprintf("Proceed with %s? [confirm] ", action)) {
		return nil
	}
	return t.c.Post("/api/v1/system/"+action, map[string]bool{"confirm": true}, nil)
}

func (t *ctl) ask(q string) bool {
	fmt.Fprint(t.out, q)
	l, _ := t.in.ReadString('\n')
	l = strings.TrimSpace(strings.ToLower(l))
	return l == "" || l == "y" || l == "yes"
}

func (t *ctl) clear(w []string) error {
	if len(w) >= 1 && match(w[0], "counters", 4) {
		path := "/api/v1/ports/reset-counters"
		if len(w) > 1 {
			path = "/api/v1/ports/" + w[1] + "/reset-counters"
		}
		return t.c.Post(path, nil, nil)
	}
	if len(w) >= 1 && match(w[0], "mac", 3) {
		return t.c.Post("/api/v1/mac-table/flush", map[string]bool{}, nil)
	}
	return errors.New("usage: clear counters [PORT] | clear mac address-table")
}

func (t *ctl) tab() *tabwriter.Writer { return tabwriter.NewWriter(t.out, 0, 2, 2, ' ', 0) }

func human(bps float64) string {
	switch {
	case bps >= 1e9:
		return fmt.Sprintf("%.2fG", bps/1e9)
	case bps >= 1e6:
		return fmt.Sprintf("%.1fM", bps/1e6)
	case bps >= 1e3:
		return fmt.Sprintf("%.0fk", bps/1e3)
	}
	return fmt.Sprintf("%.0f", bps)
}

func vlanCol(p api.PortView) string {
	if p.Mode == config.ModeTrunk {
		n := "-"
		if p.Native != 0 {
			n = strconv.Itoa(p.Native)
		}
		return "trunk n:" + n
	}
	return strconv.Itoa(p.PVID)
}

func (t *ctl) show(w []string) error {
	if len(w) == 0 {
		return errors.New("show what? (try 'help')")
	}
	switch {
	case match(w[0], "interfaces", 3):
		var ports []api.PortView
		if err := t.c.Get("/api/v1/ports", &ports); err != nil {
			return err
		}
		if len(w) > 1 && match(w[1], "counters", 4) {
			tw := t.tab()
			fmt.Fprintln(tw, "Port\tRxPkts\tRxBytes\tRxErr\tRxDrop\tTxPkts\tTxBytes\tTxErr\tTxDrop")
			for _, p := range ports {
				c := p.Counters
				fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", p.Name, c.RxPackets, c.RxBytes, c.RxErrors, c.RxDrops, c.TxPackets, c.TxBytes, c.TxErrors, c.TxDrops)
			}
			return tw.Flush()
		}
		if len(w) > 1 && !match(w[1], "status", 2) {
			for _, p := range ports {
				if p.Name == w[1] {
					b, _ := json.MarshalIndent(p, "", "  ")
					fmt.Fprintln(t.out, string(b))
					return nil
				}
			}
			return fmt.Errorf("no such port %s", w[1])
		}
		tw := t.tab()
		fmt.Fprintln(tw, "Port\tName\tStatus\tVlan\tTagged\tSpeed\tDuplex\tSTP\tRx bps\tTx bps\tErrors")
		for _, p := range ports {
			st := "notconnect"
			switch {
			case !p.Exists:
				st = "missing"
			case p.Shutdown || !p.AdminUp:
				st = "disabled"
			case p.LinkUp:
				st = "connected"
			}
			stp := p.STP.State
			if stp == "" {
				stp = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", p.Name, trunc(p.Description, 18), st, vlanCol(p),
				dash(p.Tagged.String()), p.Speed, p.Duplex, stp, human(p.Rate.RxBps), human(p.Rate.TxBps),
				p.Counters.RxErrors+p.Counters.TxErrors)
		}
		return tw.Flush()
	case match(w[0], "vlan", 1):
		var vl []api.VLANView
		if err := t.c.Get("/api/v1/vlans", &vl); err != nil {
			return err
		}
		tw := t.tab()
		fmt.Fprintln(tw, "VLAN\tName\tUntagged ports\tTagged ports")
		for _, v := range vl {
			name := v.Name
			if v.Mgmt {
				name += " (mgmt)"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", v.ID, name, dash(strings.Join(v.Untagged, ", ")), dash(strings.Join(v.Tagged, ", ")))
		}
		return tw.Flush()
	case match(w[0], "mac", 1):
		q := []string{}
		for i := 1; i+1 < len(w); i++ {
			switch {
			case match(w[i], "vlan", 1):
				q = append(q, "vlan="+w[i+1])
			case match(w[i], "interface", 3):
				q = append(q, "port="+w[i+1])
			case match(w[i], "address", 3):
				q = append(q, "mac="+w[i+1])
			}
		}
		var resp struct {
			Entries []state.MACEntry `json:"entries"`
			Aging   int              `json:"aging"`
			Total   int              `json:"total"`
		}
		if err := t.c.Get("/api/v1/mac-table?"+strings.Join(q, "&"), &resp); err != nil {
			return err
		}
		tw := t.tab()
		fmt.Fprintln(tw, "Vlan\tMac Address\tType\tPort\tAge")
		for _, e := range resp.Entries {
			typ := "DYNAMIC"
			age := strconv.Itoa(resp.Aging - e.Expire)
			if e.Static {
				typ, age = "STATIC", "-"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", e.VLAN, e.MAC, typ, e.Port, age)
		}
		tw.Flush()
		fmt.Fprintf(t.out, "Total MAC addresses: %d (aging time %ds)\n", resp.Total, resp.Aging)
		return nil
	case match(w[0], "spanning-tree", 2):
		var s struct {
			Config config.STP       `json:"config"`
			Bridge map[string]any   `json:"bridge"`
			Ports  []map[string]any `json:"ports"`
		}
		if err := t.c.Get("/api/v1/stp", &s); err != nil {
			return err
		}
		if !s.Config.Enabled {
			fmt.Fprintln(t.out, "Spanning tree is disabled.")
		}
		if b := s.Bridge; b != nil {
			fmt.Fprintf(t.out, "Spanning tree mode %v\n  Root ID    Priority %v  Address %v  Cost %v  Port %v\n  Bridge ID  Priority %v  Address %v\n  Hello %vs  Max Age %vs  Forward Delay %vs\n",
				b["proto"], b["root_priority"], b["root_id"], b["root_cost"], b["root_port"], b["priority"], b["id"], b["hello"], b["max_age"], b["fwd_delay"])
			if b["is_root"] == true {
				fmt.Fprintln(t.out, "  This bridge is the root")
			}
		}
		tw := t.tab()
		fmt.Fprintln(tw, "\nInterface\tRole\tState\tCost\tPrio\tEdge\tP2P")
		for _, p := range s.Ports {
			if p["stp"] != true {
				fmt.Fprintf(tw, "%v\t-\tSTP off\t-\t-\t-\t-\n", p["name"])
				continue
			}
			fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\t%v\t%v\n", p["name"], p["role"], p["state"], p["path_cost"], p["priority"], yn(p["oper_edge"]), yn(p["ptp"]))
		}
		return tw.Flush()
	case match(w[0], "running-config", 3), match(w[0], "startup-config", 3), match(w[0], "candidate-config", 3):
		which := map[byte]string{'r': "running", 's': "startup", 'c': "candidate"}[w[0][0]]
		var b []byte
		if err := t.c.Get("/api/v1/config/"+which+"?format=yaml", &b); err != nil {
			return err
		}
		_, err := t.out.Write(b)
		return err
	case match(w[0], "configuration", 4):
		return t.showDiff()
	case match(w[0], "commit", 3):
		var st api.ConfigStatus
		if err := t.c.Get("/api/v1/commit", &st); err != nil {
			return err
		}
		t.printStatus(st)
		return nil
	case match(w[0], "history", 4), match(w[0], "rollback", 4):
		var h []commit.Version
		if err := t.c.Get("/api/v1/config/history", &h); err != nil {
			return err
		}
		tw := t.tab()
		fmt.Fprintln(tw, "#\tVersion\tTime\tUser\tComment")
		for _, v := range h {
			cur := ""
			if v.Index == 0 {
				cur = " (current)"
			}
			fmt.Fprintf(tw, "%d\t%d%s\t%s\t%s\t%s\n", v.Index, v.ID, cur, v.Time.Local().Format("2006-01-02 15:04:05"), v.User, v.Comment)
		}
		return tw.Flush()
	case match(w[0], "system", 2), match(w[0], "version", 1):
		var s api.SystemView
		if err := t.c.Get("/api/v1/system", &s); err != nil {
			return err
		}
		fmt.Fprintf(t.out, "bsdswitch %s on FreeBSD %s (%s)\nHostname: %s   Uptime: %s\nCPU: %s x%d (%.0f%%)   Memory: %d/%d MB\nManagement: %s %s\nWeb UI: %s\nPorts up: %d/%d   VLANs: %d\n",
			s.Version, s.OSRelease, s.Machine, s.Hostname, (time.Duration(s.UptimeSec) * time.Second).String(), s.CPUModel, s.NCPU, s.CPUPercent,
			s.MemUsed>>20, s.MemTotal>>20, s.Management.Interface, strings.Join(s.Management.Addresses, " "),
			strings.Join(s.Management.URLs, " "), s.PortsUp, s.PortsTotal, s.VLANCount)
		t.printStatus(s.Config)
		return nil
	case match(w[0], "audit", 2):
		var a []commit.AuditEntry
		if err := t.c.Get("/api/v1/audit?n=50", &a); err != nil {
			return err
		}
		tw := t.tab()
		for _, e := range a {
			ok := "ok"
			if !e.OK {
				ok = "FAILED"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Time.Local().Format("2006-01-02 15:04:05"), e.User, e.Action, ok, e.Detail)
		}
		return tw.Flush()
	case match(w[0], "logging", 3):
		var l struct {
			Daemon []struct {
				Time time.Time `json:"time"`
				Text string    `json:"text"`
			} `json:"daemon"`
		}
		if err := t.c.Get("/api/v1/logs?n=100", &l); err != nil {
			return err
		}
		for _, x := range l.Daemon {
			fmt.Fprintf(t.out, "%s %s\n", x.Time.Local().Format("Jan 02 15:04:05"), x.Text)
		}
		return nil
	case match(w[0], "lldp", 2):
		var n []map[string]any
		if err := t.c.Get("/api/v1/lldp/neighbors", &n); err != nil {
			return err
		}
		tw := t.tab()
		fmt.Fprintln(tw, "Local port\tSystem name\tPort ID\tChassis\tCapabilities")
		for _, x := range n {
			fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\n", x["local_port"], x["system_name"], x["port_id"], x["chassis_id"], x["capabilities"])
		}
		return tw.Flush()
	case match(w[0], "capabilities", 3):
		var c []api.Capability
		if err := t.c.Get("/api/v1/capabilities", &c); err != nil {
			return err
		}
		tw := t.tab()
		for _, x := range c {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", x.Feature, x.Status, x.Notes)
		}
		return tw.Flush()
	}
	return fmt.Errorf("unknown show command %q", strings.Join(w, " "))
}

func (t *ctl) printStatus(st api.ConfigStatus) {
	if st.Pending != nil {
		left := time.Until(time.UnixMilli(st.Pending.DeadlineMs)).Round(time.Second)
		fmt.Fprintf(t.out, "Commit #%d by %s is waiting for confirmation: automatic rollback in %v ('confirm' to keep, 'rollback' to undo).\n", st.Pending.ID, st.Pending.User, left)
	}
	if st.Staged {
		fmt.Fprintln(t.out, "There are staged (uncommitted) changes ('show configuration diff').")
	}
	if st.StartupDiffers {
		fmt.Fprintln(t.out, "Running configuration differs from startup configuration ('write memory' to save).")
	}
	if st.BootMode == "safe" {
		fmt.Fprintln(t.out, "WARNING: booted in SAFE MODE because the startup configuration could not be applied.")
	}
	if st.Pending == nil && !st.Staged && !st.StartupDiffers {
		fmt.Fprintln(t.out, "Configuration is saved; nothing pending.")
	}
}

func (t *ctl) showDiff() error {
	var pv commit.Preview
	if err := t.c.Get("/api/v1/config/diff", &pv); err != nil {
		return err
	}
	if !pv.Changed {
		fmt.Fprintln(t.out, "No staged changes.")
		return nil
	}
	fmt.Fprint(t.out, pv.Diff)
	fmt.Fprintln(t.out, "\nCommands that will run on commit:")
	for _, c := range pv.Commands {
		fmt.Fprintln(t.out, "  "+c)
	}
	for _, w := range pv.Warnings {
		fmt.Fprintln(t.out, "warning: "+w)
	}
	for _, m := range pv.MgmtImpact {
		fmt.Fprintln(t.out, "MANAGEMENT ACCESS: "+m)
	}
	return nil
}

func (t *ctl) commit(w []string) error {
	timeout := 60
	comment := ""
	for i := 0; i < len(w); i++ {
		switch {
		case match(w[i], "confirm", 4) || w[i] == "confirmed":
			if i+1 < len(w) {
				if n, err := strconv.Atoi(w[i+1]); err == nil {
					timeout = n
					i++
				}
			}
		case w[i] == "comment":
			comment = strings.Join(w[i+1:], " ")
			i = len(w)
		}
	}
	var p api.PendingView
	if err := t.c.Post("/api/v1/commit", map[string]any{"timeout": timeout, "comment": comment}, &p); err != nil {
		return err
	}
	fmt.Fprintf(t.out, "Commit #%d applied. It will be rolled back automatically in %ds unless you run 'confirm'.\n", p.ID, timeout)
	return nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yn(v any) string {
	if v == true {
		return "yes"
	}
	return "no"
}

var _ = sort.Strings
var _ = stats.Window
