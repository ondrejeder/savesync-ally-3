package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/store"
	"github.com/ondrejeder/savesync-ally-3/internal/web"
)

// A layout marker tells the other devices that a device replaced the cloud
// after moving the game to a different folder or filter. It is stored in
// the game folder's appProperties as
//
//	device|time[|p][|i][|d=rel/path][|u=N]
//
// p: still uploading (the others wait), i: the filter changed (the others
// take the new one), d / u: the new folder is rel below / N levels above the
// old one, so the others can follow on their own. Without d/u (and i) the
// change cannot be mirrored and the others ask the user.
type layoutMark struct {
	by, at  string
	pending bool
	include bool
	down    string // slash-separated
	up      int
}

// maxShift: how many folder levels a change may span and still be followed
// automatically.
const maxShift = 2

// layoutValueMax: Drive allows 124 bytes per property, key included.
const layoutValueMax = 124 - len("layout")

func parseLayout(raw string) layoutMark {
	parts := strings.Split(raw, "|")
	m := layoutMark{by: parts[0]}
	if len(parts) > 1 {
		m.at = parts[1]
	}
	for _, p := range parts[min(2, len(parts)):] {
		switch {
		case p == "p":
			m.pending = true
		case p == "i":
			m.include = true
		case strings.HasPrefix(p, "d="):
			m.down = p[2:]
		case strings.HasPrefix(p, "u="):
			m.up, _ = strconv.Atoi(p[2:])
		}
	}
	return m
}

// id identifies one change; the flags may change while it is in progress.
func (m layoutMark) id() string { return m.by + "|" + m.at }

func layoutID(raw string) string {
	if raw == "" {
		return ""
	}
	return parseLayout(raw).id()
}

func (m layoutMark) String() string {
	s := m.id()
	if m.pending {
		s += "|p"
	}
	if m.include {
		s += "|i"
	}
	if m.down != "" {
		s += "|d=" + m.down
	}
	if m.up > 0 {
		s += "|u=" + strconv.Itoa(m.up)
	}
	return s
}

// newLayout describes a change from (oldRoot, oldInclude) to (root, include).
func newLayout(device, oldRoot, root string, includeChanged bool) layoutMark {
	m := layoutMark{by: device, at: time.Now().UTC().Format(time.RFC3339Nano), pending: true, include: includeChanged}
	m.down, m.up = shiftBetween(oldRoot, root)
	if len(m.String()) > layoutValueMax {
		m.down, m.up, m.include = "", 0, false // too long: the others ask
	}
	return m
}

// shiftBetween returns how root lies relative to oldRoot: a relative path
// below it, or a number of levels above it (both up to maxShift).
func shiftBetween(oldRoot, root string) (down string, up int) {
	oldRoot, root = filepath.Clean(oldRoot), filepath.Clean(root)
	if samePath(oldRoot, root) {
		return "", 0
	}
	if rel, err := filepath.Rel(oldRoot, root); err == nil && !escapes(rel) {
		if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) <= maxShift {
			return strings.Join(parts, "/"), 0
		}
		return "", 0
	}
	if rel, err := filepath.Rel(root, oldRoot); err == nil && !escapes(rel) {
		if n := len(strings.Split(filepath.ToSlash(rel), "/")); n <= maxShift {
			return "", n
		}
	}
	return "", 0
}

