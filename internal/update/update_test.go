package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"3.0.1", "3.0.0", true},
		{"3.1.0", "3.0.9", true},
		{"v3.0.0", "3.0.0", false},
		{"3.0.0", "3.0.1", false},
		{"3.0.0", "2e9d441-dirty", true},
		{"3.0.0-2-gabc", "3.0.0", false},
		{"10.0.0", "9.9.9", true},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestAssetNames(t *testing.T) {
	for _, p := range []string{"windows-amd64", "linux-amd64", "linux-arm64"} {
		if got := PlatformFromName(AssetName(p)); got != p {
			t.Errorf("round trip %s -> %s", p, got)
		}
	}
	if PlatformFromName("README.md") != "" || PlatformFromName("savesync") != "" {
		t.Error("non-binaries must not map to a platform")
	}
}

func TestReplaceAndVerify(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "savesync")
	os.WriteFile(exe, []byte("old binary"), 0o755)
	newBin := filepath.Join(dir, "download")
	os.WriteFile(newBin, []byte("new binary"), 0o644)

	sum, _ := FileSHA256(newBin)
	if err := Verify(newBin, Asset{Size: 10, SHA256: sum}); err != nil {
		t.Fatal(err)
	}
	if err := Verify(newBin, Asset{Size: 10, SHA256: "00"}); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if err := Replace(exe, newBin); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatalf("exe = %q", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old binary" {
		t.Fatalf("old = %q", b)
	}
	CleanupOld(exe)
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old not removed")
	}
}
