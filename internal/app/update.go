package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/platform"
	"github.com/ondrejeder/savesync-ally-3/internal/update"
	"github.com/ondrejeder/savesync-ally-3/internal/web"
)

// updateEvery: how often Drive is asked for a new release.
const updateEvery = 6 * time.Hour

// checkUpdate reads the release manifest from Drive. Without force it does
// nothing if the last check is recent.
func (a *App) checkUpdate(ctx context.Context, force bool) error {
	a.mu.Lock()
	recent := time.Since(a.updateChecked) < updateEvery
	rootID := a.rootID
	a.mu.Unlock()
	if (recent && !force) || !a.connected() || rootID == "" {
		return nil
	}
	m, err := a.latestRelease(ctx, rootID)
	a.mu.Lock()
	a.updateChecked = time.Now()
	a.mu.Unlock()
	if err != nil {
		return err
	}
	var available *update.Manifest
	if m != nil && update.Newer(m.Version, a.Version) {
		if _, ok := m.Files[update.Platform()]; ok {
			available = m
		}
	}
	a.mu.Lock()
	a.update = available
	a.updateErr = ""
	a.mu.Unlock()
	a.notifyUI()
	if available != nil {
		a.alert("update-"+available.Version, "", "SaveSync Ally "+available.Version,
			"Je dostupná nová verze. Nainstaluješ ji jedním klikem v aplikaci.")
	}
	return nil
}

func (a *App) latestRelease(ctx context.Context, rootID string) (*update.Manifest, error) {
	folder, err := a.cloud.AppFolder(ctx, rootID, false)
	if err != nil || folder == "" {
		return nil, err
	}
	id, err := a.cloud.FindFile(ctx, folder, update.ManifestName)
	if err != nil || id == "" {
		return nil, err
	}
	var buf bytes.Buffer
	if err := a.cloud.Download(ctx, id, &buf); err != nil {
		return nil, err
	}
	var m update.Manifest
	if err := json.Unmarshal(bytes.TrimPrefix(buf.Bytes(), []byte{0xEF, 0xBB, 0xBF}), &m); err != nil {
		return nil, fmt.Errorf("neplatný manifest aktualizace: %w", err)
	}
	return &m, nil
}

// installUpdate downloads, verifies and swaps in the new binary, then asks
// the daemon to restart into it.
func (a *App) installUpdate() {
	err := a.doInstallUpdate()
	a.mu.Lock()
	a.updating = false
	if err != nil {
		a.updateErr = err.Error()
	}
	a.mu.Unlock()
	a.notifyUI()
	if err != nil {
		a.Log.Printf("update: %v", err)
		return
	}
	if a.Restart != nil {
		a.Restart()
	}
}

func (a *App) doInstallUpdate() error {
	a.mu.Lock()
	m := a.update
	a.mu.Unlock()
	if m == nil {
		return errors.New("Žádná aktualizace není k dispozici.")
	}
	asset, ok := m.Files[update.Platform()]
	if !ok {
		return fmt.Errorf("Aktualizace pro %s není k dispozici.", update.Platform())
	}
	exe, err := platform.CurrentExe()
	if err != nil {
		return err
	}
	installed, err := platform.InstalledExe()
	if err != nil {
		return err
	}
	if !platform.SamePath(exe, installed) {
		return errors.New("Aktualizovat jde jen nainstalovanou aplikaci (spusť savesync install).")
	}

	tmpDir := filepath.Join(a.dataDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(tmpDir, "update-"+asset.Name)
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err = a.cloud.Download(ctx, asset.ID, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("stažení aktualizace selhalo: %w", err)
	}
	if err := update.Verify(tmp, asset); err != nil {
		return err
	}
	if err := update.Replace(exe, tmp); err != nil {
		return err
	}
	a.Log.Printf("updated %s -> %s, restarting", a.Version, m.Version)
	return nil
}

func (a *App) updateState() *web.UpdateStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.update == nil && a.updateErr == "" {
		return nil
	}
	st := &web.UpdateStatus{Installing: a.updating, Error: a.updateErr}
	if a.update != nil {
		st.Version, st.Notes = a.update.Version, a.update.Notes
	}
	return st
}