func escapes(rel string) bool {
	return rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// followLayout applies another device's folder change to this device's
// game. It succeeds only when the change is a known shift that lands on a
// sensible folder here; the caller then syncs normally (nothing is deleted,
// differences become conflicts).
func (a *App) followLayout(game store.Game, m layoutMark, raw string) (store.Game, bool) {
	if !m.include && m.down == "" && m.up == 0 {
		return game, false
	}
	root := filepath.Clean(game.Root)
	if !isDir(root) {
		return game, false
	}
	switch {
	case m.down != "":
		for _, part := range strings.Split(m.down, "/") {
			if part == "" || part == "." || part == ".." {
				return game, false
			}
		}
		root = filepath.Join(root, filepath.FromSlash(m.down))
		if !isDir(root) {
			// The game keeps its saves there; the folder may just not exist yet.
			if err := os.MkdirAll(root, 0o755); err != nil {
				return game, false
			}
		}
	case m.up > 0:
		for i := 0; i < m.up; i++ {
			parent := filepath.Dir(root)
			if parent == root {
				return game, false
			}
			root = parent
		}
		// Never widen a game to a whole drive or the home folder.
		home, _ := os.UserHomeDir()
		if filepath.Dir(root) == root || (home != "" && samePath(root, filepath.Clean(home))) {
			return game, false
		}
	}
	if err := a.checkRootFree(root, game.ID); err != nil {
		return game, false
	}
	include := game.Include
	if m.include {
		a.mu.Lock()
		var found bool
		for _, cg := range a.cloudGames {
			if p, ok := cg.Devices[m.by]; ok && cg.ID == game.ID {
				include, found = p.Include, true
			}
		}
		a.mu.Unlock()
		if !found {
			return game, false
		}
	}
	if !samePath(root, game.Root) || !slices.Equal(include, game.Include) {
		if err := a.store.ResetBaseline(game.ID); err != nil {
			return game, false
		}
	}
	old := game.Root
	game.Root, game.Include, game.Layout = root, include, raw
	if err := a.store.PutGame(game); err != nil {
		return game, false
	}
	a.updateWatcher()
	a.Log.Printf("%s: following layout change by %s: %s -> %s", game.Name, m.by, old, root)
	what := "složku"
	if samePath(root, old) {
		what = "filtr"
	}
	notice := fmt.Sprintf("Podle zařízení %s jsem upravil %s: %s", m.by, what, displayRoot(root))
	a.setStatus(game.ID, func(s *gameStatus) {
		s.notice, s.noticeUntil = notice, time.Now().Add(30*time.Minute)
	})
	a.alert("layout-"+m.id(), game.ID, game.Name+": složka upravena", notice)
	return game, true
}

// checkLayout runs before each sync. It reports false when the game must
// wait (a change elsewhere is in progress or needs the user).
func (a *App) checkLayout(game *store.Game) bool {
	raw := a.cloudLayout(game.ID)
	if raw == "" || layoutID(raw) == layoutID(game.Layout) {
		return true
	}
	m := parseLayout(raw)
	if m.pending {
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.progress, s.conflict = web.StatusPending, nil, nil
			s.message = fmt.Sprintf("Na zařízení %s se mění složka této hry, počkám na dokončení.", m.by)
		})
		return false
	}
	if g, ok := a.followLayout(*game, m, raw); ok {
		*game = g
		return true
	}
	a.setStatus(game.ID, func(s *gameStatus) {
		s.status, s.progress, s.conflict = web.StatusLayout, nil, nil
		s.message = fmt.Sprintf("Na zařízení %s se změnila složka nebo filtr této hry. Zkontroluj složku i tady, synchronizace zatím čeká.", m.by)
	})
	a.alert("layout-"+m.id(), game.ID, game.Name+": zkontroluj složku",
		"Na zařízení "+m.by+" se změnila složka hry. Synchronizace tady čeká, než ji zkontroluješ.")
	return false
}

// finishLayout clears the "still uploading" flag of this device's change
// once a sync has succeeded, so the others can follow.
func (a *App) finishLayout(ctx context.Context, gameID, device string) {
	game, ok := a.store.Game(gameID)
	if !ok {
		return
	}
	m := parseLayout(game.Layout)
	if !m.pending || m.by != device || a.cloudLayout(gameID) != game.Layout {
		return
	}
	m.pending = false
	final := m.String()
	if err := a.cloud.SetLayout(ctx, gameID, final); err != nil {
		a.Log.Printf("finish layout %s: %v", game.Name, err)
		return
	}
	game.Layout = final
	if err := a.store.PutGame(game); err != nil {
		a.Log.Printf("finish layout %s: %v", game.Name, err)
	}
	a.setCloudLayout(gameID, final)
}

func (a *App) setCloudLayout(id, raw string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.cloudGames {
		if a.cloudGames[i].ID == id {
			a.cloudGames[i].Layout = raw
		}
	}
}
