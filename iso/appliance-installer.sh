#!/bin/sh
#
# bsdswitch appliance installer. Runs on the FreeBSD install media: it is
# sourced from /etc/installerconfig (the "preamble" of a bsdinstall
# script), drives the bsdinstall building blocks itself, installs the
# bsdswitch package from the media (offline) and reboots. It never returns
# to bsdinstall's own scripted flow.
#
# Menu:  Install bsdswitch appliance | Standard FreeBSD installer | Shell

MEDIA=/bsdswitch
: ${DIALOG_OK=0}
export BSDINSTALL_CHROOT=/mnt
export BSDINSTALL_DISTDIR=/usr/freebsd-dist
export DISTRIBUTIONS="kernel.txz base.txz"
export nonInteractive=YES
BT="bsdswitch installer"
REL=$(uname -r)

d() { bsddialog --backtitle "$BT" "$@" 3>&1 1>&2 2>&3; }
fail() {
	bsddialog --backtitle "$BT" --title "Installation failed" --msgbox "$1\n\nSee /tmp/bsdinstall_log (Alt+F3 shows it live). Choose OK for a shell." 0 0
	exec /bin/sh
}

# ---- 1. what to do ----------------------------------------------------------------
choice=$(d --title "bsdswitch" --menu "FreeBSD $REL\n\nThis media installs a managed-switch appliance. Installing erases the selected disk." 0 0 0 \
	install "Install bsdswitch appliance (recommended)" \
	standard "Standard FreeBSD installer (bsdinstall)" \
	shell "Shell") || exec /bin/sh
case "$choice" in
standard) exec bsdinstall ;;
shell) exec /bin/sh ;;
esac

# ---- 2. disk -----------------------------------------------------------------------
items=""
for disk in $(sysctl -n kern.disks); do
	case "$disk" in cd*|md*) continue ;; esac
	size=$(diskinfo "$disk" 2>/dev/null | awk '{ printf "%.1f GB", $3 / 1e9 }')
	desc=$(geom disk list "$disk" 2>/dev/null | awk -F': ' '/descr:/ { print $2; exit }')
	items="$items $disk \"$size ${desc:-disk}\""
