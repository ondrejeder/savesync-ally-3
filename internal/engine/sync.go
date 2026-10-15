package engine

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Cloud is the part of the Drive client the engine needs.
type Cloud interface {
	ListFiles(ctx context.Context, gameID string) (map[string]CloudFile, error)
	Upload(ctx context.Context, gameID, existingID, rel string, body io.Reader, size, mtime int64, device string) (CloudFile, error)
	Download(ctx context.Context, id string, w io.Writer) error
	Trash(ctx context.Context, id string) error
}

// GameRef is what the engine needs to know about a game.
type GameRef struct {
	ID      string
	Root    string
	Include []string
}

// GameState is what a device remembers about a game between syncs. It is
// stored locally and never shared with other devices.
type GameState struct {
	Baseline Baseline `json:"baseline"`
	// Hashes caches the last scan so unchanged files are not re-hashed.
	Hashes    map[string]LocalFile `json:"hashes,omitempty"`
	ScannedAt int64                `json:"scannedAt,omitempty"` // unix nanoseconds
	// Downloads remembers when each file was last written from the cloud,
	// for the "game still running with an old save" guard.
	Downloads map[string]int64 `json:"downloads,omitempty"`
}

func (st GameState) clone() GameState {
	out := GameState{Baseline: Baseline{}, Hashes: st.Hashes, ScannedAt: st.ScannedAt, Downloads: map[string]int64{}}
	for k, v := range st.Baseline {
		out.Baseline[k] = v
	}
	for k, v := range st.Downloads {
		out.Downloads[k] = v
	}
	return out
}

// Result describes what one sync run did.
type Result struct {
	Conflicts []string
	// Guarded means the conflict was raised by the download guard: local
	// files changed shortly after a newer cloud version was downloaded.
	Guarded        bool
	Uploaded       int
	Downloaded     int
	Deleted        int
	DownloadedFrom string // device that produced the downloaded files
	Files          int    // files in sync after the run
	Bytes          int64  // their total size
	Backup         string
	Local          map[string]LocalFile
	Cloud          map[string]CloudFile
}

// Changed reports whether any file was transferred or deleted.
func (r Result) Changed() bool { return r.Uploaded+r.Downloaded+r.Deleted > 0 }

