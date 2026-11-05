// Command bsdswitch is a multi-call binary. It behaves as
//
//	bsdswitchd         the management daemon (REST API, web UI)
//	bsdswitch-privd    the root helper that starts and supervises bsdswitchd
//	bsdswitchctl       the Cisco-style CLI
//	bsdswitch-console  the pfSense-style console menu
//
// depending on the name it is invoked as (symlinks), or on its first
// argument (`bsdswitch daemon|privd|ctl|console`).
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Lukas0923xcv/freebsdswitch/internal/cli"
	"github.com/Lukas0923xcv/freebsdswitch/internal/console"
	"github.com/Lukas0923xcv/freebsdswitch/internal/daemon"
	"github.com/Lukas0923xcv/freebsdswitch/internal/privd"
	"github.com/Lukas0923xcv/freebsdswitch/internal/version"
	"github.com/Lukas0923xcv/freebsdswitch/internal/webui"
)

func main() {
	daemon.UI = webui.FS()
	name := filepath.Base(os.Args[0])
	args := os.Args[1:]
	if name == "bsdswitch" && len(args) > 0 {
		switch args[0] {
		case "daemon":
			name = "bsdswitchd"
		case "privd":
			name = "bsdswitch-privd"
		case "ctl":
			name = "bsdswitchctl"
		case "console":
			name = "bsdswitch-console"
		case "version", "--version", "-v":
			fmt.Printf("bsdswitch %s (%s, %s)\n", version.Version, version.Commit, version.Date)
			return
		}
		args = args[1:]
	}
	switch name {
	case "bsdswitchd":
		os.Exit(daemon.Main(args))
	case "bsdswitch-privd":
		os.Exit(privd.Main(args))
	case "bsdswitchctl":
		os.Exit(cli.Main(args))
	case "bsdswitch-console":
		os.Exit(console.Main(args))
	default:
		fmt.Fprintf(os.Stderr, "usage: bsdswitch daemon|privd|ctl|console|version [args]\n")
		os.Exit(2)
	}
}
