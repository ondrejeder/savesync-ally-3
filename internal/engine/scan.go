package engine

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// TempSuffix marks partially written downloads; such files are never synced.
const TempSuffix = ".savesync-tmp"

// ErrRootMissing means the game's folder does not exist on this device.
var ErrRootMissing = errors.New("game folder not found")

// Scan hashes every file under root that matches include (all files when
// include is empty). Keys are slash-separated paths relative to root.
func Scan(root string, include []string) (map[string]LocalFile, error) {
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil, ErrRootMissing
	}
	out := map[string]LocalFile{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // unreadable entry: skip, do not abort the whole game
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// Names that could not round-trip through the cloud (e.g. a backslash
		// in a Linux file name) are skipped instead of re-uploaded forever.
		if strings.HasSuffix(rel, TempSuffix) || !safeRel(rel) || !Included(rel, include) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		sum, err := FileMD5(p)
		if err != nil {
			return err
		}
		out[rel] = LocalFile{MD5: sum, Size: info.Size(), MTime: info.ModTime().Unix()}
		return nil
	})
	return out, err
}

// Included matches a relative path against glob patterns. A pattern without
// "/" matches the file name in any folder; with "/" it matches the full path.
func Included(rel string, include []string) bool {
	if len(include) == 0 {
		return true
	}
	base := path.Base(rel)
	for _, pattern := range include {
		pattern = strings.TrimSpace(filepath.ToSlash(pattern))
		if pattern == "" {
			continue
		}
		target := rel
		if !strings.Contains(pattern, "/") {
			target = base
		}
		if ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(target)); ok {
			return true
		}
	}
	return false
}

func FileMD5(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
