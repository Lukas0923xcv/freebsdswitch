package privsep

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

// Listeners are passed to the daemon as fds 3.. in this order; the names
// are announced in BSDSWITCH_LISTEN_FDS.
type Listener struct {
	Name string // https4, https6, http4, http6
	Addr string
}

// SupervisorConfig configures the root helper.
type SupervisorConfig struct {
	User      string   // _bsdswitch
	Exe       string   // path of the multi-call binary
	Args      []string // daemon args (argv[0] = bsdswitchd)
	Listeners []Listener
	Socket    string
	Logf      func(string, ...any)
}

// Supervise binds listeners, then runs and restarts the daemon until ctx
// is cancelled. Must run as root.
func Supervise(ctx context.Context, cfg SupervisorConfig) error {
	u, err := user.Lookup(cfg.User)
	if err != nil {
		return fmt.Errorf("user %s: %w (create it with: pw useradd %s -d /nonexistent -s /usr/sbin/nologin)", cfg.User, err, cfg.User)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	var files []*os.File
	var names string
	for _, l := range cfg.Listeners {
		netw := "tcp4"
		if l.Name == "https6" || l.Name == "http6" {
			netw = "tcp6"
		}
		ln, err := net.Listen(netw, l.Addr)
		if err != nil {
			// IPv6 may be unavailable; continue without it
			cfg.Logf("privd: cannot listen on %s %s: %v", netw, l.Addr, err)
			continue
		}
		f, err := ln.(*net.TCPListener).File()
		if err != nil {
			return err
		}
		ln.Close()
		files = append(files, f)
		if names != "" {
			names += ","
		}
		names += l.Name
	}
	backoff := time.Second
	first := true
	for ctx.Err() == nil {
		args := append([]string{}, cfg.Args...)
		if first {
			args = append(args, "-boot")
		}
		cmd := exec.Command(cfg.Exe, args[1:]...)
		cmd.Args[0] = args[0]
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.ExtraFiles = files
		cmd.Env = []string{"PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin",
			"BSDSWITCH_LISTEN_FDS=" + names, "BSDSWITCH_PRIVD_SOCK=" + cfg.Socket, "HOME=/nonexistent"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, Setpgid: true}
		start := time.Now()
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start daemon: %w", err)
		}
		first = false
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-ctx.Done():
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				cmd.Process.Kill()
			}
			return nil
		case err := <-done:
			cfg.Logf("privd: daemon exited: %v", err)
			if time.Since(start) > time.Minute {
				backoff = time.Second
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}
	return nil
}

// InheritedListeners returns the listeners passed by privd.
func InheritedListeners() map[string]net.Listener {
	out := map[string]net.Listener{}
	names := os.Getenv("BSDSWITCH_LISTEN_FDS")
	if names == "" {
		return out
	}
	for i, n := range splitComma(names) {
		f := os.NewFile(uintptr(3+i), n)
		l, err := net.FileListener(f)
		f.Close()
		if err == nil {
			out[n] = l
		}
	}
	return out
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
