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
	t    *testing.T
	root string
	base Baseline
	s    *Syncer
}

func newDevice(t *testing.T, name string, cloud Cloud) *device {
	return &device{t: t, root: t.TempDir(), base: Baseline{}, s: &Syncer{Cloud: cloud, Device: name, BackupDir: t.TempDir()}}
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
	next, res, err := d.s.Sync(context.Background(), GameRef{ID: "g", Root: d.root}, d.base, keep)
	if err != nil {
		d.t.Fatalf("sync: %v", err)
	}
	d.base = next
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
	_, _, err := ally.s.Sync(context.Background(), GameRef{ID: "g", Root: ally.root}, ally.base, "")
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
