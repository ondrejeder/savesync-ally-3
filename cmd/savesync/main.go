// Command savesync synchronizes game saves between devices through Google Drive.
//
//	savesync [open]    open the app window (starts the daemon if needed)
//	savesync run       run the daemon (used by autostart)
//	savesync install   install for the current user and enable autostart
//	savesync uninstall remove autostart and shortcuts
//	savesync publish   upload a release to Drive (development)
//	savesync version   print the version
package main

import (
	"fmt"
	"os"

	"github.com/ondrejeder/savesync-ally-3/internal/platform"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "0.0.0-dev"

func main() {
	platform.AttachConsole()

	cmd, args := "open", []string(nil)
	if len(os.Args) > 1 {
		cmd, args = os.Args[1], os.Args[2:]
	}

	var err error
	switch cmd {
	case "open":
		err = cmdOpen()
	case "run":
		err = cmdRun(args)
	case "install":
		err = cmdInstall()
	case "uninstall":
		err = cmdUninstall()
	case "publish":
		err = cmdPublish(args)
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "savesync:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`SaveSync Ally ` + version + `

Použití:
  savesync [open]      otevře okno aplikace (případně spustí daemon)
  savesync run [--demo] spustí daemon (sync + UI server)
  savesync install     nainstaluje aplikaci pro aktuálního uživatele
  savesync uninstall   odebere autostart a zástupce (data zůstanou)
  savesync publish --notes "…" <dist>  nahraje release na Drive (vývoj)
  savesync version     vypíše verzi
`)
}
