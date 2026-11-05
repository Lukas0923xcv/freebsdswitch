package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/auth"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/mock"
	"github.com/Lukas0923xcv/freebsdswitch/internal/commit"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/logbuf"
	"github.com/Lukas0923xcv/freebsdswitch/internal/stats"
)

func init() { auth.BcryptCost = 4 }

type env struct {
	t       *testing.T
	srv     *httptest.Server
	api     *Server
	sim     *mock.Sim
	eng     *commit.Engine
	am      *auth.Manager
	col     *stats.Collector
	adminPW string
}

type fakeDiag struct{}

func (fakeDiag) Ping(ctx context.Context, h string, n int, v6 bool) (string, error) {
	return "PING " + h, nil
}
func (fakeDiag) Traceroute(ctx context.Context, h string, v6 bool) (string, error) {
	return "traceroute " + h, nil
}
func (fakeDiag) Capture(ctx context.Context, i string, n int, f []string, p string) error {
	return nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	sim := mock.Lab()
	eng, err := commit.New(commit.Paths{Startup: dir + "/etc/config.yaml", State: dir + "/db"}, sim, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	eng.Persist = false
	st := config.Default("vtnet0", []string{"vtnet1", "vtnet2", "vtnet3", "vtnet4", "igb1"})
	config.Save(dir+"/etc/config.yaml", st)
	if err := eng.Boot(context.Background(), true, ""); err != nil {
		t.Fatal(err)
	}
	am, _ := auth.Open(dir + "/etc/users.yaml")
	pw, _ := am.EnsureAdmin()
	col := stats.New(sim)
	col.Poll(context.Background())
	col.Poll(context.Background())
	s := New(Options{Engine: eng, Auth: am, Stats: col, Backend: sim, Logs: logbuf.New(100, nil),
		StateDir: dir + "/db", Diag: fakeDiag{}, Sim: sim, InsecureCookies: true,
		Factory: func(ctx context.Context) (*config.Config, error) {
			return config.Default("vtnet0", []string{"vtnet1"}), nil
		}})
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return &env{t: t, srv: ts, api: s, sim: sim, eng: eng, am: am, col: col, adminPW: pw}
}

// client is a cookie session.
type tclient struct {
	e    *env
	hc   *http.Client
	csrf string
	tok  string
}

func (e *env) anon() *tclient {
	jar, _ := cookiejar.New(nil)
	return &tclient{e: e, hc: &http.Client{Jar: jar}}
}

func (c *tclient) do(method, path string, body any) (int, []byte) {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		if b, ok := body.(string); ok {
			rd = strings.NewReader(b)
		} else {
			js, _ := json.Marshal(body)
			rd = bytes.NewReader(js)
		}
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if body != nil {
		if _, ok := body.(string); ok {
			req.Header.Set("Content-Type", "application/yaml")
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if c.csrf != "" {
		req.Header.Set(CSRFHeader, c.csrf)
	}
	if c.tok != "" {
		req.Header.Set("Authorization", "Bearer "+c.tok)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (c *tclient) ok(method, path string, body any, out any) {
	c.e.t.Helper()
	code, b := c.do(method, path, body)
	if code >= 300 {
		c.e.t.Fatalf("%s %s: %d %s", method, path, code, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.e.t.Fatalf("%s %s: decode: %v (%s)", method, path, err, b)
		}
	}
}

func (c *tclient) login(user, pw string) int {
	code, b := c.do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pw})
	if code == 200 {
		var m meResp
		json.Unmarshal(b, &m)
		c.csrf = m.CSRF
	}
	return code
}

// admin returns a logged-in admin who already changed the initial password.
func (e *env) admin() *tclient {
	c := e.anon()
	if code := c.login("admin", e.adminPW); code != 200 {
		e.t.Fatalf("login %d", code)
	}
	c.ok("POST", "/api/v1/auth/password", map[string]string{"old_password": e.adminPW, "new_password": "new admin secret"}, nil)
	e.adminPW = "new admin secret"
	return c
}

func TestAllEndpointsRequireAuth(t *testing.T) {
	e := newEnv(t)
	c := e.anon()
	for _, ep := range []string{
		"GET /api/v1/system", "GET /api/v1/interfaces", "PUT /api/v1/interfaces/assignment", "GET /api/v1/ports",
		"PATCH /api/v1/ports/vtnet1", "PATCH /api/v1/ports", "GET /api/v1/ports/vtnet1/stats", "POST /api/v1/ports/reset-counters",
		"GET /api/v1/vlans", "POST /api/v1/vlans", "DELETE /api/v1/vlans/10", "PATCH /api/v1/vlans/matrix",
		"GET /api/v1/mac-table", "POST /api/v1/mac-table/flush", "POST /api/v1/static-macs", "GET /api/v1/stp",
		"PATCH /api/v1/bridge", "PATCH /api/v1/system/settings", "PATCH /api/v1/management",
		"GET /api/v1/config/running", "PUT /api/v1/config/candidate", "DELETE /api/v1/config/candidate", "GET /api/v1/config/diff",
		"GET /api/v1/config/history", "POST /api/v1/config/rollback/1", "POST /api/v1/config/save", "GET /api/v1/config/export",
		"POST /api/v1/config/import", "GET /api/v1/commit", "POST /api/v1/commit", "POST /api/v1/commit/confirm",
		"POST /api/v1/commit/cancel", "GET /api/v1/users", "POST /api/v1/users", "GET /api/v1/tokens", "POST /api/v1/tokens",
		"GET /api/v1/tls", "POST /api/v1/tls", "GET /api/v1/audit", "GET /api/v1/logs", "GET /api/v1/events",
		"POST /api/v1/diag/ping", "POST /api/v1/diag/capture", "POST /api/v1/system/reboot", "POST /api/v1/system/restart",
		"POST /api/v1/console/apply", "POST /api/v1/console/factory-reset", "GET /api/v1/capabilities",
	} {
		m, p, _ := strings.Cut(ep, " ")
		if code, _ := c.do(m, p, nil); code != 401 {
			t.Errorf("%s: expected 401, got %d", ep, code)
		}
	}
}

func TestLoginFlowAndForcedPasswordChange(t *testing.T) {
	e := newEnv(t)
	c := e.anon()
	if code := c.login("admin", "wrong"); code != 401 {
		t.Fatalf("bad login: %d", code)
	}
	if code := c.login("admin", e.adminPW); code != 200 {
		t.Fatal(code)
	}
	if code, b := c.do("GET", "/api/v1/ports", nil); code != 403 || !strings.Contains(string(b), "password change") {
		t.Fatalf("expected forced change: %d %s", code, b)
	}
	if code, _ := c.do("POST", "/api/v1/auth/password", map[string]string{"old_password": e.adminPW, "new_password": "admin"}); code != 400 {
		t.Fatal("weak password accepted")
	}
	c.ok("POST", "/api/v1/auth/password", map[string]string{"old_password": e.adminPW, "new_password": "much better pw"}, nil)
	c.ok("GET", "/api/v1/ports", nil, nil)
	c.ok("POST", "/api/v1/auth/logout", nil, nil)
	if code, _ := c.do("GET", "/api/v1/ports", nil); code != 401 {
		t.Fatal("logout did not end session")
	}
}

func TestRateLimitedLogin(t *testing.T) {
	e := newEnv(t)
	c := e.anon()
	for i := 0; i < 5; i++ {
		c.login("admin", "nope")
	}
	if code := c.login("admin", e.adminPW); code != 429 {
		t.Fatalf("expected 429, got %d", code)
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t)
	c := e.admin()
	csrf := c.csrf
	c.csrf = ""
	if code, b := c.do("POST", "/api/v1/vlans", map[string]any{"id": 10, "name": "x"}); code != 403 || !strings.Contains(string(b), "CSRF") {
		t.Fatalf("expected CSRF rejection: %d %s", code, b)
	}
	c.csrf = "wrong"
	if code, _ := c.do("POST", "/api/v1/vlans", map[string]any{"id": 10, "name": "x"}); code != 403 {
		t.Fatal("wrong CSRF accepted")
	}
	c.csrf = csrf
	c.ok("POST", "/api/v1/vlans", map[string]any{"id": 10, "name": "x"}, nil)
}

func TestReadOnlyRole(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	a.ok("POST", "/api/v1/users", map[string]any{"name": "viewer", "role": "readonly", "password": "viewer secret"}, nil)
	r := e.anon()
	if r.login("viewer", "viewer secret") != 200 {
		t.Fatal("viewer login")
	}
	for _, p := range []string{"/api/v1/ports", "/api/v1/vlans", "/api/v1/system", "/api/v1/config/running", "/api/v1/stp", "/api/v1/mac-table", "/api/v1/config/diff"} {
		r.ok("GET", p, nil, nil)
	}
	for _, ep := range []struct {
		m, p string
		b    any
	}{
		{"PATCH", "/api/v1/ports/vtnet1", map[string]any{"description": "x"}},
		{"POST", "/api/v1/vlans", map[string]any{"id": 20, "name": "y"}},
		{"POST", "/api/v1/commit", map[string]any{}},
		{"POST", "/api/v1/config/save", nil},
		{"GET", "/api/v1/users", nil},
		{"POST", "/api/v1/system/reboot", map[string]any{"confirm": true}},
		{"GET", "/api/v1/config/export", nil},
	} {
		if code, _ := r.do(ep.m, ep.p, ep.b); code != 403 {
			t.Errorf("readonly %s %s: %d", ep.m, ep.p, code)
		}
	}
}

func TestAPITokens(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	var resp struct {
		Token string `json:"token"`
		Info  struct {
			ID string `json:"id"`
		} `json:"info"`
	}
	a.ok("POST", "/api/v1/tokens", map[string]any{"name": "ci", "role": "admin"}, &resp)
	tc := e.anon()
	tc.tok = resp.Token
	// no CSRF needed for bearer tokens
	tc.ok("POST", "/api/v1/vlans", map[string]any{"id": 30, "name": "via-token"}, nil)
	var ro struct {
		Token string `json:"token"`
	}
	a.ok("POST", "/api/v1/tokens", map[string]any{"name": "mon", "role": "readonly"}, &ro)
	rc := e.anon()
	rc.tok = ro.Token
	rc.ok("GET", "/api/v1/ports", nil, nil)
	if code, _ := rc.do("POST", "/api/v1/vlans", map[string]any{"id": 31, "name": "x"}); code != 403 {
		t.Fatal("readonly token wrote")
	}
	a.ok("DELETE", "/api/v1/tokens/"+resp.Info.ID, nil, nil)
	if code, _ := tc.do("GET", "/api/v1/ports", nil); code != 401 {
		t.Fatal("deleted token still valid")
	}
}

func TestPortVLANCommitConfirmSaveFlow(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	a.ok("POST", "/api/v1/vlans", map[string]any{"id": 10, "name": "office"}, nil)
	a.ok("PATCH", "/api/v1/ports/vtnet2", map[string]any{"pvid": 10, "description": "PC-2"}, nil)
	a.ok("PATCH", "/api/v1/vlans/matrix", map[string]any{"changes": []map[string]any{{"port": "vtnet4", "vlan": 10, "membership": "T"}}}, nil)

	var st ConfigStatus
	a.ok("GET", "/api/v1/commit", nil, &st)
	if !st.Staged || st.Pending != nil {
		t.Fatalf("status %+v", st)
	}
	var pv commit.Preview
	a.ok("GET", "/api/v1/config/diff", nil, &pv)
	cmds := strings.Join(pv.Commands, "\n")
	for _, want := range []string{"ifconfig bridge0 ifuntagged vtnet2 10", "ifconfig bridge0 +iftagged vtnet4 10", "ifconfig vtnet2 description PC-2"} {
		if !strings.Contains(cmds, want) {
			t.Errorf("preview missing %q:\n%s", want, cmds)
		}
	}
	// not applied yet
	var ports []PortView
	a.ok("GET", "/api/v1/ports", nil, &ports)
	for _, p := range ports {
		if p.Name == "vtnet2" && (p.KernelPVID != 1 || !p.Staged) {
			t.Fatalf("staged change applied too early: %+v", p)
		}
	}
	var pend PendingView
	a.ok("POST", "/api/v1/commit", map[string]any{"timeout": 30, "comment": "office"}, &pend)
	if pend.DeadlineMs-pend.NowMs < 25000 {
		t.Fatalf("deadline %+v", pend)
	}
	a.ok("GET", "/api/v1/ports", nil, &ports)
	for _, p := range ports {
		if p.Name == "vtnet2" && p.KernelPVID != 10 {
			t.Fatalf("not applied: %+v", p)
		}
	}
	if code, _ := a.do("POST", "/api/v1/config/save", nil); code != 409 {
		t.Fatalf("save during pending should conflict, got %d", code)
	}
	a.ok("POST", "/api/v1/commit/confirm", nil, nil)
	a.ok("GET", "/api/v1/commit", nil, &st)
	if !st.StartupDiffers {
		t.Fatal("startup should differ")
	}
	a.ok("POST", "/api/v1/config/save", nil, nil)
	a.ok("GET", "/api/v1/commit", nil, &st)
	if st.StartupDiffers || st.Staged || st.Pending != nil {
		t.Fatalf("after save %+v", st)
	}
	var cmp map[string]any
	a.ok("GET", "/api/v1/config/compare?a=startup&b=running", nil, &cmp)
	if cmp["diff"] != "" {
		t.Fatal("startup != running after save")
	}
}

func TestCancelRollsBack(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	a.ok("PATCH", "/api/v1/ports/vtnet1", map[string]any{"shutdown": true}, nil)
	a.ok("POST", "/api/v1/commit", map[string]any{"timeout": 30}, nil)
	a.ok("POST", "/api/v1/commit/cancel", nil, nil)
	snap, _ := e.sim.Snapshot(context.Background())
	if !snap.Get("vtnet1").AdminUp() {
		t.Fatal("not rolled back")
	}
	if code, _ := a.do("POST", "/api/v1/commit/confirm", nil); code != 409 {
		t.Fatal("confirm without pending should be 409")
	}
}

func TestValidationErrorsReturned(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	code, b := a.do("PATCH", "/api/v1/ports/vtnet1", map[string]any{"pvid": 99})
	if code != 400 {
		t.Fatal(code)
	}
	var ae apiError
	json.Unmarshal(b, &ae)
	if len(ae.Fields) == 0 || ae.Fields[0].Path != "ports[vtnet1].pvid" {
		t.Fatalf("%s", b)
	}
	if code, _ := a.do("POST", "/api/v1/vlans", map[string]any{"id": 5000, "name": "x"}); code != 400 {
		t.Fatal("VLAN 5000 accepted")
	}
	if code, _ := a.do("PATCH", "/api/v1/management", map[string]any{"ipv4": map[string]any{"mode": "static", "address": "1.2.3.4"}}); code != 400 {
		t.Fatal("bad IP accepted")
	}
	if code, _ := a.do("PATCH", "/api/v1/bridge", map[string]any{"mtu": 70000}); code != 400 {
		t.Fatal("bad MTU accepted")
	}
	if code, _ := a.do("DELETE", "/api/v1/vlans/1", nil); code != 400 {
		t.Fatal("deleting VLAN 1 allowed")
	}
	if code, _ := a.do("PATCH", "/api/v1/ports/vtnet1", map[string]any{"bogus": 1}); code != 400 {
		t.Fatal("unknown field accepted")
	}
}

func TestVLANDeleteMovesPorts(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	a.ok("POST", "/api/v1/vlans", map[string]any{"id": 20, "name": "iot"}, nil)
	a.ok("PATCH", "/api/v1/ports/vtnet3", map[string]any{"pvid": 20}, nil)
	var r map[string]any
	a.ok("DELETE", "/api/v1/vlans/20", nil, &r)
	if moved := r["moved_to_vlan1"].([]any); len(moved) != 1 || moved[0] != "vtnet3" {
		t.Fatal(r)
	}
}

func TestBulkAndStaticMACAndMACTable(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	a.ok("POST", "/api/v1/vlans", map[string]any{"id": 10, "name": "a"}, nil)
	a.ok("PATCH", "/api/v1/ports", map[string]any{"names": []string{"vtnet1", "vtnet2"}, "set": map[string]any{"pvid": 10}}, nil)
	a.ok("POST", "/api/v1/static-macs", map[string]any{"mac": "02-00-00-00-00-01", "port": "vtnet1", "vlan": 10}, nil)
	a.ok("POST", "/api/v1/commit", map[string]any{}, nil)
	a.ok("POST", "/api/v1/commit/confirm", nil, nil)
	e.sim.Learn("bridge0", "02:aa:bb:cc:dd:ee", 1, "vtnet3")
	var mt struct {
		Entries []map[string]any `json:"entries"`
	}
	a.ok("GET", "/api/v1/mac-table?vlan=10", nil, &mt)
	if len(mt.Entries) != 1 || mt.Entries[0]["static"] != true {
		t.Fatalf("%+v", mt)
	}
	a.ok("GET", "/api/v1/mac-table?port=vtnet3", nil, &mt)
	if len(mt.Entries) != 1 {
		t.Fatalf("%+v", mt)
	}
	a.ok("POST", "/api/v1/mac-table/flush", map[string]any{}, nil)
	a.ok("GET", "/api/v1/mac-table", nil, &mt)
	if len(mt.Entries) != 1 {
		t.Fatalf("flush should keep static only: %+v", mt)
	}
	a.ok("DELETE", "/api/v1/static-macs/10/02:00:00:00:00:01", nil, nil)
}

func TestInterfacesAndAssignment(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	var ifs []InterfaceView
	a.ok("GET", "/api/v1/interfaces", nil, &ifs)
	roles := map[string]string{}
	for _, i := range ifs {
		roles[i.Name] = i.Role
		if i.Driver == "vtnet" && i.Speed != NA {
			t.Errorf("virtio speed should be n/a: %+v", i)
		}
	}
	if roles["vtnet0"] != "management" || roles["vtnet1"] != "port" || roles["igb0"] != "unused" {
		t.Fatal(roles)
	}
	a.ok("PUT", "/api/v1/interfaces/assignment", map[string]any{"management": "vtnet0", "ports": []string{"vtnet1", "vtnet2", "igb0"}, "unused": []string{"vtnet3", "vtnet4", "igb1"}}, nil)
	var cand config.Config
	a.ok("GET", "/api/v1/config/candidate", nil, &cand)
	if len(cand.Ports) != 3 || cand.Port("igb0") == nil {
		t.Fatalf("%+v", cand.Ports)
	}
}

func TestPortViewSpeed(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	var ports []PortView
	a.ok("GET", "/api/v1/ports", nil, &ports)
	for _, p := range ports {
		if p.Name == "vtnet1" && (p.Speed != NA || p.SpeedSettable || p.SpeedSetting != NA) {
			t.Errorf("vtnet: %+v", p)
		}
		if p.Name == "igb1" && (p.Speed != "1000" || !p.SpeedSettable || len(p.SpeedOptions) != 4) {
			t.Errorf("igb: %+v", p)
		}
	}
	a.ok("PATCH", "/api/v1/ports/igb1", map[string]any{"speed": "100", "duplex": "full"}, nil)
	var pv commit.Preview
	a.ok("GET", "/api/v1/config/diff", nil, &pv)
	if !strings.Contains(strings.Join(pv.Commands, "\n"), "ifconfig igb1 media 100baseTX mediaopt full-duplex") {
		t.Fatal(pv.Commands)
	}
}

func TestConfigImportExportAndHistory(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	code, y := a.do("GET", "/api/v1/config/export", nil)
	if code != 200 || !strings.Contains(string(y), "bridge0") {
		t.Fatal(code)
	}
	mod := strings.Replace(string(y), "hostname: bsdswitch", "hostname: imported", 1)
	a.ok("POST", "/api/v1/config/import", mod, nil)
	a.ok("POST", "/api/v1/commit", map[string]any{}, nil)
	a.ok("POST", "/api/v1/commit/confirm", nil, nil)
	if e.eng.Running().System.Hostname != "imported" {
		t.Fatal("import not applied")
	}
	if code, _ := a.do("POST", "/api/v1/config/import", "version: 1\nnonsense: true\n"); code != 400 {
		t.Fatal("invalid YAML accepted")
	}
	var h []commit.Version
	a.ok("GET", "/api/v1/config/history", nil, &h)
	if len(h) != 2 {
		t.Fatal(h)
	}
	a.ok("POST", "/api/v1/config/rollback/"+itoa(h[1].ID), nil, nil)
	var cand config.Config
	a.ok("GET", "/api/v1/config/candidate", nil, &cand)
	if cand.System.Hostname != "bsdswitch" {
		t.Fatal("rollback not staged")
	}
	code, yb := a.do("GET", "/api/v1/config/running?format=yaml", nil)
	if code != 200 || !strings.HasPrefix(string(yb), "# bsdswitch") {
		t.Fatal(string(yb))
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestUsersAdmin(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	a.ok("POST", "/api/v1/users", map[string]any{"name": "ops", "role": "admin", "password": "ops secret 1"}, nil)
	if code, _ := a.do("POST", "/api/v1/users", map[string]any{"name": "Bad Name", "role": "admin", "password": "x secret 123"}); code != 400 {
		t.Fatal("bad user name")
	}
	a.ok("PATCH", "/api/v1/users/ops", map[string]any{"role": "readonly"}, nil)
	if code, _ := a.do("DELETE", "/api/v1/users/admin", nil); code != 400 {
		t.Fatal("deleting yourself should fail")
	}
	a.ok("DELETE", "/api/v1/users/ops", nil, nil)
	var audit []commit.AuditEntry
	a.ok("GET", "/api/v1/audit", nil, &audit)
	found := false
	for _, x := range audit {
		if x.Action == "user-delete" && x.User == "admin" {
			found = true
		}
	}
	if !found {
		t.Fatal("audit log missing user-delete")
	}
}

func TestDiagnostics(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	if code, _ := a.do("POST", "/api/v1/diag/ping", map[string]any{"host": "-f 1.1.1.1"}); code != 400 {
		t.Fatal("option injection accepted")
	}
	if code, _ := a.do("POST", "/api/v1/diag/ping", map[string]any{"host": "1.1.1.1; reboot"}); code != 400 {
		t.Fatal("shell injection accepted")
	}
	var r map[string]any
	a.ok("POST", "/api/v1/diag/ping", map[string]any{"host": "192.168.1.1", "count": 2}, &r)
	if !strings.Contains(r["output"].(string), "PING") {
		t.Fatal(r)
	}
	if code, _ := a.do("POST", "/api/v1/diag/capture", map[string]any{"port": "vtnet1", "filter": "port 53; rm"}); code != 400 {
		t.Fatal("bad filter accepted")
	}
	if code, _ := a.do("POST", "/api/v1/diag/capture", map[string]any{"port": "em9"}); code != 400 {
		t.Fatal("unknown port accepted")
	}
	var c capture
	a.ok("POST", "/api/v1/diag/capture", map[string]any{"port": "vtnet1", "count": 10, "filter": "vlan 10"}, &c)
	for i := 0; i < 50; i++ {
		a.ok("GET", "/api/v1/diag/capture/"+c.ID, nil, &c)
		if c.Done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !c.Done {
		t.Fatal("capture not done")
	}
}

func TestConsoleEndpointsNotOverNetwork(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	if code, _ := a.do("POST", "/api/v1/console/factory-reset", nil); code != 403 {
		t.Fatalf("factory reset over network: %d", code)
	}
	if code, _ := a.do("POST", "/api/v1/console/reset-password", map[string]string{"user": "admin"}); code != 403 {
		t.Fatal("password reset over network")
	}
	// via local socket handler it works
	h := LocalSession(e.api)
	req := httptest.NewRequest("POST", "/api/v1/console/reset-password", strings.NewReader(`{"user":"admin"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "password") {
		t.Fatal(rec.Code, rec.Body.String())
	}
}

func TestAllowedSubnets(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	a.ok("PATCH", "/api/v1/management", map[string]any{"allowed_subnets": []string{"192.168.1.0/24"}}, nil)
	a.ok("POST", "/api/v1/commit", map[string]any{}, nil)
	a.ok("POST", "/api/v1/commit/confirm", nil, nil)
	for remote, want := range map[string]int{"10.0.0.5:5555": 403, "192.168.1.77:5555": 401, "127.0.0.1:1": 401} {
		req := httptest.NewRequest("GET", "/api/v1/ports", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		e.api.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: got %d want %d", remote, rec.Code, want)
		}
	}
}

func TestSSE(t *testing.T) {
	e := newEnv(t)
	a := e.admin()
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/events", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := a.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(resp.Header)
	}
	rd := bufio.NewReader(resp.Body)
	got := map[string]bool{}
	go func() {
		time.Sleep(100 * time.Millisecond)
		e.col.Poll(context.Background())
		e.eng.Audit(commit.AuditEntry{})
		e.eng.DiscardCandidate("admin")
	}()
	for !(got["hello"] && got["stats"] && got["commit"]) {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended, got %v", got)
		}
		if strings.HasPrefix(line, "event: ") {
			got[strings.TrimSpace(strings.TrimPrefix(line, "event: "))] = true
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t)
	resp, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	c := e.anon()
	code := c.login("admin", e.adminPW)
	if code != 200 {
		t.Fatal(code)
	}
}

func TestRedirect(t *testing.T) {
	h := RedirectHandler(func() int { return 443 })
	req := httptest.NewRequest("GET", "http://10.0.0.2/ports?x=1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 301 || rec.Header().Get("Location") != "https://10.0.0.2/ports?x=1" {
		t.Fatal(rec.Code, rec.Header())
	}
}
