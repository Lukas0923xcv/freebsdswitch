package freebsd

import (
	"encoding/binary"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
)

// RTM_IFINFO message type (net/route.h).
const rtmIFINFO = 0x0e

// ParseIfList parses the output of the NET_RT_IFLIST sysctl and returns
// counters by interface index. Layout (sys/net/if.h, MI since FreeBSD 11):
//
//	struct if_msghdr { u_short msglen; u_char version; u_char type;
//	  int addrs; int flags; u_short index; u_short spare; struct if_data data; }
//	struct if_data { u8 type, physical, addrlen, hdrlen, link_state, vhid;
//	  u16 datalen; u32 mtu, metric; u64 baudrate, ipackets, ierrors,
//	  opackets, oerrors, collisions, ibytes, obytes, imcasts, omcasts,
//	  iqdrops, oqdrops, noproto, hwassist; ... }
func ParseIfList(b []byte) map[int]backend.Counters {
	out := map[int]backend.Counters{}
	le := binary.LittleEndian // amd64 and arm64 are little endian
	for len(b) >= 4 {
		msglen := int(le.Uint16(b[0:2]))
		if msglen < 4 || msglen > len(b) {
			break
		}
		m := b[:msglen]
		b = b[msglen:]
		if m[3] != rtmIFINFO || len(m) < 16+136 {
			continue
		}
		idx := int(le.Uint16(m[12:14]))
		d := m[16:]
		u64 := func(off int) uint64 { return le.Uint64(d[off : off+8]) }
		ls := int(d[4])
		out[idx] = backend.Counters{
			LinkState:  ls,
			Baudrate:   u64(16),
			RxPackets:  u64(24),
			RxErrors:   u64(32),
			TxPackets:  u64(40),
			TxErrors:   u64(48),
			Collisions: u64(56),
			RxBytes:    u64(64),
			TxBytes:    u64(72),
			RxMcast:    u64(80),
			TxMcast:    u64(88),
			RxDrops:    u64(96),
			TxDrops:    u64(104),
		}
	}
	return out
}
