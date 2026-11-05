// Package privd is the root helper bsdswitch-privd.
package privd

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/freebsd"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/privsep"
)

func logf(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }

// Main runs bsdswitch-privd.
func Main(args []string) int {
	fl := flag.NewFlagSet("bsdswitch-privd", flag.ContinueOnError)
	cfgDir := fl.String("config-dir", "/usr/local/etc/bsdswitch", "configuration directory")
	stateDir := fl.String("state", "/var/db/bsdswitch", "state directory")
	userName := fl.String("user", "_bsdswitch", "unprivileged user for bsdswitchd")
	sock := fl.String("socket", privsep.DefaultSocket, "privileged command socket")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if os.Geteuid() != 0 {
		logf("bsdswitch-privd must run as root")
		return 1
	}
	u, err := user.Lookup(*userName)
	if err != nil {
		logf("user %s not found: %v", *userName, err)
		return 1
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	// directories owned by the daemon user
	for _, d := range []string{*cfgDir, filepath.Join(*cfgDir, "tls"), *stateDir, filepath.Join(*stateDir, "capture"), filepath.Join(*stateDir, "update")} {
		os.MkdirAll(d, 0o750)
		os.Chown(d, uid, gid)
	}
	filepath.Walk(*cfgDir, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			os.Lchown(p, uid, gid)
		}
		return nil
	})
	filepath.Walk(*stateDir, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			os.Lchown(p, uid, gid)
		}
		return nil
	})
	runDir := filepath.Dir(*sock)
	os.MkdirAll(runDir, 0o750)
	os.Chown(runDir, 0, gid)
	os.Chmod(runDir, 0o770)

	for _, m := range []string{"if_bridge", "if_lagg", "bridgestp"} {
		exec.Command("/sbin/kldload", "-n", m).Run()
	}

	os.Remove(*sock)
	l, err := net.Listen("unix", *sock)
	if err != nil {
		logf("listen %s: %v", *sock, err)
		return 1
	}
	os.Chown(*sock, 0, gid)
	os.Chmod(*sock, 0o660)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go privsep.Serve(ctx, l, freebsd.New(), logf)

	httpsPort, httpPort := 443, 80
	for _, p := range []string{filepath.Join(*stateDir, "running.yaml"), filepath.Join(*cfgDir, "config.yaml")} {
		if c, err := config.Load(p); err == nil {
			httpsPort, httpPort = c.Management.HTTPSPort, c.Management.HTTPPort
			break
		}
	}
	exe, err := os.Executable()
	if err != nil {
		logf("executable: %v", err)
		return 1
	}
	err = privsep.Supervise(ctx, privsep.SupervisorConfig{
		User: *userName, Exe: exe,
		Args: []string{"bsdswitchd", "-config-dir", *cfgDir, "-state", *stateDir},
		Listeners: []privsep.Listener{
			{Name: "https4", Addr: fmt.Sprintf("0.0.0.0:%d", httpsPort)},
			{Name: "https6", Addr: fmt.Sprintf("[::]:%d", httpsPort)},
			{Name: "http4", Addr: fmt.Sprintf("0.0.0.0:%d", httpPort)},
			{Name: "http6", Addr: fmt.Sprintf("[::]:%d", httpPort)},
		},
		Socket: *sock, Logf: logf,
	})
	if err != nil {
		logf("%v", err)
		return 1
	}
	return 0
}
