# REST API (`/api/v1`)

Everything is JSON unless noted. Two ways to authenticate:

- **Session:** `POST /auth/login` sets an `HttpOnly` cookie and returns a
  `csrf` value. Send it as `X-CSRF-Token` on every non-GET request.
- **Token:** `Authorization: Bearer bsw_…`. Tokens are created under
  System › API tokens or with `POST /tokens`.

Roles: `readonly` may call every `GET`; `admin` may call everything.
Validation errors return `400 {"error":"validation failed","fields":[{"path":"ports[vtnet1].pvid","message":"VLAN 99 is not defined"}]}`.
`409` means a commit is pending (or none is pending, for confirm/cancel).

**Most configuration endpoints only stage changes in the candidate
configuration.** Nothing changes on the switch until `POST /commit`.

| Method & path | Role | Description |
|---|---|---|
| `POST /auth/login` | – | `{username,password}` → `{user,role,csrf,must_change}` |
| `POST /auth/logout` | ro | end the session |
| `GET /auth/me` | ro | current user, CSRF token |
| `POST /auth/password` | ro | `{old_password,new_password}` |
| `GET /system` | ro | host info, versions, management addresses/URLs, config status |
| `GET /interfaces` | ro | detected NICs: driver, MAC, link, capabilities, role |
| `PUT /interfaces/assignment` | admin | `{management, ports[], unused[]}` (staged) |
| `GET /ports` | ro | ports: config + live state + counters + rates |
| `GET /ports/{name}` | ro | one port: status + candidate config |
| `PATCH /ports/{name}` | admin | partial update: description, shutdown, speed, duplex, mode, pvid, native, tagged, stp{…}, max_addresses, isolated |
| `PATCH /ports` | admin | bulk: `{names[], set:{…}}` |
| `GET /ports/{name}/stats?since=` | ro | counters + 1 s history (last hour) |
| `POST /ports/{name}/reset-counters`, `POST /ports/reset-counters` | admin | software counter reset |
| `GET /stats/total?since=` | ro | summed throughput history |
| `GET /vlans?source=running\|candidate` | ro | VLANs with untagged/tagged members |
| `POST /vlans` | admin | `{id,name}` |
| `PATCH /vlans/{id}` | admin | `{name}` |
| `DELETE /vlans/{id}` | admin | access ports move to VLAN 1 |
| `PATCH /vlans/matrix` | admin | `{changes:[{port,vlan,membership:"U"\|"T"\|""}]}` |
| `GET /mac-table?vlan=&port=&mac=` | ro | forwarding table |
| `POST /mac-table/flush` | admin | flush dynamic entries (immediate) |
| `GET/POST /static-macs`, `DELETE /static-macs/{vlan}/{mac}` | ro/admin | static entries |
| `GET /stp` | ro | bridge/root info and per-port role/state |
| `PATCH /bridge` | admin | mac_aging, max_addresses, mtu, stp{…} |
| `PATCH /system/settings` | admin | hostname, domain, timezone, dns, ntp |
| `PATCH /management` | admin | interface, ipv4/ipv6, inband, ssh, allowed_subnets, ports, session_timeout |
| `GET /config/{running\|startup\|candidate}` | ro | add `?format=yaml` for YAML |
| `PUT /config/candidate` | admin | replace the candidate (YAML or JSON body) |
| `DELETE /config/candidate` | admin | discard staged changes |
| `GET /config/diff` | ro | `{changed, diff, plan, commands, warnings, mgmt_impact}` |
| `GET /config/compare?a=&b=` | ro | unified diff between running, startup, candidate or a history id |
| `GET /config/history`, `GET /config/history/{id}` | ro | last 10 versions |
| `POST /config/rollback/{id}` | admin | stage a history version |
| `POST /config/save` | admin | running → startup (+ rc.conf) |
| `GET /config/export?which=` | admin | download YAML |
| `POST /config/import` | admin | YAML body → candidate |
| `GET /commit` | ro | `{startup_differs, staged, pending, boot_mode}` |
| `POST /commit` | admin | `{timeout (s, 10–3600, default 60), comment}` applies the candidate |
| `POST /commit/confirm` | admin | keep the pending commit |
| `POST /commit/cancel` | admin | roll back now |
| `POST /commit/extend` | admin | `{timeout}` |
| `GET/POST /users`, `PATCH/DELETE /users/{name}` | admin | user management |
| `GET/POST /tokens`, `DELETE /tokens/{id}` | ro | own tokens (admins: all, `?all=1`) |
| `GET /sessions` | admin | active sessions |
| `GET /tls`, `POST /tls` | ro/admin | certificate info; `{cert,key}` or `{regenerate:true}` |
| `GET /audit?n=` | ro | audit log |
| `GET /logs?n=` | ro | daemon log + syslog tail |
| `GET /events?topics=stats,commit,link,logs` | ro | server-sent events |
| `POST /diag/ping`, `POST /diag/traceroute` | admin | `{host,count,family}` |
| `POST /diag/capture`, `GET /diag/capture/{id}[.pcap]` | admin | `{port,count,seconds,filter}` → pcap |
| `POST /system/restart`, `/system/reboot`, `/system/shutdown` | admin | reboot/shutdown need `{confirm:true}` |
| `GET /capabilities` | ro | feature/limitation matrix |
| `GET /version` | – | version |

Console-only endpoints (`/console/*`) are reachable only through the local
unix socket as root and apply changes immediately. They are the recovery
tools behind the console menu.
