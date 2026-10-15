// Package app ties the store, Drive client, watcher and sync engine together
// and produces the state shown in the UI.
package app

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/drive"
	"github.com/ondrejeder/savesync-ally-3/internal/engine"
	"github.com/ondrejeder/savesync-ally-3/internal/platform"
	"github.com/ondrejeder/savesync-ally-3/internal/store"
	"github.com/ondrejeder/savesync-ally-3/internal/update"
	"github.com/ondrejeder/savesync-ally-3/internal/watch"
	"github.com/ondrejeder/savesync-ally-3/internal/web"
)

const (
	// cloudInterval: how often the Drive change log is checked.
	cloudInterval = time.Minute
	// guardWindow: local changes this soon after a download are confirmed
	// by the user instead of being uploaded.
	guardWindow = 10 * time.Minute
	// busyRetry: retry delay when a save file is locked by a running game.
	busyRetry = time.Minute
)

type App struct {
	Version string
	Log     *log.Logger
	// OnChange is called whenever the UI state changes.
	OnChange func()
	// Notify shows a desktop notification (platform.Notify by default).
	Notify func(title, body string) error
	// ScanInterval: full re-check of every game as a safety net next to the
	// watcher and the Drive change log.
	ScanInterval time.Duration
	// Restart is called after an update replaced the binary.
	Restart func()

	store   *store.Store
	auth    *drive.Auth // nil when no OAuth client is configured
	cloud   *drive.Client
	syncer  *engine.Syncer
	watcher *watch.Watcher
	dataDir string
	queue   chan job

	mu           sync.Mutex
	games        map[string]*gameStatus
	cloudGames   []drive.GameFolder
	rootID       string
	account      string
	driveErr     string
	cloudChecked time.Time
	queued       map[string]bool
	versionDirs  map[string]string // gameID -> versions folder ID
	notified     map[string]time.Time

	update        *update.Manifest // newer release, if any
	updateChecked time.Time
	updating      bool
	updateErr     string
}

type job struct {
	gameID string
	keep   engine.Side
}

type gameStatus struct {
	status      string
	message     string
	lastSync    time.Time
	files       int
	bytes       int64
	progress    *web.Progress
	conflict    []web.ConflictSide
	guarded     bool
	notice      string
	noticeUntil time.Time
}

func New(st *store.Store, auth *drive.Auth, dataDir, version string, logger *log.Logger) *App {
	a := &App{
		Version:      version,
		Log:          logger,
		Notify:       platform.Notify,
		ScanInterval: platform.DefaultScanInterval,
		store:        st,
		auth:         auth,
		dataDir:      dataDir,
		queue:        make(chan job, 128),
		games:        map[string]*gameStatus{},
		queued:       map[string]bool{},
		versionDirs:  map[string]string{},
		notified:     map[string]time.Time{},
	}
	if auth != nil {
		a.cloud = &drive.Client{Auth: auth}
		auth.OnChange = func() {
			a.notifyUI()
			if _, reauth := auth.Status(); reauth {
				a.alert("reauth", "", "Přihlášení ke Google Drive vypršelo",
					"Synchronizace čeká. Otevři SaveSync Ally a připoj Drive znovu.")
				return
			}
			go a.fullCheck(context.Background())
		}
	}
	a.syncer = &engine.Syncer{
		Cloud:       a.cloud,
		Device:      st.Config().Device,
		BackupDir:   filepath.Join(dataDir, "backups"),
		GuardWindow: guardWindow,
	}
	return a
}

func (a *App) setStatus(id string, fn func(*gameStatus)) {
	a.mu.Lock()
	s := a.games[id]
	if s == nil {
		s = &gameStatus{status: web.StatusPending}
		a.games[id] = s
	}
	fn(s)
	a.mu.Unlock()
	a.notifyUI()
}

func (a *App) notifyUI() {
	if a.OnChange != nil {
		a.OnChange()
	}
}

// alert shows a desktop notification, at most once per key per 10 minutes
// (key "" for gameID means app-wide).
func (a *App) alert(key, gameID, title, body string) {
	k := key + "|" + gameID
	a.mu.Lock()
	if t, ok := a.notified[k]; ok && time.Since(t) < 10*time.Minute {
		a.mu.Unlock()
		return
	}
	a.notified[k] = time.Now()
	a.mu.Unlock()
	if a.Notify != nil {
		if err := a.Notify(title, body); err != nil {
			a.Log.Printf("notify: %v", err)
		}
	}
}

