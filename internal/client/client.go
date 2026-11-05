// Package client is a small REST client for /api/v1 used by bsdswitchctl
// and the console menu. It talks to the local unix socket (as root) or to
// a remote switch over HTTPS with an API token or username/password.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

type Client struct {
	base  string
	hc    *http.Client
	token string
	csrf  string
}

// APIError is a non-2xx response.
type APIError struct {
	Status int
	Msg    string
	Fields []struct {
		Path    string `json:"path"`
		Message string `json:"message"`
	}
}

func (e *APIError) Error() string {
	var sb strings.Builder
	sb.WriteString(e.Msg)
	for _, f := range e.Fields {
		fmt.Fprintf(&sb, "\n  %s: %s", f.Path, f.Message)
	}
	return sb.String()
}

// Unix connects to the local API socket.
func Unix(path string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}
	return &Client{base: "http://local", hc: &http.Client{Transport: tr, Timeout: 120 * time.Second}}
}

// Remote connects over HTTPS. insecure skips certificate verification
// (self-signed default certificate).
func Remote(url, token string, insecure bool) *Client {
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure}}
	return &Client{base: strings.TrimRight(url, "/"), token: token, hc: &http.Client{Transport: tr, Jar: jar, Timeout: 120 * time.Second}}
}

// Login authenticates with username/password (cookie session).
func (c *Client) Login(user, pw string) error {
	var resp struct {
		CSRF       string `json:"csrf"`
		MustChange bool   `json:"must_change"`
	}
	if err := c.Do("POST", "/api/v1/auth/login", map[string]string{"username": user, "password": pw}, &resp); err != nil {
		return err
	}
	c.csrf = resp.CSRF
	if resp.MustChange {
		return fmt.Errorf("password change required: log in to the web UI first")
	}
	return nil
}

// Do performs a JSON request. body may be nil, []byte (sent as YAML) or
// any JSON-encodable value. out may be nil, *[]byte (raw) or a pointer.
func (c *Client) Do(method, path string, body, out any) error {
	var rd io.Reader
	ct := "application/json"
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
		ct = "application/yaml"
	default:
		js, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(js)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return err
	}
	if rd != nil {
		req.Header.Set("Content-Type", ct)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		e := &APIError{Status: resp.StatusCode}
		var env struct {
			Error  string `json:"error"`
			Fields []struct {
				Path    string `json:"path"`
				Message string `json:"message"`
			} `json:"fields"`
		}
		if json.Unmarshal(data, &env) == nil && env.Error != "" {
			e.Msg = env.Error
			e.Fields = env.Fields
		} else {
			e.Msg = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		return e
	}
	switch o := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*o = data
		return nil
	default:
		return json.Unmarshal(data, out)
	}
}

func (c *Client) Get(path string, out any) error         { return c.Do("GET", path, nil, out) }
func (c *Client) Post(path string, body, out any) error  { return c.Do("POST", path, body, out) }
func (c *Client) Patch(path string, body, out any) error { return c.Do("PATCH", path, body, out) }