done
[ -n "$items" ] || fail "No disks found."
DISK=$(eval d --title \"Target disk\" --menu \"Install bsdswitch on which disk?\" 0 0 0 $items) || exec /bin/sh

FS=$(d --title "File system" --menu "ZFS gives boot environments, which bsdswitch uses for safe in-place updates (System > Update)." 0 0 0 \
	zfs "ZFS (recommended, needs >= 2 GB RAM)" \
	ufs "UFS (small flash devices, little RAM)") || exec /bin/sh

opts=$(d --title "Options" --checklist "Console settings" 0 0 0 \
	serial "Enable serial console (115200 8N1) as well" on \
	autologin "Show the menu on the console without a password (physically secured box only)" off) || opts=""

# ---- 3. host name and root password -------------------------------------------------
HOSTNAME=$(d --title "Host name" --inputbox "Host name of the switch:" 0 50 "bsdswitch") || exec /bin/sh
[ -n "$HOSTNAME" ] || HOSTNAME=bsdswitch
while :; do
	PW1=$(d --title "root password" --insecure --passwordbox "Password for root (console and SSH).\nThe web UI admin account gets its own one-time password." 0 60) || exec /bin/sh
	PW2=$(d --title "root password" --insecure --passwordbox "Repeat the password:" 0 60) || exec /bin/sh
	if [ -z "$PW1" ] || [ ${#PW1} -lt 8 ]; then
		d --msgbox "Use at least 8 characters." 0 0
	elif [ "$PW1" != "$PW2" ]; then
		d --msgbox "The passwords do not match." 0 0
	else
		break
	fi
done

d --title "Confirm" --defaultno --yesno "ALL DATA ON $DISK WILL BE ERASED.\n\nInstall FreeBSD $REL + bsdswitch on $DISK ($FS)?" 0 0 || exec /bin/sh

# ---- 4. install -------------------------------------------------------------------
exec 3>&1
step() { bsddialog --backtitle "$BT" --infobox "$1" 5 60; echo "== $1" >> /tmp/bsdinstall_log; }

mkdir -p /tmp/bsdinstall_etc
step "Partitioning $DISK ($FS)"
if [ "$FS" = zfs ]; then
	export ZFSBOOT_DISKS="$DISK"
	export ZFSBOOT_VDEV_TYPE=stripe
	export ZFSBOOT_POOL_NAME=zroot
	export ZFSBOOT_SWAP_SIZE=1g
	export ZFSBOOT_CONFIRM_LAYOUT=0
	export ZFSBOOT_FORCE_4K_SECTORS=1
	bsdinstall zfsboot || fail "ZFS partitioning failed"
else
	bsdinstall scriptedpart "$DISK" || fail "partitioning failed"
fi
bsdinstall mount || fail "mounting the new file systems failed"
step "Extracting FreeBSD $REL"
bsdinstall distextract || fail "extracting the base system failed"
bsdinstall bootconfig || fail "boot configuration failed"
step "Configuring the system"
bsdinstall config || fail "writing configuration failed"

# hostname, root password, time zone
chroot $BSDINSTALL_CHROOT sysrc hostname="$HOSTNAME" >/dev/null
printf '%s\n' "$PW1" | chroot $BSDINSTALL_CHROOT pw usermod root -h 0 || fail "setting the root password failed"
unset PW1 PW2
chroot $BSDINSTALL_CHROOT tzsetup UTC >/dev/null 2>&1 || true

step "Installing bsdswitch (offline)"
mkdir -p $BSDINSTALL_CHROOT/tmp/bsdswitch
cp $MEDIA/*.pkg $BSDINSTALL_CHROOT/tmp/bsdswitch/ || fail "copying packages failed"
[ -d $MEDIA/extra ] && cp $MEDIA/extra/*.pkg $BSDINSTALL_CHROOT/tmp/bsdswitch/ 2>/dev/null
chroot $BSDINSTALL_CHROOT /bin/sh -c '
	set -e
	cd /tmp/bsdswitch
	# bootstrap pkg from the media (no network needed)
	mkdir -p /tmp/pkgx
	tar -xf pkg.pkg -C /tmp/pkgx /usr/local/sbin/pkg-static
	ASSUME_ALWAYS_YES=yes /tmp/pkgx/usr/local/sbin/pkg-static add pkg.pkg
	for p in $(ls *.pkg | grep -v -e "^pkg.pkg$" -e "^bsdswitch"); do
		ASSUME_ALWAYS_YES=yes pkg add -M "$p" || true
	done
	ASSUME_ALWAYS_YES=yes pkg add bsdswitch*.pkg
' >> /tmp/bsdinstall_log 2>&1 || fail "installing the bsdswitch package failed"

iflags="-y --no-start"
case "$opts" in *serial*) iflags="$iflags --serial" ;; esac
case "$opts" in *autologin*) iflags="$iflags --console-autologin" ;; esac
BSDSWITCH_INSTALL_CHROOT=1 chroot $BSDINSTALL_CHROOT /bin/sh /usr/local/share/bsdswitch/install.sh $iflags >> /tmp/bsdinstall_log 2>&1 \
	|| fail "appliance setup failed"
rm -rf $BSDINSTALL_CHROOT/tmp/bsdswitch $BSDINSTALL_CHROOT/tmp/pkgx

bsdinstall entropy
bsdinstall umount

bsddialog --backtitle "$BT" --title "Done" --msgbox "bsdswitch is installed on $DISK.\n\nRemove the installation media and press OK to reboot.\n\nAfter the reboot, log in as root on the console: the first-boot wizard asks for the management interface and prints the web UI address and a one-time admin password." 0 0
reboot
sleep 60
