#!/bin/sh
#
# build-iso.sh - build a bsdswitch installer ISO (and memstick image) by
# remastering the official FreeBSD release media. The result installs
# FreeBSD + bsdswitch fully offline.
#
# usage: iso/build-iso.sh -p dist/bsdswitch-0.1.0.pkg [-v 15.1] [-a amd64|arm64]
#                         [-o dist] [-m] [-x "lldpd"] [-k]
#   -p FILE   bsdswitch package to put on the media (make pkg)
#   -v VER    FreeBSD release (default 15.1; minimum 15.0)
#   -a ARCH   amd64 (default) or arm64
#   -o DIR    output directory (default dist)
#   -m        also build a memstick (USB) image (FreeBSD host only)
#   -x PKGS   extra packages to include for offline install (e.g. "lldpd");
#             needs a FreeBSD host (pkg fetch)
#   -k        keep the work directory
#
# Works on FreeBSD (base tools: fetch, tar, makefs, mkimg, mdconfig; or
# xorriso from packages) and on Linux with xorriso + curl.
set -eu

VER=15.1
ARCH=amd64
OUT=dist
PKGFILE=""
MEMSTICK=0
EXTRA=""
KEEP=0
TOP=$(cd "$(dirname "$0")/.." && pwd)

while getopts "p:v:a:o:mx:k" o; do
	case $o in
	p) PKGFILE=$OPTARG ;;
	v) VER=$OPTARG ;;
	a) ARCH=$OPTARG ;;
	o) OUT=$OPTARG ;;
	m) MEMSTICK=1 ;;
	x) EXTRA=$OPTARG ;;
	k) KEEP=1 ;;
	*) sed -n '2,22p' "$0"; exit 2 ;;
	esac
