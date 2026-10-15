package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// browser is a way to launch a browser binary, optionally through flatpak.
type browser struct {
	name    string
	command []string // command prefix, e.g. {"msedge.exe"} or {"flatpak", "run", "org.chromium.Chromium"}
	profile string   // --user-data-dir / firefox profile location
	firefox bool
}

func (b browser) args(url string) []string {
	if b.firefox {
		return []string{"--new-instance", "--profile", b.profile, "--kiosk", url}
	}
	return []string{
		"--app=" + url,
		"--user-data-dir=" + b.profile,
		"--window-size=1100,780",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate",
		"--class=SaveSyncAlly",
	}
}

// launch starts the browser detached from this process.
func (b browser) launch(url string) error {
	if err := os.MkdirAll(b.profile, 0o755); err != nil {
		return err
	}
	argv := append(append([]string{}, b.command[1:]...), b.args(url)...)
	cmd := exec.Command(b.command[0], argv...)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", b.name, err)
	}
	return cmd.Process.Release()
}

// OpenURL opens url in the user's default browser (e.g. Google sign-in,
// where the user is already logged in).
func OpenURL(url string) error {
	return openDefault(url)
}

// OpenAppWindow opens url as a standalone app window, falling back to the
// default browser when no suitable browser is found.
func OpenAppWindow(url string) error {
	data, err := DataDir()
	if err != nil {
		return err
	}
	for _, b := range findBrowsers(data) {
		if err := b.launch(url); err == nil {
			return nil
		}
	}
	return openDefault(url)
}

// StartDetached starts a background process that outlives this one.
func StartDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
