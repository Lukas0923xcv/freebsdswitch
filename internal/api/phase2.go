package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/config"
	"github.com/Lukas0923xcv/freebsdswitch/internal/plan"
	"github.com/Lukas0923xcv/freebsdswitch/internal/version"
)

func (s *Server) phase2Routes() {
	m := s.handle
	ro, rw := "readonly", "admin"
	m("GET /api/v1/lags", ro, s.getLAGs)
	m("POST /api/v1/lags", rw, s.createLAG)
	m("PUT /api/v1/lags/{name}", rw, s.updateLAG)
	m("DELETE /api/v1/lags/{name}", rw, s.deleteLAG)
	m("GET /api/v1/mirrors", ro, s.getMirrors)
	m("PUT /api/v1/mirrors", rw, s.putMirrors)
	m("GET /api/v1/acls", ro, s.getACLs)
	m("PUT /api/v1/acls", rw, s.putACLs)
	m("GET /api/v1/rate-limits", ro, s.getRateLimits)
	m("PUT /api/v1/rate-limits", rw, s.putRateLimits)
	m("GET /api/v1/filters/preview", ro, s.filterPreview)
	m("GET /api/v1/services", ro, s.getServices)
	m("PATCH /api/v1/services", rw, s.patchServices)
	m("GET /api/v1/lldp/neighbors", ro, s.lldpNeighbors)
	m("GET /api/v1/system/update", rw, s.getUpdate)
	m("POST /api/v1/system/update/upload", rw, s.uploadUpdate)
	m("POST /api/v1/system/update/check", rw, s.checkUpdate)
	m("POST /api/v1/system/update/apply", rw, s.applyUpdate)
	m("POST /api/v1/system/update/revert", rw, s.revertUpdate)
}

// ---- LAG -----------------------------------------------------------------------

type LAGView struct {
	config.LAG
	Exists bool              `json:"exists"`
	Up     bool              `json:"up"`
	Proto  string            `json:"oper_proto"`
	Ports  []LAGMemberStatus `json:"ports"`
	LACP   string            `json:"lacp_id"`
	Port   *config.Port      `json:"port"`
}

type LAGMemberStatus struct {
	Name  string   `json:"name"`
	Flags []string `json:"flags"`
	Link  bool     `json:"link"`
	// Active means the member is collecting and distributing (LACP) or
	// the active failover port.
	Active bool `json:"active"`
}

