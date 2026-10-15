package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeCloud is an in-memory Drive shared by several simulated devices.
type fakeCloud struct {
	files   map[string]map[string]*fakeFile // gameID -> rel -> file
	nextID  int
	corrupt bool // serve wrong content on download
}

type fakeFile struct {
	id, device string
	data       []byte
	mtime      int64
}

func newFakeCloud() *fakeCloud { return &fakeCloud{files: map[string]map[string]*fakeFile{}} }

func sum(b []byte) string { h := md5.Sum(b); return hex.EncodeToString(h[:]) }

func (f *fakeCloud) ListFiles(_ context.Context, gameID string) (map[string]CloudFile, error) {
	out := map[string]CloudFile{}
	for rel, ff := range f.files[gameID] {
		out[rel] = CloudFile{ID: ff.id, MD5: sum(ff.data), Size: int64(len(ff.data)), MTime: ff.mtime, Device: ff.device}
	}
	return out, nil
}

func (f *fakeCloud) Upload(_ context.Context, gameID, existingID, rel string, body io.Reader, _, mtime int64, device string) (CloudFile, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return CloudFile{}, err
	}
	if f.files[gameID] == nil {
		f.files[gameID] = map[string]*fakeFile{}
	}
	ff := f.files[gameID][rel]
	if existingID == "" || ff == nil {
		f.nextID++
		ff = &fakeFile{id: fmt.Sprintf("f%d", f.nextID)}
		f.files[gameID][rel] = ff
	}
	ff.data, ff.mtime, ff.device = data, mtime, device
	return CloudFile{ID: ff.id, MD5: sum(data), Size: int64(len(data)), MTime: mtime, Device: device}, nil
}

func (f *fakeCloud) Download(_ context.Context, id string, w io.Writer) error {
	for _, game := range f.files {
		for _, ff := range game {
			if ff.id == id {
				data := ff.data
				if f.corrupt {
					data = []byte("garbage")
				}
				_, err := w.Write(data)
				return err
			}
		}
	}
	return errors.New("not found")
}

func (f *fakeCloud) Trash(_ context.Context, id string) error {
	for _, game := range f.files {
		for rel, ff := range game {
			if ff.id == id {
				delete(game, rel)
				return nil
			}
		}
	}
	return errors.New("not found")
}

type device struct {
	t     *testing.T
	root  string
	state GameState
	s     *Syncer
}

func newDevice(t *testing.T, name string, cloud Cloud) *device {
	return &device{t: t, root: t.TempDir(), s: &Syncer{Cloud: cloud, Device: name, BackupDir: t.TempDir()}}
}

func (d *device) write(rel, content string) {
	p := filepath.Join(d.root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		d.t.Fatal(err)
	}
}

func (d *device) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(d.root, filepath.FromSlash(rel)))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func (d *device) sync(keep Side) Result {
	d.t.Helper()
	next, res, err := d.s.Sync(context.Background(), GameRef{ID: "g", Root: d.root}, d.state, keep)
	if err != nil {
		d.t.Fatalf("sync: %v", err)
	}
	d.state = next
	return res
}

func TestSyncBetweenTwoDevices(t *testing.T) {
	cloud := newFakeCloud()
	pc, ally := newDevice(t, "PC", cloud), newDevice(t, "Ally", cloud)

	pc.write("save1.sav", "pc-v1")
	pc.write("sub/opts.ini", "opts")
	if r := pc.sync(""); r.Uploaded != 2 {
		t.Fatalf("pc first sync = %+v", r)
	}

	// Ally sets up the game in an empty folder: everything downloads.
	if r := ally.sync(""); r.Downloaded != 2 || ally.read("sub/opts.ini") != "opts" {
		t.Fatalf("ally setup = %+v", r)
	}

	// Ally plays, PC picks it up.
	ally.write("save1.sav", "ally-v2")
	if r := ally.sync(""); r.Uploaded != 1 {
		t.Fatalf("ally upload = %+v", r)
	}
	if r := pc.sync(""); r.Downloaded != 1 || pc.read("save1.sav") != "ally-v2" || r.Backup == "" {
		t.Fatalf("pc download = %+v, content %q", r, pc.read("save1.sav"))
	}

	// Nothing changed: nothing happens.
	if r := pc.sync(""); r.Changed() {
		t.Fatalf("idle sync changed something: %+v", r)
	}
}

