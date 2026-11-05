#!/bin/sh
# Build a FreeBSD package (bsdswitch-VERSION.pkg) with pkg-create(8).
# Must run on FreeBSD (it needs pkg). Cross-building for arm64 on amd64
# works: pass the arm64 binary and ARCH=aarch64.
#
# usage: packaging/mkpkg.sh BINARY VERSION [ARCH] [OUTDIR]
set -eu
BIN=$1
VERSION=$2
ARCH=${3:-$(uname -p)}
OUT=${4:-dist}
TOP=$(cd "$(dirname "$0")/.." && pwd)
MAJOR=$(freebsd-version -u | cut -d. -f1)
STAGE=$(mktemp -d /tmp/bsdswitch-pkg.XXXXXX)
trap 'rm -rf "$STAGE"' EXIT
P=$STAGE/usr/local

install -d $P/sbin $P/libexec $P/etc/rc.d $P/share/bsdswitch $P/share/doc/bsdswitch
install -m 0555 "$BIN" $P/sbin/bsdswitch
for l in bsdswitchd bsdswitchctl bsdswitch-console; do ln -sf bsdswitch $P/sbin/$l; done
ln -sf ../sbin/bsdswitch $P/libexec/bsdswitch-privd
install -m 0555 $TOP/packaging/rc.d/bsdswitchd $P/etc/rc.d/bsdswitchd
install -m 0555 $TOP/scripts/install.sh $P/share/bsdswitch/install.sh
install -m 0444 $TOP/docs/config.yaml.sample $P/share/bsdswitch/config.yaml.sample
install -m 0444 $TOP/README.md $P/share/doc/bsdswitch/README.md

{
	echo "name: bsdswitch"
	echo "version: \"$VERSION\""
	echo "origin: net-mgmt/bsdswitch"
	echo "comment: \"Managed Ethernet switch (web UI, CLI, console menu) on if_bridge\""
	echo "www: https://github.com/Lukas0923xcv/freebsdswitch"
	echo "maintainer: ports@FreeBSD.org"
	echo "prefix: /usr/local"
	echo "abi: \"FreeBSD:$MAJOR:$ARCH\""
	echo "licenselogic: single"
	echo "licenses: [BSD2CLAUSE]"
	echo "categories: [net-mgmt]"
	echo "desc: <<EOD"
	cat $TOP/packaging/pkg/pkg-descr
	echo "EOD"
	echo "scripts: {"
	echo "  pre-install: <<EOD"
	cat $TOP/packaging/pkg/pre-install
	echo "EOD"
	echo "  post-install: <<EOD"
	cat $TOP/packaging/pkg/post-install
	echo "EOD"
	echo "}"
	echo "messages: [ { message: <<EOD"
	sed -n '/message: <</,/^EOM/p' $TOP/packaging/pkg/pkg-message | sed '1d;$d'
	echo "EOD"
	echo "} ]"
} > $STAGE/+MANIFEST

PLIST=$STAGE/plist
sed 's|^|/usr/local/|' $TOP/packaging/pkg/pkg-plist > $PLIST
mkdir -p "$OUT"
pkg create -M $STAGE/+MANIFEST -p $PLIST -r $STAGE -o "$OUT"
ls -l "$OUT"/bsdswitch-$VERSION.pkg
