// Package logbuf is an in-memory ring of recent log lines with
// subscribers (for the live log view).
package logbuf

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type Line struct {
	Time time.Time `json:"time"`
	Text string    `json:"text"`
	Src  string    `json:"src"`
}

type Buffer struct {
	mu    sync.Mutex
	lines []Line
	max   int
	subs  map[chan Line]bool
	out   io.Writer
}

func New(max int, out io.Writer) *Buffer {
	return &Buffer{max: max, subs: map[chan Line]bool{}, out: out}
}

func (b *Buffer) Printf(f string, a ...any) {
	b.Add("bsdswitchd", fmt.Sprintf(f, a...))
}

func (b *Buffer) Add(src, text string) {
	l := Line{Time: time.Now(), Text: strings.TrimRight(text, "\n"), Src: src}
	b.mu.Lock()
	b.lines = append(b.lines, l)
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
	subs := make([]chan Line, 0, len(b.subs))
	for c := range b.subs {
		subs = append(subs, c)
	}
	out := b.out
	b.mu.Unlock()
	if out != nil {
		fmt.Fprintf(out, "%s %s\n", l.Time.Format(time.RFC3339), l.Text)
	}
	for _, c := range subs {
		select {
		case c <- l:
		default:
		}
	}
}

func (b *Buffer) Tail(n int) []Line {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > len(b.lines) {
		n = len(b.lines)
	}
	return append([]Line{}, b.lines[len(b.lines)-n:]...)
}

func (b *Buffer) Subscribe() chan Line {
	c := make(chan Line, 64)
	b.mu.Lock()
	b.subs[c] = true
	b.mu.Unlock()
	return c
}

func (b *Buffer) Unsubscribe(c chan Line) {
	b.mu.Lock()
	delete(b.subs, c)
	b.mu.Unlock()
}
