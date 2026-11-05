#!/bin/sh
#
# install.sh - turn a fresh FreeBSD 15 installation into a bsdswitch
# appliance.
#
#   fetch -o install.sh https://raw.githubusercontent.com/Lukas0923xcv/freebsdswitch/main/scripts/install.sh
#   sh install.sh --pkg ./bsdswitch-0.1.0.pkg
#
# Options:
#   --pkg FILE|URL         install this bsdswitch package (default: pkg install bsdswitch,
#                          or keep an already installed one)
#   --with-lldp            also install lldpd (LLDP neighbor discovery)
#   --serial               enable the serial console (115200 8N1) in addition to VGA
#   --console-autologin    show the menu on ttyv0/ttyu0 without a password (like pfSense);
#                          only do this if the console is physically secured
#   --console-only         only (re)install the console-menu-on-login hook
#   --no-start             do not start bsdswitchd now
#   -y                     do not ask for confirmation
#
# What needs root: this script, bsdswitch-privd (the small helper that runs
# ifconfig/sysctl/route and binds port 443). bsdswitchd itself runs as the
# unprivileged user _bsdswitch.
set -eu

PKG=""
LLDP=0
SERIAL=0
AUTOLOGIN=0
CONSOLE_ONLY=0
START=1
YES=0

die() { echo "install.sh: $*" >&2; exit 1; }
msg() { printf '\033[1m==> %s\033[0m\n' "$*"; }

while [ $# -gt 0 ]; do
	case "$1" in
	--pkg) PKG=$2; shift ;;
	--with-lldp) LLDP=1 ;;
	--serial) SERIAL=1 ;;
	--console-autologin) AUTOLOGIN=1 ;;
	--console-only) CONSOLE_ONLY=1 ;;
	--no-start) START=0 ;;
	-y) YES=1 ;;
	-h|--help) sed -n '2,25p' "$0"; exit 0 ;;
	*) die "unknown option $1" ;;
	esac
	shift
done

[ "$(id -u)" = 0 ] || die "must be run as root"
# Set by the ISO installer: we run inside a chroot of the target system,
# so nothing may touch the running (installer) kernel.
CHROOT=${BSDSWITCH_INSTALL_CHROOT:-0}
if [ "$CHROOT" = 1 ]; then START=0; YES=1; fi
[ "$(uname -s)" = FreeBSD ] || die "this is not FreeBSD"
REL=$(freebsd-version -u)
MAJOR=${REL%%.*}
MINOR=${REL#*.}; MINOR=${MINOR%%[-.]*}
if [ "$MAJOR" -lt 15 ]; then
	die "FreeBSD $REL is too old: bsdswitch needs 15.0 or later (if_bridge VLAN filtering)"
fi

install_console_hook() {
	msg "console menu on root login (ttyv0 / ttyu0)"
	PROFILE=/root/.profile
	touch $PROFILE
	if ! grep -q 'BEGIN bsdswitch console menu' $PROFILE; then
		cat >> $PROFILE <<'EOP'
# BEGIN bsdswitch console menu
# Show the bsdswitch menu when root logs in on the local or serial console.
# Set BSDSWITCH_NO_MENU=1 (or choose 7 in the menu) to get a shell.
case "$(tty 2>/dev/null)" in
/dev/ttyv0|/dev/ttyu0|/dev/console)
	[ -z "${BSDSWITCH_NO_MENU:-}" ] && [ -x /usr/local/sbin/bsdswitch-console ] && exec /usr/local/sbin/bsdswitch-console
	;;
esac
# END bsdswitch console menu
EOP
	fi
	if [ $AUTOLOGIN = 1 ]; then
		msg "console auto-login (menu without password)"
		if ! grep -q '^bsdswitch|' /etc/gettytab; then
			cat >> /etc/gettytab <<'EOG'

# bsdswitch: auto-login root on the console (shows the menu)
bsdswitch|al.bsdswitch:\
	:al=root:tc=Pc:
bsdswitch.serial|al.bsdswitch.serial:\
	:al=root:tc=3wire.115200:
EOG
		fi
		sed -i '' -E 's|^(ttyv0[[:space:]]+"/usr/libexec/getty )[^"]*"|\1bsdswitch"|' /etc/ttys
		sed -i '' -E 's|^(ttyu0[[:space:]]+"/usr/libexec/getty )[^"]*"|\1bsdswitch.serial"|' /etc/ttys
		[ "$CHROOT" = 1 ] || kill -HUP 1 2>/dev/null || true
	fi
}

