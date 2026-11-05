# bsdswitch

**bsdswitch turns a FreeBSD machine into a managed Ethernet switch**, in
the same spirit as pfSense turning FreeBSD into a firewall: a console menu,
an HTTPS web UI, a Cisco-flavoured CLI and a REST API. Changes are staged and
applied with Junos-style **commit-confirm**, and roll back automatically if
you lose access.

Frames are forwarded by the kernel (`if_bridge(4)` with VLAN filtering,
available since **FreeBSD 15.0**). bsdswitch is only the management and
control plane: it contains no kernel module and no userland packet
forwarder.

- **Target:** FreeBSD 15.1-RELEASE (current release; supported until
  2027-03-31). The minimum is 15.0. 15.2 is expected in December 2026 and
  needs no changes.
- **Architectures:** amd64; arm64 is cross-compiled.
- One static Go binary (about 9 MB) with the web UI embedded. It works
  offline, with no CDNs and no external fonts.

```
            Web UI (Svelte SPA, embedded)   bsdswitchctl     bsdswitch-console
                         \                      |                /
                       HTTPS /api/v1 + SSE      |      local unix socket (root)
                                      \         |        /
                    +------------- bsdswitchd  (user _bsdswitch) ----------------+
                    | config store: candidate / running / startup / history(10)  |
                    | validator -> planner (desired vs. live kernel -> commands) |
                    | commit-confirm engine (timer lives in the daemon)          |
                    | stats (1 h of 1 s samples in RAM), auth, audit log         |
                    +-----------------------------+------------------------------+
                                                  | argv-only commands, allowlisted
                    +-------------- bsdswitch-privd  (root, small) --------------+
                    | binds :443/:80, runs ifconfig/sysctl/route/sysrc/service   |
                    | after policy checks, supervises bsdswitchd                 |
                    +-------------------------------------------------------------+
                         kernel: if_bridge (vlanfilter), bridgestp, lagg, pf, dummynet
```

## Contents

