package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/drive"
	"github.com/ondrejeder/savesync-ally-3/internal/store"
	"github.com/ondrejeder/savesync-ally-3/internal/web"
)

const (
	// hintCacheTTL: the cloud file list is reused while the setup dialog is open.
	hintCacheTTL = 2 * time.Minute
	// checkFiles: cloud files compared with a chosen folder.
	checkFiles = 300
	// probeFiles / probeDirs bound the search for a better folder.
	probeFiles = 20
	probeDirs  = 200
)

type hintCache struct {
	mu    sync.Mutex
	files map[string]hintFiles
}

type hintFiles struct {
	rels []string
	at   time.Time
}

// publishPath records where this device keeps the game, so other devices
// can show it when the game is set up there. Errors are only logged.
func (a *App) publishPath(ctx context.Context, game store.Game) {
	device := a.store.Config().Device
	want := drive.DevicePath{Root: displayRoot(game.Root), Include: game.Include, OS: runtime.GOOS}
	a.mu.Lock()
	known := false
	for _, cg := range a.cloudGames {
		if cg.ID == game.ID {
			cur, ok := cg.Devices[device]
			known = ok && cur.Root == want.Root && cur.OS == want.OS && slices.Equal(cur.Include, want.Include)
		}
	}
	a.mu.Unlock()
	if known {
		return
	}
	want.Updated = time.Now().UTC()
	devices, err := a.cloud.SetDevicePath(ctx, game.ID, device, want)
	if err != nil {
		a.Log.Printf("publish path %s: %v", game.Name, err)
		return
	}
	a.mu.Lock()
	for i := range a.cloudGames {
		if a.cloudGames[i].ID == game.ID {
			a.cloudGames[i].Devices = devices
		}
	}
	a.mu.Unlock()
}

// Hint describes how the other devices set up a cloud game and, when root
// is given, whether that folder matches the files in the cloud.
func (a *App) Hint(id, root string) (web.Hint, error) {
	var hint web.Hint
	a.mu.Lock()
	var folder *drive.GameFolder
	for i := range a.cloudGames {
		if a.cloudGames[i].ID == id {
			cg := a.cloudGames[i]
			folder = &cg
		}
	}
	a.mu.Unlock()
	if folder == nil {
		return hint, errors.New("Hra v cloudu nebyla nalezena.")
	}
	device := a.store.Config().Device
	for name, d := range folder.Devices {
		if name == device {
			continue
		}
		h := web.DeviceHint{Device: name, Root: d.Root, Include: d.Include, OS: d.OS}
		if !d.Updated.IsZero() {
			h.Updated = d.Updated.Format(time.RFC3339)
		}
		if d.OS == runtime.GOOS {
			if p := expandRoot(d.Root); isDir(p) {
				h.Local = p
			}
		}
		hint.Devices = append(hint.Devices, h)
	}
	sort.Slice(hint.Devices, func(i, j int) bool { return hint.Devices[i].Updated > hint.Devices[j].Updated })

	rels, err := a.cloudRels(id)
	if err != nil {
		return hint, err
	}
	hint.Files = len(rels)
	hint.Sample = sample(rels, 2)
	if root = strings.TrimSpace(root); root != "" && len(rels) > 0 {
		hint.Check = checkPath(filepath.Clean(root), rels)
	}
	return hint, nil
}

// cloudRels returns the relative paths of a game's cloud files, sorted.
func (a *App) cloudRels(id string) ([]string, error) {
	a.hints.mu.Lock()
	cached, ok := a.hints.files[id]
	a.hints.mu.Unlock()
	if ok && time.Since(cached.at) < hintCacheTTL {
		return cached.rels, nil
	}
	if err := a.requireDrive(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	files, err := a.cloud.ListFiles(ctx, id)
	if err != nil {
		return nil, err
	}
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	a.hints.mu.Lock()
	if a.hints.files == nil {
		a.hints.files = map[string]hintFiles{}
	}
	a.hints.files[id] = hintFiles{rels: rels, at: time.Now()}
	a.hints.mu.Unlock()
	return rels, nil
}

// sample picks the n deepest paths: they show the folder structure best.
func sample(rels []string, n int) []string {
	out := append([]string(nil), rels...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Count(out[i], "/") > strings.Count(out[j], "/")
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// checkPath looks for the cloud files in root, then in its parents and
// sub-folders, to catch a folder picked one level too high or too low.
func checkPath(root string, rels []string) *web.PathCheck {
	if len(rels) > checkFiles {
		rels = rels[:checkFiles]
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) == 0 {
		return &web.PathCheck{Status: "empty"}
	}
	if n := score(root, rels); n > 0 {
		return &web.PathCheck{Status: "ok", Matched: n}
	}
	probe := rels
	if len(probe) > probeFiles {
		probe = sample(rels, probeFiles)
	}
	for dir, i := root, 0; i < 2; i++ {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		if score(parent, probe) > 0 {
			return &web.PathCheck{Status: "higher", Matched: score(parent, rels), Suggest: parent}
		}
		dir = parent
	}
	best, bestScore := "", 0
	level := []string{root}
	seen := 0
	for depth := 0; depth < 2 && seen < probeDirs; depth++ {
		var next []string
		for _, dir := range level {
			subs, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range subs {
				if !e.IsDir() || seen >= probeDirs {
					continue
				}
				seen++
				p := filepath.Join(dir, e.Name())
				if n := score(p, probe); n > bestScore {
					best, bestScore = p, n
				}
				next = append(next, p)
			}
		}
		if best != "" {
			break // prefer the shallowest match
		}
		level = next
	}
	if best != "" {
		return &web.PathCheck{Status: "deeper", Matched: score(best, rels), Suggest: best}
	}
	return &web.PathCheck{Status: "other"}
}

// score counts the cloud files that exist under dir.
func score(dir string, rels []string) int {
	n := 0
	for _, rel := range rels {
		if fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err == nil && fi.Mode().IsRegular() {
			n++
		}
	}
	return n
}

// displayRoot shortens the home directory to ~ (paths differ per user and
// machine anyway, and it keeps them readable on small screens).
func displayRoot(root string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return root
	}
	if samePath(root, home) {
		return "~"
	}
	prefix := strings.TrimRight(home, `\/`) + string(filepath.Separator)
	if len(root) > len(prefix) && samePath(root[:len(prefix)], prefix) {
		return "~" + string(filepath.Separator) + root[len(prefix):]
	}
	return root
}

// expandRoot is the inverse of displayRoot on this device.
func expandRoot(root string) string {
	if root != "~" && !strings.HasPrefix(root, "~/") && !strings.HasPrefix(root, `~\`) {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, filepath.FromSlash(strings.ReplaceAll(root[1:], `\`, "/")))
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
