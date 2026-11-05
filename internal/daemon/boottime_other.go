//go:build !freebsd

package daemon

func bootTime() string { return "" }