// State builds what the UI shows: local games first, then cloud-only ones.
func (a *App) State() web.State {
	cfg := a.store.Config()
	st := web.State{
		Version:   a.Version,
		Device:    cfg.Device,
		Autostart: platform.AutostartEnabled(),
		Versions:  a.store.Versions(),
		Update:    a.updateState(),
		Games:     []web.Game{},
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.auth == nil:
		st.Drive.Error = drive.ErrNoClient.Error()
	default:
		st.Drive.Connected, st.Drive.NeedsReauth = a.auth.Status()
		st.Drive.Account = a.account
		st.Drive.Error = a.driveErr
	}
	if !a.cloudChecked.IsZero() {
		st.CloudChecked = a.cloudChecked.Format(time.RFC3339)
	}

	local := map[string]bool{}
	now := time.Now()
	for _, g := range cfg.Games {
		local[g.ID] = true
		view := web.Game{ID: g.ID, Name: g.Name, Root: g.Root, Include: g.Include, Status: web.StatusPending}
		if a.watcher != nil {
			view.Watching = a.watcher.Watching(g.ID)
		}
		if s := a.games[g.ID]; s != nil {
			view.Status, view.Message, view.Files, view.Bytes = s.status, s.message, s.files, s.bytes
			view.Progress, view.Conflict, view.Guarded = s.progress, s.conflict, s.guarded
			if !s.lastSync.IsZero() {
				view.LastSync = s.lastSync.Format(time.RFC3339)
			}
			if s.notice != "" && now.Before(s.noticeUntil) {
				view.Notice = s.notice
			}
		}
		st.Games = append(st.Games, view)
	}
	sort.SliceStable(st.Games, func(i, j int) bool { return st.Games[i].Name < st.Games[j].Name })
	for _, cg := range a.cloudGames {
		if local[cg.ID] {
			continue
		}
		view := web.Game{ID: cg.ID, Name: cg.Name, Status: web.StatusCloudOnly, CloudDevice: cg.UpdatedBy}
		if !cg.UpdatedAt.IsZero() {
			view.CloudModified = cg.UpdatedAt.Format(time.RFC3339)
		}
		st.Games = append(st.Games, view)
	}
	return st
}

// Action handles buttons from the UI.
func (a *App) Action(kind, gameID string) (string, error) {
	switch kind {
	case "connect":
		if a.auth == nil {
			return "", drive.ErrNoClient
		}
		url, err := a.auth.BeginLogin()
		if err != nil {
			return "", err
		}
		if err := platform.OpenURL(url); err != nil {
			return "", fmt.Errorf("nepodařilo se otevřít prohlížeč: %w", err)
		}
		return "Dokonči přihlášení v prohlížeči.", nil
	case "disconnect":
		if a.auth != nil {
			a.auth.Disconnect()
		}
		a.mu.Lock()
		a.account, a.rootID, a.cloudGames = "", "", nil
		a.mu.Unlock()
		_ = a.store.SaveChangesToken("")
		a.notifyUI()
		return "Google Drive odpojen.", nil
	case "sync":
		a.enqueue(job{gameID: gameID})
		return "Synchronizuji…", nil
	case "sync-all":
		go a.fullCheck(context.Background())
		return "Synchronizuji všechny hry…", nil
	case "keep-local", "keep-cloud":
		side := engine.KeepLocal
		if kind == "keep-cloud" {
			side = engine.KeepCloud
		}
		a.enqueue(job{gameID: gameID, keep: side})
		return "Řeším konflikt…", nil
	case "update-check":
		go func() {
			if err := a.checkUpdate(context.Background(), true); err != nil {
				a.mu.Lock()
				a.updateErr = "Kontrola aktualizací selhala: " + err.Error()
				a.mu.Unlock()
				a.notifyUI()
			}
		}()
		return "Kontroluji aktualizace…", nil
	case "update-install":
		a.mu.Lock()
		if a.update == nil || a.updating {
			a.mu.Unlock()
			return "", fmt.Errorf("žádná aktualizace k instalaci")
		}
		a.updating, a.updateErr = true, ""
		a.mu.Unlock()
		a.notifyUI()
		go a.installUpdate()
		return "Instaluji aktualizaci, aplikace se za chvíli restartuje…", nil
	case "open-folder":
		game, ok := a.store.Game(gameID)
		if !ok {
			return "", fmt.Errorf("hra nebyla nalezena")
		}
		if err := platform.OpenFolder(game.Root); err != nil {
			return "", err
		}
		return "Otevírám složku…", nil
	default:
		return "", fmt.Errorf("neznámá akce %q", kind)
	}
}
