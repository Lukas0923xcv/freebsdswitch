// Package api implements the bsdswitch REST API (/api/v1), the SSE event
// stream and serves the embedded web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/auth"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/commit"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/logbuf"
	"github.com/Lukas0923xcv/freebsdswitch/internal/stats"
	"github.com/Lukas0923xcv/freebsdswitch/internal/tlsutil"
	"github.com/Lukas0923xcv/freebsdswitch/internal/version"
)

const (
	SessionCookie = "bsdswitch_session"
	CSRFHeader    = "X-CSRF-Token"
	maxBody       = 2 << 20
)

// Options configures the server.
type Options struct {
	Engine  *commit.Engine
	Auth    *auth.Manager
	Stats   *stats.Collector
	Backend backend.Backend
	Logs    *logbuf.Buffer
	// UI is the embedded SPA (may be nil).
	UI fs.FS
	// StateDir for captures and update downloads.
	StateDir string
	// TLS manages the web certificate (may be nil in tests).
	TLS *tlsutil.Manager
	// Diag runs ping/traceroute/packet capture.
	Diag Diagnoser
	// OnRestart / OnPower are invoked for system actions.
	OnRestart func()
	// Syslog file to tail in the logs view.
	SyslogPath string
	// Sim enables simulator-only endpoints (e.g. link toggling for tests).
	Sim SimControl
	// InsecureCookies drops the Secure flag (plain-HTTP tests only).
	InsecureCookies bool
	// Factory returns factory default configuration (console reset).
	Factory FactoryFunc
}

// SimControl lets e2e tests poke the simulator.
type SimControl interface {
	SetLink(name string, up bool)
	Learn(bridge, mac string, vlan int, port string)
}

// Server is the HTTP handler.
type Server struct {
	opt Options
	mux *http.ServeMux
	mu  sync.Mutex
	// captures in progress
	captures map[string]*capture
}

type ctxKey int

const sessionKey ctxKey = 1

// New builds the server.
func New(opt Options) *Server {
	s := &Server{opt: opt, mux: http.NewServeMux(), captures: map[string]*capture{}}
	s.routes()
	return s
}

