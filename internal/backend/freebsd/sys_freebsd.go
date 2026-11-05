//go:build freebsd

package freebsd

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
)

type cpuSampler struct {
	mu   sync.Mutex
	last []uint64
	pct  float64
	when time.Time
}

func (l *Local) Counters(ctx context.Context) (map[string]backend.Counters, error) {
	rib, err := route.FetchRIB(unix.AF_UNSPEC, route.RIBTypeInterface, 0)
	if err != nil {
		return nil, err
	}
	byIdx := ParseIfList(rib)
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := map[string]backend.Counters{}
	for _, i := range ifs {
		if c, ok := byIdx[i.Index]; ok {
			out[i.Name] = c
		}
	}
	return out, nil
}

func (l *Local) SysInfo(ctx context.Context) (backend.SysInfo, error) {
	var s backend.SysInfo
	s.Hostname, _ = unix.Sysctl("kern.hostname")
	rel, _ := unix.Sysctl("kern.osrelease")
	s.OSRelease = rel
	if v, err := run(ctx, "/bin/freebsd-version", "-u"); err == nil {
		s.OSRelease = strings.TrimSpace(v)
	}
	s.Machine, _ = unix.Sysctl("hw.machine_arch")
	s.CPUModel, _ = unix.Sysctl("hw.model")
	if n, err := unix.SysctlUint32("hw.ncpu"); err == nil {
		s.NCPU = int(n)
	}
	if tv, err := unix.SysctlTimeval("kern.boottime"); err == nil {
		s.UptimeSec = time.Now().Unix() - tv.Sec
	}
	if m, err := unix.SysctlUint64("hw.physmem"); err == nil {
		s.MemTotal = m
	}
	pagesize, _ := unix.SysctlUint32("hw.pagesize")
	free, _ := unix.SysctlUint32("vm.stats.vm.v_free_count")
	inact, _ := unix.SysctlUint32("vm.stats.vm.v_inactive_count")
	laundry, _ := unix.SysctlUint32("vm.stats.vm.v_laundry_count")
	avail := uint64(free+inact+laundry) * uint64(pagesize)
	if s.MemTotal > avail {
		s.MemUsed = s.MemTotal - avail
	}
	if raw, err := unix.SysctlRaw("vm.loadavg"); err == nil && len(raw) >= 24 {
		// struct loadavg { fixpt_t ldavg[3]; long fscale; }
		le := binary.LittleEndian
		scale := float64(le.Uint64(raw[16:24]))
		if scale > 0 {
			for i := 0; i < 3; i++ {
				s.Load[i] = float64(le.Uint32(raw[i*4:i*4+4])) / scale
			}
		}
	}
	s.CPUPercent = l.cpu.sample()
	return s, nil
}

// sample computes CPU utilisation from kern.cp_time deltas
// (user, nice, sys, intr, idle as longs).
func (c *cpuSampler) sample() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.when) < 500*time.Millisecond {
		return c.pct
	}
	raw, err := unix.SysctlRaw("kern.cp_time")
	if err != nil || len(raw) < 40 {
		return c.pct
	}
	cur := make([]uint64, 5)
	for i := range cur {
		cur[i] = binary.LittleEndian.Uint64(raw[i*8 : i*8+8])
	}
	if c.last != nil {
		var total, idle uint64
		for i := range cur {
			total += cur[i] - c.last[i]
		}
		idle = cur[4] - c.last[4]
		if total > 0 {
			c.pct = 100 * float64(total-idle) / float64(total)
		}
	}
	c.last = cur
	c.when = time.Now()
	return c.pct
}
