// Package daemon wires up bsdswitchd.
package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/api"
	"github.com/Lukas0923xcv/freebsdswitch/internal/auth"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/freebsd"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/mock"
	"github.com/Lukas0923xcv/freebsdswitch/internal/commit"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/logbuf"
	"github.com/Lukas0923xcv/freebsdswitch/internal/plan"
	"github.com/Lukas0923xcv/freebsdswitch/internal/privsep"
	"github.com/Lukas0923xcv/freebsdswitch/internal/stats"
	"github.com/Lukas0923xcv/freebsdswitch/internal/tlsutil"
	"github.com/Lukas0923xcv/freebsdswitch/internal/version"
)

// Paths used on the appliance.
const (
	DefaultConfigDir = "/usr/local/etc/bsdswitch"
	DefaultStateDir  = "/var/db/bsdswitch"
	DefaultAPISocket = "/var/run/bsdswitch/api.sock"
)

// UI is set by main to the embedded web UI filesystem.
var UI fs.FS

// Options from the command line.
type Options struct {
	ConfigDir   string
	StateDir    string
	Sim         bool
	Listen      string // dev: listen directly with TLS on this address
	ListenHTTP  string // dev: plain HTTP (tests only, insecure cookies)
	APISocket   string
	UIDir       string
	Boot        bool
	NoFilter    bool
	SimPorts    int
}

func parseFlags(args []string) (*Options, error) {
	o := &Options{}
	fl := flag.NewFlagSet("bsdswitchd", flag.ContinueOnError)
	fl.StringVar(&o.ConfigDir, "config-dir", DefaultConfigDir, "configuration directory (config.yaml, users.yaml, tls/)")
	fl.StringVar(&o.StateDir, "state", DefaultStateDir, "state directory (running config, history, audit log)")
	fl.BoolVar(&o.Sim, "sim", false, "use the built-in kernel simulator (development / UI testing on any OS)")
	fl.StringVar(&o.Listen, "listen", "", "listen for HTTPS on this address instead of inherited sockets (e.g. 127.0.0.1:8443)")
	fl.StringVar(&o.ListenHTTP, "listen-http", "", "INSECURE: plain HTTP on this address (tests only)")
	fl.StringVar(&o.APISocket, "socket", DefaultAPISocket, "local API socket for the console and bsdswitchctl")
	fl.StringVar(&o.UIDir, "ui-dir", "", "serve the web UI from this directory instead of the embedded copy")
	fl.BoolVar(&o.Boot, "boot", false, "set by bsdswitch-privd on the first start after it was started")
	fl.BoolVar(&o.NoFilter, "no-mgmt-filter", false, "accept web connections on any local address")
	fl.IntVar(&o.SimPorts, "sim-ports", 0, "simulator: number of virtio switch ports (default lab: 4 vtnet + 2 igb)")
	if err := fl.Parse(args); err != nil {
		return nil, err
	}
	return o, nil
}

// Main is the entry point of bsdswitchd.
func Main(args []string) int {
	o, err := parseFlags(args)
	if err != nil {
		return 2
	}
	if err := Run(o); err != nil {
		fmt.Fprintln(os.Stderr, "bsdswitchd:", err)
		return 1
	}
	return 0
}

