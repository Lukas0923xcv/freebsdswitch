package daemon

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
)

type realDiag struct{ be backend.Backend }

func runOut(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/sbin:/bin:/usr/sbin:/usr/bin", "LANG=C"}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (d *realDiag) Ping(ctx context.Context, host string, count int, v6 bool) (string, error) {
	args := []string{"-c", strconv.Itoa(count), "-t", strconv.Itoa(count + 5)}
	if v6 {
		args = append([]string{"-6"}, args...)
	}
	return runOut(ctx, time.Duration(count+10)*time.Second, "/sbin/ping", append(args, "--", host)...)
}

func (d *realDiag) Traceroute(ctx context.Context, host string, v6 bool) (string, error) {
	prog := "/usr/sbin/traceroute"
	if v6 {
		prog = "/usr/sbin/traceroute6"
	}
	return runOut(ctx, 90*time.Second, prog, "-n", "-w", "2", "-q", "1", "-m", "20", host)
}

func (d *realDiag) Capture(ctx context.Context, iface string, count int, filter []string, path string) error {
	args := []string{"tcpdump", "-n", "-i", iface, "-c", strconv.Itoa(count), "-s", "0", "-w", path}
	_, err := d.be.Run(ctx, backend.Op{Argv: append(args, filter...)})
	return err
}

// simDiag returns canned output in simulation mode.
type simDiag struct{}

func (simDiag) Ping(ctx context.Context, host string, count int, v6 bool) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "PING %s: 56 data bytes\n", host)
	for i := 0; i < count; i++ {
		fmt.Fprintf(&sb, "64 bytes from %s: icmp_seq=%d ttl=64 time=0.%03d ms\n", host, i, 200+i*13)
	}
	fmt.Fprintf(&sb, "\n--- %s ping statistics ---\n%d packets transmitted, %d packets received, 0.0%% packet loss\n(simulated)\n", host, count, count)
	return sb.String(), nil
}

func (simDiag) Traceroute(ctx context.Context, host string, v6 bool) (string, error) {
	return fmt.Sprintf("traceroute to %s, 20 hops max\n 1  192.168.1.1  0.412 ms\n 2  %s  1.020 ms\n(simulated)\n", host, host), nil
}

func (simDiag) Capture(ctx context.Context, iface string, count int, filter []string, path string) error {
	// write an empty but valid pcap file (global header only)
	h := make([]byte, 24)
	binary.LittleEndian.PutUint32(h[0:], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(h[4:], 2)
	binary.LittleEndian.PutUint16(h[6:], 4)
	binary.LittleEndian.PutUint32(h[16:], 65535)
	binary.LittleEndian.PutUint32(h[20:], 1)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
	return os.WriteFile(path, h, 0o640)
}