1. [Feature and limitation overview](#features-and-limitations)
2. [Proxmox VE test setup](#proxmox-ve-test-setup)
3. [Installation](#installation): ISO, pkg, install script, from source
4. [First-time setup](#first-time-setup)
5. [Daily use](#daily-use): web UI, CLI, commit-confirm
6. [Recovering from a lock-out](#recovering-from-a-lock-out)
7. [Configuration file and generated commands](#configuration)
8. [Security and privileges](#security-and-privileges)
9. [Development and tests](#development-and-tests)
10. [Files and directories](#files)

---

## Features and limitations

This table compares bsdswitch with a typical hardware smart switch
(Cisco SG/CBS, HP/Aruba, Netgear, UniFi). It is the same table the web UI
shows under **System › About**.

| Feature | bsdswitch | Notes |
|---|---|---|
| 802.1Q VLANs (access / trunk / native) | ✅ | `if_bridge` vlanfilter, all 4094 VLANs |
| Per-VLAN MAC learning, static MACs, aging, flush | ✅ | |
| RSTP / STP | ✅ | One instance for the whole bridge |
| MSTP / PVST+ | ❌ | `if_bridge` has a single spanning tree |
| Port security (max MACs, sticky) | ✅ | `ifmaxaddr`, `sticky` |
| Protected / isolated ports | ✅ | `private` flag |
| Link aggregation (LACP) | ✅ | `lagg(4)` as a bridge member |
| Port mirroring (SPAN) | ⚠️ | A span port receives **all** bridged traffic. No per-port, per-direction or per-VLAN selection, and no RSPAN |
| ACLs (IP/TCP/UDP/ICMP) | ⚠️ | Uses `pf` on bridge members; costs CPU |
| MAC ACLs | ❌ | `pf` cannot match MAC addresses |
| Rate limiting | ⚠️ | `dummynet` pipes per port; software shaping that adds latency |
| QoS / 802.1p priority queues | ❌ | No hardware queues; the PCP bits are passed through |
| Q-in-Q (802.1ad) | ⚠️ | Per-port `qinq` and `ifvlanproto 802.1ad`; no selective QinQ |
| IGMP / MLD snooping | ❌ | Multicast is flooded within the VLAN. Workaround: put multicast-heavy devices in their own VLAN |
| DHCP snooping, dynamic ARP inspection, 802.1X | ❌ | |
| Storm control | ❌ | Can be approximated with a rate limit on the port |
| LLDP | ✅ | Through the `lldpd` package |
| SNMP (MIB-II, BRIDGE-MIB) | ✅ | `bsnmpd` with `snmp_bridge`, read-only |
| Remote syslog, config backup and restore | ✅ | |
| Jumbo frames | ⚠️ | One MTU for the whole bridge, and the NIC must support it |
| Speed / duplex setting | ⚠️ | Only on NICs that support it. Virtio shows **n/a** |
| Line rate / hardware offload | ❌ | The CPU forwards every frame. Expect a few Gbit/s in total on a modern x86 machine, not line rate on 24 ports |
| PoE | ❌ | |
| Commit-confirm, rollback, running vs. startup config | ✅ | Better than most smart switches |

Legend: ✅ supported, ⚠️ partial (see notes), ❌ not possible with
`if_bridge`. Unsupported features are marked as such in the UI rather
than faked.

---

## Proxmox VE test setup

The development switch is a FreeBSD VM. Each virtio NIC of the VM is one
switch port, connected to its own Proxmox Linux bridge (`vmbrX`). Test
clients (other VMs or LXC containers) are plugged into those bridges.

```
 LAN (vmbr0) ---- net0  vtnet0   management (outside the switch)
   vmbr11   ---- net1  vtnet1   switch port 1   <- LXC h1
   vmbr12   ---- net2  vtnet2   switch port 2   <- LXC h2
   vmbr13   ---- net3  vtnet3   switch port 3   <- LXC h3
   vmbr14   ---- net4  vtnet4   switch port 4   <- LXC "trunk" client (tagged VLANs)
```

### 1. Port bridges on the Proxmox host

Add one bridge without physical ports per switch port, in
`/etc/network/interfaces` on the PVE host or via
*Datacenter › node › System › Network › Create › Linux Bridge*:

```
auto vmbr11
iface vmbr11 inet manual
	bridge-ports none
	bridge-stp off
	bridge-fd 0
#bsdswitch port 1
```

Repeat for `vmbr12` to `vmbr14`, then run `ifreload -a`.

- Leave these bridges **not VLAN aware**. A non-VLAN-aware Linux bridge
  passes 802.1Q-tagged frames unchanged, which is what trunk tests need.
  If you do make them VLAN aware, give the switch NIC `trunks=1-4094`,
  otherwise Proxmox drops the tagged frames.
- With `bridge-stp off`, the Linux bridge forwards STP BPDUs, so RSTP
  between two bsdswitch VMs works.
- LLDP frames (`01:80:c2:00:00:0e`) are **not** forwarded by a Linux
  bridge by default. For LLDP tests run
  `echo 16384 > /sys/class/net/vmbr11/bridge/group_fwd_mask`.
- LACP frames (`…:02`) can never be forwarded by a Linux bridge. Test LACP
  with the epair lab inside the VM (see below) or on real hardware.

### 2. The VM

```sh
qm create 900 --name bsdswitch --ostype other --machine q35 \
  --cores 2 --memory 2048 --cpu host \
  --scsihw virtio-scsi-single --scsi0 local-lvm:16 \
  --serial0 socket \
  --net0 virtio,bridge=vmbr0,firewall=0 \
  --net1 virtio,bridge=vmbr11,firewall=0 \
  --net2 virtio,bridge=vmbr12,firewall=0 \
  --net3 virtio,bridge=vmbr13,firewall=0 \
  --net4 virtio,bridge=vmbr14,firewall=0 \
  --ide2 local:iso/bsdswitch-0.1.0-freebsd15.1-amd64.iso,media=cdrom --boot 'order=scsi0;ide2'
```

**The VM must be able to send frames with other hosts' MAC addresses.**
A switch forwards frames whose source MAC belongs to the clients, not to
the VM's NIC:

- **Proxmox firewall off on every switch NIC (`firewall=0`).** If the
  firewall is enabled on a NIC, its *MAC filter* option (VM › Firewall ›
  Options, on by default) drops every frame whose source MAC is not the
  NIC's own. That is fatal for a switch. If you need the firewall, at
  least turn off *MAC filter* and *IP filter* for this VM.
- Proxmox Linux bridges do no other MAC filtering or anti-spoofing, so
  nothing else is needed on the host.
- **Promiscuous mode in FreeBSD:** `if_bridge` puts every member port into
  promiscuous mode automatically (`ifconfig` shows `PROMISC`). virtio-net
  supports this. You don't need to configure anything.
- `serial0 socket` gives you the console menu in the Proxmox xterm.js
  console. Choose "serial console" in the installer, or run
  `install.sh --serial`.

### 3. Test clients

LXC containers are the cheapest "hosts on switch ports":

```sh
pct create 911 local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst --hostname h1 \
  --net0 name=eth0,bridge=vmbr11,ip=10.10.0.1/24,firewall=0 --start 1
pct create 912 local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst --hostname h2 \
  --net0 name=eth0,bridge=vmbr12,ip=10.10.0.2/24,firewall=0 --start 1
pct create 913 local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst --hostname h3 \
  --net0 name=eth0,bridge=vmbr13,ip=10.10.0.3/24,firewall=0 --start 1
# a client on the trunk port: two tagged VLANs
pct create 914 local:vztmpl/debian-12-standard_12.7-1_amd64.tar.zst --hostname trunk \
  --net0 name=eth10,bridge=vmbr14,tag=10,ip=10.10.0.4/24,firewall=0 \
  --net1 name=eth20,bridge=vmbr14,tag=20,ip=10.20.0.4/24,firewall=0 --start 1
```

Put vtnet1 and vtnet2 in VLAN 10 and vtnet3 in VLAN 20: h1 can ping h2 but
not h3, even though all three share one IP subnet. On a non-VLAN-aware
bridge, Proxmox's `tag=10` makes the container's frames arrive tagged on
the trunk port, so the "trunk" container reaches h1 and h2 through VLAN 10.

### 4. virtio (vtnet) notes

- virtio-net has no real link speed or duplex. The UI shows **n/a**, and
  any speed setting is ignored with a warning instead of failing.
- bsdswitch disables **LRO** and **TSO** on every switch port
  (`ifconfig vtnetX -lro -tso`). `if_bridge` also disables LRO itself.
  Hardware VLAN filtering (`-vlanhwfilter`) is disabled on NICs that have
  it (em/igb/ix), because it would drop tags the bridge needs to see. Set
  `offloads: keep` on a port to opt out.
- If you see checksum errors on bridged traffic (rare, older
  hypervisors), also disable `txcsum`/`rxcsum` on the vtnet ports.
- Throughput through `if_bridge` in a VM is a few Gbit/s, limited by CPU.

### 5. A full lab inside one VM, without extra vNICs

`make lab-up` creates seven `epair(4)` switch ports and VNET jails as
hosts, plus a looped port pair for STP. `make lab-run` starts bsdswitch
on that lab (web UI on port 8443 of the VM), and `make lab-test` runs the
integration tests (see [Development](#development-and-tests)).

---

## Installation

### A) Installer ISO (FreeBSD + bsdswitch in one step)

Download `bsdswitch-<version>-freebsd15.1-amd64.iso`, or build it (see
[iso/README.md](iso/README.md)), and boot from it. It is the official
FreeBSD 15.1 disc1 image with the bsdswitch package added, so it installs
**without network access**:

1. FreeBSD asks *"Begin an installation using the provided script?"*.
   Choose **Install**.
2. bsdswitch menu: **Install bsdswitch appliance**, *Standard FreeBSD
   installer* or *Shell*.
3. Choose the target disk (**it will be erased**), ZFS (recommended:
   boot environments allow safe updates) or UFS, serial console yes/no,
   host name and root password.
4. FreeBSD and bsdswitch are installed and the machine reboots into the
   first-boot wizard.

A memstick image for USB sticks is built with `iso/build-iso.sh -m`.

### B) pkg on an existing FreeBSD 15 installation

```sh
pkg add ./bsdswitch-0.1.0.pkg          # built with `make pkg` on FreeBSD
sh /usr/local/share/bsdswitch/install.sh -y   # appliance settings + console menu
```

### C) Install script on a fresh FreeBSD installation

```sh
fetch -o install.sh https://raw.githubusercontent.com/Lukas0923xcv/freebsdswitch/main/scripts/install.sh
sh install.sh --pkg https://…/bsdswitch-0.1.0.pkg --serial
```

The script:

- installs the package and sets `bsdswitchd_enable=YES`
- loads `if_bridge`, `bridgestp` and `if_lagg` at boot (`kld_list`)
- writes switch sysctls to `/etc/sysctl.conf` (no IP forwarding, no `pf`
  on bridged frames)
- makes root's console login (`ttyv0`/`ttyu0`) start the menu

Options: `--with-lldp`, `--console-autologin` (menu without password,
like pfSense; only for physically secured boxes) and `--serial`.

### D) From source

```sh
pkg install go git gmake node npm   # node only to rebuild the web UI
gmake web build                     # build/bsdswitch
gmake pkg                           # dist/bsdswitch-<version>.pkg
```

The built web UI is committed (`internal/webui/dist`), so `go build`
alone works without Node.js. To cross-compile on any OS: `make freebsd`
writes `build/bsdswitch-freebsd-amd64` and `build/bsdswitch-freebsd-arm64`.

---

## First-time setup

Log in as root on the console (VGA or serial). If there is no
configuration yet, the **first-boot wizard** starts:

```
Detected Ethernet interfaces:
  #  Interface  Driver  MAC                Link  Role
  1  vtnet0     vtnet   bc:24:11:aa:00:00  up    unused
  2  vtnet1     vtnet   bc:24:11:aa:00:01  up    unused
  ...
Management interface (number or name) [vtnet0]:
Hostname [bsdswitch]: sw01
Configure IPv4 via DHCP? (Y/n): n
IPv4 address with prefix (e.g. 192.168.1.10/24): 192.168.1.10/24
IPv4 default gateway (empty for none): 192.168.1.1
...
Setup complete.
Web UI: https://192.168.1.10/
Login: admin / Kq7m-Xh3Pz-T9wc  (you must choose a new password at first login)
```

All other interfaces become switch ports in VLAN 1. The management
interface stays outside the bridge, so a VLAN mistake cannot lock you
out. There is no hard-coded default password: the admin password is
random, shown only on the console, and must be changed at the first
login.

After that, the console shows the menu:

```
bsdswitch 0.1.0 - sw01
========================================================================
 Management: vtnet0   (static, link up) 192.168.1.10/24
 Web UI:     https://192.168.1.10/
 Switch ports: 4 (4 up)   VLANs: 3   Bridge: bridge0
========================================================================
 1) Assign management interface      6) Roll back to previous config
 2) Set management IP                 7) Shell
 3) Reset web UI admin password       8) Reboot
 4) Reset to factory defaults         9) Shut down
 5) Show status                       r) Restart bsdswitchd
                                      0) Logout
```

## Daily use

### Web UI

| Page | What it does |
|---|---|
| **Dashboard** | Switch faceplate. Colour shows link up/down/disabled; badges show speed and access VLAN or **T** for trunk; an amber outline marks an STP-blocking port. Also system info, CPU/RAM, running-vs-startup status and live throughput. Click a port to edit it. |
| **Interfaces** | Every detected NIC with driver, MAC, link and capabilities. Assign management, switch port or unused. |
| **Ports** | Table of every port with live rates and errors, an edit dialog, bulk edit, a per-port traffic graph (last hour, rx/tx bps and pps) and counter reset. |
| **VLANs** | VLAN list, **membership matrix** (click a cell: not a member → untagged → tagged) and in-band management VLAN. |
| **MAC table** | Search and filter, static entries, aging time, flush. |
| **Spanning tree** | Mode, priority and timers, root bridge information, and per-port role, state, cost and edge. |
| **System** | General settings, management access (interface, IP, SSH, allowed subnets), users and roles, API tokens, TLS certificate, configuration (running YAML, diff to startup, save, backup/restore, YAML editor, the last 10 versions with rollback), logs (live), audit log, diagnostics (ping, traceroute, packet capture as pcap download), power and an About page with the feature table. |

**The change workflow.** Every edit is *staged*. A blue bar shows how many
changes are pending. *Review* shows the YAML diff and the **exact
commands** that will run, and warns if a change affects management
access. *Apply* starts a commit-confirm with a countdown (10 s to 5 min):
confirm, or the daemon rolls everything back on its own, even if your
browser lost the connection. Afterwards an amber bar reminds you that the
running configuration is not yet the startup configuration until you
press **Save**.

### CLI (`bsdswitchctl`)

As root on the switch, the CLI talks to the local socket. From elsewhere
use `-H https://sw01 -t <API token>` or `-u admin`.

```
bsdswitchctl show interfaces
Port    Name    Status      Vlan        Tagged  Speed  Duplex  STP         Rx bps  Tx bps  Errors
vtnet1  PC-1    connected   1           -       n/a    n/a     forwarding  3.9M    2.4M    0
vtnet4  Uplink  connected   trunk n:1   10,20   n/a    n/a     forwarding  6.9M    17.5M   0

bsdswitchctl configure
sw01(config)# vlan 30
sw01(config-vlan-30)# name guests
sw01(config-vlan-30)# interface vtnet3
sw01(config-if-vtnet3)# switchport access vlan 30
sw01(config-if-vtnet3)# spanning-tree portfast
sw01(config-if-vtnet3)# end

bsdswitchctl show configuration diff     # YAML diff + commands to run
bsdswitchctl commit confirm 120 comment guests on port 3
bsdswitchctl confirm                     # keep it (otherwise auto-rollback)
bsdswitchctl write memory                # running -> startup
bsdswitchctl show history                # last 10 versions
bsdswitchctl rollback 1                  # stage the previous version
```

Other commands: `show vlan`, `show mac address-table [vlan N] [interface P]`,
`show spanning-tree`, `show running-config`, `show startup-config`,
`show commit`, `show system`, `show audit`, `show logging`,
`show capabilities`, `clear counters`, `clear mac address-table`,
`discard`, `reload`. One-shot configuration:
`bsdswitchctl configure -c "interface vtnet2" -c "switchport access vlan 10"`
or `-f script.txt`.

### REST API

All endpoints are under `/api/v1`, are JSON, and are used by both the UI
and the CLI. Authenticate with the session cookie and the `X-CSRF-Token`
header, or with `Authorization: Bearer <token>`. Read-only users can call
every `GET`. Live data comes from `GET /api/v1/events` (server-sent
events: `stats`, `commit`, `link`, `logs`). See
[docs/API.md](docs/API.md) for the endpoint list.

---

## Recovering from a lock-out

1. **Do nothing.** If the last change cut you off, the commit-confirm
   timer rolls it back (default 60 s). This also works across a daemon
   restart.
2. **Console menu (VGA, serial or the Proxmox console):**
   - **1** moves management to another interface.
   - **2** sets the management IP, DHCP or static.
   - **6** rolls back to one of the last 10 configurations, immediately.
   - **3** resets the web UI admin password (prints a new one-time password).
   - **4** resets to factory defaults: management via DHCP on the first
     NIC with link, all other ports in VLAN 1, users reset.

   Console changes are applied immediately and saved. You are at the box,
   so there is no confirm timer.
3. **Reboot.** An unconfirmed or unsaved change is gone after a reboot,
   because the startup configuration is applied at boot. If the startup
   configuration itself is broken, bsdswitch boots in **safe mode**: only
   the management interface is configured and the UI shows a red banner.
4. **By hand:** `service bsdswitchd stop`, edit
   `/usr/local/etc/bsdswitch/config.yaml` (previous versions are in
   `/var/db/bsdswitch/history/`), then `service bsdswitchd start`. The
   management address is also kept in `/etc/rc.conf`
   (`ifconfig_<mgmt>=…`), so the box stays reachable even if bsdswitchd
   does not start.

---

## Configuration

`/usr/local/etc/bsdswitch/config.yaml` is the **startup** configuration.
The running configuration is kept in `/var/db/bsdswitch/running.yaml`, and
staged edits in `candidate.yaml`. Example
([docs/config.yaml.sample](docs/config.yaml.sample)):

```yaml
version: 1
system: { hostname: sw01, timezone: Europe/Vienna, dns: [192.168.1.1], ntp: [pool.ntp.org] }
management:
  interface: vtnet0                       # dedicated port, outside the bridge
  ipv4: { mode: static, address: 192.168.1.10/24, gateway: 192.168.1.1 }
  ipv6: { mode: none }
  ssh: { enabled: true, password_auth: true }
bridge:
  name: bridge0
  mac_aging: 300
  stp: { enabled: true, mode: rstp, priority: 32768 }
vlans: [{ id: 1, name: default }, { id: 10, name: office }, { id: 20, name: iot }]
ports:
  - { name: vtnet1, description: PC-1,   mode: access, pvid: 1,  stp: { edge: true } }
  - { name: vtnet2, description: PC-2,   mode: access, pvid: 10, stp: { edge: true } }
  - { name: vtnet3, description: Camera, mode: access, pvid: 20, stp: { edge: true } }
  - { name: vtnet4, description: Uplink, mode: trunk, native: 1, tagged: "10,20" }
static_macs:
  - { mac: "00:11:22:33:44:55", port: vtnet1, vlan: 1 }
```

On a freshly booted system, the planner generates the following
([internal/plan/testdata/example.sh](internal/plan/testdata/example.sh)).
Later commits only run the difference: for example
`ifconfig bridge0 ifuntagged vtnet2 20` or
`ifconfig bridge0 +iftagged vtnet4 30`.

```sh
# Kernel settings (sysctl)
sysctl net.inet.ip.forwarding=0 net.inet6.ip6.forwarding=0 net.link.bridge.log_mac_flap=1 \
       net.link.bridge.pfil_bridge=0 net.link.bridge.pfil_member=0 net.link.bridge.pfil_onlyip=0
# Management interface vtnet0 (outside the bridge)
ifconfig vtnet0 up
ifconfig vtnet0 inet 192.168.1.10/24 alias
# Switch port settings
ifconfig vtnet1 -lro -tso
ifconfig vtnet1 description PC-1
ifconfig vtnet1 up
...
# Bridge bridge0  (vlanfilter is enabled before any member is added)
ifconfig bridge0 create
ifconfig bridge0 vlanfilter
ifconfig bridge0 timeout 300
ifconfig bridge0 addm vtnet1 untagged 1
ifconfig bridge0 stp vtnet1 edge vtnet1
ifconfig bridge0 addm vtnet2 untagged 10
ifconfig bridge0 stp vtnet2 edge vtnet2
ifconfig bridge0 addm vtnet3 untagged 20
ifconfig bridge0 stp vtnet3 edge vtnet3
ifconfig bridge0 addm vtnet4 untagged 1 tagged 10,20
ifconfig bridge0 stp vtnet4
ifconfig bridge0 up
# Static MAC address entries
ifconfig bridge0 static vtnet1 00:11:22:33:44:55 vlan 1
# Default routes
route add default 192.168.1.1
# System services
hostname sw01
tzsetup Europe/Vienna
...
```

**In-band management** (*VLANs › In-band management*) adds a `vlan(4)`
interface on the bridge, for example `ifconfig bridge0.99 create inet
10.99.0.2/24`. `if_bridge` does not put host traffic on any VLAN by
itself. The web UI accepts connections only on addresses of the management
interface and of that VLAN interface. Bridge member ports have no IP
addresses (FreeBSD refuses them), so the UI is never reachable through a
switch port otherwise.

**What persists where:**

- bsdswitchd owns the bridge and applies the startup configuration at
  boot. The bridge is deliberately not in `rc.conf`, so the two cannot
  conflict.
- On *Save*, the management interface, gateway, host name and SSH
  settings are also written to `rc.conf`.

---

## Security and privileges

| Component | Runs as | Why |
|---|---|---|
| `bsdswitchd` (API, web UI, planner, auth) | `_bsdswitch` | Reads kernel state (no root needed) and is the only process exposed to the network |
| `bsdswitch-privd` | root | Binds ports 443 and 80 and passes the sockets to the daemon. Runs commands only from a fixed allowlist (`ifconfig`, `sysctl net.link.bridge.*` and `net.inet*.forwarding`, `route … default`, `sysrc` with an allowlist of keys, `service` on a few services, `shutdown -r/-p now`, `tcpdump` into the capture directory, …), always as argv, never through a shell. Writes only allowlisted files (`resolv.conf`, `ntp.conf`, …) |
| `bsdswitch-console` | root | Local recovery tool on the console |

**Web security:**

- HTTPS only; HTTP redirects to HTTPS. A self-signed certificate is
  generated on first start; you can upload your own.
- Login is rate-limited (5 failures lock out the source IP and the user
  name for 5 minutes or more).
- Sessions use a CSRF token; cookies are `Secure`, `HttpOnly` and
  `SameSite=Strict`; there is an idle timeout.
- Passwords are stored as bcrypt hashes. The first login forces a
  password change.
- Roles are `admin` and `read-only`. API tokens are stored as SHA-256
  hashes.
- A strict Content-Security-Policy is set.
- Optional allowed management subnets.
- An audit log records who changed what and when.
- Input is validated in both the UI and the API: VLANs 1–4094, MTU ranges,
  IP formats, PVID/native/tagged consistency, and no shell characters in
  descriptions.

---

## Development and tests

```sh
make test     # unit tests (config, validation, diff/planner, parsers, policy) + API tests
make e2e      # Playwright: login, VLAN matrix, commit-confirm rollback, save (simulator)
make dev      # daemon with the built-in kernel simulator on http://127.0.0.1:8080
              # (no FreeBSD needed); `cd web && npm run dev` for UI hot reload
```

**Simulator mode.** `bsdswitchd -sim` uses an in-memory kernel that
understands every `ifconfig`, `route` and `sysctl` command the planner
generates. It exists so the API tests, the UI tests and UI development run
on any OS. The planner tests apply plans to it and check that a second
plan is empty (idempotence) and that rollbacks converge.

**Integration tests on real FreeBSD.** These run inside a single VM with
no extra vNICs:

```sh
make lab-test   # root, FreeBSD 15: epair(4) ports + VNET jails as hosts
```

They check:

- VLAN isolation: ping within a VLAN works; across VLANs it fails even in
  the same IP subnet.
- Tagged and native trunk traffic.
- RSTP blocks a looped port pair.
- An unconfirmed commit really rolls back the kernel.
- The management station reaches the API.

`make lab-up` / `make lab-run` / `make lab-down` keep the lab around for
manual testing. CI runs everything, including the FreeBSD job in a
FreeBSD 15.1 VM ([.github/workflows/ci.yml](.github/workflows/ci.yml)).

## Files

| Path | Purpose |
|---|---|
| `/usr/local/sbin/bsdswitch` | Multi-call binary (`bsdswitchd`, `bsdswitchctl` and `bsdswitch-console` are symlinks; `/usr/local/libexec/bsdswitch-privd`) |
| `/usr/local/etc/rc.d/bsdswitchd` | rc script: `bsdswitchd_enable="YES"` |
| `/usr/local/etc/bsdswitch/config.yaml` | Startup configuration |
| `/usr/local/etc/bsdswitch/users.yaml` | Users and API tokens (bcrypt / SHA-256) |
| `/usr/local/etc/bsdswitch/tls/` | Web UI certificate |
| `/var/db/bsdswitch/` | Running and candidate config, `history/`, `audit.log`, pending commit |
| `/var/run/bsdswitch/` | `api.sock` (local CLI and console), `privd.sock` |

Logs go to syslog (`/var/log/messages`, tag `bsdswitchd`) and to the live
log in the UI.
