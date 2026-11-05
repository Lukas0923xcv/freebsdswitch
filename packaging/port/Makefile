# Port skeleton for the FreeBSD ports tree (category net-mgmt).
#
# Before committing to a ports tree, generate the Go module dependency
# tuples with:  make gomod-vendor  and paste them into GH_TUPLE.
# For a quick local package without a ports tree use `make pkg` in the
# bsdswitch source tree instead.

PORTNAME=	bsdswitch
DISTVERSIONPREFIX=	v
DISTVERSION=	0.1.0
CATEGORIES=	net-mgmt

MAINTAINER=	ports@FreeBSD.org
COMMENT=	Managed Ethernet switch (web UI, CLI, console menu) on if_bridge
WWW=		https://github.com/Lukas0923xcv/freebsdswitch

LICENSE=	BSD2CLAUSE

ONLY_FOR_ARCHS=	aarch64 amd64
ONLY_FOR_ARCHS_REASON=	tested on amd64, cross-compiled for arm64

USES=		go:modules
USE_GITHUB=	yes
GH_ACCOUNT=	Lukas0923xcv
GH_PROJECT=	freebsdswitch
# GH_TUPLE=	(output of `make gomod-vendor`)

GO_TARGET=	./cmd/bsdswitch
GO_LDFLAGS=	-s -w -X github.com/Lukas0923xcv/freebsdswitch/internal/version.Version=${DISTVERSION}

USE_RC_SUBR=	bsdswitchd
SUB_FILES=	pkg-message

USERS=		_bsdswitch
GROUPS=		_bsdswitch

.include <bsd.port.pre.mk>

.if ${OSVERSION} < 1500000
IGNORE=		requires FreeBSD 15.0 or later (if_bridge VLAN filtering)
.endif

do-install:
	${INSTALL_PROGRAM} ${WRKDIR}/bin/bsdswitch ${STAGEDIR}${PREFIX}/sbin/bsdswitch
	${RLN} ${STAGEDIR}${PREFIX}/sbin/bsdswitch ${STAGEDIR}${PREFIX}/sbin/bsdswitchd
	${RLN} ${STAGEDIR}${PREFIX}/sbin/bsdswitch ${STAGEDIR}${PREFIX}/sbin/bsdswitchctl
	${RLN} ${STAGEDIR}${PREFIX}/sbin/bsdswitch ${STAGEDIR}${PREFIX}/sbin/bsdswitch-console
	${MKDIR} ${STAGEDIR}${PREFIX}/libexec
	${RLN} ${STAGEDIR}${PREFIX}/sbin/bsdswitch ${STAGEDIR}${PREFIX}/libexec/bsdswitch-privd
	${MKDIR} ${STAGEDIR}${DATADIR} ${STAGEDIR}${DOCSDIR}
	${INSTALL_SCRIPT} ${WRKSRC}/scripts/install.sh ${STAGEDIR}${DATADIR}/install.sh
	${INSTALL_DATA} ${WRKSRC}/docs/config.yaml.sample ${STAGEDIR}${DATADIR}/config.yaml.sample
	${INSTALL_DATA} ${WRKSRC}/README.md ${STAGEDIR}${DOCSDIR}/README.md

.include <bsd.port.post.mk>