// Run starts the daemon and blocks until SIGTERM/SIGINT.
func Run(o *Options) error {
	logs := logbuf.New(5000, os.Stderr)
	logs.Printf("bsdswitchd %s (%s) starting", version.Version, version.Commit)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	for _, d := range []string{o.ConfigDir, o.StateDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}

	var be backend.Backend
	var sim *mock.Sim
	var diag api.Diagnoser
	switch {
	case o.Sim:
		if o.SimPorts > 0 {
			nics := []mock.NIC{{Name: "vtnet0", Link: true}}
			for i := 1; i <= o.SimPorts; i++ {
				nics = append(nics, mock.NIC{Name: fmt.Sprintf("vtnet%d", i), Link: i%5 != 0})
			}
			sim = mock.New(nics...)
		} else {
			sim = mock.Lab()
		}
		sim.Traffic = true
		be = sim
		diag = simDiag{}
		logs.Printf("SIMULATION MODE: no system changes are made")
	case os.Getenv("BSDSWITCH_PRIVD_SOCK") != "":
		c := privsep.NewClient(os.Getenv("BSDSWITCH_PRIVD_SOCK"))
		be = c
		diag = &realDiag{be: c}
	default:
		if os.Geteuid() != 0 {
			return errors.New("not started by bsdswitch-privd and not root; use `service bsdswitchd start` or -sim")
		}
		l := freebsd.New()
		be = l
		diag = &realDiag{be: l}
		logs.Printf("warning: running without privilege separation")
	}

	paths := commit.Paths{Startup: filepath.Join(o.ConfigDir, "config.yaml"), State: o.StateDir}
	eng, err := commit.New(paths, be, logs.Printf)
	if err != nil {
		return err
	}
	eng.Persist = !o.Sim

	// No configuration at all (console wizard not run): create factory
	// defaults so the box is reachable via DHCP on the first NIC with link.
	if _, err := os.Stat(paths.Startup); os.IsNotExist(err) {
		if c, err := FactoryDefault(ctx, be); err == nil {
			config.Save(paths.Startup, c)
			os.WriteFile(filepath.Join(o.StateDir, "wizard-pending"), nil, 0o640)
			logs.Printf("no configuration found: created factory defaults (management %s via DHCP)", c.Management.Interface)
		} else {
			logs.Printf("cannot create factory defaults: %v", err)
		}
	}
	fresh := o.Sim || o.Boot && isFreshBoot(o.StateDir)
	fallback := ""
	if c, err := FactoryDefault(ctx, be); err == nil {
		fallback = c.Management.Interface
	}
	if err := eng.Boot(ctx, fresh, fallback); err != nil {
		logs.Printf("BOOT WARNING: %v", err)
	}
	logs.Printf("running configuration established (%s)", eng.BootMode())

	am, err := auth.Open(filepath.Join(o.ConfigDir, "users.yaml"))
	if err != nil {
		return err
	}
	am.IdleTimeout = time.Duration(eng.Running().Management.SessionTimeout) * time.Minute
	if pw, err := am.EnsureAdmin(); err != nil {
		return err
	} else if pw != "" {
		WriteInitialPassword(o.StateDir, pw)
		logs.Printf("created web UI user 'admin' with a one-time password (shown on the console, menu option 5); it must be changed at first login")
		if o.Sim {
			fmt.Fprintf(os.Stderr, "\n  *** initial admin password: %s ***\n\n", pw)
		}
	}

	col := stats.New(be)
	go col.Run(ctx)

	tm, err := tlsutil.Open(filepath.Join(o.ConfigDir, "tls"), eng.Running().System.Hostname)
	if err != nil {
		return fmt.Errorf("TLS: %w", err)
	}

	ui := UI
	if o.UIDir != "" {
		ui = os.DirFS(o.UIDir)
	}
	opt := api.Options{Engine: eng, Auth: am, Stats: col, Backend: be, Logs: logs, UI: ui, StateDir: o.StateDir,
		TLS: tm, Diag: diag, SyslogPath: "/var/log/messages", InsecureCookies: o.ListenHTTP != "" && o.Listen == ""}
	opt.Factory = func(ctx context.Context) (*config.Config, error) { return FactoryDefault(ctx, be) }
	if sim != nil {
		opt.Sim = sim
		opt.SyslogPath = ""
	}
	opt.OnRestart = func() {
		logs.Printf("restart requested via API")
		stop()
	}
	srv := api.New(opt)

	// session timeout follows config changes
	go func() {
		ch := eng.Subscribe()
		defer eng.Unsubscribe(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				am.IdleTimeout = time.Duration(eng.Running().Management.SessionTimeout) * time.Minute
			}
		}
	}()

	filter := newMgmtFilter(eng, be, logs.Printf)
	if !o.NoFilter && !o.Sim {
		go filter.run(ctx)
	} else {
		filter.disabled = true
	}

	tlsCfg := tm.TLSConfig()
	var servers []*http.Server
	var wg sync.WaitGroup
	serve := func(l net.Listener, h http.Handler, useTLS bool, name string) {
		hs := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
			ErrorLog: nil}
		servers = append(servers, hs)
		if useTLS {
			hs.TLSConfig = tlsCfg
			l = tls.NewListener(l, tlsCfg)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			logs.Printf("listening on %s (%s)", l.Addr(), name)
			if err := hs.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logs.Printf("%s: %v", name, err)
			}
		}()
	}
	httpsPort := func() int { return eng.Running().Management.HTTPSPort }
	inherited := privsep.InheritedListeners()
	for name, l := range inherited {
		l = filter.wrap(l)
		if strings.HasPrefix(name, "https") {
			serve(l, srv, true, name)
		} else {
			serve(l, api.RedirectHandler(httpsPort), false, name)
		}
	}
	if o.Listen != "" {
		l, err := net.Listen("tcp", o.Listen)
		if err != nil {
			return err
		}
		serve(l, srv, true, "https")
	}
	if o.ListenHTTP != "" {
		l, err := net.Listen("tcp", o.ListenHTTP)
		if err != nil {
			return err
		}
		serve(l, srv, false, "http (INSECURE, testing only)")
	}
	if len(inherited) == 0 && o.Listen == "" && o.ListenHTTP == "" {
		logs.Printf("warning: no web listeners (not started by privd and no -listen given)")
	}
	if o.APISocket != "" {
		os.MkdirAll(filepath.Dir(o.APISocket), 0o750)
		os.Remove(o.APISocket)
		if l, err := net.Listen("unix", o.APISocket); err == nil {
			os.Chmod(o.APISocket, 0o600)
			serve(l, api.LocalSession(srv), false, "local socket")
		} else {
			logs.Printf("local API socket: %v", err)
		}
	}
	<-ctx.Done()
	logs.Printf("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		s.Shutdown(sctx)
	}
	wg.Wait()
	return nil
}

