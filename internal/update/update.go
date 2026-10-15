// Package update handles releases: the manifest published to Drive, version
// comparison and replacing the running binary.
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ManifestName is the release manifest stored next to the binaries in Drive.
const ManifestName = "latest.json"

// Manifest describes the newest release.
type Manifest struct {
	Version   string           `json:"version"`
	Notes     string           `json:"notes,omitempty"`
	Published time.Time        `json:"published"`
	Files     map[string]Asset `json:"files"` // platform key -> binary
}

// Asset is one platform binary stored in Drive.
type Asset struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Platform is the key of the running binary in Manifest.Files.
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// AssetName is the file name used for a platform's binary.
func AssetName(platform string) string {
	name := "savesync-" + platform
	if strings.HasPrefix(platform, "windows-") {
		name += ".exe"
	}
	return name
}

// PlatformFromName is the inverse of AssetName ("" if it is not a binary).
func PlatformFromName(name string) string {
	name = strings.TrimSuffix(name, ".exe")
	if !strings.HasPrefix(name, "savesync-") {
		return ""
	}
	p := strings.TrimPrefix(name, "savesync-")
	if !strings.Contains(p, "-") {
		return ""
	}
	return p
}

// Newer reports whether version a is newer than b. Versions are dotted
// numbers ("3.0.1", optional "v"); anything else (development builds such as
// "2e9d441-dirty") counts as 0.0.0.
func Newer(a, b string) bool {
	pa, pb := parse(a), parse(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

// IsRelease reports whether v is a plain release version (no dev suffix).
func IsRelease(v string) bool {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

func parse(v string) [3]int {
	var out [3]int
	if !IsRelease(v) {
		return out
	}
	for i, p := range strings.Split(strings.TrimPrefix(v, "v"), ".") {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

// FileSHA256 returns the hex SHA-256 of a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Replace swaps the binary at exe for the verified file at newBin. The old
// binary is kept as exe+".old" for manual rollback. Renaming a running
// binary is allowed on both Windows and Linux.
func Replace(exe, newBin string) error {
	old := exe + ".old"
	_ = os.Remove(old)
	staged := exe + ".new"
	if err := copyFile(newBin, staged); err != nil {
		return err
	}
	if err := os.Rename(exe, old); err != nil {
		os.Remove(staged)
		return fmt.Errorf("přejmenování staré verze selhalo: %w", err)
	}
	if err := os.Rename(staged, exe); err != nil {
		_ = os.Rename(old, exe) // put the old binary back
		return fmt.Errorf("nová verze se nepodařilo umístit: %w", err)
	}
	return nil
}

// CleanupOld removes the rollback copy once the new version runs fine.
func CleanupOld(exe string) {
	_ = os.Remove(exe + ".old")
	_ = os.Remove(exe + ".new")
}

// Verify checks size and checksum of a downloaded asset.
func Verify(path string, a Asset) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if a.Size > 0 && st.Size() != a.Size {
		return fmt.Errorf("stažená aktualizace má špatnou velikost (%d místo %d)", st.Size(), a.Size)
	}
	sum, err := FileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, a.SHA256) {
		return errors.New("stažená aktualizace nesouhlasí s kontrolním součtem")
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
