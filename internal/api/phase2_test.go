package api

import (
	"strings"
	"testing"

	"github.com/Lukas0923xcv/freebsdswitch/internal/commit"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

func TestLAGLifecycle(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	a.ok("PATCH", "/api/v1/ports/vtnet3", map[string]any{"mode": "trunk", "tagged": []int{}}, nil)
	a.ok("POST", "/api/v1/lags", map[string]any{"protocol": "lacp", "members": []string{"vtnet3", "vtnet4"}, "hash": "l2,l3"}, nil)
	var cand config.Config
	a.ok("GET", "/api/v1/config/candidate", nil, &cand)
	if cand.Port("vtnet3") != nil || cand.Port("lagg0") == nil || cand.Port("lagg0").Mode != "trunk" {
		t.Fatalf("lag ports: %+v", cand.Ports)
	}
	var pv commit.Preview
	a.ok("GET", "/api/v1/config/diff", nil, &pv)
	cmds := strings.Join(pv.Commands, "\n")
	for _, w := range []string{"ifconfig lagg0 create", "ifconfig lagg0 laggproto lacp", "ifconfig lagg0 laggport vtnet3", "ifconfig bridge0 addm lagg0"} {
		if !strings.Contains(cmds, w) {
			t.Errorf("missing %q:\n%s", w, cmds)
		}
	}
	a.ok("POST", "/api/v1/commit", map[string]any{}, nil)
	a.ok("POST", "/api/v1/commit/confirm", nil, nil)
	var lags []LAGView
	a.ok("GET", "/api/v1/lags", nil, &lags)
	if len(lags) != 1 || !lags[0].Exists || len(lags[0].Ports) != 2 || !lags[0].Ports[0].Active {
		t.Fatalf("%+v", lags)
	}
	// the same member cannot be in two LAGs
	if code, _ := a.do("POST", "/api/v1/lags", map[string]any{"protocol": "lacp", "members": []string{"vtnet4"}}); code != 400 {
		t.Fatal("member reuse accepted")
	}
	a.ok("DELETE", "/api/v1/lags/lagg0", nil, nil)
	a.ok("GET", "/api/v1/config/candidate", nil, &cand)
	if cand.Port("vtnet3") == nil || cand.Port("vtnet3").Mode != "trunk" || cand.Port("lagg0") != nil {
		t.Fatalf("after delete: %+v", cand.Ports)
	}
}

func TestACLRateLimitPreview(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	a.ok("PUT", "/api/v1/acls", []map[string]any{{"name": "guest", "port": "vtnet2", "direction": "in", "default": "block",
		"rules": []map[string]any{{"action": "pass", "proto": "udp", "dst_port": "53"}, {"action": "block", "dst": "192.168.0.0/16"}, {"action": "pass"}}}}, nil)
	a.ok("PUT", "/api/v1/rate-limits", []map[string]any{{"port": "vtnet2", "ingress_kbps": 20000}, {"port": "vtnet1"}}, nil)
	var p map[string]any
	a.ok("GET", "/api/v1/filters/preview", nil, &p)
	conf := p["pf_conf"].(string)
	if !strings.Contains(conf, "pass in quick on vtnet2 proto udp from any to any port 53 keep state dnpipe 1001") ||
		!strings.Contains(conf, "block drop in quick on vtnet2 from any to 192.168.0.0/16") {
		t.Fatal(conf)
	}
	if code, _ := a.do("PUT", "/api/v1/acls", []map[string]any{{"name": "x", "direction": "in", "default": "pass", "rules": []map[string]any{{"action": "pass", "dst_port": "80"}}}}); code != 400 {
		t.Fatal("port without tcp/udp accepted")
	}
	if code, _ := a.do("PUT", "/api/v1/acls", []map[string]any{{"name": "x", "direction": "in", "default": "pass", "rules": []map[string]any{{"action": "pass", "src": "1.2.3.4; rm"}}}}); code != 400 {
		t.Fatal("bad address accepted")
	}
	var rl []config.RateLimit
	a.ok("GET", "/api/v1/rate-limits", nil, &rl)
	if len(rl) != 1 {
		t.Fatal("zero rate limits should be dropped", rl)
	}
}

func TestMirrorsAndServices(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	if code, _ := a.do("PUT", "/api/v1/mirrors", []map[string]any{{"destination": "vtnet1"}}); code != 400 {
		t.Fatal("switch port as mirror destination accepted")
	}
	a.ok("PUT", "/api/v1/mirrors", []map[string]any{{"destination": "igb0"}}, nil)
	a.ok("PATCH", "/api/v1/services", map[string]any{"snmp": map[string]any{"enabled": true, "community": "monitor", "location": "rack 1"},
		"lldp": map[string]any{"enabled": true}, "syslog": map[string]any{"servers": []string{"192.168.1.5:514"}}}, nil)
	var pv commit.Preview
	a.ok("GET", "/api/v1/config/diff", nil, &pv)
	cmds := strings.Join(pv.Commands, "\n")
	for _, w := range []string{"ifconfig bridge0 span igb0", "bsnmpd_enable=YES", "lldpd_enable=YES", "@192.168.1.5:514"} {
		if !strings.Contains(cmds, w) {
			t.Errorf("missing %q", w)
		}
	}
	a.ok("POST", "/api/v1/commit", map[string]any{}, nil)
	a.ok("POST", "/api/v1/commit/confirm", nil, nil)
	var n []Neighbor
	a.ok("GET", "/api/v1/lldp/neighbors", nil, &n)
	if len(n) == 0 {
		t.Fatal("no simulated neighbors")
	}
	if code, _ := a.do("PATCH", "/api/v1/services", map[string]any{"snmp": map[string]any{"enabled": true, "community": "bad community!"}}); code != 400 {
		t.Fatal("bad community accepted")
	}
}

func TestParseLLDP(t *testing.T) {
	js := `{"lldp":{"interface":{"vtnet4":{"via":"LLDP","age":"0 day, 00:01:02",
	 "chassis":{"core":{"id":{"type":"mac","value":"52:54:00:aa:bb:cc"},"descr":"FreeBSD","mgmt-ip":"192.168.1.1",
	   "capability":[{"type":"Bridge","enabled":false},{"type":"Router","enabled":true}]}},
	 "port":{"id":{"type":"ifname","value":"ix0"},"descr":"uplink","ttl":"120"}}}}}`
	n, err := ParseLLDP([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 || n[0].SystemName != "core" || n[0].PortID != "ix0" || n[0].ChassisID != "52:54:00:aa:bb:cc" ||
		n[0].MgmtIP[0] != "192.168.1.1" || len(n[0].Capabilities) != 1 || n[0].LocalPort != "vtnet4" {
		t.Fatalf("%+v", n)
	}
	js2 := `{"lldp":{"interface":[{"vtnet1":{"chassis":{"id":{"type":"mac","value":"aa"}},"port":{"id":{"value":"p1"}}}},{"vtnet2":{"chassis":{"sw2":{"id":{"value":"bb"}}},"port":{"id":{"value":"p2"}}}}]}}`
	n, err = ParseLLDP([]byte(js2))
	if err != nil || len(n) != 2 || n[0].ChassisID != "aa" || n[1].SystemName != "sw2" {
		t.Fatalf("%+v %v", n, err)
	}
}

func TestUpdateUpload(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	code, _ := a.do("POST", "/api/v1/system/update/upload?name=../../etc/passwd", strings.Repeat("x", 2000))
	if code != 400 {
		t.Fatal("bad name accepted")
	}
	a.ok("POST", "/api/v1/system/update/upload?name=bsdswitch-0.2.0.pkg", strings.Repeat("x", 2000), nil)
	var st updateStatus
	a.ok("GET", "/api/v1/system/update", nil, &st)
	if st.Uploaded != "bsdswitch-0.2.0.pkg" {
		t.Fatal(st)
	}
	var r map[string]string
	a.ok("POST", "/api/v1/system/update/apply", map[string]any{"source": "upload"}, &r)
	if !strings.HasPrefix(r["boot_environment"], "bsdswitch-") {
		t.Fatal(r)
	}
}
