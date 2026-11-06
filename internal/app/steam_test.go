package app

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// vdf builds a binary VDF shortcuts file like Steam writes it.
type vdf struct{ bytes.Buffer }

func (v *vdf) begin(key string) { v.WriteByte(0x00); v.WriteString(key); v.WriteByte(0) }
func (v *vdf) end()             { v.WriteByte(0x08) }
func (v *vdf) str(key, val string) {
	v.WriteByte(0x01)
	v.WriteString(key)
	v.WriteByte(0)
	v.WriteString(val)
	v.WriteByte(0)
}
func (v *vdf) u32(key string, n uint32) {
	v.WriteByte(0x02)
	v.WriteString(key)
	v.WriteByte(0)
	_ = binary.Write(v, binary.LittleEndian, n)
}

func shortcutsFile() []byte {
	var v vdf
	v.begin("shortcuts")
	v.begin("0")
	v.u32("appid", 3141592653)
	v.str("AppName", "Driver San Francisco")
	v.str("Exe", `"/games/driver/Driver.exe"`)
	v.u32("LastPlayTime", 1)
	v.begin("tags")
	v.str("0", "racing")
	v.end()
	v.end()
	v.begin("1")
	v.str("appname", "Old Shortcut") // no appid: derived from exe + name
	v.str("exe", `"C:\old.exe"`)
	v.end()
	v.end()
	v.end()
	return v.Bytes()
}

func TestParseShortcuts(t *testing.T) {
	got := parseShortcuts(shortcutsFile())
	if got["3141592653"] != "Driver San Francisco" {
		t.Errorf("appid shortcut: %v", got)
	}
	old := strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(`"C:\old.exe"Old Shortcut`))|0x80000000), 10)
	if got[old] != "Old Shortcut" {
		t.Errorf("derived id %s missing: %v", old, got)
	}
	if len(parseShortcuts([]byte{0x00, 's', 'h'})) != 0 {
		t.Error("truncated file must give no names")
	}
}

func TestSteamGameNamesNonSteam(t *testing.T) {
	root := t.TempDir()
	compat := filepath.Join(root, "steamapps", "compatdata")
	cfg := filepath.Join(root, "userdata", "123", "config")
	for _, d := range []string{compat, cfg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(cfg, "shortcuts.vdf"), shortcutsFile(), 0o644)
	_ = os.WriteFile(filepath.Join(root, "steamapps", "appmanifest_1551360.acf"), []byte("\"AppState\"\n{\n\t\"name\"\t\t\"Forza Horizon 5\"\n}"), 0o644)
	names := steamGameNames(compat)
	if names["3141592653"] != "Driver San Francisco" || names["1551360"] != "Forza Horizon 5" {
		t.Errorf("names: %v", names)
	}
}