func (s *Server) getLAGs(w http.ResponseWriter, r *http.Request) {
	cand, _ := s.opt.Engine.Candidate()
	snap, err := s.opt.Backend.Snapshot(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	out := []LAGView{}
	for _, l := range cand.LAGs {
		v := LAGView{LAG: l, Ports: []LAGMemberStatus{}, Port: cand.Port(l.Name)}
		if i := snap.Get(l.Name); i != nil && i.Lagg != nil {
			v.Exists, v.Up, v.Proto, v.LACP = true, i.LinkUp(), i.Lagg.Proto, i.Lagg.LACP
			for _, p := range i.Lagg.Ports {
				st := LAGMemberStatus{Name: p.Name, Flags: p.Flags}
				if m := snap.Get(p.Name); m != nil {
					st.Link = m.LinkUp()
				}
				for _, f := range p.Flags {
					if f == "DISTRIBUTING" || f == "ACTIVE" && i.Lagg.Proto == "failover" {
						st.Active = true
					}
				}
				v.Ports = append(v.Ports, st)
			}
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

func nextLagg(c *config.Config) string {
	used := map[string]bool{}
	for _, l := range c.LAGs {
		used[l.Name] = true
	}
	for i := 0; ; i++ {
		n := fmt.Sprintf("lagg%d", i)
		if !used[n] {
			return n
		}
	}
}

// setLAG creates or updates a LAG: members are removed from the switch
// ports; the LAG itself becomes a switch port (inheriting the VLAN
// settings of the first member when new).
func setLAG(c *config.Config, l config.LAG, isNew bool) error {
	if len(l.Members) == 0 {
		return errors.New("select at least one member")
	}
	var first *config.Port
	members := map[string]bool{}
	for _, m := range l.Members {
		members[m] = true
		if first == nil {
			if p := c.Port(m); p != nil {
				cp := *p
				first = &cp
			}
		}
	}
	// previous members of this LAG that are dropped go back to VLAN 1
	var old []string
	for _, x := range c.LAGs {
		if x.Name == l.Name {
			old = x.Members
		}
	}
	var ports []config.Port
	for _, p := range c.Ports {
		if !members[p.Name] {
			ports = append(ports, p)
		}
	}
	for _, o := range old {
		if !members[o] {
			ports = append(ports, config.Port{Name: o, Mode: config.ModeAccess, PVID: config.DefaultVID})
		}
	}
	c.Ports = ports
	if c.Port(l.Name) == nil {
		lp := config.Port{Name: l.Name, Mode: config.ModeAccess, PVID: config.DefaultVID}
		if first != nil {
			lp.Mode, lp.PVID, lp.Native, lp.Tagged, lp.Description = first.Mode, first.PVID, first.Native, first.Tagged, first.Description
		}
		c.Ports = append(c.Ports, lp)
	}
	var lags []config.LAG
	for _, x := range c.LAGs {
		if x.Name != l.Name {
			lags = append(lags, x)
		}
	}
	c.LAGs = append(lags, l)
	var macs []config.StaticMAC
	for _, sm := range c.StaticMACs {
		if !members[sm.Port] {
			macs = append(macs, sm)
		}
	}
	c.StaticMACs = macs
	c.ApplyDefaults()
	return nil
}

func (s *Server) createLAG(w http.ResponseWriter, r *http.Request) {
	var l config.LAG
	if err := decode(r, &l); err != nil {
		writeErr(w, 400, err)
		return
	}
	c, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		if l.Name == "" {
			l.Name = nextLagg(c)
		}
		for _, x := range c.LAGs {
			if x.Name == l.Name {
				return fmt.Errorf("%s already exists", l.Name)
			}
		}
		return setLAG(c, l, true)
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 201, c.Port(l.Name))
}

func (s *Server) updateLAG(w http.ResponseWriter, r *http.Request) {
	var l config.LAG
	if err := decode(r, &l); err != nil {
		writeErr(w, 400, err)
		return
	}
	l.Name = r.PathValue("name")
	_, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		found := false
		for _, x := range c.LAGs {
			found = found || x.Name == l.Name
		}
		if !found {
			return fmt.Errorf("no such LAG")
		}
		return setLAG(c, l, false)
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) deleteLAG(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	_, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		var lags []config.LAG
		var members []string
		for _, x := range c.LAGs {
			if x.Name == name {
				members = x.Members
			} else {
				lags = append(lags, x)
			}
		}
		if members == nil {
			return fmt.Errorf("no such LAG")
		}
		c.LAGs = lags
		lp := c.Port(name)
		var ports []config.Port
		for _, p := range c.Ports {
			if p.Name != name {
				ports = append(ports, p)
			}
		}
		// members become individual ports with the LAG's VLAN settings
		for _, m := range members {
			np := config.Port{Name: m, Mode: config.ModeAccess, PVID: config.DefaultVID}
			if lp != nil {
				np.Mode, np.PVID, np.Native, np.Tagged = lp.Mode, lp.PVID, lp.Native, lp.Tagged
			}
			ports = append(ports, np)
		}
		c.Ports = ports
		var macs []config.StaticMAC
		for _, sm := range c.StaticMACs {
			if sm.Port != name {
				macs = append(macs, sm)
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
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- mirrors / ACLs / rate limits ------------------------------------------------

func (s *Server) getMirrors(w http.ResponseWriter, r *http.Request) {
	c, _ := s.opt.Engine.Candidate()
	writeJSON(w, 200, nonNil(c.Mirrors))
}

func nonNil[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}

func (s *Server) putMirrors(w http.ResponseWriter, r *http.Request) {
	var ms []config.Mirror
	if err := decode(r, &ms); err != nil {
		writeErr(w, 400, err)
		return
	}
	_, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		c.Mirrors = ms
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, ms)
}

func (s *Server) getACLs(w http.ResponseWriter, r *http.Request) {
	c, _ := s.opt.Engine.Candidate()
	writeJSON(w, 200, nonNil(c.ACLs))
}

func (s *Server) putACLs(w http.ResponseWriter, r *http.Request) {
	var acls []config.ACL
	if err := decode(r, &acls); err != nil {
		writeErr(w, 400, err)
		return
	}
	_, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		c.ACLs = acls
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, acls)
}

func (s *Server) getRateLimits(w http.ResponseWriter, r *http.Request) {
	c, _ := s.opt.Engine.Candidate()
	writeJSON(w, 200, nonNil(c.RateLimits))
}

func (s *Server) putRateLimits(w http.ResponseWriter, r *http.Request) {
	var rl []config.RateLimit
	if err := decode(r, &rl); err != nil {
		writeErr(w, 400, err)
		return
	}
	var clean []config.RateLimit
	for _, x := range rl {
		if x.Ingress > 0 || x.Egress > 0 {
			clean = append(clean, x)
		}
	}
	_, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		c.RateLimits = clean
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, nonNil(clean))
}

// filterPreview shows the generated pf ruleset and dummynet commands for
// the candidate configuration.
func (s *Server) filterPreview(w http.ResponseWriter, r *http.Request) {
	c, _ := s.opt.Engine.Candidate()
	var dn []string
	for _, o := range plan.DnctlOps(c) {
		dn = append(dn, o.String())
	}
	writeJSON(w, 200, map[string]any{"pf_conf": plan.PFConf(c), "dnctl": nonNil(dn), "enabled": plan.FilteringEnabled(c),
		"notes": []string{
			"Rules are evaluated by pf on the switch ports: 'in' = traffic entering the switch through the port, 'out' = leaving through it.",
			"Only untagged IP traffic is filtered; 802.1Q-tagged frames on trunk ports pass unfiltered.",
			"Filtering and shaping run on the CPU and reduce forwarding performance.",
		}})
}

// ---- services: SNMP / LLDP / syslog -------------------------------------------------

type servicesBody struct {
	SNMP   *config.SNMP   `json:"snmp"`
	LLDP   *config.LLDP   `json:"lldp"`
	Syslog *config.Syslog `json:"syslog"`
}

func (s *Server) getServices(w http.ResponseWriter, r *http.Request) {
	c, _ := s.opt.Engine.Candidate()
	sys := c.Syslog
	sys.Servers = nonNil(sys.Servers)
	writeJSON(w, 200, map[string]any{"snmp": c.SNMP, "lldp": c.LLDP, "syslog": sys, "lldpd_installed": lldpInstalled()})
}

func lldpInstalled() bool {
	_, err := os.Stat("/usr/local/sbin/lldpd")
	return err == nil
}

func (s *Server) patchServices(w http.ResponseWriter, r *http.Request) {
	var b servicesBody
	if err := decode(r, &b); err != nil {
		writeErr(w, 400, err)
		return
	}
	_, err := s.opt.Engine.Modify(r.Context(), s.user(r), func(c *config.Config) error {
		if b.SNMP != nil {
			c.SNMP = *b.SNMP
		}
		if b.LLDP != nil {
			if b.LLDP.Enabled && !lldpInstalled() && s.opt.Sim == nil {
				return errors.New("lldpd is not installed (pkg install lldpd, or install.sh --with-lldp)")
			}
			c.LLDP = *b.LLDP
		}
		if b.Syslog != nil {
			c.Syslog = *b.Syslog
		}
		return nil
	})
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- LLDP neighbors ----------------------------------------------------------------

// Neighbor is one LLDP neighbor.
type Neighbor struct {
	LocalPort    string   `json:"local_port"`
	ChassisID    string   `json:"chassis_id"`
	SystemName   string   `json:"system_name"`
	SystemDescr  string   `json:"system_descr"`
	PortID       string   `json:"port_id"`
	PortDescr    string   `json:"port_descr"`
	MgmtIP       []string `json:"mgmt_ip"`
	Capabilities []string `json:"capabilities"`
	TTL          string   `json:"ttl"`
	Age          string   `json:"age"`
}

func (s *Server) lldpNeighbors(w http.ResponseWriter, r *http.Request) {
	if !s.opt.Engine.Running().LLDP.Enabled {
		writeJSON(w, 200, []Neighbor{})
		return
	}
	if s.opt.Sim != nil {
		writeJSON(w, 200, []Neighbor{
			{LocalPort: "vtnet4", ChassisID: "52:54:00:aa:bb:cc", SystemName: "core-router", SystemDescr: "FreeBSD 15.1-RELEASE", PortID: "ix0", PortDescr: "to bsdswitch", MgmtIP: []string{"192.168.1.1"}, Capabilities: []string{"Router"}, TTL: "120", Age: "0 day, 00:03:12"},
			{LocalPort: "vtnet1", ChassisID: "3c:22:fb:11:22:33", SystemName: "office-printer", PortID: "3c:22:fb:11:22:33", Capabilities: []string{"Station"}, TTL: "120", Age: "0 day, 01:10:00"},
		})
		return
	}
	out, err := s.opt.Backend.Run(r.Context(), backend.Exec("lldpcli", "-f", "json", "show", "neighbors", "details"))
	if err != nil {
		writeErr(w, 500, fmt.Errorf("lldpcli: %w", err))
		return
	}
	n, err := ParseLLDP([]byte(out))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, n)
}

// ParseLLDP parses `lldpcli -f json show neighbors details`. lldpcli
// emits objects instead of single-element arrays, so everything is
// normalised to lists first.
func ParseLLDP(b []byte) ([]Neighbor, error) {
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("lldpcli output: %w", err)
	}
	out := []Neighbor{}
	lldp, _ := root["lldp"].(map[string]any)
	for _, ifc := range list(lldp["interface"]) {
		im, _ := ifc.(map[string]any)
		for local, v := range im {
			d, _ := v.(map[string]any)
			n := Neighbor{LocalPort: local, Age: str(d["age"]), MgmtIP: []string{}, Capabilities: []string{}}
			if ch, ok := d["chassis"].(map[string]any); ok {
				for name, cv := range ch {
					cm, _ := cv.(map[string]any)
					if cm == nil {
						continue
					}
					if _, hasID := cm["id"]; hasID {
						n.SystemName = name
					} else {
						cm = ch
					}
					if id, ok := cm["id"].(map[string]any); ok {
						n.ChassisID = str(id["value"])
					}
					n.SystemDescr = str(cm["descr"])
					for _, ip := range list(cm["mgmt-ip"]) {
						n.MgmtIP = append(n.MgmtIP, str(ip))
					}
					for _, c := range list(cm["capability"]) {
						if cmap, ok := c.(map[string]any); ok && cmap["enabled"] == true {
							n.Capabilities = append(n.Capabilities, str(cmap["type"]))
						}
					}
					break
				}
			}
			if p, ok := d["port"].(map[string]any); ok {
				if id, ok := p["id"].(map[string]any); ok {
					n.PortID = str(id["value"])
				}
				n.PortDescr = str(p["descr"])
				n.TTL = str(p["ttl"])
			}
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return config.NaturalLess(out[i].LocalPort, out[j].LocalPort) })
	return out, nil
}

func list(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	default:
		return []any{x}
	}
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case map[string]any:
		if s, ok := x["value"].(string); ok {
			return s
		}
	}
	return fmt.Sprint(v)
}

