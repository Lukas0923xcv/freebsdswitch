package api

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/auth"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/commit"
	"github.com/Lukas0923xcv/freebsdswitch/internal/logbuf"
	"github.com/Lukas0923xcv/freebsdswitch/internal/stats"
)

// ---- users -------------------------------------------------------------------

func (s *Server) getUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.opt.Auth.Users())
}

func (s *Server) addUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		Role       string `json:"role"`
		Password   string `json:"password"`
		MustChange bool   `json:"must_change"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	err := s.opt.Auth.AddUser(req.Name, req.Role, req.Password, req.MustChange)
	s.audit(r, "user-add", req.Name+" ("+req.Role+")", err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 201, map[string]bool{"ok": true})
}

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role       string `json:"role"`
		Password   string `json:"password"`
		MustChange *bool  `json:"must_change"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	name := r.PathValue("name")
	err := s.opt.Auth.UpdateUser(name, req.Role, req.Password, req.MustChange)
	detail := name
	if req.Role != "" {
		detail += " role=" + req.Role
	}
	if req.Password != "" {
		detail += " password reset"
	}
	s.audit(r, "user-update", detail, err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == s.user(r) {
		badRequest(w, "you cannot delete your own account")
		return
	}
	err := s.opt.Auth.DeleteUser(name)
	s.audit(r, "user-delete", name, err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) getSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.opt.Auth.Sessions())
}

// ---- tokens ------------------------------------------------------------------

func (s *Server) getTokens(w http.ResponseWriter, r *http.Request) {
	sess := session(r)
	owner := sess.User
	if sess.Role == auth.RoleAdmin && r.URL.Query().Get("all") == "1" {
		owner = ""
	}
	writeJSON(w, 200, s.opt.Auth.Tokens(owner))
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Role    string `json:"role"`
		TTLDays int    `json:"ttl_days"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	sess := session(r)
	if sess.ViaToken && sess.User != "console" {
		badRequest(w, "tokens cannot create tokens")
		return
	}
	secret, tok, err := s.opt.Auth.CreateToken(sess.User, req.Name, req.Role, time.Duration(req.TTLDays)*24*time.Hour)
	s.audit(r, "token-create", req.Name, err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 201, map[string]any{"token": secret, "info": tok})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	sess := session(r)
	owner := sess.User
	if sess.Role == auth.RoleAdmin {
		owner = ""
	}
	err := s.opt.Auth.DeleteToken(r.PathValue("id"), owner)
	s.audit(r, "token-delete", r.PathValue("id"), err)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- TLS -----------------------------------------------------------------------

func (s *Server) getTLS(w http.ResponseWriter, r *http.Request) {
	if s.opt.TLS == nil {
		writeJSON(w, 200, map[string]any{})
		return
	}
	writeJSON(w, 200, s.opt.TLS.Info())
}

func (s *Server) putTLS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cert       string `json:"cert"`
		Key        string `json:"key"`
		Regenerate bool   `json:"regenerate"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if s.opt.TLS == nil {
		badRequest(w, "TLS is not enabled")
		return
	}
	var err error
	if req.Regenerate {
		err = s.opt.TLS.Regenerate(s.opt.Engine.Running().System.Hostname)
	} else {
		err = s.opt.TLS.Install([]byte(req.Cert), []byte(req.Key))
	}
	s.audit(r, "tls-cert", "", err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, s.opt.TLS.Info())
}

// ---- audit / logs ----------------------------------------------------------------

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > 5000 {
		n = 500
	}
	writeJSON(w, 200, s.opt.Engine.AuditLog(n))
}

func tailFile(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return []string{}
	}
	defer f.Close()
	st, _ := f.Stat()
	const max = 512 << 10
	if st != nil && st.Size() > max {
		f.Seek(st.Size()-max, io.SeekStart)
	}
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if lines == nil {
		lines = []string{}
	}
	return lines
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > 5000 {
		n = 500
	}
	resp := map[string]any{"daemon": s.opt.Logs.Tail(n), "syslog": []string{}}
	if s.opt.SyslogPath != "" {
		resp["syslog"] = tailFile(s.opt.SyslogPath, n)
	}
	writeJSON(w, 200, resp)
}

