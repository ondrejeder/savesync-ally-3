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

// Result describes what one sync run did.
type Result struct {
	Conflicts  []string
	Uploaded   int
	Downloaded int
	Deleted    int
	Files      int   // files in sync after the run
	Bytes      int64 // their total size
	Backup     string
	Local      map[string]LocalFile
	Cloud      map[string]CloudFile
}

// Changed reports whether any file was transferred or deleted.
func (r Result) Changed() bool { return r.Uploaded+r.Downloaded+r.Deleted > 0 }

// Syncer executes plans. Progress, if set, is called before each transfer.
type Syncer struct {
	Cloud       Cloud
	Device      string
	BackupDir   string
	KeepBackups int
	Progress    func(done, total int, up bool)
}

// Sync brings one game in sync. With keep set, it resolves a conflict by
// making the other side an exact copy. It returns the new baseline, which the
// caller must persist; on partial failure the baseline only contains the
// files that really are in sync, so the next run retries the rest.
func (s *Syncer) Sync(ctx context.Context, game GameRef, base Baseline, keep Side) (Baseline, Result, error) {
	var res Result
	local, err := Scan(game.Root, game.Include)
	if err != nil {
		return base, res, err
	}
	cloud, err := s.Cloud.ListFiles(ctx, game.ID)
	if err != nil {
		return base, res, err
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
	} else {
		plan = MakePlan(base, local, cloud)
	}
	if len(plan.Conflicts) > 0 {
		res.Conflicts = plan.Conflicts
		return base, res, nil
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
			return base, res, fmt.Errorf("záloha před přepsáním selhala: %w", err)
		}
		res.Backup = backup
	}

	next := Baseline{}
	for k, v := range base {
		next[k] = v
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
				next[a.Path] = md5sum
				res.Uploaded++
			}
		case Download:
			done++
			s.progress(done, total, false)
			err = s.download(ctx, game, cloud[a.Path], a.Path)
			if err == nil {
				next[a.Path] = cloud[a.Path].MD5
				res.Downloaded++
			}
		case DeleteCloud:
			err = s.Cloud.Trash(ctx, a.CloudID)
			if err == nil {
				delete(next, a.Path)
				res.Deleted++
			}
		case DeleteLocal:
			err = os.Remove(filepath.Join(game.Root, filepath.FromSlash(a.Path)))
			if err == nil || errors.Is(err, os.ErrNotExist) {
				err = nil
				delete(next, a.Path)
				res.Deleted++
			}
		case Accept:
			next[a.Path] = local[a.Path].MD5
		case Forget:
			delete(next, a.Path)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", a.Kind, a.Path, err))
		}
	}

	for rel, md5sum := range next {
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
	zr, err := zip.OpenReader(filepath.Join(s.BackupDir, game.ID, name))
	if err != nil {
		return err
	}
	defer zr.Close()

	var existing []string
	for _, f := range zr.File {
		if !safeRel(f.Name) {
			return fmt.Errorf("záloha obsahuje neplatnou cestu %q", f.Name)
		}
		if _, err := os.Stat(filepath.Join(game.Root, filepath.FromSlash(f.Name))); err == nil {
			existing = append(existing, f.Name)
		}
	}
	if len(existing) > 0 {
		if _, err := s.backup(game, existing); err != nil {
			return err
		}
	}
	for _, f := range zr.File {
		if err := extract(f, filepath.Join(game.Root, filepath.FromSlash(f.Name))); err != nil {
			return err
		}
	}
	return nil
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