// ---- in-place update ------------------------------------------------------------------

var pkgNameRe = regexp.MustCompile(`^bsdswitch-[0-9A-Za-z._,\-]+\.pkg$`)

func (s *Server) updateDir() string { return filepath.Join(s.opt.StateDir, "update") }

type updateStatus struct {
	Current     string `json:"current"`
	Uploaded    string `json:"uploaded"`
	BootEnvs    bool   `json:"boot_environments"`
	LastBE      string `json:"last_be"`
	RepoVersion string `json:"repo_version"`
	Message     string `json:"message"`
}

func (s *Server) updateStatus(ctx context.Context) updateStatus {
	st := updateStatus{Current: version.Version}
	if ents, err := os.ReadDir(s.updateDir()); err == nil {
		for _, e := range ents {
			if pkgNameRe.MatchString(e.Name()) {
				st.Uploaded = e.Name()
			}
		}
	}
	if s.opt.Sim != nil {
		st.BootEnvs = true
	} else if _, err := s.opt.Backend.Run(ctx, backend.Exec("bectl", "list", "-H")); err == nil {
		st.BootEnvs = true
	}
	if b, err := os.ReadFile(filepath.Join(s.updateDir(), "last-be")); err == nil {
		st.LastBE = strings.TrimSpace(string(b))
	}
	return st
}