done
[ -n "$PKGFILE" ] && [ -f "$PKGFILE" ] || { echo "need -p bsdswitch.pkg (build it with 'make pkg')"; exit 2; }
case "$VER" in 15.*|1[6-9].*) ;; *) echo "FreeBSD $VER is too old (need 15.0+)"; exit 2 ;; esac
case "$ARCH" in
amd64) IMGARCH=amd64; ABIARCH=amd64 ;;
arm64) IMGARCH=arm64-aarch64; ABIARCH=aarch64 ;;
*) echo "unsupported arch $ARCH"; exit 2 ;;
esac
MAJOR=${VER%%.*}
ABI="FreeBSD:$MAJOR:$ABIARCH"
BASEURL=${FREEBSD_MIRROR:-https://download.freebsd.org}/releases/ISO-IMAGES/$VER
PKGURL=${PKG_MIRROR:-https://pkg.freebsd.org}/$ABI/quarterly
NAME=FreeBSD-$VER-RELEASE-$IMGARCH
BSWVER=$(basename "$PKGFILE" .pkg | sed 's/^bsdswitch-//')
WORK=$TOP/iso/work/$VER-$ARCH
mkdir -p "$WORK" "$OUT"
OUT=$(cd "$OUT" && pwd)

msg() { printf '\033[1m==> %s\033[0m\n' "$*"; }
have() { command -v "$1" >/dev/null 2>&1; }
dl() { # url file
	if have fetch; then fetch -m -o "$2" "$1"; else curl -fL -z "$2" -o "$2" "$1"; fi
}
sha256of() {
	if have sha256; then sha256 -q "$1"; else sha256sum "$1" | cut -d' ' -f1; fi
}

# ---- 1. download and verify the release media --------------------------------
msg "downloading FreeBSD $VER $ARCH media"
dl "$BASEURL/CHECKSUM.SHA256-$NAME" "$WORK/CHECKSUM.SHA256"
fetch_img() { # suffix
	f=$NAME-$1
	[ -f "$WORK/$f" ] && return 0
	dl "$BASEURL/$f.xz" "$WORK/$f.xz"
	want=$(sed -n "s/^SHA256 ($f.xz) = //p" "$WORK/CHECKSUM.SHA256")
	got=$(sha256of "$WORK/$f.xz")
	[ -n "$want" ] && [ "$want" = "$got" ] || { echo "checksum mismatch for $f.xz"; exit 1; }
	xz -dk "$WORK/$f.xz"
}
fetch_img disc1.iso

# ---- 2. payload ---------------------------------------------------------------------
msg "preparing payload"
PAY=$WORK/payload
rm -rf "$PAY"
mkdir -p "$PAY/bsdswitch/extra" "$PAY/etc"
cp "$PKGFILE" "$PAY/bsdswitch/bsdswitch-$BSWVER.pkg"
dl "$PKGURL/Latest/pkg.pkg" "$PAY/bsdswitch/pkg.pkg"
if [ -n "$EXTRA" ]; then
	if have pkg && [ "$(uname -s)" = FreeBSD ]; then
		msg "fetching extra packages: $EXTRA"
		pkg -o ABI="$ABI" -o IGNORE_OSVERSION=yes fetch -y -d -o "$WORK/extra" $EXTRA
		cp "$WORK"/extra/All/*.pkg "$PAY/bsdswitch/extra/" 2>/dev/null || true
		rm -f "$PAY"/bsdswitch/extra/pkg-[0-9]*.pkg
	else
		echo "warning: -x needs a FreeBSD host with pkg; extra packages skipped"
	fi
fi
install -m 0555 "$TOP/iso/appliance-installer.sh" "$PAY/bsdswitch/appliance-installer.sh"
cat > "$PAY/etc/installerconfig" <<'EOF'
# bsdswitch: this preamble is sourced by `bsdinstall script`. The appliance
# installer offers the standard installer and a shell as well; it never
# returns here.
. /bsdswitch/appliance-installer.sh
EOF
cat > "$PAY/bsdswitch/README.txt" <<EOF
bsdswitch $BSWVER installer media for FreeBSD $VER ($ARCH).
Boot it and choose "Install bsdswitch appliance".
EOF

ISO=$OUT/bsdswitch-$BSWVER-freebsd$VER-$ARCH.iso

# ---- 3. extract the original media ---------------------------------------------------
# The FreeBSD ISO's El Torito images are not files in the ISO tree, so the
# boot records cannot simply be replayed: the tree is extracted, the
# payload added, and a new ISO built with explicit BIOS (cdboot) and UEFI
# (FAT image) boot entries.
ROOT=$WORK/root
EFIIMG=$WORK/efiboot.img
rm -rf "$ROOT" "$WORK/bootimg" "$WORK/esp"
mkdir -p "$ROOT" "$WORK/bootimg"
if have xorriso; then
	TOOL=xorriso
	msg "extracting the ISO with xorriso"
	[ "$(id -u)" = 0 ] || echo "note: not root, files on the ISO will not be owned by root (run with sudo)"
	xorriso -osirrox on -indev "$WORK/$NAME-disc1.iso" -extract / "$ROOT" \
		-extract_boot_images "$WORK/bootimg/" 2>&1 | grep -v -e '^xorriso : UPDATE' -e '^Extracted from ISO' || true
	src=$(ls "$WORK"/bootimg/*uefi* 2>/dev/null | head -1)
	[ -n "$src" ] || { echo "no UEFI boot image found in the original ISO"; ls -l "$WORK/bootimg"; exit 1; }
	cp "$src" "$EFIIMG"
elif [ "$(uname -s)" = FreeBSD ]; then
	TOOL=makefs
	msg "extracting the ISO with tar (base system tools)"
	tar -xf "$WORK/$NAME-disc1.iso" -C "$ROOT"
	mkdir -p "$WORK/esp/EFI/BOOT"
	efiname=BOOTX64.efi
	[ "$ARCH" = arm64 ] && efiname=BOOTAA64.efi
	cp "$ROOT/boot/loader.efi" "$WORK/esp/EFI/BOOT/$efiname"
	makefs -t msdos -o fat_type=12 -o sectors_per_cluster=1 -o volume_label=EFISYS -s 2048k "$EFIIMG" "$WORK/esp"
else
	echo "need xorriso (Linux: apt install xorriso / FreeBSD: pkg install xorriso)"
	exit 1
fi
chmod -R u+w "$ROOT"

# ---- 4. add the payload --------------------------------------------------------------
cp -R "$PAY/bsdswitch" "$ROOT/"
cp "$PAY/etc/installerconfig" "$ROOT/etc/installerconfig"
cp "$EFIIMG" "$ROOT/boot/bsdswitch-efiboot.img"
# keep the original volume label so the live system's /etc/fstab still matches
LABEL=$(awk '$2 == "/" { sub("/dev/iso9660/", "", $1); print $1 }' "$ROOT/etc/fstab")
[ -n "$LABEL" ] || { echo "cannot determine the volume label from /etc/fstab"; exit 1; }

# ---- 5. build the new ISO ----------------------------------------------------------------
msg "building ISO with $TOOL"
rm -f "$ISO"
if [ $TOOL = xorriso ]; then
	BIOS=""
	if [ "$ARCH" = amd64 ]; then
		LOADSZ=$(( ($(wc -c < "$ROOT/boot/cdboot") + 511) / 512 ))
		BIOS="-b boot/cdboot -no-emul-boot -boot-load-size $LOADSZ -eltorito-alt-boot"
	fi
	xorriso -as mkisofs -o "$ISO" -V "$LABEL" -R -publisher bsdswitch -c boot/boot.catalog \
		$BIOS -e boot/bsdswitch-efiboot.img -no-emul-boot "$ROOT"
	xorriso -indev "$ISO" -report_el_torito plain 2>/dev/null | grep -i -E 'El Torito|boot img'
else
	BOOT=""
	[ "$ARCH" = amd64 ] && BOOT="-o bootimage=i386;$ROOT/boot/cdboot -o no-emul-boot"
	BOOT="$BOOT -o bootimage=i386;$EFIIMG -o no-emul-boot -o platformid=efi"
	makefs -t cd9660 $BOOT -o rockridge -o label="$LABEL" -o publisher="bsdswitch" "$ISO" "$ROOT"
	echo "note: hybrid USB boot is not added in makefs mode; use the memstick image (-m) for USB"
fi
ls -l "$ISO"

# ---- 4. memstick image (FreeBSD only) ------------------------------------------------------
if [ $MEMSTICK = 1 ]; then
	[ "$(uname -s)" = FreeBSD ] || { echo "memstick images need a FreeBSD host (UFS)"; exit 1; }
	[ "$(id -u)" = 0 ] || { echo "memstick: run as root (mdconfig/mount)"; exit 1; }
	fetch_img memstick.img
	IMG=$OUT/bsdswitch-$BSWVER-freebsd$VER-$ARCH-memstick.img
	msg "building memstick image"
	cp "$WORK/$NAME-memstick.img" "$IMG"
	# grow the image by 256 MB for the payload
	truncate -s +256m "$IMG"
	MD=$(mdconfig -a -t vnode -f "$IMG")
	trap 'umount "$WORK/mnt" 2>/dev/null; mdconfig -d -u $MD 2>/dev/null' EXIT
	gpart recover "$MD" >/dev/null 2>&1 || true
	IDX=$(gpart show -p "$MD" | awk '/freebsd-ufs/ { sub(/.*p/, "", $3); print $3; exit }')
	gpart resize -i "$IDX" "$MD"
	growfs -y "/dev/${MD}p$IDX"
	mkdir -p "$WORK/mnt"
	mount "/dev/${MD}p$IDX" "$WORK/mnt"
	cp -R "$PAY/bsdswitch" "$WORK/mnt/"
	cp "$PAY/etc/installerconfig" "$WORK/mnt/etc/installerconfig"
	umount "$WORK/mnt"
	mdconfig -d -u "$MD"
	trap - EXIT
	ls -l "$IMG"
fi

[ $KEEP = 1 ] || rm -rf "$WORK/root" "$WORK/payload" "$WORK/esp" "$WORK/bootimg" "$WORK/mnt"
msg "done"
echo "Proxmox: upload $(basename "$ISO") to ISO storage and boot the VM from it."
[ $MEMSTICK = 1 ] && echo "USB: dd if=<memstick.img> of=/dev/daX bs=1m conv=sync"
exit 0
