// Package backend abstracts every interaction with the operating system
// so that the planner, commit engine and API can be tested against a
// simulated kernel.
package backend

import (
	"context"
	"regexp"
	"strings"

	"github.com/Lukas0923xcv/freebsdswitch/internal/state"
)

// Op is one system command. Commands are always executed as an argv
// vector (never through a shell).
type Op struct {
	Argv    []string `json:"argv"`
	Comment string   `json:"comment,omitempty"`
	// IgnoreError marks best-effort commands (e.g. disabling an offload
	// the NIC might reject).
	IgnoreError bool `json:"ignore_error,omitempty"`
	// Path/Content turn the op into a file write (Argv is ignored).
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
}

// Exec is a shorthand constructor.
func Exec(argv ...string) Op { return Op{Argv: argv} }

// IsFile reports whether the op writes a file.
func (o Op) IsFile() bool { return o.Path != "" }

// Execute runs op against b (command or file write).
func Execute(ctx context.Context, b Backend, o Op) (string, error) {
	if o.IsFile() {
		return "", b.WriteFile(ctx, o.Path, []byte(o.Content))
	}
	out, err := b.Run(ctx, o)
	if err != nil && o.IgnoreError {
		return out, nil
	}
	return out, err
}

var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./\-]+$`)

// String renders the op as a copy-pasteable shell command.
func (o Op) String() string {
	if o.IsFile() {
		c := o.Content
		if !strings.HasSuffix(c, "\n") {
			c += "\n"
		}
		return "cat > " + ShellQuote(o.Path) + " <<'EOF'\n" + c + "EOF"
	}
	parts := make([]string, len(o.Argv))
	for i, a := range o.Argv {
		parts[i] = ShellQuote(a)
	}
	return strings.Join(parts, " ")
}

// ShellQuote quotes s for /bin/sh if needed.
func ShellQuote(s string) string {
	if s != "" && safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Counters are the per-interface statistics from if_data.
type Counters struct {
	RxPackets  uint64 `json:"rx_packets"`
	RxBytes    uint64 `json:"rx_bytes"`
	RxErrors   uint64 `json:"rx_errors"`
	RxDrops    uint64 `json:"rx_drops"`
	TxPackets  uint64 `json:"tx_packets"`
	TxBytes    uint64 `json:"tx_bytes"`
	TxErrors   uint64 `json:"tx_errors"`
	TxDrops    uint64 `json:"tx_drops"`
	RxMcast    uint64 `json:"rx_multicast"`
	TxMcast    uint64 `json:"tx_multicast"`
	Collisions uint64 `json:"collisions"`
	LinkState  int    `json:"link_state"` // 0 unknown, 1 down, 2 up
	Baudrate   uint64 `json:"baudrate"`
}

// SysInfo is general host information.
type SysInfo struct {
	Hostname   string     `json:"hostname"`
	OSRelease  string     `json:"os_release"` // e.g. 15.1-RELEASE-p6
	Machine    string     `json:"machine"`    // amd64
	UptimeSec  int64      `json:"uptime_sec"`
	CPUModel   string     `json:"cpu_model"`
	NCPU       int        `json:"ncpu"`
	CPUPercent float64    `json:"cpu_percent"`
	MemTotal   uint64     `json:"mem_total"`
	MemUsed    uint64     `json:"mem_used"`
	Load       [3]float64 `json:"load"`
}

// Routes holds the default gateways.
type Routes struct {
	Default4 string `json:"default4"`
	Default6 string `json:"default6"`
}

// Backend is the full interface used by the daemon.
type Backend interface {
	// Snapshot returns the current state of all interfaces.
	Snapshot(ctx context.Context) (*state.Snapshot, error)
	// MACTable returns the forwarding table of a bridge.
	MACTable(ctx context.Context, bridge string) ([]state.MACEntry, error)
	// Counters returns statistics for all interfaces.
	Counters(ctx context.Context) (map[string]Counters, error)
	// Routes returns default routes.
	Routes(ctx context.Context) (Routes, error)
	// Sysctl reads sysctl values.
	Sysctl(ctx context.Context, names ...string) (map[string]string, error)
	// SysInfo returns host information.
	SysInfo(ctx context.Context) (SysInfo, error)
	// Run executes a (privileged) command.
	Run(ctx context.Context, op Op) (string, error)
	// WriteFile writes a (privileged) file from the allowlist.
	WriteFile(ctx context.Context, path string, data []byte) error
}

// SysctlsOfInterest are read for planning.
var SysctlsOfInterest = []string{
	"net.link.bridge.pfil_member",
	"net.link.bridge.pfil_bridge",
	"net.link.bridge.pfil_onlyip",
	"net.link.bridge.log_mac_flap",
	"net.inet.ip.forwarding",
	"net.inet6.ip6.forwarding",
}
