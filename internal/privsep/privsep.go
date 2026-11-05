// Package privsep implements the split between the unprivileged daemon
// (bsdswitchd, user _bsdswitch) and the small root helper
// (bsdswitch-privd). The helper:
//
//   - binds the HTTPS/HTTP ports and passes the sockets to the daemon,
//   - executes only commands that pass freebsd.CheckOp (argv, no shell),
//   - writes only allowlisted files,
//   - supervises (restarts) the daemon.
//
// Reads (ifconfig output, counters, sysctls) do not need root and are done
// by the daemon itself.
package privsep

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Lukas0923xcv/freebsdswitch/internal/backend"
	"github.com/Lukas0923xcv/freebsdswitch/internal/backend/freebsd"
)

// DefaultSocket is where privd listens.
const DefaultSocket = "/var/run/bsdswitch/privd.sock"

type request struct {
	Op        backend.Op `json:"op"`
	TimeoutMs int64      `json:"timeout_ms,omitempty"`
}

type response struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// Executor is what the server side runs (freebsd.Local in production).
type Executor interface {
	Run(ctx context.Context, op backend.Op) (string, error)
	WriteFile(ctx context.Context, path string, data []byte) error
}

// Serve handles privileged requests on l until ctx is done. Every request
// is checked against the policy before execution.
func Serve(ctx context.Context, l net.Listener, ex Executor, logf func(string, ...any)) error {
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go serveConn(ctx, c, ex, logf)
	}
}

func serveConn(ctx context.Context, c net.Conn, ex Executor, logf func(string, ...any)) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	enc := json.NewEncoder(c)
	for sc.Scan() {
		var req request
		var resp response
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			resp.Error = "bad request"
			enc.Encode(resp)
			return
		}
		if err := freebsd.CheckOp(req.Op); err != nil {
			logf("privd: DENIED %s: %v", req.Op.String(), err)
			resp.Error = err.Error()
			enc.Encode(resp)
			continue
		}
		timeout := 30 * time.Second
		if req.TimeoutMs > 0 && req.TimeoutMs < int64(5*time.Minute/time.Millisecond) {
			timeout = time.Duration(req.TimeoutMs) * time.Millisecond
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		var err error
		if req.Op.IsFile() {
			err = ex.WriteFile(rctx, req.Op.Path, []byte(req.Op.Content))
		} else {
			resp.Output, err = ex.Run(rctx, req.Op)
		}
		cancel()
		if err != nil {
			resp.Error = err.Error()
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

// Client is the daemon side: reads locally, writes through privd.
type Client struct {
	*freebsd.Local
	path string
	mu   sync.Mutex
	conn net.Conn
	rd   *bufio.Reader
}

// NewClient connects lazily to the privd socket.
func NewClient(path string) *Client { return &Client{Local: freebsd.New(), path: path} }

func (c *Client) call(ctx context.Context, req request) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if c.conn == nil {
			conn, err := net.DialTimeout("unix", c.path, 5*time.Second)
			if err != nil {
				return "", fmt.Errorf("privd unavailable: %w", err)
			}
			c.conn = conn
			c.rd = bufio.NewReader(conn)
		}
		b, _ := json.Marshal(req)
		dl := time.Now().Add(35 * time.Second)
		if req.TimeoutMs > 0 {
			dl = time.Now().Add(time.Duration(req.TimeoutMs)*time.Millisecond + 5*time.Second)
		}
		c.conn.SetDeadline(dl)
		if _, err := c.conn.Write(append(b, '\n')); err != nil {
			c.conn.Close()
			c.conn = nil
			continue
		}
		line, err := c.rd.ReadBytes('\n')
		if err != nil {
			c.conn.Close()
			c.conn = nil
			if attempt == 0 && errors.Is(err, net.ErrClosed) {
				continue
			}
			return "", fmt.Errorf("privd: %w", err)
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil {
			return "", fmt.Errorf("privd: bad response")
		}
		if resp.Error != "" {
			return resp.Output, errors.New(resp.Error)
		}
		return resp.Output, nil
	}
	return "", fmt.Errorf("privd: connection failed")
}

func (c *Client) Run(ctx context.Context, op backend.Op) (string, error) {
	req := request{Op: op}
	if op.Argv[0] == "tcpdump" {
		req.TimeoutMs = 65000
		if dl, ok := ctx.Deadline(); ok {
			req.TimeoutMs = time.Until(dl).Milliseconds()
		}
	}
	return c.call(ctx, req)
}

func (c *Client) WriteFile(ctx context.Context, path string, data []byte) error {
	_, err := c.call(ctx, request{Op: backend.Op{Path: path, Content: string(data)}})
	return err
}

// Interface assertion.
var _ backend.Backend = (*Client)(nil)
