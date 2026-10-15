package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/engine"
	"github.com/ondrejeder/savesync-ally-3/internal/platform"
	"github.com/ondrejeder/savesync-ally-3/internal/store"
	"github.com/ondrejeder/savesync-ally-3/internal/web"
)

const maxListedFiles = 40

// Browse lists sub-folders (and a preview of files) for the folder picker.
func (a *App) Browse(path string) (web.Listing, error) {
	listing := web.Listing{Dirs: []web.Entry{}, Files: []web.Entry{}}
	for _, p := range platform.Places() {
		listing.Places = append(listing.Places, web.Entry{Name: p.Label, Path: p.Path, Icon: p.Icon})
	}
	if path == "" {
		if len(listing.Places) > 0 {
			path = listing.Places[0].Path
		} else {
			return listing, nil
		}
	}
	path = filepath.Clean(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		return listing, fmt.Errorf("složku nelze otevřít: %w", err)
	}
	listing.Path = path
	if parent := filepath.Dir(path); parent != path {
		listing.Parent = parent
	}
	steamNames := steamGameNames(path)
	for _, e := range entries {
		full := filepath.Join(path, e.Name())
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(full); err == nil {
				isDir = st.IsDir()
			}
		}
		if isDir {
			listing.Dirs = append(listing.Dirs, web.Entry{Name: e.Name(), Path: full, Label: steamNames[e.Name()]})
		} else if len(listing.Files) < maxListedFiles {
			listing.Files = append(listing.Files, web.Entry{Name: e.Name()})
		} else {
			listing.More++
		}
	}
	sort.Slice(listing.Dirs, func(i, j int) bool {
		// Recognized Steam games first, then alphabetical.
		li, lj := listing.Dirs[i].Label, listing.Dirs[j].Label
		if (li == "") != (lj == "") {
			return li != ""
		}
		return strings.ToLower(listing.Dirs[i].Label+listing.Dirs[i].Name) < strings.ToLower(listing.Dirs[j].Label+listing.Dirs[j].Name)
	})
	return listing, nil
}

var acfName = regexp.MustCompile(`"name"\s+"([^"]+)"`)

// steamGameNames maps compatdata app IDs to game names using the
// appmanifest_<id>.acf files next to the compatdata folder.
func steamGameNames(dir string) map[string]string {
	names := map[string]string{}
	if filepath.Base(dir) != "compatdata" {
		return names
	}
	manifests, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), "appmanifest_*.acf"))
	for _, m := range manifests {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "appmanifest_"), ".acf")
		if data, err := os.ReadFile(m); err == nil {
			if match := acfName.FindSubmatch(data); match != nil {
				names[id] = string(match[1])
			}
		}
	}
	return names
}

func cleanRequest(req web.GameRequest, needName bool) (web.GameRequest, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Root = strings.TrimSpace(req.Root)
	var include []string
	for _, p := range req.Include {
		if p = strings.TrimSpace(p); p != "" {
			include = append(include, p)
		}
	}
	req.Include = include
	if needName && req.Name == "" {
		return req, errors.New("Zadej název hry.")
	}
	if req.Root == "" {
		return req, errors.New("Vyber složku se savy.")
	}
	if !filepath.IsAbs(req.Root) {
		return req, errors.New("Cesta ke složce musí být absolutní.")
	}
	req.Root = filepath.Clean(req.Root)
	if st, err := os.Stat(req.Root); err != nil || !st.IsDir() {
		return req, errors.New("Vybraná složka neexistuje.")
	}
	if isTooBroad(req.Root) {
		return req, errors.New("Tahle složka je příliš obecná (kořen disku nebo domovská složka). Vyber složku konkrétní hry.")
	}
	return req, nil
}

