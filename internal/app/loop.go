package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/drive"
	"github.com/ondrejeder/savesync-ally-3/internal/engine"
	"github.com/ondrejeder/savesync-ally-3/internal/store"
	"github.com/ondrejeder/savesync-ally-3/internal/watch"
	"github.com/ondrejeder/savesync-ally-3/internal/web"
)

// Run starts the watcher and the cloud monitor and processes the sync queue
// until ctx ends. Only this goroutine syncs, so two syncs of the same game can
// never overlap.
func (a *App) Run(ctx context.Context) {
	if w, err := watch.New(); err != nil {
		a.Log.Printf("file watcher unavailable, relying on periodic scans: %v", err)
	} else {
		w.Log = a.Log
		w.Ignore = func(name string) bool { return strings.HasSuffix(name, engine.TempSuffix) }
		w.OnChange = func(id string) { a.enqueue(job{gameID: id}) }
		a.mu.Lock()
		a.watcher = w
		a.mu.Unlock()
		defer w.Close()
	}
	go a.monitor(ctx)
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

// monitor checks the Drive change log every minute, re-checks everything
// after sleep, after a network outage and every ScanInterval.
func (a *App) monitor(ctx context.Context) {
	ticker := time.NewTicker(cloudInterval)
	defer ticker.Stop()
	last := time.Now()
	var lastFull time.Time
	offline := false
	for {
		now := time.Now()
		// A tick that arrives much later than scheduled means the device was
		// asleep: anything may have changed in the meantime.
		resumed := now.Sub(last) > 3*cloudInterval
		if resumed {
			a.Log.Printf("resumed after %s, checking everything", now.Sub(last).Round(time.Second))
		}
		last = now
		a.updateWatcher()

		a.mu.Lock()
		hadDriveErr := a.driveErr != ""
		a.mu.Unlock()
		full := lastFull.IsZero() || resumed || offline || hadDriveErr || now.Sub(lastFull) >= a.ScanInterval
		var err error
		if full {
			err = a.fullCheck(ctx)
		} else {
			err = a.checkChanges(ctx)
		}
		switch {
		case errors.Is(err, drive.ErrReauth), errors.Is(err, drive.ErrNotConnected):
			// Waiting for the user; the auth callback triggers a full check.
		case err != nil:
			if !offline {
				a.Log.Printf("drive unreachable: %v", err)
			}
			offline = true
		default:
			offline = false
			if full {
				lastFull = now
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) connected() bool {
	if a.cloud == nil {
		return false
	}
	ok, _ := a.auth.Status()
	return ok
}

func (a *App) updateWatcher() {
	a.mu.Lock()
	w := a.watcher
	a.mu.Unlock()
	if w == nil {
		return
	}
	roots := map[string]string{}
	for _, g := range a.store.Config().Games {
		roots[g.ID] = g.Root
	}
	w.Set(roots)
	a.notifyUI() // "watching" flags may have changed
}

// fullCheck reloads the cloud game list, queues every game and restarts the
// change log from now. Scans are cheap thanks to the hash cache.
func (a *App) fullCheck(ctx context.Context) error {
	if !a.connected() {
		return drive.ErrNotConnected
	}
	token, tokenErr := a.cloud.StartPageToken(ctx)
	if err := a.refresh(ctx); err != nil {
		return err
	}
	a.updateWatcher()
	a.enqueueAll()
	if tokenErr == nil {
		_ = a.store.SaveChangesToken(token)
	}
	a.markChecked()
	return nil
}

// checkChanges asks Drive what changed since the last check and queues only
// the affected games.
func (a *App) checkChanges(ctx context.Context) error {
	if !a.connected() {
		return drive.ErrNotConnected
	}
	token := a.store.ChangesToken()
	if token == "" {
		return a.fullCheck(ctx)
	}
	changes, next, err := a.cloud.Changes(ctx, token)
	if errors.Is(err, drive.ErrChangesExpired) {
		return a.fullCheck(ctx)
	}
	if err != nil {
		a.setDriveErr(err)
		return err
	}

	device := a.store.Config().Device
	local := map[string]bool{}
	for _, g := range a.store.Config().Games {
		local[g.ID] = true
	}
	refresh := false
	queued := map[string]bool{}
	for _, ch := range changes {
		switch {
		case ch.Kind == "game" || ch.Kind == "root":
			refresh = true // new, renamed or deleted game
		case ch.Removed || ch.Folder:
			// permanent deletions are only version pruning; other folders are internal
		case ch.Device == device:
			// our own upload
		default:
			for _, parent := range ch.Parents {
				if local[parent] && !queued[parent] {
					queued[parent] = true
					a.enqueue(job{gameID: parent})
				}
			}
		}
	}
	if refresh {
		if err := a.refresh(ctx); err != nil {
			return err
		}
	}
	if next != token {
		_ = a.store.SaveChangesToken(next)
	}
	a.markChecked()
	return nil
}

func (a *App) markChecked() {
	a.mu.Lock()
	a.cloudChecked = time.Now()
	a.driveErr = ""
	a.mu.Unlock()
	a.notifyUI()
}

func (a *App) setDriveErr(err error) {
	if errors.Is(err, drive.ErrReauth) {
		return
	}
	a.mu.Lock()
	a.driveErr = "Google Drive je teď nedostupný, zkusím to znovu za chvíli."
	a.mu.Unlock()
	a.notifyUI()
}

// refresh reloads the account and the list of cloud games.
func (a *App) refresh(ctx context.Context) error {
	if !a.connected() {
		return drive.ErrNotConnected
	}
	a.mu.Lock()
	rootID, account := a.rootID, a.account
	a.mu.Unlock()
	if rootID == "" {
		id, err := a.cloud.EnsureRoot(ctx)
		if err != nil {
			a.setDriveErr(err)
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
		a.setDriveErr(err)
		return err
	}
	// rootID and the game list are published together, so a loaded root
	// always comes with a complete list (see removedFromCloud).
	a.mu.Lock()
	a.rootID, a.cloudGames = rootID, games
	a.mu.Unlock()
	a.notifyUI()
	return nil
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
		a.mu.Lock()
		delete(a.queued, j.gameID)
		a.mu.Unlock()
		a.Log.Printf("sync queue full, skipping %s", j.gameID)
	}
}

func (a *App) syncGame(ctx context.Context, j job) {
	game, ok := a.store.Game(j.gameID)
	if !ok || !a.connected() {
		return
	}
	if a.removedFromCloud(game.ID) {
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.progress, s.conflict = web.StatusError, nil, nil
			s.message = "Hra byla smazána z cloudu (možná na jiném zařízení). Lokální soubory jsou v pořádku."
		})
		a.alert("removed", game.ID, game.Name+": smazáno z cloudu",
			"Hra se už nesynchronizuje. Lokální savy zůstaly na disku.")
		return
	}
	a.setStatus(game.ID, func(s *gameStatus) {
		s.status, s.message, s.progress = web.StatusSyncing, "", nil
	})
	device := a.store.Config().Device
	a.syncer.Device = device
	a.syncer.Progress = func(done, total int, up bool) {
		dir := "down"
		if up {
			dir = "up"
		}
		a.setStatus(game.ID, func(s *gameStatus) {
			s.progress = &web.Progress{Direction: dir, Done: done, Total: total}
		})
	}

	state, err := a.store.State(game.ID)
	if err != nil {
		a.fail(game, err)
		return
	}
	// Our own downloads must not look like local changes to the watcher.
	a.mu.Lock()
	w := a.watcher
	a.mu.Unlock()
	if w != nil {
		w.Pause(game.ID)
	}
	ref := engine.GameRef{ID: game.ID, Root: game.Root, Include: game.Include}
	next, res, err := a.syncer.Sync(ctx, ref, state, j.keep)
	if w != nil {
		w.Resume(game.ID)
	}
	if saveErr := a.store.SaveState(game.ID, next); saveErr != nil && err == nil {
		err = saveErr
	}

	switch {
	case errors.Is(err, engine.ErrRootMissing):
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.message, s.progress = web.StatusMissing, "Složka nenalezena, synchronizace pozastavena", nil
		})
	case errors.Is(err, engine.ErrBusy):
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.progress = web.StatusPending, nil
			s.message = "Savy jsou právě otevřené (běží hra?), zkusím to za chvíli."
		})
		time.AfterFunc(busyRetry, func() { a.enqueue(job{gameID: game.ID}) })
	case errors.Is(err, drive.ErrReauth):
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.message, s.progress = web.StatusPending, "Čeká na připojení Google Drive", nil
		})
	case err != nil:
		a.fail(game, err)
	case len(res.Conflicts) > 0:
		sides := conflictSides(device, res)
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.message, s.progress, s.conflict, s.guarded = web.StatusConflict, "", nil, sides, res.Guarded
		})
		if res.Guarded {
			a.alert("conflict", game.ID, game.Name+": potvrď verzi",
				"Lokální save se změnil hned po stažení novější verze. Možná hra běžela se starým stavem.")
		} else {
			a.alert("conflict", game.ID, game.Name+": konflikt", "Obě zařízení mají jinou verzi. Vyber, kterou ponechat.")
		}
	default:
		if res.Changed() {
			a.Log.Printf("%s: %d up, %d down, %d deleted", game.Name, res.Uploaded, res.Downloaded, res.Deleted)
		}
		if res.Uploaded > 0 || res.Deleted > 0 {
			if err := a.cloud.TouchGame(ctx, game.ID, device); err != nil {
				a.Log.Printf("touch %s: %v", game.Name, err)
			}
			a.maybeVersion(ctx, game, next)
		}
		a.setStatus(game.ID, func(s *gameStatus) {
			s.status, s.message, s.progress, s.conflict, s.guarded = web.StatusSynced, "", nil, nil, false
			s.lastSync = time.Now()
			s.files, s.bytes = res.Files, res.Bytes
			if res.Uploaded > 0 || j.keep != "" {
				s.notice = "" // this device's state is now the current one
			}
			if res.Downloaded > 0 {
				from := res.DownloadedFrom
				if from == "" {
					from = "cloudu"
				}
				s.notice = fmt.Sprintf("Stažena novější verze z %s. Pokud hra právě běží, ukonči ji bez ukládání a spusť znovu.", from)
				s.noticeUntil = time.Now().Add(guardWindow)
			}
		})
		if res.Downloaded > 0 {
			from := res.DownloadedFrom
			if from == "" {
				from = "cloudu"
			}
			a.alert("download", game.ID, game.Name+": stažena verze z "+from,
				"Pokud hra právě běží, ukonči ji bez ukládání a spusť znovu.")
		}
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

func (a *App) fail(game store.Game, err error) {
	a.Log.Printf("sync %s: %v", game.Name, err)
	a.setDriveErrIfNetwork(err)
	a.setStatus(game.ID, func(s *gameStatus) {
		s.status, s.message, s.progress = web.StatusError, err.Error(), nil
	})
}

// setDriveErrIfNetwork flags Drive as unreachable for transport errors, so
// the monitor does a full re-check once the connection is back.
func (a *App) setDriveErrIfNetwork(err error) {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && !errors.Is(err, context.Canceled) {
		a.setDriveErr(err)
	}
}

func conflictSides(device string, res engine.Result) []web.ConflictSide {
	local := web.ConflictSide{Side: "local", Device: device, Files: len(res.Local)}
	cloud := web.ConflictSide{Side: "cloud", Device: "cloud", Files: len(res.Cloud)}
	var lt, ct int64
	paths := append([]string(nil), res.Conflicts...)
	sort.Strings(paths)
	for _, rel := range paths {
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
