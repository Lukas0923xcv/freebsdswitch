// Package webui embeds the built web UI (web/ -> internal/webui/dist via
// `make web`). The built files are committed so that the Go build (and the
// FreeBSD port) does not need Node.js.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the UI root, or nil if the UI was not built.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
