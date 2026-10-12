package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScan(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("slot1.sav", "hello")
	write("profiles/main/data.sav", "x")
	write("profiles/main/log.txt", "ignored by filter")
	write("save.bak", "a game's own backup is still a save")
	write("pending.sav"+TempSuffix, "partial download")

	all, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"slot1.sav", "profiles/main/data.sav", "profiles/main/log.txt", "save.bak"} {
		if _, ok := all[want]; !ok {
			t.Errorf("missing %s in %v", want, all)
		}
	}
	if len(all) != 4 {
		t.Errorf("got %d files, want 4: %v", len(all), all)
	}
	if all["slot1.sav"].MD5 != "5d41402abc4b2a76b9719d911017c592" {
		t.Errorf("md5 = %s", all["slot1.sav"].MD5)
	}

	filtered, err := Scan(root, []string{"*.SAV"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Errorf("filtered = %v, want slot1.sav and data.sav", filtered)
	}

	if _, err := Scan(filepath.Join(root, "nope"), nil); err != ErrRootMissing {
		t.Errorf("missing root err = %v", err)
	}
}

func TestIncluded(t *testing.T) {
	cases := []struct {
		rel     string
		include []string
		want    bool
	}{
		{"a/b/save.dat", nil, true},
		{"a/b/save.dat", []string{"*.dat"}, true},
		{"a/b/save.dat", []string{"a/*/save.dat"}, true},
		{"a/b/save.dat", []string{"b/*.dat"}, false},
		{"a/b/log.txt", []string{"*.dat", "*.sav"}, false},
	}
	for _, tc := range cases {
		if got := Included(tc.rel, tc.include); got != tc.want {
			t.Errorf("Included(%q, %v) = %v", tc.rel, tc.include, got)
		}
	}
}