// apiError is the JSON error envelope.
type apiError struct {
	Error  string              `json:"error"`
	Fields []config.FieldError `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	e := apiError{Error: err.Error()}
	var ve *config.ValidationError
	if errors.As(err, &ve) {
		e.Fields = ve.Errors
		e.Error = "validation failed"
		code = http.StatusBadRequest
	}
	switch {
	case errors.Is(err, commit.ErrPending):
		code = http.StatusConflict
	case errors.Is(err, commit.ErrNoPending), errors.Is(err, commit.ErrNoChanges):
		code = http.StatusConflict
	case errors.Is(err, auth.ErrRateLimited):
		code = http.StatusTooManyRequests
	case errors.Is(err, auth.ErrNotFound), errors.Is(err, commit.ErrNoSuchVersion):
		code = http.StatusNotFound
	}
	writeJSON(w, code, e)
}

func badRequest(w http.ResponseWriter, f string, a ...any) {
	writeErr(w, http.StatusBadRequest, fmt.Errorf(f, a...))
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// session returns the authenticated session.
func session(r *http.Request) *auth.Session {
	s, _ := r.Context().Value(sessionKey).(*auth.Session)
	return s
}

func remoteIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// LocalSession marks requests arriving on the local unix socket (root
// console / bsdswitchctl as root) as authenticated admin.
func LocalSession(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := &auth.Session{User: "console", Role: auth.RoleAdmin, Remote: "local", ViaToken: true}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, s)))
	})
}

// handle registers a route. role: "" = public, readonly, admin.
func (s *Server) handle(pattern, role string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if role == "" {
			h(w, r)
			return
		}
		sess := session(r)
		if sess == nil {
			sess = s.authenticate(r)
		}
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "authentication required"})
			return
		}
		// CSRF: cookie-authenticated, state-changing requests must carry
		// the per-session token (double submit).
		if !sess.ViaToken && r.Method != http.MethodGet && r.Method != http.MethodHead {
			if tok := r.Header.Get(CSRFHeader); tok == "" || tok != sess.CSRF {
				writeJSON(w, http.StatusForbidden, apiError{Error: "missing or invalid CSRF token"})
				return
			}
		}
		if sess.MustChange && !strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
			writeJSON(w, http.StatusForbidden, apiError{Error: "password change required"})
			return
		}
		if role == auth.RoleAdmin && sess.Role != auth.RoleAdmin {
			writeJSON(w, http.StatusForbidden, apiError{Error: "administrator role required"})
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

func (s *Server) authenticate(r *http.Request) *auth.Session {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if sess, ok := s.opt.Auth.AuthToken(strings.TrimPrefix(h, "Bearer ")); ok {
			sess.Remote = remoteIP(r)
			return sess
		}
		return nil
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil
	}
	sess, ok := s.opt.Auth.Session(c.Value)
	if !ok {
		return nil
	}
	sess.ID = c.Value
	return sess
}

// ServeHTTP adds security headers and the management-subnet check.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.TLS != nil {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	if !s.allowedRemote(r) {
		http.Error(w, "forbidden: source address not in allowed management subnets", http.StatusForbidden)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) allowedRemote(r *http.Request) bool {
	if session(r) != nil { // local socket
		return true
	}
	subnets := s.opt.Engine.Running().Management.AllowedSubnets
	if len(subnets) == 0 {
		return true
	}
	ip, err := netip.ParseAddr(remoteIP(r))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return true
	}
	for _, sn := range subnets {
		if p, err := netip.ParsePrefix(sn); err == nil && p.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) user(r *http.Request) string {
	if sess := session(r); sess != nil {
		return sess.User
	}
	return "?"
}

func (s *Server) audit(r *http.Request, action, detail string, err error) {
	e := commit.AuditEntry{User: s.user(r), Remote: remoteIP(r), Action: action, Detail: detail, OK: err == nil}
	if err != nil {
		if e.Detail != "" {
			e.Detail += ": "
		}
		e.Detail += err.Error()
	}
	s.opt.Engine.Audit(e)
}

func (s *Server) routes() {
	m := s.handle
	ro, rw := auth.RoleReadOnly, auth.RoleAdmin

	// auth
	m("POST /api/v1/auth/login", "", s.login)
	m("POST /api/v1/auth/logout", ro, s.logout)
	m("GET /api/v1/auth/me", ro, s.me)
	m("POST /api/v1/auth/password", ro, s.changePassword)

	// status
	m("GET /api/v1/system", ro, s.getSystem)
	m("GET /api/v1/interfaces", ro, s.getInterfaces)
	m("PUT /api/v1/interfaces/assignment", rw, s.putAssignment)
	m("GET /api/v1/ports", ro, s.getPorts)
	m("GET /api/v1/ports/{name}", ro, s.getPort)
	m("PATCH /api/v1/ports/{name}", rw, s.patchPort)
	m("PATCH /api/v1/ports", rw, s.bulkPorts)
	m("GET /api/v1/ports/{name}/stats", ro, s.portStats)
	m("POST /api/v1/ports/{name}/reset-counters", rw, s.resetCounters)
	m("POST /api/v1/ports/reset-counters", rw, s.resetCounters)
	m("GET /api/v1/stats/total", ro, s.totalStats)

	m("GET /api/v1/vlans", ro, s.getVLANs)
	m("POST /api/v1/vlans", rw, s.createVLAN)
	m("PATCH /api/v1/vlans/{id}", rw, s.patchVLAN)
	m("DELETE /api/v1/vlans/{id}", rw, s.deleteVLAN)
	m("PATCH /api/v1/vlans/matrix", rw, s.patchMatrix)

	m("GET /api/v1/mac-table", ro, s.getMACTable)
	m("POST /api/v1/mac-table/flush", rw, s.flushMACs)
	m("GET /api/v1/static-macs", ro, s.getStaticMACs)
	m("POST /api/v1/static-macs", rw, s.addStaticMAC)
	m("DELETE /api/v1/static-macs/{vlan}/{mac}", rw, s.deleteStaticMAC)

	m("GET /api/v1/stp", ro, s.getSTP)
	m("PATCH /api/v1/bridge", rw, s.patchBridge)
	m("PATCH /api/v1/system/settings", rw, s.patchSystem)
	m("PATCH /api/v1/management", rw, s.patchManagement)

	// config lifecycle
	m("GET /api/v1/config/{which}", ro, s.getConfig)
	m("PUT /api/v1/config/candidate", rw, s.putCandidate)
	m("DELETE /api/v1/config/candidate", rw, s.discardCandidate)
	m("GET /api/v1/config/diff", ro, s.getDiff)
	m("GET /api/v1/config/compare", ro, s.compareConfigs)
	m("GET /api/v1/config/history", ro, s.getHistory)
	m("GET /api/v1/config/history/{id}", ro, s.getHistoryVersion)
	m("POST /api/v1/config/rollback/{id}", rw, s.rollback)
	m("POST /api/v1/config/save", rw, s.save)
	m("GET /api/v1/config/export", rw, s.export)
	m("POST /api/v1/config/import", rw, s.importConfig)

	m("GET /api/v1/commit", ro, s.getCommit)
	m("POST /api/v1/commit", rw, s.doCommit)
	m("POST /api/v1/commit/confirm", rw, s.confirm)
	m("POST /api/v1/commit/cancel", rw, s.cancel)
	m("POST /api/v1/commit/extend", rw, s.extend)

	// users & tokens
	m("GET /api/v1/users", rw, s.getUsers)
	m("POST /api/v1/users", rw, s.addUser)
	m("PATCH /api/v1/users/{name}", rw, s.patchUser)
	m("DELETE /api/v1/users/{name}", rw, s.deleteUser)
	m("GET /api/v1/tokens", ro, s.getTokens)
	m("POST /api/v1/tokens", ro, s.createToken)
	m("DELETE /api/v1/tokens/{id}", ro, s.deleteToken)
	m("GET /api/v1/sessions", rw, s.getSessions)

	m("GET /api/v1/tls", ro, s.getTLS)
	m("POST /api/v1/tls", rw, s.putTLS)

	m("GET /api/v1/audit", ro, s.getAudit)
	m("GET /api/v1/logs", ro, s.getLogs)
	m("GET /api/v1/events", ro, s.events)

	m("POST /api/v1/diag/ping", rw, s.ping)
	m("POST /api/v1/diag/traceroute", rw, s.traceroute)
	m("POST /api/v1/diag/capture", rw, s.startCapture)
	m("GET /api/v1/diag/capture/{id}", rw, s.getCapture)

	m("POST /api/v1/system/restart", rw, s.restartDaemon)
	m("POST /api/v1/system/reboot", rw, s.reboot)
	m("POST /api/v1/system/shutdown", rw, s.shutdown)

	s.phase2Routes()
	s.consoleRoutes()

	m("GET /api/v1/capabilities", ro, s.capabilities)
	m("GET /api/v1/version", "", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"version": version.Version})
	})
	if s.opt.Sim != nil {
		m("POST /api/v1/sim/link", rw, s.simLink)
		m("POST /api/v1/sim/learn", rw, s.simLearn)
	}
	m("/api/", "", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, apiError{Error: "no such endpoint"})
	})
	s.mux.Handle("/", s.ui())
}

// ui serves the SPA with index.html fallback.
func (s *Server) ui() http.Handler {
	if s.opt.UI == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "bsdswitch "+version.Version+": web UI not built into this binary (run `make web`). The REST API is at /api/v1/.\n")
		})
	}
	files := http.FileServer(http.FS(s.opt.UI))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(s.opt.UI, p); err != nil {
			// SPA route: serve index.html
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r2)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// RedirectHandler redirects HTTP to HTTPS.
func RedirectHandler(httpsPort func() int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if p := httpsPort(); p != 443 {
			host = fmt.Sprintf("%s:%d", host, p)
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
}

func nowMs() int64 { return time.Now().UnixMilli() }
