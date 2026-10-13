//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "SaveSyncAlly3"

// AttachConsole connects a GUI-subsystem binary to the parent terminal so CLI
// commands still print output. It is a no-op when started without a console.
func AttachConsole() {
	const attachParentProcess = ^uint32(0)
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	if r, _, _ := kernel32.NewProc("AttachConsole").Call(uintptr(attachParentProcess)); r == 0 {
		return
	}
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil && h != 0 {
		os.Stdout = os.NewFile(uintptr(h), "stdout")
	}
	if h, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE); err == nil && h != 0 {
		os.Stderr = os.NewFile(uintptr(h), "stderr")
	}
}

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// StartDaemon launches `exe run` in the background.
func StartDaemon(exe string) error {
	cmd := exec.Command(exe, "run")
	cmd.Dir = filepath.Dir(exe)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// StopService is a no-op on Windows; the daemon is stopped through the API.
func StopService() {}

func findBrowsers(data string) []browser {
	profile := filepath.Join(data, "window")
	candidates := []struct{ name, path string }{
		{"Edge", filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`)},
		{"Edge", filepath.Join(os.Getenv("ProgramFiles"), `Microsoft\Edge\Application\msedge.exe`)},
		{"Chrome", filepath.Join(os.Getenv("ProgramFiles"), `Google\Chrome\Application\chrome.exe`)},
		{"Chrome", filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`)},
	}
	var out []browser
	for _, c := range candidates {
		if _, err := os.Stat(c.path); err == nil {
			out = append(out, browser{name: c.name, command: []string{c.path}, profile: profile})
		}
	}
	return out
}

func openDefault(url string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	detach(cmd)
	return cmd.Start()
}

// InstallIntegration registers autostart and creates Start menu/desktop shortcuts.
func InstallIntegration(exe string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("autostart: %w", err)
	}
	defer k.Close()
	if err := k.SetStringValue(runValue, fmt.Sprintf(`"%s" run`, exe)); err != nil {
		return fmt.Errorf("autostart: %w", err)
	}
	for _, link := range shortcutPaths() {
		if err := createShortcut(link, exe, "open"); err != nil {
			return fmt.Errorf("shortcut %s: %w", link, err)
		}
	}
	return nil
}

// UninstallIntegration removes autostart and shortcuts.
func UninstallIntegration() error {
	if k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(runValue)
		k.Close()
	}
	for _, link := range shortcutPaths() {
		_ = os.Remove(link)
	}
	return nil
}

// AutostartEnabled reports whether the Run key points at an existing binary.
func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	return err == nil && v != ""
}

func shortcutPaths() []string {
	return []string{
		filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\SaveSync Ally.lnk`),
		filepath.Join(os.Getenv("USERPROFILE"), `Desktop\SaveSync Ally.lnk`),
	}
}

// createShortcut uses the WScript.Shell COM object through a hidden PowerShell.
func createShortcut(link, target, args string) error {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := fmt.Sprintf(
		"$s=(New-Object -ComObject WScript.Shell).CreateShortcut(%s);$s.TargetPath=%s;$s.Arguments=%s;$s.WorkingDirectory=%s;$s.IconLocation=%s;$s.Save()",
		q(link), q(target), q(args), q(filepath.Dir(target)), q(target+",0"),
	)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Places are quick links in the folder picker.
func Places() []Place {
	home := os.Getenv("USERPROFILE")
	places := []Place{
		{"Domů", home, "home"},
		{"Dokumenty", filepath.Join(home, "Documents"), "doc"},
		{"Uložené hry", filepath.Join(home, "Saved Games"), "gamepad"},
		{"AppData Roaming", os.Getenv("APPDATA"), "folder"},
		{"AppData Local", os.Getenv("LOCALAPPDATA"), "folder"},
		{"AppData LocalLow", filepath.Join(home, "AppData", "LocalLow"), "folder"},
		{"Steam userdata", filepath.Join(os.Getenv("ProgramFiles(x86)"), "Steam", "userdata"), "steam"},
	}
	for c := 'C'; c <= 'Z'; c++ {
		drive := string(c) + `:\`
		if _, err := os.Stat(drive); err == nil {
			places = append(places, Place{"Disk " + string(c) + ":", drive, "disk"})
		}
	}
	return existing(places)
}

func openFolder(path string) error {
	cmd := exec.Command("explorer.exe", path)
	return cmd.Start()
}
