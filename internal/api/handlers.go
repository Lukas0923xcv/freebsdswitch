package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/auth"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/commit"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/state"
	"github.com/Lukas0923xcv/freebsdswitch/internal/textdiff"
	"github.com/Lukas0923xcv/freebsdswitch/internal/version"
)

func versionInfo() (string, string) { return version.Version, version.Commit }

// ---- auth ------------------------------------------------------------------

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type meResp struct {
	User       string `json:"user"`
	Role       string `json:"role"`
	CSRF       string `json:"csrf,omitempty"`
	MustChange bool   `json:"must_change"`
	Timeout    int    `json:"session_timeout"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	sess, err := s.opt.Auth.Login(req.Username, req.Password, remoteIP(r))
	s.opt.Engine.Audit(commit.AuditEntry{User: req.Username, Remote: remoteIP(r), Action: "login", OK: err == nil, Detail: errString(err)})
	if err != nil {
		code := http.StatusUnauthorized
		if errors.Is(err, auth.ErrRateLimited) {
			code = http.StatusTooManyRequests
		}
		writeErr(w, code, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: sess.ID, Path: "/", HttpOnly: true,
		Secure: !s.opt.InsecureCookies, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, meResp{User: sess.User, Role: sess.Role, CSRF: sess.CSRF, MustChange: sess.MustChange,
		Timeout: s.opt.Engine.Running().Management.SessionTimeout})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if sess := session(r); sess != nil && sess.ID != "" {
		s.opt.Auth.Logout(sess.ID)
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: !s.opt.InsecureCookies, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	sess := session(r)
	writeJSON(w, 200, meResp{User: sess.User, Role: sess.Role, CSRF: sess.CSRF, MustChange: sess.MustChange,
		Timeout: s.opt.Engine.Running().Management.SessionTimeout})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	sess := session(r)
	if sess.ViaToken {
		badRequest(w, "password changes require an interactive session")
		return
	}
	err := s.opt.Auth.ChangeOwnPassword(sess.User, req.Old, req.New)
	s.audit(r, "password-change", "", err)
	if err != nil {
		if errors.Is(err, auth.ErrBadCredentials) {
			writeErr(w, 403, errors.New("current password is wrong"))
			return
		}
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- status ------------------------------------------------------------------

func (s *Server) getSystem(w http.ResponseWriter, r *http.Request) {
	v, err := s.systemView(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) getInterfaces(w http.ResponseWriter, r *http.Request) {
	v, err := s.interfaceViews(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, v)
}

type assignment struct {
	Management string   `json:"management"`
	Ports      []string `json:"ports"`
	Unused     []string `json:"unused"`
}

func (s *Server) putAssignment(w http.ResponseWriter, r *http.Request) {
	var a assignment
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err)
		return
	}
	c, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		if a.Management == "" {
			return errors.New("a management interface is required")
		}
		c.Management.Interface = a.Management
		var ports []config.Port
		for _, name := range a.Ports {
			if p := c.Port(name); p != nil {
				ports = append(ports, *p)
			} else {
				ports = append(ports, config.Port{Name: name, Mode: config.ModeAccess, PVID: config.DefaultVID})
			}
		}
		// keep LAG ports
		for _, l := range c.LAGs {
			if p := c.Port(l.Name); p != nil {
				ports = append(ports, *p)
			}
		}
		c.Ports = ports
		c.Unused = a.Unused
		// static MACs on removed ports are dropped
		var macs []config.StaticMAC
		for _, m := range c.StaticMACs {
			for _, p := range ports {
				if p.Name == m.Port {
					macs = append(macs, m)
				}
			}
		}
		c.StaticMACs = macs
		c.ApplyDefaults()
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) getPorts(w http.ResponseWriter, r *http.Request) {
	v, err := s.portViews(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) getPort(w http.ResponseWriter, r *http.Request) {
	v, err := s.portViews(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	for _, p := range v {
		if p.Name == r.PathValue("name") {
			cand, _ := s.opt.Engine.Candidate()
			writeJSON(w, 200, map[string]any{"status": p, "config": cand.Port(p.Name)})
			return
		}
	}
	writeErr(w, 404, fmt.Errorf("no such port"))
}

// portPatch is a partial port update; nil fields are unchanged.
type portPatch struct {
	Description *string        `json:"description"`
	Shutdown    *bool          `json:"shutdown"`
	Speed       *string        `json:"speed"`
	Duplex      *string        `json:"duplex"`
	Mode        *string        `json:"mode"`
	PVID        *int           `json:"pvid"`
	Native      *int           `json:"native"`
	Tagged      *config.VIDSet `json:"tagged"`
	MaxAddr     *int           `json:"max_addresses"`
	Isolated    *bool          `json:"isolated"`
	STP         *struct {
		Enabled  *bool `json:"enabled"`
		Edge     *bool `json:"edge"`
		AutoEdge *bool `json:"auto_edge"`
		Priority *int  `json:"priority"`
		PathCost *int  `json:"path_cost"`
	} `json:"stp"`
}

func (pp *portPatch) apply(p *config.Port) {
	if pp.Description != nil {
		p.Description = strings.TrimSpace(*pp.Description)
	}
	if pp.Shutdown != nil {
		p.Shutdown = *pp.Shutdown
	}
	if pp.Speed != nil && *pp.Speed != NA {
		p.Speed = *pp.Speed
	}
	if pp.Duplex != nil && *pp.Duplex != NA {
		p.Duplex = *pp.Duplex
	}
	if pp.Mode != nil && *pp.Mode != p.Mode {
		switch *pp.Mode {
		case config.ModeTrunk:
			p.Native = p.PVID
			p.PVID = 0
		case config.ModeAccess:
			p.PVID = p.Native
			if p.PVID == 0 {
				p.PVID = config.DefaultVID
			}
			p.Native = 0
			p.Tagged = config.VIDSet{}
		}
		p.Mode = *pp.Mode
	}
	if pp.PVID != nil {
		p.PVID = *pp.PVID
	}
	if pp.Native != nil {
		p.Native = *pp.Native
	}
	if pp.Tagged != nil {
		p.Tagged = pp.Tagged.Normalize()
	}
	if pp.MaxAddr != nil {
		p.MaxAddr = *pp.MaxAddr
	}
	if pp.Isolated != nil {
		p.Private = *pp.Isolated
	}
	if st := pp.STP; st != nil {
		if st.Enabled != nil {
			v := *st.Enabled
			p.STP.Enabled = &v
		}
		if st.Edge != nil {
			p.STP.Edge = *st.Edge
		}
		if st.AutoEdge != nil {
			v := *st.AutoEdge
			p.STP.AutoEdge = &v
		}
		if st.Priority != nil {
			p.STP.Priority = *st.Priority
		}
		if st.PathCost != nil {
			p.STP.PathCost = *st.PathCost
		}
	}
}

func (s *Server) patchPort(w http.ResponseWriter, r *http.Request) {
	var pp portPatch
	if err := decode(r, &pp); err != nil {
		writeErr(w, 400, err)
		return
	}
	name := r.PathValue("name")
	c, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		p := c.Port(name)
		if p == nil {
			return fmt.Errorf("%s is not a switch port", name)
		}
		pp.apply(p)
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, c.Port(name))
}

func (s *Server) bulkPorts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string  `json:"names"`
		Set   portPatch `json:"set"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if len(req.Names) == 0 {
		badRequest(w, "no ports selected")
		return
	}
	_, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		for _, n := range req.Names {
			p := c.Port(n)
			if p == nil {
				return fmt.Errorf("%s is not a switch port", n)
			}
			req.Set.apply(p)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "ports": req.Names})
}