func (s *Server) getUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.updateStatus(r.Context()))
}

func (s *Server) uploadUpdate(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.URL.Query().Get("name"))
	if !pkgNameRe.MatchString(name) {
		badRequest(w, "file name must look like bsdswitch-<version>.pkg")
		return
	}
	os.MkdirAll(s.updateDir(), 0o750)
	// keep only one uploaded package
	if ents, err := os.ReadDir(s.updateDir()); err == nil {
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".pkg") {
				os.Remove(filepath.Join(s.updateDir(), e.Name()))
			}
		}
	}
	f, err := os.OpenFile(filepath.Join(s.updateDir(), name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, 128<<20))
	f.Close()
	if err != nil || n < 1024 {
		os.Remove(f.Name())
		badRequest(w, "upload failed or file too small")
		return
	}
	s.audit(r, "update-upload", fmt.Sprintf("%s (%d bytes)", name, n), nil)
	writeJSON(w, 200, s.updateStatus(r.Context()))
}

func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	st := s.updateStatus(r.Context())
	if s.opt.Sim != nil {
		st.RepoVersion = version.Version
		st.Message = "simulation: repository not queried"
		writeJSON(w, 200, st)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if _, err := s.opt.Backend.Run(ctx, backend.Exec("pkg", "update")); err != nil {
		st.Message = "pkg update failed: " + err.Error()
	}
	if out, err := s.opt.Backend.Run(ctx, backend.Exec("pkg", "rquery", "%v", "bsdswitch")); err == nil {
		st.RepoVersion = strings.TrimSpace(out)
	} else if st.Message == "" {
		st.Message = "bsdswitch is not available in the configured pkg repositories"
	}
	writeJSON(w, 200, st)
}