// ---- SSE events -------------------------------------------------------------------

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, fmt.Errorf("streaming unsupported"))
		return
	}
	topics := map[string]bool{"stats": true, "commit": true, "link": true}
	if t := r.URL.Query().Get("topics"); t != "" {
		topics = map[string]bool{}
		for _, x := range strings.Split(t, ",") {
			topics[x] = true
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	var mu sync.Mutex
	send := func(ev string, v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return true
		}
		mu.Lock()
		defer mu.Unlock()
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	send("hello", s.configStatus())
	var statCh chan map[string]stats.Sample
	if topics["stats"] || topics["link"] {
		statCh = s.opt.Stats.Subscribe()
		defer s.opt.Stats.Unsubscribe(statCh)
	}
	var evCh chan commit.Event
	if topics["commit"] {
		evCh = s.opt.Engine.Subscribe()
		defer s.opt.Engine.Unsubscribe(evCh)
	}
	var logs chan logbuf.Line
	if topics["logs"] {
		logs = s.opt.Logs.Subscribe()
		defer s.opt.Logs.Unsubscribe(logs)
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	lastLink := map[string]int{}
	sess := session(r)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if sess != nil && sess.ID != "" {
				if _, ok := s.opt.Auth.Session(sess.ID); !ok {
					send("logout", map[string]string{"reason": "session expired"})
					return
				}
			}
			mu.Lock()
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
			mu.Unlock()
		case smp := <-statCh:
			if topics["stats"] {
				run := s.opt.Engine.Running()
				var tot stats.Sample
				ports := map[string]stats.Sample{}
				for _, p := range run.Ports {
					x := smp[p.Name]
					ports[p.Name] = x
					tot.T = x.T
					tot.RxBps += x.RxBps
					tot.TxBps += x.TxBps
					tot.RxPps += x.RxPps
					tot.TxPps += x.TxPps
				}
				if !send("stats", map[string]any{"total": tot, "ports": ports}) {
					return
				}
			}
			if topics["link"] {
				changed := map[string]bool{}
				for _, p := range s.opt.Engine.Running().Ports {
					c, ok := s.opt.Stats.Counters(p.Name)
					if !ok {
						continue
					}
					if prev, seen := lastLink[p.Name]; seen && prev != c.LinkState {
						changed[p.Name] = c.LinkState == 2
					}
					lastLink[p.Name] = c.LinkState
				}
				if len(changed) > 0 && !send("link", changed) {
					return
				}
			}
		case ev := <-evCh:
			if !send("commit", map[string]any{"event": ev, "status": s.configStatus()}) {
				return
			}
		case l := <-logs:
			if !send("log", l) {
				return
			}
		}
	}
}

// ---- diagnostics -------------------------------------------------------------------