// FactoryDefault builds the factory configuration: management on the
// first Ethernet interface with link (else the first one) via DHCP, all
// other Ethernet interfaces are access ports in VLAN 1.
func FactoryDefault(ctx context.Context, be backend.Backend) (*config.Config, error) {
	snap, err := be.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	eth := snap.Ethernet()
	if len(eth) == 0 {
		return nil, errors.New("no Ethernet interfaces found")
	}
	mgmt := eth[0].Name
	for _, e := range eth {
		if e.LinkUp() {
			mgmt = e.Name
			break
		}
	}
	var ports []string
	for _, e := range eth {
		if e.Name != mgmt {
			ports = append(ports, e.Name)
		}
	}
	return config.Default(mgmt, ports), nil
}

// WriteInitialPassword stores the one-time admin password for the console.
func WriteInitialPassword(stateDir, pw string) {
	os.WriteFile(filepath.Join(stateDir, "initial-admin-password"), []byte(pw+"\n"), 0o600)
}

// isFreshBoot compares kern.boottime with the value recorded last time.
func isFreshBoot(stateDir string) bool {
	bt := bootTime()
	f := filepath.Join(stateDir, "boottime")
	prev, _ := os.ReadFile(f)
	os.WriteFile(f, []byte(bt), 0o640)
	return bt == "" || strings.TrimSpace(string(prev)) != bt
}

// ---- management access filter ------------------------------------------------------

// mgmtFilter only accepts web connections addressed to an IP of the
// management interface, the in-band management VLAN interface or
// loopback. (Bridge member ports carry no IP addresses, so the web UI is
// never reachable through a switch port unless in-band management is
// configured.)
type mgmtFilter struct {
	mu       sync.RWMutex
	allowed  map[netip.Addr]bool
	eng      *commit.Engine
	be       backend.Backend
	logf     func(string, ...any)
	disabled bool
}

func newMgmtFilter(eng *commit.Engine, be backend.Backend, logf func(string, ...any)) *mgmtFilter {
	return &mgmtFilter{allowed: map[netip.Addr]bool{}, eng: eng, be: be, logf: logf}
}

func (f *mgmtFilter) refresh(ctx context.Context) {
	snap, err := f.be.Snapshot(ctx)
	if err != nil {
		return
	}
	run := f.eng.Running()
	names := []string{run.Management.Interface, "lo0"}
	if run.Management.InBand.Enabled {
		names = append(names, plan.InBandName(run))
	}
	m := map[netip.Addr]bool{}
	for _, n := range names {
		if i := snap.Get(n); i != nil {
			for _, a := range append(append([]string{}, i.IPv4...), i.IPv6...) {
				if p, err := netip.ParsePrefix(a); err == nil {
					m[p.Addr()] = true
				}
			}
		}
	}
	f.mu.Lock()
	f.allowed = m
	f.mu.Unlock()
}

func (f *mgmtFilter) run(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	ch := f.eng.Subscribe()
	defer f.eng.Unsubscribe(ch)
	f.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.refresh(ctx)
		case <-ch:
			f.refresh(ctx)
		}
	}
}

func (f *mgmtFilter) ok(local net.Addr) bool {
	if f.disabled {
		return true
	}
	ta, ok := local.(*net.TCPAddr)
	if !ok {
		return true
	}
	a, ok := netip.AddrFromSlice(ta.IP)
	if !ok {
		return false
	}
	a = a.Unmap().WithZone("")
	if a.IsLoopback() {
		return true
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.allowed[a]
}

type filteredListener struct {
	net.Listener
	f *mgmtFilter
}

func (l filteredListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.f.ok(c.LocalAddr()) {
			return c, nil
		}
		l.f.logf("rejected web connection from %s to non-management address %s", c.RemoteAddr(), c.LocalAddr())
		c.Close()
	}
}

func (f *mgmtFilter) wrap(l net.Listener) net.Listener { return filteredListener{l, f} }
