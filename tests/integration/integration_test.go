//go:build integration

// Integration tests against the real FreeBSD kernel: if_bridge with VLAN
// filtering, epair(4) switch ports and VNET jails as hosts.
//
// Run inside a FreeBSD 15 VM as root:
//
//	make lab-test
//
// (equivalent to: go build -o build/bsdswitch ./cmd/bsdswitch &&
//
//	go test -tags integration -v -count=1 ./tests/integration/)
package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/api"
	"github.com/Lukas0923xcv/freebsdswitch/internal/client"
)

var (
	lab  string
	sock string
	c    *client.Client
	eps  = map[string][2]string{} // jail -> (host side, jail side)
)

func sh(t *testing.T, cmd string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
	return string(out)
}

// ping from jail src to dst; returns true if at least one reply arrives.
func ping(src, dst string) bool {
	err := exec.Command("jexec", "bswlab_"+src, "ping", "-c", "2", "-t", "3", "-o", dst).Run()
	return err == nil
}

// eventually retries ping until want or timeout.
func eventually(t *testing.T, what, src, dst string, want bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if ping(src, dst) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: ping %s -> %s: want reachable=%v", what, src, dst, want)
		}
		time.Sleep(time.Second)
	}
}

// never asserts that ping fails for the whole duration.
func never(t *testing.T, what, src, dst string, d time.Duration) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if ping(src, dst) {
			t.Fatalf("%s: ping %s -> %s succeeded but must fail (VLAN isolation)", what, src, dst)
		}
	}
}

func TestMain(m *testing.M) {
	if runtime.GOOS != "freebsd" || os.Geteuid() != 0 {
		fmt.Println("integration tests need FreeBSD and root; skipping")
		os.Exit(0)
	}
	os.Exit(run(m))
}

func run(m *testing.M) int {
	root, _ := filepath.Abs("../..")
	lab, _ = os.MkdirTemp("", "bswlab")
	os.Setenv("LAB", lab)
	os.Setenv("BSDSWITCH_BIN", filepath.Join(root, "build/bsdswitch"))
	os.Setenv("LAB_WEB", "0.0.0.0:8443")
	labsh := filepath.Join(root, "tests/integration/lab.sh")
	if out, err := exec.Command("sh", labsh, "up").CombinedOutput(); err != nil {
		fmt.Printf("lab up failed: %v\n%s\n", err, out)
		exec.Command("sh", labsh, "down").Run()
		return 1
	}
	defer exec.Command("sh", labsh, "down").Run()
	b, _ := os.ReadFile(filepath.Join(lab, "epairs"))
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(l)
		if len(f) == 3 {
			eps[f[0]] = [2]string{f[1], f[2]}
		}
	}
	d := exec.Command("sh", labsh, "run")
	logf, _ := os.Create(filepath.Join(lab, "daemon.log"))
	d.Stdout, d.Stderr = logf, logf
	if err := d.Start(); err != nil {
		fmt.Println(err)
		return 1
	}
	defer d.Process.Kill()
	sock = filepath.Join(lab, "api.sock")
	c = client.Unix(sock)
	for i := 0; i < 60; i++ {
		var v map[string]string
		if c.Get("/api/v1/version", &v) == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	code := m.Run()
	if code != 0 {
		out, _ := os.ReadFile(filepath.Join(lab, "daemon.log"))
		fmt.Printf("---- daemon log ----\n%s\n", out)
		o, _ := exec.Command("ifconfig", "bridge77").CombinedOutput()
		fmt.Printf("---- ifconfig bridge77 ----\n%s\n", o)
	}
	return code
}

func TestBridgeConfigured(t *testing.T) {
	out := sh(t, "ifconfig bridge77")
	if !strings.Contains(out, "VLANFILTER") {
		t.Fatalf("vlanfilter not enabled:\n%s", out)
	}
	if !strings.Contains(out, "member: "+eps["h1"][0]) {
		t.Fatalf("h1 port not a member:\n%s", out)
	}
	// the management interface must not be a bridge member
	if strings.Contains(out, "member: "+eps["mgmt"][0]+" ") {
		t.Fatal("management interface is a bridge member")
	}
}

func TestSameVLANReachable(t *testing.T) {
	eventually(t, "VLAN 10 access<->access", "h1", "10.10.0.2", true, 40*time.Second)
}

func TestVLANIsolation(t *testing.T) {
	eventually(t, "warm-up", "h1", "10.10.0.2", true, 40*time.Second)
	never(t, "VLAN 10 -> VLAN 20 (same IP subnet)", "h1", "10.10.0.3", 8*time.Second)
}

