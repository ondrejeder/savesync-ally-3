//go:build linux

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const serviceName = "savesync-ally3.service"

// AttachConsole is only needed on Windows.
func AttachConsole() {}

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// StartDaemon prefers the systemd user service when installed, so the daemon
// lives in its own cgroup and is restarted on crash or self-update.
func StartDaemon(exe string) error {
	if path, err := servicePath(); err == nil {
		if _, err := os.Stat(path); err == nil {
			if err := exec.Command("systemctl", "--user", "start", serviceName).Run(); err == nil {
				return nil
			}
		}
	}
	cmd := exec.Command(exe, "run")
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// StopService stops the systemd user service so it does not restart the
// daemon while it is being replaced.
func StopService() {
	_ = exec.Command("systemctl", "--user", "stop", serviceName).Run()
}

func findBrowsers(data string) []browser {
	profile := filepath.Join(data, "window")
	var out []browser
	for _, name := range []string{
		"chromium", "chromium-browser", "google-chrome-stable", "google-chrome",
		"brave-browser", "brave", "microsoft-edge-stable", "microsoft-edge", "vivaldi",
	} {
		if path, err := exec.LookPath(name); err == nil {
			out = append(out, browser{name: name, command: []string{path}, profile: profile})
		}
	}
	home, _ := os.UserHomeDir()
	// Flatpak browsers can only write inside their own ~/.var/app/<id>.
	for _, id := range []string{
		"org.chromium.Chromium", "com.google.Chrome", "com.brave.Browser",
		"com.microsoft.Edge", "io.github.ungoogled_software.ungoogled_chromium",
	} {
		if flatpakInstalled(id) {
			out = append(out, browser{
				name:    id,
				command: []string{"flatpak", "run", id},
				profile: filepath.Join(home, ".var", "app", id, "savesync-window"),
			})
		}
	}
	if path, err := exec.LookPath("firefox"); err == nil {
		out = append(out, browser{name: "firefox", command: []string{path}, profile: filepath.Join(data, "window-firefox"), firefox: true})
	}
	if flatpakInstalled("org.mozilla.firefox") {
		out = append(out, browser{
			name:    "org.mozilla.firefox",
			command: []string{"flatpak", "run", "org.mozilla.firefox"},
			profile: filepath.Join(home, ".var", "app", "org.mozilla.firefox", "savesync-window"),
			firefox: true,
		})
	}
	return out
}

func flatpakInstalled(id string) bool {
	if _, err := exec.LookPath("flatpak"); err != nil {
		return false
	}
	return exec.Command("flatpak", "info", "--show-ref", id).Run() == nil
}

func openDefault(url string) error {
	cmd := exec.Command("xdg-open", url)
	detach(cmd)
	return cmd.Start()
}

// InstallIntegration creates and enables the systemd user service and a desktop entry.
func InstallIntegration(exe string) error {
	svc, err := servicePath()
	if err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=SaveSync Ally 3
After=network-online.target

[Service]
Type=simple
ExecStart=%q run
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, exe)
	if err := writeFile(svc, unit, 0o644); err != nil {
		return err
	}

	data, err := DataDir()
	if err != nil {
		return err
	}
	icon := filepath.Join(data, "icon.svg")
	if err := writeFile(icon, IconSVG, 0o644); err != nil {
		return err
	}
	desktop, err := desktopPath()
	if err != nil {
		return err
	}
	entry := fmt.Sprintf(`[Desktop Entry]
Version=1.0
Type=Application
Name=SaveSync Ally
Comment=Synchronizace uložených pozic her
Exec=%q open
Icon=%s
Terminal=false
Categories=Utility;Game;
StartupWMClass=SaveSyncAlly
`, exe, icon)
	if err := writeFile(desktop, entry, 0o755); err != nil {
		return err
	}

	if err := exec.Command("systemctl", "--user", "daemon-reload").Run(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if out, err := exec.Command("systemctl", "--user", "enable", serviceName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable: %v: %s", err, out)
	}
	return nil
}

// UninstallIntegration disables the service and removes the desktop entry.
func UninstallIntegration() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", serviceName).Run()
	if svc, err := servicePath(); err == nil {
		_ = os.Remove(svc)
	}
	if desktop, err := desktopPath(); err == nil {
		_ = os.Remove(desktop)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

// AutostartEnabled reports whether the systemd user service is enabled.
func AutostartEnabled() bool {
	return exec.Command("systemctl", "--user", "is-enabled", "--quiet", serviceName).Run() == nil
}

func servicePath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", serviceName), nil
}

func desktopPath() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "applications", "savesync-ally3.desktop"), nil
}

func writeFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), mode)
}

// Places are quick links in the folder picker. Steam's compatdata folders
// hold the Proton prefixes where Windows games keep their saves.
func Places() []Place {
	home, _ := os.UserHomeDir()
	places := []Place{
		{"Domů", home, "home"},
		{"Dokumenty", filepath.Join(home, "Documents"), "doc"},
		{".local/share", filepath.Join(home, ".local", "share"), "folder"},
		{".config", filepath.Join(home, ".config"), "folder"},
		{"Proton prefixy", filepath.Join(home, ".local", "share", "Steam", "steamapps", "compatdata"), "steam"},
		{"Proton prefixy", filepath.Join(home, ".steam", "steam", "steamapps", "compatdata"), "steam"},
		{"Proton prefixy (Flatpak)", filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam", "steamapps", "compatdata"), "steam"},
	}
	if cards, err := filepath.Glob("/run/media/*/*/steamapps/compatdata"); err == nil {
		for _, c := range cards {
			places = append(places, Place{"Proton prefixy (SD karta)", c, "steam"})
		}
	}
	return existing(places)
}

func openFolder(path string) error {
	return openDefault(path)
}

// Notify shows a desktop notification (freedesktop notifications).
func Notify(title, body string) error {
	icon := ""
	if data, err := DataDir(); err == nil {
		icon = filepath.Join(data, "icon.svg")
	}
	if path, err := exec.LookPath("notify-send"); err == nil {
		return exec.Command(path, "-a", "SaveSync Ally", "-i", icon, title, body).Start()
	}
	return exec.Command("gdbus", "call", "--session",
		"--dest", "org.freedesktop.Notifications",
		"--object-path", "/org/freedesktop/Notifications",
		"--method", "org.freedesktop.Notifications.Notify",
		"SaveSync Ally", "0", icon, title, body, "[]", "{}", "10000").Start()
}

// DefaultScanInterval is how often every game is fully re-scanned as a
// safety net next to the file watcher.
const DefaultScanInterval = 30 * time.Minute
