package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/engine"
	"github.com/ondrejeder/savesync-ally-3/internal/store"
	"github.com/ondrejeder/savesync-ally-3/internal/web"
)

// versionEvery: at most one cloud version per game in this time from the
// same device, so frequent autosaves do not push good versions out. A device
// switch always creates a version.
const versionEvery = 30 * time.Minute

func (a *App) versionsFolder(ctx context.Context, gameID string) (string, error) {
	a.mu.Lock()
	id := a.versionDirs[gameID]
	a.mu.Unlock()
	if id != "" {
		return id, nil
	}
	id, err := a.cloud.VersionsFolder(ctx, gameID)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.versionDirs[gameID] = id
	a.mu.Unlock()
	return id, nil
}

// maybeVersion stores a zip snapshot of the game after an upload when the
// newest version is old enough or came from another device, then prunes old
// versions. Failures are logged; they never fail the sync itself.
func (a *App) maybeVersion(ctx context.Context, game store.Game, st engine.GameState) {
	if err := a.makeVersion(ctx, game, st); err != nil {
		a.Log.Printf("version %s: %v", game.Name, err)
	}
}

func (a *App) makeVersion(ctx context.Context, game store.Game, st engine.GameState) error {
	folder, err := a.versionsFolder(ctx, game.ID)
	if err != nil {
		return err
	}
	versions, err := a.cloud.ListVersions(ctx, folder)
	if err != nil {
		return err
	}
	device := a.store.Config().Device
	if len(versions) > 0 && versions[0].Device == device && time.Since(versions[0].Created) < versionEvery {
		return nil
	}

	// Snapshot exactly the files that are now in sync.
	var rels []string
	for rel, md5 := range st.Baseline {
		if h, ok := st.Hashes[rel]; ok && h.MD5 == md5 {
			rels = append(rels, rel)
		}
	}
	if len(rels) == 0 {
		return nil
	}
	tmpDir := filepath.Join(a.dataDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(tmpDir, "version-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := engine.WriteSnapshot(f, game.Root, rels); err != nil {
		return err
	}
	size, err := f.Seek(0, 2)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	if err := a.cloud.UploadVersion(ctx, folder, f, size, device, len(rels), time.Now()); err != nil {
		return err
	}

	keep := a.store.Versions()
	// versions does not include the one just uploaded.
	for i := keep - 1; i < len(versions); i++ {
		if err := a.cloud.Delete(ctx, versions[i].ID); err != nil {
			a.Log.Printf("prune version of %s: %v", game.Name, err)
		}
	}
	return nil
}

// Versions lists the cloud versions of a game for the UI.
func (a *App) Versions(id string) ([]web.Version, error) {
	if err := a.requireDrive(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	folder, err := a.versionsFolder(ctx, id)
	if err != nil {
		return nil, err
	}
	list, err := a.cloud.ListVersions(ctx, folder)
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	out := make([]web.Version, 0, len(list))
	for _, v := range list {
		out = append(out, web.Version{ID: v.ID, Device: v.Device, Created: v.Created.Format(time.RFC3339), Files: v.Files, Bytes: v.Size})
	}
	return out, nil
}

// RestoreVersion replaces the local saves with a cloud version (current
// files are backed up first) and uploads it as the new current state.
func (a *App) RestoreVersion(id, versionID string) (string, error) {
	game, ok := a.store.Game(id)
	if !ok {
		return "", errors.New("Hru nejdřív nastav na tomto zařízení.")
	}
	if err := a.requireDrive(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tmpDir := filepath.Join(a.dataDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(tmpDir, "restore-*.zip")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if err := a.cloud.Download(ctx, versionID, f); err != nil {
		f.Close()
		return "", fmt.Errorf("verzi se nepodařilo stáhnout: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	a.mu.Lock()
	w := a.watcher
	a.mu.Unlock()
	if w != nil {
		w.Pause(id)
	}
	err = a.syncer.RestoreZip(engine.GameRef{ID: id, Root: game.Root, Include: game.Include}, f.Name(), true)
	if w != nil {
		w.Resume(id)
	}
	if err != nil {
		return "", err
	}
	a.clearGuard(id)
	a.enqueue(job{gameID: id})
	return "Verze obnovena, nahrávám ji jako aktuální…", nil
}

// clearGuard drops the recent-download marks of a game: after the user
// deliberately restored files, they must upload without a question.
func (a *App) clearGuard(id string) {
	st, err := a.store.State(id)
	if err != nil {
		return
	}
	st.Downloads = nil
	_ = a.store.SaveState(id, st)
}
