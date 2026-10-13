// Package app ties the store, Drive client and sync engine together and
// produces the state shown in the UI.
package app

import (
	"context"
	"errors"
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
	"github.com/ondrejeder/savesync-ally-3/internal/web"
)

// How often everything is re-checked until the watcher and Changes API
// (phase 4) take over.
const pollInterval = 2 * time.Minute

type App struct {
	Version string
	Log     *log.Logger
	// OnChange is called whenever the UI state changes.
	OnChange func()

	store  *store.Store
	auth   *drive.Auth // nil when no OAuth client is configured
	cloud  *drive.Client
	syncer *engine.Syncer
	queue  chan job

	mu         sync.Mutex
	games      map[string]*gameStatus
	cloudGames []drive.GameFolder
	rootID     string
	account    string
	driveErr   string
	queued     map[string]bool
}

type job struct {
	gameID string
	keep   engine.Side
}

type gameStatus struct {
	status   string
	message  string
	lastSync time.Time
	files    int
	bytes    int64
	progress *web.Progress
	conflict []web.ConflictSide
}

func New(st *store.Store, auth *drive.Auth, dataDir, version string, logger *log.Logger) *App {
	a := &App{
		Version: version,
		Log:     logger,
		store:   st,
		auth:    auth,
		queue:   make(chan job, 64),
		games:   map[string]*gameStatus{},
		queued:  map[string]bool{},
	}
	if auth != nil {
		a.cloud = &drive.Client{Auth: auth}
		auth.OnChange = func() {
			a.notify()
			go a.refresh(context.Background())
		}
	}
	a.syncer = &engine.Syncer{
		Cloud:     a.cloud,
		Device:    st.Config().Device,
		BackupDir: filepath.Join(dataDir, "backups"),
	}
	return a
}

// Run processes the sync queue until ctx ends. Only this goroutine syncs, so
// two syncs of the same game can never overlap.
func (a *App) Run(ctx context.Context) {
	go a.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-a.queue:
			a.mu.Lock()
			delete(a.queued, j.gameID)
			a.mu.Unlock()
			a.syncGame(ctx, j)
		}
	}
}

func (a *App) poll(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		a.refresh(ctx)
		a.enqueueAll()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// refresh reloads the account and the list of cloud games.
func (a *App) refresh(ctx context.Context) {
	if a.cloud == nil {
		return
	}
	if connected, _ := a.auth.Status(); !connected {
		return
	}
	a.mu.Lock()
	rootID, account := a.rootID, a.account
	a.mu.Unlock()
	err := func() error {
		if rootID == "" {
			id, err := a.cloud.EnsureRoot(ctx)
			if err != nil {
				return err
			}
			rootID = id
		}
		if account == "" {
			if email, err := a.cloud.Account(ctx); err == nil {
				a.mu.Lock()
				a.account = email
				a.mu.Unlock()
			}
		}
		games, err := a.cloud.ListGames(ctx, rootID)
		if err != nil {
			return err
		}
		// rootID and the game list are published together, so a loaded
		// root always comes with a complete list (see removedFromCloud).
		a.mu.Lock()
		a.rootID, a.cloudGames = rootID, games
		a.mu.Unlock()
		return nil
	}()
	a.mu.Lock()
	a.driveErr = ""
	if err != nil && !errors.Is(err, drive.ErrReauth) {
		a.driveErr = "Google Drive je nedostupný: " + err.Error()
		a.Log.Printf("refresh: %v", err)
	}
	a.mu.Unlock()
	a.notify()
}

func (a *App) enqueueAll() {
	for _, g := range a.store.Config().Games {
		a.enqueue(job{gameID: g.ID})
	}
}

func (a *App) enqueue(j job) {
	a.mu.Lock()
	// A pending plain sync is enough; conflict resolutions are always queued.
	if j.keep == "" && a.queued[j.gameID] {
		a.mu.Unlock()
		return
	}
	a.queued[j.gameID] = true
	a.mu.Unlock()
	select {
	case a.queue <- j:
	default:
		a.Log.Printf("sync queue full, skipping %s", j.gameID)
	}
}

func (a *App) syncGame(ctx context.Context, j job) {
	game, ok := a.store.Game(j.gameID)
	if !ok || a.cloud == nil {
		return
	}
	if connected, _ := a.auth.Status(); !connected {
		return
	}
	if a.removedFromCloud(game.ID) {
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.progress = web.StatusError, nil
			s.message = "Hra byla smazána z cloudu (možná na jiném zařízení). Lokální soubory jsou v pořádku."
		})
		return
	}
	a.setStatus(game.ID, func(s *gameStatus) {
		s.status, s.message, s.progress = web.StatusSyncing, "", nil
	})
	a.syncer.Device = a.store.Config().Device
	a.syncer.Progress = func(done, total int, up bool) {
		dir := "down"
		if up {
			dir = "up"
		}
		a.setStatus(game.ID, func(s *gameStatus) {
			s.progress = &web.Progress{Direction: dir, Done: done, Total: total}
		})
	}

	base, err := a.store.Baseline(game.ID)
	if err != nil {
		a.fail(game.ID, err)
		return
	}
	next, res, err := a.syncer.Sync(ctx, engine.GameRef{ID: game.ID, Root: game.Root, Include: game.Include}, base, j.keep)
	if saveErr := a.store.SaveBaseline(game.ID, next); saveErr != nil && err == nil {
		err = saveErr
	}
	switch {
	case errors.Is(err, engine.ErrRootMissing):
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.message, s.progress = web.StatusMissing, "Složka nenalezena, synchronizace pozastavena", nil
		})
	case errors.Is(err, drive.ErrReauth):
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.message, s.progress = web.StatusPending, "Čeká na připojení Google Drive", nil
		})
	case err != nil:
		a.fail(game.ID, err)
	case len(res.Conflicts) > 0:
		sides := conflictSides(a.store.Config().Device, res)
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.message, s.progress, s.conflict = web.StatusConflict, "", nil, sides
		})
	default:
		if res.Uploaded > 0 || res.Deleted > 0 {
			if err := a.cloud.TouchGame(ctx, game.ID, a.store.Config().Device); err != nil {
				a.Log.Printf("touch %s: %v", game.Name, err)
			}
		}
		if res.Changed() {
			a.Log.Printf("%s: %d up, %d down, %d deleted", game.Name, res.Uploaded, res.Downloaded, res.Deleted)
		}
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.message, s.progress, s.conflict = web.StatusSynced, "", nil, nil
			s.lastSync = time.Now()
			s.files, s.bytes = res.Files, res.Bytes
		})
	}
}

