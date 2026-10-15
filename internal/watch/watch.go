// Package watch reports which games changed on disk, with debouncing so a
// game that is still writing its save is not synced half-way.
package watch

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Defaults: sync after Quiet without events, but at the latest after MaxWait
// when the game keeps writing.
const (
	DefaultQuiet   = 10 * time.Second
	DefaultMaxWait = 2 * time.Minute
)

// Watcher watches game folders recursively.
type Watcher struct {
	Quiet   time.Duration
	MaxWait time.Duration
	// Ignore reports file names the app writes itself (temp downloads).
	Ignore   func(name string) bool
	OnChange func(gameID string)
	Log      *log.Logger

	mu      sync.Mutex
	fs      *fsnotify.Watcher
	roots   map[string]string    // gameID -> root
	dirs    map[string]string    // watched dir -> gameID
	pending map[string]*pending  // gameID -> debounce state
	muted   map[string]time.Time // gameID -> ignore events until (or zero = while syncing)
	paused  map[string]bool
}

type pending struct {
	first time.Time
	timer *time.Timer
}

func New() (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		Quiet:   DefaultQuiet,
		MaxWait: DefaultMaxWait,
		fs:      fw,
		roots:   map[string]string{},
		dirs:    map[string]string{},
		pending: map[string]*pending{},
		muted:   map[string]time.Time{},
		paused:  map[string]bool{},
	}
	go w.loop()
	return w, nil
}

func (w *Watcher) Close() error { return w.fs.Close() }

// Set reconciles the watched games (gameID -> root). Call it whenever the
// game list changes and periodically, so folders that appear later (an SD
// card, a game installed after setup) start being watched.
//
// fsnotify is never called while holding w.mu: on Windows, Add/Remove wait
// for fsnotify's goroutine, which may itself be waiting for the event loop.
func (w *Watcher) Set(games map[string]string) {
	var drop []string
	add := map[string]string{}
	w.mu.Lock()
	for id, root := range w.roots {
		newRoot, ok := games[id]
		_, statErr := os.Stat(root)
		if !ok || newRoot != root || statErr != nil {
			drop = append(drop, id)
		}
	}
	for id, root := range games {
		if _, ok := w.roots[id]; ok && !contains(drop, id) {
			continue
		}
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			add[id] = root
		}
	}
	w.mu.Unlock()

	for _, id := range drop {
		w.unwatch(id)
	}
	for id, root := range add {
		w.mu.Lock()
		w.roots[id] = root
		w.mu.Unlock()
		w.addTree(id, root)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Watching reports whether a game's folder is currently watched.
func (w *Watcher) Watching(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.roots[id]
	return ok
}

// Pause ignores events for a game while the app itself writes to it.
func (w *Watcher) Pause(id string) {
	w.mu.Lock()
	w.paused[id] = true
	w.mu.Unlock()
}

// Resume ends Pause; events arriving during a short grace period are still
// ignored because the OS may deliver them late.
func (w *Watcher) Resume(id string) {
	w.mu.Lock()
	delete(w.paused, id)
	w.muted[id] = time.Now().Add(2 * time.Second)
	w.mu.Unlock()
}

func (w *Watcher) unwatch(id string) {
	var dirs []string
	w.mu.Lock()
	for dir, gid := range w.dirs {
		if gid == id {
			dirs = append(dirs, dir)
			delete(w.dirs, dir)
		}
	}
	delete(w.roots, id)
	if p := w.pending[id]; p != nil {
		p.timer.Stop()
		delete(w.pending, id)
	}
	w.mu.Unlock()
	for _, dir := range dirs {
		_ = w.fs.Remove(dir)
	}
}

func (w *Watcher) addTree(id, root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		w.mu.Lock()
		_, known := w.dirs[p]
		if !known {
			w.dirs[p] = id
		}
		w.mu.Unlock()
		if known {
			return nil
		}
		if err := w.fs.Add(p); err != nil {
			w.mu.Lock()
			delete(w.dirs, p)
			w.mu.Unlock()
			if w.Log != nil {
				w.Log.Printf("watch %s: %v", p, err)
			}
		}
		return nil
	})
}

func (w *Watcher) loop() {
	for {
		select {
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			if w.Log != nil {
				w.Log.Printf("watcher: %v", err)
			}
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	if ev.Op == fsnotify.Chmod {
		return
	}
	if w.Ignore != nil && w.Ignore(ev.Name) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	id := w.gameForLocked(ev.Name)
	if id == "" {
		return
	}
	// New sub-folders must be watched too (fsnotify is not recursive). Adding
	// happens on another goroutine so the event loop never waits on fsnotify.
	if ev.Has(fsnotify.Create) {
		if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
			go w.addTree(id, ev.Name)
		}
	}
	if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
		delete(w.dirs, ev.Name)
	}
	if w.paused[id] || time.Now().Before(w.muted[id]) {
		return
	}
	w.scheduleLocked(id)
}

// scheduleLocked debounces: each event restarts the quiet timer, but never
// beyond MaxWait after the first event.
func (w *Watcher) scheduleLocked(id string) {
	now := time.Now()
	p := w.pending[id]
	if p == nil {
		p = &pending{first: now}
		w.pending[id] = p
	} else {
		p.timer.Stop()
	}
	delay := w.Quiet
	if remaining := p.first.Add(w.MaxWait).Sub(now); remaining < delay {
		delay = max(remaining, 0)
	}
	p.timer = time.AfterFunc(delay, func() { w.fire(id, p) })
}

func (w *Watcher) fire(id string, p *pending) {
	w.mu.Lock()
	if w.pending[id] != p {
		w.mu.Unlock()
		return // superseded or unwatched
	}
	delete(w.pending, id)
	w.mu.Unlock()
	if w.OnChange != nil {
		w.OnChange(id)
	}
}

// gameForLocked maps a path to the game whose root contains it.
func (w *Watcher) gameForLocked(path string) string {
	if id, ok := w.dirs[path]; ok {
		return id
	}
	if id, ok := w.dirs[filepath.Dir(path)]; ok {
		return id
	}
	best, bestLen := "", 0
	for id, root := range w.roots {
		if (path == root || strings.HasPrefix(path, root+string(filepath.Separator))) && len(root) > bestLen {
			best, bestLen = id, len(root)
		}
	}
	return best
}