// isTooBroad blocks folders that would sync far more than one game.
func isTooBroad(root string) bool {
	if filepath.Dir(root) == root {
		return true // drive or filesystem root
	}
	home, _ := os.UserHomeDir()
	for _, p := range platform.Places() {
		if platform.SamePath(root, p.Path) {
			return true
		}
	}
	return home != "" && platform.SamePath(root, home)
}

func (a *App) checkRootFree(root, exceptID string) error {
	for _, g := range a.store.Config().Games {
		if g.ID == exceptID {
			continue
		}
		if platform.SamePath(g.Root, root) || within(root, g.Root) || within(g.Root, root) {
			return fmt.Errorf("Tahle složka se překrývá se hrou %q.", g.Name)
		}
	}
	return nil
}

func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

func (a *App) requireDrive() error {
	if a.cloud == nil {
		return errors.New("Google Drive není nastavený.")
	}
	if connected, _ := a.auth.Status(); !connected {
		return errors.New("Nejdřív připoj Google Drive.")
	}
	return nil
}

// AddGame creates the cloud folder and starts syncing a new game.
func (a *App) AddGame(req web.GameRequest) (string, error) {
	req, err := cleanRequest(req, true)
	if err != nil {
		return "", err
	}
	if err := a.checkRootFree(req.Root, ""); err != nil {
		return "", err
	}
	if err := a.requireDrive(); err != nil {
		return "", err
	}
	a.mu.Lock()
	for _, cg := range a.cloudGames {
		if strings.EqualFold(cg.Name, req.Name) {
			a.mu.Unlock()
			return "", fmt.Errorf("Hra %q už v cloudu je. Použij u ní tlačítko „Nastavit na tomto zařízení“.", cg.Name)
		}
	}
	rootID := a.rootID
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if rootID == "" {
		if rootID, err = a.cloud.EnsureRoot(ctx); err != nil {
			return "", err
		}
	}
	folder, err := a.cloud.CreateGame(ctx, rootID, req.Name)
	if err != nil {
		return "", err
	}
	if err := a.store.PutGame(store.Game{ID: folder.ID, Name: req.Name, Root: req.Root, Include: req.Include}); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.rootID = rootID
	a.cloudGames = append(a.cloudGames, folder)
	a.mu.Unlock()
	a.updateWatcher()
	a.enqueue(job{gameID: folder.ID})
	return fmt.Sprintf("%s přidána, nahrávám savy…", req.Name), nil
}

// SetupGame links a cloud game to a folder on this device.
func (a *App) SetupGame(id string, req web.GameRequest) (string, error) {
	req, err := cleanRequest(req, false)
	if err != nil {
		return "", err
	}
	if err := a.checkRootFree(req.Root, id); err != nil {
		return "", err
	}
	a.mu.Lock()
	name := ""
	for _, cg := range a.cloudGames {
		if cg.ID == id {
			name = cg.Name
		}
	}
	a.mu.Unlock()
	if name == "" {
		return "", errors.New("Hra v cloudu nebyla nalezena.")
	}
	if err := a.store.PutGame(store.Game{ID: id, Name: name, Root: req.Root, Include: req.Include}); err != nil {
		return "", err
	}
	a.updateWatcher()
	a.enqueue(job{gameID: id})
	return fmt.Sprintf("%s nastavena. Pokud ve složce už jsou jiné savy, zeptám se, kterou verzi ponechat.", name), nil
}

