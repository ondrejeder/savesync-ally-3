package app

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPath(t *testing.T) {
	base := t.TempDir()
	profile := filepath.Join(base, "Game", "Profile")
	touch(t, filepath.Join(profile, "Saves", "slot1.sav"))
	touch(t, filepath.Join(profile, "Saves", "auto", "a.sav"))
	touch(t, filepath.Join(profile, "settings.ini"))
	rels := []string{"auto/a.sav", "slot1.sav"} // cloud root = Profile/Saves

	cases := []struct {
		root, status, suggest string
	}{
		{filepath.Join(profile, "Saves"), "ok", ""},
		{profile, "deeper", filepath.Join(profile, "Saves")},
		{filepath.Join(base, "Game"), "deeper", filepath.Join(profile, "Saves")},
		{filepath.Join(profile, "Saves", "auto"), "higher", filepath.Join(profile, "Saves")},
		{filepath.Join(base, "missing"), "empty", ""},
	}
	for _, c := range cases {
		got := checkPath(c.root, rels)
		if got.Status != c.status || got.Suggest != c.suggest {
			t.Errorf("checkPath(%s) = %+v, want %s %s", c.root, got, c.status, c.suggest)
		}
	}

	other := filepath.Join(base, "Other")
	touch(t, filepath.Join(other, "unrelated.txt"))
	if got := checkPath(other, rels); got.Status != "other" {
		t.Errorf("unrelated folder: %+v", got)
	}
	if got := checkPath(filepath.Join(profile, "Saves"), rels); got.Matched != 2 {
		t.Errorf("matched = %d, want 2", got.Matched)
	}
}

func TestDisplayRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	p := filepath.Join(home, "Documents", "Game")
	short := displayRoot(p)
	if want := "~" + string(filepath.Separator) + filepath.Join("Documents", "Game"); short != want {
		t.Fatalf("displayRoot = %q, want %q", short, want)
	}
	if back := expandRoot(short); back != p {
		t.Fatalf("expandRoot = %q, want %q", back, p)
	}
	if displayRoot(home) != "~" {
		t.Fatal("home should be ~")
	}
	outside := filepath.Join(filepath.Dir(home), "elsewhere")
	if displayRoot(outside) != outside {
		t.Fatal("paths outside home must stay unchanged")
	}
}

func TestSample(t *testing.T) {
	got := sample([]string{"a.sav", "x/y/z.sav", "b/c.sav"}, 2)
	if len(got) != 2 || got[0] != "x/y/z.sav" || got[1] != "b/c.sav" {
		t.Fatalf("sample = %v", got)
	}
}