func TestTrunk(t *testing.T) {
	eventually(t, "trunk tagged VLAN 10", "h4", "10.10.0.1", true, 45*time.Second)
	eventually(t, "trunk tagged VLAN 20", "h4", "10.20.0.3", true, 45*time.Second)
	eventually(t, "trunk native VLAN 1", "h4", "10.1.0.5", true, 45*time.Second)
}

func TestSpanningTreeBlocksLoop(t *testing.T) {
	deadline := time.Now().Add(45 * time.Second)
	for {
		var s struct {
			Ports []map[string]any `json:"ports"`
		}
		if err := c.Get("/api/v1/stp", &s); err != nil {
			t.Fatal(err)
		}
		blocked := 0
		for _, p := range s.Ports {
			n := p["name"].(string)
			if (n == eps["loop"][0] || n == eps["loop"][1]) && (p["state"] == "discarding" || p["state"] == "blocking") {
				blocked++
			}
		}
		if blocked >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no loop port is discarding: %+v", s.Ports)
		}
		time.Sleep(time.Second)
	}
	// no broadcast storm: normal traffic still works
	eventually(t, "VLAN 1 with loop present", "h4", "10.1.0.5", true, 20*time.Second)
}

func TestManagementReachableFromMgmtStation(t *testing.T) {
	out, err := exec.Command("jexec", "bswlab_mgmt", "fetch", "-q", "-o", "-", "--no-verify-peer", "--no-verify-hostname",
		"https://192.0.2.1:8443/api/v1/version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "version") {
		t.Fatalf("web API not reachable from management station: %v %s", err, out)
	}
}

func commit(t *testing.T, timeout int) {
	t.Helper()
	var p api.PendingView
	if err := c.Post("/api/v1/commit", map[string]any{"timeout": timeout, "comment": t.Name()}, &p); err != nil {
		t.Fatal(err)
	}
}

func TestCommitConfirmRollback(t *testing.T) {
	eventually(t, "baseline", "h1", "10.10.0.2", true, 40*time.Second)
	// move h2 to VLAN 20: h1 can no longer reach it
	if err := c.Patch("/api/v1/ports/"+eps["h2"][0], map[string]any{"pvid": 20}, nil); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	commit(t, 10)
	never(t, "h2 moved to VLAN 20", "h1", "10.10.0.2", 5*time.Second)
	// not confirmed -> rollback by the daemon after 10 s
	eventually(t, "after automatic rollback", "h1", "10.10.0.2", true, 30*time.Second)
	if time.Since(start) < 9*time.Second {
		t.Fatal("rolled back too early")
	}
	var st api.ConfigStatus
	c.Get("/api/v1/commit", &st)
	if st.Pending != nil {
		t.Fatal("still pending after rollback")
	}
}

func TestCommitConfirmKeep(t *testing.T) {
	// move h3 into VLAN 10 and confirm
	if err := c.Patch("/api/v1/ports/"+eps["h3"][0], map[string]any{"pvid": 10}, nil); err != nil {
		t.Fatal(err)
	}
	commit(t, 30)
	eventually(t, "h3 joined VLAN 10", "h1", "10.10.0.3", true, 20*time.Second)
	if err := c.Post("/api/v1/commit/confirm", nil, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	eventually(t, "still reachable after confirm", "h1", "10.10.0.3", true, 5*time.Second)
	// restore
	if err := c.Patch("/api/v1/ports/"+eps["h3"][0], map[string]any{"pvid": 20}, nil); err != nil {
		t.Fatal(err)
	}
	commit(t, 30)
	c.Post("/api/v1/commit/confirm", nil, nil)
	never(t, "h3 back in VLAN 20", "h1", "10.10.0.3", 4*time.Second)
}

func TestTrunkRemoveTaggedVLAN(t *testing.T) {
	eventually(t, "baseline", "h4", "10.20.0.3", true, 45*time.Second)
	if err := c.Patch("/api/v1/ports/"+eps["h4"][0], map[string]any{"tagged": []int{10}}, nil); err != nil {
		t.Fatal(err)
	}
	commit(t, 60)
	never(t, "VLAN 20 removed from trunk", "h4", "10.20.0.3", 5*time.Second)
	eventually(t, "VLAN 10 still on trunk", "h4", "10.10.0.1", true, 10*time.Second)
	c.Post("/api/v1/commit/cancel", nil, nil)
	eventually(t, "trunk restored", "h4", "10.20.0.3", true, 20*time.Second)
}
