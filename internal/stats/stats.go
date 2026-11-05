// Package stats samples interface counters once per second and keeps the
// last hour of throughput per interface in RAM.
package stats

import (
	"context"
	"sync"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
)

// Window is how many samples are kept (1/s => 1 h).
const Window = 3600

// Sample is the rate over one interval.
type Sample struct {
	T     int64   `json:"t"` // unix seconds
	RxBps float64 `json:"rx_bps"`
	TxBps float64 `json:"tx_bps"`
	RxPps float64 `json:"rx_pps"`
	TxPps float64 `json:"tx_pps"`
}

type ring struct {
	buf  [Window]Sample
	n    int
	head int
}

func (r *ring) add(s Sample) {
	r.buf[r.head] = s
	r.head = (r.head + 1) % Window
	if r.n < Window {
		r.n++
	}
}

func (r *ring) since(t int64) []Sample {
	out := make([]Sample, 0, r.n)
	start := (r.head - r.n + Window) % Window
	for i := 0; i < r.n; i++ {
		s := r.buf[(start+i)%Window]
		if s.T > t {
			out = append(out, s)
		}
	}
	return out
}

// Collector is safe for concurrent use.
type Collector struct {
	be       backend.Backend
	mu       sync.Mutex
	last     map[string]backend.Counters
	lastT    time.Time
	base     map[string]backend.Counters // reset baselines
	cur      map[string]backend.Counters
	hist     map[string]*ring
	latest   map[string]Sample
	subs     map[chan map[string]Sample]bool
	Interval time.Duration
}

func New(be backend.Backend) *Collector {
	return &Collector{be: be, last: map[string]backend.Counters{}, base: map[string]backend.Counters{},
		cur: map[string]backend.Counters{}, hist: map[string]*ring{}, latest: map[string]Sample{},
		subs: map[chan map[string]Sample]bool{}, Interval: time.Second}
}

// Run samples until ctx is done.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	c.Poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Poll(ctx)
		}
	}
}

func rate(cur, prev uint64, dt float64) float64 {
	if cur < prev || dt <= 0 {
		return 0 // counter wrapped or interface re-created
	}
	return float64(cur-prev) / dt
}

// Poll takes one sample.
func (c *Collector) Poll(ctx context.Context) {
	cnt, err := c.be.Counters(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	dt := now.Sub(c.lastT).Seconds()
	snap := map[string]Sample{}
	for name, v := range cnt {
		c.cur[name] = v
		if p, ok := c.last[name]; ok && !c.lastT.IsZero() {
			s := Sample{T: now.Unix(),
				RxBps: rate(v.RxBytes, p.RxBytes, dt) * 8, TxBps: rate(v.TxBytes, p.TxBytes, dt) * 8,
				RxPps: rate(v.RxPackets, p.RxPackets, dt), TxPps: rate(v.TxPackets, p.TxPackets, dt)}
			r := c.hist[name]
			if r == nil {
				r = &ring{}
				c.hist[name] = r
			}
			r.add(s)
			c.latest[name] = s
			snap[name] = s
		}
	}
	c.last = cnt
	c.lastT = now
	subs := make([]chan map[string]Sample, 0, len(c.subs))
	for ch := range c.subs {
		subs = append(subs, ch)
	}
	c.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// Subscribe receives every new sample set.
func (c *Collector) Subscribe() chan map[string]Sample {
	ch := make(chan map[string]Sample, 4)
	c.mu.Lock()
	c.subs[ch] = true
	c.mu.Unlock()
	return ch
}

func (c *Collector) Unsubscribe(ch chan map[string]Sample) {
	c.mu.Lock()
	delete(c.subs, ch)
	c.mu.Unlock()
}

func sub(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return a - b
}

// Counters returns counters relative to the last reset.
func (c *Collector) Counters(name string) (backend.Counters, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.cur[name]
	if !ok {
		return v, false
	}
	b := c.base[name]
	v.RxPackets, v.RxBytes = sub(v.RxPackets, b.RxPackets), sub(v.RxBytes, b.RxBytes)
	v.RxErrors, v.RxDrops = sub(v.RxErrors, b.RxErrors), sub(v.RxDrops, b.RxDrops)
	v.TxPackets, v.TxBytes = sub(v.TxPackets, b.TxPackets), sub(v.TxBytes, b.TxBytes)
	v.TxErrors, v.TxDrops = sub(v.TxErrors, b.TxErrors), sub(v.TxDrops, b.TxDrops)
	v.RxMcast, v.TxMcast, v.Collisions = sub(v.RxMcast, b.RxMcast), sub(v.TxMcast, b.TxMcast), sub(v.Collisions, b.Collisions)
	return v, true
}

// Reset sets the counter baseline to "now". FreeBSD has no generic way
// to zero NIC counters, so this is done in software (kernel counters keep
// counting; `netstat -i` shows the raw values).
func (c *Collector) Reset(names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range names {
		c.base[n] = c.cur[n]
	}
}

// History returns samples newer than since (unix seconds).
func (c *Collector) History(name string, since int64) []Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.hist[name]
	if r == nil {
		return []Sample{}
	}
	return r.since(since)
}

// Latest returns the last sample of every interface.
func (c *Collector) Latest() map[string]Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Sample, len(c.latest))
	for k, v := range c.latest {
		out[k] = v
	}
	return out
}

// Total sums a set of interfaces' history (dashboard graph).
func (c *Collector) Total(names []string, since int64) []Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	agg := map[int64]*Sample{}
	var order []int64
	for _, n := range names {
		r := c.hist[n]
		if r == nil {
			continue
		}
		for _, s := range r.since(since) {
			a := agg[s.T]
			if a == nil {
				a = &Sample{T: s.T}
				agg[s.T] = a
				order = append(order, s.T)
			}
			a.RxBps += s.RxBps
			a.TxBps += s.TxBps
			a.RxPps += s.RxPps
			a.TxPps += s.TxPps
		}
	}
	// insertion order follows the first interface; sort to be safe
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && order[j] < order[j-1]; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	out := make([]Sample, 0, len(order))
	for _, t := range order {
		out = append(out, *agg[t])
	}
	return out
}
