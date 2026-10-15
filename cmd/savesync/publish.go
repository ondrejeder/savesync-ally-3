package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ondrejeder/savesync-ally-3/internal/drive"
	"github.com/ondrejeder/savesync-ally-3/internal/platform"
	"github.com/ondrejeder/savesync-ally-3/internal/update"
)

// cmdPublish uploads the binaries in a dist folder and the release manifest
// to the app folder in Drive, where every device picks them up.
func cmdPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	notes := fs.String("notes", "", "release notes shown in the app")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("použití: savesync publish [--notes text] <složka s buildy>")
	}
	if !update.IsRelease(version) {
		return fmt.Errorf("publikovat jde jen release build s verzí X.Y.Z (tahle binárka je %q, použij scripts\\release.ps1)", version)
	}
	dist := fs.Arg(0)

	entries, err := os.ReadDir(dist)
	if err != nil {
		return err
	}
	var binaries []string
	for _, e := range entries {
		if !e.IsDir() && update.PlatformFromName(e.Name()) != "" {
			binaries = append(binaries, e.Name())
		}
	}
	if len(binaries) == 0 {
		return fmt.Errorf("ve složce %s nejsou žádné buildy (savesync-<os>-<arch>)", dist)
	}
	sort.Strings(binaries)

	configDir, err := platform.ConfigDir()
	if err != nil {
		return err
	}
	auth, err := drive.NewAuth(configDir)
	if err != nil {
		return err
	}
	if connected, _ := auth.Status(); !connected {
		return errors.New("Google Drive není připojený: přihlas se nejdřív v aplikaci")
	}
	client := &drive.Client{Auth: auth}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	root, err := client.EnsureRoot(ctx)
	if err != nil {
		return err
	}
	folder, err := client.AppFolder(ctx, root, true)
	if err != nil {
		return err
	}

	manifest := update.Manifest{Version: version, Notes: strings.TrimSpace(*notes), Published: time.Now().UTC(), Files: map[string]update.Asset{}}
	for _, name := range binaries {
		path := filepath.Join(dist, name)
		sum, err := update.FileSHA256(path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		st, _ := f.Stat()
		fmt.Printf("Nahrávám %s (%.1f MB)…\n", name, float64(st.Size())/1e6)
		id, err := client.PutFile(ctx, folder, name, f, st.Size())
		f.Close()
		if err != nil {
			return fmt.Errorf("upload %s: %w", name, err)
		}
		manifest.Files[update.PlatformFromName(name)] = update.Asset{ID: id, Name: name, Size: st.Size(), SHA256: sum}
	}

	// The manifest goes last, so clients never see it before the binaries.
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if _, err := client.PutFile(ctx, folder, update.ManifestName, strings.NewReader(string(data)), int64(len(data))); err != nil {
		return fmt.Errorf("upload manifestu: %w", err)
	}
	fmt.Printf("Verze %s publikována pro: %s\n", version, strings.Join(keys(manifest.Files), ", "))
	return nil
}

func keys(m map[string]update.Asset) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