// removedFromCloud reports whether the game folder is gone from Drive. It
// only answers once the cloud list has been loaded.
func (a *App) removedFromCloud(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rootID == "" || a.driveErr != "" {
		return false
	}
	for _, cg := range a.cloudGames {
		if cg.ID == id {
			return false
		}
	}
	return true
}

func (a *App) fail(id string, err error) {
	a.Log.Printf("sync %s: %v", id, err)
	a.setStatus(id, func(s *gameStatus) {
		s.status, s.message, s.progress = web.StatusError, err.Error(), nil
	})
}

func conflictSides(device string, res engine.Result) []web.ConflictSide {
	local := web.ConflictSide{Side: "local", Device: device, Files: len(res.Local)}
	cloud := web.ConflictSide{Side: "cloud", Device: "cloud", Files: len(res.Cloud)}
	var lt, ct int64
	for _, rel := range res.Conflicts {
		if l, ok := res.Local[rel]; ok && l.MTime > lt {
			lt = l.MTime
		}
		if c, ok := res.Cloud[rel]; ok && c.MTime > ct {
			ct = c.MTime
			if c.Device != "" {
				cloud.Device = c.Device
			}
		}
	}
	if lt > 0 {
		local.Modified = time.Unix(lt, 0).Format(time.RFC3339)
	}
	if ct > 0 {
		cloud.Modified = time.Unix(ct, 0).Format(time.RFC3339)
	}
	return []web.ConflictSide{local, cloud}
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
	a.notify()
}

func (a *App) notify() {
	if a.OnChange != nil {
		a.OnChange()
	}
}

// State builds what the UI shows: local games first, then cloud-only ones.
func (a *App) State() web.State {
	cfg := a.store.Config()
	st := web.State{Version: a.Version, Device: cfg.Device, Autostart: platform.AutostartEnabled(), Games: []web.Game{}}

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

	local := map[string]bool{}
	for _, g := range cfg.Games {
		local[g.ID] = true
		view := web.Game{ID: g.ID, Name: g.Name, Root: g.Root, Include: g.Include, Status: web.StatusPending}
		if s := a.games[g.ID]; s != nil {
			view.Status, view.Message, view.Files, view.Bytes = s.status, s.message, s.files, s.bytes
			view.Progress, view.Conflict = s.progress, s.conflict
			if !s.lastSync.IsZero() {
				view.LastSync = s.lastSync.Format(time.RFC3339)
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
		a.notify()
		return "Google Drive odpojen.", nil
	case "sync":
		a.enqueue(job{gameID: gameID})
		return "Synchronizuji…", nil
	case "sync-all":
		go func() {
			a.refresh(context.Background())
			a.enqueueAll()
		}()
		return "Synchronizuji všechny hry…", nil
	case "keep-local", "keep-cloud":
		side := engine.KeepLocal
		if kind == "keep-cloud" {
			side = engine.KeepCloud
		}
		a.enqueue(job{gameID: gameID, keep: side})
		return "Řeším konflikt…", nil
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