// EditGame renames a game and/or changes its folder or filter.
func (a *App) EditGame(id string, req web.GameRequest) (string, error) {
	game, ok := a.store.Game(id)
	if !ok {
		return "", errors.New("Hra nebyla nalezena.")
	}
	req, err := cleanRequest(req, true)
	if err != nil {
		return "", err
	}
	if err := a.checkRootFree(req.Root, id); err != nil {
		return "", err
	}
	if req.Name != game.Name {
		if err := a.requireDrive(); err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.cloud.RenameGame(ctx, id, req.Name); err != nil {
			return "", err
		}
		a.mu.Lock()
		for i := range a.cloudGames {
			if a.cloudGames[i].ID == id {
				a.cloudGames[i].Name = req.Name
			}
		}
		a.mu.Unlock()
	}
	// A different folder or filter means the old baseline no longer describes
	// this folder; start fresh so differences become a question, not a deletion.
	if !platform.SamePath(req.Root, game.Root) || strings.Join(req.Include, "\n") != strings.Join(game.Include, "\n") {
		if err := a.store.ResetBaseline(id); err != nil {
			return "", err
		}
	}
	game.Name, game.Root, game.Include = req.Name, req.Root, req.Include
	if err := a.store.PutGame(game); err != nil {
		return "", err
	}
	a.updateWatcher()
	a.enqueue(job{gameID: id})
	return "Uloženo.", nil
}

// RemoveGame stops syncing on this device; with deleteCloud it also moves
// the game's cloud folder to the Drive trash. Local files are never deleted.
func (a *App) RemoveGame(id string, deleteCloud bool) (string, error) {
	name := id
	if g, ok := a.store.Game(id); ok {
		name = g.Name
	}
	if deleteCloud {
		if err := a.requireDrive(); err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.cloud.Trash(ctx, id); err != nil {
			return "", err
		}
		a.mu.Lock()
		next := a.cloudGames[:0]
		for _, cg := range a.cloudGames {
			if cg.ID != id {
				next = append(next, cg)
			} else {
				name = cg.Name
			}
		}
		a.cloudGames = next
		a.mu.Unlock()
	}
	if err := a.store.RemoveGame(id); err != nil {
		return "", err
	}
	a.mu.Lock()
	delete(a.games, id)
	a.mu.Unlock()
	a.updateWatcher()
	if deleteCloud {
		return fmt.Sprintf("%s smazána z cloudu (v koši Google Drive zůstane 30 dní). Lokální soubory zůstaly.", name), nil
	}
	return fmt.Sprintf("%s se na tomto zařízení už nesynchronizuje. Soubory zůstaly na místě.", name), nil
}

func (a *App) Backups(id string) ([]web.Backup, error) {
	list, err := engine.ListBackups(a.syncer.BackupDir, id)
	if err != nil {
		return nil, err
	}
	out := make([]web.Backup, 0, len(list))
	for _, b := range list {
		out = append(out, web.Backup{Name: b.Name, Created: b.Created.Format(time.RFC3339), Files: b.Files, Bytes: b.Bytes})
	}
	return out, nil
}

// RestoreBackup puts a backup back into the game folder (backing up the
// current files first) and syncs the restored state to the cloud.
func (a *App) RestoreBackup(id, name string) (string, error) {
	game, ok := a.store.Game(id)
	if !ok {
		return "", errors.New("Hra nebyla nalezena.")
	}
	a.mu.Lock()
	w := a.watcher
	a.mu.Unlock()
	if w != nil {
		w.Pause(id)
	}
	err := a.syncer.RestoreBackup(engine.GameRef{ID: id, Root: game.Root, Include: game.Include}, name)
	if w != nil {
		w.Resume(id)
	}
	if err != nil {
		return "", err
	}
	a.clearGuard(id)
	a.enqueue(job{gameID: id})
	return "Záloha obnovena, synchronizuji…", nil
}

func (a *App) SaveSettings(s web.Settings) (string, error) {
	device := strings.TrimSpace(s.Device)
	if device == "" {
		return "", errors.New("Zadej název zařízení.")
	}
	if len([]rune(device)) > 40 {
		return "", errors.New("Název zařízení je moc dlouhý.")
	}
	if s.Versions < 1 || s.Versions > 5 {
		return "", errors.New("Počet verzí musí být 1 až 5.")
	}
	if err := a.store.SetDevice(device); err != nil {
		return "", err
	}
	if err := a.store.SetVersions(s.Versions); err != nil {
		return "", err
	}
	a.notifyUI()
	return "Nastavení uloženo.", nil
}