// Diagnoser runs diagnostics.
type Diagnoser interface {
	Ping(ctx context.Context, host string, count int, v6 bool) (string, error)
	Traceroute(ctx context.Context, host string, v6 bool) (string, error)
	// Capture runs tcpdump on iface writing to path.
	Capture(ctx context.Context, iface string, count int, filter []string, path string) error
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.\-]{0,252})$`)

func validHost(h string) bool {
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	return hostRe.MatchString(h) && !strings.HasPrefix(h, "-")
}

type diagReq struct {
	Host   string `json:"host"`
	Count  int    `json:"count"`
	Family string `json:"family"` // "", "4", "6"
}

func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	var req diagReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if !validHost(req.Host) {
		badRequest(w, "invalid host")
		return
	}
	if req.Count <= 0 || req.Count > 20 {
		req.Count = 4
	}
	if s.opt.Diag == nil {
		badRequest(w, "diagnostics unavailable")
		return
	}
	v6 := req.Family == "6"
	if a, err := netip.ParseAddr(req.Host); err == nil && a.Is6() {
		v6 = true
	}
	out, err := s.opt.Diag.Ping(r.Context(), req.Host, req.Count, v6)
	writeJSON(w, 200, map[string]any{"output": out, "error": errString(err)})
}

func (s *Server) traceroute(w http.ResponseWriter, r *http.Request) {
	var req diagReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if !validHost(req.Host) {
		badRequest(w, "invalid host")
		return
	}
	if s.opt.Diag == nil {
		badRequest(w, "diagnostics unavailable")
		return
	}
	v6 := req.Family == "6"
	if a, err := netip.ParseAddr(req.Host); err == nil && a.Is6() {
		v6 = true
	}
	out, err := s.opt.Diag.Traceroute(r.Context(), req.Host, v6)
	writeJSON(w, 200, map[string]any{"output": out, "error": errString(err)})
}

type capture struct {
	ID      string    `json:"id"`
	Port    string    `json:"port"`
	Count   int       `json:"count"`
	Filter  string    `json:"filter"`
	Started time.Time `json:"started"`
	Done    bool      `json:"done"`
	Error   string    `json:"error"`
	Size    int64     `json:"size"`
	path    string
}

var filterWordRe = regexp.MustCompile(`^[A-Za-z0-9.:/\-]+$`)

func (s *Server) startCapture(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port    string `json:"port"`
		Count   int    `json:"count"`
		Seconds int    `json:"seconds"`
		Filter  string `json:"filter"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	run := s.opt.Engine.Running()
	okPort := req.Port == run.Management.Interface || req.Port == run.Bridge.Name || run.Port(req.Port) != nil
	if !okPort {
		badRequest(w, "unknown port")
		return
	}
	if req.Count <= 0 || req.Count > 100000 {
		req.Count = 1000
	}
	if req.Seconds <= 0 || req.Seconds > 60 {
		req.Seconds = 30
	}
	var filter []string
	if f := strings.TrimSpace(req.Filter); f != "" {
		filter = strings.Fields(f)
		for _, x := range filter {
			if !filterWordRe.MatchString(x) {
				badRequest(w, "capture filter may only contain letters, digits and . : / - (e.g. \"port 53\", \"host 10.0.0.1\", \"vlan 10\")")
				return
			}
		}
	}
	if s.opt.Diag == nil {
		badRequest(w, "diagnostics unavailable")
		return
	}
	rb := make([]byte, 8)
	rand.Read(rb)
	id := hex.EncodeToString(rb)
	dir := filepath.Join(s.opt.StateDir, "capture")
	os.MkdirAll(dir, 0o770)
	c := &capture{ID: id, Port: req.Port, Count: req.Count, Filter: strings.Join(filter, " "), Started: time.Now(), path: filepath.Join(dir, id+".pcap")}
	s.mu.Lock()
	// keep at most 5 captures
	if len(s.captures) >= 5 {
		var oldest *capture
		for _, x := range s.captures {
			if oldest == nil || x.Started.Before(oldest.Started) {
				oldest = x
			}
		}
		os.Remove(oldest.path)
		delete(s.captures, oldest.ID)
	}
	s.captures[id] = c
	s.mu.Unlock()
	s.audit(r, "capture", fmt.Sprintf("%s count=%d filter=%q", req.Port, req.Count, c.Filter), nil)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.Seconds)*time.Second)
		defer cancel()
		err := s.opt.Diag.Capture(ctx, req.Port, req.Count, filter, c.path)
		s.mu.Lock()
		c.Done = true
		if err != nil && ctx.Err() == nil {
			c.Error = err.Error()
		}
		if st, err := os.Stat(c.path); err == nil {
			c.Size = st.Size()
		}
		s.mu.Unlock()
	}()
	writeJSON(w, 202, c)
}

func (s *Server) getCapture(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("id"), ".pcap")
	s.mu.Lock()
	c, ok := s.captures[id]
	var cc capture
	if ok {
		cc = *c
	}
	s.mu.Unlock()
	if !ok {
		writeErr(w, 404, fmt.Errorf("no such capture"))
		return
	}
	if r.URL.Query().Get("download") == "" && !strings.HasSuffix(r.PathValue("id"), ".pcap") {
		writeJSON(w, 200, cc)
		return
	}
	if !cc.Done {
		writeErr(w, 409, fmt.Errorf("capture still running"))
		return
	}
	w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="bsdswitch-%s-%s.pcap"`, cc.Port, cc.Started.Format("20060102-150405")))
	http.ServeFile(w, r, cc.path)
}

// ---- power ---------------------------------------------------------------------------

func (s *Server) restartDaemon(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "restart-daemon", "", nil)
	writeJSON(w, 202, map[string]bool{"ok": true})
	if s.opt.OnRestart != nil {
		go func() {
			time.Sleep(500 * time.Millisecond)
			s.opt.OnRestart()
		}()
	}
}

func (s *Server) power(w http.ResponseWriter, r *http.Request, flag, action string) {
	var req struct {
		Confirm bool `json:"confirm"`
	}
	decode(r, &req)
	if !req.Confirm {
		badRequest(w, "set confirm=true to %s", action)
		return
	}
	s.audit(r, action, "", nil)
	writeJSON(w, 202, map[string]bool{"ok": true})
	go func() {
		time.Sleep(time.Second)
		if _, err := backend.Execute(context.Background(), s.opt.Backend, backend.Exec("shutdown", flag, "now")); err != nil {
			s.opt.Logs.Printf("%s failed: %v", action, err)
		}
	}()
}

func (s *Server) reboot(w http.ResponseWriter, r *http.Request)   { s.power(w, r, "-r", "reboot") }
func (s *Server) shutdown(w http.ResponseWriter, r *http.Request) { s.power(w, r, "-p", "shutdown") }
