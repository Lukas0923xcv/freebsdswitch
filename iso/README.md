# Installer ISO

`iso/build-iso.sh` remasters the official FreeBSD release media and adds:

- `/bsdswitch/bsdswitch-<ver>.pkg`: the bsdswitch package
- `/bsdswitch/pkg.pkg`: pkg itself, so the target can be bootstrapped
  offline
- `/bsdswitch/extra/*.pkg`: optional extra packages (`-x lldpd`)
- `/bsdswitch/appliance-installer.sh`: the appliance installer
- `/etc/installerconfig`: makes the FreeBSD installer run it

When booted, FreeBSD's installer asks "Begin an installation using the
provided script?". After **Install**, the bsdswitch menu offers:

- **Install bsdswitch appliance**: asks for the disk, ZFS or UFS, console
  options, host name and root password, then installs FreeBSD and
  bsdswitch and reboots into the first-boot wizard
- **Standard FreeBSD installer**: plain `bsdinstall`
- **Shell**

The appliance install is built from the documented `bsdinstall` targets
(`zfsboot`/`scriptedpart`, `mount`, `distextract`, `bootconfig`,
`config`, `entropy`, `umount`), followed by `pkg add` of the bundled
packages and `install.sh` in a chroot.

## Build

On the FreeBSD dev VM (recommended):

```sh
gmake pkg                                    # dist/bsdswitch-<ver>.pkg
pkg install -y xorriso                       # optional; base tools also work
sh iso/build-iso.sh -p dist/bsdswitch-0.1.0.pkg -v 15.1 -m -x lldpd
```

On Linux (CI): `apt install xorriso`, then the same command without `-m`
and `-x`. Memstick images need UFS write support, which Linux lacks, and
extra packages need `pkg fetch`.

Outputs in `dist/`:

| File | Use |
|---|---|
| `bsdswitch-<ver>-freebsd15.1-amd64.iso` | Proxmox CD-ROM, IPMI virtual media |
| `bsdswitch-<ver>-freebsd15.1-amd64-memstick.img` (`-m`) | `dd` to a USB stick for real hardware |
| `…-arm64.iso` (`-a arm64`) | arm64 (UEFI) |

The download is verified against FreeBSD's `CHECKSUM.SHA256` file.
`FREEBSD_MIRROR` and `PKG_MIRROR` select other mirrors.

## How it is remastered

- **With xorriso:** `xorriso -indev disc1.iso -outdev new.iso -boot_image
  any replay -map …` adds files and replays the original El Torito
  BIOS/UEFI boot records and the hybrid USB layout unchanged.
- **Without xorriso (FreeBSD base only):** the ISO is extracted with
  `tar`, the files are added, and it is rebuilt with `makefs -t cd9660`
  using `cdboot` (BIOS) plus an EFI system partition image
  (`makefs -t msdos`), with the original volume label so `/etc/fstab`
  still matches. Use the memstick image for USB in this mode.
- **Memstick:** the official `memstick.img` is grown by 256 MB, its UFS
  partition is enlarged (`gpart resize` + `growfs`), and the payload is
  copied in.

## Status

The scripts follow the FreeBSD release tooling but **have not been
boot-tested yet**: freebsd.org is not reachable from the environment they
were written in. Please test the first ISO in a Proxmox VM before using it
on hardware. The CI workflow builds the ISO on tags.