// Syncer executes plans. Progress, if set, is called before each transfer.
type Syncer struct {
	Cloud       Cloud
	Device      string
	BackupDir   string
	KeepBackups int
	// GuardWindow: a local change within this time after a download is not
	// uploaded automatically but reported as a conflict. Zero disables it.
	GuardWindow time.Duration
	Progress    func(done, total int, up bool)
	Now         func() time.Time
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Sync brings one game in sync. With keep set, it resolves a conflict by
// making the other side an exact copy. It returns the new state, which the
// caller must persist; on partial failure the baseline only contains the
// files that really are in sync, so the next run retries the rest.
func (s *Syncer) Sync(ctx context.Context, game GameRef, st GameState, keep Side) (GameState, Result, error) {
	var res Result
	now := s.now()
	local, err := ScanCached(game.Root, game.Include, st.Hashes, st.ScannedAt)
	if err != nil {
		return st, res, err
	}
	next := st.clone()
	next.Hashes, next.ScannedAt = local, now.UnixNano()

	cloud, err := s.Cloud.ListFiles(ctx, game.ID)
	if err != nil {
		return next, res, err
	}
	for rel := range cloud {
		if !safeRel(rel) {
			delete(cloud, rel) // never let a cloud name escape the game folder
		}
	}
	res.Local, res.Cloud = local, cloud

	var plan Plan
	if keep != "" {
		plan = ResolvePlan(local, cloud, keep)
		next.Downloads = map[string]int64{} // the user decided; no guard left to apply
	} else {
		plan = MakePlan(st.Baseline, local, cloud)
	}
	if len(plan.Conflicts) > 0 {
		res.Conflicts = plan.Conflicts
		return next, res, nil
	}

	// Download guard: a game that was running while a newer save arrived may
	// write its old in-memory state back. Ask instead of uploading it.
	if keep == "" && s.GuardWindow > 0 {
		var guarded []string
		for _, a := range plan.Actions {
			if t, ok := st.Downloads[a.Path]; ok && a.Kind == Upload && now.Sub(time.Unix(t, 0)) < s.GuardWindow {
				guarded = append(guarded, a.Path)
			}
		}
		if len(guarded) > 0 {
			res.Conflicts, res.Guarded = guarded, true
			return next, res, nil
		}
	}

	// Back up every local file that is about to be overwritten or deleted.
	var risky []string
	for _, a := range plan.Actions {
		if _, exists := local[a.Path]; exists && (a.Kind == Download || a.Kind == DeleteLocal) {
			risky = append(risky, a.Path)
		}
	}
	if len(risky) > 0 {
		backup, err := s.backup(game, risky)
		if err != nil {
			return next, res, fmt.Errorf("záloha před přepsáním selhala: %w", err)
		}
		res.Backup = backup
	}

	hashes := map[string]LocalFile{}
	for k, v := range local {
		hashes[k] = v
	}
	total, done := 0, 0
	for _, a := range plan.Actions {
		if a.Kind == Upload || a.Kind == Download {
			total++
		}
	}

	var errs []error
	for _, a := range plan.Actions {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		var err error
		switch a.Kind {
		case Upload:
			done++
			s.progress(done, total, true)
			var md5sum string
			md5sum, err = s.upload(ctx, game, a, local[a.Path])
			if err == nil {
				next.Baseline[a.Path] = md5sum
				delete(next.Downloads, a.Path)
				res.Uploaded++
			}
		case Download:
			done++
			s.progress(done, total, false)
			c := cloud[a.Path]
			err = s.download(ctx, game, c, a.Path)
			if err == nil {
				next.Baseline[a.Path] = c.MD5
				next.Downloads[a.Path] = now.Unix()
				if lf, ok := statLocal(game, a.Path, c.MD5); ok {
					hashes[a.Path] = lf
				}
				if c.Device != "" {
					res.DownloadedFrom = c.Device
				}
				res.Downloaded++
			}
		case DeleteCloud:
			err = s.Cloud.Trash(ctx, a.CloudID)
			if err == nil {
				delete(next.Baseline, a.Path)
				res.Deleted++
			}
		case DeleteLocal:
			err = os.Remove(filepath.Join(game.Root, filepath.FromSlash(a.Path)))
			if err == nil || errors.Is(err, os.ErrNotExist) {
				err = nil
				delete(next.Baseline, a.Path)
				delete(hashes, a.Path)
				res.Deleted++
			}
		case Accept:
			next.Baseline[a.Path] = local[a.Path].MD5
		case Forget:
			delete(next.Baseline, a.Path)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", a.Kind, a.Path, err))
		}
	}
	next.Hashes = hashes

	// Forget download times once the guard window is over.
	for p, t := range next.Downloads {
		if now.Sub(time.Unix(t, 0)) >= s.GuardWindow {
			delete(next.Downloads, p)
		}
	}

	for rel, md5sum := range next.Baseline {
		if l, ok := local[rel]; ok && l.MD5 == md5sum {
			res.Files++
			res.Bytes += l.Size
		} else if c, ok := cloud[rel]; ok && c.MD5 == md5sum {
			res.Files++
			res.Bytes += c.Size
		}
	}
	return next, res, errors.Join(errs...)
}

// statLocal builds a hash-cache entry for a file whose content is known.
func statLocal(game GameRef, rel, md5sum string) (LocalFile, bool) {
	info, err := os.Stat(filepath.Join(game.Root, filepath.FromSlash(rel)))
	if err != nil {
		return LocalFile{}, false
	}
	return LocalFile{MD5: md5sum, Size: info.Size(), MTime: info.ModTime().Unix(), ModNano: info.ModTime().UnixNano()}, true
}

func (s *Syncer) progress(done, total int, up bool) {
	if s.Progress != nil {
		s.Progress(done, total, up)
	}
}

func (s *Syncer) upload(ctx context.Context, game GameRef, a Action, l LocalFile) (string, error) {
	p := filepath.Join(game.Root, filepath.FromSlash(a.Path))
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	uploaded, err := s.Cloud.Upload(ctx, game.ID, a.CloudID, a.Path, f, st.Size(), st.ModTime().Unix(), s.Device)
	if err != nil {
		return "", err
	}
	// If the game wrote the file while it was uploading, the cloud copy may
	// not match what was scanned; leave the baseline alone so it is retried.
	if uploaded.MD5 != "" && uploaded.MD5 != l.MD5 {
		return "", errors.New("soubor se během nahrávání změnil, zkusím to znovu")
	}
	return l.MD5, nil
}

// download writes to a temp file, verifies the checksum and only then
// replaces the real file, so a broken transfer never corrupts a save.
func (s *Syncer) download(ctx context.Context, game GameRef, c CloudFile, rel string) error {
	dst := filepath.Join(game.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + TempSuffix
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := md5.New()
	err = s.Cloud.Download(ctx, c.ID, io.MultiWriter(f, h))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && c.MD5 != "" && hex.EncodeToString(h.Sum(nil)) != c.MD5 {
		err = errors.New("stažený soubor nesouhlasí s kontrolním součtem")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if c.MTime > 0 {
		t := time.Unix(c.MTime, 0)
		_ = os.Chtimes(tmp, t, t)
	}
	return os.Rename(tmp, dst)
}

// backup zips the given files into BackupDir/<gameID>/<timestamp>.zip and
// keeps only the newest KeepBackups archives.
func (s *Syncer) backup(game GameRef, rels []string) (string, error) {
	dir := filepath.Join(s.BackupDir, game.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stamp := time.Now().Format(backupStamp)
	name := filepath.Join(dir, stamp+".zip")
	out, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	for i := 2; errors.Is(err, os.ErrExist) && i < 100; i++ {
		name = filepath.Join(dir, fmt.Sprintf("%s-%d.zip", stamp, i))
		out, err = os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(out)
	for _, rel := range rels {
		if err := addToZip(zw, filepath.Join(game.Root, filepath.FromSlash(rel)), rel); err != nil {
			zw.Close()
			out.Close()
			os.Remove(name)
			return "", err
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(name)
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	s.pruneBackups(dir)
	return name, nil
}

func addToZip(zw *zip.Writer, src, rel string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	hdr, err := zip.FileInfoHeader(st)
	if err != nil {
		return err
	}
	hdr.Name = rel
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

func (s *Syncer) pruneBackups(dir string) {
	keep := s.KeepBackups
	if keep <= 0 {
		keep = 10
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var zips []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".zip") {
			zips = append(zips, e.Name())
		}
	}
	sort.Slice(zips, func(i, j int) bool { return backupKey(zips[i]) < backupKey(zips[j]) })
	for len(zips) > keep {
		_ = os.Remove(filepath.Join(dir, zips[0]))
		zips = zips[1:]
	}
}

// safeRel rejects absolute paths and anything that could leave the game folder.
func safeRel(rel string) bool {
	if rel == "" || strings.Contains(rel, `\`) || strings.Contains(rel, ":") || path.IsAbs(rel) {
		return false
	}
	clean := path.Clean(rel)
	return clean == rel && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

const backupStamp = "2006-01-02_15-04-05"

// backupKey orders backups chronologically, including "<stamp>-N.zip"
// names created within the same second.
func backupKey(name string) string {
	base := strings.TrimSuffix(name, ".zip")
	n := 1
	if len(base) > len(backupStamp)+1 {
		fmt.Sscanf(base[len(backupStamp)+1:], "%d", &n)
		base = base[:len(backupStamp)]
	}
	return fmt.Sprintf("%s#%03d", base, n)
}

// BackupInfo describes one local backup archive.
type BackupInfo struct {
	Name    string
	Created time.Time
	Files   int
	Bytes   int64
}

// ListBackups returns a game's backups, newest first.
func ListBackups(backupDir, gameID string) ([]BackupInfo, error) {
	dir := filepath.Join(backupDir, gameID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []BackupInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		info := BackupInfo{Name: e.Name()}
		if len(e.Name()) < len(backupStamp) {
			continue
		}
		if t, err := time.ParseInLocation(backupStamp, e.Name()[:len(backupStamp)], time.Local); err == nil {
			info.Created = t
		}
		if zr, err := zip.OpenReader(filepath.Join(dir, e.Name())); err == nil {
			for _, f := range zr.File {
				info.Files++
				info.Bytes += int64(f.UncompressedSize64)
			}
			zr.Close()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return backupKey(out[i].Name) > backupKey(out[j].Name) })
	return out, nil
}

// RestoreBackup extracts a backup into the game folder. Files it would
// overwrite are backed up first, so a restore can itself be undone.
func (s *Syncer) RestoreBackup(game GameRef, name string) error {
	if name != filepath.Base(name) || !strings.HasSuffix(name, ".zip") {
		return errors.New("neplatný název zálohy")
	}
	return s.RestoreZip(game, filepath.Join(s.BackupDir, game.ID, name), false)
}

// RestoreZip extracts an archive into the game folder after backing up the
// files it touches. With exact, local files that are not in the archive are
// removed too (after the backup), so the folder matches the archive.
func (s *Syncer) RestoreZip(game GameRef, zipPath string, exact bool) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	inZip := map[string]bool{}
	for _, f := range zr.File {
		if !safeRel(f.Name) {
			return fmt.Errorf("archiv obsahuje neplatnou cestu %q", f.Name)
		}
		inZip[f.Name] = true
	}
	local, err := Scan(game.Root, game.Include)
	if err != nil && !errors.Is(err, ErrRootMissing) {
		return err
	}
	var touched, extra []string
	for rel := range local {
		switch {
		case inZip[rel]:
			touched = append(touched, rel)
		case exact:
			touched = append(touched, rel)
			extra = append(extra, rel)
		}
	}
	if len(touched) > 0 {
		if _, err := s.backup(game, touched); err != nil {
			return err
		}
	}
	for _, f := range zr.File {
		if err := extract(f, filepath.Join(game.Root, filepath.FromSlash(f.Name))); err != nil {
			return err
		}
	}
	for _, rel := range extra {
		if err := os.Remove(filepath.Join(game.Root, filepath.FromSlash(rel))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// WriteSnapshot zips the given local files of a game into w.
func WriteSnapshot(w io.Writer, root string, rels []string) error {
	zw := zip.NewWriter(w)
	sort.Strings(rels)
	for _, rel := range rels {
		if err := addToZip(zw, filepath.Join(root, filepath.FromSlash(rel)), rel); err != nil {
			zw.Close()
			return err
		}
	}
	return zw.Close()
}

func extract(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + TempSuffix
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = os.Chtimes(tmp, f.Modified, f.Modified)
	return os.Rename(tmp, dst)
}
