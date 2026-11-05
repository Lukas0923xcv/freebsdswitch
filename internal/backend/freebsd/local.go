package freebsd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/state"
)

// Local talks to the local kernel directly. Reads work unprivileged;
// Run/WriteFile need root and are normally only used inside
// bsdswitch-privd.
type Local struct {
	cpu cpuSampler
}

func New() *Local { return &Local{} }

func run(ctx context.Context, prog string, args ...string) (string, error) {
	path, ok := Programs[prog]
	if !ok {
		path = prog
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin", "LANG=C"}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return out.String(), fmt.Errorf("%s %s: %v: %s", prog, strings.Join(args, " "), err, msg)
	}
	return out.String(), nil
}

func (l *Local) Snapshot(ctx context.Context) (*state.Snapshot, error) {
	cl, err := run(ctx, "ifconfig", "-C")
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, "ifconfig", "-a", "-m", "-D")
	if err != nil {
		return nil, err
	}
	ifs, err := state.ParseIfconfig(out, state.ParseCloners(cl))
	if err != nil {
		return nil, err
	}
	return &state.Snapshot{Interfaces: ifs}, nil
}

func (l *Local) MACTable(ctx context.Context, bridge string) ([]state.MACEntry, error) {
	out, err := run(ctx, "ifconfig", bridge, "addr")
	if err != nil {
		return nil, err
	}
	return state.ParseBridgeAddr(out), nil
}

func (l *Local) Routes(ctx context.Context) (backend.Routes, error) {
	var r backend.Routes
	if out, err := run(ctx, "route", "-n", "get", "-inet", "default"); err == nil {
		r.Default4 = ParseRouteGateway(out)
	}
	if out, err := run(ctx, "route", "-n", "get", "-inet6", "default"); err == nil {
		r.Default6 = ParseRouteGateway(out)
	}
	return r, nil
}

// ParseRouteGateway extracts "gateway: x" from `route -n get` output.
func ParseRouteGateway(out string) string {
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "gateway:") {
			g := strings.TrimSpace(strings.TrimPrefix(l, "gateway:"))
			if i := strings.IndexByte(g, '%'); i >= 0 {
				g = g[:i]
			}
			return g
		}
	}
	return ""
}

func (l *Local) Sysctl(ctx context.Context, names ...string) (map[string]string, error) {
	out, err := run(ctx, "sysctl", append([]string{"-i", "-e"}, names...)...)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m, nil
}

func (l *Local) Run(ctx context.Context, op backend.Op) (string, error) {
	if err := CheckOp(op); err != nil {
		return "", err
	}
	return run(ctx, op.Argv[0], op.Argv[1:]...)
}

func (l *Local) WriteFile(ctx context.Context, path string, data []byte) error {
	if err := CheckOp(backend.Op{Path: path, Content: string(data)}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if strings.HasSuffix(path, "key.pem") {
		mode = 0o600
	}
	tmp := path + ".bsdswitch.tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