func sinceParam(r *http.Request) int64 {
	if v, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64); err == nil {
		return v
	}
	return 0
}

func (s *Server) portStats(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	c, _ := s.opt.Stats.Counters(name)
	writeJSON(w, 200, map[string]any{"counters": c, "history": s.opt.Stats.History(name, sinceParam(r))})
}

func (s *Server) totalStats(w http.ResponseWriter, r *http.Request) {
	run := s.opt.Engine.Running()
	var names []string
	for _, p := range run.Ports {
		names = append(names, p.Name)
	}
	writeJSON(w, 200, s.opt.Stats.Total(names, sinceParam(r)))
}

func (s *Server) resetCounters(w http.ResponseWriter, r *http.Request) {
	names := []string{}
	if n := r.PathValue("name"); n != "" {
		names = append(names, n)
	} else {
		for _, p := range s.opt.Engine.Running().Ports {
			names = append(names, p.Name)
		}
	}
	s.opt.Stats.Reset(names...)
	s.audit(r, "reset-counters", strings.Join(names, ","), nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- VLANs -------------------------------------------------------------------

type VLANView struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Untagged []string `json:"untagged"`
	Tagged   []string `json:"tagged"`
	Mgmt     bool     `json:"management"`
}

func vlanViews(c *config.Config) []VLANView {
	out := []VLANView{}
	for _, v := range c.VLANs {
		vv := VLANView{ID: v.ID, Name: v.Name, Untagged: []string{}, Tagged: []string{},
			Mgmt: c.Management.InBand.Enabled && c.Management.InBand.VLAN == v.ID}
		for _, p := range c.Ports {
			switch p.Membership(v.ID) {
			case config.MemberUntagged:
				vv.Untagged = append(vv.Untagged, p.Name)
			case config.MemberTagged:
				vv.Tagged = append(vv.Tagged, p.Name)
			}
		}
		out = append(out, vv)
	}
	return out
}

func (s *Server) getVLANs(w http.ResponseWriter, r *http.Request) {
	c := s.opt.Engine.Running()
	if r.URL.Query().Get("source") == "candidate" {
		c, _ = s.opt.Engine.Candidate()
	}
	writeJSON(w, 200, vlanViews(c))
}

func (s *Server) createVLAN(w http.ResponseWriter, r *http.Request) {
	var v config.VLAN
	if err := decode(r, &v); err != nil {
		writeErr(w, 400, err)
		return
	}
	_, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		if c.VLAN(v.ID) != nil {
			return fmt.Errorf("VLAN %d already exists", v.ID)
		}
		c.VLANs = append(c.VLANs, v)
		c.Sort()
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 201, v)
}