func TestOfflineEditsConflictAndResolve(t *testing.T) {
	cloud := newFakeCloud()
	pc, ally := newDevice(t, "PC", cloud), newDevice(t, "Ally", cloud)
	pc.write("save.sav", "v1")
	pc.sync("")
	ally.sync("")

	pc.write("save.sav", "pc-progress")
	ally.write("save.sav", "ally-progress")
	pc.sync("")
	r := ally.sync("")
	if len(r.Conflicts) != 1 || ally.read("save.sav") != "ally-progress" {
		t.Fatalf("expected untouched conflict, got %+v / %q", r, ally.read("save.sav"))
	}

	// User keeps the cloud (PC) version on the Ally; old local file is backed up.
	r = ally.sync(KeepCloud)
	if ally.read("save.sav") != "pc-progress" || r.Backup == "" {
		t.Fatalf("resolve = %+v / %q", r, ally.read("save.sav"))
	}
	if got := zipContent(t, r.Backup, "save.sav"); got != "ally-progress" {
		t.Fatalf("backup content = %q", got)
	}
	if r := ally.sync(""); r.Changed() || len(r.Conflicts) > 0 {
		t.Fatalf("after resolve = %+v", r)
	}
}

func TestCorruptDownloadKeepsLocalFile(t *testing.T) {
	cloud := newFakeCloud()
	pc, ally := newDevice(t, "PC", cloud), newDevice(t, "Ally", cloud)
	pc.write("save.sav", "v1")
	pc.sync("")
	ally.sync("")
	pc.write("save.sav", "v2")
	pc.sync("")

	cloud.corrupt = true
	_, _, err := ally.s.Sync(context.Background(), GameRef{ID: "g", Root: ally.root}, ally.state, "")
	if err == nil {
		t.Fatal("expected checksum error")
	}
	if ally.read("save.sav") != "v1" {
		t.Fatalf("local file damaged: %q", ally.read("save.sav"))
	}
	if _, err := os.Stat(filepath.Join(ally.root, "save.sav"+TempSuffix)); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}

func TestSafeRel(t *testing.T) {
	for rel, want := range map[string]bool{
		"a.sav": true, "dir/a.sav": true,
		"../evil": false, "a/../../evil": false, "/etc/passwd": false, `C:\x`: false, "a//b": false, "": false,
	} {
		if got := safeRel(rel); got != want {
			t.Errorf("safeRel(%q) = %v", rel, got)
		}
	}
}

func zipContent(t *testing.T, zipPath, name string) string {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == name {
			rc, _ := f.Open()
			var buf bytes.Buffer
			io.Copy(&buf, rc)
			rc.Close()
			return buf.String()
		}
	}
	return "<not in zip>"
}

func TestRestoreBackup(t *testing.T) {
	cloud := newFakeCloud()
	pc, ally := newDevice(t, "PC", cloud), newDevice(t, "Ally", cloud)
	pc.write("save.sav", "good")
	pc.sync("")
	ally.sync("")
	ally.write("save.sav", "broken")
	ally.sync("")
	r := pc.sync("") // pc downloads "broken", backing up "good"
	if r.Backup == "" || pc.read("save.sav") != "broken" {
		t.Fatalf("setup: %+v", r)
	}

	backups, err := ListBackups(pc.s.BackupDir, "g")
	if err != nil || len(backups) != 1 || backups[0].Files != 1 {
		t.Fatalf("backups = %+v, %v", backups, err)
	}
	if err := pc.s.RestoreBackup(GameRef{ID: "g", Root: pc.root}, backups[0].Name); err != nil {
		t.Fatal(err)
	}
	if pc.read("save.sav") != "good" {
		t.Fatalf("restored content = %q", pc.read("save.sav"))
	}
	// The restore itself made a safety backup of "broken".
	if backups, _ := ListBackups(pc.s.BackupDir, "g"); len(backups) != 2 {
		t.Fatalf("want safety backup, got %+v", backups)
	}
	// Restored file uploads on the next sync.
	if r := pc.sync(""); r.Uploaded != 1 {
		t.Fatalf("after restore = %+v", r)
	}
	if err := pc.s.RestoreBackup(GameRef{ID: "g", Root: pc.root}, "../x.zip"); err == nil {
		t.Fatal("expected invalid name error")
	}
}

