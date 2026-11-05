#!/bin/sh
#
# lab.sh - virtual switch test lab inside ONE FreeBSD VM, no extra vNICs.
#
# Switch ports are epair(4) "a" ends on the host; the "b" ends are moved
# into VNET jails that act as hosts plugged into the switch.
#
#                       +------------------------------+
#   jail mgmt  ---------| epair0a  management (outside)|  192.0.2.1/24
#   (192.0.2.2)         |                              |
#   jail h1  -----------| epair1a  access VLAN 10      |
#   jail h2  -----------| epair2a  access VLAN 10      |
#   jail h3  -----------| epair3a  access VLAN 20      |
#   jail h4  -----------| epair4a  trunk native 1,     |
#   (vlan 10/20 ifs)    |          tagged 10,20        |
#   jail h5  -----------| epair5a  access VLAN 1       |
#            +----------| epair6a  \  loop: both ends  |
#            +----------| epair6b  /  are switch ports |
#                       +------------------------------+
#   Hosts h1, h2, h3 share the IP subnet 10.10.0.0/24, so a ping from h1 to
#   h3 can only fail because of VLAN isolation, not because of routing.
#
# usage: lab.sh up | down | run | status
#   up      create epairs + jails and write $LAB/etc/config.yaml
#   run     start bsdswitchd (foreground, as root, no privsep) on the lab
#   down    destroy everything
#
# Requires root, FreeBSD 15.0+, a kernel with VIMAGE (GENERIC has it).
set -eu

LAB=${LAB:-/tmp/bsdswitch-lab}
BIN=${BSDSWITCH_BIN:-$(cd "$(dirname "$0")/../.." && pwd)/build/bsdswitch}
BRIDGE=${LAB_BRIDGE:-bridge77}
WEB=${LAB_WEB:-0.0.0.0:8443}
JAILS="mgmt h1 h2 h3 h4 h5"
ALLJAILS="$JAILS h6"

j() { jexec "$1" sh -c "$2"; }

up() {
	[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
	for m in if_bridge bridgestp if_epair if_lagg; do kldload -n $m; done
	sysctl -n kern.features.vimage >/dev/null 2>&1 || { echo "kernel lacks VIMAGE"; exit 1; }
	mkdir -p $LAB/etc $LAB/db
	: > $LAB/epairs
	i=0
	for jn in $JAILS; do
		ep=$(ifconfig epair create)          # e.g. epair0a
		base=${ep%a}
		echo "$jn ${base}a ${base}b" >> $LAB/epairs
		jail -c name=bswlab_$jn vnet persist host.hostname=$jn vnet.interface=${base}b
		ifconfig ${base}a up
		i=$((i + 1))
	done
	loop=$(ifconfig epair create)
	echo "loop ${loop%a}a ${loop%a}b" >> $LAB/epairs
	# LACP partner: jail h6 bundles two epairs into lagg0
	l1=$(ifconfig epair create); l2=$(ifconfig epair create)
	echo "lag1 ${l1%a}a ${l1%a}b" >> $LAB/epairs
	echo "lag2 ${l2%a}a ${l2%a}b" >> $LAB/epairs
	ifconfig ${l1%a}a up; ifconfig ${l2%a}a up
	jail -c name=bswlab_h6 vnet persist host.hostname=h6 vnet.interface=${l1%a}b vnet.interface=${l2%a}b
	j bswlab_h6 "kldload -n if_lagg 2>/dev/null; ifconfig ${l1%a}b up; ifconfig ${l2%a}b up; \
		ifconfig lagg0 create laggproto lacp laggport ${l1%a}b laggport ${l2%a}b; ifconfig lagg0 inet 10.1.0.6/24 up"
	ifconfig ${loop%a}a up
	ifconfig ${loop%a}b up

	ifa() { awk -v n="$1" '$1 == n { print $2 }' $LAB/epairs; }
	ifb() { awk -v n="$1" '$1 == n { print $3 }' $LAB/epairs; }

	# management station
	j bswlab_mgmt "ifconfig $(ifb mgmt) inet 192.0.2.2/24 up"
	# hosts
	j bswlab_h1 "ifconfig $(ifb h1) inet 10.10.0.1/24 up"
	j bswlab_h2 "ifconfig $(ifb h2) inet 10.10.0.2/24 up"
	j bswlab_h3 "ifconfig $(ifb h3) inet 10.10.0.3/24 up; ifconfig $(ifb h3) inet 10.20.0.3/24 alias"
	j bswlab_h4 "ifconfig $(ifb h4) up; \
		ifconfig $(ifb h4).10 create vlan 10 vlandev $(ifb h4) inet 10.10.0.4/24 up; \
		ifconfig $(ifb h4).20 create vlan 20 vlandev $(ifb h4) inet 10.20.0.4/24 up; \
		ifconfig $(ifb h4) inet 10.1.0.4/24"
	j bswlab_h5 "ifconfig $(ifb h5) inet 10.1.0.5/24 up"

	cat > $LAB/etc/config.yaml <<EOF
version: 1
system: { hostname: bswlab, timezone: UTC }
management:
  interface: $(ifa mgmt)
  ipv4: { mode: static, address: 192.0.2.1/24 }
  ipv6: { mode: none }
  ssh: { enabled: false }
bridge:
  name: $BRIDGE
  allow_virtual: true
  mac_aging: 300
  stp: { enabled: true, mode: rstp, priority: 32768, hello: 2, max_age: 20, fwd_delay: 15 }
vlans:
  - { id: 1, name: default }
  - { id: 10, name: ten }
  - { id: 20, name: twenty }
ports:
  - { name: $(ifa h1), description: h1, mode: access, pvid: 10, stp: { edge: true } }
  - { name: $(ifa h2), description: h2, mode: access, pvid: 10, stp: { edge: true } }
  - { name: $(ifa h3), description: h3, mode: access, pvid: 20, stp: { edge: true } }
  - { name: $(ifa h4), description: h4-trunk, mode: trunk, native: 1, tagged: "10,20" }
  - { name: $(ifa h5), description: h5, mode: access, pvid: 1, stp: { edge: true } }
  - { name: $(ifa loop), description: loop-a, mode: access, pvid: 1 }
  - { name: $(ifb loop), description: loop-b, mode: access, pvid: 1 }
EOF
	echo "lab is up; config in $LAB/etc/config.yaml"
	cat $LAB/epairs
}

run() {
	exec "$BIN" daemon -config-dir $LAB/etc -state $LAB/db -socket $LAB/api.sock -listen "$WEB" -no-mgmt-filter
}

down() {
	[ -f $LAB/pid ] && kill "$(cat $LAB/pid)" 2>/dev/null || true
	for jn in $ALLJAILS; do jail -r bswlab_$jn 2>/dev/null || true; done
	if [ -f $LAB/epairs ]; then
		while read -r name a b; do ifconfig "$a" destroy 2>/dev/null || true; done < $LAB/epairs
	fi
	ifconfig $BRIDGE destroy 2>/dev/null || true
	for i in $(ifconfig -l); do case $i in $BRIDGE.*) ifconfig "$i" destroy ;; esac; done
	rm -rf $LAB
	echo "lab removed"
}

status() {
	[ -f $LAB/epairs ] && cat $LAB/epairs
	ifconfig $BRIDGE 2>/dev/null || echo "$BRIDGE not present"
	jls | grep bswlab || true
}

case "${1:-}" in
up) up ;;
down) down ;;
run) run ;;
status) status ;;
*) echo "usage: $0 up|run|down|status"; exit 2 ;;
esac
