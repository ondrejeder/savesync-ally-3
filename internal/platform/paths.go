// Package platform hides the OS differences: paths, autostart, opening the
// app window and starting detached processes.
package platform

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// Port is the loopback port of the daemon's UI/API server.
	Port = 45455
	// AppID identifies the daemon in /api/ping responses.
	AppID = "savesync-ally-3"
)

// ConfigDir holds settings, OAuth token and the UI session token.
func ConfigDir() (string, error) {
	if dir := os.Getenv("SAVESYNC_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if runtime.GOOS == "windows" {
		return windowsDir("APPDATA")
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "savesync-ally3"), nil
}

// DataDir holds the installed binary, logs, local backups and per-device state.
func DataDir() (string, error) {
	if dir := os.Getenv("SAVESYNC_DATA_DIR"); dir != "" {
		return dir, nil
	}
	if runtime.GOOS == "windows" {
		return windowsDir("LOCALAPPDATA")
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "savesync-ally3"), nil
}

// InstalledExe is where `savesync install` puts the binary.
func InstalledExe() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	name := "savesync"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name), nil
}

// CurrentExe returns the absolute path of the running binary.
func CurrentExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

// SamePath compares paths the way the OS does (case-insensitive on Windows).
func SamePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// CopyFile copies src to dst atomically (temp file + rename) and keeps it executable.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func windowsDir(env string) (string, error) {
	base := os.Getenv(env)
	if base == "" {
		return "", errors.New(env + " is not set")
	}
	return filepath.Join(base, "SaveSyncAlly3"), nil
}