// A game left running on the handheld writes its old in-memory state right
// after a newer save was downloaded: that must become a question.
func TestDownloadGuard(t *testing.T) {
	cloud := newFakeCloud()
	pc, ally := newDevice(t, "PC", cloud), newDevice(t, "Ally", cloud)
	now := time.Unix(1_800_000_000, 0)
	ally.s.Now = func() time.Time { return now }
	ally.s.GuardWindow = 10 * time.Minute

	pc.write("save.sav", "v1")
	pc.sync("")
	ally.sync("")
	pc.write("save.sav", "pc-v2")
	pc.sync("")

	now = now.Add(time.Hour)
	if r := ally.sync(""); r.Downloaded != 1 || r.DownloadedFrom != "PC" {
		t.Fatalf("download = %+v", r)
	}
	// Running game autosaves its stale state 3 minutes later.
	now = now.Add(3 * time.Minute)
	ally.write("save.sav", "stale ally state")
	r := ally.sync("")
	if !r.Guarded || len(r.Conflicts) != 1 || r.Uploaded != 0 {
		t.Fatalf("guard did not trigger: %+v", r)
	}
	if got := cloud.files["g"]["save.sav"].data; string(got) != "pc-v2" {
		t.Fatalf("cloud overwritten: %q", got)
	}

	// After the window, a normal local change uploads again.
	ally.sync(KeepCloud)
	now = now.Add(20 * time.Minute)
	ally.write("save.sav", "ally-v3")
	if r := ally.sync(""); r.Uploaded != 1 || r.Guarded {
		t.Fatalf("after window = %+v", r)
	}
}

func TestHashCacheSkipsUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.sav")
	os.WriteFile(p, []byte("one"), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(p, old, old)

	first, err := ScanCached(root, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Poison the cached hash: if the cache is used, the poisoned value survives.
	cached := map[string]LocalFile{"a.sav": first["a.sav"]}
	entry := cached["a.sav"]
	entry.MD5 = "cached"
	cached["a.sav"] = entry
	second, _ := ScanCached(root, nil, cached, time.Now().UnixNano())
	if second["a.sav"].MD5 != "cached" {
		t.Fatalf("cache not used: %+v", second["a.sav"])
	}
	// A modified file is re-hashed.
	os.WriteFile(p, []byte("two!"), 0o644)
	third, _ := ScanCached(root, nil, cached, time.Now().UnixNano())
	if third["a.sav"].MD5 == "cached" || third["a.sav"].MD5 == first["a.sav"].MD5 {
		t.Fatalf("changed file not re-hashed: %+v", third["a.sav"])
	}
}

func TestSnapshotRestoreExact(t *testing.T) {
	d := newDevice(t, "PC", newFakeCloud())
	d.write("a.sav", "a1")
	d.write("sub/b.sav", "b1")
	var buf bytes.Buffer
	if err := WriteSnapshot(&buf, d.root, []string{"a.sav", "sub/b.sav"}); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(t.TempDir(), "v.zip")
	os.WriteFile(snap, buf.Bytes(), 0o644)

	d.write("a.sav", "a2-bad")
	d.write("new.sav", "created later")
	if err := d.s.RestoreZip(GameRef{ID: "g", Root: d.root}, snap, true); err != nil {
		t.Fatal(err)
	}
	if d.read("a.sav") != "a1" || d.read("sub/b.sav") != "b1" || d.read("new.sav") != "<missing>" {
		t.Fatalf("after restore: a=%q b=%q new=%q", d.read("a.sav"), d.read("sub/b.sav"), d.read("new.sav"))
	}
	backups, _ := ListBackups(d.s.BackupDir, "g")
	if len(backups) != 1 || backups[0].Files != 3 {
		t.Fatalf("safety backup = %+v", backups)
	}
}