func vidParam(r *http.Request) (int, error) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < config.MinVID || id > config.MaxVID {
		return 0, fmt.Errorf("invalid VLAN ID")
	}
	return id, nil
}

func (s *Server) patchVLAN(w http.ResponseWriter, r *http.Request) {
	id, err := vidParam(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	_, err = s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		v := c.VLAN(id)
		if v == nil {
			return fmt.Errorf("no such VLAN")
		}
		v.Name = req.Name
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "name": req.Name})
}

func (s *Server) deleteVLAN(w http.ResponseWriter, r *http.Request) {
	id, err := vidParam(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if id == config.DefaultVID {
		badRequest(w, "VLAN 1 is the default VLAN and cannot be deleted")
		return
	}
	var moved []string
	_, err = s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		if c.VLAN(id) == nil {
			return fmt.Errorf("no such VLAN")
		}
		if c.Management.InBand.Enabled && c.Management.InBand.VLAN == id {
			return fmt.Errorf("VLAN %d is the in-band management VLAN; disable in-band management first", id)
		}
		var keep []config.VLAN
		for _, v := range c.VLANs {
			if v.ID != id {
				keep = append(keep, v)
			}
		}
		c.VLANs = keep
		for i := range c.Ports {
			p := &c.Ports[i]
			if p.Mode == config.ModeAccess && p.PVID == id {
				moved = append(moved, p.Name)
			}
			p.SetMembership(id, config.MemberNone)
			if p.Mode == config.ModeTrunk && p.Native == 0 && len(p.Tagged) == 0 {
				p.Mode, p.PVID = config.ModeAccess, config.DefaultVID
			}
		}
		var macs []config.StaticMAC
		for _, m := range c.StaticMACs {
			if m.VLAN != id {
				macs = append(macs, m)
			}
		}
		c.StaticMACs = macs
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "moved_to_vlan1": moved})
}

type matrixChange struct {
	Port       string `json:"port"`
	VLAN       int    `json:"vlan"`
	Membership string `json:"membership"` // U, T, ""
}

