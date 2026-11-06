package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/platform"
	"github.com/ondrejeder/savesync-ally-3/internal/web"
)

const (
	// recentBudget bounds the whole search; AppData folders can be huge.
	recentBudget = 10 * time.Second
	recentDepth  = 8
	recentMax    = 10
)

// Folders that change all the time without being saves.
var recentSkipDirs = map[string]bool{
	"temp": true, "tmp": true, "cache": true, "caches": true, "cache2": true, "shadercache": true,
	"gpucache": true, "code cache": true, "dawncache": true, "grshadercache": true, "d3dscache": true,
	"crashdumps": true, "crashpad": true, "crashes": true, "logs": true, "log": true, "htmlcache": true,
	"appcache": true, "depotcache": true, "steamapps": true, "microsoft": true, "packages": true,
	"google": true, "mozilla": true, "chromium": true, "bravesoftware": true, "discord": true,
	"spotify": true, "nvidia": true, "nvidia corporation": true, "amd": true, "intel": true,
	"node_modules": true, ".git": true, "npm-cache": true, "go-build": true, "pip": true,
	"trash": true, "flatpak": true, "baloo": true, "kscreen": true, "pulse": true, "dconf": true,
	"savesyncally3": true, "savesync-ally-3": true, "electron": true, "squirreltemp": true,
	"windows": true, "inetcache": true, "webcache": true, "connecteddevicesplatform": true,
	"sentry": true, "network": true, "indexeddb": true, "blob_storage": true, "service worker": true,
}

var recentSkipExt = map[string]bool{
	".log": true, ".tmp": true, ".etl": true, ".lock": true, ".dmp": true, ".ldb": true,
	".pma": true, ".old": true, ".lnk": true,
}

// RecentFolders finds folders with files changed in the last minutes, in
// the usual save locations (Proton prefixes included). After playing a
// game and saving, its save folder is near the top.
func (a *App) RecentFolders(minutes int) (web.RecentResult, error) {
	if minutes <= 0 || minutes > 24*60 {
		minutes = 30
	}
	since := time.Now().Add(-time.Duration(minutes) * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), recentBudget)
	defer cancel()

	// Folders that are already synced change on every download: skip them.
	var skip []string
	for _, g := range a.store.Config().Games {
		skip = append(skip, filepath.Clean(g.Root))
	}
	if dir, err := platform.DataDir(); err == nil {
		skip = append(skip, dir)
	}
	if dir, err := platform.ConfigDir(); err == nil {
		skip = append(skip, dir)
	}

	type folder struct {
		files  int
		newest time.Time
		sample []string
	}
	found := map[string]*folder{}
	labels := map[string]string{} // folder -> game name of its Proton prefix
	result := web.RecentResult{Folders: []web.RecentFolder{}}

	for _, loc := range recentLocations() {
		names := map[string]string{}
		if loc.compatdata != "" {
			names = steamGameNames(loc.compatdata)
		}
		err := filepath.WalkDir(loc.path, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if p == loc.path {
					return nil
				}
				name := strings.ToLower(d.Name())
				if recentSkipDirs[name] || (loc.steamUser && (name == "config" || name == "7")) ||
					depth(loc.path, p) > recentDepth || underAny(p, skip) || browserProfile(p) {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || noiseFile(d.Name()) {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.ModTime().Before(since) {
				return nil
			}
			dir := filepath.Dir(p)
			f := found[dir]
			if f == nil {
				f = &folder{}
				found[dir] = f
				if loc.compatdata != "" {
					if rel, err := filepath.Rel(loc.compatdata, dir); err == nil {
						id, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
						labels[dir] = names[id]
					}
				}
			}
			f.files++
			if info.ModTime().After(f.newest) {
				f.newest = info.ModTime()
			}
			if len(f.sample) < 3 {
				f.sample = append(f.sample, d.Name())
			}
			return nil
		})
		if ctx.Err() != nil || err == context.DeadlineExceeded {
			result.Partial = true
			break
		}
	}

	for dir, f := range found {
		result.Folders = append(result.Folders, web.RecentFolder{
			Path: dir, Label: labels[dir], Files: f.files,
			Modified: f.newest.Format(time.RFC3339), Sample: f.sample,
		})
	}
	sort.Slice(result.Folders, func(i, j int) bool { return result.Folders[i].Modified > result.Folders[j].Modified })
	if len(result.Folders) > recentMax {
		result.Folders = result.Folders[:recentMax]
	}
	return result, nil
}

type recentLocation struct {
	path       string
	compatdata string // set for Proton prefixes: the compatdata folder
	steamUser  bool   // Steam userdata: skip the client's own config
}

// browserProfile spots Chromium/Electron app data (browsers, Discord, …),
// which changes constantly while the app runs.
func browserProfile(dir string) bool {
	for _, marker := range []string{"Local State", "Local Storage", "Session Storage"} {
		if _, err := os.Lstat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

func noiseFile(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return recentSkipExt[filepath.Ext(lower)]
}

// recentLocations: the save locations from the folder picker, without the
// home folder and whole drives; compatdata expands to each prefix's user.
func recentLocations() []recentLocation {
	var out []recentLocation
	for _, p := range platform.Places() {
		switch {
		case p.Icon == "home" || p.Icon == "disk":
		case filepath.Base(p.Path) == "compatdata":
			users, _ := filepath.Glob(filepath.Join(p.Path, "*", "pfx", "drive_c", "users", "steamuser"))
			for _, u := range users {
				out = append(out, recentLocation{path: u, compatdata: p.Path})
			}
		default:
			out = append(out, recentLocation{path: p.Path, steamUser: filepath.Base(p.Path) == "userdata"})
		}
	}
	return out
}

func depth(base, p string) int {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return 0
	}
	return strings.Count(filepath.ToSlash(rel), "/") + 1
}

func underAny(p string, roots []string) bool {
	for _, r := range roots {
		if samePath(p, r) {
			return true
		}
		if rel, err := filepath.Rel(r, p); err == nil && !escapes(rel) {
			return true
		}
	}
	return false
}