if [ $CONSOLE_ONLY = 1 ]; then
	install_console_hook
	exit 0
fi

cat <<EOT
This turns this FreeBSD $REL machine into a bsdswitch appliance:
  - installs the bsdswitch package and enables bsdswitchd
  - loads if_bridge, bridgestp and if_lagg at boot
  - sets switch-friendly sysctls (no IP forwarding, no pf on bridged frames)
  - shows the bsdswitch menu when root logs in on the console
All Ethernet interfaces except the management interface become switch
ports when you run the first-boot wizard.
EOT
if [ $YES = 0 ]; then
	printf "Continue? [y/N] "
	read -r a
	case "$a" in y|Y|yes) ;; *) exit 1 ;; esac
fi

msg "installing bsdswitch"
export ASSUME_ALWAYS_YES=yes
if [ -n "$PKG" ]; then
	case "$PKG" in
	http://*|https://*)
		f=/tmp/$(basename "$PKG")
		fetch -o "$f" "$PKG"
		PKG=$f ;;
	esac
	pkg add -f "$PKG"
elif pkg info -e bsdswitch; then
	echo "bsdswitch is already installed"
else
	pkg install -y bsdswitch || die "bsdswitch is not in your pkg repositories; build it with 'make pkg' and use --pkg FILE"
fi
if [ $LLDP = 1 ]; then
	msg "installing lldpd"
	pkg install -y lldpd || echo "warning: lldpd could not be installed (LLDP stays unavailable)"
fi

msg "rc.conf"
sysrc bsdswitchd_enable=YES
for m in if_bridge bridgestp if_lagg; do
	case " $(sysrc -n kld_list 2>/dev/null) " in
	*" $m "*) ;;
	*) sysrc kld_list+=" $m" ;;
	esac
done
sysrc sshd_enable=YES
sysrc sendmail_enable=NONE >/dev/null 2>&1 || true
sysrc dumpdev=NO

msg "sysctl.conf"
if ! grep -q 'BEGIN bsdswitch' /etc/sysctl.conf 2>/dev/null; then
	cat >> /etc/sysctl.conf <<'EOS'
# BEGIN bsdswitch
# A switch bridges, it does not route between its management networks.
net.inet.ip.forwarding=0
net.inet6.ip6.forwarding=0
# pf only filters bridged frames when ACLs are configured (bsdswitchd
# toggles pfil_member at runtime).
net.link.bridge.pfil_member=0
net.link.bridge.pfil_bridge=0
net.link.bridge.pfil_onlyip=0
net.link.bridge.log_mac_flap=1
# END bsdswitch
EOS
fi
if [ "$CHROOT" != 1 ]; then
	for m in if_bridge bridgestp if_lagg; do kldload -n $m 2>/dev/null || true; done
	sysctl -f /etc/sysctl.conf >/dev/null 2>&1 || true
fi

if [ $SERIAL = 1 ]; then
	msg "serial console"
	sysrc -f /boot/loader.conf boot_multicons=YES console="comconsole,vidconsole" comconsole_speed=115200
fi

install_console_hook

cat > /etc/motd.template <<'EOM'

  bsdswitch - managed switch on FreeBSD
  Run 'bsdswitch-console' for the menu or 'bsdswitchctl help' for the CLI.

EOM

if [ $START = 1 ]; then
	msg "starting bsdswitchd"
	service bsdswitchd restart || service bsdswitchd start
fi

cat <<EOT

Done. Next steps:
  1. Log in as root on the console (or run 'bsdswitch-console'):
     the first-boot wizard asks for the management interface and IP.
  2. Open the web UI at the URL it prints and log in as 'admin' with the
     one-time password shown in the menu header.
EOT
