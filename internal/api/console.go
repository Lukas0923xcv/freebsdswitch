package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
)

// Console-only endpoints. They are reachable exclusively through the local
// unix socket (root on the console), never over the network, and apply
// changes immediately without commit-confirm: the operator is physically
// at the box and these are the recovery tools.

// FactoryFunc builds the factory default configuration.
type FactoryFunc func(ctx context.Context) (*config.Config, error)

func (s *Server) consoleOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := session(r)
		if sess == nil || sess.Remote != "local" || sess.User != "console" {
			writeJSON(w, http.StatusForbidden, apiError{Error: "only available on the local console"})
			return
		}
		h(w, r)
	}
}

func (s *Server) consoleRoutes() {
	s.handle("POST /api/v1/console/apply", "admin", s.consoleOnly(s.consoleApply))
	s.handle("POST /api/v1/console/apply-version/{id}", "admin", s.consoleOnly(s.consoleApplyVersion))
	s.handle("POST /api/v1/console/reset-password", "admin", s.consoleOnly(s.consoleResetPassword))
	s.handle("POST /api/v1/console/factory-reset", "admin", s.consoleOnly(s.consoleFactoryReset))
	s.handle("GET /api/v1/console/initial-password", "admin", s.consoleOnly(s.consoleInitialPassword))
}

func (s *Server) consoleApply(w http.ResponseWriter, r *http.Request) {
	c, err := readConfigBody(r)
	if err == nil {
		err = s.opt.Engine.ApplyNow(r.Context(), c, "console", "console apply")
	}
	if err == nil && r.URL.Query().Get("save") == "1" {
		err = s.opt.Engine.Save(r.Context(), "console")
	}
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) consoleApplyVersion(w http.ResponseWriter, r *http.Request) {
	var id int
	fmt.Sscanf(r.PathValue("id"), "%d", &id)
	c, err := s.opt.Engine.HistoryConfig(id)
	if err == nil {
		err = s.opt.Engine.ApplyNow(r.Context(), c, "console", fmt.Sprintf("console rollback to version %d", id))
	}
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) consoleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User string `json:"user"`
	}
	decode(r, &req)
	if req.User == "" {
		req.User = "admin"
	}
	pw, err := s.opt.Auth.ResetPassword(req.User)
	s.audit(r, "password-reset", req.User+" (console)", err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	os.WriteFile(filepath.Join(s.opt.StateDir, "initial-admin-password"), []byte(pw+"\n"), 0o600)
	writeJSON(w, 200, map[string]string{"user": req.User, "password": pw})
}

func (s *Server) consoleFactoryReset(w http.ResponseWriter, r *http.Request) {
	if s.opt.Factory == nil {
		writeErr(w, 500, fmt.Errorf("factory defaults unavailable"))
		return
	}
	c, err := s.opt.Factory(r.Context())
	if err == nil {
		err = s.opt.Engine.ApplyNow(r.Context(), c, "console", "factory reset")
	}
	if err == nil {
		err = s.opt.Engine.Save(r.Context(), "console")
	}
	var pw string
	if err == nil {
		// all users are removed; a new admin with a one-time password is created
		for _, u := range s.opt.Auth.Users() {
			if u.Name != "admin" {
				s.opt.Auth.DeleteUser(u.Name)
			}
		}
		for _, t := range s.opt.Auth.Tokens("") {
			s.opt.Auth.DeleteToken(t.ID, "")
		}
		pw, err = s.opt.Auth.ResetPassword("admin")
		os.WriteFile(filepath.Join(s.opt.StateDir, "initial-admin-password"), []byte(pw+"\n"), 0o600)
		os.WriteFile(filepath.Join(s.opt.StateDir, "wizard-pending"), nil, 0o600)
	}
	s.audit(r, "factory-reset", "", err)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]string{"password": pw, "management": c.Management.Interface})
}

func (s *Server) consoleInitialPassword(w http.ResponseWriter, r *http.Request) {
	pw := ""
	for _, u := range s.opt.Auth.Users() {
		if u.Name == "admin" && u.MustChange {
			if b, err := os.ReadFile(filepath.Join(s.opt.StateDir, "initial-admin-password")); err == nil {
				pw = string(b)
			}
		}
	}
	writeJSON(w, 200, map[string]string{"password": pw})
}
