package privsep

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
)

type rec struct{ ran []string }

func (r *rec) Run(ctx context.Context, op backend.Op) (string, error) {
	r.ran = append(r.ran, op.String())
	return "ok", nil
}
func (r *rec) WriteFile(ctx context.Context, p string, d []byte) error {
	r.ran = append(r.ran, "write "+p)
	return nil
}

func TestPolicyEnforcedServerSide(t *testing.T) {
	sock := t.TempDir() + "/p.sock"
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ex := &rec{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, l, ex, t.Logf)
	c := NewClient(sock)
	if out, err := c.Run(ctx, backend.Exec("ifconfig", "bridge0", "addm", "vtnet1")); err != nil || out != "ok" {
		t.Fatal(out, err)
	}
	if _, err := c.Run(ctx, backend.Exec("sh", "-c", "id")); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("expected policy denial, got %v", err)
	}
	if err := c.WriteFile(ctx, "/etc/passwd", []byte("x")); err == nil {
		t.Fatal("file write should be denied")
	}
	if err := c.WriteFile(ctx, "/etc/resolv.conf", []byte("nameserver 1.1.1.1\n")); err != nil {
		t.Fatal(err)
	}
	if len(ex.ran) != 2 {
		t.Fatalf("executed %v", ex.ran)
	}
}