func (s *Server) patchMatrix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Changes []matrixChange `json:"changes"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	c, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		for _, ch := range req.Changes {
			p := c.Port(ch.Port)
			if p == nil {
				return fmt.Errorf("%s is not a switch port", ch.Port)
			}
			if c.VLAN(ch.VLAN) == nil {
				return fmt.Errorf("VLAN %d is not defined", ch.VLAN)
			}
			switch ch.Membership {
			case config.MemberUntagged, config.MemberTagged, config.MemberNone:
			default:
				return fmt.Errorf("membership must be U, T or empty")
			}
			p.SetMembership(ch.VLAN, ch.Membership)
		}
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, vlanViews(c))
}

// ---- MAC table -----------------------------------------------------------------

func (s *Server) getMACTable(w http.ResponseWriter, r *http.Request) {
	run := s.opt.Engine.Running()
	entries, err := s.opt.Backend.MACTable(r.Context(), run.Bridge.Name)
	if err != nil {
		entries = []state.MACEntry{}
	}
	q := r.URL.Query()
	vlan, _ := strconv.Atoi(q.Get("vlan"))
	port := q.Get("port")
	mac := strings.ToLower(q.Get("mac"))
	out := []state.MACEntry{}
	for _, e := range entries {
		if vlan != 0 && e.VLAN != vlan || port != "" && e.Port != port || mac != "" && !strings.Contains(e.MAC, mac) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].VLAN != out[j].VLAN {
			return out[i].VLAN < out[j].VLAN
		}
		return out[i].MAC < out[j].MAC
	})
	writeJSON(w, 200, map[string]any{"entries": out, "aging": run.Bridge.MACAging, "max": run.Bridge.MaxAddresses, "total": len(entries)})
}

func (s *Server) flushMACs(w http.ResponseWriter, r *http.Request) {
	run := s.opt.Engine.Running()
	var req struct {
		All bool `json:"all"`
	}
	decode(r, &req)
	verb := "flush"
	if req.All {
		// flushall would also drop configured static entries; they are
		// restored on the next commit. We only allow dynamic flush here.
		verb = "flush"
	}
	_, err := backend.Execute(r.Context(), s.opt.Backend, backend.Exec("ifconfig", run.Bridge.Name, verb))
	s.audit(r, "mac-flush", "", err)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) getStaticMACs(w http.ResponseWriter, r *http.Request) {
	c, _ := s.opt.Engine.Candidate()
	if c.StaticMACs == nil {
		c.StaticMACs = []config.StaticMAC{}
	}
	writeJSON(w, 200, c.StaticMACs)
}

func (s *Server) addStaticMAC(w http.ResponseWriter, r *http.Request) {
	var m config.StaticMAC
	if err := decode(r, &m); err != nil {
		writeErr(w, 400, err)
		return
	}
	m.MAC = strings.ToLower(strings.ReplaceAll(m.MAC, "-", ":"))
	_, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		c.StaticMACs = append(c.StaticMACs, m)
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 201, m)
}

func (s *Server) deleteStaticMAC(w http.ResponseWriter, r *http.Request) {
	vlan, _ := strconv.Atoi(r.PathValue("vlan"))
	mac := strings.ToLower(r.PathValue("mac"))
	_, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		var keep []config.StaticMAC
		found := false
		for _, m := range c.StaticMACs {
			if m.MAC == mac && m.VLAN == vlan {
				found = true
				continue
			}
			keep = append(keep, m)
		}
		if !found {
			return fmt.Errorf("no such static entry")
		}
		c.StaticMACs = keep
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- STP / bridge / system / management ------------------------------------------

func (s *Server) getSTP(w http.ResponseWriter, r *http.Request) {
	run := s.opt.Engine.Running()
	snap, err := s.opt.Backend.Snapshot(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	resp := map[string]any{"config": run.Bridge.STP, "bridge": nil, "ports": []any{}}
	if b := snap.Get(run.Bridge.Name); b != nil && b.Bridge != nil {
		br := b.Bridge
		resp["bridge"] = map[string]any{
			"id": br.ID, "priority": br.Priority, "proto": br.Proto, "hello": br.HelloTime, "max_age": br.MaxAge,
			"fwd_delay": br.FwdDelay, "hold_count": br.HoldCount, "root_id": br.RootID, "root_priority": br.RootPriority,
			"root_cost": br.RootCost, "root_port": br.RootPort, "is_root": br.IsRoot(),
		}
		var ports []map[string]any
		for _, m := range br.Members {
			pc := run.Port(m.Name)
			if pc == nil {
				continue
			}
			rootPort := br.RootPort != 0 && m.PortNo == br.RootPort
			ports = append(ports, map[string]any{
				"name": m.Name, "port_no": m.PortNo, "stp": m.Has("STP"), "role": m.Role, "state": m.State,
				"priority": m.Priority, "path_cost": m.PathCost, "admin_cost": pc.STP.PathCost,
				"edge": pc.STP.Edge, "auto_edge": config.Bool(pc.STP.AutoEdge, true), "oper_edge": m.Has("EDGE"),
				"ptp": m.Has("PTP"), "proto": m.Proto, "is_root_port": rootPort,
			})
		}
		if ports == nil {
			ports = []map[string]any{}
		}
		resp["ports"] = ports
	}
	writeJSON(w, 200, resp)
}

// patchSection decodes a JSON object onto a copy of a config section.
func patchSection[T any](r *http.Request, dst *T) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func (s *Server) patchBridge(w http.ResponseWriter, r *http.Request) {
	c, _ := s.opt.Engine.Candidate()
	b := c.Bridge
	if err := patchSection(r, &b); err != nil {
		writeErr(w, 400, err)
		return
	}
	nc, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		b.Name = c.Bridge.Name // renaming the bridge is not supported via the API
		c.Bridge = b
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, nc.Bridge)
}

func (s *Server) patchSystem(w http.ResponseWriter, r *http.Request) {
	c, _ := s.opt.Engine.Candidate()
	sys := c.System
	if err := patchSection(r, &sys); err != nil {
		writeErr(w, 400, err)
		return
	}
	nc, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		c.System = sys
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, nc.System)
}

func (s *Server) patchManagement(w http.ResponseWriter, r *http.Request) {
	c, _ := s.opt.Engine.Candidate()
	m := c.Management
	if err := patchSection(r, &m); err != nil {
		writeErr(w, 400, err)
		return
	}
	nc, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		if m.Interface != c.Management.Interface {
			// moving management: remove it from switch ports
			var ports []config.Port
			for _, p := range c.Ports {
				if p.Name != m.Interface {
					ports = append(ports, p)
				}
			}
			c.Ports = ports
		}
		c.Management = m
		c.ApplyDefaults()
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, nc.Management)
}

// ---- config lifecycle ---------------------------------------------------------------

func wantYAML(r *http.Request) bool {
	return r.URL.Query().Get("format") == "yaml" || strings.Contains(r.Header.Get("Accept"), "yaml")
}

func writeConfig(w http.ResponseWriter, r *http.Request, c *config.Config) {
	if wantYAML(r) {
		b, err := c.Marshal()
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("which") {
	case "running":
		writeConfig(w, r, s.opt.Engine.Running())
	case "startup":
		c, err := s.opt.Engine.Startup()
		if err != nil {
			writeErr(w, 404, fmt.Errorf("no startup configuration saved"))
			return
		}
		writeConfig(w, r, c)
	case "candidate":
		c, staged := s.opt.Engine.Candidate()
		w.Header().Set("X-Staged", strconv.FormatBool(staged))
		writeConfig(w, r, c)
	default:
		writeErr(w, 404, fmt.Errorf("unknown configuration %q", r.PathValue("which")))
	}
}

func readConfigBody(r *http.Request) (*config.Config, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return nil, err
	}
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "json") {
		var c config.Config
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		c.ApplyDefaults()
		return &c, nil
	}
	return config.Parse(b)
}

func (s *Server) putCandidate(w http.ResponseWriter, r *http.Request) {
	c, err := readConfigBody(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.opt.Engine.SetCandidate(r.Context(), c, s.user(r)); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) discardCandidate(w http.ResponseWriter, r *http.Request) {
	s.opt.Engine.DiscardCandidate(s.user(r))
	s.audit(r, "discard", "", nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) getDiff(w http.ResponseWriter, r *http.Request) {
	pv, err := s.opt.Engine.Preview(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, pv)
}

func (s *Server) configByName(name string) (*config.Config, error) {
	switch name {
	case "running":
		return s.opt.Engine.Running(), nil
	case "startup":
		return s.opt.Engine.Startup()
	case "candidate":
		c, _ := s.opt.Engine.Candidate()
		return c, nil
	}
	id, err := strconv.Atoi(name)
	if err != nil {
		return nil, fmt.Errorf("unknown configuration %q", name)
	}
	return s.opt.Engine.HistoryConfig(id)
}

func (s *Server) compareConfigs(w http.ResponseWriter, r *http.Request) {
	an, bn := r.URL.Query().Get("a"), r.URL.Query().Get("b")
	if an == "" {
		an = "startup"
	}
	if bn == "" {
		bn = "running"
	}
	a, err := s.configByName(an)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	b, err := s.configByName(bn)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	ay, _ := a.Marshal()
	by, _ := b.Marshal()
	writeJSON(w, 200, map[string]any{"a": an, "b": bn, "diff": textdiff.Unified(an, bn, string(ay), string(by), 3)})
}

func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.opt.Engine.History())
}

func (s *Server) getHistoryVersion(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		badRequest(w, "invalid version")
		return
	}
	c, err := s.opt.Engine.HistoryConfig(id)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeConfig(w, r, c)
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		badRequest(w, "invalid version")
		return
	}
	err = s.opt.Engine.LoadRollback(r.Context(), id, s.user(r))
	s.audit(r, "rollback-load", fmt.Sprintf("version %d staged", id), err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "staged": id})
}

func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	err := s.opt.Engine.Save(r.Context(), s.user(r))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	which := r.URL.Query().Get("which")
	if which == "" {
		which = "running"
	}
	c, err := s.configByName(which)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	b, _ := c.Marshal()
	name := fmt.Sprintf("bsdswitch-%s-%s-%s.yaml", c.System.Hostname, which, time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	s.audit(r, "export", which, nil)
	w.Write(b)
}

func (s *Server) importConfig(w http.ResponseWriter, r *http.Request) {
	c, err := readConfigBody(r)
	if err == nil {
		err = s.opt.Engine.SetCandidate(r.Context(), c, s.user(r))
	}
	s.audit(r, "import", "configuration staged", err)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) getCommit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.configStatus())
}

func (s *Server) doCommit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Timeout int    `json:"timeout"`
		Comment string `json:"comment"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &req); err != nil {
			writeErr(w, 400, err)
			return
		}
	}
	p, err := s.opt.Engine.Commit(r.Context(), s.user(r), req.Comment, time.Duration(req.Timeout)*time.Second)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, PendingView{ID: p.ID, User: p.User, Comment: p.Comment, DeadlineMs: p.Deadline.UnixMilli(), StartedMs: p.Started.UnixMilli(), NowMs: nowMs()})
}

func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Engine.Confirm(s.user(r)); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if err := s.opt.Engine.Cancel(r.Context(), s.user(r)); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) extend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Timeout int `json:"timeout"`
	}
	decode(r, &req)
	if req.Timeout == 0 {
		req.Timeout = 60
	}
	p, err := s.opt.Engine.Extend(s.user(r), time.Duration(req.Timeout)*time.Second)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, PendingView{ID: p.ID, DeadlineMs: p.Deadline.UnixMilli(), NowMs: nowMs()})
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, Capabilities)
}

func (s *Server) simLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port string `json:"port"`
		Up   bool   `json:"up"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.opt.Sim.SetLink(req.Port, req.Up)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) simLearn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MAC  string `json:"mac"`
		VLAN int    `json:"vlan"`
		Port string `json:"port"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s.opt.Sim.Learn(s.opt.Engine.Running().Bridge.Name, req.MAC, req.VLAN, req.Port)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