// applyUpdate installs the uploaded package (or upgrades from the
// repository). On ZFS a boot environment is created first, so the system
// can be booted into the previous version. The daemon restarts itself
// afterwards (forwarding is not interrupted).
func (s *Server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"` // upload | repo
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if s.opt.Engine.PendingCommit() != nil {
		writeErr(w, 409, errors.New("confirm or roll back the pending commit first"))
		return
	}
	st := s.updateStatus(r.Context())
	var op backend.Op
	switch req.Source {
	case "upload":
		if st.Uploaded == "" {
			badRequest(w, "no package uploaded")
			return
		}
		op = backend.Exec("pkg", "add", "-f", filepath.Join("/var/db/bsdswitch/update", st.Uploaded))
		if s.opt.StateDir != "/var/db/bsdswitch" && s.opt.Sim == nil {
			badRequest(w, "updates require the default state directory")
			return
		}
	case "repo":
		op = backend.Exec("pkg", "upgrade", "-y", "bsdswitch")
	default:
		badRequest(w, "source must be upload or repo")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	be := ""
	if st.BootEnvs {
		be = "bsdswitch-" + strings.ReplaceAll(version.Version, "+", ".") + "-" + time.Now().Format("20060102-150405")
		if s.opt.Sim == nil {
			if _, err := s.opt.Backend.Run(ctx, backend.Exec("bectl", "create", be)); err != nil {
				writeErr(w, 500, fmt.Errorf("creating boot environment %s: %w", be, err))
				return
			}
		}
		os.MkdirAll(s.updateDir(), 0o750)
		os.WriteFile(filepath.Join(s.updateDir(), "last-be"), []byte(be+"\n"), 0o640)
	}
	var err error
	if s.opt.Sim == nil {
		_, err = s.opt.Backend.Run(ctx, op)
	}
	s.audit(r, "update", fmt.Sprintf("%s (fallback boot environment %q)", op.String(), be), err)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 202, map[string]string{"boot_environment": be, "message": "Update installed. bsdswitchd restarts now; reload the page in a few seconds."})
	if s.opt.OnRestart != nil && s.opt.Sim == nil {
		go func() { time.Sleep(time.Second); s.opt.OnRestart() }()
	}
}

// revertUpdate activates the pre-update boot environment for the next
// boot (bectl activate -t) and reboots.
func (s *Server) revertUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm bool `json:"confirm"`
	}
	decode(r, &req)
	st := s.updateStatus(r.Context())
	if st.LastBE == "" {
		badRequest(w, "no pre-update boot environment recorded")
		return
	}
	if !req.Confirm {
		badRequest(w, "set confirm=true to reboot into %s", st.LastBE)
		return
	}
	if s.opt.Sim == nil {
		if _, err := s.opt.Backend.Run(r.Context(), backend.Exec("bectl", "activate", "-t", st.LastBE)); err != nil {
			writeErr(w, 500, err)
			return
		}
	}
	s.audit(r, "update-revert", st.LastBE, nil)
	writeJSON(w, 202, map[string]string{"message": "rebooting into " + st.LastBE})
	if s.opt.Sim == nil {
		go func() {
			time.Sleep(time.Second)
			backend.Execute(context.Background(), s.opt.Backend, backend.Exec("shutdown", "-r", "now"))
		}()
	}
}
